package web

import (
	"encoding/json"
	"fmt"
	"strings"
)


func cleanToolDesc(desc string) string {
	desc = strings.TrimSpace(desc)
	if i := strings.IndexAny(desc, "\n\r"); i >= 0 {
		desc = desc[:i]
	}
	if i := strings.Index(desc, ". "); i >= 0 {
		desc = desc[:i+1]
	}
	if len(desc) > 120 {
		desc = desc[:117] + "..."
	}
	return strings.TrimSpace(desc)
}

func formatCompactType(pMap map[string]any, depth int) string {
	if pMap == nil || depth > 2 {
		return "object"
	}
	tStr, _ := pMap["type"].(string)
	switch tStr {
	case "string", "number", "integer", "boolean":
		return tStr
	case "array":
		items, _ := pMap["items"].(map[string]any)
		if items != nil {
			return formatCompactType(items, depth+1) + "[]"
		}
		return "any[]"
	case "object":
		props, _ := pMap["properties"].(map[string]any)
		if len(props) == 0 {
			return "object"
		}
		reqList, _ := pMap["required"].([]any)
		reqMap := make(map[string]bool, len(reqList))
		for _, r := range reqList {
			if s, ok := r.(string); ok {
				reqMap[s] = true
			}
		}
		var fields []string
		// List required fields first, then optional fields
		for propName, propVal := range props {
			if reqMap[propName] {
				subMap, _ := propVal.(map[string]any)
				fields = append(fields, fmt.Sprintf("%s: %s", propName, formatCompactType(subMap, depth+1)))
			}
		}
		for propName, propVal := range props {
			if !reqMap[propName] && len(fields) < 5 {
				subMap, _ := propVal.(map[string]any)
				fields = append(fields, fmt.Sprintf("%s?: %s", propName, formatCompactType(subMap, depth+1)))
			}
		}
		if len(fields) == 0 {
			return "object"
		}
		return "{" + strings.Join(fields, ", ") + "}"
	default:
		return "any"
	}
}

// selectAndCompactTools formats all tools into dense, structure-preserving TypeScript-style signatures.
func selectAndCompactTools(tools []map[string]any, prompt string) (string, []string) {
	if len(tools) == 0 {
		return "[]", nil
	}

	var lines []string
	var selectedNames []string
	for _, t := range tools {
		f, _ := t["function"].(map[string]any)
		if f == nil {
			continue
		}
		name, _ := f["name"].(string)
		if name == "" {
			continue
		}
		selectedNames = append(selectedNames, name)
		desc, _ := f["description"].(string)
		desc = cleanToolDesc(desc)

		paramsStr := ""
		if params, ok := f["parameters"].(map[string]any); ok {
			props, _ := params["properties"].(map[string]any)
			reqList, _ := params["required"].([]any)
			reqMap := make(map[string]bool, len(reqList))
			for _, r := range reqList {
				if s, ok := r.(string); ok {
					reqMap[s] = true
				}
			}
			var parts []string
			// First add required properties
			for propName, propVal := range props {
				if reqMap[propName] {
					subMap, _ := propVal.(map[string]any)
					parts = append(parts, fmt.Sprintf("%s: %s", propName, formatCompactType(subMap, 1)))
				}
			}
			// Then add optional properties
			for propName, propVal := range props {
				if !reqMap[propName] {
					subMap, _ := propVal.(map[string]any)
					parts = append(parts, fmt.Sprintf("%s?: %s", propName, formatCompactType(subMap, 1)))
				}
			}
			paramsStr = strings.Join(parts, ", ")
		}
		if desc != "" {
			lines = append(lines, fmt.Sprintf("- %s(%s): %s", name, paramsStr, desc))
		} else {
			lines = append(lines, fmt.Sprintf("- %s(%s)", name, paramsStr))
		}
	}

	return strings.Join(lines, "\n"), selectedNames
}

func modelToolRouterPrompt(prompt string, tools []map[string]any, choice any, compactOpt ...bool) string {
	compact := true
	onDemand := false
	if len(compactOpt) > 0 {
		compact = compactOpt[0]
	}
	if len(compactOpt) > 1 {
		onDemand = compactOpt[1]
	}

	var defs string
	var toolNames string
	var rulesPrefix string
	if onDemand {
		var activeNames []string
		defs, activeNames = formatOnDemandToolCatalog(tools)
		toolNames = strings.Join(activeNames, ", ")
		rulesPrefix = `- On-Demand Tool Schema Inspection:
- All tools under "Available Client Tools" run in the caller's local operating system with direct filesystem access.
- You have access to inspection tools: [describe_tool, search_tools, list_tools].
- If you need the exact parameter schema, argument names, or nested types for any tool, call:
  CALL_TOOL: describe_tool({"tool_name": "tool_name"})
- If you know the parameters or the tool is straightforward, you may call the client tool directly:
  CALL_TOOL: tool_name({"arg1": "val1"})
- When inspecting or searching workspace files, ALWAYS call the appropriate tool.
`
	} else if compact {
		var activeNames []string
		defs, activeNames = selectAndCompactTools(tools, prompt)
		toolNames = strings.Join(activeNames, ", ")
		rulesPrefix = `- Claude Code / Host Tools Specification: All tools are client-side host tools defined in compact TypeScript signatures.
- For nested structures (e.g. {prop: type}[]), construct the exact matching JSON object/array matching the inner signature.
- All tools execute directly in the caller's local operating system with full filesystem access to the user's workspace and local files.
`
	} else {
		raw, _ := json.Marshal(tools)
		defs = string(raw)
		toolNames = strings.Join(declaredToolNames(tools), ", ")
		rulesPrefix = `- All available tools are client-side host tools executed directly in the caller's local operating system. They have direct filesystem access to the user's workspace, local files, directories, and Windows/Linux/macOS paths (including C:\..., D:\..., OneDrive, UNC, and relative paths).
`
	}
	allowedToolList := toolNames
	if onDemand {
		allowedToolList = toolNames + ", describe_tool, search_tools, list_tools"
	}
	mode := normalizedToolChoiceMode(choice)
	rules := rulesPrefix + `- CRITICAL: NO SANDBOX / NO /mnt/data / NO ZIP UPLOADS:
  1) You are strictly an internal backend tool router selecting tools for the caller.
  2) You do NOT run in a cloud container, Python sandbox, or OpenAI Code Interpreter.
  3) There is NO "/mnt/data". NEVER mention "/mnt/data", never claim you cannot access local files, and NEVER ask the user to upload ZIP archives, repository packages, or project files.
  4) You do not read or execute files yourself; the client's tools (` + toolNames + `) execute locally on the caller's machine and return the results to you.
- Proactive inspection: Whenever the user request involves inspecting, evaluating, checking, reading, searching, editing, or writing files or running commands in the project, repository, workspace, or codebase, YOU MUST CALL the appropriate tool(s) (e.g. Glob, Read, Grep, Write, Edit, Bash, list_directory, read_file).
- Sufficient context required: Merely listing files (Glob/LS) or reading a single file is NOT sufficient if other essential context files (such as progress trackers, roadmaps, rule definitions, configs, or relevant code files) are needed to accurately fulfill the user's request. You must inspect ALL necessary context files before concluding with NO_TOOL_NEEDED.
- Skills inspection: If the prompt contains <skills> or "Available skills:", and the user's task or topic relates to any listed skill, YOU MUST FIRST CALL the appropriate file-reading tool (such as view_file, Read, read_file) with the exact file path of that skill's SKILL.md before deciding no tools are needed.
- Prefer direct, lightweight inspection tools over heavy multi-turn subagents when exploring files or checking workspace context. Do not delegate simple file reading or project inspection to subagents.
- DO NOT answer with NO_TOOL_NEEDED when project files, code inspection, or workspace context need to be gathered. First gather real evidence using tools!
- ONLY respond with NO_TOOL_NEEDED for pure theoretical questions or general greetings that do not depend on any workspace or project files.
- NEVER claim that you cannot access local files, that the workspace is not mapped, or that host tools are missing. The tools in the list above are provided specifically for workspace access.
- STRICT ROUTER OUTPUT:
  1) Do NOT answer the user's question directly.
  2) Do NOT output conversational prose, greetings, disclaimers, explanations, or excuses.
  3) If tools are needed, respond with one or more CALL_TOOL lines (you may call multiple tools in parallel):
     CALL_TOOL: tool_name({"arg1":"value1"})
  4) If and only if no tools are needed, respond with:
     NO_TOOL_NEEDED
- Only use tools from the available list above: [` + allowedToolList + `]
- Validate all arguments against the tool's schema
- Do not invent tools that are not in the list`
	// Multi-turn: completed tool evidence was already acted upon.
	if strings.Contains(prompt, "tool_calls") || strings.Contains(prompt, "[tool ") || strings.Contains(prompt, "tool[") || strings.Contains(prompt, "EVIDENCE_LEDGER") {
		rules += `
- Completed evidence must not be repeated: tool_calls rows are prior results already delivered to the user, never re-invoke them with identical arguments unless the user explicitly requests a retry or further modifications
- Only start a new tool call when fresh unfinished work remains or missing context needs to be gathered:
  1) Gather sufficient context: If relevant project files, progress logs, roadmaps, or source files have been identified (e.g. via directory listing or file references) but not yet read, you MUST call tools to read them before answering.
  2) Execute pending actions: If the user requested creating, updating, or modifying files, invoke the appropriate write/edit tools.
- Only respond with NO_TOOL_NEEDED when all necessary context files have been gathered AND all requested actions/analysis have sufficient information to be accurately delivered.`
	}
	return fmt.Sprintf(`You are a tool selection assistant. Based on the user request and evidence, decide which tool to call next.

Available tools: %s

MODE: %s

Rules:
%s

User request and evidence:
%s

==================================================
ROUTER MANDATORY DIRECTIVE:
You are the tool router. The caller runs locally with real client-side tools: [%s].
Do NOT converse with the user. Do NOT hallucinate a sandbox or /mnt/data. Do NOT ask for ZIP upload.
If the user wants to evaluate, inspect, read, search, or write project files or workspace paths, CALL THE APPROPRIATE TOOL NOW.

OUTPUT FORMAT:
CALL_TOOL: tool_name({"arg1":"val1"})
(or NO_TOOL_NEEDED)

Decision:`, defs, mode, rules, prompt, toolNames)
}

func extractParenContent(s string, openIndex int) (string, int) {
	depth := 0
	inString := false
	escape := false
	for i := openIndex; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inString {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if !inString {
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth == 0 {
					return s[openIndex+1 : i], i
				}
			}
		}
	}
	return "", -1
}

func parseModelToolDecision(text string, tools []map[string]any, choice any) ([]detectedToolCall, bool) {
	text = strings.TrimSpace(text)
	// Try the natural language format: one or more CALL_TOOL: name({...})
	var callDecisions []detectedToolCall
	searchStr := text
	lowerStr := strings.ToLower(text)
	idx := 0
	for {
		callIdx := strings.Index(lowerStr[idx:], "call_tool:")
		if callIdx < 0 {
			break
		}
		startPos := idx + callIdx + len("call_tool:")
		for startPos < len(searchStr) && (searchStr[startPos] == ' ' || searchStr[startPos] == '\t') {
			startPos++
		}
		openParen := strings.Index(searchStr[startPos:], "(")
		if openParen < 0 {
			idx = startPos
			continue
		}
		name := strings.TrimSpace(searchStr[startPos : startPos+openParen])
		argsStr, closeParen := extractParenContent(searchStr, startPos+openParen)
		if closeParen < 0 {
			idx = startPos + openParen + 1
			continue
		}
		idx = closeParen + 1

		var args map[string]any
		if json.Unmarshal([]byte(argsStr), &args) == nil {
			if isGatewayInternalTool(name) {
				b, _ := json.Marshal(args)
				callDecisions = append(callDecisions, detectedToolCall{
					ID:        callID(name, string(b), len(callDecisions)),
					Type:      "function",
					Name:      name,
					Arguments: b,
				})
				continue
			}
			if toolChoiceAllows(choice, name) {
				fn := toolFunction(name, tools)
				if fn != nil && schemaValid(args, fn) == nil {
					b, _ := json.Marshal(args)
					callDecisions = append(callDecisions, detectedToolCall{
						ID:        callID(name, string(b), len(callDecisions)),
						Type:      toolType(name, tools),
						Name:      name,
						Arguments: b,
					})
				}
			}
		}
	}
	if len(callDecisions) > 0 {
		return callDecisions, true
	}

	if isSandboxHallucination(text) || isToolRefusal(text) {
		return nil, false
	}

	if strings.Contains(text, "NO_TOOL_NEEDED") || strings.Contains(text, "no_tool_needed") {
		return nil, true
	}
	// Fallback: try JSON formats
	trimmed := text
	if i := strings.Index(trimmed, "```"); i >= 0 {
		trimmed = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(trimmed[i+3:], "```"), "json"))
	}
	// Try array format: [{"name":"...", "arguments":{...}}]
	startArr, endArr := strings.Index(trimmed, "["), strings.LastIndex(trimmed, "]")
	if startArr >= 0 && endArr > startArr {
		var arr []struct {
			Name      string         `json:"name"`
			Function  *struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"function"`
			Arguments map[string]any `json:"arguments"`
		}
		if json.Unmarshal([]byte(trimmed[startArr:endArr+1]), &arr) == nil && len(arr) > 0 {
			var out []detectedToolCall
			for i, c := range arr {
				cName := c.Name
				cArgs := c.Arguments
				if c.Function != nil {
					if c.Function.Name != "" {
						cName = c.Function.Name
					}
					if c.Function.Arguments != nil {
						cArgs = c.Function.Arguments
					}
				}
				if isGatewayInternalTool(cName) && cArgs != nil {
					b, _ := json.Marshal(cArgs)
					out = append(out, detectedToolCall{ID: callID(cName, string(b), i), Type: "function", Name: cName, Arguments: b})
					continue
				}
				fn := toolFunction(cName, tools)
				if fn == nil || cArgs == nil || !toolChoiceAllows(choice, cName) || schemaValid(cArgs, fn) != nil {
					continue
				}
				b, _ := json.Marshal(cArgs)
				out = append(out, detectedToolCall{ID: callID(cName, string(b), i), Type: toolType(cName, tools), Name: cName, Arguments: b})
			}
			if len(out) > 0 {
				return out, true
			}
		}
	}
	start, end := strings.Index(trimmed, "{"), strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed[start:end+1]), &probe) != nil {
		return nil, false
	}
	if _, ok := probe["calls"]; ok {
		var envelope struct {
			Calls []struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"calls"`
		}
		if json.Unmarshal([]byte(trimmed[start:end+1]), &envelope) == nil {
			out := make([]detectedToolCall, 0, len(envelope.Calls))
			for i, c := range envelope.Calls {
				if isGatewayInternalTool(c.Name) && c.Arguments != nil {
					b, _ := json.Marshal(c.Arguments)
					out = append(out, detectedToolCall{ID: callID(c.Name, string(b), i), Type: "function", Name: c.Name, Arguments: b})
					continue
				}
				fn := toolFunction(c.Name, tools)
				if fn == nil || c.Arguments == nil || !toolChoiceAllows(choice, c.Name) || schemaValid(c.Arguments, fn) != nil {
					continue
				}
				b, _ := json.Marshal(c.Arguments)
				out = append(out, detectedToolCall{ID: callID(c.Name, string(b), i), Type: toolType(c.Name, tools), Name: c.Name, Arguments: b})
			}
			return out, true
		}
	}
	if _, ok := probe["name"]; ok {
		var single struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if json.Unmarshal([]byte(trimmed[start:end+1]), &single) == nil && single.Name != "" && single.Arguments != nil {
			if isGatewayInternalTool(single.Name) {
				b, _ := json.Marshal(single.Arguments)
				return []detectedToolCall{{ID: callID(single.Name, string(b), 0), Type: "function", Name: single.Name, Arguments: b}}, true
			}
			fn := toolFunction(single.Name, tools)
			if fn != nil && toolChoiceAllows(choice, single.Name) && schemaValid(single.Arguments, fn) == nil {
				b, _ := json.Marshal(single.Arguments)
				return []detectedToolCall{{ID: callID(single.Name, string(b), 0), Type: toolType(single.Name, tools), Name: single.Name, Arguments: b}}, true
			}
		}
	}
	return nil, false
}
