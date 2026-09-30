package web

import (
	"strings"
	"testing"
)

func TestToolPlanningModeDefaultsToDirect(t *testing.T) {
	for _, raw := range []string{"", "direct", "DIRECT", "native", "NATIVE", "unexpected"} {
		if got := toolPlanningMode(raw); got != "direct" {
			t.Fatalf("toolPlanningMode(%q)=%q, want direct", raw, got)
		}
	}
}

func TestToolPlanningModeSupportsRouterAndSlim(t *testing.T) {
	if got := toolPlanningMode("router"); got != "router" {
		t.Fatalf("toolPlanningMode(router)=%q, want router", got)
	}
	if got := toolPlanningMode("router_slim"); got != "router_slim" {
		t.Fatalf("toolPlanningMode(router_slim)=%q, want router_slim", got)
	}
	for _, mode := range []string{"direct", "native", "router", "router_slim", ""} {
		if !isValidToolPlanningMode(mode) {
			t.Fatalf("expected mode %q to be valid", mode)
		}
	}
	if isValidToolPlanningMode("invalid_mode") {
		t.Fatal("expected invalid mode to fail")
	}
}

func TestFormatDirectToolPrompt(t *testing.T) {
	tools := testTools()
	prompt := formatDirectToolPrompt("what is the weather?", tools, "auto")
	if !strings.Contains(prompt, "<tools>") || !strings.Contains(prompt, "<tool_name>get_weather</tool_name>") {
		t.Fatalf("expected direct tools prompt to contain get_weather schema: %s", prompt)
	}
	if !strings.Contains(prompt, "<parameters>") || !strings.Contains(prompt, "city") {
		t.Fatalf("expected direct tools prompt to contain parameters schema: %s", prompt)
	}
	if strings.Contains(prompt, "CRITICAL: NO SANDBOX") {
		t.Fatal("direct tool prompt must not contain router negative constraints")
	}
}

func TestIsTrivialGreeting(t *testing.T) {
	cases := []struct {
		prompt string
		want   bool
	}{
		{"hi", true},
		{"Hello!", true},
		{"你好", true},
		{"ping", true},
		{"<turn role=\"system\">Some system prompt</turn>\n<turn role=\"user\">hi</turn>", true},
		{"<turn role=\"user\"><local-command-caveat>...</local-command-caveat><command-name>/clear</command-name>hi</turn>", true},
		{"hi, please search the repo for all auth functions", false},
		{"Check files in C:\\Users\\kang", false},
		{"what is the capital of France?", false},
	}
	for _, tc := range cases {
		if got := isTrivialGreeting(tc.prompt); got != tc.want {
			t.Errorf("isTrivialGreeting(%q) = %v, want %v", tc.prompt, got, tc.want)
		}
	}
}
