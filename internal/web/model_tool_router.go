package web

import (
	"encoding/json"
	"fmt"
	"strings"
)

func modelToolRouterPrompt(prompt string, tools []map[string]any, choice any) string {
	defs, _ := json.Marshal(tools)
	mode := normalizedToolChoiceMode(choice)
	rules := `- All available tools are host client-side tools executed directly in the caller's local environment. They have full access to the user's workspace, local files, directories, and Windows/Linux/macOS paths (including C:\..., OneDrive, and relative paths).
- Proactive inspection: Whenever the user request involves the current project, codebase, repository, learning plan, progress, workspace, tasks, or requires reading, checking, searching, editing, or writing files or running commands, YOU MUST CALL the appropriate tool (e.g. Glob, Read, Grep, Write, Edit, Bash).
- Sufficient context required: Merely listing files (Glob/LS) or reading a single file is NOT sufficient if other essential context files (such as progress trackers, roadmaps, rule definitions, configs, or relevant code files) are needed to accurately fulfill the user's request. You must inspect ALL necessary context files before concluding with NO_TOOL_NEEDED.
- Prefer direct, lightweight inspection tools (Glob, Read, Grep) over heavy multi-turn subagents (such as Agent or Task) when exploring files or checking workspace context. Do not delegate simple file reading or project inspection to subagents.
- DO NOT answer with NO_TOOL_NEEDED when project files, code inspection, or workspace context need to be gathered. First gather real evidence using tools!
- ONLY respond with NO_TOOL_NEEDED for pure theoretical questions or general greetings that do not depend on any workspace or project files.
- NEVER claim that you cannot access local files, that the workspace is not mapped, or that host tools are missing. The tools in the list above are provided specifically for workspace access.
- If tools are needed, respond with one or more CALL_TOOL lines (you may call multiple tools in parallel):
  CALL_TOOL: tool_name({"arg1":"value1"})
- Only use tools from the available list above
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
	return fmt.Sprintf(`You are a tool selection assistant. Based on the user request, decide which tool to call next.

Available tools: %s

MODE: %s

Rules:
%s

User request and evidence:
%s`, defs, mode, rules, prompt)
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

	if strings.Contains(text, "NO_TOOL_NEEDED") || strings.Contains(text, "no_tool_needed") {
		return nil, true
	}
	// Fallback: try the old JSON format
	if i := strings.Index(text, "```"); i >= 0 {
		text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(text[i+3:], "```"), "json"))
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(text[start:end+1]), &probe) != nil {
		return nil, false
	}
	if _, ok := probe["calls"]; !ok {
		return nil, false
	}
	var envelope struct {
		Calls []struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"calls"`
	}
	if json.Unmarshal([]byte(text[start:end+1]), &envelope) != nil {
		return nil, false
	}
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
