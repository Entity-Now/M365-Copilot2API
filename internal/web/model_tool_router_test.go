package web

import (
	"strings"
	"testing"
)

func TestParseModelToolDecisionAutoAndParallel(t *testing.T) {
	calls, ok := parseModelToolDecision(`{"calls":[{"name":"get_weather","arguments":{"city":"Beijing"}},{"name":"get_time","arguments":{"city":"Beijing"}}]}`, testTools(), "auto")
	if !ok || len(calls) != 2 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}
func TestParseModelToolDecisionNoCall(t *testing.T) {
	calls, ok := parseModelToolDecision(`{"calls":[]}`, testTools(), "auto")
	if !ok || len(calls) != 0 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}
func TestModelToolRouterPromptMarksCompletedResults(t *testing.T) {
	p := modelToolRouterPrompt(`assistant tool_calls: [...]
tool[call_x]: 2026-07-18`, testTools(), "auto")
	if !strings.Contains(p, "Completed evidence must not be repeated") || !strings.Contains(p, "tool[call_x]: 2026-07-18") || !strings.Contains(p, "unfinished work remains") {
		t.Fatalf("missing multi-turn evidence constraint: %s", p)
	}
}

func TestParseModelToolDecisionRejectsBadSchema(t *testing.T) {
	calls, ok := parseModelToolDecision("```json\n{\"calls\":[{\"name\":\"get_weather\",\"arguments\":{\"city\":2}}]}\n```", testTools(), "auto")
	if !ok || len(calls) != 0 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}

func TestParseModelToolDecisionMultipleCallToolLines(t *testing.T) {
	text := `I will check the weather and time for Beijing:
CALL_TOOL: get_weather({"city":"Beijing"})
CALL_TOOL: get_time({"city":"Beijing"})`
	calls, ok := parseModelToolDecision(text, testTools(), "auto")
	if !ok || len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d (ok=%v)", len(calls), ok)
	}
	if calls[0].Name != "get_weather" || calls[1].Name != "get_time" {
		t.Fatalf("unexpected call order/names: %v", calls)
	}
}

func TestParseModelToolDecisionMultilineArgs(t *testing.T) {
	text := `CALL_TOOL: get_weather({
		"city": "Beijing"
	})`
	calls, ok := parseModelToolDecision(text, testTools(), "auto")
	if !ok || len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d (ok=%v)", len(calls), ok)
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("unexpected tool name: %s", calls[0].Name)
	}
}

func TestModelToolRouterPromptSufficientContextConstraint(t *testing.T) {
	p := modelToolRouterPrompt("analyze project files", testTools(), "auto")
	if !strings.Contains(p, "Sufficient context required") {
		t.Fatalf("expected Sufficient context required in prompt: %s", p)
	}
}

func TestParseModelToolDecisionRejectsSandboxHallucination(t *testing.T) {
	text := `但当前会话中没有读取到项目文件，/mnt/data 为空，因此暂时无法做基于源码的可靠评估，也无法将文档写入你提到的项目根目录。请将项目目录打包为 ZIP 上传，或至少上传以下内容：

- 主要源代码目录
- README.md
- requirements.txt、pyproject.toml 或其他依赖文件
- UI 模板与静态资源
- 配置文件及 .env.example，请勿上传真实密钥
- 测试、Docker、CI/CD 等相关文件

收到文件后，我会直接完成检查并生成类似 PROJECT_OPTIMIZATION_REVIEW.md 的报告，不需要你再逐步确认。`

	calls, ok := parseModelToolDecision(text, testTools(), "auto")
	if ok || len(calls) > 0 {
		t.Fatalf("expected sandbox hallucination to be rejected, got ok=%v calls=%v", ok, calls)
	}
}

func TestParseModelToolDecisionSingleObjectAndArrayJSON(t *testing.T) {
	single := `{"name":"get_weather","arguments":{"city":"Beijing"}}`
	calls, ok := parseModelToolDecision(single, testTools(), "auto")
	if !ok || len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("expected 1 call, got ok=%v calls=%v", ok, calls)
	}

	arr := `[{"name":"get_weather","arguments":{"city":"Beijing"}}]`
	calls2, ok2 := parseModelToolDecision(arr, testTools(), "auto")
	if !ok2 || len(calls2) != 1 || calls2[0].Name != "get_weather" {
		t.Fatalf("expected 1 call from array, got ok=%v calls=%v", ok2, calls2)
	}
}

func TestModelToolRouterPromptAntiSandbox(t *testing.T) {
	p := modelToolRouterPrompt("analyze project files", testTools(), "auto")
	if !strings.Contains(p, "NO SANDBOX") || !strings.Contains(p, "NO /mnt/data") || !strings.Contains(p, "ROUTER MANDATORY DIRECTIVE") {
		t.Fatalf("expected anti-sandbox directives in router prompt: %s", p)
	}
}

func TestModelToolSlimRouterPrompt(t *testing.T) {
	tools := testTools()
	p := modelToolSlimRouterPrompt("check files", tools, "auto")
	if !strings.Contains(p, "Available tools (summary):") {
		t.Fatalf("expected summary list header: %s", p)
	}
	if !strings.Contains(p, "- get_weather:") || !strings.Contains(p, "- get_time:") {
		t.Fatalf("expected tool names in summary: %s", p)
	}
	if !strings.Contains(p, "NEED_TOOLS:") || !strings.Contains(p, "BATCH MULTI-TOOL CALLING:") {
		t.Fatalf("expected NEED_TOOLS and batch directive in slim router prompt: %s", p)
	}
}

func TestParseNeedToolsDecision(t *testing.T) {
	tools := testTools()
	tests := []struct {
		input    string
		expected []string
	}{
		{"NEED_TOOLS: [get_weather, get_time]", []string{"get_weather", "get_time"}},
		{"NEED_TOOLS: get_weather, unknown_tool", []string{"get_weather"}},
		{"need_tools: [\"get_weather\", \"get_time\"]", []string{"get_weather", "get_time"}},
		{"CALL_TOOL: get_weather({\"city\":\"Beijing\"})", nil},
		{"NO_TOOL_NEEDED", nil},
	}
	for _, tc := range tests {
		got := parseNeedToolsDecision(tc.input, tools)
		if len(got) != len(tc.expected) {
			t.Fatalf("input %q: got %v, want %v", tc.input, got, tc.expected)
		}
		for i := range got {
			if got[i] != tc.expected[i] {
				t.Fatalf("input %q at %d: got %s, want %s", tc.input, i, got[i], tc.expected[i])
			}
		}
	}
}

func TestFilterToolsByName(t *testing.T) {
	tools := testTools()
	filtered := filterToolsByName(tools, []string{"get_weather"})
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered tool, got %d", len(filtered))
	}
	fn, _ := filtered[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("unexpected filtered tool: %v", fn)
	}
}

func TestCompactToolSchemaList(t *testing.T) {
	tools := testTools()
	compact := compactToolSchemaList(tools)
	if len(compact) != len(tools) {
		t.Fatalf("expected %d compact tools, got %d", len(tools), len(compact))
	}
	fn, _ := compact[0]["function"].(map[string]any)
	if fn["name"] == "" {
		t.Fatal("expected function name to be preserved")
	}
}

func TestModelToolRouterPromptPreservesFullTools(t *testing.T) {
	customTools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "search_code",
				"description": "Searches codebase",
				"parameters": map[string]any{
					"$schema":              "http://json-schema.org/draft-07/schema#",
					"title":                "SearchCodeParams",
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"query"},
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "search query string",
						},
					},
				},
			},
		},
	}
	p := modelToolRouterPrompt("search for main function", customTools, "auto")
	// The default router prompt must preserve original $schema, title, and all properties without compaction
	if !strings.Contains(p, "SearchCodeParams") {
		t.Fatalf("expected full uncompacted schema (SearchCodeParams) in default router prompt: %s", p)
	}
	if !strings.Contains(p, "draft-07/schema") {
		t.Fatalf("expected full $schema in default router prompt: %s", p)
	}
}

func TestIsToolRefusalRealWorld(t *testing.T) {
	refusalText := `我可以帮你完成，但当前对话里有一个关键限制：

你要求的是**“按照规划内容完成文档编写”**，而真正编写这些文档必须先扫描并阅读整个 C:\Langauge\CSharp\WDM 项目的结构、README、解决方案、模块、现有 docs 等内容，否则会违反你自己在 CLAUDE.md 里要求的：

获取充分上下文，必须直接查阅全部关键文件后再输出；禁止凭空猜测。

而我当前无法直接读取你本地 C:\Langauge\CSharp\WDM 的完整项目内容，因此现在如果直接生成几十份文档，大部分都会变成假设性的内容。

我建议按下面顺序进行：`

	if !isToolRefusal(refusalText) {
		t.Fatalf("expected real-world refusal text to be identified as tool refusal")
	}

	calls, parsed := parseModelToolDecision(refusalText, testTools(), "auto")
	if parsed || len(calls) > 0 {
		t.Fatalf("expected refusal to return parsed=false, got parsed=%v, calls=%v", parsed, calls)
	}
}

func TestExtractTargetWorkspacePath(t *testing.T) {
	text := `而我当前无法直接读取你本地 C:\Langauge\CSharp\WDM 的完整项目内容`
	path := extractTargetWorkspacePath(text, "")
	if path != `C:\Langauge\CSharp\WDM` {
		t.Fatalf("expected C:\\Langauge\\CSharp\\WDM, got %s", path)
	}

	linuxText := "Please inspect /home/user/myproject/src before continuing"
	linuxPath := extractTargetWorkspacePath("", linuxText)
	if linuxPath != "/home/user/myproject/src" {
		t.Fatalf("expected /home/user/myproject/src, got %s", linuxPath)
	}
}

func TestTrySynthesizeWorkspaceInspection(t *testing.T) {
	clientTools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name": "list_dir",
				"parameters": map[string]any{
					"type":     "object",
					"required": []any{"path"},
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name": "view_file",
				"parameters": map[string]any{
					"type":     "object",
					"required": []any{"AbsolutePath"},
					"properties": map[string]any{
						"AbsolutePath": map[string]any{"type": "string"},
					},
				},
			},
		},
	}

	refusal := `当前无法直接读取你本地 C:\Langauge\CSharp\WDM 的完整项目内容`
	prompt := `请帮我编写 C:\Langauge\CSharp\WDM 的文档`

	calls, ok := trySynthesizeWorkspaceInspection(prompt, refusal, clientTools, "auto")
	if !ok || len(calls) != 1 {
		t.Fatalf("expected synthesized call, got ok=%v, calls=%v", ok, calls)
	}
	if calls[0].Name != "list_dir" {
		t.Fatalf("expected list_dir tool, got %s", calls[0].Name)
	}
	if !strings.Contains(string(calls[0].Arguments), `C:\\Langauge\\CSharp\\WDM`) {
		t.Fatalf("expected arguments to contain path, got %s", string(calls[0].Arguments))
}
}

func TestModelToolRouterPromptOnDemandCatalog(t *testing.T) {
	p := modelToolRouterPrompt("what is the weather?", testTools(), "auto", true, true)
	if !strings.Contains(p, "Gateway Inspection Tools") || !strings.Contains(p, "describe_tool") || !strings.Contains(p, "search_tools") {
		t.Fatalf("expected on-demand inspection tools in prompt: %s", p)
	}
	if !strings.Contains(p, "- get_weather") {
		t.Fatalf("expected get_weather client tool in catalog: %s", p)
	}
}

func TestParseModelToolDecisionInternalGatewayTools(t *testing.T) {
	text := `CALL_TOOL: describe_tool({"tool_name":"get_weather"})`
	calls, ok := parseModelToolDecision(text, testTools(), "auto")
	if !ok || len(calls) != 1 || calls[0].Name != "describe_tool" {
		t.Fatalf("expected describe_tool call, got ok=%v calls=%v", ok, calls)
	}

	search := `CALL_TOOL: search_tools({"query":"weather"})`
	calls2, ok2 := parseModelToolDecision(search, testTools(), "auto")
	if !ok2 || len(calls2) != 1 || calls2[0].Name != "search_tools" {
		t.Fatalf("expected search_tools call, got ok=%v calls=%v", ok2, calls2)
	}

	arr := `[{"name":"describe_tool","arguments":{"tool_name":"get_weather"}}]`
	calls3, ok3 := parseModelToolDecision(arr, testTools(), "auto")
	if !ok3 || len(calls3) != 1 || calls3[0].Name != "describe_tool" {
		t.Fatalf("expected describe_tool from array, got ok=%v calls=%v", ok3, calls3)
	}
}

func TestExecuteGatewayTool(t *testing.T) {
	tools := testTools()

	// describe_tool
	call := detectedToolCall{
		Name:      "describe_tool",
		Arguments: []byte(`{"tool_name":"get_weather"}`),
	}
	res := executeGatewayTool(call, tools)
	if !strings.Contains(res, "Full specification for tool \"get_weather\"") || !strings.Contains(res, "city") {
		t.Fatalf("unexpected describe_tool result: %s", res)
	}

	// search_tools
	callSearch := detectedToolCall{
		Name:      "search_tools",
		Arguments: []byte(`{"query":"weather"}`),
	}
	resSearch := executeGatewayTool(callSearch, tools)
	if !strings.Contains(resSearch, "Matching tools") || !strings.Contains(resSearch, "get_weather") {
		t.Fatalf("unexpected search_tools result: %s", resSearch)
	}

	// list_tools
	callList := detectedToolCall{
		Name: "list_tools",
	}
	resList := executeGatewayTool(callList, tools)
	if !strings.Contains(resList, "Available client tools") || !strings.Contains(resList, "get_weather") {
		t.Fatalf("unexpected list_tools result: %s", resList)
	}
}


