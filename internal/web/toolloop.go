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
		if err := schemaValid(args, fn); err != nil {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: err.Error()})
			continue
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
	"tools are not available",
	"tool is not available",
	"not actually registered",
	"not actually available",
	"not available in this session",
	"工具不可用",
	"工具未暴露",
	"未映射",
	"未映射 windows 工作区",
	"缺少能够直接访问",
	"缺少宿主",
	"无法访问本地文件",
	"无法直接访问",
	"没有权限访问本地",
	"无法修改本地文件",
	"无法将文档写入",
	"无法将文件写入",
	"无法写入本地",
	"没有读取到项目文件",
	"未读取到项目文件",
	"未读取到任何文件",
	"无法读取项目文件",
	"当前会话中没有读取到",
	"无法做基于源码的可靠评估",
	"暂时无法做基于源码",
	"无法直接读取本地",
	"无法查看本地文件",
	"无法访问本地路径",
	"无法直接在本地",
	"不能直接操作本地",
}

func isToolRefusal(text string) bool {
	low := strings.ToLower(text)
	for _, p := range toolRefusalPatterns {
		if strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}

	// 语义特征句匹配
	specialPhrases := []string{
		"关键限制",
		"无法直接读取",
		"无法直接访问",
		"无法直接查看",
		"无法直接扫描",
		"无法直接获取",
		"当前无法读取",
		"当前无法访问",
		"没有权限读取",
		"没有权限访问",
		"无法在本地",
		"不能在本地",
		"无法对本地",
		"不能对本地",
		"我无法读取你",
		"我无法访问你",
		"无法直接读取你",
		"无法直接访问你",
	}
	for _, sp := range specialPhrases {
		if strings.Contains(low, sp) {
			return true
		}
	}

	actionPrefixes := []string{"无法", "不能", "没有权限", "没权限", "当前无法", "暂时无法", "不可直接", "不能直接", "无法直接", "难以直接", "无法自行", "无法主动"}
	actionVerbs := []string{"读取", "访问", "查看", "扫描", "获取", "操作", "打开", "写入", "修改", "编辑", "执行", "检视", "浏览"}
	actionTargets := []string{"本地", "文件", "项目", "目录", "代码", "工程", "路径", "内容", "workspace", "磁盘"}

	for _, pre := range actionPrefixes {
		if idx := strings.Index(low, pre); idx >= 0 {
			tail := low[idx+len(pre):]
			if len(tail) > 120 {
				tail = tail[:120]
			}
			hasVerb := false
			for _, v := range actionVerbs {
				if strings.Contains(tail, v) {
					hasVerb = true
					break
				}
			}
			if hasVerb {
				for _, tgt := range actionTargets {
					if strings.Contains(tail, tgt) {
						return true
					}
				}
			}
		}
	}

	enPrefixes := []string{"cannot", "can't", "unable to", "don't have access", "do not have access", "no access"}
	enTargets := []string{"local", "file", "directory", "project", "workspace", "filesystem", "path"}
	for _, pre := range enPrefixes {
		if idx := strings.Index(low, pre); idx >= 0 {
			tail := low[idx+len(pre):]
			if len(tail) > 120 {
				tail = tail[:120]
			}
			for _, tgt := range enTargets {
				if strings.Contains(tail, tgt) {
					return true
				}
			}
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

var sandboxHallucinationPatterns = []string{
	"I can run that for you",
	"I'll run that",
	"let me run that",
	"let me execute",
	"running in sandbox",
	"executing in sandbox",
	"code interpreter",
	"python sandbox",
	"sandbox environment",
	"/mnt/data",
	"/mnt/data 为空",
	"/mnt/data is empty",
	"linux container",
	"linux sandbox",
	"cloud sandbox",
	"execution environment has changed",
	"cannot access the Windows path",
	"only provides Linux",
	"只提供 Linux 容器",
	"no Windows execution",
	"don't have a Windows",
	"cannot execute on Windows",
	"no execution channel",
	"没有 Windows 执行通道",
	"没有执行通道",
	"cannot run commands on",
	"don't have command execution",
	"无法执行命令",
	"执行环境已经切换",
	"I don't have SSH access tools",
	"I don't have any tools",
	"none of which can reach",
	"打包为 zip",
	"打包为zip",
	"上传 zip",
	"upload a zip",
	"upload as zip",
	"upload zip",
	"upload a .zip",
	"请将项目目录打包",
	"请将项目打包",
	"打包上传",
	"上传项目文件",
	"上传源代码",
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
