package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mailwisp/internal/auth"
	"mailwisp/internal/cloudflaretemp"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

const (
	cloudflareTempMaxPageItems    = 20
	cloudflareTempMaxPayloadBytes = 32 << 20
)

var errCloudflareTempPayloadTooLarge = errors.New("Cloudflare Temp Email response payload is too large")

type cloudflareTempRawMail struct {
	ID        int64  `json:"id"`
	MessageID string `json:"message_id"`
	Source    string `json:"source"`
	Address   string `json:"address"`
	Raw       string `json:"raw"`
	Metadata  string `json:"metadata"`
	CreatedAt string `json:"created_at"`
}

type cloudflareTempAttachment struct {
	Filename    string `json:"filename"`
	MIMEType    string `json:"mimeType"`
	Disposition string `json:"disposition"`
	Size        int64  `json:"size"`
}

type cloudflareTempParsedMail struct {
	ID          int64                      `json:"id"`
	MessageID   string                     `json:"message_id"`
	Source      string                     `json:"source"`
	Address     string                     `json:"address"`
	Metadata    string                     `json:"metadata"`
	CreatedAt   string                     `json:"created_at"`
	Sender      string                     `json:"sender"`
	Subject     string                     `json:"subject"`
	Text        string                     `json:"text"`
	HTML        string                     `json:"html"`
	Attachments []cloudflareTempAttachment `json:"attachments"`
}

func (s *Server) handleCloudflareTempOpenSettings(w http.ResponseWriter, r *http.Request) {
	if !s.cloudflareTempEnabled(w, r) {
		return
	}
	domains := s.cloudflareTemp.Domains()
	writeJSON(w, http.StatusOK, map[string]any{
		"title": "MailWisp", "announcement": "", "alwaysShowAnnouncement": false,
		"prefix": "", "addressRegex": "", "minAddressLen": 1, "maxAddressLen": 64,
		"defaultDomains": domains, "domains": domains, "randomSubdomainDomains": []string{}, "domainLabels": []string{},
		"needAuth": false, "adminContact": "", "enableUserCreateEmail": true,
		"disableAnonymousUserCreateEmail": false, "disableCustomAddressName": false, "enableUserDeleteEmail": true,
		"enableAutoReply": false, "enableIndexAbout": false, "copyright": "MailWisp",
		"cfTurnstileSiteKey": "", "enableWebhook": false, "isS3Enabled": false, "enableSendMail": false,
		"version": "v1.10.0-mailwisp.1", "showGithub": false, "showGithubForUser": false,
		"disableAdminPasswordCheck": false, "enableAddressPassword": false, "enableAgentEmailInfo": false,
		"smtpImapProxyConfig": map[string]any{
			"smtp": map[string]any{"host": "", "port": 8025, "starttls": false},
			"imap": map[string]any{"host": "", "port": 11143, "starttls": false},
		},
		"statusUrl": "", "enableGlobalTurnstileCheck": false,
	})
}

func (s *Server) handleCloudflareTempUserOpenSettings(w http.ResponseWriter, r *http.Request) {
	if !s.cloudflareTempEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enable": false, "enableMailVerify": false, "oauth2ClientIDs": []any{},
	})
}

func (s *Server) handleCloudflareTempCreateAddress(w http.ResponseWriter, r *http.Request) {
	if !s.cloudflareTempEnabled(w, r) {
		return
	}
	if !s.limiter.allow(s.clientIP(r)) {
		writeCloudflareTempText(w, http.StatusTooManyRequests, "Rate limit exceeded")
		return
	}
	var request struct {
		Name                  string `json:"name"`
		Domain                string `json:"domain"`
		EnableRandomSubdomain bool   `json:"enableRandomSubdomain"`
	}
	if !decodeCloudflareTempJSON(w, r, &request, true) {
		return
	}
	if request.EnableRandomSubdomain {
		writeCloudflareTempText(w, http.StatusBadRequest, "Random subdomain is not supported")
		return
	}
	created, err := s.cloudflareTemp.CreateAddress(r.Context(), request.Name, request.Domain)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jwt": created.Capability.Plaintext, "address": created.Inbox.Address, "address_id": created.AddressID,
	})
}

func (s *Server) handleCloudflareTempSettings(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.cloudflareTempPrincipal(w, r, auth.ScopeInboxRead)
	if !ok {
		return
	}
	inbox, err := s.cloudflareTemp.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"address": inbox.Address, "send_balance": 0})
}

func (s *Server) handleCloudflareTempRawMails(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.cloudflareTempPrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	if !s.beginCloudflareTempHeavyRequest(w) {
		return
	}
	defer s.endCloudflareTempHeavyRequest()
	limit, offset, ok := cloudflareTempPage(w, r)
	if !ok {
		return
	}
	inbox, page, compatibilityIDs, ok := s.cloudflareTempMessagePage(w, r, principal.InboxID, limit, offset)
	if !ok {
		return
	}
	results := make([]cloudflareTempRawMail, 0, len(page.Items))
	var payloadBytes int64
	for _, summary := range page.Items {
		detail, err := s.cloudflareTemp.Mailboxes().GetMessage(r.Context(), principal.InboxID, summary.ID)
		if err != nil {
			writeCloudflareTempMappedError(w, err)
			return
		}
		raw, err := s.readCloudflareTempRaw(r, principal.InboxID, summary.ID, &payloadBytes)
		if err != nil {
			writeCloudflareTempMappedError(w, err)
			return
		}
		results = append(results, presentCloudflareTempRawMail(compatibilityIDs[summary.ID], detail, inbox.Address, raw))
	}
	count := 0
	if offset == 0 {
		count = page.Total
	}
	writeCloudflareTempBoundedJSON(w, http.StatusOK, map[string]any{"results": results, "count": count})
}

func (s *Server) handleCloudflareTempRawMail(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.cloudflareTempPrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	if !s.beginCloudflareTempHeavyRequest(w) {
		return
	}
	defer s.endCloudflareTempHeavyRequest()
	compatibilityID, messageID, found, err := s.cloudflareTempMessageID(r, principal.InboxID)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	inbox, err := s.cloudflareTemp.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	detail, err := s.cloudflareTemp.Mailboxes().GetMessage(r.Context(), principal.InboxID, messageID)
	if errors.Is(err, mailbox.ErrMessageNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	var payloadBytes int64
	raw, err := s.readCloudflareTempRaw(r, principal.InboxID, messageID, &payloadBytes)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	writeCloudflareTempBoundedJSON(w, http.StatusOK, presentCloudflareTempRawMail(compatibilityID, detail, inbox.Address, raw))
}

func (s *Server) handleCloudflareTempParsedMails(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.cloudflareTempPrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	if !s.beginCloudflareTempHeavyRequest(w) {
		return
	}
	defer s.endCloudflareTempHeavyRequest()
	limit, offset, ok := cloudflareTempPage(w, r)
	if !ok {
		return
	}
	inbox, page, compatibilityIDs, ok := s.cloudflareTempMessagePage(w, r, principal.InboxID, limit, offset)
	if !ok {
		return
	}
	results := make([]cloudflareTempParsedMail, 0, len(page.Items))
	var payloadBytes int64
	for _, summary := range page.Items {
		detail, err := s.cloudflareTemp.Mailboxes().GetMessage(r.Context(), principal.InboxID, summary.ID)
		if err != nil {
			writeCloudflareTempMappedError(w, err)
			return
		}
		parsed := presentCloudflareTempParsedMail(compatibilityIDs[summary.ID], detail, inbox.Address)
		payloadBytes += int64(len(parsed.Text) + len(parsed.HTML) + len(parsed.Subject) + len(parsed.Sender))
		if payloadBytes > cloudflareTempMaxPayloadBytes {
			writeCloudflareTempMappedError(w, errCloudflareTempPayloadTooLarge)
			return
		}
		results = append(results, parsed)
	}
	count := 0
	if offset == 0 {
		count = page.Total
	}
	writeCloudflareTempBoundedJSON(w, http.StatusOK, map[string]any{"results": results, "count": count})
}

func (s *Server) handleCloudflareTempParsedMail(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.cloudflareTempPrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	if !s.beginCloudflareTempHeavyRequest(w) {
		return
	}
	defer s.endCloudflareTempHeavyRequest()
	compatibilityID, messageID, found, err := s.cloudflareTempMessageID(r, principal.InboxID)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	inbox, err := s.cloudflareTemp.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	detail, err := s.cloudflareTemp.Mailboxes().GetMessage(r.Context(), principal.InboxID, messageID)
	if errors.Is(err, mailbox.ErrMessageNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	parsed := presentCloudflareTempParsedMail(compatibilityID, detail, inbox.Address)
	if len(parsed.Text)+len(parsed.HTML)+len(parsed.Subject)+len(parsed.Sender) > cloudflareTempMaxPayloadBytes {
		writeCloudflareTempMappedError(w, errCloudflareTempPayloadTooLarge)
		return
	}
	writeCloudflareTempBoundedJSON(w, http.StatusOK, parsed)
}

func (s *Server) handleCloudflareTempDeleteMail(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.cloudflareTempPrincipal(w, r, auth.ScopeMessageDelete)
	if !ok {
		return
	}
	_, messageID, found, err := s.cloudflareTempMessageID(r, principal.InboxID)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	if found {
		if deleteErr := s.cloudflareTemp.Mailboxes().DeleteMessage(r.Context(), principal.InboxID, messageID); deleteErr != nil && !errors.Is(deleteErr, mailbox.ErrMessageNotFound) {
			writeCloudflareTempMappedError(w, deleteErr)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) handleCloudflareTempDeleteAddress(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.cloudflareTempPrincipal(w, r, auth.ScopeInboxDelete)
	if !ok {
		return
	}
	if err := s.cloudflareTemp.Mailboxes().Delete(r.Context(), principal.InboxID); err != nil {
		writeCloudflareTempMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) cloudflareTempPrincipal(w http.ResponseWriter, r *http.Request, scopes ...auth.Scope) (auth.Principal, bool) {
	if !s.cloudflareTempEnabled(w, r) {
		return auth.Principal{}, false
	}
	scheme, plaintext, found := strings.Cut(r.Header.Get("Authorization"), " ")
	plaintext = strings.TrimSpace(plaintext)
	if !found || !strings.EqualFold(scheme, "Bearer") || plaintext == "" || s.authenticator == nil {
		writeCloudflareTempText(w, http.StatusUnauthorized, "Invalid address credential")
		return auth.Principal{}, false
	}
	principal, err := s.authenticator.Authenticate(r.Context(), plaintext, scopes...)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *Server) cloudflareTempEnabled(w http.ResponseWriter, r *http.Request) bool {
	legacyPath := !strings.HasPrefix(r.URL.Path, "/compat/cloudflare-temp/")
	if s.cloudflareTemp == nil || (legacyPath && !s.cloudflareTempLegacyPaths) {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (s *Server) beginCloudflareTempHeavyRequest(w http.ResponseWriter) bool {
	select {
	case s.cloudflareTempHeavy <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		writeCloudflareTempText(w, http.StatusServiceUnavailable, "Cloudflare Temp Email compatibility worker is busy")
		return false
	}
}

func (s *Server) endCloudflareTempHeavyRequest() { <-s.cloudflareTempHeavy }

func (s *Server) cloudflareTempMessagePage(w http.ResponseWriter, r *http.Request, inboxID message.InboxID, limit, offset int) (mailbox.Inbox, mailbox.MessagePage, map[message.MessageID]int64, bool) {
	inbox, err := s.cloudflareTemp.Mailboxes().Get(r.Context(), inboxID)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return mailbox.Inbox{}, mailbox.MessagePage{}, nil, false
	}
	page, err := s.cloudflareTemp.Mailboxes().ListMessagePage(r.Context(), inboxID, limit, offset)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return mailbox.Inbox{}, mailbox.MessagePage{}, nil, false
	}
	messageIDs := make([]message.MessageID, 0, len(page.Items))
	for _, summary := range page.Items {
		messageIDs = append(messageIDs, summary.ID)
	}
	compatibilityIDs, err := s.cloudflareTemp.EnsureMessageIDs(r.Context(), inboxID, messageIDs)
	if err != nil {
		writeCloudflareTempMappedError(w, err)
		return mailbox.Inbox{}, mailbox.MessagePage{}, nil, false
	}
	return inbox, page, compatibilityIDs, true
}

func (s *Server) cloudflareTempMessageID(r *http.Request, inboxID message.InboxID) (int64, message.MessageID, bool, error) {
	compatibilityID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || compatibilityID <= 0 {
		return 0, "", false, nil
	}
	messageID, err := s.cloudflareTemp.FindMessageID(r.Context(), inboxID, compatibilityID)
	if errors.Is(err, cloudflaretemp.ErrMessageIDNotFound) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return compatibilityID, messageID, true, nil
}

func (s *Server) readCloudflareTempRaw(r *http.Request, inboxID message.InboxID, messageID message.MessageID, payloadBytes *int64) (string, error) {
	source, err := s.cloudflareTemp.Mailboxes().OpenSource(r.Context(), inboxID, messageID)
	if err != nil {
		return "", err
	}
	defer source.Reader.Close()
	if source.Size < 0 || source.Size > cloudflareTempMaxPayloadBytes-*payloadBytes {
		return "", errCloudflareTempPayloadTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(source.Reader, source.Size+1))
	if err != nil {
		return "", fmt.Errorf("read Cloudflare Temp Email Raw MIME: %w", err)
	}
	if int64(len(data)) != source.Size {
		return "", errors.New("Cloudflare Temp Email Raw MIME size mismatch")
	}
	*payloadBytes += int64(len(data))
	return string(data), nil
}

func cloudflareTempPage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > cloudflareTempMaxPageItems {
		writeCloudflareTempText(w, http.StatusBadRequest, "Invalid limit")
		return 0, 0, false
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 || offset > 100_000 {
		writeCloudflareTempText(w, http.StatusBadRequest, "Invalid offset")
		return 0, 0, false
	}
	return limit, offset, true
}

func decodeCloudflareTempJSON(w http.ResponseWriter, r *http.Request, target any, optional bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		if optional && errors.Is(err, io.EOF) {
			return true
		}
		writeCloudflareTempText(w, http.StatusBadRequest, "Invalid JSON body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeCloudflareTempText(w, http.StatusBadRequest, "Invalid JSON body")
		return false
	}
	return true
}

func presentCloudflareTempRawMail(compatibilityID int64, detail mailbox.MessageDetail, address, raw string) cloudflareTempRawMail {
	return cloudflareTempRawMail{
		ID: compatibilityID, MessageID: detail.HeaderMessageID, Source: detail.EnvelopeSender,
		Address: address, Raw: raw, Metadata: "{}", CreatedAt: cloudflareTempTime(detail.ReceivedAt),
	}
}

func presentCloudflareTempParsedMail(compatibilityID int64, detail mailbox.MessageDetail, address string) cloudflareTempParsedMail {
	attachments := make([]cloudflareTempAttachment, 0, len(detail.Attachments))
	for _, attachment := range detail.Attachments {
		attachments = append(attachments, cloudflareTempAttachment{
			Filename: attachment.FileName, MIMEType: attachment.ContentType,
			Disposition: attachment.Disposition, Size: attachment.SizeBytes,
		})
	}
	return cloudflareTempParsedMail{
		ID: compatibilityID, MessageID: detail.HeaderMessageID, Source: detail.EnvelopeSender,
		Address: address, Metadata: "{}", CreatedAt: cloudflareTempTime(detail.ReceivedAt),
		Sender: cloudflareTempSender(detail), Subject: detail.Subject, Text: detail.Text,
		HTML: string(detail.HTMLSource), Attachments: attachments,
	}
}

func cloudflareTempSender(detail mailbox.MessageDetail) string {
	if len(detail.From) == 0 {
		return detail.EnvelopeSender
	}
	if detail.From[0].Name == "" {
		return detail.From[0].Address
	}
	return detail.From[0].Name + " <" + detail.From[0].Address + ">"
}

func cloudflareTempTime(value time.Time) string { return value.UTC().Format("2006-01-02 15:04:05") }

func writeCloudflareTempMappedError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated), errors.Is(err, auth.ErrForbidden):
		writeCloudflareTempText(w, http.StatusUnauthorized, "Invalid address credential")
	case errors.Is(err, cloudflaretemp.ErrInvalidAddressName), errors.Is(err, mailbox.ErrInvalidDomain), errors.Is(err, mailbox.ErrInvalidLocalPart):
		writeCloudflareTempText(w, http.StatusBadRequest, "Failed to create address")
	case errors.Is(err, mailbox.ErrAddressConflict):
		writeCloudflareTempText(w, http.StatusBadRequest, "Address already exists")
	case errors.Is(err, errCloudflareTempPayloadTooLarge):
		writeCloudflareTempText(w, http.StatusRequestEntityTooLarge, "Mail response exceeds the MailWisp compatibility limit")
	case errors.Is(err, mailbox.ErrInboxNotFound):
		writeCloudflareTempText(w, http.StatusBadRequest, "Invalid address")
	case errors.Is(err, mailbox.ErrMessageNotFound):
		writeJSON(w, http.StatusOK, nil)
	default:
		writeCloudflareTempText(w, http.StatusInternalServerError, "Internal server error")
	}
}

func writeCloudflareTempText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

func writeCloudflareTempBoundedJSON(w http.ResponseWriter, status int, value any) {
	var buffer bytes.Buffer
	limited := &cloudflareTempLimitedWriter{destination: &buffer, remaining: cloudflareTempMaxPayloadBytes}
	if err := json.NewEncoder(limited).Encode(value); err != nil {
		writeCloudflareTempText(w, http.StatusRequestEntityTooLarge, "Mail response exceeds the MailWisp compatibility limit")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(buffer.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(buffer.Bytes())
}

type cloudflareTempLimitedWriter struct {
	destination io.Writer
	remaining   int
}

func (w *cloudflareTempLimitedWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, errCloudflareTempPayloadTooLarge
	}
	written, err := w.destination.Write(data)
	w.remaining -= written
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	return written, err
}
