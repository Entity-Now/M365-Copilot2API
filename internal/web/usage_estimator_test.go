package web

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"m365-copilot2api/internal/chathub"
)

func TestEstimateTokensUsesGPTTokenizer(t *testing.T) {
	count, source := tokenEstimator("gpt-5")
	if count == nil {
		t.Fatal("expected GPT token estimator")
	}
	if source != usageSourceTiktoken {
		t.Fatalf("expected %q source, got %q", usageSourceTiktoken, source)
	}

	for _, text := range []string{
		"hello world",
		"你好，世界",
		"function_call({\"value\":42})",
	} {
		want := int64(count(text))
		if got := EstimateTokens(text); got != want {
			t.Fatalf("EstimateTokens(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestEstimateTokensEmptyAndUnicode(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("EstimateTokens(empty) = %d, want 0", got)
	}
	if got := EstimateTokens("🙂"); got <= 0 {
		t.Fatalf("EstimateTokens(unicode) = %d, want positive count", got)
	}
}

func TestEstimateResponsesUsageRouterSlim(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")
	t.Setenv("M365_CONFIG_PATH", cfgPath)

	tool := chathub.Tool{
		Type: "function",
		Function: json.RawMessage(`{
			"name": "super_long_tool",
			"description": "This is a detailed description of the tool that goes on for a while.",
			"parameters": {
				"type": "object",
				"properties": {
					"prop1": {"type": "string", "description": "detail 1"},
					"prop2": {"type": "string", "description": "detail 2"},
					"prop3": {"type": "string", "description": "detail 3"},
					"prop4": {"type": "string", "description": "detail 4"}
				}
			}
		}`),
	}
	tools := []chathub.Tool{tool}

	// 1. Router mode
	settingsStore := openSettingsStore()
	cfg := settingsStore.get()
	cfg.ToolPlanningMode = "router"
	cfg.EnableCompactToolRouter = false
	cfg.EnableOnDemandToolSchema = false
	_ = settingsStore.save(cfg)

	estRouter := estimateResponsesUsage("m365-copilot", nil, tools, nil, "")
	routerTokens := estRouter.Values["input_tokens"].(int)

	// 2. Router slim mode
	cfg.ToolPlanningMode = "router_slim"
	_ = settingsStore.save(cfg)

	estSlim := estimateResponsesUsage("m365-copilot", nil, tools, nil, "")
	slimTokens := estSlim.Values["input_tokens"].(int)

	if slimTokens >= routerTokens {
		t.Fatalf("expected slim tokens (%d) < router tokens (%d)", slimTokens, routerTokens)
	}
}

