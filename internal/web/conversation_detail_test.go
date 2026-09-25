package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m365-copilot2api/internal/auth"
	"m365-copilot2api/internal/chathub"
)

func TestConversationListAndDetailUseCompleteLocalHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("M365_SESSION_CACHE", filepath.Join(dir, "sessions.json"))
	t.Setenv("M365_CONVERSATION_CACHE", filepath.Join(dir, "conversations.json"))
	store, err := auth.OpenStore(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{tokens: store, sessionResolver: openSessionResolver()}

	s.cloudClients = newM365CloudClientManager()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer detail-key")
	req.Header.Set(sessionHeaderName, "session-detail")
	body := &oaiReq{Messages: []oaiMsg{
		{Role: "user", Content: "show the complete answer"},
		{Role: "assistant", Content: "complete body", ReasoningContent: "complete reasoning"},
	}}
	s.sessionResolver.Bind("", "conversation-detail", "account-a", body, "", req)

	listRecorder := httptest.NewRecorder()
	listRequest := httptest.NewRequest(http.MethodGet, "/api/m365/conversations", nil)
	listRequest.Header.Set("Authorization", "Bearer detail-key")
	s.handleM365Conversations(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var list struct {
		Count int              `json:"count"`
		Data  []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || list.Data[0]["messageCount"] != float64(2) {
		t.Fatalf("list response=%s", listRecorder.Body.String())
	}

	detailRecorder := httptest.NewRecorder()
	detailRequest := httptest.NewRequest(http.MethodGet, "/api/m365/conversations/detail?id=conversation-detail", nil)
	detailRequest.Header.Set("Authorization", "Bearer detail-key")
	s.handleM365ConversationDetail(detailRecorder, detailRequest)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
	var detail struct {
		ConversationID string   `json:"conversationId"`
		Messages       []oaiMsg `json:"messages"`
	}
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ConversationID != "conversation-detail" || len(detail.Messages) != 2 {
		t.Fatalf("detail response=%s", detailRecorder.Body.String())
	}
	if detail.Messages[1].ReasoningContent != "complete reasoning" || contentToString(detail.Messages[1].Content) != "complete body" {
		t.Fatalf("assistant message=%#v", detail.Messages[1])
	}
}

func TestConversationDetailPageContainsCompleteViews(t *testing.T) {
	body, err := os.ReadFile("../../web/conversation.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, needle := range []string{
		`id="conversationView"`,
		`id="jsonView"`,
		"reasoning_content",
		"tool_calls",
		"/api/m365/conversations/detail?id=",
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("conversation page missing %q", needle)
		}
	}
}

func TestConversationTimestampPrefersUpdateTime(t *testing.T) {
	created := time.Now().Add(-time.Hour).UnixMilli()
	updated := time.Now().UnixMilli()
	if got := conversationTimestamp(map[string]any{"createTimeUtc": created, "updateTimeUtc": updated}); got != updated {
		t.Fatalf("timestamp=%d want %d", got, updated)
	}
}

func TestM365CloudRefreshTokenChangeInvalidatesCachedAccessToken(t *testing.T) {
	client := NewM365CloudClient("client", "tenant", "old-refresh")
	client.accessToken = "cached-access"
	client.expiresAt = time.Now().Add(time.Hour)

	client.updateRefreshToken("new-refresh")

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.refreshToken != "new-refresh" {
		t.Fatalf("refresh token was not updated")
	}
	if client.accessToken != "" || !client.expiresAt.IsZero() {
		t.Fatalf("cached access token was not invalidated")
	}
}

func TestConversationTitleCleaning(t *testing.T) {
	tests := []struct {
		name     string
		messages []oaiMsg
		want     string
	}{
		{
			name: "xml turn with session tag",
			messages: []oaiMsg{
				{Role: "system", Content: "x-anthropic-billing-header: cc_version=2.1; You are a Claude agent..."},
				{Role: "user", Content: `<turn role="user"> <session> 我想学一下现在完成时的英语 </session> </turn>`},
			},
			want: "我想学一下现在完成时的英语",
		},
		{
			name: "bracket turn format",
			messages: []oaiMsg{
				{Role: "user", Content: "[user]\nHow does garbage collection work in Go?"},
			},
			want: "How does garbage collection work in Go?",
		},
		{
			name: "plain user text",
			messages: []oaiMsg{
				{Role: "user", Content: "Hello world"},
			},
			want: "Hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := conversationTitle(tt.messages)
			if got != tt.want {
				t.Fatalf("conversationTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConversationDetailAdminLookupAndTools(t *testing.T) {
	dir := t.TempDir()
	store, _ := auth.OpenStore(filepath.Join(dir, "accounts.json"))
	s := &Server{tokens: store, sessionResolver: openSessionResolver()}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer user-key")
	body := &oaiReq{
		Messages: []oaiMsg{
			{Role: "user", Content: "test prompt"},
			{Role: "assistant", Content: "test answer"},
		},
	}
	// Add tool
	body.Tools = []chathub.Tool{
		{Type: "function", Function: []byte(`{"name":"bash","description":"Execute command"}`)},
	}
	s.sessionResolver.Bind("", "admin-test-conv", "account-a", body, "", req)

	// Admin call has no Authorization header
	adminReq := httptest.NewRequest(http.MethodGet, "/api/m365/conversations/detail?id=admin-test-conv", nil)
	rec := httptest.NewRecorder()
	s.handleM365ConversationDetail(rec, adminReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res struct {
		ConversationID string           `json:"conversationId"`
		Messages       []oaiMsg         `json:"messages"`
		Tools          []map[string]any `json:"tools"`
		Router         map[string]any   `json:"router"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.ConversationID != "admin-test-conv" || len(res.Messages) != 2 {
		t.Fatalf("unexpected detail: %+v", res)
	}
	if len(res.Tools) != 1 {
		t.Fatalf("expected 1 tool in detail, got %d", len(res.Tools))
	}
	fn, _ := res.Tools[0]["function"].(map[string]any)
	if fn["name"] != "bash" {
		t.Fatalf("expected tool bash, got %v", fn["name"])
	}
	if res.Router == nil || res.Router["planningMode"] != "router" {
		t.Fatalf("expected router info, got %+v", res.Router)
	}
}

func TestExtractSkillsFromMessages(t *testing.T) {
	prompt := `<skills>
You can use specialized skills to help you.
- **SKILL.md** (required): The main instruction file.
- **scripts/** - Helper scripts.

Available skills:
- a11y-debugging (/path/to/a11y/SKILL.md): Uses Chrome DevTools MCP for accessibility (a11y) debugging.
- modern-web-guidance (/path/to/web/SKILL.md): Search tool for modern web development best practices.
  Trigger immediately for:
  - UI/Layout: Modals, dialogs
- test-runner: Runs tests automatically.
</skills>`

	messages := []oaiMsg{
		{Role: "system", Content: prompt},
		{Role: "user", Content: "Hello world"},
	}

	skills := extractSkillsFromMessages(messages)
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d: %+v", len(skills), skills)
	}

	if skills[0]["name"] != "a11y-debugging" || skills[0]["path"] != "/path/to/a11y/SKILL.md" {
		t.Errorf("skill[0] mismatch: %+v", skills[0])
	}
	if skills[1]["name"] != "modern-web-guidance" || !strings.Contains(skills[1]["description"], "Trigger immediately for") {
		t.Errorf("skill[1] mismatch: %+v", skills[1])
	}
	if skills[2]["name"] != "test-runner" || skills[2]["path"] != "" {
		t.Errorf("skill[2] mismatch: %+v", skills[2])
	}
}

func TestRouterDirectivesInfoIncludesToolsInPromptTemplate(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "fetch_file",
				"description": "Fetches a file content by path.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "The path to file"},
					},
				},
			},
		},
	}

	slimInfo := getRouterDirectivesInfo("router_slim", true, tools)
	slimTemplate, ok := slimInfo["promptTemplate"].(string)
	if !ok || !strings.Contains(slimTemplate, "- fetch_file: Fetches a file content by path.") {
		t.Fatalf("expected slim template to contain compact summary, got %v", slimTemplate)
	}
	// Verify parameters schema is NOT in the slim template
	if strings.Contains(slimTemplate, `"properties"`) || strings.Contains(slimTemplate, `"The path to file"`) {
		t.Fatalf("slim template must not leak parameter schemas: %s", slimTemplate)
	}

	// Verify conversation.html contains the router slim notice
	body, err := os.ReadFile("../../web/conversation.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	if !strings.Contains(page, "ROUTER_SLIM · 本地保留 / 上游精简") {
		t.Fatalf("conversation.html missing router slim badge")
	}
	if !strings.Contains(page, "工具精简模式 (Router Slim) 生效说明") {
		t.Fatalf("conversation.html missing router slim notice banner")
	}
}

