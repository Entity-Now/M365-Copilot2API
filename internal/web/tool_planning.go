package web

import "strings"

const (
	ToolPlanningModeRouter     = "router"
	ToolPlanningModeRouterSlim = "router_slim"
)

func toolPlanningMode(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == ToolPlanningModeRouterSlim {
		return ToolPlanningModeRouterSlim
	}
	return ToolPlanningModeRouter
}

func isValidToolPlanningMode(mode string) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	return mode == "" || mode == ToolPlanningModeRouter || mode == ToolPlanningModeRouterSlim
}
