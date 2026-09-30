package web

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var fencedToolCall = regexp.MustCompile("(?s)```([A-Za-z0-9_.-]+)\\s*\\n(.*?)\\n```")
var selfWrittenToolResultPattern = regexp.MustCompile(`(?i)<tool_(?:response|result)\b`)
var shellToolNamePattern = regexp.MustCompile(`^(?i)(bash|sh|shell|zsh|run|exec|execute|command|cmd|terminal|run_command|run_terminal_cmd|execute_command|execute_bash|shell_exec|system|powershell|pwsh)$`)
var markdownHeadingPattern = regexp.MustCompile(`(?m)^#{1,6}\s`)

// illustrativeProseLimit is the amount of natural-language text outside fenced
// blocks beyond which bare shell blocks are treated as examples.
const illustrativeProseLimit = 120

var shellLangs = map[string]bool{
	"bash":           true,
	"sh":             true,
	"shell":          true,
	"zsh":            true,
	"console":        true,
	"shell-session":  true,
	"shellsession":   true,
	"shsession":      true,
	"container.exec": true,
	"container.run":  true,
	"container.bash": true,
	"powershell":     true,
	"pwsh":           true,
	"ps1":            true,
	"posh":           true,
	"cmd":            true,
	"bat":            true,
	"batch":          true,
	"dosbatch":       true,
}

func isShellLang(name string) bool {
	return shellLangs[strings.ToLower(strings.TrimSpace(name))]
}

// declaredShell returns the shell-ish tool name the client actually
// declared (bash/sh/shell/run_command/powershell/cmd/etc.), or "" if none.
func declaredShell(allowed map[string]bool) string {
	for _, n := range []string{"bash", "sh", "shell", "run_command", "execute_command", "run", "terminal", "powershell", "pwsh", "cmd"} {
		if allowed[n] {
			return n
		}
	}
	for n := range allowed {
		if shellToolNamePattern.MatchString(n) {
			return n
		}
	}
	return ""
}

// truncateAtFabricatedToolResponse acts as a stop sequence when the model writes
// its own <tool_response>...</tool_response> fiction after calling a tool.
func truncateAtFabricatedToolResponse(text string) string {
	loc := selfWrittenToolResultPattern.FindStringIndex(text)
	if loc == nil {
		return text
	}
	head := text[:loc[0]]
	if fencedToolCall.MatchString(head) {
		return strings.TrimRight(head, " \t\r\n")
	}
	return text
}

// proseOutsideFences returns the length in runes of the text left after all
// fenced code blocks are removed.
func proseOutsideFences(text string) int {
	stripped := fencedToolCall.ReplaceAllString(text, "")
	return len([]rune(strings.TrimSpace(stripped)))
}

// isIllustrativeShell determines if a generic shell block is an illustrative code example.
func isIllustrativeShell(text string) bool {
	proseLen := proseOutsideFences(text)
	if proseLen <= illustrativeProseLimit {
		return false
	}
	// If prose exceeds 120 runes, check if it's just a single action with a concise
	// one-line preamble and zero tail commentary.
	firstLoc := fencedToolCall.FindStringIndex(text)
	if firstLoc != nil {
		preamble := strings.TrimSpace(text[:firstLoc[0]])
		tail := strings.TrimSpace(text[firstLoc[1]:])
		hasHeading := markdownHeadingPattern.MatchString(preamble)
		if len([]rune(preamble)) <= 100 && !hasHeading && !strings.Contains(preamble, "```") && len([]rune(tail)) == 0 {
			return false
		}
	}
	return true
}

// isProseDocument determines if a multi-fence response is actually a written markdown
// document (such as a README with code examples) rather than an actionable tool call.
func isProseDocument(text string, calls []detectedToolCall) bool {
	if len(calls) < 2 {
		return false
	}
	firstFenceLoc := fencedToolCall.FindStringIndex(text)
	if firstFenceLoc != nil {
		preamble := text[:firstFenceLoc[0]]
		trimmedPreamble := strings.TrimSpace(preamble)
		hasHeading := markdownHeadingPattern.MatchString(preamble)
		if len([]rune(trimmedPreamble)) < 200 && !hasHeading && !strings.Contains(preamble, "```") {
			return false
		}
	}
	prose := strings.TrimSpace(fencedToolCall.ReplaceAllString(text, ""))
	hasMarkdownHeaders := markdownHeadingPattern.MatchString(prose)
	return len(calls) >= 4 || hasMarkdownHeaders || len([]rune(prose)) >= 300
}

// executedOnlyFirstNote generates an upstream notification note informing M365's
// conversation memory that only the first call actually ran.
func executedOnlyFirstNote(droppedCalls int, hasFabricatedTail bool) string {
	if hasFabricatedTail {
		return "(Note: only the first tool call in your previous reply was actually run. Everything you wrote after it, including the <tool_response> you wrote yourself, did not happen. Here is the real output of that first call:)"
	}
	if droppedCalls > 0 {
		return fmt.Sprintf("(Note: only the first of the %d tool calls in your previous reply was run. Anything written after it was before its result existed. Here is the real output of the first one:)", droppedCalls+1)
	}
	return ""
}

// parseFencedHeadersAndBody parses YAML-like header lines (key: value) and a free-form body
// or search/replace diff block within a fenced tool block.
func parseFencedHeadersAndBody(args string) map[string]any {
	result := make(map[string]any)
	lines := strings.Split(args, "\n")
	bodyStart := -1

	// Check for aider-style search/replace block
	if strings.Contains(args, "<<<<<<< SEARCH") && strings.Contains(args, "=======") && strings.Contains(args, ">>>>>>> REPLACE") {
		searchStart := strings.Index(args, "<<<<<<< SEARCH")
		divider := strings.Index(args, "=======")
		replaceEnd := strings.Index(args, ">>>>>>> REPLACE")
		if searchStart >= 0 && divider > searchStart && replaceEnd > divider {
			searchContent := strings.TrimPrefix(args[searchStart:divider], "<<<<<<< SEARCH\n")
			searchContent = strings.TrimPrefix(searchContent, "<<<<<<< SEARCH\r\n")
			replaceContent := strings.TrimPrefix(args[divider:replaceEnd], "=======\n")
			replaceContent = strings.TrimPrefix(replaceContent, "=======\r\n")
			result["old"] = strings.TrimRight(searchContent, "\r\n")
			result["new"] = strings.TrimRight(replaceContent, "\r\n")
			result["search"] = result["old"]
			result["replace"] = result["new"]
			result["old_string"] = result["old"]
			result["new_string"] = result["new"]
			headerPart := args[:searchStart]
			for _, line := range strings.Split(headerPart, "\n") {
				line = strings.TrimSpace(line)
				if idx := strings.Index(line, ":"); idx > 0 {
					k := strings.TrimSpace(line[:idx])
					v := strings.TrimSpace(line[idx+1:])
					result[k] = v
					if k == "file" || k == "path" || k == "target" || k == "target_file" {
						result["file_path"] = v
						result["path"] = v
					}
				}
			}
			return result
		}
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			bodyStart = i + 1
			break
		}
		if idx := strings.Index(line, ":"); idx > 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			result[k] = v
			if k == "file" || k == "path" || k == "target" || k == "target_file" {
				result["file_path"] = v
				result["path"] = v
			}
		} else {
			bodyStart = i
			break
		}
	}

	if bodyStart >= 0 && bodyStart < len(lines) {
		body := strings.Join(lines[bodyStart:], "\n")
		body = strings.TrimRight(body, "\r\n")
		if len(result) == 0 {
			trimmedBody := strings.TrimSpace(body)
			result["command"] = trimmedBody
			result["content"] = body
			result["path"] = trimmedBody
			result["file_path"] = trimmedBody
		} else {
			result["content"] = body
			result["body"] = body
		}
	}

	return result
}

func fencedToolCalls(text string, tools []map[string]any, choice any) []detectedToolCall {
	text = truncateAtFabricatedToolResponse(text)
	allowed := allowedToolNames(tools)
	shell := declaredShell(allowed)
	illustrative := isIllustrativeShell(text)
	var out []detectedToolCall
	for _, m := range fencedToolCall.FindAllStringSubmatch(text, -1) {
		name := m[1]
		args := strings.TrimSpace(m[2])
		var v any
		_ = json.Unmarshal([]byte(args), &v)
		// Auto-convert bash/shell code blocks to tool calls, but only when
		// the client declared the tool.
		if isShellLang(name) {
			if illustrative {
				continue
			}
			converted := name
			if !allowed[name] {
				if shell == "" {
					continue
				}
				converted = shell
			}
			if m, ok := v.(map[string]any); ok {
				if cmd, hasCmd := m["command"]; hasCmd && cmd != "" {
					cmdBytes, _ := json.Marshal(map[string]any{"command": cmd, "timeout": m["timeout"], "workdir": m["workdir"]})
					out = append(out, detectedToolCall{ID: callID(converted, string(cmdBytes), len(out)), Type: "function", Name: converted, Arguments: cmdBytes})
					continue
				}
			}
			if v == nil {
				cmdBytes, _ := json.Marshal(map[string]any{"command": args})
				out = append(out, detectedToolCall{ID: callID(converted, string(cmdBytes), len(out)), Type: "function", Name: converted, Arguments: cmdBytes})
				continue
			}
			continue
		}
		if !allowed[name] || !toolChoiceAllows(choice, name) {
			continue
		}
		if v == nil {
			parsed := parseFencedHeadersAndBody(args)
			if len(parsed) > 0 {
				b, _ := json.Marshal(parsed)
				out = append(out, detectedToolCall{ID: callID(name, string(b), len(out)), Type: toolType(name, tools), Name: name, Arguments: b})
				continue
			}
			continue
		}
		b, _ := json.Marshal(v)
		out = append(out, detectedToolCall{ID: callID(name, string(b), len(out)), Type: toolType(name, tools), Name: name, Arguments: b})
	}
	// Also check for plain JSON objects with a "command" field (not in fenced blocks)
	if len(out) == 0 && shell != "" && !illustrative {
		for i := 0; i < len(text); i++ {
			if text[i] != '{' {
				continue
			}
			end := strings.Index(text[i:], "\n")
			if end < 0 {
				end = len(text) - i
			}
			line := text[i : i+end]
			braceEnd := strings.LastIndex(line, "}")
			if braceEnd < 0 {
				continue
			}
			if !strings.Contains(line[:braceEnd+1], `"command"`) {
				continue
			}
			var obj map[string]any
			if json.Unmarshal([]byte(line[:braceEnd+1]), &obj) != nil {
				continue
			}
			if cmd, hasCmd := obj["command"]; hasCmd && cmd != "" {
				cmdBytes, _ := json.Marshal(map[string]any{"command": cmd, "timeout": obj["timeout"], "workdir": obj["workdir"]})
				out = append(out, detectedToolCall{ID: callID(shell, string(cmdBytes), len(out)), Type: "function", Name: shell, Arguments: cmdBytes})
				break
			}
		}
	}
	if isProseDocument(text, out) {
		return nil
	}
	return out
}
