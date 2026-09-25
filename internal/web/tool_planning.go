package web

import (
	"regexp"
	"strings"
)

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

var reGreetingTags = regexp.MustCompile(`(?s)<[^>]+>.*?</[^>]+>|<[^>]+>`)

// isTrivialGreeting returns true if the prompt is purely a casual greeting
// that never needs tool invocation or workspace file inspection.
func isTrivialGreeting(prompt string) bool {
	text := prompt
	if strings.Contains(text, "<turn role=\"user\">") {
		idx := strings.LastIndex(text, "<turn role=\"user\">")
		if idx != -1 {
			text = text[idx+len("<turn role=\"user\">"):]
			if end := strings.Index(text, "</turn>"); end != -1 {
				text = text[:end]
			}
		}
	}
	text = reGreetingTags.ReplaceAllString(text, " ")
	p := strings.TrimSpace(strings.ToLower(text))
	p = strings.Trim(p, "!?.。？！~ \t\r\n`'\"")
	switch p {
	case "hi", "hello", "hey", "hi there", "hello there", "你好", "您好", "早上好", "下午好", "晚上好", "test", "ping":
		return true
	}
	return false
}
