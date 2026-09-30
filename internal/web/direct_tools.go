package web

import (
	"encoding/json"
	"fmt"
	"strings"
)

// formatDirectToolPrompt injects the complete, uncompressed JSON schema of client tools
// into the prompt context using standard Anthropic-style <tools> definitions.
// It preserves all property descriptions, nested objects, enums, and constraints,
// completely avoiding the prompt pollution and schema loss of the two-stage router.
func formatDirectToolPrompt(prompt string, tools []map[string]any, choice any) string {
	if len(tools) == 0 || fmt.Sprint(choice) == "none" {
		return prompt
	}

	var sb strings.Builder
	sb.WriteString("# Available Tools\n\n")
	sb.WriteString("You have access to the following tools to help satisfy the user's request:\n\n<tools>\n")

	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)

		sb.WriteString("<tool_description>\n")
		sb.WriteString(fmt.Sprintf("<tool_name>%s</tool_name>\n", name))
		if desc != "" {
			sb.WriteString(fmt.Sprintf("<description>%s</description>\n", desc))
		}
		if params != nil {
			paramBytes, err := json.MarshalIndent(params, "", "  ")
			if err == nil {
				sb.WriteString("<parameters>\n")
				sb.Write(paramBytes)
				sb.WriteString("\n</parameters>\n")
			}
		}
		sb.WriteString("</tool_description>\n")
	}
	sb.WriteString("</tools>\n\n")

	sb.WriteString(`To call a tool, respond with a <tool_call> inside a <tool_calls> block:
<tool_calls>
<tool_call>
<name>tool_name</name>
<arguments>{"arg_name": "value"}</arguments>
</tool_call>
</tool_calls>

You may also call tools using fenced markdown code blocks:
` + "```" + `tool_name
{"arg_name": "value"}
` + "```\n\n")

	if fmt.Sprint(choice) == "required" {
		sb.WriteString("CRITICAL: You MUST call at least one tool to fulfill this request.\n\n")
	} else if m, ok := choice.(map[string]any); ok {
		if f, ok := m["function"].(map[string]any); ok {
			if n, ok := f["name"].(string); ok && n != "" {
				sb.WriteString(fmt.Sprintf("CRITICAL: You MUST call the tool %q to fulfill this request.\n\n", n))
			}
		}
	} else {
		sb.WriteString("If no tool is needed, respond directly with your answer.\n\n")
	}

	sb.WriteString(prompt)
	return sb.String()
}

// isToolCallStreamPrefix checks if the text begins with or is currently inside a tool call marker.
func isToolCallStreamPrefix(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if strings.HasPrefix(trimmed, "<tool_calls") ||
		strings.HasPrefix(trimmed, "<tool_call") ||
		strings.HasPrefix(trimmed, "CALL_TOOL:") ||
		strings.HasPrefix(trimmed, "```json") {
		return true
	}
	// Prefix might still be incomplete XML tag like "<tool"
	if strings.HasPrefix(trimmed, "<tool") {
		return true
	}
	return false
}
