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
