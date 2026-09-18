package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m365-copilot2api/internal/auth"
)

const graphDelegatedScope = "openid profile offline_access https://graph.microsoft.com/User.ReadWrite.All https://graph.microsoft.com/Organization.Read.All https://graph.microsoft.com/LicenseAssignment.ReadWrite.All"

const maxPendingGraphAuthorizations = 64

type graphAuthorizationData struct {
	TenantID              string    `json:"tenantId"`
	Organization          string    `json:"organization,omitempty"`
	ClientID              string    `json:"clientId"`
	EncryptedRefreshToken string    `json:"refreshToken"`
	Scopes                string    `json:"scopes"`
	AuthorizedAt          time.Time `json:"authorizedAt"`
	LastValidatedAt       time.Time `json:"lastValidatedAt,omitempty"`
}

func graphAuthorizationPath() string {
	if dir := strings.TrimSpace(os.Getenv("M365_DATA_DIR")); dir != "" {
		return filepath.Join(dir, "graph-authorization.json")
	}
	if p := strings.TrimSpace(os.Getenv("M365_CONFIG")); p != "" {
		return filepath.Join(filepath.Dir(p), "graph-authorization.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "m365-copilot2api", "graph-authorization.json")
}

func graphWizardClientID() string {
	return strings.TrimSpace(os.Getenv("M365_GRAPH_CLIENT_ID"))
}

func graphWizardRedirectURI(r *http.Request) string {
	if configured := strings.TrimSpace(os.Getenv("M365_GRAPH_REDIRECT_URI")); configured != "" {
		return configured
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/admin/graph/authorization/callback"
}

func loadGraphAuthorization() (graphAuthorizationData, error) {
	b, err := os.ReadFile(graphAuthorizationPath())
	if err != nil {
		return graphAuthorizationData{}, err
	}
	var data graphAuthorizationData
	if err := json.Unmarshal(b, &data); err != nil {
		return graphAuthorizationData{}, err
	}
	if data.EncryptedRefreshToken == "" {
		return graphAuthorizationData{}, fmt.Errorf("stored Graph authorization is incomplete")
	}
	return data, nil
}

func saveGraphAuthorization(data graphAuthorizationData) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(graphAuthorizationPath(), b, 0600)
}

func (s *Server) graphAuthorizationStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	if !auth.HasMasterKey() {
		writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "首次使用前需要初始化安全存储，请先完成安全设置")
		return
	}
	clientID := graphWizardClientID()
	if clientID == "" {
		writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "create an Entra app registration, configure its SPA redirect URI and set M365_GRAPH_CLIENT_ID")
		return
	}
	cookie, err := r.Cookie("m365_admin_session")
	if err != nil || cookie.Value == "" || !s.validAdminSession(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "administrator login required")
		return
	}
	verifier, err := auth.Verifier()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "could not initialize authorization")
		return
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "could not initialize authorization")
		return
	}
	state := hex.EncodeToString(random)
	redirectURI := graphWizardRedirectURI(r)
	s.mu.Lock()
	if s.graphAuthorizations == nil {
		s.graphAuthorizations = map[string]pendingGraphAuthorization{}
	}
	for key, pending := range s.graphAuthorizations {
		if time.Since(pending.Created) > 10*time.Minute {
			delete(s.graphAuthorizations, key)
		}
	}
	if len(s.graphAuthorizations) >= maxPendingGraphAuthorizations {
		s.mu.Unlock()
		writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_error", "too many pending Graph authorization requests")
		return
	}
	s.graphAuthorizations[state] = pendingGraphAuthorization{Verifier: verifier, Created: time.Now(), AdminSession: cookie.Value, RedirectURI: redirectURI}
	s.mu.Unlock()
	authorizeURL := auth.AuthorizationURL("https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize", clientID, redirectURI, state, auth.Challenge(verifier), graphDelegatedScope)
	u, _ := url.Parse(authorizeURL)
	q := u.Query()
	q.Set("prompt", "consent")
	u.RawQuery = q.Encode()
	jsonOut(w, map[string]any{"url": u.String(), "redirectUri": redirectURI, "permissionType": "delegated"})
}

func (s *Server) graphAuthorizationCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	cookie, cookieErr := r.Cookie("m365_admin_session")
	s.mu.Lock()
	pending, ok := s.graphAuthorizations[state]
	if ok {
		delete(s.graphAuthorizations, state)
	}
	s.mu.Unlock()
	if !ok || state == "" || code == "" || cookieErr != nil || cookie.Value == "" || cookie.Value != pending.AdminSession || time.Since(pending.Created) > 10*time.Minute || !s.validAdminSession(r) {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid or expired Graph authorization callback")
		return
	}
	tokenEndpoint := "https://login.microsoftonline.com/organizations/oauth2/v2.0/token"
	if configured := strings.TrimSpace(os.Getenv("M365_GRAPH_TOKEN_URL")); configured != "" {
		tokenEndpoint = configured
	}
	token, err := auth.ExchangeCodeAt(code, pending.Verifier, pending.RedirectURI, graphWizardClientID(), graphDelegatedScope, tokenEndpoint)
	if err != nil || token.RefreshToken == "" {
		writeOpenAIError(w, http.StatusBadGateway, "graph_error", "Graph authorization failed")
		return
	}
	encrypted, err := auth.EncryptSecret(token.RefreshToken)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "could not persist Graph authorization")
		return
	}
	data := graphAuthorizationData{TenantID: token.TenantID, ClientID: graphWizardClientID(), EncryptedRefreshToken: encrypted, Scopes: token.Scope, AuthorizedAt: time.Now()}
	if err := saveGraphAuthorization(data); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "could not persist Graph authorization")
		return
	}
	http.Redirect(w, r, "/?graph_authorized=1", http.StatusSeeOther)
}

func (s *Server) graphAuthorizationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	readiness := graphReadinessStatus()
	data, err := loadGraphAuthorization()
	if err != nil {
		if os.IsNotExist(err) {
			jsonOut(w, readiness)
			return
		}
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "could not read Graph authorization")
		return
	}
	result := map[string]any{"ready": readiness.Ready, "authorized": readiness.Authorized, "masterKeyConfigured": readiness.MasterKeyConfigured, "clientConfigured": readiness.ClientConfigured, "tenantId": data.TenantID, "organization": data.Organization, "scopes": data.Scopes, "authorizedAt": data.AuthorizedAt, "lastValidatedAt": data.LastValidatedAt, "permissionType": readiness.PermissionType, "applicationFallbackConfigured": readiness.ApplicationFallbackConfigured, "message": readiness.Message, "missingSteps": readiness.MissingSteps}
	jsonOut(w, result)
}

func (s *Server) graphAuthorizationRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	err := os.Remove(graphAuthorizationPath())
	if err != nil && !os.IsNotExist(err) {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "could not remove Graph authorization")
		return
	}
	s.mu.Lock()
	s.graphAuthorizations = map[string]pendingGraphAuthorization{}
	s.mu.Unlock()
	jsonOut(w, map[string]any{"revoked": true, "consentRevocationRequired": true})
}
