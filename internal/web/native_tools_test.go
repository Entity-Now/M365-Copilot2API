package web

import (
	"encoding/json"
	"testing"

	"m365-copilot2api/internal/chathub"
)

func TestNativeToolCallsOnlyFromFrame(t *testing.T) {
	tools := []chathub.Tool{{Type: "function", Function: json.RawMessage(`{"name":"get_current_time","parameters":{"type":"object"}}`)}}
	events := []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"get_current_time","arguments":{"timezone":"Asia/Shanghai"}}}`)}
	c := nativeToolCalls(events, tools)
	if len(c) != 1 || c[0].Name != "get_current_time" || string(c[0].Arguments) != "{"+`"timezone":"Asia/Shanghai"`+"}" {
		t.Fatalf("%+v", c)
	}
	if len(nativeToolCalls([]json.RawMessage{json.RawMessage(`{"text":"现在几点"}`)}, tools)) != 0 {
		t.Fatal("inferred a tool call")
	}
}

func TestNativeToolCallsPreservesUpstreamCallID(t *testing.T) {
	tools := []chathub.Tool{{Type: "function", Function: json.RawMessage(`{"name":"get_current_time","parameters":{"type":"object"}}`)}}
	events := []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"id":"call_upstream_123","pluginName":"get_current_time","arguments":{"timezone":"Asia/Shanghai"}}}`)}
	c := nativeToolCalls(events, tools)
	if len(c) != 1 {
		t.Fatalf("got %d calls, want 1: %+v", len(c), c)
	}
	if c[0].ID != "call_upstream_123" {
		t.Fatalf("call ID=%q, want preserved upstream ID", c[0].ID)
	}
}

func TestNativeToolCallsDoesNotTreatIDAsToolName(t *testing.T) {
	tools := []chathub.Tool{{Type: "function", Function: json.RawMessage(`{"name":"call_upstream_123","parameters":{"type":"object"}}`)}}
	events := []json.RawMessage{json.RawMessage(`{"id":"call_upstream_123","arguments":{"timezone":"Asia/Shanghai"}}`)}
	if c := nativeToolCalls(events, tools); len(c) != 0 {
		t.Fatalf("treated correlation ID as a tool name: %+v", c)
	}
}
