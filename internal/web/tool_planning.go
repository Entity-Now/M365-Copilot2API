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

// isTrivialGreeting returns true if the prompt is purely a casual greeting
// that never needs tool invocation or workspace file inspection.
func isTrivialGreeting(prompt string) bool {
	p := strings.TrimSpace(strings.ToLower(prompt))
	p = strings.Trim(p, "!?.。？！~ \t\r\n`'\"")
	switch p {
	case "hi", "hello", "hey", "hi there", "hello there", "你好", "您好", "早上好", "下午好", "晚上好", "test", "ping":
		return true
	}
	return false
}
