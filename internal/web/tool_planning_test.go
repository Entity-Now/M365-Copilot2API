package web

import "testing"

func TestToolPlanningModeDefaultsToRouter(t *testing.T) {
	for _, raw := range []string{"", "router", "ROUTER", "unexpected"} {
		if got := toolPlanningMode(raw); got != "router" {
			t.Fatalf("toolPlanningMode(%q)=%q, want router", raw, got)
		}
	}
}

func TestToolPlanningModeRejectsUnverifiedNativeMode(t *testing.T) {
	if got := toolPlanningMode(" native "); got != "router" {
		t.Fatalf("toolPlanningMode(native)=%q, want router", got)
	}
}

func TestToolPlanningModeSupportsRouterSlim(t *testing.T) {
	for _, raw := range []string{"router_slim", "ROUTER_SLIM", " router_slim "} {
		if got := toolPlanningMode(raw); got != "router_slim" {
			t.Fatalf("toolPlanningMode(%q)=%q, want router_slim", raw, got)
		}
	}
	if !isValidToolPlanningMode("router_slim") || !isValidToolPlanningMode("router") || !isValidToolPlanningMode("") {
		t.Fatal("expected valid modes to pass")
	}
	if isValidToolPlanningMode("invalid_mode") {
		t.Fatal("expected invalid mode to fail")
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
