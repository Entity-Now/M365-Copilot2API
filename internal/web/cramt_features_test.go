package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"m365-copilot2api/internal/chathub"
)

func TestModelToneOpusAndGpt6(t *testing.T) {
	cases := []struct {
		model    string
		wantTone string
	}{
		{"claude-opus", "Claude_Opus"},
		{"claude-opus-5", "Claude_Opus"},
		{"claude-opus-5[1m]", "Claude_Opus"},
		{"claude-opus-4.5", "Claude_Opus"},
		{"gpt-6", "Gpt_6_Reasoning"},
		{"gpt-6-reasoning", "Gpt_6_Reasoning"},
		{"gpt-6-think-deeper", "Gpt_6_Reasoning"},
		{"gpt-5.6", "Gpt_5_6_Chat"},
		{"gpt-5.6-quick", "Gpt_5_6_Chat"},
		{"gpt-5.6-think-deeper", "Gpt_5_6_Reasoning"},
		{"quick", "Gpt_5_5_Chat"},
		{"think-deeper", "Gpt_5_5_Reasoning"},
		{"auto", "magic"},
		{"m365-copilot", "magic"},
	}

	for _, tc := range cases {
		got := modelTone(tc.model)
		if got != tc.wantTone {
			t.Errorf("modelTone(%q) = %q, want %q", tc.model, got, tc.wantTone)
		}
	}
}

func TestPaidScenarioToneAutoElevation(t *testing.T) {
	if !chathub.IsPaidScenarioTone("Claude_Opus") {
		t.Errorf("expected Claude_Opus to be paid scenario tone")
	}
	if !chathub.IsPaidScenarioTone("Gpt_6_Reasoning") {
		t.Errorf("expected Gpt_6_Reasoning to be paid scenario tone")
	}
	if chathub.IsPaidScenarioTone("Gpt_5_5_Chat") {
		t.Errorf("did not expect Gpt_5_5_Chat to be paid scenario tone")
	}
}

func TestPriorityAccessExhaustionParsing(t *testing.T) {
	dailyRefusal := "You've used your available priority access to the Opus model for today. You can choose another available model or wait until tomorrow to use the Opus model again."
	weeklyRefusal := "You've used your available priority access to the Opus model for the week. You can choose another available model or wait until Monday to use the Opus model again."
	zhRefusal := "您已用完今天对 Opus 模型的优先访问权限。您可以选择其他可用模型，或等到明天再使用 Opus 模型。"

	now := time.Date(2026, 9, 29, 14, 0, 0, 0, time.UTC)

	ok, window, secs := chathub.ParsePriorityAccessExhaustion(dailyRefusal, now)
	if !ok || window != "day" || secs <= 0 {
		t.Fatalf("failed daily refusal detection: ok=%v, window=%s, secs=%d", ok, window, secs)
	}

	ok, window, secs = chathub.ParsePriorityAccessExhaustion(weeklyRefusal, now)
	if !ok || window != "week" || secs <= 0 {
		t.Fatalf("failed weekly refusal detection: ok=%v, window=%s, secs=%d", ok, window, secs)
	}

	ok, window, secs = chathub.ParsePriorityAccessExhaustion(zhRefusal, now)
	if !ok || window != "day" || secs <= 0 {
		t.Fatalf("failed zh refusal detection: ok=%v, window=%s, secs=%d", ok, window, secs)
	}

	// Normal text should not trigger
	ok, _, _ = chathub.ParsePriorityAccessExhaustion("Here is the answer to your question about priority queues in Go.", now)
	if ok {
		t.Errorf("normal text should not trigger priority access exhaustion")
	}
}

func TestCouldBePriorityAccessPrefix(t *testing.T) {
	if !chathub.CouldBePriorityAccessPrefix("You've used") {
		t.Errorf("expected prefix to match")
	}
	if !chathub.CouldBePriorityAccessPrefix("You have used your available priority access") {
		t.Errorf("expected prefix to match")
	}
	if chathub.CouldBePriorityAccessPrefix("Package main imports fmt and prints hello world.") {
		t.Errorf("normal content should not match prefix")
	}
}

func TestFabricatedToolResponseTruncation(t *testing.T) {
	input := "I'll run the command:\n```bash\nls -la\n```\n<tool_response>\nfake_result_here.txt\n</tool_response>\nDone! All files found."
	truncated := truncateAtFabricatedToolResponse(input)
	if strings.Contains(truncated, "<tool_response>") {
		t.Errorf("expected <tool_response> to be truncated, got: %s", truncated)
	}
	if strings.Contains(truncated, "Done! All files found") {
		t.Errorf("expected fabricated tail to be removed, got: %s", truncated)
	}
	if !strings.Contains(truncated, "ls -la") {
		t.Errorf("expected tool call to be preserved, got: %s", truncated)
	}
}

func TestFencedShellAliasesRouting(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "run_command"}},
	}

	// Test pwsh / powershell alias
	pwshText := "```pwsh\nGet-ChildItem -Path .\n```"
	calls := fencedToolCalls(pwshText, tools, "auto")
	if len(calls) != 1 || calls[0].Name != "run_command" {
		t.Fatalf("expected pwsh to route to run_command, got %+v", calls)
	}

	// Test zsh alias
	zshText := "```zsh\nfind . -name '*.go'\n```"
	calls = fencedToolCalls(zshText, tools, "auto")
	if len(calls) != 1 || calls[0].Name != "run_command" {
		t.Fatalf("expected zsh to route to run_command, got %+v", calls)
	}

	// Test container.exec alias
	containerText := "```container.exec\ncat config.json\n```"
	calls = fencedToolCalls(containerText, tools, "auto")
	if len(calls) != 1 || calls[0].Name != "run_command" {
		t.Fatalf("expected container.exec to route to run_command, got %+v", calls)
	}
}

func TestFencedHeadersAndSearchReplaceParsing(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "write_file"}},
		{"type": "function", "function": map[string]any{"name": "edit_file"}},
	}

	// Test headers + body
	writeText := "```write_file\npath: main.go\n\npackage main\nfunc main() {}\n```"
	calls := fencedToolCalls(writeText, tools, "auto")
	if len(calls) != 1 || calls[0].Name != "write_file" {
		t.Fatalf("expected write_file call, got %+v", calls)
	}
	var args map[string]any
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil {
		t.Fatalf("failed unmarshaling arguments: %v", err)
	}
	if args["path"] != "main.go" || !strings.Contains(args["content"].(string), "package main") {
		t.Fatalf("arguments mismatch: %+v", args)
	}

	// Test SEARCH/REPLACE diff
	editText := "```edit_file\npath: config.yaml\n<<<<<<< SEARCH\ndebug: false\n=======\ndebug: true\n>>>>>>> REPLACE\n```"
	calls = fencedToolCalls(editText, tools, "auto")
	if len(calls) != 1 || calls[0].Name != "edit_file" {
		t.Fatalf("expected edit_file call, got %+v", calls)
	}
	var editArgs map[string]any
	if err := json.Unmarshal(calls[0].Arguments, &editArgs); err != nil {
		t.Fatalf("failed unmarshaling arguments: %v", err)
	}
	if editArgs["path"] != "config.yaml" || editArgs["old"] != "debug: false" || editArgs["new"] != "debug: true" {
		t.Fatalf("edit arguments mismatch: %+v", editArgs)
	}
}

func TestProseDocumentGuardDoesNotExecuteMarkdownDoc(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "bash"}},
	}

	doc := `# Project Setup Guide

Here is how you set up the project:

` + "```bash\ngit clone https://example.com/repo.git\n```" + `

Then configure the environment:

` + "```bash\ncp .env.example .env\n```" + `

Next build the binaries:

` + "```bash\ngo build -o app ./cmd/server\n```" + `

Finally start the service:

` + "```bash\n./app --port 8080\n```"

	calls := fencedToolCalls(doc, tools, "auto")
	if len(calls) != 0 {
		t.Fatalf("markdown document with code examples must not be executed as tools, got %d calls", len(calls))
	}
}

func TestPublicIdentityGPT6AndClaudeOpus(t *testing.T) {
	ansGpt6 := publicIdentityAnswerForModel("gpt-6-reasoning", "en")
	if !strings.Contains(ansGpt6, "GPT-6") {
		t.Errorf("expected GPT-6 family in answer, got: %s", ansGpt6)
	}

	ansOpus := publicIdentityAnswerForModel("claude-opus-5", "en")
	if !strings.Contains(ansOpus, "Claude") {
		t.Errorf("expected Claude family in answer, got: %s", ansOpus)
	}
}
