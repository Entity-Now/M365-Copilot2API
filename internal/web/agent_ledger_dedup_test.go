package web

import "testing"

func TestCanonicalToolArgumentsDeduplicateEquivalentJSON(t *testing.T) {
	ledger := agentLedger{Completed: []toolEvidence{{
		Name:      "workspace_write_file",
		Arguments: `{"path":"main.go","content":"x"}`,
	}}}
	if !ledger.hasCompleted("workspace_write_file", ` { "content":"x", "path":"main.go" } `) {
		t.Fatal("equivalent JSON arguments were not deduplicated")
	}
}

func TestFilterCompletedCallsKeepsNewArguments(t *testing.T) {
	ledger := agentLedger{Completed: []toolEvidence{{
		Name:      "workspace_write_file",
		Arguments: `{"path":"main.go","content":"old"}`,
	}}}
	calls := []detectedToolCall{
		{Name: "workspace_write_file", Arguments: []byte(`{"path":"main.go","content":"old"}`)},
		{Name: "workspace_write_file", Arguments: []byte(`{"path":"main.go","content":"new"}`)},
	}
	got := filterCompletedCalls(calls, ledger)
	if len(got) != 1 || string(got[0].Arguments) != `{"path":"main.go","content":"new"}` {
		t.Fatalf("unexpected filtered calls: %#v", got)
	}
}

func TestRouterContextStaysCompact(t *testing.T) {
	ledger := agentLedger{Completed: []toolEvidence{{
		ID: "call_1", Name: "workspace_write_file", Arguments: `{"path":"main.go"}`, Result: "written successfully",
	}}}
	ctx := ledger.RouterContext()
	if len(ctx) > 2000 {
		t.Fatalf("router context unexpectedly large: %d bytes", len(ctx))
	}
	if len(ctx) == 0 {
		t.Fatal("router context is empty")
	}
}

func TestFilterCompletedCallsAllowsFailedRetries(t *testing.T) {
	ledger := agentLedger{Completed: []toolEvidence{{
		Name:      "workspace_write_file",
		Arguments: `{"path":"main.go","content":"x"}`,
		Failed:    true,
	}}}
	calls := []detectedToolCall{
		{Name: "workspace_write_file", Arguments: []byte(`{"path":"main.go","content":"x"}`)},
	}
	got := filterCompletedCalls(calls, ledger)
	if len(got) != 1 {
		t.Fatalf("failed tool call should be allowed to retry, got: %#v", got)
	}
}

func TestActiveLedgerAllowsCrossTurnRetries(t *testing.T) {
	msgs := []oaiMsg{
		{Role: "user", Content: "write main.go"},
		{Role: "assistant", ToolCalls: []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": "write_file", "arguments": "{\"path\":\"main.go\"}"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
		{Role: "user", Content: "please retry writing main.go"},
	}
	active := buildAgentLedger(activeMessages(msgs))
	calls := []detectedToolCall{
		{Name: "write_file", Arguments: []byte(`{"path":"main.go"}`)},
	}
	got := filterCompletedCalls(calls, active)
	if len(got) != 1 {
		t.Fatalf("new user turn should allow re-invoking tool, got: %#v", got)
	}
}
