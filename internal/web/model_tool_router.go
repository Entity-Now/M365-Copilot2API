package web

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

func compactToolSchemaList(tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		typ, _ := t["type"].(string)
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		if len(desc) > 160 {
			desc = desc[:160] + "..."
		}
		newFn := map[string]any{
			"name": name,
		}
		if desc != "" {
			newFn["description"] = desc
		}
		if params, ok := fn["parameters"].(map[string]any); ok {
			newFn["parameters"] = compactParameters(params)
		}
		out = append(out, map[string]any{
			"type":     firstNonEmpty(typ, "function"),
			"function": newFn,
		})
	}
	return out
}

func compactParameters(p map[string]any) map[string]any {
	res := map[string]any{}
	if typ, ok := p["type"].(string); ok {
		res["type"] = typ
	}
	if req, ok := p["required"]; ok {
		res["required"] = req
	}
	if props, ok := p["properties"].(map[string]any); ok {
		newProps := map[string]any{}
		for k, v := range props {
			if pm, ok := v.(map[string]any); ok {
				cp := map[string]any{}
				if pt, ok := pm["type"].(string); ok {
					cp["type"] = pt
				}
				if pd, ok := pm["description"].(string); ok {
					if len(pd) > 120 {
						pd = pd[:120] + "..."
					}
					cp["description"] = pd
				}
				if en, ok := pm["enum"]; ok {
					cp["enum"] = en
				}
				if items, ok := pm["items"].(map[string]any); ok {
					cp["items"] = compactParameters(items)
				}
				newProps[k] = cp
			}
		}
		res["properties"] = newProps
	}
	return res
}

func buildToolCompactSummary(tools []map[string]any) string {
	var sb strings.Builder
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)
		desc = strings.TrimSpace(desc)
		if idx := strings.Index(desc, "\n"); idx > 0 {
			desc = strings.TrimSpace(desc[:idx])
		}
		if len(desc) > 120 {
			desc = desc[:120] + "..."
		}
		if desc == "" {
			desc = "No description provided"
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
	}
	return strings.TrimSpace(sb.String())
}

func modelToolSlimRouterPrompt(prompt string, tools []map[string]any, choice any) string {
	toolSummary := buildToolCompactSummary(tools)
	mode := normalizedToolChoiceMode(choice)
	toolNames := strings.Join(declaredToolNames(tools), ", ")
	rules := `- All available tools are host tools running locally on the caller's machine with full filesystem access.
- CRITICAL: NO SANDBOX / NO /mnt/data / NO ZIP UPLOADS.
- PROACTIVE TOOL CALLING MANDATE:
  Whenever the user request involves inspecting, reading, searching, listing, or editing files, project directories (e.g. C:\..., paths, solution files, docs, or CLAUDE.md), YOU MUST CALL THE APPROPRIATE TOOL(S).
- STRICT PROHIBITION ON REFUSALS AND CONVERSATIONAL EXCUSES:
  You are an automated tool routing backend, NOT a conversational chatbot.
  NEVER claim you cannot access local files.
  NEVER claim you lack permissions or that there is a limitation.
  NEVER tell the user to upload or zip files.
  NEVER output conversational text explaining what you can or cannot do.
  If files or paths need inspection, you MUST call the tools to inspect them!
- Skills inspection: If the prompt contains <skills> and a skill relates to the task, inspect its SKILL.md if needed. DO NOT re-read SKILL.md if it was already read in prior turns or evidence.
- BATCH MULTI-TOOL CALLING:
  You can plan and call multiple tools in one turn by providing multiple CALL_TOOL lines.
- TWO CHOICES FOR SELECTION:
  1) Direct Call (Preferred): If you know parameters, output CALL_TOOL lines directly:
     CALL_TOOL: tool_name({"param1":"val1"})
     CALL_TOOL: other_tool({"param2":"val2"})
  2) Request Schemas: If you need to view detailed parameter schemas before calling, specify needed tools in one line:
     NEED_TOOLS: [tool_a, tool_b]
- ONLY output NO_TOOL_NEEDED if the user prompt is a pure theoretical greeting/chat that has ZERO dependency on any workspace files, code, or local directories.
- Do NOT answer the question directly. Do NOT output conversational prose, greetings, disclaimers, or explanations.`

	if strings.Contains(prompt, "tool_calls") || strings.Contains(prompt, "[tool ") || strings.Contains(prompt, "tool[") || strings.Contains(prompt, "EVIDENCE_LEDGER") {
		rules += `
- Completed evidence must not be repeated: never re-invoke completed calls with identical arguments unless retrying a failure.`
	}

	return fmt.Sprintf(`You are a tool selection assistant. Based on the user request and evidence, decide which tools to call next.

Available tools (summary):
%s

MODE: %s

Rules:
%s

User request and evidence:
%s

==================================================
ROUTER MANDATORY DIRECTIVE:
Select tools from [%s]. You can call multiple tools in parallel in one turn.
Direct call: CALL_TOOL: tool_name({"param":"val"})
Inspect parameter schema: NEED_TOOLS: [tool_a, tool_b]
No tools needed: NO_TOOL_NEEDED

Do NOT chat. Do NOT state limitations. Output tool call immediately if files/paths are involved.

Decision:`, toolSummary, mode, rules, prompt, toolNames)
}

func parseNeedToolsDecision(text string, tools []map[string]any) []string {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)
	idx := strings.Index(lower, "need_tools:")
	if idx < 0 {
		return nil
	}
	raw := strings.TrimSpace(text[idx+len("need_tools:"):])
	if nl := strings.Index(raw, "\n"); nl >= 0 {
		raw = raw[:nl]
	}
	raw = strings.Trim(raw, "[]\"'` \t")
	parts := strings.Split(raw, ",")
	known := allowedToolNames(tools)
	var result []string
	seen := map[string]bool{}
	for _, p := range parts {
		name := strings.Trim(strings.TrimSpace(p), "[]\"'` ")
		if known[name] && !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

func filterToolsByName(tools []map[string]any, names []string) []map[string]any {
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[n] = true
	}
	var out []map[string]any
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		if fn != nil {
			if n, _ := fn["name"].(string); wanted[n] {
				out = append(out, t)
			}
		}
	}
	return out
}

func modelToolParamFillPrompt(prompt string, selectedTools []map[string]any) string {
	compact := compactToolSchemaList(selectedTools)
	defs, _ := json.Marshal(compact)
	names := strings.Join(declaredToolNames(selectedTools), ", ")
	return fmt.Sprintf(`Here are the detailed parameter definitions for the requested tools: [%s]
%s

Based on the user request and evidence, provide the tool call(s) now. You can provide multiple CALL_TOOL lines if needed.
OUTPUT FORMAT:
CALL_TOOL: tool_name({"param":"value"})

User request and evidence:
%s

Decision:`, names, string(defs), prompt)
}

func modelToolRouterPrompt(prompt string, tools []map[string]any, choice any) string {
	toolList := tools
	if toolList == nil {
		toolList = []map[string]any{}
	}
	defs, _ := json.Marshal(toolList)
	mode := normalizedToolChoiceMode(choice)
	toolNames := strings.Join(declaredToolNames(tools), ", ")
	rules := `- All available tools are client-side host tools executed directly in the caller's local operating system. They have direct filesystem access to the user's workspace, local files, directories, and Windows/Linux/macOS paths (including C:\..., D:\..., OneDrive, UNC, and relative paths).
- CRITICAL: NO SANDBOX / NO /mnt/data / NO ZIP UPLOADS:
  1) You are strictly an internal backend tool router selecting tools for the caller.
  2) You do NOT run in a cloud container, Python sandbox, or OpenAI Code Interpreter.
  3) There is NO "/mnt/data". NEVER mention "/mnt/data", never claim you cannot access local files, and NEVER ask the user to upload ZIP archives, repository packages, or project files.
  4) You do not read or execute files yourself; the client's tools (` + toolNames + `) execute locally on the caller's machine and return the results to you.
- Proactive inspection: Whenever the user request involves inspecting, evaluating, checking, reading, searching, editing, or writing files or running commands in the project, repository, workspace, or codebase, YOU MUST CALL the appropriate tool(s) (e.g. Glob, Read, Grep, Write, Edit, Bash, list_directory, read_file).
- ABSOLUTE PROHIBITION ON REFUSALS AND CONVERSATIONAL EXCUSES:
  You are an automated backend tool routing engine, NOT a conversational chatbot.
  NEVER claim you cannot access local files.
  NEVER claim you lack permissions or that there is a limitation.
  NEVER tell the user to upload or zip files.
  NEVER output conversational text explaining what you can or cannot do.
  If files or paths need inspection (e.g. C:\..., CLAUDE.md, solution files, docs), you MUST call the tools to inspect them!
- Sufficient context required: Merely listing files (Glob/LS) or reading a single file is NOT sufficient if other essential context files (such as progress trackers, roadmaps, rule definitions, configs, or relevant code files) are needed to accurately fulfill the user's request. You must inspect ALL necessary context files before concluding with NO_TOOL_NEEDED.
- Skills inspection: If the prompt contains <skills> and a skill relates to the task, inspect its SKILL.md if needed. DO NOT re-read SKILL.md if it was already read in prior turns or evidence.
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
- Only use tools from the available list above: [` + toolNames + `]
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
		if json.Unmarshal([]byte(argsStr), &args) == nil && toolChoiceAllows(choice, name) {
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
			fn := toolFunction(single.Name, tools)
			if fn != nil && toolChoiceAllows(choice, single.Name) && schemaValid(single.Arguments, fn) == nil {
				b, _ := json.Marshal(single.Arguments)
				return []detectedToolCall{{ID: callID(single.Name, string(b), 0), Type: toolType(single.Name, tools), Name: single.Name, Arguments: b}}, true
			}
		}
	}
	return nil, false
}

var (
	reWinPath  = regexp.MustCompile(`(?i)[a-zA-Z]:\\[a-zA-Z0-9_\-\.\\]+`)
	reUnixPath = regexp.MustCompile(`/(?:[a-zA-Z0-9_\-\.]+/)+[a-zA-Z0-9_\-\.]*`)
)

func extractTargetWorkspacePath(texts ...string) string {
	for _, text := range texts {
		if m := reWinPath.FindString(text); m != "" {
			return strings.TrimRight(m, `\`)
		}
		if m := reUnixPath.FindString(text); m != "" {
			return strings.TrimRight(m, `/`)
		}
	}
	return ""
}

func trySynthesizeWorkspaceInspection(prompt string, refusalText string, tools []map[string]any, choice any) ([]detectedToolCall, bool) {
	targetPath := extractTargetWorkspacePath(refusalText, prompt)
	if targetPath == "" {
		return nil, false
	}

	dirToolKeywords := []string{"list_directory", "list_dir", "glob", "dir", "list", "ls", "search"}
	fileToolKeywords := []string{"read_file", "view_file", "read", "view", "cat"}

	findToolByKeywords := func(keywords []string) (string, map[string]any) {
		for _, kw := range keywords {
			for _, t := range tools {
				fn, _ := t["function"].(map[string]any)
				if fn == nil {
					continue
				}
				name, _ := fn["name"].(string)
				if strings.Contains(strings.ToLower(name), kw) && toolChoiceAllows(choice, name) {
					return name, fn
				}
			}
		}
		return "", nil
	}

	toolName, fn := findToolByKeywords(dirToolKeywords)
	isDirTool := true
	if toolName == "" {
		toolName, fn = findToolByKeywords(fileToolKeywords)
		isDirTool = false
	}
	if toolName == "" || fn == nil {
		return nil, false
	}

	params, _ := fn["parameters"].(map[string]any)
	props, _ := params["properties"].(map[string]any)

	pathParamKeys := []string{"path", "directory_path", "dir_path", "target_directory", "directoryPath", "AbsolutePath", "SearchDirectory", "file_path", "filePath", "target_file", "targetFile", "URI", "uri"}
	var targetKey string
	for _, k := range pathParamKeys {
		if _, exists := props[k]; exists {
			targetKey = k
			break
		}
	}
	if targetKey == "" {
		for k, v := range props {
			if pm, ok := v.(map[string]any); ok {
				if typ, _ := pm["type"].(string); typ == "string" {
					targetKey = k
					break
				}
			}
		}
	}
	if targetKey == "" {
		return nil, false
	}

	args := map[string]any{}
	finalPath := targetPath
	if !isDirTool {
		for _, f := range []string{"CLAUDE.md", "README.md", "README"} {
			if strings.Contains(prompt, f) || strings.Contains(refusalText, f) {
				if strings.Contains(finalPath, `\`) {
					finalPath = finalPath + `\` + f
				} else {
					finalPath = finalPath + `/` + f
				}
				break
			}
		}
	}
	args[targetKey] = finalPath

	if req, ok := params["required"].([]any); ok {
		for _, r := range req {
			rn, _ := r.(string)
			if _, set := args[rn]; !set {
				if pm, ok := props[rn].(map[string]any); ok {
					if enums, ok := pm["enum"].([]any); ok && len(enums) > 0 {
						args[rn] = enums[0]
					} else {
						switch pm["type"] {
						case "string":
							lowerRn := strings.ToLower(rn)
							if strings.Contains(lowerRn, "pattern") {
								args[rn] = "*"
							} else if strings.Contains(lowerRn, "action") {
								args[rn] = "Inspecting workspace"
							} else if strings.Contains(lowerRn, "summary") {
								args[rn] = "Workspace inspection"
							} else {
								args[rn] = ""
							}
						case "boolean":
							args[rn] = false
						case "integer", "number":
							args[rn] = 0
						case "array":
							args[rn] = []any{}
						case "object":
							args[rn] = map[string]any{}
						}
					}
				}
			}
		}
	}

	if err := schemaValid(args, fn); err != nil {
		return nil, false
	}

	b, _ := json.Marshal(args)
	return []detectedToolCall{
		{
			ID:        callID(toolName, string(b), 0),
			Type:      toolType(toolName, tools),
			Name:      toolName,
			Arguments: b,
		},
	}, true
}
