package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"m365-copilot2api/internal/auth"
	"m365-copilot2api/internal/chathub"
	"m365-copilot2api/internal/mcp"
	"m365-copilot2api/internal/outbound"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type pendingPKCE struct {
	Verifier    string
	Created     time.Time
	Status      string
	Account     any
	Error       string
	RedirectURI string
}

type pendingGraphAuthorization struct {
	Verifier     string
	Created      time.Time
	AdminSession string
	RedirectURI  string
}

func (s *Server) getRateLimitCooldown() time.Duration {
	secs := s.settings.get().RateLimitCooldownSeconds
	if secs < 5 {
		secs = 30
	}
	return time.Duration(secs) * time.Second
}

func (s *Server) featureFlags() chathub.FeatureFlags {
	cfg := s.settings.get()
	return chathub.FeatureFlags{
		MemoryV2:             cfg.EnableMemoryV2,
		DeepWork:             cfg.EnableDeepWork,
		ComputerUse:          cfg.EnableComputerUse,
		RealtimeVoice:        cfg.EnableRealtimeVoice,
		SystemPromptOverride: cfg.EnableSystemPromptOverride,
		DesignerImageGen4o:   cfg.EnableDesignerImageGen4o,
		CodeCanvas:           cfg.EnableCodeCanvas,
		SydneyReconnect:      cfg.EnableSydneyReconnect,
	}
}

const maxAccountProbe = 16
const rateLimitProbePrompt = "Reply with exactly: OK"

func (s *Server) logThrottlingWarning(accountID string, throttling any) {
	maxMsgs := s.settings.get().MaxConversationMessages
	if maxMsgs <= 0 {
		return
	}
	t, ok := throttling.(map[string]any)
	if !ok {
		return
	}
	num, ok := t["numUserMessagesInConversation"]
	if !ok {
		return
	}
	n, ok := num.(float64)
	if !ok {
		return
	}
	if n > float64(maxMsgs)*0.8 {
		log.Printf("[throttling-warning] account=%s messages=%.0f/%d approaching limit", accountID, n, maxMsgs)
	}
}

func shouldRotateConversation(historyLen, maxMessages int) bool {
	return maxMessages > 0 && historyLen >= maxMessages
}

func shouldRotateResolvedConversation(historyLen, requestLen, maxMessages int) bool {
	if requestLen > historyLen {
		historyLen = requestLen
	}
	return shouldRotateConversation(historyLen, maxMessages)
}

func (s *Server) markAccountResult(accountID string, err error) {
	if s == nil || s.accountPool == nil || accountID == "" {
		return
	}
	if err != nil {
		s.accountPool.MarkFailure(accountID, err, s.getRateLimitCooldown())
		return
	}
	s.accountPool.MarkSuccess(accountID)
}

func (s *Server) recordAccountChatResult(accountID string, result chathub.Result, err error) {
	s.markAccountResult(accountID, err)
	if err != nil || s == nil || s.accountPool == nil || accountID == "" {
		return
	}
	if result.Throttling != nil {
		s.accountPool.UpdateThrottling(accountID, result.Throttling)
		s.logThrottlingWarning(accountID, result.Throttling)
	}
	meterError := ""
	hasAccess := true
	hasMetering := false
	if result.MeteringInformation != nil {
		if raw, marshalErr := json.Marshal(result.MeteringInformation); marshalErr == nil {
			hasMetering = true
			meterError, hasAccess = ParseMetering(accountID, json.RawMessage(raw))
			applyMeteringCooldown(s.accountPool, accountID, meterError)
		}
	}
	remaining := remainingAllowances(result.Throttling)
	if hasMetering || len(remaining) > 0 {
		s.accountPool.UpdateMetering(accountID, meterError, hasAccess, remaining)
	}
}

// confirmRateLimitNotice verifies a text-channel rate-limit notice with a
// separate, fresh ChatHub conversation. A single notice is not enough to cool
// down an account because the upstream can occasionally emit a false positive.
func (s *Server) confirmRateLimitNotice(ctx context.Context, acc auth.AccountToken, noticeErr error) (bool, error) {
	if !errors.Is(noticeErr, chathub.ErrRateLimitNotice) {
		return IsRateLimited(noticeErr), noticeErr
	}

	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	probeSettings := s.settings.get()
	_, probeErr := s.chatWithAccount(probeCtx, acc.ID, chathub.Account{
		AccessToken: acc.AccessToken,
		OID:         acc.OID,
		TID:         acc.TID,
	}, chathub.Request{
		Text:         rateLimitProbePrompt,
		Tone:         "magic",
		Started:      true,
		LicenseType:  probeSettings.LicenseType,
		Scenario:     probeSettings.Scenario,
		FeatureFlags: s.featureFlags(),
	})
	if probeErr == nil {
		return false, nil
	}
	if errors.Is(probeErr, chathub.ErrRateLimitNotice) || IsRateLimited(probeErr) {
		return true, &UpstreamHTTPError{
			Status:     http.StatusTooManyRequests,
			RetryAfter: int(s.getRateLimitCooldown().Seconds()),
		}
	}
	return false, probeErr
}

type Server struct {
	mu                   sync.Mutex
	requestSlots         chan struct{}
	tokens               *auth.Store
	accountPool          *accountHealth
	accountConcurrency   *accountConcurrency
	pkce                 map[string]pendingPKCE
	graphAuthorizations  map[string]pendingGraphAuthorization
	chat                 *chathub.Client
	proxyClients         sync.Map
	sessions             *sessionStore
	userSessions         *userSessionStore
	sessionResolver      *sessionResolver
	conversationManager  *conversationManager
	adminPassword        string
	adminPasswordHistory []string
	adminSessions        map[string]time.Time
	mustChangePassword   bool
	loginAttempts        map[string]loginAttempt
	apiKeys              *apiKeyStore
	debug                *debugStore
	settings             *settingsStore
	responseMu           sync.Mutex
	responseMessages     map[string]map[string]*RespNode
	usage                *usageLog
	generatedImages      map[string]generatedImage
	convCache            *conversationCache
	cloudClients         *m365CloudClientManager
	lastHealthyAccount   string
}

const maxResponsesPerTenant = 256

func (s *Server) clientForProxy(proxyURL string) *chathub.Client {
	if proxyURL == "" {
		return s.chat
	}
	if v, ok := s.proxyClients.Load(proxyURL); ok {
		return v.(*chathub.Client)
	}
	clients, err := outbound.New(proxyURL)
	if err != nil {
		log.Printf("[bound-proxy] invalid proxy %q: %v", proxyURL, err)
		return s.chat
	}
	c := &chathub.Client{
		HTTPHeader: make(http.Header),
		HTTPClient: clients.HTTP,
		Dialer:     clients.WebSocket,
		Trace:      s.chat.Trace,
	}
	c.HTTPHeader.Set("Origin", "https://m365.cloud.microsoft")
	c.HTTPHeader.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0")
	actual, _ := s.proxyClients.LoadOrStore(proxyURL, c)
	return actual.(*chathub.Client)
}

type ToolCallRecord struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Type      string `json:"type,omitempty"`
}

type RespNode struct {
	At        time.Time                  `json:"at"`
	Messages  []oaiMsg                   `json:"messages"`
	ToolCalls map[string]*ToolCallRecord `json:"tool_calls,omitempty"`
	Version   int64                      `json:"version"`
	Consumed  bool                       `json:"consumed"`
	ParentID  string                     `json:"parent_id,omitempty"`
	Tenant    string                     `json:"tenant,omitempty"`
	SessionID string                     `json:"session_id,omitempty"`
}

// respHistory is kept as an alias so older code or tests referencing the old
// name continue to compile; the new canonical type is RespNode.
type respHistory = RespNode

func New() (*Server, error) {
	store, err := auth.OpenStore("")
	if err != nil {
		return nil, err
	}
	password, mustChange, err := loadAdminPassword()
	if err != nil {
		return nil, err
	}
	var history []string
	if data, ok := readPersistedAdminData(); ok {
		history = data.History
	}
	sessionTTL := 30 * time.Minute
	if v := os.Getenv("M365_USER_SESSION_TTL_MINUTES"); v != "" {
		if d, err := time.ParseDuration(v + "m"); err == nil {
			sessionTTL = d
		}
	}
	SetM365AuthStore(store)
	maxConcurrentRequests := 128
	if raw := strings.TrimSpace(os.Getenv("M365_MAX_CONCURRENT_REQUESTS")); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			maxConcurrentRequests = parsed
		}
	}
	return &Server{
		requestSlots:        make(chan struct{}, maxConcurrentRequests),
		tokens:              store,
		accountPool:         newAccountHealth(),
		accountConcurrency:  newAccountConcurrency(),
		pkce:                map[string]pendingPKCE{},
		graphAuthorizations: map[string]pendingGraphAuthorization{},
		chat: func() *chathub.Client {
			c := chathub.NewClient()
			c.Trace = func(meta map[string]any) { fmt.Printf("[multimodal-trace] %s\\n", mustJSON(meta)) }
			return c
		}(),
		sessions:             openSessionStore(),
		userSessions:         openUserSessionStore(sessionTTL),
		sessionResolver:      openSessionResolver(),
		conversationManager:  openConversationManager(),
		adminPassword:        password,
		adminPasswordHistory: history,
		adminSessions:        map[string]time.Time{},
		mustChangePassword:   mustChange,
		loginAttempts:        map[string]loginAttempt{},
		apiKeys:              openAPIKeys(),
		debug:                openDebugStore(),
		settings:             openSettingsStore(),
		responseMessages:     map[string]map[string]*RespNode{},
		usage:                openUsageLog(),
		generatedImages:      map[string]generatedImage{},
		convCache:            newConversationCache(),
		cloudClients:         newM365CloudClientManager(),
	}, nil
}

func init() { SetM365AuthStore(nil) }

func (s *Server) StartConvCacheGC() {
	go func() {
		for {
			time.Sleep(2 * time.Minute)
			s.convCache.GC()
		}
	}()
}

func (s *Server) PreheatPool() {
	if s.chat == nil || s.chat.Pool == nil {
		return
	}
	accounts := s.tokens.List()
	cfg := s.settings.get()
	for _, acc := range accounts {
		if acc.OID == "" || acc.TID == "" {
			oid, tid := extractOIDTID(acc.AccessToken)
			acc.OID, acc.TID = oid, tid
		}
		if acc.OID == "" {
			continue
		}
		for i := 0; i < 2; i++ {
			go func(a auth.AccountToken) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				reqID := uuid.NewString()
				sid := uuid.NewString()
				cid := uuid.NewString()
				wsURL, err := chathub.BuildWSURL(chathub.Account{AccessToken: a.AccessToken, OID: a.OID, TID: a.TID}, sid, cid, reqID, cfg.LicenseType, cfg.Scenario)
				if err != nil {
					return
				}
				s.chat.Pool.Warm(ctx, chathub.Account{AccessToken: a.AccessToken, OID: a.OID, TID: a.TID}, wsURL)
			}(acc)
		}
	}
}

func (s *Server) InitM365CloudClient() {
	accounts := s.tokens.List()
	if s.cloudClients == nil {
		s.cloudClients = newM365CloudClientManager()
	}
	for _, acc := range accounts {
		s.syncM365CloudClient(acc)
	}
}

func (s *Server) syncM365CloudClient(acc auth.AccountToken) {
	if s == nil {
		return
	}
	if s.cloudClients == nil {
		s.cloudClients = newM365CloudClientManager()
	}
	clientID := firstNonEmpty(os.Getenv("M365_CLIENT_ID"), acc.ClientID, auth.DefaultClientID)
	s.cloudClients.sync(acc.ID, clientID, acc.TID, acc.RefreshToken)
}

func (s *Server) RefreshExpiredTokens() {
	results := s.tokens.RefreshAllExpired()
	for _, r := range results {
		if r.Success {
			log.Printf("[token-refresh] account=%s refreshed, expires=%s", r.Email, r.ExpiresAt.Format(time.RFC3339))
		} else {
			log.Printf("[token-refresh] account=%s failed: %s", r.Email, r.Error)
		}
	}
}

func (s *Server) Routes() http.Handler {
	mcp.APIKeyValidator = s.validAPIKey
	m := http.NewServeMux()
	m.HandleFunc("/api/admin/login", s.adminLogin)
	m.HandleFunc("/api/admin/logout", s.adminLogout)
	m.HandleFunc("/api/admin/session", s.adminSession)
	m.HandleFunc("/api/admin/change-password", s.adminChangePassword)
	m.HandleFunc("/api/admin/keys", s.adminKeys)
	m.HandleFunc("/api/admin/models", s.adminModels)
	m.HandleFunc("/api/admin/models/test", s.adminModelTest)
	m.HandleFunc("/api/admin/models/sync", s.adminModelSync)
	m.HandleFunc("/api/admin/settings", s.adminSettings)
	m.HandleFunc("/api/admin/proxy-pool", s.proxyPool)
	m.HandleFunc("/api/admin/deployments", s.deployments)
	m.HandleFunc("/api/admin/deployment", s.deploymentAction)
	m.HandleFunc("/api/admin/deployment/check", s.deploymentCheck)
	m.HandleFunc("/api/admin/debug/logs", s.debugList)
	m.HandleFunc("/api/admin/debug/detail", s.debugDetail)
	m.HandleFunc("/api/admin/syslogs", s.handleSysLogs)
	m.HandleFunc("/api/admin/syslogs/clear", s.handleSysLogsClear)
	// Microsoft Graph batch user creation is temporarily disabled and will be restored in a later version.
	// Keep the implementation files intact, but do not register readiness, authorization, or batch routes.
	m.HandleFunc("/api/health", s.health)
	m.HandleFunc("/api/version", s.version)
	m.HandleFunc("/api/update", s.update)
	m.HandleFunc("/api/accounts", s.accounts)
	m.HandleFunc("/api/accounts/refresh", s.refreshAccount)
	m.HandleFunc("/api/accounts/schedule", s.scheduleAccount)
	m.HandleFunc("/api/accounts/token-health", s.tokenHealth)
	m.HandleFunc("/api/accounts/clear-cooldown", s.clearCooldown)
	m.HandleFunc("/api/accounts/delete", s.deleteAccount)
	m.HandleFunc("/api/accounts/provision", s.provisionAccount)
	m.HandleFunc("/api/accounts/bind-proxy", s.bindProxy)
	m.HandleFunc("/api/auth/start", s.startPKCE)
	m.HandleFunc("/api/auth/status", s.pkceStatus)
	m.HandleFunc("/api/auth/callback", s.callbackPKCE)
	m.HandleFunc("/api/chat", s.chatOnce)
	m.HandleFunc("/api/chat/stream", s.chatStream)
	m.HandleFunc("/api/conversations", s.conversations)
	m.HandleFunc("/api/conversations/delete", s.deleteConversation)
	m.HandleFunc("/api/conversations/cleanup", s.conversationCleanup)
	m.HandleFunc("/api/conversations/whitelist", s.conversationWhitelist)
	m.HandleFunc("/v1/sessions", s.handleSessions)
	m.HandleFunc("/v1/sessions/", s.handleSessionDelete)
	m.HandleFunc("/api/m365/conversations", s.handleM365Conversations)
	m.HandleFunc("/api/m365/conversations/detail", s.handleM365ConversationDetail)
	m.HandleFunc("/api/m365/conversations/delete", s.handleM365Delete)
	m.HandleFunc("/api/m365/conversations/cleanup", s.handleM365Cleanup)
	m.HandleFunc("/api/m365/conversations/cleanup-all", s.handleM365CleanupAll)
	m.HandleFunc("/api/stats", s.handleCacheStats)
	m.HandleFunc("/api/stats/reset", s.handleCacheStatsReset)
	m.HandleFunc("/api/usage", s.adminUsage)
	m.HandleFunc("/api/usage/logs", s.adminUsageLogs)
	m.HandleFunc("/api/plugins", s.plugins)
	m.HandleFunc("/v1/models", s.openaiModels)
	m.HandleFunc("/v1/chat/completions", s.openaiChat)
	m.HandleFunc("/v1/responses", s.responsesDisabled)
	m.HandleFunc("/responses", s.responsesDisabled)
	m.HandleFunc("/v1/mcp/sse", mcp.HandleSSE)
	m.HandleFunc("/v1/mcp/message", mcp.HandleMessage)
	m.HandleFunc("/v1/mcp/tools", mcp.HandleToolsList)
	m.HandleFunc("/v1/messages", s.anthropicMessages)
	m.HandleFunc("/v1/images/generations", s.imageGenerations)
	m.HandleFunc("/v1/images/edits", s.imageEdits)
	m.HandleFunc("/v1/images/files/", s.generatedImageFile)
	m.HandleFunc("/v1/memory/flags", s.handleMemoryFlags)
	m.HandleFunc("/v1/memory/instructions", s.handleMemoryInstructions)
	m.HandleFunc("/v1/memory/instructions/", s.handleMemoryInstructionsID)
	m.HandleFunc("/v1/memory/settings", s.handleMemorySettings)
	m.HandleFunc("/", s.rootPage)
	return recoverPanics(requestID(httpTrace(securityHeaders(s.limitConcurrency(s.adminMiddleware(s.debugMiddleware(m)))))))
}

func (s *Server) limitConcurrency(next http.Handler) http.Handler {
	if s.requestSlots == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.requestSlots <- struct{}{}:
			defer func() { <-s.requestSlots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			writeOpenAIError(w, http.StatusServiceUnavailable, "server_overloaded", "server is overloaded; retry shortly")
		}
	})
}

func (s *Server) adminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/images/files/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/admin/login" || r.URL.Path == "/api/admin/session" || r.URL.Path == "/api/admin/change-password" || r.URL.Path == "/api/admin/logout" || r.URL.Path == "/api/auth/start" || r.URL.Path == "/api/auth/status" || r.URL.Path == "/api/auth/callback" || r.URL.Path == "/" || r.URL.Path == "/login" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/responses" {
			if !s.validAPIKey(r) {
				writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "valid API key required")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if s.adminPassword == "" {
			writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "administrator password is not configured")
			return
		}
		if !s.validAdminSession(r) {
			writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "administrator login required")
			return
		}
		s.mu.Lock()
		mustChange := s.mustChangePassword
		s.mu.Unlock()
		if mustChange && r.URL.Path != "/api/admin/change-password" && r.URL.Path != "/api/admin/logout" {
			writeOpenAIError(w, http.StatusForbidden, "password_change_required", "administrator password must be changed before using the console")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func secureAdminCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// Only trust X-Forwarded-Proto from a loopback reverse proxy.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) validAdminSession(r *http.Request) bool {
	c, err := r.Cookie("m365_admin_session")
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.adminSessions[c.Value]
	if !ok || time.Now().After(expires) {
		delete(s.adminSessions, c.Value)
		return false
	}
	return true
}

const maxAdminSessions = 4096

// pruneAdminSessions drops expired entries; callers must hold s.mu.
func pruneAdminSessions(m map[string]time.Time, now time.Time) {
	for k, exp := range m {
		if now.After(exp) {
			delete(m, k)
		}
	}
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	ip, now := clientIP(r), time.Now()
	if ok, wait := s.loginAllowed(ip, now); !ok {
		seconds := int(wait.Seconds()) + 1
		w.Header().Set("Retry-After", fmt.Sprint(seconds))
		auditLog(r, "admin_login_locked", fmt.Sprintf("locked wait=%ds", seconds))
		writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_error", "admin_login_locked")
		return
	}
	var body struct {
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	decodeErr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	s.mu.Lock()
	passwordHash := s.adminPassword
	mustChange := s.mustChangePassword
	s.mu.Unlock()
	if decodeErr != nil || body.Password == "" || !checkPassword(passwordHash, body.Password) {
		s.recordLoginFailure(ip, now)
		auditLog(r, "admin_login_failed", "invalid password")
		writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "invalid_administrator_password")
		return
	}
	s.clearLoginFailures(ip)
	auditLog(r, "admin_login_success", "")
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		writeOpenAIError(w, 500, "internal_error", "session failure")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	pruneAdminSessions(s.adminSessions, now)
	if len(s.adminSessions) >= maxAdminSessions {
		// Evict the oldest entry to keep the map bounded.
		var oldest string
		var oldestExp time.Time
		for k, exp := range s.adminSessions {
			if oldest == "" || exp.Before(oldestExp) {
				oldest, oldestExp = k, exp
			}
		}
		delete(s.adminSessions, oldest)
	}
	ttl := 24 * time.Hour
	maxAge := 86400
	if body.Remember {
		ttl = 30 * 24 * time.Hour
		maxAge = 30 * 86400
	}
	s.adminSessions[token] = now.Add(ttl)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "m365_admin_session", Value: token, Path: "/", HttpOnly: true, Secure: secureAdminCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
	jsonOut(w, map[string]any{"status": "authenticated", "must_change_password": mustChange})
}
func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("m365_admin_session"); e == nil {
		s.mu.Lock()
		delete(s.adminSessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "m365_admin_session", Path: "/", HttpOnly: true, Secure: secureAdminCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	jsonOut(w, map[string]string{"status": "logged_out"})
}
func (s *Server) adminSession(w http.ResponseWriter, r *http.Request) {
	authenticated := s.validAdminSession(r)
	s.mu.Lock()
	mustChange := s.mustChangePassword
	s.mu.Unlock()
	jsonOut(w, map[string]bool{"authenticated": authenticated, "must_change_password": authenticated && mustChange})
}

func (s *Server) adminKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonOut(w, map[string]any{"keys": s.apiKeys.list()})
	case http.MethodPost:
		var b struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(r.Body).Decode(&b) != nil {
			writeOpenAIError(w, 400, "invalid_request_error", "bad json")
			return
		}
		if strings.TrimSpace(b.Name) == "" {
			b.Name = "API key"
		}
		rec, raw, e := s.apiKeys.create(b.Name)
		if e != nil {
			writeOpenAIError(w, 500, "internal_error", e.Error())
			return
		}
		jsonOut(w, map[string]any{"key": raw, "record": rec})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		deleted, e := s.apiKeys.delete(id)
		if e != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "internal_error", e.Error())
			return
		}
		if !deleted {
			writeOpenAIError(w, 404, "not_found", "key not found")
			return
		}
		jsonOut(w, map[string]string{"status": "deleted"})
	case http.MethodPut:
		var b struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Revoked    *bool    `json:"revoked"`
			AccountIDs []string `json:"accountIds"`
		}
		if json.NewDecoder(r.Body).Decode(&b) != nil || b.ID == "" {
			writeOpenAIError(w, 400, "invalid_request_error", "bad json")
			return
		}
		updated, e := s.apiKeys.update(b.ID, b.Name, b.Revoked, b.AccountIDs)
		if e != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "internal_error", e.Error())
			return
		}
		if !updated {
			writeOpenAIError(w, 404, "not_found", "key not found")
			return
		}
		jsonOut(w, map[string]string{"status": "updated"})
	default:
		writeOpenAIError(w, 405, "invalid_request_error", "method not allowed")
	}
}

// rawAPIKey returns the full API key presented by the caller (X-API-Key or
// Authorization: Bearer), or "" when none is present. Unlike extractAPIKey it
// does not truncate: callers that use the key as a tenant/isolation identity
// need the complete secret so distinct keys never collide on a shared prefix.
func rawAPIKey(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if raw == "" {
		v := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			raw = strings.TrimSpace(v[7:])
		}
	}
	return raw
}

func (s *Server) validAPIKey(r *http.Request) bool {
	raw := rawAPIKey(r)
	return raw != "" && s.apiKeys.valid(raw)
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[jsonOut] encode error: %v", err)
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	list := s.tokens.List()
	throttlingSummary := map[string]any{}
	if s.accountPool != nil {
		for _, a := range list {
			if t := s.accountPool.GetThrottling(a.ID); t != nil {
				throttlingSummary[a.ID] = t
			}
		}
	}
	jsonOut(w, map[string]any{
		"status":             "ok",
		"auth":               []string{"pkce"},
		"chat":               "chathub",
		"clientId":           auth.ClientID(),
		"scope":              auth.Scope(),
		"tokenCache":         s.tokens.Path(),
		"accountCount":       len(list),
		"accountConcurrency": s.accountConcurrency.Snapshot(),
		"throttling":         throttlingSummary,
	})
}

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	list := s.tokens.List()
	type view struct {
		ID                 string         `json:"id"`
		Email              string         `json:"email"`
		DisplayName        string         `json:"displayName,omitempty"`
		Status             string         `json:"status"`
		ScheduleEnabled    bool           `json:"scheduleEnabled"`
		CallCount          uint64         `json:"callCount"`
		RateLimited        bool           `json:"rateLimited"`
		ImageLimited       bool           `json:"imageLimited"`
		AuthFailed         bool           `json:"authFailed"`
		AuthFailReason     string         `json:"authFailReason,omitempty"`
		CooldownUntil      *time.Time     `json:"cooldownUntil,omitempty"`
		Throttling         any            `json:"throttling,omitempty"`
		MeterError         string         `json:"meterError,omitempty"`
		MeterHasAccess     bool           `json:"meterHasAccess"`
		RemainingAllowance map[string]int `json:"remainingAllowance,omitempty"`
		Capabilities       map[string]any `json:"capabilities"`
		Concurrency        int            `json:"concurrency"`
		OID                string         `json:"oid,omitempty"`
		TID                string         `json:"tid,omitempty"`
		ExpiresAt          time.Time      `json:"expiresAt,omitempty"`
		UpdatedAt          time.Time      `json:"updatedAt,omitempty"`
		BoundProxy         string         `json:"boundProxy,omitempty"`
	}
	out := make([]view, 0, len(list))
	for _, a := range list {
		status := a.Status
		var cooldownUntil *time.Time
		var callCount uint64
		var rateLimited bool
		var throttling any
		var meterError string
		var meterHasAccess = true
		var remainingAllowance map[string]int
		var authFailReason string
		var imageLimited bool
		if s.accountPool != nil {
			if until, ok := s.accountPool.CooldownUntil(a.ID); ok {
				status = "cooldown"
				cooldownUntil = &until
			}
			callCount = s.accountPool.CallCount(a.ID)
			rateLimited = s.accountPool.RateLimited(a.ID)
			throttling = s.accountPool.GetThrottling(a.ID)
			meterError, meterHasAccess, remainingAllowance = s.accountPool.GetMetering(a.ID)
			authFailReason = s.accountPool.AuthFailReason(a.ID)
			imageLimited = s.accountPool.ImageLimited(a.ID)
		}
		concurrency := s.accountConcurrency.Inflight(a.ID)
		capabilities := map[string]any{
			"chat":             a.OID != "" && a.TID != "",
			"tokenRefresh":     a.RefreshToken != "",
			"scheduling":       true,
			"meteringObserved": len(remainingAllowance) > 0 || meterError != "",
			"imageApiVerified": false,
			"source":           "local_observation",
		}
		out = append(out, view{
			ID: a.ID, Email: a.Email, DisplayName: a.DisplayName,
			Status: status, ScheduleEnabled: !a.ScheduleDisabled, CallCount: callCount, RateLimited: rateLimited,
			ImageLimited:   imageLimited,
			AuthFailed:     s.accountPool != nil && !s.accountPool.Available(a.ID) && authFailReason != "",
			AuthFailReason: authFailReason,
			CooldownUntil:  cooldownUntil, Throttling: throttling,
			MeterError: meterError, MeterHasAccess: meterHasAccess, RemainingAllowance: remainingAllowance,
			Capabilities: capabilities,
			Concurrency:  concurrency,
			OID:          a.OID, TID: a.TID,
			ExpiresAt: a.ExpiresAt, UpdatedAt: a.UpdatedAt, BoundProxy: a.BoundProxy,
		})
	}
	jsonOut(w, map[string]any{"accounts": out, "health": s.accountPool.Snapshot()})
}

func (s *Server) refreshAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
		return
	}
	acc, err := s.tokens.EnsureValid(strings.TrimSpace(body.ID))
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "token_refresh_error", err.Error())
		return
	}
	s.syncM365CloudClient(acc)
	jsonOut(w, map[string]any{"status": "refreshed", "account": map[string]any{
		"id": acc.ID, "email": acc.Email, "displayName": acc.DisplayName,
		"status": acc.Status, "expiresAt": acc.ExpiresAt, "updatedAt": acc.UpdatedAt,
	}})
}

func (s *Server) scheduleAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body struct {
		ID      string   `json:"id"`
		IDs     []string `json:"ids"`
		Enabled bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
		return
	}
	ids := body.IDs
	if id := strings.TrimSpace(body.ID); id != "" {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "id or ids required")
		return
	}
	cleanIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			cleanIDs = append(cleanIDs, id)
		}
	}
	if len(cleanIDs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "id or ids required")
		return
	}
	if err := s.tokens.SetScheduleEnabledBatch(cleanIDs, body.Enabled); err != nil {
		writeOpenAIError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	jsonOut(w, map[string]any{"status": "updated", "updated": len(cleanIDs), "scheduleEnabled": body.Enabled})
}

func (s *Server) tokenHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		results := s.tokens.RefreshAllExpired()
		refreshed, failed := 0, 0
		for _, r := range results {
			if r.Success {
				refreshed++
			} else {
				failed++
			}
		}
		jsonOut(w, map[string]any{"refreshed": refreshed, "failed": failed, "results": results})
		return
	}
	list := s.tokens.List()
	now := time.Now()
	type entry struct {
		ID        string    `json:"id"`
		Email     string    `json:"email"`
		Status    string    `json:"status"`
		ExpiresAt time.Time `json:"expires_at"`
		Expired   bool      `json:"expired"`
		ExpiresIn string    `json:"expires_in"`
	}
	out := make([]entry, 0, len(list))
	for _, a := range list {
		e := entry{ID: a.ID, Email: a.Email, Status: a.Status, ExpiresAt: a.ExpiresAt}
		if now.After(a.ExpiresAt) {
			e.Expired = true
			e.ExpiresIn = "expired"
		} else {
			e.ExpiresIn = a.ExpiresAt.Sub(now).Truncate(time.Second).String()
		}
		out = append(out, e)
	}
	jsonOut(w, map[string]any{"accounts": out, "now": now.Format(time.RFC3339)})
}

func (s *Server) clearCooldown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	s.accountPool.ClearAllCooldowns()
	jsonOut(w, map[string]any{"status": "ok"})
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
		return
	}
	if err := s.tokens.Delete(body.ID); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	s.cloudClients.remove(body.ID)
	jsonOut(w, map[string]string{"status": "deleted"})
}

func (s *Server) provisionAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	if !s.validAdminSession(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "administrator login required")
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" || body.Password == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "email and password required")
		return
	}
	set, err := auth.ROPC(body.Email, body.Password)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "ropc_error", err.Error())
		return
	}
	acc, err := s.tokens.Upsert(set)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "upsert_error", err.Error())
		return
	}
	s.syncM365CloudClient(acc)
	jsonOut(w, map[string]any{"status": "provisioned", "account": map[string]any{
		"id": acc.ID, "email": acc.Email, "displayName": acc.DisplayName,
		"status": acc.Status, "expiresAt": acc.ExpiresAt,
	}})
}

func (s *Server) bindProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	if !s.validAdminSession(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "administrator login required")
		return
	}
	var body struct {
		ID       string `json:"id"`
		ProxyURL string `json:"proxyUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "id required")
		return
	}
	if body.ProxyURL != "" {
		if err := outbound.ValidateProxyURL(body.ProxyURL); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_proxy", err.Error())
			return
		}
	}
	if err := s.tokens.SetBoundProxy(body.ID, body.ProxyURL); err != nil {
		writeOpenAIError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	acc, _ := s.tokens.Get(body.ID)
	if acc.BoundProxy == "" {
		s.proxyClients.Range(func(key, _ any) bool {
			if keyStr, ok := key.(string); ok && keyStr != "" {
				s.proxyClients.Delete(keyStr)
			}
			return true
		})
	}
	jsonOut(w, map[string]any{"ok": true, "id": body.ID, "boundProxy": acc.BoundProxy})
}

func (s *Server) startPKCE(w http.ResponseWriter, _ *http.Request) {
	v, err := auth.Verifier()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "pkce failure")
		return
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "state failure")
		return
	}
	state := hex.EncodeToString(b)
	redirectURI := auth.RedirectURI()
	s.mu.Lock()
	s.pkce[state] = pendingPKCE{Verifier: v, Created: time.Now(), Status: "pending", RedirectURI: redirectURI}
	s.mu.Unlock()
	jsonOut(w, map[string]string{
		"status": "pkce_ready",
		"state":  state,
		"url": auth.AuthorizationURL(
			auth.AuthorizeEndpoint(),
			auth.ClientID(),
			redirectURI,
			state,
			auth.Challenge(v),
			auth.Scope(),
		),
		"redirectUri": redirectURI,
		"note":        "If redirect is nativeclient, paste the final URL/code into /api/auth/callback after login.",
	})
}

func (s *Server) pkceStatus(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "missing state")
		return
	}
	s.mu.Lock()
	p, ok := s.pkce[state]
	if ok && time.Since(p.Created) > 10*time.Minute {
		delete(s.pkce, state)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		jsonOut(w, map[string]any{"status": "expired"})
		return
	}
	out := map[string]any{"status": p.Status}
	if p.Account != nil {
		out["account"] = p.Account
	}
	if p.Error != "" {
		out["error"] = p.Error
	}
	jsonOut(w, out)
}

func (s *Server) callbackPKCE(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	oauthError := r.URL.Query().Get("error")
	// also accept pasted full callback URL
	if code == "" && oauthError == "" {
		if u := r.URL.Query().Get("url"); u != "" {
			if parsed, err := http.NewRequest(http.MethodGet, u, nil); err == nil {
				code = parsed.URL.Query().Get("code")
				oauthError = parsed.URL.Query().Get("error")
				if state == "" {
					state = parsed.URL.Query().Get("state")
				}
			}
		}
	}
	if state == "" || (code == "" && oauthError == "") {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "missing state or authorization result")
		return
	}
	s.mu.Lock()
	p, ok := s.pkce[state]
	if !ok || time.Since(p.Created) > 10*time.Minute {
		if ok {
			delete(s.pkce, state)
		}
		s.mu.Unlock()
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid or expired state")
		return
	}
	if p.Status != "pending" {
		s.mu.Unlock()
		writeOpenAIError(w, http.StatusConflict, "invalid_request_error", "authorization result already consumed")
		return
	}
	p.Status = "processing"
	s.pkce[state] = p
	s.mu.Unlock()
	if oauthError != "" {
		log.Printf("oauth_error stage=callback error=%q", oauthError)
		s.mu.Lock()
		p.Status = "error"
		p.Error = oauthError
		s.pkce[state] = p
		s.mu.Unlock()
		writeOpenAIError(w, http.StatusBadRequest, "auth_error", "Microsoft authorization failed: "+oauthError)
		return
	}
	redirectURI := p.RedirectURI
	if redirectURI == "" {
		redirectURI = auth.RedirectURI()
	}
	tok, err := auth.ExchangeCode(code, p.Verifier, redirectURI)
	if err != nil {
		logOAuthError("code_exchange", err)
		s.mu.Lock()
		p.Status = "error"
		p.Error = err.Error()
		s.pkce[state] = p
		s.mu.Unlock()
		writeOpenAIError(w, http.StatusBadRequest, "auth_error", err.Error())
		return
	}
	acc, err := s.tokens.Upsert(tok)
	if err != nil {
		s.mu.Lock()
		p.Status = "error"
		p.Error = err.Error()
		s.pkce[state] = p
		s.mu.Unlock()
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	s.syncM365CloudClient(acc)
	memoryDisabled := false
	memoryError := ""
	memoryCtx, cancelMemory := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelMemory()
	if _, err := disableAndVerifyMemory(memoryCtx, acc); err != nil {
		memoryError = err.Error()
		log.Printf("[personalization] account=%s disable memory failed: %v", acc.Email, err)
	} else {
		memoryDisabled = true
		log.Printf("[personalization] account=%s saved memory and history insights disabled", acc.Email)
	}
	s.mu.Lock()
	p.Status = "authenticated"
	p.Account = map[string]any{"id": acc.ID, "email": acc.Email, "displayName": acc.DisplayName, "status": acc.Status, "oid": acc.OID, "tid": acc.TID, "memoryDisabled": memoryDisabled, "memoryError": memoryError}
	s.pkce[state] = p
	s.mu.Unlock()
	// Browser loopback callbacks should finish in a friendly page instead of
	// displaying a raw JSON response. Keep JSON for the manual/API flow.
	if strings.HasPrefix(redirectURI, "http://127.0.0.1:") || strings.HasPrefix(redirectURI, "http://localhost:") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>M365 Copilot2API 授权完成</title><style>body{font:16px system-ui;text-align:center;padding:15vh 20px;color:#242424}main{max-width:520px;margin:auto}h1{font-size:26px}</style><main><h1>授权完成</h1><p>账号已经自动加入账号池，可以关闭此页面。</p><script>if(window.opener){window.opener.postMessage({type:"m365-auth-complete"},window.location.origin);setTimeout(()=>window.close(),300)}</script></main>`)
		return
	}
	jsonOut(w, map[string]any{
		"status":          "authenticated",
		"memory_disabled": memoryDisabled,
		"memory_error":    memoryError,
		"account":         map[string]any{"id": acc.ID, "email": acc.Email, "displayName": acc.DisplayName, "status": acc.Status, "oid": acc.OID, "tid": acc.TID},
	})
}

func (s *Server) resolveAccount(accountID string) (auth.AccountToken, error) {
	if accountID == "" {
		// Failover mode: prefer the last healthy account, only rotate on failure
		s.mu.Lock()
		preferred := s.lastHealthyAccount
		s.mu.Unlock()
		if preferred != "" && s.accountAvailable(preferred) {
			if acc, err := s.tokens.EnsureValid(preferred); err == nil {
				accountID = preferred
				return acc, nil
			} else {
				s.accountPool.ReleaseProbe(preferred)
			}
		}
		// No preferred account or it's unavailable; fall back to round-robin
		acc, ok := s.tokens.Next()
		if !ok {
			return auth.AccountToken{}, fmt.Errorf("no accounts; login first")
		}
		accountID = acc.ID
		selected := s.accountAvailable(accountID)
		for i, count := 0, len(s.tokens.List()); !selected && i < count; i++ {
			acc, ok = s.tokens.Next()
			if !ok {
				break
			}
			accountID = acc.ID
			selected = s.accountAvailable(accountID)
		}
		if !s.tokens.ScheduleEnabled(accountID) {
			return auth.AccountToken{}, fmt.Errorf("no accounts enabled for scheduling")
		}
		if !selected {
			until := s.accountPool.EarliestRecovery()
			retry := int(time.Until(until).Seconds())
			if retry < 5 {
				retry = 5
			}
			return auth.AccountToken{}, &UpstreamHTTPError{Status: 429, RetryAfter: retry, Body: "all accounts are cooling down; try again later"}
		}
	}
	result, err := s.tokens.EnsureValid(accountID)
	if err != nil && s.accountPool != nil {
		s.accountPool.ReleaseProbe(accountID)
	}
	if err == nil {
		s.mu.Lock()
		s.lastHealthyAccount = accountID
		s.mu.Unlock()
	}
	return result, err
}

func (s *Server) resolveBoundAccount(accountIDs []string, accountID string) (auth.AccountToken, error) {
	allowed := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = struct{}{}
		}
	}
	if accountID != "" {
		if _, ok := allowed[accountID]; !ok {
			return auth.AccountToken{}, ErrAccountNotBound
		}
		return s.resolveAccount(accountID)
	}
	for i, count := 0, len(s.tokens.List()); i < count; i++ {
		acc, ok := s.tokens.Next()
		if !ok {
			break
		}
		if _, ok := allowed[acc.ID]; !ok || !s.tokens.ScheduleEnabled(acc.ID) || !s.accountAvailable(acc.ID) {
			continue
		}
		result, err := s.tokens.EnsureValid(acc.ID)
		if err != nil {
			if s.accountPool != nil {
				s.accountPool.ReleaseProbe(acc.ID)
			}
			continue
		}
		return result, nil
	}
	return auth.AccountToken{}, fmt.Errorf("no bound account available")
}

// nextHealthyAccount returns the next round-robin account that is still
// healthy, skipping the given id first, and validates its token. Used by the
// failover path after a rate-limited or auth-failed attempt.
func (s *Server) nextHealthyAccount(avoidID string, accountIDs []string) (auth.AccountToken, error) {
	allowed := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = struct{}{}
		}
	}
	for i, count := 0, len(s.tokens.List()); i < count; i++ {
		acc, ok := s.tokens.Next()
		if !ok {
			return auth.AccountToken{}, fmt.Errorf("no accounts; login first")
		}
		if avoidID != "" && acc.ID == avoidID {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[acc.ID]; !ok {
				continue
			}
		}
		if !s.tokens.ScheduleEnabled(acc.ID) || !s.accountAvailable(acc.ID) {
			continue
		}
		result, err := s.tokens.EnsureValid(acc.ID)
		if err != nil {
			if s.accountPool != nil {
				s.accountPool.ReleaseProbe(acc.ID)
			}
			continue
		}
		return result, nil
	}
	if len(allowed) > 0 {
		return auth.AccountToken{}, fmt.Errorf("no bound account available")
	}
	return auth.AccountToken{}, fmt.Errorf("no healthy account available for failover")
}

func (s *Server) resolveImageAccount(accountIDs []string, accountID string) (auth.AccountToken, error) {
	if accountID != "" {
		return s.resolveAccount(accountID)
	}
	allowed := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = struct{}{}
		}
	}
	for i := 0; i < maxAccountProbe; i++ {
		acc, ok := s.tokens.Next()
		if !ok {
			return auth.AccountToken{}, fmt.Errorf("no accounts; login first")
		}
		if len(allowed) > 0 {
			if _, ok := allowed[acc.ID]; !ok {
				continue
			}
		}
		if s.accountAvailable(acc.ID) && (s.accountPool == nil || s.accountPool.ImageGenAvailable(acc.ID)) {
			result, err := s.tokens.EnsureValid(acc.ID)
			if err == nil {
				s.mu.Lock()
				s.lastHealthyAccount = acc.ID
				s.mu.Unlock()
				return result, nil
			}
		}
	}
	return auth.AccountToken{}, &UpstreamHTTPError{Status: 429, RetryAfter: 60, Body: "all accounts are cooling down or image quota exhausted; try again later"}
}

func (s *Server) nextHealthyImageAccount(avoidID string, accountIDs []string) (auth.AccountToken, error) {
	allowed := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = struct{}{}
		}
	}
	for i := 0; i < maxAccountProbe; i++ {
		acc, ok := s.tokens.Next()
		if !ok {
			return auth.AccountToken{}, fmt.Errorf("no accounts; login first")
		}
		if avoidID != "" && acc.ID == avoidID {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[acc.ID]; !ok {
				continue
			}
		}
		if !s.tokens.ScheduleEnabled(acc.ID) || !s.accountAvailable(acc.ID) || (s.accountPool != nil && !s.accountPool.ImageGenAvailable(acc.ID)) {
			continue
		}
		result, err := s.tokens.EnsureValid(acc.ID)
		if err != nil {
			if s.accountPool != nil {
				s.accountPool.ReleaseProbe(acc.ID)
			}
			continue
		}
		return result, nil
	}
	if len(allowed) > 0 {
		return auth.AccountToken{}, fmt.Errorf("no bound image account available")
	}
	return auth.AccountToken{}, fmt.Errorf("no healthy image account available for failover")
}

type chatBody struct {
	AccountID             string                   `json:"accountId"`
	Message               string                   `json:"message"`
	Prompt                string                   `json:"prompt"`
	Tone                  string                   `json:"tone"`
	ConversationID        string                   `json:"conversationId"`
	SessionID             string                   `json:"sessionId"`
	SessionKey            string                   `json:"sessionKey"`
	ConversationSignature string                   `json:"conversationSignature"`
	Attachments           []chathub.Attachment     `json:"attachments,omitempty"`
	PreviousMessages      []chathub.ContextMessage `json:"previousMessages,omitempty"`
	ConnectedFederatedIDs []string                 `json:"connectedFederatedIds,omitempty"`
	Tools                 []chathub.Tool           `json:"tools,omitempty"`
	// Legacy OpenAI-compatible clients still send functions/function_call.
	Functions       []json.RawMessage `json:"functions,omitempty"`
	ToolChoice      any               `json:"tool_choice,omitempty"`
	FunctionCall    any               `json:"function_call,omitempty"`
	Reasoning       *reasoningConfig  `json:"reasoning,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	ResponseFormat  *responseFormat   `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type       string         `json:"type"`
	JSONSchema map[string]any `json:"json_schema,omitempty"`
}

func isImageModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-image-2", "image-2", "dall-e-3", "dall-e-2", "designer":
		return true
	default:
		return false
	}
}

func modelTone(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-image-2", "image-2", "dall-e-3", "dall-e-2", "designer":
		return "Magic"
	case "gpt-5.2":
		return "Gpt_5_2_Chat"
	case "gpt-5.2-reasoning":
		return "Gpt_5_2_Reasoning"
	case "gpt-5.3":
		return "Gpt_5_3_Chat"
	case "gpt-5.4":
		return "Gpt_5_4_Chat"
	case "gpt-5.4-reasoning":
		return "Gpt_5_4_Reasoning"
	case "gpt-5.5":
		return "Gpt_5_5_Chat"
	case "gpt-5.5-reasoning":
		return "Gpt_5_5_Reasoning"
	case "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.6-reasoning":
		return "Gpt_5_6_Reasoning"
	case "claude", "claude-sonnet":
		return "Claude_Sonnet"
	case "claude-sonnet-reasoning":
		return "Claude_Sonnet_Reasoning"
	case "gpt-5.4-quick":
		return "Gpt_5_4_Chat"
	case "gpt-5.3-think-deeper":
		return "Gpt_5_3_Chat"
	default:
		return "magic"
	}
}

func sseRaw(ctx context.Context, w http.ResponseWriter, f http.Flusher, payload string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprint(w, payload); err != nil {
		return err
	}
	if f != nil {
		f.Flush()
	}
	return nil
}

func (s *Server) chatOnce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body chatBody
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
		return
	}
	text := strings.TrimSpace(firstNonEmpty(body.Message, body.Prompt))
	if text == "" && len(body.Attachments) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "message or attachment required")
		return
	}
	if body.SessionKey != "" {
		if v, ok := s.sessions.get(body.SessionKey); ok {
			body.AccountID = firstNonEmpty(body.AccountID, v.AccountID)
			body.ConversationID = firstNonEmpty(body.ConversationID, v.ConversationID)
			body.SessionID = firstNonEmpty(body.SessionID, v.SessionID)
		}
	}
	accountIDs := s.apiKeys.accountIDs(rawAPIKey(r))
	var acc auth.AccountToken
	var err error
	if len(accountIDs) > 0 {
		acc, err = s.resolveBoundAccount(accountIDs, body.AccountID)
	} else {
		acc, err = s.resolveAccount(body.AccountID)
	}
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if acc.OID == "" || acc.TID == "" {
		if claimsOID, claimsTID := extractOIDTID(acc.AccessToken); claimsOID != "" {
			acc.OID = claimsOID
			acc.TID = claimsTID
		}
	}
	if acc.OID == "" || acc.TID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "account_error", "account missing oid/tid — re-login with PKCE browser client")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.settings.get().ChatTimeoutSeconds)*time.Second)
	defer cancel()
	chatSettings := s.settings.get()
	res, err := s.chatWithAccount(ctx, acc.ID, chathub.Account{
		AccessToken: acc.AccessToken,
		OID:         acc.OID,
		TID:         acc.TID,
	}, chathub.Request{
		Text:                  text,
		Tone:                  body.Tone,
		ConversationID:        body.ConversationID,
		SessionID:             body.SessionID,
		Attachments:           body.Attachments,
		LicenseType:           chatSettings.LicenseType,
		Scenario:              chatSettings.Scenario,
		ConversationSignature: body.ConversationSignature,
		PreviousMessages:      body.PreviousMessages,
		ConnectedFederatedIDs: body.ConnectedFederatedIDs,
		FeatureFlags:          s.featureFlags(),
	})
	if err != nil {
		originalErr := err
		// Failover: a rate-limited or auth-failed account must not take down the
		// request when the pool has other healthy accounts. Only auto-selected
		// requests fail over; an explicitly chosen account is respected, and a
		// conversation-bound chat stays on its account.
		if body.AccountID == "" && (IsRateLimited(err) || IsAuthFailure(err)) {
			if s.accountPool != nil {
				if errors.Is(originalErr, chathub.ErrImageLimit) || IsImageLimitErr(originalErr) {
					s.accountPool.MarkImageLimited(acc.ID)
				} else {
					s.accountPool.MarkFailure(acc.ID, originalErr, s.getRateLimitCooldown())
				}
			}
			next, nerr := s.nextHealthyAccount(acc.ID, accountIDs)
			if nerr == nil {
				ctx2 := ctx
				failoverReq := chathub.Request{
					Text:                  text,
					Tone:                  body.Tone,
					Attachments:           body.Attachments,
					LicenseType:           chatSettings.LicenseType,
					Scenario:              chatSettings.Scenario,
					ConversationSignature: body.ConversationSignature,
					PreviousMessages:      body.PreviousMessages,
					ConnectedFederatedIDs: body.ConnectedFederatedIDs,
					FeatureFlags:          s.featureFlags(),
				}
				res2, err2 := s.chatWithAccount(ctx2, next.ID, chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}, failoverReq)
				if err2 == nil {
					acc = next
					res = res2
					err = nil
				} else {
					if s.accountPool != nil {
						if errors.Is(err2, chathub.ErrImageLimit) || IsImageLimitErr(err2) {
							s.accountPool.MarkImageLimited(next.ID)
						} else {
							s.accountPool.MarkFailure(next.ID, err2, s.getRateLimitCooldown())
						}
					}
					err = err2
				}
			}
		}
		if err != nil {
			if errors.Is(err, chathub.ErrImageLimit) && s.accountPool != nil {
				s.accountPool.MarkImageLimited(acc.ID)
			}
			writeUpstreamError(w, err)
			return
		}
	}
	if res.Throttling != nil && s.accountPool != nil {
		s.accountPool.UpdateThrottling(acc.ID, res.Throttling)
		s.logThrottlingWarning(acc.ID, res.Throttling)
	}
	res.Text = sanitizePublicAssistantText(res.Text)
	res.Reasoning = sanitizePublicReasoningText(res.Reasoning)
	if body.SessionKey != "" {
		s.sessions.upsert(conversation{ID: body.SessionKey, AccountID: acc.ID, ConversationID: res.ConversationID, SessionID: res.SessionID, Title: text})
	}
	if res.Throttling != nil {
		if b, err := json.Marshal(res.Throttling); err == nil {
			w.Header().Set("X-M365-Throttling", string(b))
		}
	}
	if len(res.Scores) > 0 {
		if b, err := json.Marshal(res.Scores); err == nil {
			w.Header().Set("X-M365-Scores", string(b))
		}
	}
	jsonOut(w, map[string]any{
		"status":                    "ok",
		"text":                      res.Text,
		"conversationId":            res.ConversationID,
		"sessionId":                 res.SessionID,
		"requestId":                 res.RequestID,
		"throttling":                res.Throttling,
		"suggestedResponses":        res.SuggestedResponses,
		"result":                    res.RawResult,
		"events":                    res.Events,
		"images":                    res.Images,
		"account":                   map[string]any{"id": acc.ID, "email": acc.Email},
		"offense":                   res.Offense,
		"scores":                    res.Scores,
		"conversationTransferToken": res.ConversationTransferToken,
		"meteringInformation":       res.MeteringInformation,
		"spokenText":                res.SpokenText,
		"storageMessageId":          res.StorageMessageID,
		"timestamps":                res.Timestamps,
	})
}

// dropTransientConversation 异步删除 router/repair/test 轮创建的一次性云端对话，
// 避免每请求都往 M365 对话列表塞一条记录。删除失败不阻塞请求，留给 auto_cleanup 兜底。
func (s *Server) dropTransientConversation(accountID, conversationID string) {
	if conversationID == "" || s.cloudClients == nil {
		return
	}
	var client *M365CloudClient
	var ok bool
	if strings.TrimSpace(accountID) != "" {
		client, ok = s.cloudClients.get(strings.TrimSpace(accountID))
	}
	if !ok || client == nil {
		if session, found := s.sessionResolver.GetConversationByID(conversationID); found {
			client, ok = s.cloudClients.get(session.AccountID)
		}
	}
	if !ok || client == nil {
		return
	}
	go func(id string, cl *M365CloudClient) {
		if err := cl.DeleteConversation(id); err != nil {
			log.Printf("[transient-conv] delete failed id=%s err=%v", id, err)
		}
	}(conversationID, client)
}

func (s *Server) adminModelSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	syncUpstreamTones()
	tones := liveUpstreamTones()
	jsonOut(w, map[string]any{"synced": true, "upstream_tones": tones, "count": len(tones)})
}

func (s *Server) adminModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	jsonOut(w, map[string]any{"object": "list", "data": modelCatalog()})
}

// adminModelTest 由控制台模型测试调用，通过管理员会话鉴权，不依赖明文 API Key
// （密钥加固后 list 不再返回 raw，前端无法再自行携带 key 调用 /v1 端点）。
func (s *Server) adminModelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var b struct {
		Model     string `json:"model"`
		AccountID string `json:"account_id"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil || strings.TrimSpace(b.Model) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json: model required")
		return
	}
	acc, err := s.resolveAccount(b.AccountID)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if acc.OID == "" || acc.TID == "" {
		if o, t := extractOIDTID(acc.AccessToken); o != "" {
			acc.OID, acc.TID = o, t
		}
	}
	if acc.OID == "" || acc.TID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "account_error", "account missing oid/tid")
		return
	}
	tone, toneErr := reasoningTone(b.Model, "")
	if toneErr != nil {
		writeOpenAIError(w, http.StatusBadRequest, "model_not_found", toneErr.Error())
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.settings.get().ChatTimeoutSeconds)*time.Second)
	defer cancel()
	testCfg := s.settings.get()
	res, err := s.chatWithAccount(ctx, acc.ID, chathub.Account{AccessToken: acc.AccessToken, OID: acc.OID, TID: acc.TID}, chathub.Request{
		Text:         `Say "OK" in one word.`,
		Tone:         tone,
		LicenseType:  testCfg.LicenseType,
		Scenario:     testCfg.Scenario,
		FeatureFlags: s.featureFlags(),
	})
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "m365_error", upstreamError(err))
		return
	}
	if res.ConversationID != "" {
		s.dropTransientConversation(acc.ID, res.ConversationID)
	}
	jsonOut(w, map[string]any{"ok": true, "model": b.Model, "reply": sanitizePublicAssistantTextForModel(res.Text, b.Model), "latency_ms": ms})
}

func (s *Server) openaiModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	data := modelCatalog()
	created := time.Now().Unix()
	for _, model := range data {
		model["created"] = created
	}
	// Codex v0.144.5 requires `models`, while OpenAI-compatible clients use
	// `data`. Keep both aliases backed by the same catalog.
	jsonOut(w, map[string]any{"object": "list", "data": data, "models": data})
}

type oaiMsg struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []map[string]any `json:"tool_calls,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
}

type oaiReq struct {
	Model          string          `json:"model"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Messages       []oaiMsg        `json:"messages"`
	Stream         bool            `json:"stream"`
	StreamOptions  *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	MaxTokens           *int                 `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                 `json:"max_completion_tokens,omitempty"`
	Temperature         *float64             `json:"temperature,omitempty"`
	TopP                *float64             `json:"top_p,omitempty"`
	FrequencyPenalty    *float64             `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64             `json:"presence_penalty,omitempty"`
	Stop                any                  `json:"stop,omitempty"`
	N                   *int                 `json:"n,omitempty"`
	Seed                *int64               `json:"seed,omitempty"`
	Logprobs            *bool                `json:"logprobs,omitempty"`
	TopLogprobs         *int                 `json:"top_logprobs,omitempty"`
	User                string               `json:"user"`
	AccountID           string               `json:"accountId"`
	ConversationID      string               `json:"conversation_id"`
	SessionID           string               `json:"session_id"`
	SessionKey          string               `json:"session_key"`
	ConversationIDC     string               `json:"conversationId,omitempty"`
	SessionIDC          string               `json:"sessionId,omitempty"`
	Attachments         []chathub.Attachment `json:"attachments,omitempty"`
	Tools               []chathub.Tool       `json:"tools,omitempty"`
	Functions           []json.RawMessage    `json:"functions,omitempty"`
	ToolChoice          any                  `json:"tool_choice,omitempty"`
	FunctionCall        any                  `json:"function_call,omitempty"`
	ParallelToolCalls   *bool                `json:"parallel_tool_calls,omitempty"`
	Reasoning           *reasoningConfig     `json:"reasoning,omitempty"`
	ReasoningEffort     string               `json:"reasoning_effort,omitempty"`
	Metadata            *oaiMetadata         `json:"metadata,omitempty"`
}

type oaiMetadata struct {
	CopilotTempSession bool `json:"copilot_temp_session"`
}

func (r *oaiReq) shouldSendStreamUsage() bool {
	if r.StreamOptions == nil {
		return true
	}
	return r.StreamOptions.IncludeUsage
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func contentToString(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			t, _ := m["type"].(string)
			switch t {
			case "text", "input_text", "output_text":
				if s, _ := m["text"].(string); s != "" {
					b.WriteString(s)
				}
			case "image_url":
				url := extractMediaURL(m, "image_url")
				b.WriteString("[image:" + shortHash(url) + "]")
			case "input_image", "image":
				url := extractMediaURL(m, "image_url", "url", "source")
				if raw, ok2 := m["image_url"].(map[string]any); ok2 {
					if u := stringValue(raw, "url", "data", "image_url"); u != "" {
						url = u
					}
				}
				if raw, ok2 := m["source"].(map[string]any); ok2 && url == "" {
					url = stringValue(raw, "url", "data", "source")
				}
				b.WriteString("[image:" + shortHash(url) + "]")
			case "input_file", "file":
				url := stringValue(m, "file_data", "file_url", "url", "source", "file_id")
				b.WriteString("[file:" + shortHash(url) + "]")
			case "input_audio", "audio":
				url := stringValue(m, "data", "audio_url", "url", "source")
				b.WriteString("[audio:" + shortHash(url) + "]")
			}
		}
		return b.String()
	default:
		return fmt.Sprint(v)
	}
}

func extractMediaURL(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case map[string]any:
			if u, ok := v["url"].(string); ok && u != "" {
				return u
			}
		}
	}
	return ""
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func normalizeLegacyTools(body *oaiReq) {
	if len(body.Tools) == 0 && len(body.Functions) > 0 {
		body.Tools = make([]chathub.Tool, 0, len(body.Functions))
		for _, f := range body.Functions {
			body.Tools = append(body.Tools, chathub.Tool{Type: "function", Function: f})
		}
	}
	if body.ToolChoice == nil && body.FunctionCall != nil {
		body.ToolChoice = body.FunctionCall
	}
}

func buildAnswerRequest(answerPrompt, tone string, body oaiReq, ledger agentLedger, planningMode string, mcpServerURL string, cfg runtimeSettings, flags chathub.FeatureFlags, locale chathubLocale, disableMemory bool) chathub.Request {
	if len(ledger.Completed) > 0 || len(ledger.Pending) > 0 {
		answerPrompt += "\n" + ledger.RouterContext()
	}
	if len(ledger.Completed) > 0 {
		answerPrompt += "\nFINAL ANSWER RULE: Report only actions supported by completed tool results. If the goal is not fully verified, state exactly what remains unconfirmed."
	}
	if isToolRefusal(answerPrompt) || isSandboxHallucination(answerPrompt) || strings.Contains(answerPrompt, `:\`) {
		answerPrompt += "\nENVIRONMENT NOTICE: You are connected to a local workspace host. Do NOT assume a cloud sandbox, Python container, or /mnt/data. Do NOT ask the user to upload ZIP archives."
	}
	req := chathub.Request{Text: answerPrompt, Tone: tone, ConversationID: body.ConversationID, SessionID: body.SessionID, Attachments: body.Attachments, LicenseType: cfg.LicenseType, Scenario: cfg.Scenario, FeatureFlags: flags, Locale: locale.Locale, Market: locale.Market, TimeZone: locale.TimeZone, TimeZoneOffset: locale.TimeZoneOffset, DeviceOS: locale.DeviceOS, DisableMemory: disableMemory}
	// The audited HAR set does not contain a complete client-tool invocation
	// lifecycle, stable call identifier, argument-delta contract, or tool-result
	// continuation for the former native/API-plugin payload. Keep client tools
	// on the gateway's validated router path and never silently forward an
	// inferred plugin or MCP declaration to ChatHub.
	_ = planningMode
	_ = mcpServerURL
	return req
}

func (s *Server) openaiChat(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFrom(r)
	if requestID == "" {
		requestID = uuid.NewString()
	}
	startedAt := time.Now()
	log.Printf("[req-trace] id=%s stage=http_start stream=%t", requestID, r.URL.Query().Get("stream") == "true")
	defer func() {
		log.Printf("[req-trace] id=%s stage=http_return total_ms=%d", requestID, time.Since(startedAt).Milliseconds())
	}()
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	const maxChatRequestBody = 10 << 20
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxChatRequestBody))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "read body")
		return
	}
	var body oaiReq
	if err := json.Unmarshal(raw, &body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
		return
	}
	responseFormat := body.ResponseFormat
	effort := body.ReasoningEffort
	if body.Reasoning != nil && strings.TrimSpace(body.Reasoning.Effort) != "" {
		effort = body.Reasoning.Effort
	}
	tone, toneErr := reasoningTone(body.Model, effort)
	if toneErr != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", toneErr.Error())
		return
	}
	normalizeLegacyTools(&body)
	body.ConversationID = firstNonEmpty(body.ConversationID, body.ConversationIDC)
	body.SessionID = firstNonEmpty(body.SessionID, body.SessionIDC)
	log.Printf("[req-trace] id=%s stage=body_parsed messages=%d tools=%d choice=%s raw_bytes=%d", requestID, len(body.Messages), len(body.Tools), normalizedToolChoiceMode(body.ToolChoice), len(raw))
	if err := validateToolConversation(body.Messages); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "tool_protocol_error", err.Error())
		return
	}
	// Rebuild a protocol-neutral evidence ledger from actual tool calls/results.
	// Round limits apply only to the current user turn; full history still informs evidence.
	activeLedger := buildAgentLedger(activeMessages(body.Messages))
	if err := activeLedger.CanContinue(maxToolRounds()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "tool_round_limit", "message": err.Error(), "completed_calls": len(activeLedger.Completed)}})
		return
	}
	// Gateway must not truncate context. All messages are forwarded as-is and
	// any limit is left to the upstream M365 service and the client's own
	// budgeting. See https://github.com/HEXUXIU/M365-Copilot2API/issues/73
	// Preserve role boundaries when adapting OpenAI messages to ChatHub's
	// single message.text field. This keeps system/developer instructions,
	// history, and the current user turn distinguishable.
	var prompt string
	if isImageModel(body.Model) {
		prompt, body.Attachments = extractImagePrompt(body.Messages, body.Attachments)
	} else {
		prompt, body.Attachments = flattenPromptMessages(body.Messages, body.Attachments)
	}
	log.Printf("[req-trace] id=%s stage=prompt_flattened prompt_len=%d attachments=%d", requestID, len(prompt), len(body.Attachments))
	fmt.Printf("[multimodal-entry] messages=%d attachments=%d prompt_len=%d\n", len(body.Messages), len(body.Attachments), len(prompt))
	prompt = strings.TrimSpace(prompt)
	if responseFormat != nil {
		switch responseFormat.Type {
		case "json_object":
			prompt += "\nYou must respond with valid JSON."
		case "json_schema":
			if responseFormat.JSONSchema != nil {
				if schema, ok := responseFormat.JSONSchema["schema"]; ok {
					prompt += "\nYou must respond with valid JSON that conforms to this schema:\n" + mustJSON(schema)
				} else {
					prompt += "\nYou must respond with valid JSON."
				}
			} else {
				prompt += "\nYou must respond with valid JSON."
			}
		}
	}
	if prompt == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "messages required")
		return
	}
	if answer, ok := publicIdentityAnswer(body.Messages, body.Model); ok && responseFormat == nil {
		s.writePublicIdentityChatResponse(w, r, &body, prompt, answer, startedAt)
		return
	}

	if body.SessionKey != "" {
		if v, ok := s.sessions.get(body.SessionKey); ok {
			body.AccountID = firstNonEmpty(body.AccountID, v.AccountID)
			body.ConversationID = firstNonEmpty(body.ConversationID, v.ConversationID)
			body.SessionID = firstNonEmpty(body.SessionID, v.SessionID)
		}
	}
	if body.User != "" && body.ConversationID == "" {
		if us, ok := s.userSessions.Get(tenantFromRequest(r), body.User); ok {
			body.AccountID = firstNonEmpty(body.AccountID, us.AccountID)
			body.ConversationID = us.ConversationID
			body.SessionID = us.SessionID
			log.Printf("[user-session] hit user=%s conversation=%s session=%s", body.User, us.ConversationID, us.SessionID)
		}
	}
	if body.Metadata != nil && body.Metadata.CopilotTempSession {
		body.ConversationID = ""
		body.SessionID = ""
		log.Printf("[temp-session] copilot_temp_session=true, clearing conversation/session for one-shot request")
	}
	answerPrompt := prompt
	requestedAccountID := body.AccountID
	convCacheNamespace := responseNamespace(tenantFromRequest(r), firstNonEmpty(responseSessionID(r), body.SessionKey, body.User))
	if body.ConversationID == "" && len(body.Messages) > 0 && (body.Metadata == nil || !body.Metadata.CopilotTempSession) {
		resolved := s.sessionResolver.Resolve(r, &body)
		if !resolved.IsNew {
			if maxMessages := s.settings.get().MaxConversationMessages; shouldRotateResolvedConversation(resolved.HistoryLen, len(body.Messages), maxMessages) {
				log.Printf("[session-resolver] rotating conversation=%s history=%d limit=%d", resolved.ConversationID, resolved.HistoryLen, maxMessages)
				s.convCache.Invalidate(convCacheNamespace, resolved.AccountID, firstNonEmpty(body.Model, "m365-copilot"))
			} else if requestedAccountID == "" && !s.accountAvailable(resolved.AccountID) {
				log.Printf("[session-resolver] bound account=%s is unavailable/cooling down, rotating session to healthy account", resolved.AccountID)
				s.convCache.Invalidate(convCacheNamespace, resolved.AccountID, firstNonEmpty(body.Model, "m365-copilot"))
			} else {
				body.ConversationID = resolved.ConversationID
				body.SessionID = resolved.SessionID
				body.AccountID = firstNonEmpty(body.AccountID, resolved.AccountID)
				log.Printf("[session-resolver] matched=%s conversation=%s history=%d total=%d", resolved.MatchedBy, resolved.ConversationID, resolved.HistoryLen, len(body.Messages))
				if resolved.HistoryLen > 0 && resolved.HistoryLen < len(body.Messages) {
					incPrompt, incAtt := flattenPromptMessages(body.Messages[resolved.HistoryLen:], nil)
					incPrompt = strings.TrimSpace(incPrompt)
					if incPrompt != "" {
						answerPrompt = incPrompt
						body.Attachments = incAtt
					}
				}
			}
		}
	}
	accountID := body.AccountID
	accountIDs := s.apiKeys.accountIDs(rawAPIKey(r))
	var acc auth.AccountToken
	if isImageModel(body.Model) {
		acc, err = s.resolveImageAccount(accountIDs, accountID)
	} else if len(accountIDs) > 0 {
		acc, err = s.resolveBoundAccount(accountIDs, accountID)
	} else {
		acc, err = s.resolveAccount(accountID)
	}
	if err != nil {
		log.Printf("[account-route] resolve failed requested=%q err=%v", accountID, err)
		writeUpstreamErrorWithAccount(w, err, accountID)
		return
	}
	log.Printf("[account-route] selected id=%q email=%q token_present=%t oid_present=%t tid_present=%t", acc.ID, acc.Email, acc.AccessToken != "", acc.OID != "", acc.TID != "")
	if acc.OID == "" || acc.TID == "" {
		if o, t := extractOIDTID(acc.AccessToken); o != "" {
			acc.OID, acc.TID = o, t
		}
	}
	if acc.OID == "" || acc.TID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "account_error", "account missing oid/tid")
		return
	}

	// Conversation cache: reuse existing M365 conversation for same account+model
	// to avoid re-processing full system prompt + history each request (latency
	// drops from 3-5s to ~1s). Only kicks in when no explicit conversation ID
	// was provided by client, session key, user session, or session resolver.
	convReused := false
	convCacheModel := firstNonEmpty(body.Model, "m365-copilot")
	if body.ConversationID == "" && len(body.Messages) > 1 &&
		(body.Metadata == nil || !body.Metadata.CopilotTempSession) {
		if cached := s.convCache.Lookup(convCacheNamespace, acc.ID, convCacheModel); cached != nil && !shouldRotateConversation(cached.MessageCount, s.settings.get().MaxConversationMessages) {
			if !s.accountAvailable(acc.ID) {
				s.convCache.Invalidate(convCacheNamespace, acc.ID, convCacheModel)
			} else if len(body.Messages) == cached.MessageCount && messagesHash(body.Messages) == cached.MessagesHash {
				// identical full context: reuse same conversation without re-sending history
				body.ConversationID = cached.ConversationID
				body.SessionID = cached.SessionID
				answerPrompt = ""
				convReused = true
				log.Printf("[conv-cache] hit-identical account=%s model=%s conversation=%s", acc.ID, convCacheModel, cached.ConversationID)
			} else if len(body.Messages) > cached.MessageCount && messagesHash(body.Messages[:cached.MessageCount]) == cached.MessagesHash {
				incPrompt, incAtt := flattenPromptMessages(body.Messages[cached.MessageCount:], nil)
				incPrompt = strings.TrimSpace(incPrompt)
				if incPrompt != "" {
					body.ConversationID = cached.ConversationID
					body.SessionID = cached.SessionID
					answerPrompt = incPrompt
					body.Attachments = incAtt
					convReused = true
					log.Printf("[conv-cache] hit account=%s model=%s conversation=%s cached_msgs=%d new_msgs=%d", acc.ID, convCacheModel, cached.ConversationID, cached.MessageCount, len(body.Messages))
				}
			}
		}
	}
	if !convReused && body.ConversationID == "" {
		log.Printf("[conv-cache] miss account=%s model=%s", acc.ID, convCacheModel)
	}

	// Normalize tools once. Selection is always made by the upstream model;
	// the gateway only validates its structured decision and converts protocols.
	toolMaps := make([]map[string]any, 0, len(body.Tools))
	for _, tool := range body.Tools {
		var f map[string]any
		_ = json.Unmarshal(tool.Function, &f)
		toolMaps = append(toolMaps, map[string]any{"type": tool.Type, "function": f})
	}
	if body.ToolChoice == nil && len(toolMaps) > 0 {
		body.ToolChoice = "auto"
	}
	// Do not synthesize an upstream MCPServer plugin URL. The local MCP endpoint
	// remains available to MCP clients, while OpenAI/Responses/Anthropic tools
	// are selected through the validated router and correlated by client call ID.
	mcpServerURL := ""
	validateCalls := func(stage string, calls []detectedToolCall) ([]detectedToolCall, int) {
		calls = dedupeToolCalls(calls)
		valid, rejected := validateDetectedToolCalls(calls, toolMaps, body.ToolChoice)
		for _, call := range rejected {
			log.Printf("[tool-validation] id=%s stage=%s rejected_name=%q reason=%q", requestID, stage, call.Name, call.Reason)
		}
		return valid, len(rejected)
	}
	planningMode := s.settings.get().ToolPlanningMode
	toolCfg := s.settings.get()

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.settings.get().ChatTimeoutSeconds)*time.Second)
	defer cancel()
	account := chathub.Account{AccessToken: acc.AccessToken, OID: acc.OID, TID: acc.TID}
	localeInfo := parseLocaleFromHeaders(r)
	// The stream is opened by the actual response path below. Do not emit a
	// tool preamble here: a request may contain tools in its schema while still
	// being an ordinary text question.
	// Ask the upstream model to select and validate the next tool. The gateway
	// remains tool-agnostic; it only validates and serializes the decision.
	if (planningMode == "router" || planningMode == "router_slim") && len(toolMaps) > 0 && fmt.Sprint(body.ToolChoice) != "none" {
		var routePrompt string
		if planningMode == "router_slim" {
			routePrompt = modelToolSlimRouterPrompt(prompt+"\n"+activeLedger.RouterContext(), toolMaps, body.ToolChoice)
		} else {
			routePrompt = modelToolRouterPrompt(prompt+"\n"+activeLedger.RouterContext(), toolMaps, body.ToolChoice)
		}
		log.Printf("[req-trace] id=%s stage=router_start mode=%s prompt_len=%d tools=%d", requestID, planningMode, len(routePrompt), len(toolMaps))
		routeRes, routeErr := s.chatWithAccount(ctx, acc.ID, account, chathub.Request{Text: routePrompt, Tone: tone, Attachments: body.Attachments, LicenseType: toolCfg.LicenseType, Scenario: toolCfg.Scenario})
		if routeErr != nil {
			if requestedAccountID == "" && (IsRateLimited(routeErr) || IsAuthFailure(routeErr)) {
				if s.accountPool != nil {
					s.accountPool.MarkFailure(acc.ID, routeErr, s.getRateLimitCooldown())
				}
				tried := map[string]bool{acc.ID: true}
				curID := acc.ID
				for range s.tokens.List() {
					next, nerr := s.nextHealthyAccount(curID, accountIDs)
					if nerr != nil {
						break
					}
					if tried[next.ID] {
						break
					}
					tried[next.ID] = true
					if res2, err2 := s.chatWithAccount(ctx, next.ID, chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}, chathub.Request{Text: routePrompt, Tone: tone, Attachments: body.Attachments, LicenseType: toolCfg.LicenseType, Scenario: toolCfg.Scenario}); err2 == nil {
						s.dropTransientConversation(next.ID, routeRes.ConversationID)
						routeRes, routeErr = res2, nil
						acc = next
						account = chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}
						break
					} else {
						if s.accountPool != nil {
							s.accountPool.MarkFailure(next.ID, err2, s.getRateLimitCooldown())
						}
						if !IsRateLimited(err2) && !IsAuthFailure(err2) {
							routeErr = err2
							break
						}
						routeErr = err2
						curID = next.ID
					}
				}
			}
			if routeErr != nil {
				writeUpstreamError(w, routeErr)
				return
			}
		}
		s.dropTransientConversation(acc.ID, routeRes.ConversationID)
		if planningMode == "router_slim" {
			if needNames := parseNeedToolsDecision(routeRes.Text, toolMaps); len(needNames) > 0 {
				neededTools := filterToolsByName(toolMaps, needNames)
				if len(neededTools) > 0 {
					fillPrompt := modelToolParamFillPrompt(prompt+"\n"+activeLedger.RouterContext(), neededTools)
					fillRes, fillErr := s.chatWithAccount(ctx, acc.ID, account, chathub.Request{Text: fillPrompt, Tone: tone, Attachments: body.Attachments, LicenseType: toolCfg.LicenseType, Scenario: toolCfg.Scenario})
					if fillErr == nil {
						s.dropTransientConversation(acc.ID, fillRes.ConversationID)
						routeRes = fillRes
					}
				}
			}
		}
		calls, parsed := parseModelToolDecision(routeRes.Text, toolMaps, body.ToolChoice)
		log.Printf("[req-trace] id=%s stage=router_initial_decision calls=%d parsed=%t text_preview=%q", requestID, len(calls), parsed, compactToolResult(routeRes.Text, 80))
		if isSandboxHallucination(routeRes.Text) || isToolRefusal(routeRes.Text) {
			log.Printf("[router-sandbox-eject] id=%s model hallucinated sandbox or refused tools: %q", requestID, compactToolResult(routeRes.Text, 120))
			toolList := strings.Join(declaredToolNames(toolMaps), ", ")
			var toolDefsBytes []byte
			if planningMode == "router_slim" {
				toolDefsBytes, _ = json.Marshal(compactToolSchemaList(toolMaps))
			} else {
				toolDefsBytes, _ = json.Marshal(toolMaps)
			}
			correctionText := fmt.Sprintf(`CRITICAL CORRECTION:
You incorrectly claimed that files cannot be accessed or that /mnt/data is required, or hallucinated a cloud sandbox/container.
1. You are running as an internal tool router for a local host. There is NO sandbox and NO /mnt/data. Never ask to upload ZIP archives or project files.
2. The caller's tools execute directly in their local environment with full access to local files, Windows drives (C:\...), and workspace paths.
3. Real client tools available: [%s]
Tool definitions: %s

You MUST call the appropriate client tool now to inspect the workspace or perform the requested action.
Do not provide explanations or chat text.

User request:
%s

OUTPUT FORMAT:
CALL_TOOL: tool_name({"arg1":"value1"})

Decision:`, toolList, string(toolDefsBytes), prompt+"\n"+activeLedger.RouterContext())

			retryRes, retryErr := s.chatWithAccount(ctx, acc.ID, account, chathub.Request{Text: correctionText, Tone: tone, Attachments: body.Attachments, LicenseType: toolCfg.LicenseType, Scenario: toolCfg.Scenario})
			if retryErr == nil {
				s.dropTransientConversation(acc.ID, retryRes.ConversationID)
				routeRes = retryRes
				calls, parsed = parseModelToolDecision(routeRes.Text, toolMaps, body.ToolChoice)
			}
			if len(calls) == 0 {
				if synthCalls, ok := trySynthesizeWorkspaceInspection(prompt, routeRes.Text, toolMaps, body.ToolChoice); ok && len(synthCalls) > 0 {
					log.Printf("[router-synth-fallback] id=%s synthesized %d tool calls after refusal retry: %s", requestID, len(synthCalls), synthCalls[0].Name)
					calls = synthCalls
					parsed = true
				}
			}
		}
		if !parsed {
			if isSandboxHallucination(routeRes.Text) || isToolRefusal(routeRes.Text) {
				if synthCalls, ok := trySynthesizeWorkspaceInspection(prompt, routeRes.Text, toolMaps, body.ToolChoice); ok && len(synthCalls) > 0 {
					log.Printf("[router-synth-fallback] id=%s synthesized %d tool calls for unparsed refusal: %s", requestID, len(synthCalls), synthCalls[0].Name)
					calls = synthCalls
					parsed = true
				} else {
					writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "model refused to call available tools and hallucinated sandbox")
					return
				}
			} else {
				repairRes, repairErr := s.chatWithAccount(ctx, acc.ID, account, chathub.Request{Text: `Repair this tool routing output into JSON only with shape {"calls":[{"name":"function_name","arguments":{}}]}. Do not invent calls; use {"calls":[]} if unrecoverable. OUTPUT:
` + compactToolResult(routeRes.Text, 6000), Tone: tone, Attachments: body.Attachments, LicenseType: toolCfg.LicenseType, Scenario: toolCfg.Scenario})
				if repairErr == nil {
					s.dropTransientConversation(acc.ID, repairRes.ConversationID)
					calls, parsed = parseModelToolDecision(repairRes.Text, toolMaps, body.ToolChoice)
				}
				if !parsed {
					writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "model returned an invalid tool routing decision")
					return
				}
			}
		}
		calls = filterCompletedCalls(calls, activeLedger)
		calls, _ = validateCalls("router", calls)
		if len(calls) == 0 && (isSandboxHallucination(routeRes.Text) || isToolRefusal(routeRes.Text)) {
			if synthCalls, ok := trySynthesizeWorkspaceInspection(prompt, routeRes.Text, toolMaps, body.ToolChoice); ok && len(synthCalls) > 0 {
				synthCalls = filterCompletedCalls(synthCalls, activeLedger)
				synthCalls, _ = validateCalls("router", synthCalls)
				if len(synthCalls) > 0 {
					log.Printf("[router-synth-fallback] id=%s synthesized %d validated tool calls: %s", requestID, len(synthCalls), synthCalls[0].Name)
					calls = synthCalls
				}
			}
			if len(calls) == 0 {
				writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "model refused to execute tools for workspace task")
				return
			}
		}
		log.Printf("[req-trace] id=%s stage=router_final_decision calls=%d", requestID, len(calls))
		if len(calls) > 0 {
			scope := fmt.Sprintf("%d:%v", len(body.Messages), completedCallIDs(activeLedger))
			for i := range calls {
				calls[i].ID = scopedCallID(calls[i].Name, string(calls[i].Arguments), i, scope)
			}
			calls = limitToolCalls(calls, adaptiveToolCallLimit(calls, configuredToolCallLimit(s.settings)))
			if body.ParallelToolCalls != nil && !*body.ParallelToolCalls && len(calls) > 1 {
				calls = calls[:1]
			}
			_ = writeToolResponse(w, "chatcmpl-"+uuid.NewString(), firstNonEmpty(body.Model, "m365-copilot"), body.Stream, body.shouldSendStreamUsage(), calls, routeRes)
			return
		}
		if fmt.Sprint(body.ToolChoice) == "required" {
			var defs []byte
			if planningMode == "router_slim" {
				defs, _ = json.Marshal(compactToolSchemaList(toolMaps))
			} else {
				defs, _ = json.Marshal(toolMaps)
			}
			retryText := `Select at least one required next tool call from FUNCTION_DEFINITIONS. Validate every argument against its schema. Return JSON only as {"calls":[{"name":"function_name","arguments":{}}]}.
APPLICATION_REQUEST_AND_EVIDENCE:
` + prompt + "\n" + activeLedger.RouterContext() + "\nFUNCTION_DEFINITIONS:\n" + string(defs)
			retryRes, retryErr := s.chatWithAccount(ctx, acc.ID, account, chathub.Request{Text: retryText, Tone: tone, Attachments: body.Attachments, LicenseType: toolCfg.LicenseType, Scenario: toolCfg.Scenario})
			if retryErr == nil {
				s.dropTransientConversation(acc.ID, retryRes.ConversationID)
				calls, parsed = parseModelToolDecision(retryRes.Text, toolMaps, body.ToolChoice)
				calls = filterCompletedCalls(calls, activeLedger)
				calls, _ = validateCalls("router", calls)
				if parsed && len(calls) > 0 {
					scope := fmt.Sprintf("%d:%v:required-retry", len(body.Messages), completedCallIDs(activeLedger))
					for i := range calls {
						calls[i].ID = scopedCallID(calls[i].Name, string(calls[i].Arguments), i, scope)
					}
					calls = limitToolCalls(calls, adaptiveToolCallLimit(calls, configuredToolCallLimit(s.settings)))
					if body.ParallelToolCalls != nil && !*body.ParallelToolCalls && len(calls) > 1 {
						calls = calls[:1]
					}
					_ = writeToolResponse(w, "chatcmpl-"+uuid.NewString(), firstNonEmpty(body.Model, "m365-copilot"), body.Stream, body.shouldSendStreamUsage(), calls, retryRes)
					return
				}
			}
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "model did not select a required tool after constrained retry")
			return
		}
	}
	if body.Stream {
		answerReq := buildAnswerRequest(answerPrompt, tone, body, activeLedger, planningMode, mcpServerURL, s.settings.get(), s.featureFlags(), localeInfo, body.Metadata != nil && body.Metadata.CopilotTempSession)
		answerPrompt = answerReq.Text
		log.Printf("[req-trace] id=%s stage=answer_start prompt_len=%d native_tools=%d mcp=%s", requestID, len(answerPrompt), len(answerReq.Tools), mcpServerURL)
		id := "chatcmpl-" + uuid.NewString()
		model := firstNonEmpty(body.Model, "m365-copilot")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeOpenAIError(w, http.StatusInternalServerError, "server_error", "stream unsupported")
			return
		}
		if err := sseRaw(r.Context(), w, flusher, ": connected\n\n"); err != nil {
			return
		}
		sw := newSSEWriter(w, flusher)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		keepaliveDone := make(chan struct{})
		defer close(keepaliveDone)
		go func() {
			for {
				select {
				case <-keepaliveDone:
					return
				case <-r.Context().Done():
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = sw.raw(": keepalive\n\n")
				}
			}
		}()
		var text strings.Builder
		var pending strings.Builder
		var streamedTools []detectedToolCall
		first := true
		identityFilter := newPublicIdentityStreamFilter(model)
		emitText := func(part string) error {
			if part == "" {
				return nil
			}
			part = identityFilter.Push(part)
			if part == "" {
				return nil
			}
			if err := r.Context().Err(); err != nil {
				return err
			}
			delta := map[string]any{"content": part}
			if first {
				delta["role"] = "assistant"
				first = false
			}
			chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}
			if err := sw.data(mustJSON(chunk)); err != nil {
				return err
			}
			return nil
		}
		res, err := s.chatWithAccountEvents(ctx, acc.ID, account, answerReq, func(ev chathub.StreamEvent) error {
			if ev.Kind == "tool" && ev.ToolName != "" && len(ev.Arguments) > 0 {
				toolKnown := false
				for _, tm := range toolMaps {
					if fn, ok := tm["function"].(map[string]any); ok {
						if fn["name"] == ev.ToolName {
							toolKnown = true
							break
						}
					}
				}
				if toolKnown {
					streamedTools = append(streamedTools, detectedToolCall{ID: "call_" + uuid.NewString(), Name: ev.ToolName, Arguments: ev.Arguments})
				} else {
					log.Printf("[tool-event] id=%s skipping unknown native tool %q (not in client-declared tools)", requestID, ev.ToolName)
				}
				return nil
			}
			if ev.Kind != "text" || ev.Text == "" {
				return nil
			}
			return streamEmitText(ev, &text, &pending, emitText)
		})
		if err != nil && text.Len() == 0 && len(streamedTools) == 0 && requestedAccountID == "" && (IsRateLimited(err) || IsAuthFailure(err)) {
			originalErr := err
			if s.accountPool != nil {
				if errors.Is(originalErr, chathub.ErrImageLimit) || IsImageLimitErr(originalErr) {
					s.accountPool.MarkImageLimited(acc.ID)
				} else {
					s.accountPool.MarkFailure(acc.ID, originalErr, s.getRateLimitCooldown())
				}
			}
			if convReused {
				s.invalidateConvCache(convCacheNamespace, acc.ID, convCacheModel)
				convReused = false
			}
			tried := map[string]bool{acc.ID: true}
			curID := acc.ID
			curErr := err
			for range s.tokens.List() {
				next, nerr := s.nextHealthyAccount(curID, accountIDs)
				if nerr != nil {
					break
				}
				if tried[next.ID] {
					break
				}
				tried[next.ID] = true
				failoverReq := answerReq
				failoverReq.ConversationID = ""
				failoverReq.SessionID = ""
				failoverReq.Text = prompt
				ctx2 := ctx
				res2, err2 := s.chatWithAccountEvents(ctx2, next.ID, chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}, failoverReq, func(ev chathub.StreamEvent) error {
					if ev.Kind == "tool" && ev.ToolName != "" && len(ev.Arguments) > 0 {
						toolKnown := false
						for _, tm := range toolMaps {
							if fn, ok := tm["function"].(map[string]any); ok {
								if fn["name"] == ev.ToolName {
									toolKnown = true
									break
								}
							}
						}
						if toolKnown {
							streamedTools = append(streamedTools, detectedToolCall{ID: "call_" + uuid.NewString(), Name: ev.ToolName, Arguments: ev.Arguments})
						}
						return nil
					}
					if ev.Kind != "text" || ev.Text == "" {
						return nil
					}
					return streamEmitText(ev, &text, &pending, emitText)
				})
				if err2 == nil {
					res = res2
					acc = next
					account = chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}
					err = nil
					curErr = nil
					break
				} else {
					if s.accountPool != nil {
						if errors.Is(err2, chathub.ErrImageLimit) || IsImageLimitErr(err2) {
							s.accountPool.MarkImageLimited(next.ID)
						} else {
							s.accountPool.MarkFailure(next.ID, err2, s.getRateLimitCooldown())
						}
					}
					err = err2
					curErr = err2
					curID = next.ID
					if !IsRateLimited(err2) && !IsAuthFailure(err2) {
						break
					}
				}
			}
			if curErr == nil {
				err = nil
			} else {
				err = curErr
			}
		}
		if err != nil {
			log.Printf("[req-trace] id=%s stage=stream_error err=%v", requestID, err)
			if errors.Is(err, chathub.ErrImageLimit) && s.accountPool != nil {
				s.accountPool.MarkImageLimited(acc.ID)
			}
			if convReused {
				s.invalidateConvCache(convCacheNamespace, acc.ID, convCacheModel)
			}
			msg := upstreamError(err)
			if IsRateLimited(err) {
				msg = "upstream is rate limiting; try again shortly"
			}
			if errors.Is(err, chathub.ErrOffensiveContent) {
				msg = "M365 content policy flagged this request as offensive"
			}
			msg = sanitizePublicInternalText(msg)
			code := "upstream_error"
			if IsRateLimited(err) {
				code = "rate_limit_error"
			} else if ClassifyError(err) == CategoryClientCanceled {
				code = "client_canceled"
			}
			_ = sseRaw(r.Context(), w, flusher, "data: "+mustJSON(map[string]any{"error": map[string]any{"message": msg, "code": code}})+"\n\n")
			_ = sseRaw(r.Context(), w, flusher, "data: [DONE]\n\n")
			return
		}
		if res.Throttling != nil && s.accountPool != nil {
			s.accountPool.UpdateThrottling(acc.ID, res.Throttling)
			s.logThrottlingWarning(acc.ID, res.Throttling)
		}
		if isContentPolicyBlock(res.Text) {
			log.Printf("[content-policy] M365 blocked the request (streaming), sending error")
			_ = sseRaw(r.Context(), w, flusher, "data: "+mustJSON(map[string]any{"error": map[string]any{"message": "M365 content policy blocked this request; try again or switch account", "code": "upstream_content_blocked"}})+"\n\n")
			_ = sseRaw(r.Context(), w, flusher, "data: [DONE]\n\n")
			return
		}
		if isImageLimitNotice(res.Text) {
			if s.accountPool != nil {
				s.accountPool.MarkImageLimited(acc.ID)
			}
		}
		if text.Len() == 0 && strings.TrimSpace(res.Text) != "" {
			text.WriteString(res.Text)
		}
		rawCalls := streamedTools
		calls, rejected := validateCalls("stream", rawCalls)
		toolResult := chathub.Result{Text: text.String()}
		if len(calls) == 0 && rejected > 0 {
			log.Printf("[tool-validation] id=%s stage=stream rejected structured tool event", requestID)
			_ = sseRaw(r.Context(), w, flusher, "data: "+mustJSON(map[string]any{"error": map[string]any{"message": "upstream returned an invalid structured tool event", "code": "invalid_tool_call"}})+"\n\n")
			_ = sseRaw(r.Context(), w, flusher, "data: [DONE]\n\n")
			return
		}
		if len(calls) > 0 {
			log.Printf("[req-trace] id=%s stage=tool_calls_detected count=%d names=%v", requestID, len(calls), func() []string {
				var n []string
				for _, c := range calls {
					n = append(n, c.Name)
				}
				return n
			}())
			calls = limitToolCalls(calls, adaptiveToolCallLimit(calls, configuredToolCallLimit(s.settings)))
			if body.ParallelToolCalls != nil && !*body.ParallelToolCalls && len(calls) > 1 {
				calls = calls[:1]
			}
			_ = writeToolResponse(w, id, model, true, body.shouldSendStreamUsage(), calls, toolResult)
			if body.User != "" && res.ConversationID != "" {
				s.userSessions.Put(tenantFromRequest(r), body.User, res.ConversationID, res.SessionID, acc.ID)
			}
			s.bindConversation(acc, &body, r, res, answerPrompt, startedAt)
			s.storeConvCache(convCacheNamespace, acc.ID, convCacheModel, res, tone, body.Messages, convReused)
			return
		}
		finishChunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}
		if res.Throttling != nil {
			finishChunk["x_m365_throttling"] = res.Throttling
		}
		if len(res.Scores) > 0 {
			finishChunk["x_m365_scores"] = res.Scores
		}
		_ = sw.data(mustJSON(finishChunk))
		_ = sw.data("[DONE]")
		if res.Timestamps.RequestSent != "" {
			_ = sw.raw(": m365-metrics " + mustJSON(res.Timestamps) + "\n\n")
		}
		if body.User != "" && res.ConversationID != "" {
			s.userSessions.Put(tenantFromRequest(r), body.User, res.ConversationID, res.SessionID, acc.ID)
		}
		s.bindConversation(acc, &body, r, res, answerPrompt, startedAt)
		s.storeConvCache(convCacheNamespace, acc.ID, convCacheModel, res, tone, body.Messages, convReused)
		return
	}
	answerReq := buildAnswerRequest(answerPrompt, tone, body, activeLedger, planningMode, mcpServerURL, s.settings.get(), s.featureFlags(), localeInfo, body.Metadata != nil && body.Metadata.CopilotTempSession)
	answerPrompt = answerReq.Text
	var res chathub.Result
	if body.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeOpenAIError(w, http.StatusInternalServerError, "server_error", "stream unsupported")
			return
		}
		id := "chatcmpl-" + uuid.NewString()
		model := firstNonEmpty(body.Model, "m365-copilot")
		firstDelta := true
		sw2 := newSSEWriter(w, flusher)
		writeChunk := func(delta map[string]any) error {
			if err := r.Context().Err(); err != nil {
				return err
			}
			// The first SSE chunk must carry the assistant role; subsequent
			// chunks carry content or reasoning deltas.
			if firstDelta {
				firstDelta = false
				withRole := map[string]any{"role": "assistant", "content": nil}
				for k, v := range delta {
					withRole[k] = v
				}
				delta = withRole
			}
			chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []map[string]any{{"index": 0, "delta": delta}}}
			return sw2.data(mustJSON(chunk))
		}
		contentFilter := newPublicIdentityStreamFilter(firstNonEmpty(body.Model, defaultPublicModelName))
		reasoningFilter := newPublicReasoningStreamFilter()
		onDelta := func(content string) error {
			if content = contentFilter.Push(content); content != "" {
				return writeChunk(map[string]any{"content": content})
			}
			return nil
		}
		onReasoning := func(reasoning string) error {
			if reasoning = reasoningFilter.Push(reasoning); reasoning != "" {
				return writeChunk(map[string]any{"reasoning_content": reasoning})
			}
			return nil
		}
		if err := sseRaw(r.Context(), w, flusher, ": connected\n\n"); err != nil {
			return
		}
		ticker2 := time.NewTicker(15 * time.Second)
		defer ticker2.Stop()
		keepaliveDone2 := make(chan struct{})
		defer close(keepaliveDone2)
		go func() {
			for {
				select {
				case <-keepaliveDone2:
					return
				case <-r.Context().Done():
					return
				case <-ctx.Done():
					return
				case <-ticker2.C:
					_ = sw2.raw(": keepalive\n\n")
				}
			}
		}()
		streamedReasoningLen := 0
		onDeltaWrapped := func(content string) error {
			if content != "" {
				streamedReasoningLen += len(content)
			}
			return onDelta(content)
		}
		onReasoningWrapped := func(reasoning string) error {
			if reasoning != "" {
				streamedReasoningLen += len(reasoning)
			}
			return onReasoning(reasoning)
		}
		res, err = s.chatWithAccountReasoning(ctx, acc.ID, account, answerReq, onDeltaWrapped, onReasoningWrapped)
		if err != nil && streamedReasoningLen == 0 && requestedAccountID == "" && (IsRateLimited(err) || IsAuthFailure(err)) {
			originalErr := err
			if s.accountPool != nil {
				if errors.Is(originalErr, chathub.ErrImageLimit) || IsImageLimitErr(originalErr) {
					s.accountPool.MarkImageLimited(acc.ID)
				} else {
					s.accountPool.MarkFailure(acc.ID, originalErr, s.getRateLimitCooldown())
				}
			}
			if convReused {
				s.invalidateConvCache(convCacheNamespace, acc.ID, convCacheModel)
				convReused = false
			}
			tried := map[string]bool{acc.ID: true}
			curID := acc.ID
			curErr := err
			for range s.tokens.List() {
				next, nerr := s.nextHealthyAccount(curID, accountIDs)
				if nerr != nil {
					break
				}
				if tried[next.ID] {
					break
				}
				tried[next.ID] = true
				failoverReq := answerReq
				failoverReq.ConversationID = ""
				failoverReq.SessionID = ""
				failoverReq.Text = prompt
				ctx2 := ctx
				if res2, err2 := s.chatWithAccountReasoning(ctx2, next.ID, chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}, failoverReq, onDelta, onReasoning); err2 == nil {
					res = res2
					acc = next
					account = chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}
					err = nil
					curErr = nil
					break
				} else {
					if s.accountPool != nil {
						if errors.Is(err2, chathub.ErrImageLimit) || IsImageLimitErr(err2) {
							s.accountPool.MarkImageLimited(next.ID)
						} else {
							s.accountPool.MarkFailure(next.ID, err2, s.getRateLimitCooldown())
						}
					}
					curErr = err2
					err = err2
					curID = next.ID
					if !IsRateLimited(err2) && !IsAuthFailure(err2) {
						break
					}
				}
			}
			if curErr == nil {
				err = nil
			} else {
				err = curErr
			}
		}
		if err == nil {
			if content := contentFilter.Flush(); content != "" {
				if writeErr := writeChunk(map[string]any{"content": content}); writeErr != nil {
					return
				}
			}
			if reasoning := reasoningFilter.Flush(); reasoning != "" {
				if writeErr := writeChunk(map[string]any{"reasoning_content": reasoning}); writeErr != nil {
					return
				}
			}
			res.Text = sanitizePublicAssistantTextForModel(res.Text, body.Model)
			res.Reasoning = sanitizePublicReasoningText(res.Reasoning)
			if res.Throttling != nil && s.accountPool != nil {
				s.accountPool.UpdateThrottling(acc.ID, res.Throttling)
				s.logThrottlingWarning(acc.ID, res.Throttling)
			}
			if isContentPolicyBlock(res.Text) {
				log.Printf("[content-policy] M365 blocked the request (reasoning stream), sending error")
				_ = sseRaw(r.Context(), w, flusher, "data: "+mustJSON(map[string]any{"error": map[string]any{"message": "M365 content policy blocked this request; try again or switch account", "code": "upstream_content_blocked"}})+"\n\n")
				_ = sseRaw(r.Context(), w, flusher, "data: [DONE]\n\n")
				return
			}
			if isImageLimitNotice(res.Text) {
				if s.accountPool != nil {
					s.accountPool.MarkImageLimited(acc.ID)
				}
			}
		} else {
			log.Printf("[req-trace] id=%s stage=stream_error err=%v", requestID, err)
			if errors.Is(err, chathub.ErrImageLimit) && s.accountPool != nil {
				s.accountPool.MarkImageLimited(acc.ID)
			}
			if convReused {
				s.invalidateConvCache(convCacheNamespace, acc.ID, convCacheModel)
			}
			msg := upstreamError(err)
			if IsRateLimited(err) {
				msg = "upstream is rate limiting; try again shortly"
			}
			if errors.Is(err, chathub.ErrOffensiveContent) {
				msg = "M365 content policy flagged this request as offensive"
			}
			msg = sanitizePublicInternalText(msg)
			code := "upstream_error"
			if IsRateLimited(err) {
				code = "rate_limit_error"
			} else if ClassifyError(err) == CategoryClientCanceled {
				code = "client_canceled"
			}
			_ = sseRaw(r.Context(), w, flusher, "data: "+mustJSON(map[string]any{"error": map[string]any{"message": msg, "code": code}})+"\n\n")
		}
		pt := EstimateTokens(prompt)
		ct := EstimateTokens(res.Text)
		log.Printf("[usage] stream id=%s pt=%d ct=%d res.Text=%d", id, pt, ct, len(res.Text))
		if err == nil && ct == 0 {
			_ = sseRaw(r.Context(), w, flusher, "data: "+mustJSON(map[string]any{"error": map[string]any{"message": "upstream returned empty completion; the requested model may be unavailable for this tenant", "code": "upstream_error"}})+"\n\n")
		}
		finish := "stop"
		if err != nil {
			finish = "error"
		}
		usageChunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct}}
		if res.Throttling != nil {
			usageChunk["x_m365_throttling"] = res.Throttling
		}
		if len(res.Scores) > 0 {
			usageChunk["x_m365_scores"] = res.Scores
		}
		_ = sw2.data(mustJSON(usageChunk))
		_ = sw2.data("[DONE]")
		if res.Timestamps.RequestSent != "" {
			_ = sw2.raw(": m365-metrics " + mustJSON(res.Timestamps) + "\n\n")
		}
	} else if isImageModel(body.Model) {
		currentAcc := acc
		var currentRes chathub.Result
		var currentErr error
		for attempt := 0; attempt < maxAccountProbe; attempt++ {
			reqCopy := answerReq
			reqCopy.Tone = "Magic"
			currentRes, currentErr = s.chatWithAccount(ctx, currentAcc.ID, chathub.Account{AccessToken: currentAcc.AccessToken, OID: currentAcc.OID, TID: currentAcc.TID}, reqCopy)
			if currentErr == nil {
				if len(currentRes.Images) == 0 {
					if urls := extractImageURLs(currentRes.RawResult); len(urls) > 0 {
						currentRes.Images = urls
					}
				}
				if len(currentRes.Images) == 0 {
					if urls := extractImageURLs(currentRes.Text); len(urls) > 0 {
						currentRes.Images = urls
					}
				}
			}
			if currentErr == nil && len(currentRes.Images) > 0 {
				res = currentRes
				acc = currentAcc
				err = nil
				break
			}
			log.Printf("[image-model-failover] account %s (%s) failed or returned no images (err=%v, images=%d), trying next account...", currentAcc.ID, currentAcc.Email, currentErr, len(currentRes.Images))
			if s.accountPool != nil {
				if errors.Is(currentErr, chathub.ErrImageLimit) || isImageLimitNotice(currentRes.Text) || IsImageLimitErr(currentErr) {
					s.accountPool.MarkImageLimited(currentAcc.ID)
				} else if currentErr != nil {
					s.accountPool.MarkFailure(currentAcc.ID, currentErr, s.getRateLimitCooldown())
				}
			}
			if requestedAccountID != "" {
				res = currentRes
				acc = currentAcc
				err = currentErr
				break
			}
			next, nerr := s.nextHealthyImageAccount(currentAcc.ID, accountIDs)
			if nerr != nil || next.ID == "" {
				log.Printf("[image-model-failover] no more healthy image accounts available after attempt %d", attempt+1)
				res = currentRes
				acc = currentAcc
				err = currentErr
				break
			}
			currentAcc = next
		}
	} else {
		res, err = s.chatWithAccount(ctx, acc.ID, account, answerReq)
		if IsEmptyCompletion(err) && tone != "magic" {
			log.Printf("[tone-fallback] tone=%q returned empty, retrying with magic", tone)
			magicReq := answerReq
			magicReq.Tone = "magic"
			if res2, err2 := s.chatWithAccount(ctx, acc.ID, account, magicReq); err2 == nil && res2.Text != "" {
				res = res2
				err = nil
			}
		}
		if err != nil && requestedAccountID == "" && (IsRateLimited(err) || IsAuthFailure(err)) {
			originalErr := err
			if s.accountPool != nil {
				if errors.Is(originalErr, chathub.ErrImageLimit) || IsImageLimitErr(originalErr) {
					s.accountPool.MarkImageLimited(acc.ID)
				} else {
					s.accountPool.MarkFailure(acc.ID, originalErr, s.getRateLimitCooldown())
				}
			}
			if convReused {
				s.invalidateConvCache(convCacheNamespace, acc.ID, convCacheModel)
				convReused = false
			}
			tried := map[string]bool{acc.ID: true}
			curID := acc.ID
			curErr := err
			var lastRes chathub.Result
			for range s.tokens.List() {
				next, nerr := s.nextHealthyAccount(curID, accountIDs)
				if nerr != nil {
					break
				}
				if tried[next.ID] {
					break
				}
				tried[next.ID] = true
				failoverReq := answerReq
				failoverReq.ConversationID = ""
				failoverReq.SessionID = ""
				failoverReq.Text = prompt
				ctx2 := ctx
				res2, err2 := s.chatWithAccount(ctx2, next.ID, chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}, failoverReq)
				if err2 == nil {
					res = res2
					acc = next
					account = chathub.Account{AccessToken: next.AccessToken, OID: next.OID, TID: next.TID}
					err = nil
					curErr = nil
					lastRes = res2
					break
				} else {
					if s.accountPool != nil {
						if errors.Is(err2, chathub.ErrImageLimit) || IsImageLimitErr(err2) {
							s.accountPool.MarkImageLimited(next.ID)
						} else {
							s.accountPool.MarkFailure(next.ID, err2, s.getRateLimitCooldown())
						}
					}
					err = err2
					curErr = err2
					curID = next.ID
					if !IsRateLimited(err2) && !IsAuthFailure(err2) {
						break
					}
				}
			}
			if curErr == nil {
				err = nil
				res = lastRes
			} else {
				err = curErr
			}
		}
	}
	if err != nil {
		if errors.Is(err, chathub.ErrImageLimit) && s.accountPool != nil {
			s.accountPool.MarkImageLimited(acc.ID)
		}
		if convReused {
			s.invalidateConvCache(convCacheNamespace, acc.ID, convCacheModel)
			log.Printf("[conv-cache] invalidated account=%s model=%s after error: %v", acc.ID, convCacheModel, err)
		}
		writeUpstreamErrorWithAccount(w, err, acc.ID)
		return
	}
	if res.Throttling != nil && s.accountPool != nil {
		s.accountPool.UpdateThrottling(acc.ID, res.Throttling)
		s.logThrottlingWarning(acc.ID, res.Throttling)
	}
	if body.Stream {
		if body.User != "" && res.ConversationID != "" {
			s.userSessions.Put(tenantFromRequest(r), body.User, res.ConversationID, res.SessionID, acc.ID)
		}
		s.bindConversation(acc, &body, r, res, prompt, startedAt)
		s.storeConvCache(convCacheNamespace, acc.ID, convCacheModel, res, tone, body.Messages, convReused)
		return
	}

	if body.SessionKey != "" {
		s.sessions.upsert(conversation{ID: body.SessionKey, AccountID: acc.ID, ConversationID: res.ConversationID, SessionID: res.SessionID, Title: prompt})
	}
	if body.User != "" && res.ConversationID != "" {
		s.userSessions.Put(tenantFromRequest(r), body.User, res.ConversationID, res.SessionID, acc.ID)
		log.Printf("[user-session] put user=%s conversation=%s session=%s", body.User, res.ConversationID, res.SessionID)
	}
	if res.ConversationID != "" {
		s.bindConversation(acc, &body, r, res, prompt, startedAt)
		s.storeConvCache(convCacheNamespace, acc.ID, convCacheModel, res, tone, body.Messages, convReused)
	}
	if res.ConversationID != "" {
		resolved := s.sessionResolver.Resolve(r, &body)
		if !resolved.IsNew {
			w.Header().Set(sessionHeaderName, resolved.SessionID)
		}
	}
	model := body.Model
	if model == "" {
		model = "m365-copilot"
	}
	id := "chatcmpl-" + uuid.NewString()
	if len(toolMaps) > 0 && isToolRefusal(res.Text) {
		log.Printf("[tool-eject] model refused tools, retrying with correction")
		toolList := strings.Join(declaredToolNames(toolMaps), ", ")
		correction := fmt.Sprintf("Your previous response incorrectly denied that caller tools are available. The following client-side tools are real, active, and callable in the caller's environment: [%s]. Call the appropriate tool now. Do not explain tool availability.\n\nUser request:\n%s", toolList, prompt)
		res2, err2 := s.chatWithAccount(ctx, acc.ID, account, chathub.Request{Text: correction, Tone: tone, Attachments: body.Attachments, LicenseType: toolCfg.LicenseType, Scenario: toolCfg.Scenario})
		if err2 == nil && !isToolRefusal(res2.Text) {
			res = res2
		}
	}
	if len(toolMaps) > 0 && isSandboxHallucination(res.Text) {
		log.Printf("[sandbox-eject] model used code interpreter/sandbox, retrying with explicit tool instruction")
		toolList := strings.Join(declaredToolNames(toolMaps), ", ")
		correction := fmt.Sprintf("CRITICAL: You must NOT use any built-in code interpreter, Python sandbox, or cloud execution environment. You must NOT assume files or commands are limited to /mnt/data or a Linux cloud container. The caller has provided the following real, client-side tools that execute directly in their local environment: [%s]. Use these tools to perform actions or access files. Do NOT say you cannot run code or access files. Call the appropriate declared tool now.\n\nUser request:\n%s", toolList, prompt)
		res2, err2 := s.chatWithAccount(ctx, acc.ID, account, chathub.Request{Text: correction, Tone: tone, Attachments: body.Attachments, LicenseType: toolCfg.LicenseType, Scenario: toolCfg.Scenario})
		if err2 == nil && !isSandboxHallucination(res2.Text) {
			res = res2
		}
	}
	invalidDetectedTool := false
	if rawCalls := nativeToolCalls(res.Events, body.Tools); len(rawCalls) > 0 {
		calls, rejected := validateCalls("native", rawCalls)
		invalidDetectedTool = invalidDetectedTool || rejected > 0
		if len(calls) > 0 {
			calls = limitToolCalls(calls, adaptiveToolCallLimit(calls, configuredToolCallLimit(s.settings)))
			if body.ParallelToolCalls != nil && !*body.ParallelToolCalls && len(calls) > 1 {
				calls = calls[:1]
			}
			_ = writeToolResponse(w, id, model, body.Stream, body.shouldSendStreamUsage(), calls, res)
			return
		}
	}
	if invalidDetectedTool {
		writeOpenAIError(w, http.StatusBadGateway, "invalid_tool_call", "upstream returned an invalid structured tool event")
		return
	}
	if isContentPolicyBlock(res.Text) {
		log.Printf("[content-policy] M365 blocked the request, returning 503")
		writeOpenAIError(w, http.StatusServiceUnavailable, "upstream_content_blocked", "M365 content policy blocked this request; try again or switch account")
		return
	}
	if isImageLimitNotice(res.Text) {
		if s.accountPool != nil {
			s.accountPool.MarkImageLimited(acc.ID)
		}
	}
	if len(res.Images) == 0 {
		if urls := extractImageURLs(res.RawResult); len(urls) > 0 {
			res.Images = urls
		}
	}
	if len(res.Images) == 0 {
		if urls := extractImageURLs(res.Text); len(urls) > 0 {
			res.Images = urls
		}
	}
	for _, imgURL := range res.Images {
		if !strings.Contains(res.Text, imgURL) {
			res.Text = strings.TrimSpace(res.Text) + "\n\n![image](" + imgURL + ")"
		}
	}
	res.Text = sanitizePublicAssistantTextForModel(res.Text, body.Model)
	res.Reasoning = sanitizePublicReasoningText(res.Reasoning)
	log.Printf("[debug] res.Text bytes=%d content=%q", len(res.Text), res.Text)
	created := time.Now().Unix()

	if body.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeOpenAIError(w, http.StatusInternalServerError, "server_error", "stream unsupported")
			return
		}
		sw3 := newSSEWriter(w, flusher)
		ticker3 := time.NewTicker(15 * time.Second)
		defer ticker3.Stop()
		keepaliveDone3 := make(chan struct{})
		defer close(keepaliveDone3)
		go func() {
			for {
				select {
				case <-keepaliveDone3:
					return
				case <-r.Context().Done():
					return
				case <-ticker3.C:
					_ = sw3.raw(": keepalive\n\n")
				}
			}
		}()
		// one-shot "stream" — emit full content then done
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"role": "assistant", "content": res.Text},
			}},
		}
		b, _ := json.Marshal(chunk)
		_ = sw3.data(string(b))
		pt := EstimateTokens(prompt)
		ct := EstimateTokens(res.Text)
		usageChunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct}}
		if res.Throttling != nil {
			usageChunk["x_m365_throttling"] = res.Throttling
		}
		if len(res.Scores) > 0 {
			usageChunk["x_m365_scores"] = res.Scores
		}
		_ = sw3.data(mustJSON(usageChunk))
		_ = sw3.data("[DONE]")
		if res.Timestamps.RequestSent != "" {
			_ = sw3.raw(": m365-metrics " + mustJSON(res.Timestamps) + "\n\n")
		}
		return
	}

	if responseFormat != nil && (responseFormat.Type == "json_object" || responseFormat.Type == "json_schema") {
		res.Text = normalizeJSONText(res.Text)
	}
	content := any(res.Text)
	if len(res.Images) > 0 {
		parts := []any{map[string]any{"type": "text", "text": res.Text}}
		for _, u := range res.Images {
			du, _ := downloadImageAsDataURIWithToken(u, acc.AccessToken)
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": du}})
		}
		content = parts
	}
	assistant := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if res.Reasoning != "" {
		assistant["reasoning_content"] = res.Reasoning
	}
	// 上游 ChatHub 不返回 token 计数，按请求/回复文本本地估算填充
	// OpenAI 要求的 usage 字段。
	pt := EstimateTokens(prompt)
	ct := EstimateTokens(res.Text)
	if res.Timestamps.RequestSent != "" {
		w.Header().Set("X-M365-Metrics", mustJSON(res.Timestamps))
	}
	if res.Throttling != nil {
		if b, err := json.Marshal(res.Throttling); err == nil {
			w.Header().Set("X-M365-Throttling", string(b))
		}
	}
	if len(res.Scores) > 0 {
		if b, err := json.Marshal(res.Scores); err == nil {
			w.Header().Set("X-M365-Scores", string(b))
		}
	}
	jsonOut(w, map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       assistant,
			"finish_reason": "stop",
		}},
		"m365": compatM365Metadata(res),
		"usage": map[string]any{
			"prompt_tokens":     pt,
			"completion_tokens": ct,
			"total_tokens":      pt + ct,
		},
	})
}

func (s *Server) writePublicIdentityChatResponse(w http.ResponseWriter, r *http.Request, body *oaiReq, prompt, answer string, startedAt time.Time) {
	model := firstNonEmpty(body.Model, defaultPublicModelName)
	id := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()
	inputTokens := EstimateTokens(prompt)
	outputTokens := EstimateTokens(answer)
	usage := map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens}
	if s.usage != nil {
		s.usage.record(UsageRecord{
			Time:         time.Now(),
			APIKeyPrefix: extractAPIKey(r),
			Model:        model,
			Endpoint:     "/v1/chat/completions",
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			DurationMs:   time.Since(startedAt).Milliseconds(),
			Status:       http.StatusOK,
		})
	}
	if !body.Stream {
		jsonOut(w, map[string]any{
			"id":      id,
			"object":  "chat.completion",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": answer},
				"finish_reason": "stop",
			}},
			"usage": usage,
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", "stream unsupported")
		return
	}
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "content": answer},
			"finish_reason": nil,
		}},
	}
	_ = sseRaw(r.Context(), w, flusher, "data: "+mustJSON(chunk)+"\n\n")
	finish := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": usage}
	_ = sseRaw(r.Context(), w, flusher, "data: "+mustJSON(finish)+"\n\n")
	_ = sseRaw(r.Context(), w, flusher, "data: [DONE]\n\n")
}

const defaultPublicModelName = "m365-copilot"

const sessionHeaderName = "X-M365-Session-Id"

// bindConversation 在请求完成后登记会话解析器索引与缓存统计，流式与非流式
// 路径共用。会话为内容键，云端的对话由 auto_cleanup 按 2h 闲置窗口回收，
// 这里不再做"用完即删"，否则复用永远不可能命中。
func (s *Server) bindConversation(acc auth.AccountToken, body *oaiReq, r *http.Request, res chathub.Result, prompt string, startedAt time.Time) {
	if res.ConversationID == "" {
		return
	}
	historyBody := *body
	historyBody.Messages = append(cloneMessages(body.Messages), oaiMsg{
		Role:             "assistant",
		Content:          res.Text,
		ReasoningContent: res.Reasoning,
	})
	s.sessionResolver.Bind(res.SessionID, res.ConversationID, acc.ID, &historyBody, "", r)
	s.conversationManager.Record(res.ConversationID, acc.ID, conversationTitle(body.Messages))
	if s.conversationManager.ShouldCleanup() {
		if cleaned := s.conversationManager.Cleanup(); len(cleaned) > 0 {
			log.Printf("[conversation-manager] auto-cleaned %d conversations", len(cleaned))
		}
	}

	apiKey := extractAPIKey(r)
	historyTokens := int64(0)
	upper := len(body.Messages) - 1
	if upper < 0 {
		upper = 0
	}
	for _, msg := range body.Messages[:upper] {
		historyTokens += EstimateTokens(contentToString(msg.Content))
	}
	newTokens := EstimateTokens(prompt)
	sessions := s.sessionResolver.ListSessions()
	cacheStats.RecordRequest(apiKey, historyTokens > 0, newTokens, historyTokens, len(sessions))
	s.usage.record(UsageRecord{
		Time:         time.Now(),
		APIKeyPrefix: apiKey,
		AccountEmail: acc.Email,
		Model:        firstNonEmpty(body.Model, "m365-copilot"),
		Endpoint:     "/v1/chat/completions",
		Stream:       body.Stream,
		InputTokens:  newTokens,
		OutputTokens: EstimateTokens(res.Text),
		CacheTokens:  historyTokens,
		DurationMs:   time.Since(startedAt).Milliseconds(),
		Status:       200,
	})
}

func extractAPIKey(r *http.Request) string {
	key := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if key != "" {
		return key
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		key = strings.TrimSpace(auth[7:])
	}
	if len(key) > 8 {
		return key[:8] + "..."
	}
	return key
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

type chathubLocale struct {
	Locale         string
	Market         string
	TimeZone       string
	TimeZoneOffset int
	DeviceOS       string
}

func parseLocaleFromHeaders(r *http.Request) chathubLocale {
	loc := chathubLocale{}
	if override := strings.TrimSpace(r.Header.Get("X-M365-Locale")); override != "" {
		loc.Locale = strings.ToLower(override)
	} else {
		loc.Locale = strings.TrimSpace(r.Header.Get("Accept-Language"))
		if loc.Locale == "" {
			loc.Locale = "en-us"
		} else {
			if idx := strings.Index(loc.Locale, ";"); idx >= 0 {
				loc.Locale = strings.TrimSpace(loc.Locale[:idx])
			}
			if idx := strings.Index(loc.Locale, ","); idx >= 0 {
				loc.Locale = strings.TrimSpace(loc.Locale[:idx])
			}
			loc.Locale = strings.ToLower(loc.Locale)
		}
	}
	loc.Market = strings.TrimSpace(r.Header.Get("X-M365-Market"))
	if loc.Market == "" {
		loc.Market = "en-us"
	} else {
		loc.Market = strings.ToLower(strings.TrimSpace(loc.Market))
	}
	tz := strings.TrimSpace(r.Header.Get("X-M365-TimeZone"))
	if tz != "" {
		loc.TimeZone = tz
		if l, err := time.LoadLocation(tz); err == nil {
			_, offset := time.Now().In(l).Zone()
			loc.TimeZoneOffset = offset / 3600
		}
	} else {
		loc.TimeZone = "UTC"
		loc.TimeZoneOffset = 0
	}
	loc.DeviceOS = strings.TrimSpace(r.Header.Get("X-M365-DeviceOS"))
	if loc.DeviceOS == "" {
		loc.DeviceOS = "Windows"
	}
	return loc
}

func extractOIDTID(accessToken string) (oid, tid string) {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", ""
	}
	if v, ok := m["oid"].(string); ok {
		oid = v
	}
	if v, ok := m["tid"].(string); ok {
		tid = v
	}
	return oid, tid
}
