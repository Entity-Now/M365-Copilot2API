package web

import (
	"encoding/json"
	"fmt"
	"strings"
)

// isGatewayInternalTool checks if the tool call is meant for the gateway itself
// rather than being forwarded to the client Agent.
func isGatewayInternalTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "describe_tool", "get_tool_schema", "inspect_tool", "search_tools", "list_tools":
		return true
	}
	return false
}

// gatewayInternalToolDefs returns the tool definitions for internal gateway inspection.
func gatewayInternalToolDefs() []string {
	return []string{
		"- describe_tool(tool_name: string): Inspect the complete parameter schema, types, and usage for a specific tool before calling it.",
		"- search_tools(query: string): Search tools by keyword or capability if you are unsure which tool to use.",
		"- list_tools(): List all available client tool names.",
	}
}

// formatOnDemandToolCatalog formats tools into a lightweight catalog with inspection tools.
func formatOnDemandToolCatalog(tools []map[string]any) (string, []string) {
	var lines []string
	var names []string

	lines = append(lines, "# Gateway Inspection Tools:")
	for _, def := range gatewayInternalToolDefs() {
		lines = append(lines, def)
	}
	lines = append(lines, "")
	lines = append(lines, "# Available Client Tools (run locally in caller's environment):")
	for _, t := range tools {
		f, _ := t["function"].(map[string]any)
		if f == nil {
			continue
		}
		name, _ := f["name"].(string)
		if name == "" {
			continue
		}
		names = append(names, name)
		desc, _ := f["description"].(string)
		desc = cleanToolDesc(desc)
		if desc != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", name, desc))
		} else {
			lines = append(lines, fmt.Sprintf("- %s", name))
		}
	}
	return strings.Join(lines, "\n"), names
}

// executeGatewayTool executes a gateway-internal tool locally in Go without forwarding to the client.
func executeGatewayTool(call detectedToolCall, clientTools []map[string]any) string {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	var args map[string]any
	_ = json.Unmarshal(call.Arguments, &args)
	if args == nil {
		args = map[string]any{}
	}

	switch name {
	case "describe_tool", "get_tool_schema", "inspect_tool":
		targetName, _ := args["tool_name"].(string)
		if targetName == "" {
			if v, ok := args["name"].(string); ok {
				targetName = v
			} else if v, ok := args["tool"].(string); ok {
				targetName = v
			}
		}
		targetName = strings.Trim(strings.TrimSpace(targetName), "\"'`")
		if targetName == "" {
			return `{"error": "missing required parameter 'tool_name'"}`
		}

		for _, t := range clientTools {
			f, _ := t["function"].(map[string]any)
			if f == nil {
				continue
			}
			fName, _ := f["name"].(string)
			if strings.EqualFold(fName, targetName) {
				b, _ := json.MarshalIndent(f, "", "  ")
				return fmt.Sprintf("Full specification for tool %q:\n```json\n%s\n```\nUse this exact parameter structure when calling %s.", fName, string(b), fName)
			}
		}

		var avail []string
		for _, t := range clientTools {
			if f, _ := t["function"].(map[string]any); f != nil {
				if fName, _ := f["name"].(string); fName != "" {
					avail = append(avail, fName)
				}
			}
		}
		return fmt.Sprintf(`{"error": "tool %q not found in available tools list. Available tools: [%s]"}`, targetName, strings.Join(avail, ", "))

	case "search_tools":
		query, _ := args["query"].(string)
		if query == "" {
			if v, ok := args["keyword"].(string); ok {
				query = v
			} else if v, ok := args["q"].(string); ok {
				query = v
			}
		}
		query = strings.ToLower(strings.Trim(strings.TrimSpace(query), "\"'`"))
		if query == "" {
			return `{"error": "missing required parameter 'query'"}`
		}

		var matches []string
		for _, t := range clientTools {
			f, _ := t["function"].(map[string]any)
			if f == nil {
				continue
			}
			fName, _ := f["name"].(string)
			fDesc, _ := f["description"].(string)
			if strings.Contains(strings.ToLower(fName), query) || strings.Contains(strings.ToLower(fDesc), query) {
				matches = append(matches, fmt.Sprintf("- %s: %s", fName, cleanToolDesc(fDesc)))
			}
		}

		if len(matches) == 0 {
			return fmt.Sprintf("No tools found matching query %q.", query)
		}
		if len(matches) > 10 {
			matches = matches[:10]
		}
		return fmt.Sprintf("Matching tools for %q:\n%s\nCall describe_tool(tool_name) to get the detailed schema for any of these tools.", query, strings.Join(matches, "\n"))

	case "list_tools":
		var names []string
		for _, t := range clientTools {
			if f, _ := t["function"].(map[string]any); f != nil {
				if fName, _ := f["name"].(string); fName != "" {
					names = append(names, fName)
				}
			}
		}
		return fmt.Sprintf("Available client tools (%d total):\n%s", len(names), strings.Join(names, ", "))

	default:
		return fmt.Sprintf(`{"error": "unknown internal tool %q"}`, name)
	}
}
