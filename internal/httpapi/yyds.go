package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"mailwisp/internal/auth"
	"mailwisp/internal/mail"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
	"mailwisp/internal/yyds"
)

func (s *Server) handleYYDSDomains(w http.ResponseWriter, _ *http.Request) {
	if !s.yydsEnabled(w) {
		return
	}
	domains := s.yyds.Domains()
	data := make([]map[string]any, 0, len(domains))
	for _, domain := range domains {
		data = append(data, map[string]any{
			"id": uuid.NewSHA1(uuid.NameSpaceDNS, []byte(domain)).String(), "domain": domain,
			"isPublic": true, "isVerified": true, "isMxValid": true, "sourceType": "mailwisp",
		})
	}
	writeYYDS(w, http.StatusOK, data)
}

func (s *Server) handleYYDSCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.yydsEnabled(w) {
		return
	}
	if !s.limiter.allow(s.clientIP(r)) {
		writeYYDSError(w, http.StatusTooManyRequests, "rate_limit_slow_down", "请求过于频繁，请稍后再试")
		return
	}
	var request struct {
		Address        string `json:"address"`
		LocalPart      string `json:"localPart"`
		Domain         string `json:"domain"`
		WildcardRuleID string `json:"wildcardRuleId"`
		Subdomain      string `json:"subdomain"`
		SubdomainLabel string `json:"subdomainLabel"`
	}
	if !decodeYYDSJSON(w, r, &request, true) {
		return
	}
	if request.WildcardRuleID != "" || request.Subdomain != "" || request.SubdomainLabel != "" {
		writeYYDSError(w, http.StatusBadRequest, "wildcard_rule_unavailable", "MailWisp未启用YYDS泛子域名兼容")
		return
	}
	created, err := s.yyds.CreateAccount(r.Context(), yyds.CreateAccountRequest{Address: request.Address, LocalPart: request.LocalPart, Domain: request.Domain})
	if err != nil {
		writeYYDSMappedError(w, err)
		return
	}
	writeYYDS(w, http.StatusCreated, presentYYDSAccount(created.Inbox, created.Capability.Plaintext))
}

func (s *Server) handleYYDSRefreshToken(w http.ResponseWriter, r *http.Request) {
	if !s.yydsEnabled(w) {
		return
	}
	plaintext, ok := yydsBearer(w, r)
	if !ok {
		return
	}
	var request struct {
		Address string `json:"address"`
	}
	if !decodeYYDSJSON(w, r, &request, false) {
		return
	}
	if strings.TrimSpace(request.Address) == "" {
		writeYYDSError(w, http.StatusBadRequest, "address_required", "请输入邮箱地址")
		return
	}
	rotated, inbox, err := s.yyds.RefreshToken(r.Context(), plaintext, request.Address)
	if err != nil {
		writeYYDSMappedError(w, err)
		return
	}
	writeYYDS(w, http.StatusOK, map[string]any{"id": inbox.ID, "address": inbox.Address, "token": rotated.Plaintext})
}

func (s *Server) handleYYDSAccountMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.yydsPrincipal(w, r, auth.ScopeInboxRead)
	if !ok {
		return
	}
	inbox, err := s.yyds.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeYYDSMappedError(w, err)
		return
	}
	writeYYDS(w, http.StatusOK, presentYYDSAccount(inbox, ""))
}

func (s *Server) handleYYDSAccount(w http.ResponseWriter, r *http.Request) {
	required := auth.ScopeInboxRead
	if r.Method == http.MethodDelete {
		required = auth.ScopeInboxDelete
	}
	principal, ok := s.yydsPrincipal(w, r, required)
	if !ok {
		return
	}
	if r.PathValue("id") != string(principal.InboxID) {
		writeYYDSError(w, http.StatusNotFound, "account_not_found", "邮箱账号不存在")
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.yyds.Mailboxes().Delete(r.Context(), principal.InboxID); err != nil {
			writeYYDSMappedError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	inbox, err := s.yyds.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeYYDSMappedError(w, err)
		return
	}
	writeYYDS(w, http.StatusOK, presentYYDSAccount(inbox, ""))
}

func (s *Server) handleYYDSMessages(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.yydsPrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	limit, offset, ok := yydsPage(w, r)
	if !ok {
		return
	}
	inbox, err := s.yyds.Mailboxes().Get(r.Context(), principal.InboxID)
	if err != nil {
		writeYYDSMappedError(w, err)
		return
	}
	page, err := s.yyds.Mailboxes().ListMessagePage(r.Context(), principal.InboxID, limit, offset)
	if err != nil {
		writeYYDSMappedError(w, err)
		return
	}
	messages := make([]map[string]any, 0, len(page.Items))
	for _, summary := range page.Items {
		messages = append(messages, presentYYDSSummary(summary, inbox))
	}
	writeYYDS(w, http.StatusOK, map[string]any{"messages": messages, "total": page.Total, "unreadCount": page.Unread})
}

func (s *Server) handleYYDSMessage(w http.ResponseWriter, r *http.Request) {
	required := auth.ScopeMessageRead
	if r.Method == http.MethodPatch {
		required = auth.ScopeMessageUpdate
	} else if r.Method == http.MethodDelete {
		required = auth.ScopeMessageDelete
	}
	principal, messageID, ok := s.yydsMessagePrincipal(w, r, required)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		detail, err := s.yyds.Mailboxes().GetMessage(r.Context(), principal.InboxID, messageID)
		if err != nil {
			writeYYDSMappedError(w, err)
			return
		}
		inbox, err := s.yyds.Mailboxes().Get(r.Context(), principal.InboxID)
		if err != nil {
			writeYYDSMappedError(w, err)
			return
		}
		writeYYDS(w, http.StatusOK, presentYYDSDetail(detail, inbox))
	case http.MethodPatch:
		var request struct {
			Seen    *bool `json:"seen"`
			Starred *bool `json:"starred"`
		}
		if !decodeYYDSJSON(w, r, &request, true) {
			return
		}
		if request.Starred != nil || (request.Seen != nil && !*request.Seen) {
			writeYYDSError(w, http.StatusForbidden, "permission_denied", "Temporary Token不支持Starred或恢复未读")
			return
		}
		if err := s.yyds.Mailboxes().MarkMessageSeen(r.Context(), principal.InboxID, messageID); err != nil {
			writeYYDSMappedError(w, err)
			return
		}
		writeYYDS(w, http.StatusOK, map[string]any{"id": messageID, "seen": true})
	case http.MethodDelete:
		if err := s.yyds.Mailboxes().DeleteMessage(r.Context(), principal.InboxID, messageID); err != nil {
			writeYYDSMappedError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleYYDSSource(w http.ResponseWriter, r *http.Request) {
	principal, messageID, ok := s.yydsMessagePrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	source, err := s.yyds.Mailboxes().OpenSource(r.Context(), principal.InboxID, messageID)
	if err != nil {
		writeYYDSMappedError(w, err)
		return
	}
	defer source.Reader.Close()
	data, err := io.ReadAll(io.LimitReader(source.Reader, source.Size+1))
	if err != nil || int64(len(data)) != source.Size {
		writeYYDSError(w, http.StatusInternalServerError, "message_source_read_failed", "读取邮件原文失败")
		return
	}
	writeYYDS(w, http.StatusOK, map[string]any{"id": messageID, "data": string(data)})
}

func (s *Server) handleYYDSAttachment(w http.ResponseWriter, r *http.Request) {
	principal, messageID, ok := s.yydsMessagePrincipal(w, r, auth.ScopeMessageRead)
	if !ok {
		return
	}
	source, err := s.yyds.Mailboxes().OpenAttachment(r.Context(), principal.InboxID, messageID, r.PathValue("part"))
	if err != nil {
		writeYYDSMappedError(w, err)
		return
	}
	defer source.Reader.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	contentType := source.ContentType
	if mediaType, _, parseErr := mime.ParseMediaType(contentType); parseErr != nil || mediaType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": source.FileName}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	if source.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(source.Size, 10))
	}
	if _, err := io.Copy(w, source.Reader); err != nil {
		s.logger.WarnContext(r.Context(), "YYDS attachment stream failed", "request_id", requestIDFromContext(r.Context()), "error", err)
	}
}

func (s *Server) yydsPrincipal(w http.ResponseWriter, r *http.Request, scopes ...auth.Scope) (auth.Principal, bool) {
	if !s.yydsEnabled(w) || s.authenticator == nil {
		return auth.Principal{}, false
	}
	plaintext, ok := yydsBearer(w, r)
	if !ok {
		return auth.Principal{}, false
	}
	principal, err := s.authenticator.Authenticate(r.Context(), plaintext, scopes...)
	if err != nil {
		writeYYDSMappedError(w, err)
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *Server) yydsMessagePrincipal(w http.ResponseWriter, r *http.Request, scope auth.Scope) (auth.Principal, message.MessageID, bool) {
	principal, ok := s.yydsPrincipal(w, r, scope)
	if !ok {
		return auth.Principal{}, "", false
	}
	rawID := r.PathValue("id")
	if _, err := uuid.Parse(rawID); err != nil {
		writeYYDSError(w, http.StatusNotFound, "message_not_found", "邮件不存在")
		return auth.Principal{}, "", false
	}
	return principal, message.MessageID(rawID), true
}

func (s *Server) yydsEnabled(w http.ResponseWriter) bool {
	if s.yyds == nil {
		writeYYDSError(w, http.StatusNotFound, "request_failed", "YYDS兼容接口未启用")
		return false
	}
	return true
}

func yydsBearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	scheme, plaintext, found := strings.Cut(r.Header.Get("Authorization"), " ")
	plaintext = strings.TrimSpace(plaintext)
	if !found || !strings.EqualFold(scheme, "Bearer") || plaintext == "" {
		writeYYDSError(w, http.StatusUnauthorized, "temp_token_required", "缺少临时邮箱访问凭证")
		return "", false
	}
	return plaintext, true
}

func decodeYYDSJSON(w http.ResponseWriter, r *http.Request, target any, optional bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		if optional && errors.Is(err, io.EOF) {
			return true
		}
		writeYYDSError(w, http.StatusBadRequest, "request_body_read_failed", "读取请求内容失败")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeYYDSError(w, http.StatusBadRequest, "request_body_read_failed", "请求只能包含一个JSON对象")
		return false
	}
	return true
}

func yydsPage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset := 50, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeYYDSError(w, http.StatusBadRequest, "request_failed", "limit必须在1到200之间")
			return 0, 0, false
		}
		limit = parsed
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 100_000 {
			writeYYDSError(w, http.StatusBadRequest, "request_failed", "offset无效")
			return 0, 0, false
		}
		offset = parsed
	}
	return limit, offset, true
}

func presentYYDSAccount(inbox mailbox.Inbox, token string) map[string]any {
	_, domain, _ := strings.Cut(inbox.Address, "@")
	result := map[string]any{
		"id": inbox.ID, "address": inbox.Address, "domain": domain, "createdAt": inbox.CreatedAt,
		"expiresAt": inbox.ExpiresAt, "isActive": inbox.Status == "active", "inboxType": "temporary",
		"mode": "fixed", "source": "mailwisp",
	}
	if token != "" {
		result["token"] = token
	}
	return result
}

func presentYYDSSummary(summary mailbox.MessageSummary, inbox mailbox.Inbox) map[string]any {
	return map[string]any{
		"id": summary.ID, "inboxId": inbox.ID, "inbox_id": inbox.ID,
		"from":    map[string]string{"name": "", "address": summary.EnvelopeSender},
		"to":      []map[string]string{{"name": "", "address": inbox.Address}},
		"subject": summary.Subject, "seen": summary.Seen, "size": summary.SizeBytes,
		"hasAttachments": summary.HasAttachments, "createdAt": summary.ReceivedAt,
	}
}

func presentYYDSDetail(detail mailbox.MessageDetail, inbox mailbox.Inbox) map[string]any {
	result := presentYYDSSummary(detail.MessageSummary, inbox)
	result["messageId"] = detail.HeaderMessageID
	result["intro"] = detail.Preview
	result["text"] = detail.Text
	result["html"] = []string{}
	if detail.HTMLSource != "" {
		result["html"] = []string{string(detail.HTMLSource)}
	}
	result["from"] = presentYYDSAddress(detail.From, detail.EnvelopeSender)
	result["to"] = presentYYDSAddresses(detail.To, inbox.Address)
	result["cc"] = yydsAddressStrings(detail.Cc)
	result["bcc"] = []string{}
	attachments := make([]map[string]any, 0, len(detail.Attachments))
	for _, attachment := range detail.Attachments {
		attachments = append(attachments, map[string]any{
			"id": attachment.PartPath, "filename": attachment.FileName, "contentType": attachment.ContentType,
			"disposition": attachment.Disposition, "contentId": attachment.ContentID, "size": attachment.SizeBytes,
			"downloadUrl": "/compat/yyds/v1/messages/" + string(detail.ID) + "/attachments/" + attachment.PartPath,
		})
	}
	result["attachments"] = attachments
	return result
}

func presentYYDSAddress(addresses []mail.Address, fallback string) map[string]string {
	if len(addresses) == 0 {
		return map[string]string{"name": "", "address": fallback}
	}
	return map[string]string{"name": addresses[0].Name, "address": addresses[0].Address}
}

func presentYYDSAddresses(addresses []mail.Address, fallback string) []map[string]string {
	if len(addresses) == 0 {
		return []map[string]string{{"name": "", "address": fallback}}
	}
	result := make([]map[string]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, map[string]string{"name": address.Name, "address": address.Address})
	}
	return result
}

func yydsAddressStrings(addresses []mail.Address) []string {
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, address.Address)
	}
	return result
}

func writeYYDS(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"success": true, "data": data, "error": "", "errorCode": ""})
}

func writeYYDSError(w http.ResponseWriter, status int, code, text string) {
	writeJSON(w, status, map[string]any{"success": false, "error": text, "errorCode": code})
}

func writeYYDSMappedError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated), errors.Is(err, yyds.ErrAddressMismatch):
		writeYYDSError(w, http.StatusUnauthorized, "token_invalid_or_expired", "登录凭证无效或已过期")
	case errors.Is(err, auth.ErrForbidden):
		writeYYDSError(w, http.StatusForbidden, "permission_denied", "没有权限")
	case errors.Is(err, yyds.ErrInvalidRequest), errors.Is(err, mailbox.ErrInvalidDomain), errors.Is(err, mailbox.ErrInvalidLifetime), errors.Is(err, mailbox.ErrInvalidLocalPart):
		writeYYDSError(w, http.StatusBadRequest, "address_invalid_or_missing", "缺少地址或地址无效")
	case errors.Is(err, mailbox.ErrAddressConflict):
		writeYYDSError(w, http.StatusConflict, "address_already_in_use", "该邮箱地址已被使用")
	case errors.Is(err, mailbox.ErrInboxNotFound):
		writeYYDSError(w, http.StatusNotFound, "account_not_found", "邮箱账号不存在")
	case errors.Is(err, mailbox.ErrMessageNotFound):
		writeYYDSError(w, http.StatusNotFound, "message_not_found", "邮件不存在")
	default:
		writeYYDSError(w, http.StatusInternalServerError, "request_failed", "请求失败")
	}
}
