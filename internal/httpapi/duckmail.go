package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"mailwisp/internal/abuse"
	"mailwisp/internal/auth"
	"mailwisp/internal/duckmail"
	"mailwisp/internal/mail"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

type duckMailAccount struct {
	ID        string    `json:"id"`
	Address   string    `json:"address"`
	AuthType  string    `json:"authType"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type duckMailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type duckMailMessage struct {
	ID             string            `json:"id"`
	MessageID      string            `json:"msgid"`
	AccountID      string            `json:"accountId"`
	From           duckMailAddress   `json:"from"`
	To             []duckMailAddress `json:"to"`
	Subject        string            `json:"subject"`
	Text           string            `json:"text,omitempty"`
	HTML           []string          `json:"html,omitempty"`
	Seen           bool              `json:"seen"`
	Deleted        bool              `json:"isDeleted"`
	HasAttachments bool              `json:"hasAttachments"`
	Size           int64             `json:"size"`
	DownloadURL    string            `json:"downloadUrl"`
	Attachments    []duckAttachment  `json:"attachments,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

type duckAttachment struct {
	ID               string `json:"id"`
	Filename         string `json:"filename"`
	ContentType      string `json:"contentType"`
	Disposition      string `json:"disposition"`
	TransferEncoding string `json:"transferEncoding"`
	Related          bool   `json:"related"`
	Size             int64  `json:"size"`
	DownloadURL      string `json:"downloadUrl"`
}

func (s *Server) handleDuckMailDomains(w http.ResponseWriter, r *http.Request) {
	if !s.duckMailEnabled(w) {
		return
	}
	page, ok := duckPage(w, r)
	if !ok {
		return
	}
	domains := s.duckMail.Domains()
	members := make([]map[string]any, 0, len(domains))
	for _, domain := range domains {
		id := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(domain)).String()
		members = append(members, map[string]any{"id": id, "domain": domain, "ownerId": nil, "isVerified": true, "verificationToken": "", "createdAt": time.Unix(0, 0).UTC(), "updatedAt": time.Unix(0, 0).UTC()})
	}
	writeDuckHydra(w, http.StatusOK, "/compat/duckmail/domains", page, members, len(members))
}

func (s *Server) handleDuckMailCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.duckMailEnabled(w) {
		return
	}
	if !s.limiter.allow(s.clientIP(r)) {
		writeDuckError(w, http.StatusTooManyRequests, "Too Many Requests", "Too many account requests")
		return
	}
	var request struct {
		Address   string `json:"address"`
		Password  string `json:"password"`
		ExpiresIn *int64 `json:"expiresIn"`
	}
	if !decodeDuckJSON(w, r, &request) {
		return
	}
	decision, err := s.consumeCreateQuota(r)
	setCreateQuotaHeaders(w, decision, err)
	if errors.Is(err, abuse.ErrDailyCreateQuotaExceeded) {
		writeDuckError(w, http.StatusTooManyRequests, "Too Many Requests", "Daily account quota exceeded")
		return
	}
	if err != nil {
		writeDuckError(w, http.StatusInternalServerError, "Internal Server Error", "Persistent account admission failed")
		return
	}
	inbox, err := s.duckMail.CreateAccount(r.Context(), duckmail.CreateAccountRequest{Address: request.Address, Password: request.Password, ExpiresIn: request.ExpiresIn})
	if err != nil {
		writeDuckMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, presentDuckAccount(inbox))
}

func (s *Server) handleDuckMailToken(w http.ResponseWriter, r *http.Request) {
	if !s.duckMailEnabled(w) {
		return
	}
	if !s.limiter.allow(s.clientIP(r)) {
		writeDuckError(w, http.StatusTooManyRequests, "Too Many Requests", "Too many login requests")
		return
	}
	var request struct{ Address, Password string }
	if !decodeDuckJSON(w, r, &request) {
		return
	}
	issued, err := s.duckMail.Login(r.Context(), request.Address, request.Password)
	if err != nil {
		writeDuckMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": string(issued.InboxID), "token": issued.Plaintext})
}

func (s *Server) handleDuckMailMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.duckPrincipal(w, r, auth.ScopeInboxRead)
	if !ok {
		return
	}
	inbox, err := s.duckMail.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeDuckMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, presentDuckAccount(inbox))
}

func (s *Server) handleDuckMailDeleteAccount(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.duckPrincipal(w, r, auth.ScopeInboxDelete)
	if !ok {
		return
	}
	if r.PathValue("id") != string(principal.InboxID) {
		writeDuckError(w, http.StatusForbidden, "Forbidden", "You can only delete your own account")
		return
	}
	if err := s.duckMail.Mailboxes().Delete(r.Context(), principal.InboxID); err != nil {
		writeDuckMappedError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDuckMailMessages(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.duckPrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	page, ok := duckPage(w, r)
	if !ok {
		return
	}
	inbox, err := s.duckMail.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeDuckMappedError(w, err)
		return
	}
	result, err := s.duckMail.Mailboxes().ListMessagePage(r.Context(), principal.InboxID, 30, (page-1)*30)
	if err != nil {
		writeDuckMappedError(w, err)
		return
	}
	members := make([]duckMailMessage, 0, len(result.Items))
	for _, summary := range result.Items {
		members = append(members, presentDuckSummary(summary, inbox))
	}
	writeDuckHydra(w, http.StatusOK, "/compat/duckmail/messages", page, members, result.Total)
}

func (s *Server) handleDuckMailMessage(w http.ResponseWriter, r *http.Request) {
	principal, messageID, ok := s.duckMessagePrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	detail, err := s.duckMail.Mailboxes().GetMessage(r.Context(), principal.InboxID, messageID)
	if err != nil {
		writeDuckMappedError(w, err)
		return
	}
	inbox, err := s.duckMail.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeDuckMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, presentDuckDetail(detail, inbox))
}

func (s *Server) handleDuckMailSeen(w http.ResponseWriter, r *http.Request) {
	principal, messageID, ok := s.duckMessagePrincipal(w, r, auth.ScopeMessageUpdate)
	if !ok {
		return
	}
	if err := s.duckMail.Mailboxes().MarkMessageSeen(r.Context(), principal.InboxID, messageID); err != nil {
		writeDuckMappedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"seen": true})
}

func (s *Server) handleDuckMailDeleteMessage(w http.ResponseWriter, r *http.Request) {
	principal, messageID, ok := s.duckMessagePrincipal(w, r, auth.ScopeMessageDelete)
	if !ok {
		return
	}
	if err := s.duckMail.Mailboxes().DeleteMessage(r.Context(), principal.InboxID, messageID); err != nil {
		writeDuckMappedError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDuckMailSource(w http.ResponseWriter, r *http.Request) {
	principal, messageID, ok := s.duckMessagePrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	source, err := s.duckMail.Mailboxes().OpenSource(r.Context(), principal.InboxID, messageID)
	if err != nil {
		writeDuckMappedError(w, err)
		return
	}
	defer source.Reader.Close()
	data, err := io.ReadAll(io.LimitReader(source.Reader, source.Size+1))
	if err != nil || int64(len(data)) != source.Size {
		writeDuckError(w, http.StatusInternalServerError, "Internal Server Error", "Raw message source is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": string(messageID), "downloadUrl": "/compat/duckmail/sources/" + string(messageID), "data": string(data)})
}

func (s *Server) duckMessagePrincipal(w http.ResponseWriter, r *http.Request, scope auth.Scope) (auth.Principal, message.MessageID, bool) {
	principal, ok := s.duckPrincipal(w, r, scope)
	if !ok {
		return auth.Principal{}, "", false
	}
	rawID := r.PathValue("id")
	if _, err := uuid.Parse(rawID); err != nil {
		writeDuckError(w, http.StatusNotFound, "Not Found", "Message not found")
		return auth.Principal{}, "", false
	}
	return principal, message.MessageID(rawID), true
}

func (s *Server) duckPrincipal(w http.ResponseWriter, r *http.Request, scopes ...auth.Scope) (auth.Principal, bool) {
	if !s.duckMailEnabled(w) || s.authenticator == nil {
		return auth.Principal{}, false
	}
	scheme, plaintext, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(plaintext) == "" {
		writeDuckError(w, http.StatusUnauthorized, "Unauthorized", "A Bearer token is required")
		return auth.Principal{}, false
	}
	principal, err := s.authenticator.Authenticate(r.Context(), strings.TrimSpace(plaintext), scopes...)
	if err != nil {
		writeDuckMappedError(w, err)
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *Server) duckMailEnabled(w http.ResponseWriter) bool {
	if s.duckMail == nil {
		writeDuckError(w, http.StatusNotFound, "Not Found", "DuckMail compatibility is disabled")
		return false
	}
	return true
}

func decodeDuckJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		writeDuckError(w, http.StatusBadRequest, "Bad Request", "Request body is invalid")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeDuckError(w, http.StatusBadRequest, "Bad Request", "Request body must contain one JSON object")
		return false
	}
	return true
}

func duckPage(w http.ResponseWriter, r *http.Request) (int, bool) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 10_000 {
			writeDuckError(w, http.StatusBadRequest, "Bad Request", "page must be a positive integer")
			return 0, false
		}
		page = parsed
	}
	return page, true
}

func presentDuckAccount(inbox mailbox.Inbox) duckMailAccount {
	return duckMailAccount{ID: string(inbox.ID), Address: inbox.Address, AuthType: "email", CreatedAt: inbox.CreatedAt, UpdatedAt: inbox.CreatedAt}
}

func presentDuckSummary(summary mailbox.MessageSummary, inbox mailbox.Inbox) duckMailMessage {
	return duckMailMessage{
		ID: string(summary.ID), MessageID: string(summary.ID), AccountID: string(inbox.ID), From: duckMailAddress{Address: summary.EnvelopeSender}, To: []duckMailAddress{{Address: inbox.Address}},
		Subject: summary.Subject, Seen: summary.Seen, HasAttachments: summary.HasAttachments, Size: summary.SizeBytes,
		DownloadURL: "/compat/duckmail/sources/" + string(summary.ID), CreatedAt: summary.ReceivedAt, UpdatedAt: summary.ReceivedAt,
	}
}

func presentDuckDetail(detail mailbox.MessageDetail, inbox mailbox.Inbox) duckMailMessage {
	presented := presentDuckSummary(detail.MessageSummary, inbox)
	presented.MessageID = detail.HeaderMessageID
	presented.Text = detail.Text
	if detail.HTMLSource != "" {
		presented.HTML = []string{string(detail.HTMLSource)}
	}
	presented.From = presentDuckFrom(detail.From, detail.EnvelopeSender)
	presented.To = presentDuckAddresses(detail.To, inbox.Address)
	presented.Attachments = make([]duckAttachment, 0, len(detail.Attachments))
	for _, attachment := range detail.Attachments {
		presented.Attachments = append(presented.Attachments, duckAttachment{ID: attachment.PartPath, Filename: attachment.FileName, ContentType: attachment.ContentType, Disposition: attachment.Disposition, Size: attachment.SizeBytes})
	}
	return presented
}

func presentDuckFrom(addresses []mail.Address, fallback string) duckMailAddress {
	if len(addresses) == 0 {
		return duckMailAddress{Address: fallback}
	}
	return duckMailAddress{Name: addresses[0].Name, Address: addresses[0].Address}
}

func presentDuckAddresses(addresses []mail.Address, fallback string) []duckMailAddress {
	if len(addresses) == 0 {
		return []duckMailAddress{{Address: fallback}}
	}
	presented := make([]duckMailAddress, 0, len(addresses))
	for _, address := range addresses {
		presented = append(presented, duckMailAddress{Name: address.Name, Address: address.Address})
	}
	return presented
}

func writeDuckHydra[T any](w http.ResponseWriter, status int, path string, page int, members []T, total int) {
	last := (total + 29) / 30
	if last < 1 {
		last = 1
	}
	view := map[string]any{"@id": path + "?page=" + strconv.Itoa(page), "@type": "PartialCollectionView", "hydra:first": path + "?page=1", "hydra:last": path + "?page=" + strconv.Itoa(last)}
	writeJSON(w, status, map[string]any{"hydra:member": members, "hydra:totalItems": total, "hydra:view": view})
}

func writeDuckMappedError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, duckmail.ErrInvalidAccount), errors.Is(err, duckmail.ErrPermanentUnsupported):
		writeDuckError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	case errors.Is(err, duckmail.ErrAccountConflict):
		writeDuckError(w, http.StatusConflict, "Conflict", "Email address already exists")
	case errors.Is(err, duckmail.ErrLoginFailed), errors.Is(err, auth.ErrUnauthenticated):
		writeDuckError(w, http.StatusUnauthorized, "Unauthorized", "Invalid address, password, or token")
	case errors.Is(err, auth.ErrForbidden):
		writeDuckError(w, http.StatusForbidden, "Forbidden", "Token scope does not allow this action")
	case errors.Is(err, mailbox.ErrInboxNotFound), errors.Is(err, mailbox.ErrMessageNotFound):
		writeDuckError(w, http.StatusNotFound, "Not Found", "Resource not found")
	default:
		writeDuckError(w, http.StatusInternalServerError, "Internal Server Error", "Internal server error")
	}
}

func writeDuckError(w http.ResponseWriter, status int, errorType, message string) {
	writeJSON(w, status, map[string]string{"error": errorType, "message": message})
}
