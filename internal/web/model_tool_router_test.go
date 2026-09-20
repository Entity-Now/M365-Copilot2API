package web

import (
	"strings"
	"testing"
)

func TestParseModelToolDecisionAutoAndParallel(t *testing.T) {
	calls, ok := parseModelToolDecision(`{"calls":[{"name":"get_weather","arguments":{"city":"Beijing"}},{"name":"get_time","arguments":{"city":"Beijing"}}]}`, testTools(), "auto")
	if !ok || len(calls) != 2 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}
func TestParseModelToolDecisionNoCall(t *testing.T) {
	calls, ok := parseModelToolDecision(`{"calls":[]}`, testTools(), "auto")
	if !ok || len(calls) != 0 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}
func TestModelToolRouterPromptMarksCompletedResults(t *testing.T) {
	p := modelToolRouterPrompt(`assistant tool_calls: [...]
tool[call_x]: 2026-07-18`, testTools(), "auto")
	if !strings.Contains(p, "Completed evidence must not be repeated") || !strings.Contains(p, "tool[call_x]: 2026-07-18") || !strings.Contains(p, "unfinished work remains") {
		t.Fatalf("missing multi-turn evidence constraint: %s", p)
	}
}

func TestParseModelToolDecisionRejectsBadSchema(t *testing.T) {
	calls, ok := parseModelToolDecision("```json\n{\"calls\":[{\"name\":\"get_weather\",\"arguments\":{\"city\":2}}]}\n```", testTools(), "auto")
	if !ok || len(calls) != 0 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}

func TestParseModelToolDecisionMultipleCallToolLines(t *testing.T) {
	text := `I will check the weather and time for Beijing:
CALL_TOOL: get_weather({"city":"Beijing"})
CALL_TOOL: get_time({"city":"Beijing"})`
	calls, ok := parseModelToolDecision(text, testTools(), "auto")
	if !ok || len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d (ok=%v)", len(calls), ok)
	}
	if calls[0].Name != "get_weather" || calls[1].Name != "get_time" {
		t.Fatalf("unexpected call order/names: %v", calls)
	}
}

func TestParseModelToolDecisionMultilineArgs(t *testing.T) {
	text := `CALL_TOOL: get_weather({
		"city": "Beijing"
	})`
	calls, ok := parseModelToolDecision(text, testTools(), "auto")
	if !ok || len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d (ok=%v)", len(calls), ok)
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("unexpected tool name: %s", calls[0].Name)
	}
}

func TestModelToolRouterPromptSufficientContextConstraint(t *testing.T) {
	p := modelToolRouterPrompt("analyze project files", testTools(), "auto")
	if !strings.Contains(p, "Sufficient context required") {
		t.Fatalf("expected Sufficient context required in prompt: %s", p)
	}
}

