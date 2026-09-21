package web

import (
	"strings"
	"testing"
)

func TestFlattenPromptMessagesPreservesInstructionRolesAndOrder(t *testing.T) {
	messages := []oaiMsg{
		{Role: "system", Content: "system directive"},
		{Role: "developer", Content: "developer directive"},
		{Role: "user", Content: "request"},
	}

	prompt, _ := flattenPromptMessages(messages, nil)
	want := "<turn role=\"system\">\nsystem directive\n</turn>\n<turn role=\"developer\">\ndeveloper directive\n</turn>\n<turn role=\"user\">\nrequest\n</turn>"
	if prompt != want {
		t.Fatalf("prompt=%q want=%q", prompt, want)
	}
	if strings.Count(prompt, "system directive") != 1 || strings.Count(prompt, "developer directive") != 1 {
		t.Fatalf("instructions were lost or duplicated: %q", prompt)
	}
}

func TestFlattenPromptMessagesSingleUserTurnIsRaw(t *testing.T) {
	messages := []oaiMsg{
		{Role: "user", Content: "hello world"},
	}
	prompt, _ := flattenPromptMessages(messages, nil)
	if prompt != "hello world" {
		t.Fatalf("expected raw string, got %q", prompt)
	}
}

func TestFlattenPromptMessagesToolCallsAndResults(t *testing.T) {
	messages := []oaiMsg{
		{Role: "user", Content: "calculate"},
		{Role: "assistant", ToolCalls: []map[string]any{{"id": "call_1", "function": map[string]any{"name": "calc", "arguments": `{"x":1}`}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "result: 1"},
	}
	prompt, _ := flattenPromptMessages(messages, nil)
	if !strings.Contains(prompt, `<turn role="user">`) || !strings.Contains(prompt, `<tool_calls>`) || !strings.Contains(prompt, `<turn role="tool" tool_call_id="call_1">`) {
		t.Fatalf("unexpected prompt format: %s", prompt)
	}
}
