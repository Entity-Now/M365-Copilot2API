package web

import "strings"

func toolPlanningMode(raw string) string {
	// HAR evidence does not contain a complete native client-tool invocation
	// lifecycle or stable call identifier. Keep the verified router path as the
	// only selectable mode instead of silently enabling an inferred protocol.
	_ = strings.TrimSpace(raw)
	return "router"
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
