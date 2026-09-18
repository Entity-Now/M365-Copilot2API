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
