package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAdminEndpointsGetAndPut(t *testing.T) {
	st := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json"), v: defaultRuntimeSettings()}
	s := &Server{settings: st}

	// 1. GET /api/admin/endpoints
	req := httptest.NewRequest(http.MethodGet, "/api/admin/endpoints", nil)
	rec := httptest.NewRecorder()
	s.adminEndpoints(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var getRes struct {
		OK        bool              `json:"ok"`
		Endpoints []APIEndpointInfo `json:"endpoints"`
		Config    struct {
			EnableImageAPI       bool     `json:"enableImageApi"`
			EnableImageAPIPublic bool     `json:"enableImageApiPublic"`
			DefaultImageModel    string   `json:"defaultImageModel"`
			AvailableImageModels []string `json:"availableImageModels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &getRes); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}
	if !getRes.OK {
		t.Fatal("expected ok=true")
	}
	if len(getRes.Endpoints) == 0 {
		t.Fatal("expected non-empty endpoint catalog")
	}
	if len(getRes.Config.AvailableImageModels) == 0 {
		t.Fatal("expected availableImageModels in config")
	}

	// 2. PUT /api/admin/endpoints to update image API public and default model
	patchBody := `{"enableImageApiPublic": true, "defaultImageModel": "flux-4", "enableImageApi": true}`
	putReq := httptest.NewRequest(http.MethodPut, "/api/admin/endpoints", bytes.NewBufferString(patchBody))
	putRec := httptest.NewRecorder()
	s.adminEndpoints(putRec, putReq)

	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}

	cur := st.get()
	if !cur.EnableImageAPIPublic {
		t.Fatalf("expected EnableImageAPIPublic to be true, got %v", cur.EnableImageAPIPublic)
	}
	if cur.DefaultImageModel != "flux-4" {
		t.Fatalf("expected DefaultImageModel to be flux-4, got %q", cur.DefaultImageModel)
	}

	// 3. Verify getEndpointCatalog reflects the changes
	catalog := getEndpointCatalog(cur)
	var imgGen *APIEndpointInfo
	for i := range catalog {
		if catalog[i].ID == "image_generations" {
			imgGen = &catalog[i]
			break
		}
	}
	if imgGen == nil {
		t.Fatal("missing image_generations in catalog")
	}
	if imgGen.RequiresAuth {
		t.Fatal("expected image_generations RequiresAuth to be false when public enabled")
	}
}

func TestAdminMiddlewarePublicEndpointBypass(t *testing.T) {
	st := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json"), v: defaultRuntimeSettings()}
	s := &Server{
		settings:      st,
		adminPassword: "test-password",
		apiKeys:       newAPIKeyStore(filepath.Join(t.TempDir(), "keys.json")),
	}

	var handlerReached bool
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	})

	mw := s.adminMiddleware(testHandler)

	// Scenario A: EnableImageAPIPublic is false (default)
	cur := st.get()
	cur.EnableImageAPIPublic = false
	_ = st.save(cur)

	reqA := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"prompt":"test"}`))
	recA := httptest.NewRecorder()
	handlerReached = false
	mw.ServeHTTP(recA, reqA)

	if recA.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when public disabled, got %d: %s", recA.Code, recA.Body.String())
	}
	if handlerReached {
		t.Fatal("handler should not have been reached when unauthenticated")
	}

	// Scenario B: EnableImageAPIPublic is true
	cur.EnableImageAPIPublic = true
	_ = st.save(cur)

	reqB := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"prompt":"test"}`))
	recB := httptest.NewRecorder()
	handlerReached = false
	mw.ServeHTTP(recB, reqB)

	if recB.Code != http.StatusOK {
		t.Fatalf("expected 200 when public enabled, got %d: %s", recB.Code, recB.Body.String())
	}
	if !handlerReached {
		t.Fatal("handler should have been reached when public enabled")
	}
}
