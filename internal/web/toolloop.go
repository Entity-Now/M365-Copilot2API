package web

import (
	"encoding/json"
	"fmt"
	"m365-copilot2api/internal/chathub"
	"strings"

	"github.com/google/uuid"
)

type detectedToolCall struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func toolType(name string, tools []map[string]any) string {
	for _, t := range tools {
		f, _ := t["function"].(map[string]any)
		if n, _ := f["name"].(string); n == name {
			if typ, _ := t["type"].(string); typ != "" {
				return typ
			}
		}
	}
	return "function"
}

func allowedToolNames(tools []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, t := range tools {
		if f, ok := t["function"].(map[string]any); ok {
			if n, ok := f["name"].(string); ok && n != "" {
				out[n] = true
			}
		}
	}
	return out
}

func declaredToolNames(tools []map[string]any) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if f, ok := t["function"].(map[string]any); ok {
			if n, ok := f["name"].(string); ok && n != "" {
				names = append(names, n)
			}
		}
	}
	return names
}

type rejectedToolCall struct {
	Name   string
	Reason string
}

// validateDetectedToolCalls is the final trust boundary before a model-selected
// call is serialized to the client. ChatHub/native events and model-generated
// routing text are both untrusted: an undeclared name such as "unknown_tool"
// must never escape to Claude Code, Codex, or another local tool runner.
func validateDetectedToolCalls(calls []detectedToolCall, tools []map[string]any, choice any) ([]detectedToolCall, []rejectedToolCall) {
	valid := make([]detectedToolCall, 0, len(calls))
	rejected := make([]rejectedToolCall, 0)
	for _, call := range calls {
		fn := toolFunction(call.Name, tools)
		if fn == nil {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: "tool was not declared by the client"})
			continue
		}
		if !toolChoiceAllows(choice, call.Name) {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: "tool_choice does not allow this tool"})
			continue
		}
		args := map[string]any{}
		if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
			call.Arguments = json.RawMessage(`{}`)
		} else if err := json.Unmarshal(call.Arguments, &args); err != nil {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: "arguments are not a JSON object"})
			continue
		}
		args = normalizeToolArgsForFunction(args, fn)
		if err := schemaValid(args, fn); err != nil {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: err.Error()})
			continue
		}
		if updated, err := json.Marshal(args); err == nil {
			call.Arguments = updated
		}
		if call.ID == "" {
			call.ID = callID(call.Name, string(call.Arguments), len(valid))
		}
		if call.Type == "" {
			call.Type = toolType(call.Name, tools)
		}
		valid = append(valid, call)
	}
	return valid, rejected
}

var knownParameterAliases = map[string][]string{
	"file_path":           {"path", "filePath", "filepath", "target_file", "targetFile", "file", "filename", "AbsolutePath", "TargetFile", "target"},
	"path":                {"file_path", "filePath", "filepath", "target_file", "file", "filename", "AbsolutePath", "TargetFile", "directory_path", "dir_path", "DirectoryPath", "target"},
	"AbsolutePath":        {"file_path", "path", "filePath", "filepath", "target_file", "targetFile", "file", "filename", "TargetFile", "target"},
	"TargetFile":          {"file_path", "path", "filePath", "filepath", "target_file", "targetFile", "file", "filename", "AbsolutePath", "target"},
	"SearchDirectory":     {"path", "directory_path", "dir_path", "DirectoryPath", "target_directory", "dir", "directory", "SearchPath"},
	"SearchPath":          {"path", "directory_path", "dir_path", "DirectoryPath", "SearchDirectory", "target_directory", "dir"},
	"old_string":          {"old", "old_str", "search", "oldString", "oldContent", "target_content", "TargetContent", "target", "Target"},
	"new_string":          {"new", "new_str", "replace", "newString", "newContent", "replacement_content", "ReplacementContent", "replacement", "Replacement"},
	"old_str":             {"old_string", "old", "search", "oldString", "oldContent", "target_content", "TargetContent"},
	"new_str":             {"new_string", "new", "replace", "newString", "newContent", "replacement_content", "ReplacementContent"},
	"TargetContent":       {"old_string", "old", "search", "oldString", "oldContent", "target_content"},
	"ReplacementContent":  {"new_string", "new", "replace", "newString", "newContent", "replacement_content"},
	"command":             {"cmd", "bash", "script", "CommandLine"},
	"cmd":                 {"command", "script", "CommandLine"},
	"CommandLine":         {"command", "cmd", "bash", "script"},
	"content":             {"text", "body", "file_text", "code", "CodeContent"},
	"CodeContent":         {"content", "text", "body", "code"},
	"pattern":             {"query", "search", "regex", "Query", "Pattern"},
	"Pattern":             {"pattern", "query", "search", "regex", "Query"},
	"query":               {"pattern", "search", "q", "Query", "Pattern"},
	"Query":               {"query", "pattern", "search", "Pattern"},
	"directory_path":      {"path", "dir_path", "DirectoryPath", "target_directory", "dir", "directory", "SearchDirectory"},
	"DirectoryPath":       {"path", "directory_path", "dir_path", "target_directory", "dir", "directory", "SearchDirectory"},
}

func normalizeToolArgsForFunction(args map[string]any, fn map[string]any) map[string]any {
	if args == nil {
		args = make(map[string]any)
	}
	params, _ := fn["parameters"].(map[string]any)
	if params == nil {
		return args
	}
	props, _ := params["properties"].(map[string]any)
	reqList, _ := params["required"].([]any)
	reqSet := make(map[string]bool)
	for _, r := range reqList {
		if s, ok := r.(string); ok {
			reqSet[s] = true
		}
	}

	targetKeys := make(map[string]bool)
	for k := range props {
		targetKeys[k] = true
	}
	for k := range reqSet {
		targetKeys[k] = true
	}

	for targetKey := range targetKeys {
		val, exists := args[targetKey]
		isEmptyStr := false
		if s, ok := val.(string); ok && strings.TrimSpace(s) == "" && reqSet[targetKey] {
			isEmptyStr = true
		}

		if !exists || isEmptyStr {
			if aliases, ok := knownParameterAliases[targetKey]; ok {
				for _, alias := range aliases {
					if aVal, aExists := args[alias]; aExists {
						if as, ok := aVal.(string); !ok || strings.TrimSpace(as) != "" {
							args[targetKey] = aVal
							if props != nil {
								if _, declared := props[alias]; !declared {
									delete(args, alias)
								}
							}
							exists = true
							break
						}
					}
				}
			}
		}

		if !exists {
			canonicalTarget := strings.ToLower(strings.ReplaceAll(targetKey, "_", ""))
			for argK, argV := range args {
				if strings.ToLower(strings.ReplaceAll(argK, "_", "")) == canonicalTarget {
					args[targetKey] = argV
					if props != nil {
						if _, declared := props[argK]; !declared {
							delete(args, argK)
						}
					}
					break
				}
			}
		}
	}

	return args
}

func toolChoiceAllows(choice any, name string) bool {
	if choice == nil {
		return true
	}
	if s, ok := choice.(string); ok {
		return s != "none" && (s != "required" || name != "")
	}
	if m, ok := choice.(map[string]any); ok {
		if f, ok := m["function"].(map[string]any); ok {
			n, _ := f["name"].(string)
			return n == name
		}
		if n, ok := m["name"].(string); ok {
			return n == name
		}
	}
	return true
}

// callID returns a globally unique tool call id. Content hashes previously
// collided when the same tool+arguments was invoked again (duplicate tool call
// id errors from clients), so uniqueness must not depend on call content.
func callID(name, args string, index int) string {
	return "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// dedupeToolCalls collapses tool calls that share the same name and canonical
// arguments within a single upstream turn. The M365 Copilot upstream sometimes
// emits the same fenced command (or native tool event) more than once in one
// reply; without this the agent ledger counts each copy as a separate round and
// falsely trips the stuck-loop guard (agent_ledger.go). Order is preserved and
// the first occurrence wins so downstream call ids stay stable.
func dedupeToolCalls(calls []detectedToolCall) []detectedToolCall {
	if len(calls) < 2 {
		return calls
	}
	seen := make(map[string]bool, len(calls))
	out := calls[:0]
	for _, c := range calls {
		key := c.Name + "\x00" + canonicalToolArguments(string(c.Arguments))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func extractToolCalls(text string, tools []map[string]any, choice any) ([]detectedToolCall, bool) {
	allowed := allowedToolNames(tools)
	var out []detectedToolCall
	remaining := text
	for {
		start := strings.Index(remaining, "<m365-tool-call>")
		if start < 0 {
			break
		}
		end := strings.Index(remaining[start:], "</m365-tool-call>")
		if end < 0 {
			break
		}
		end += start
		content := remaining[start+len("<m365-tool-call>") : end]
		remaining = remaining[end+len("</m365-tool-call>"):]
		var raw any
		if json.Unmarshal([]byte(content), &raw) != nil {
			continue
		}
		items := []any{raw}
		if arr, ok := raw.([]any); ok {
			items = arr
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			n, _ := m["name"].(string)
			if !allowed[n] || !toolChoiceAllows(choice, n) {
				continue
			}
			a, _ := json.Marshal(m["arguments"])
			out = append(out, detectedToolCall{ID: callID(n, string(a), len(out)), Type: toolType(n, tools), Name: n, Arguments: a})
		}
	}
	return out, len(out) > 0
}

func validateToolResult(messages []oaiMsg, known map[string]bool) error {
	for _, m := range messages {
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				return fmt.Errorf("tool_call_id required")
			}
			if len(known) > 0 && !known[m.ToolCallID] {
				return fmt.Errorf("unknown tool_call_id: %s", m.ToolCallID)
			}
		}
	}
	return nil
}

var toolRefusalPatterns = []string{
	"tools are not available in this session",
	"tools are not available in the current conversation",
	"tools are not available in this environment",
	"not available in the current conversation",
	"not available in this session",
	"not callable in the current environment",
	"cannot execute the requested tools",
	"cannot invoke tools in this environment",
	"no tools are available in this conversation",
	"当前会话无法调用工具",
	"当前环境无法调用工具",
	"当前会话中无法调用工具",
	"当前对话环境不支持工具",
	"并非当前对话环境可调用",
	"并不是实际可调用的工具",
	"不是实际可调用的工具",
	"并不是可调用的工具",
	"不是可调用的工具",
	"我无法执行你在消息中要求的",
	"无法执行你在消息中要求的",
}

func isToolRefusal(text string) bool {
	low := strings.ToLower(text)
	for _, p := range toolRefusalPatterns {
		if strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}

	// Unambiguous refusal to access the caller's local workspace/files
	specialPhrases := []string{
		"无法直接读取你本地",
		"无法直接访问你本地",
		"无法访问你的本地",
		"无法读取你的本地",
		"我无法读取你本地",
		"我无法访问你本地",
		"没有权限访问本地",
		"没有权限访问你的本地",
	}
	for _, sp := range specialPhrases {
		if strings.Contains(low, sp) {
			return true
		}
	}

	return false
}

func isContentPolicyBlock(text string) bool {
	return chathub.IsContentPolicyBlock(text)
}

func isImageLimitNotice(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "无法生成更多图像") || strings.Contains(t, "unable to generate more images")
}

var actionDescriptionPatterns = []string{
	// English first-person investigation descriptions replacing tool execution
	"i would inspect",
	"i would review",
	"i would verify",
	"i would first check",
	"i would first inspect",
	"next i would inspect",
	"next i would check",
	"next i would verify",
	"next, i will inspect",
	"next, i will check",
	"next, i will verify",
	"based on currently read code",
	"based on the code read so far",

	// Chinese passive laziness and non-action phrasing
	"基于我实际读到的代码，目前只能给出",
	"基于目前读到的代码，目前只能给出",
	"基于我实际读到的代码，只能给出",
	"根据已读取代码，目前不能确认没有问题",
	"目前我不能确认没有问题，根据已读取代码",
	"根据已读取代码，尚无法确认",
	"根据已读取代码，暂时无法确认",
	"根据已读取代码，无法确认",
	"接下来我将检查",
	"接下来我将查看",
	"接下来我需要查看",
	"我需要进一步检查",
	"我将先检查",
	"下一步我将检查",
}

func isPassivityOrActionDescription(text string) bool {
	low := strings.ToLower(text)
	for _, p := range actionDescriptionPatterns {
		if strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

var sandboxHallucinationPatterns = []string{
	"/mnt/data 为空",
	"/mnt/data is empty",
	"只提供 linux 容器",
	"only provides linux",
	"only provides a linux container",
	"没有 windows 执行通道",
	"没有执行通道无法运行",
	"cannot access the windows path",
	"cannot access your local files",
	"请将项目目录打包",
	"请将项目打包",
	"请将代码打包",
	"打包为 zip 上传",
	"打包为zip上传",
	"打包成 zip 上传",
	"打包成zip上传",
	"打包上传项目",
	"上传项目文件压缩包",
	"upload the project as a zip",
	"upload the repository as a zip",
	"upload a zip of the",
	"zip and upload the",
	"please zip and upload",
	"upload the source code as a zip",
	"execution environment has changed",
}

func isSandboxHallucination(text string) bool {
	low := strings.ToLower(text)
	for _, p := range sandboxHallucinationPatterns {
		if strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
