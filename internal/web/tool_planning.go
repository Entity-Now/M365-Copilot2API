package web

import "strings"

func toolPlanningMode(raw string) string {
	// HAR evidence does not contain a complete native client-tool invocation
	// lifecycle or stable call identifier. Keep the verified router path as the
	// only selectable mode instead of silently enabling an inferred protocol.
	_ = strings.TrimSpace(raw)
	return "router"
}
