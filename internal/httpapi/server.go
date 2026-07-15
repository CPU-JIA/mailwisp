// Package httpapi implements MailWisp's public HTTP transport.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"mailwisp/internal/auth"
	"mailwisp/internal/cloudflaretemp"
	"mailwisp/internal/config"
	"mailwisp/internal/duckmail"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
	"mailwisp/internal/yyds"
)

// ReadinessChecker verifies whether a dependency can serve requests.
type ReadinessChecker interface {
	Ready(context.Context) error
}

// HTTPMetrics observes bounded route patterns and durations.
type HTTPMetrics interface {
	ObserveHTTPRequest(method, route string, status int, duration time.Duration)
}

// MailboxService is the canonical Inbox and message application boundary.
type MailboxService interface {
	Create(context.Context, mailbox.CreateRequest) (mailbox.CreatedInbox, error)
	Get(context.Context, message.InboxID) (mailbox.Inbox, error)
	Delete(context.Context, message.InboxID) error
	ListMessages(context.Context, message.InboxID, int) ([]mailbox.MessageSummary, error)
	GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error)
	OpenAttachment(context.Context, message.InboxID, message.MessageID, string) (mailbox.AttachmentSource, error)
	DeleteMessage(context.Context, message.InboxID, message.MessageID) error
}

// CapabilityAuthenticator validates one-time Bearer capabilities.
type CapabilityAuthenticator interface {
	Authenticate(context.Context, string, ...auth.Scope) (auth.Principal, error)
}

// Server owns the public HTTP server and its lifecycle.
type Server struct {
	httpServer                *http.Server
	logger                    *slog.Logger
	ready                     atomic.Bool
	readiness                 ReadinessChecker
	readinessTimeout          time.Duration
	mailbox                   MailboxService
	authenticator             CapabilityAuthenticator
	browserSessions           *auth.BrowserSessionManager
	limiter                   *createLimiter
	trustedProxies            []*net.IPNet
	duckMail                  *duckmail.Service
	yyds                      *yyds.Service
	cloudflareTemp            *cloudflaretemp.Service
	cloudflareTempLegacyPaths bool
	cloudflareTempHeavy       chan struct{}
	metricsHandler            http.Handler
	metrics                   HTTPMetrics
}

// NewServer creates a production-configured public HTTP server.
func NewServer(cfg config.HTTP, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{logger: logger, readinessTimeout: cfg.ReadinessTimeout, cloudflareTempHeavy: make(chan struct{}, 2)}
	if cfg.CreateRatePerMinute <= 0 {
		cfg.CreateRatePerMinute = 12
	}
	if cfg.CreateRateBurst <= 0 {
		cfg.CreateRateBurst = 4
	}
	server.limiter = newCreateLimiter(cfg.CreateRatePerMinute, cfg.CreateRateBurst)
	for _, rawCIDR := range cfg.TrustedProxyCIDRs {
		if _, network, err := net.ParseCIDR(rawCIDR); err == nil {
			server.trustedProxies = append(server.trustedProxies, network)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", server.handleLive)
	mux.HandleFunc("GET /readyz", server.handleReady)
	mux.HandleFunc("GET /health", server.handleReady)
	mux.HandleFunc("GET /metrics", server.handleMetrics)
	mux.HandleFunc("POST /api/v1/inboxes", server.handleCreateInbox)
	mux.HandleFunc("POST /api/v1/session", server.handleCreateSession)
	mux.HandleFunc("GET /api/v1/session", server.handleGetSession)
	mux.HandleFunc("DELETE /api/v1/session", server.handleDeleteSession)
	mux.HandleFunc("GET /api/v1/inboxes/me", server.handleGetInbox)
	mux.HandleFunc("DELETE /api/v1/inboxes/me", server.handleDeleteInbox)
	mux.HandleFunc("GET /api/v1/inboxes/me/messages", server.handleListMessages)
	mux.HandleFunc("GET /api/v1/inboxes/me/messages/{id}", server.handleGetMessage)
	mux.HandleFunc("GET /api/v1/inboxes/me/messages/{id}/attachments/{part}", server.handleGetAttachment)
	mux.HandleFunc("DELETE /api/v1/inboxes/me/messages/{id}", server.handleDeleteMessage)
	mux.HandleFunc("GET /compat/duckmail/domains", server.handleDuckMailDomains)
	mux.HandleFunc("POST /compat/duckmail/accounts", server.handleDuckMailCreateAccount)
	mux.HandleFunc("POST /compat/duckmail/token", server.handleDuckMailToken)
	mux.HandleFunc("GET /compat/duckmail/me", server.handleDuckMailMe)
	mux.HandleFunc("DELETE /compat/duckmail/accounts/{id}", server.handleDuckMailDeleteAccount)
	mux.HandleFunc("GET /compat/duckmail/messages", server.handleDuckMailMessages)
	mux.HandleFunc("GET /compat/duckmail/messages/{id}", server.handleDuckMailMessage)
	mux.HandleFunc("PATCH /compat/duckmail/messages/{id}", server.handleDuckMailSeen)
	mux.HandleFunc("DELETE /compat/duckmail/messages/{id}", server.handleDuckMailDeleteMessage)
	mux.HandleFunc("GET /compat/duckmail/sources/{id}", server.handleDuckMailSource)
	mux.HandleFunc("GET /compat/yyds/v1/domains", server.handleYYDSDomains)
	mux.HandleFunc("POST /compat/yyds/v1/accounts", server.handleYYDSCreateAccount)
	mux.HandleFunc("POST /compat/yyds/v1/inboxes", server.handleYYDSCreateAccount)
	mux.HandleFunc("POST /compat/yyds/v1/token", server.handleYYDSRefreshToken)
	mux.HandleFunc("GET /compat/yyds/v1/accounts/me", server.handleYYDSAccountMe)
	mux.HandleFunc("GET /compat/yyds/v1/accounts/{id}", server.handleYYDSAccount)
	mux.HandleFunc("DELETE /compat/yyds/v1/accounts/{id}", server.handleYYDSAccount)
	mux.HandleFunc("GET /compat/yyds/v1/messages", server.handleYYDSMessages)
	mux.HandleFunc("GET /compat/yyds/v1/messages/{id}", server.handleYYDSMessage)
	mux.HandleFunc("PATCH /compat/yyds/v1/messages/{id}", server.handleYYDSMessage)
	mux.HandleFunc("DELETE /compat/yyds/v1/messages/{id}", server.handleYYDSMessage)
	mux.HandleFunc("GET /compat/yyds/v1/sources/{id}", server.handleYYDSSource)
	mux.HandleFunc("GET /compat/yyds/v1/messages/{id}/attachments/{part}", server.handleYYDSAttachment)
	mux.HandleFunc("GET /compat/cloudflare-temp/open_api/settings", server.handleCloudflareTempOpenSettings)
	mux.HandleFunc("GET /compat/cloudflare-temp/user_api/open_settings", server.handleCloudflareTempUserOpenSettings)
	mux.HandleFunc("POST /compat/cloudflare-temp/api/new_address", server.handleCloudflareTempCreateAddress)
	mux.HandleFunc("GET /compat/cloudflare-temp/api/settings", server.handleCloudflareTempSettings)
	mux.HandleFunc("GET /compat/cloudflare-temp/api/mails", server.handleCloudflareTempRawMails)
	mux.HandleFunc("GET /compat/cloudflare-temp/api/mail/{id}", server.handleCloudflareTempRawMail)
	mux.HandleFunc("GET /compat/cloudflare-temp/api/parsed_mails", server.handleCloudflareTempParsedMails)
	mux.HandleFunc("GET /compat/cloudflare-temp/api/parsed_mail/{id}", server.handleCloudflareTempParsedMail)
	mux.HandleFunc("DELETE /compat/cloudflare-temp/api/mails/{id}", server.handleCloudflareTempDeleteMail)
	mux.HandleFunc("DELETE /compat/cloudflare-temp/api/delete_address", server.handleCloudflareTempDeleteAddress)
	mux.HandleFunc("GET /open_api/settings", server.handleCloudflareTempOpenSettings)
	mux.HandleFunc("GET /user_api/open_settings", server.handleCloudflareTempUserOpenSettings)
	mux.HandleFunc("POST /api/new_address", server.handleCloudflareTempCreateAddress)
	mux.HandleFunc("GET /api/settings", server.handleCloudflareTempSettings)
	mux.HandleFunc("GET /api/mails", server.handleCloudflareTempRawMails)
	mux.HandleFunc("GET /api/mail/{id}", server.handleCloudflareTempRawMail)
	mux.HandleFunc("GET /api/parsed_mails", server.handleCloudflareTempParsedMails)
	mux.HandleFunc("GET /api/parsed_mail/{id}", server.handleCloudflareTempParsedMail)
	mux.HandleFunc("DELETE /api/mails/{id}", server.handleCloudflareTempDeleteMail)
	mux.HandleFunc("DELETE /api/delete_address", server.handleCloudflareTempDeleteAddress)

	server.httpServer = &http.Server{
		Addr:              cfg.Addr,
		Handler:           requestID(requestLog(logger, server.observeHTTP(recoverPanic(logger, mux)))),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
	return server
}

// SetReadinessChecker configures the required dependency check.
func (s *Server) SetReadinessChecker(checker ReadinessChecker) { s.readiness = checker }

// SetMailboxService enables the canonical business API routes.
func (s *Server) SetMailboxService(service MailboxService, authenticator CapabilityAuthenticator) {
	s.mailbox = service
	s.authenticator = authenticator
}

// SetBrowserSessions enables same-origin HttpOnly Cookie authentication.
func (s *Server) SetBrowserSessions(manager *auth.BrowserSessionManager) {
	s.browserSessions = manager
}

// SetDuckMailService enables the isolated DuckMail compatibility namespace.
func (s *Server) SetDuckMailService(service *duckmail.Service) { s.duckMail = service }

// SetYYDSService enables the isolated YYDS Mail compatibility namespace.
func (s *Server) SetYYDSService(service *yyds.Service) { s.yyds = service }

// SetCloudflareTempService enables the isolated Cloudflare Temp Email compatibility namespace.
func (s *Server) SetCloudflareTempService(service *cloudflaretemp.Service, legacyPaths bool) {
	s.cloudflareTemp = service
	s.cloudflareTempLegacyPaths = legacyPaths
}

// SetMetrics enables the internal Prometheus endpoint and request observer.
func (s *Server) SetMetrics(handler http.Handler, observer HTTPMetrics) {
	s.metricsHandler = handler
	s.metrics = observer
}

// SetReady changes the readiness state exposed to the service manager.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// ListenAndServe starts accepting HTTP requests.
func (s *Server) ListenAndServe() error {
	s.logger.Info("http server listening", "addr", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.SetReady(false)
	return s.httpServer.Shutdown(ctx)
}

// Close immediately closes all HTTP listeners and active connections.
func (s *Server) Close() error {
	s.SetReady(false)
	return s.httpServer.Close()
}

func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, request *http.Request) {
	if !s.ready.Load() {
		writeError(w, request, http.StatusServiceUnavailable, "service_unavailable", "service is not ready")
		return
	}
	if s.readiness != nil {
		ctx, cancel := context.WithTimeout(request.Context(), s.readinessTimeout)
		defer cancel()
		if err := s.readiness.Ready(ctx); err != nil {
			writeError(w, request, http.StatusServiceUnavailable, "service_unavailable", "service dependency is unavailable")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, request *http.Request) {
	if s.metricsHandler == nil {
		http.NotFound(w, request)
		return
	}
	s.metricsHandler.ServeHTTP(w, request)
}

func (s *Server) handleCreateInbox(w http.ResponseWriter, request *http.Request) {
	if s.mailbox == nil {
		writeError(w, request, http.StatusNotImplemented, "not_configured", "Inbox API is not configured")
		return
	}
	if !s.limiter.allow(s.clientIP(request)) {
		writeError(w, request, http.StatusTooManyRequests, "rate_limited", "too many Inbox creation requests")
		return
	}
	var input struct {
		Domain     string `json:"domain"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	request.Body = http.MaxBytesReader(w, request.Body, 4096)
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "request body must contain one JSON object")
		return
	}
	var lifetime time.Duration
	if input.TTLSeconds != 0 {
		if input.TTLSeconds < 0 || input.TTLSeconds > int64((365*24*time.Hour)/time.Second) {
			writeError(w, request, http.StatusBadRequest, "invalid_lifetime", "ttl_seconds is invalid")
			return
		}
		lifetime = time.Duration(input.TTLSeconds) * time.Second
	}
	created, err := s.mailbox.Create(request.Context(), mailbox.CreateRequest{Domain: input.Domain, Lifetime: lifetime})
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{
		"inbox":      created.Inbox,
		"capability": map[string]any{"token": created.Capability.Plaintext, "kid": created.Capability.KID, "scopes": created.Capability.Scopes.Names(), "expires_at": created.Capability.ExpiresAt},
	}})
}

func (s *Server) handleGetInbox(w http.ResponseWriter, request *http.Request) {
	principal, ok := s.requirePrincipal(w, request, auth.ScopeInboxRead)
	if !ok {
		return
	}
	inbox, err := s.mailbox.Get(request.Context(), principal.InboxID)
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": inbox})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, request *http.Request) {
	if s.mailbox == nil || s.authenticator == nil || s.browserSessions == nil {
		writeError(w, request, http.StatusNotImplemented, "not_configured", "browser sessions are not configured")
		return
	}
	principal, err := s.authenticateBearer(request)
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	session, err := s.browserSessions.Issue(request.Context(), principal)
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	inbox, err := s.mailbox.Get(request.Context(), principal.InboxID)
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	s.setSessionCookie(w, session)
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"inbox": inbox, "expires_at": session.ExpiresAt, "csrf_token": session.CSRFToken}})
}

func (s *Server) handleGetSession(w http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireBrowserPrincipal(w, request, false, auth.ScopeInboxRead)
	if !ok {
		return
	}
	inbox, err := s.mailbox.Get(request.Context(), principal.InboxID)
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	rotated, err := s.browserSessions.Issue(request.Context(), principal)
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	s.setSessionCookie(w, rotated)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"inbox": inbox, "expires_at": rotated.ExpiresAt, "csrf_token": rotated.CSRFToken}})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, request *http.Request) {
	if _, ok := s.requireBrowserPrincipal(w, request, true, auth.ScopeInboxRead); !ok {
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteInbox(w http.ResponseWriter, request *http.Request) {
	principal, ok := s.requirePrincipal(w, request, auth.ScopeInboxDelete)
	if !ok {
		return
	}
	if err := s.mailbox.Delete(request.Context(), principal.InboxID); err != nil {
		writeMappedError(w, request, err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListMessages(w http.ResponseWriter, request *http.Request) {
	principal, ok := s.requirePrincipal(w, request, auth.ScopeMessageRead)
	if !ok {
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, request, http.StatusBadRequest, "invalid_pagination", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	messages, err := s.mailbox.ListMessages(request.Context(), principal.InboxID, limit)
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": messages})
}

func (s *Server) handleGetMessage(w http.ResponseWriter, request *http.Request) {
	principal, ok := s.requirePrincipal(w, request, auth.ScopeMessageRead)
	if !ok {
		return
	}
	messageID, err := parseMessageID(request.PathValue("id"))
	if err != nil {
		writeMappedError(w, request, mailbox.ErrMessageNotFound)
		return
	}
	detail, err := s.mailbox.GetMessage(request.Context(), principal.InboxID, messageID)
	if err != nil {
		writeMappedError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": detail})
}

func (s *Server) handleGetAttachment(w http.ResponseWriter, request *http.Request) {
	principal, ok := s.requirePrincipal(w, request, auth.ScopeMessageRead)
	if !ok {
		return
	}
	messageID, err := parseMessageID(request.PathValue("id"))
	if err != nil {
		writeMappedError(w, request, mailbox.ErrMessageNotFound)
		return
	}
	source, err := s.mailbox.OpenAttachment(request.Context(), principal.InboxID, messageID, request.PathValue("part"))
	if err != nil {
		writeMappedError(w, request, err)
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
		s.logger.WarnContext(request.Context(), "attachment stream failed", "request_id", requestIDFromContext(request.Context()), "error", err)
	}
}

func (s *Server) handleDeleteMessage(w http.ResponseWriter, request *http.Request) {
	principal, ok := s.requirePrincipal(w, request, auth.ScopeMessageDelete)
	if !ok {
		return
	}
	messageID, err := parseMessageID(request.PathValue("id"))
	if err != nil {
		writeMappedError(w, request, mailbox.ErrMessageNotFound)
		return
	}
	if err := s.mailbox.DeleteMessage(request.Context(), principal.InboxID, messageID); err != nil {
		writeMappedError(w, request, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requirePrincipal(w http.ResponseWriter, request *http.Request, scopes ...auth.Scope) (auth.Principal, bool) {
	if s.mailbox == nil || s.authenticator == nil {
		writeError(w, request, http.StatusNotImplemented, "not_configured", "Inbox API is not configured")
		return auth.Principal{}, false
	}
	if strings.TrimSpace(request.Header.Get("Authorization")) != "" {
		principal, err := s.authenticateBearer(request, scopes...)
		if err != nil {
			writeMappedError(w, request, err)
			return auth.Principal{}, false
		}
		return principal, true
	}
	principal, ok := s.requireBrowserPrincipal(w, request, request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions, scopes...)
	return principal, ok
}

func (s *Server) authenticateBearer(request *http.Request, scopes ...auth.Scope) (auth.Principal, error) {
	header := request.Header.Get("Authorization")
	scheme, plaintext, found := strings.Cut(header, " ")
	plaintext = strings.TrimSpace(plaintext)
	if !found || !strings.EqualFold(scheme, "Bearer") || plaintext == "" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return s.authenticator.Authenticate(request.Context(), plaintext, scopes...)
}

func (s *Server) requireBrowserPrincipal(w http.ResponseWriter, request *http.Request, requireCSRF bool, scopes ...auth.Scope) (auth.Principal, bool) {
	if s.mailbox == nil || s.browserSessions == nil {
		writeError(w, request, http.StatusUnauthorized, "unauthenticated", "a MailWisp capability or browser session is required")
		return auth.Principal{}, false
	}
	cookie, err := request.Cookie("__Host-mailwisp_session")
	if err != nil || cookie.Value == "" {
		writeError(w, request, http.StatusUnauthorized, "unauthenticated", "a MailWisp capability or browser session is required")
		return auth.Principal{}, false
	}
	csrf := request.Header.Get("X-MailWisp-CSRF")
	principal, err := s.browserSessions.Authenticate(request.Context(), cookie.Value, csrf, requireCSRF, scopes...)
	if err != nil {
		writeMappedError(w, request, err)
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *Server) setSessionCookie(w http.ResponseWriter, session auth.BrowserSession) {
	maxAge := max(1, int(time.Until(session.ExpiresAt).Seconds()))
	http.SetCookie(w, &http.Cookie{Name: "__Host-mailwisp_session", Value: session.CookieValue, Path: "/", MaxAge: maxAge, Expires: session.ExpiresAt, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "__Host-mailwisp_session", Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func parseMessageID(raw string) (message.MessageID, error) {
	if _, err := uuid.Parse(raw); err != nil {
		return "", err
	}
	return message.MessageID(raw), nil
}

func (s *Server) clientIP(request *http.Request) string {
	remote, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		remote = request.RemoteAddr
	}
	ip := net.ParseIP(remote)
	if ip != nil {
		for _, network := range s.trustedProxies {
			if network.Contains(ip) {
				if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(forwarded) != nil {
					return forwarded
				}
			}
		}
	}
	return remote
}

func writeMappedError(w http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		writeError(w, request, http.StatusUnauthorized, "unauthenticated", "a MailWisp capability is invalid or expired")
	case errors.Is(err, auth.ErrForbidden):
		writeError(w, request, http.StatusForbidden, "forbidden", "the capability scope does not allow this action")
	case errors.Is(err, auth.ErrCSRF):
		writeError(w, request, http.StatusForbidden, "csrf_failed", "the browser CSRF proof is missing or invalid")
	case errors.Is(err, auth.ErrBrowserSessionDisabled):
		writeError(w, request, http.StatusNotImplemented, "not_configured", "browser sessions are not configured")
	case errors.Is(err, mailbox.ErrInboxNotFound), errors.Is(err, mailbox.ErrMessageNotFound):
		writeError(w, request, http.StatusNotFound, "not_found", "the requested resource was not found")
	case errors.Is(err, mailbox.ErrInvalidDomain), errors.Is(err, mailbox.ErrInvalidLifetime), errors.Is(err, mailbox.ErrInvalidLocalPart):
		writeError(w, request, http.StatusBadRequest, "invalid_request", "the Inbox creation request is invalid")
	case errors.Is(err, mailbox.ErrAddressConflict):
		writeError(w, request, http.StatusServiceUnavailable, "address_unavailable", "a unique Inbox address could not be allocated")
	default:
		writeError(w, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

type errorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, request *http.Request, status int, code, message string) {
	body := errorBody{}
	body.Error.Code = code
	body.Error.Message = message
	body.Error.RequestID = requestIDFromContext(request.Context())
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type requestIDKey struct{}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		tracked := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(tracked, r)
		logger.InfoContext(r.Context(), "http request", "request_id", requestIDFromContext(r.Context()), "method", r.Method, "path", r.URL.Path, "status", tracked.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) observeHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		tracked := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(tracked, r)
		if s.metrics != nil {
			s.metrics.ObserveHTTPRequest(r.Method, r.Pattern, tracked.status, time.Since(started))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(r.Context(), "panic recovered", "request_id", requestIDFromContext(r.Context()), "panic", recovered)
				writeError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type createLimiter struct {
	mu       sync.Mutex
	entries  map[string]bucket
	rate     float64
	capacity float64
	calls    uint64
}

type bucket struct {
	tokens  float64
	updated time.Time
}

func newCreateLimiter(perMinute, burst int) *createLimiter {
	return &createLimiter{entries: make(map[string]bucket), rate: float64(perMinute) / 60, capacity: float64(burst)}
}

func (l *createLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls%1024 == 0 {
		cutoff := now.Add(-10 * time.Minute)
		for candidate, entry := range l.entries {
			if entry.updated.Before(cutoff) {
				delete(l.entries, candidate)
			}
		}
	}
	state := l.entries[key]
	if state.updated.IsZero() {
		state = bucket{tokens: l.capacity, updated: now}
	}
	state.tokens = minFloat(l.capacity, state.tokens+now.Sub(state.updated).Seconds()*l.rate)
	state.updated = now
	if state.tokens < 1 {
		l.entries[key] = state
		return false
	}
	state.tokens--
	l.entries[key] = state
	return true
}

func minFloat(first, second float64) float64 {
	if first < second {
		return first
	}
	return second
}
