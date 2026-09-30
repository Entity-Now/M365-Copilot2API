package web

import (
	"strings"
	"testing"

	"m365-copilot2api/internal/chathub"
)

// collectStream feeds text fragments through streamEmitText the way the live
// SSE loop does and returns everything that was emitted to the client plus the
// residual pending buffer that a final flush would send.
func collectStream(fragments []string) string {
	emitted, pending := streamDuringAndPending(fragments)
	// Final flush mirrors emitText(pending.String()) in the server.
	return emitted + pending
}

// streamDuringAndPending returns (text emitted mid-stream, residual pending).
func streamDuringAndPending(fragments []string) (string, string) {
	var text, pending strings.Builder
	var emitted strings.Builder
	emit := func(part string) error {
		emitted.WriteString(part)
		return nil
	}
	for _, f := range fragments {
		_ = streamEmitText(chathub.StreamEvent{Kind: "text", Text: f}, &text, &pending, emit)
	}
	return emitted.String(), pending.String()
}

// A ```text diagram followed by more prose and a second ```text block must be
// delivered in full. The old buffering logic dropped everything after the first
// completed fence, so the client answer was truncated at the diagram — exactly
// the clash.yaml explanation regression.
func TestStreamEmitsTextFencesAndTrailingProse(t *testing.T) {
	full := "开头说明。\n\n```text\n浏览器 → Clash → 互联网\n```\n\n配置文件地址：\n\n```text\nhttp://1.2.3.4:8080/x/clash.yaml\n```\n\n其中 8080 只提供配置文件，443 才承载流量。结束。"
	// Split into many small deltas to simulate token-by-token streaming.
	var frags []string
	for _, r := range full {
		frags = append(frags, string(r))
	}
	got := collectStream(frags)
	if got != full {
		t.Fatalf("streamed output truncated.\n want %q\n  got %q", full, got)
	}
}

// Ordinary prose with no fences streams through unchanged.
func TestStreamPlainProse(t *testing.T) {
	full := "这是一段没有代码块的普通回答，应该原样输出。"
	var frags []string
	for _, r := range full {
		frags = append(frags, string(r))
	}
	if got := collectStream(frags); got != full {
		t.Fatalf("plain prose altered.\n want %q\n  got %q", full, got)
	}
}

func TestToolStreamStateSuppressesXMLToolCall(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "Read"}},
	}
	st := newToolStreamState(tools)
	var text strings.Builder
	var emitted strings.Builder
	emit := func(s string) error {
		emitted.WriteString(s)
		return nil
	}

	chunks := []string{
		"I will inspect the file.\n",
		"<tool_calls>",
		"<tool_call>",
		"<name>Read</name>",
		"<arguments>{\"file_path\":\"main.go\"}</arguments>",
		"</tool_call>",
		"</tool_calls>",
	}
	for _, c := range chunks {
		_ = st.processStreamChunk(chathub.StreamEvent{Kind: "text", Text: c}, &text, emit)
	}

	// The emitted text should ONLY be the prose, never the <tool_calls> XML!
	if strings.Contains(emitted.String(), "<tool_calls>") || strings.Contains(emitted.String(), "Read") {
		t.Fatalf("tool call XML leaked to emitted text: %q", emitted.String())
	}
	if emitted.String() != "I will inspect the file.\n" {
		t.Fatalf("expected prose to be emitted, got: %q", emitted.String())
	}
}

func TestToolStreamStateEmitsRegularProseWithTools(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "Read"}},
	}
	st := newToolStreamState(tools)
	var text strings.Builder
	var emitted strings.Builder
	emit := func(s string) error {
		emitted.WriteString(s)
		return nil
	}

	prose := "Quicksort is an in-place sorting algorithm that uses divide-and-conquer."
	for _, r := range prose {
		_ = st.processStreamChunk(chathub.StreamEvent{Kind: "text", Text: string(r)}, &text, emit)
	}
	_ = st.flushRemaining(&text, emit)

	if emitted.String() != prose {
		t.Fatalf("expected full prose emitted, got: %q", emitted.String())
	}
}

func TestToolStreamStateAllowsRegularJSONBlock(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "Read"}},
	}
	st := newToolStreamState(tools)
	var text strings.Builder
	var emitted strings.Builder
	emit := func(s string) error {
		emitted.WriteString(s)
		return nil
	}

	content := "Here is the configuration:\n\n```json\n{\n  \"port\": 8080,\n  \"host\": \"localhost\"\n}\n```\n\nEnjoy!"
	for _, r := range content {
		_ = st.processStreamChunk(chathub.StreamEvent{Kind: "text", Text: string(r)}, &text, emit)
	}
	_ = st.flushRemaining(&text, emit)

	if emitted.String() != content {
		t.Fatalf("expected regular json block to be emitted, got: %q\nwant: %q", emitted.String(), content)
	}
}

func TestToolStreamStateSuppressesToolNamedBlock(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "Read"}},
	}
	st := newToolStreamState(tools)
	var text strings.Builder
	var emitted strings.Builder
	emit := func(s string) error {
		emitted.WriteString(s)
		return nil
	}

	chunks := []string{
		"Calling tool:\n\n",
		"```Read\n",
		"{\"file_path\": \"foo.go\"}\n",
		"```",
	}
	for _, c := range chunks {
		_ = st.processStreamChunk(chathub.StreamEvent{Kind: "text", Text: c}, &text, emit)
	}

	if strings.Contains(emitted.String(), "```Read") || strings.Contains(emitted.String(), "foo.go") {
		t.Fatalf("tool block leaked to text stream: %q", emitted.String())
	}
	if emitted.String() != "Calling tool:\n\n" {
		t.Fatalf("expected only pre-tool text, got: %q", emitted.String())
	}
}

