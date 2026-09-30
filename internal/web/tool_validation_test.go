package web

import (
	"encoding/json"
	"testing"
)

func TestValidateDetectedToolCallsRejectsUndeclaredName(t *testing.T) {
	calls := []detectedToolCall{{
		ID:        "tool_call_0",
		Name:      "unknown_tool",
		Arguments: json.RawMessage(`{"path":"E:\\SoarClient-fork","pattern":"*.jsonl"}`),
	}}

	valid, rejected := validateDetectedToolCalls(calls, testTools(), "auto")
	if len(valid) != 0 {
		t.Fatalf("undeclared call escaped validation: %#v", valid)
	}
	if len(rejected) != 1 || rejected[0].Name != "unknown_tool" {
		t.Fatalf("rejected=%#v", rejected)
	}
}

func TestValidateDetectedToolCallsRejectsInvalidArguments(t *testing.T) {
	calls := []detectedToolCall{{
		ID:        "tool_call_0",
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"city":2}`),
	}}

	valid, rejected := validateDetectedToolCalls(calls, testTools(), "auto")
	if len(valid) != 0 || len(rejected) != 1 {
		t.Fatalf("valid=%#v rejected=%#v", valid, rejected)
	}
}

func TestValidateDetectedToolCallsAcceptsDeclaredCall(t *testing.T) {
	calls := []detectedToolCall{{
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"city":"Paris"}`),
	}}

	valid, rejected := validateDetectedToolCalls(calls, testTools(), "auto")
	if len(rejected) != 0 || len(valid) != 1 {
		t.Fatalf("valid=%#v rejected=%#v", valid, rejected)
	}
	if valid[0].ID == "" || valid[0].Type != "function" {
		t.Fatalf("call was not normalized: %#v", valid[0])
	}
}

func TestDeclaredToolNames(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "fetch_data"}},
		{"type": "function", "function": map[string]any{"name": "write_file"}},
	}
	names := declaredToolNames(tools)
	if len(names) != 2 || names[0] != "fetch_data" || names[1] != "write_file" {
		t.Fatalf("unexpected declared tool names: %v", names)
	}
}

func TestValidateDetectedToolCallsNormalizesAliases(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name": "Read",
				"parameters": map[string]any{
					"type":                 "object",
					"required":             []any{"file_path"},
					"additionalProperties": false,
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string"},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name": "Edit",
				"parameters": map[string]any{
					"type":                 "object",
					"required":             []any{"file_path", "old_string", "new_string"},
					"additionalProperties": false,
					"properties": map[string]any{
						"file_path":  map[string]any{"type": "string"},
						"old_string": map[string]any{"type": "string"},
						"new_string": map[string]any{"type": "string"},
					},
				},
			},
		},
	}

	// Read call with 'path' should normalize to 'file_path'
	calls := []detectedToolCall{{
		Name:      "Read",
		Arguments: json.RawMessage(`{"path":"/workspace/repo/main.go"}`),
	}}
	valid, rejected := validateDetectedToolCalls(calls, tools, "auto")
	if len(rejected) != 0 || len(valid) != 1 {
		t.Fatalf("expected Read to pass normalization, got rejected=%#v, valid=%#v", rejected, valid)
	}
	var readArgs map[string]any
	_ = json.Unmarshal(valid[0].Arguments, &readArgs)
	if readArgs["file_path"] != "/workspace/repo/main.go" {
		t.Fatalf("expected file_path in normalized args, got: %#v", readArgs)
	}
	if _, hasPath := readArgs["path"]; hasPath {
		t.Fatalf("expected 'path' alias to be cleaned for additionalProperties:false, got: %#v", readArgs)
	}

	// Edit call with 'path', 'old', 'new' should normalize to 'file_path', 'old_string', 'new_string'
	editCalls := []detectedToolCall{{
		Name:      "Edit",
		Arguments: json.RawMessage(`{"path":"/workspace/repo/main.go","old":"foo()","new":"bar()"}`),
	}}
	valid, rejected = validateDetectedToolCalls(editCalls, tools, "auto")
	if len(rejected) != 0 || len(valid) != 1 {
		t.Fatalf("expected Edit to pass normalization, got rejected=%#v, valid=%#v", rejected, valid)
	}
	var editArgs map[string]any
	_ = json.Unmarshal(valid[0].Arguments, &editArgs)
	if editArgs["file_path"] != "/workspace/repo/main.go" || editArgs["old_string"] != "foo()" || editArgs["new_string"] != "bar()" {
		t.Fatalf("expected normalized Edit args, got: %#v", editArgs)
	}
}

func TestTrySynthesizeWorkspaceInspectionDoesNotSynthesizeWrites(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "Write", "parameters": map[string]any{"type": "object", "required": []any{"file_path", "content"}, "properties": map[string]any{"file_path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "Edit", "parameters": map[string]any{"type": "object", "required": []any{"file_path", "old_string", "new_string"}, "properties": map[string]any{"file_path": map[string]any{"type": "string"}, "old_string": map[string]any{"type": "string"}, "new_string": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "list_directory", "parameters": map[string]any{"type": "object", "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object", "required": []any{"file_path"}, "properties": map[string]any{"file_path": map[string]any{"type": "string"}}}}},
	}

	prompt := "Please edit and update CLAUDE.md in /home/user/workspace/repo to add build instructions"
	refusal := "我无法直接读取或写入本地文件 /home/user/workspace/repo"

	synth, ok := trySynthesizeWorkspaceInspection(prompt, refusal, tools, "auto")
	if !ok || len(synth) == 0 {
		t.Fatalf("expected inspection synthesis, got ok=%v, synth=%#v", ok, synth)
	}

	// Must NOT synthesize Write or Edit
	if synth[0].Name == "Write" || synth[0].Name == "Edit" {
		t.Fatalf("must not synthesize write or edit tools during inspection: %s", synth[0].Name)
	}
	// Inspection must be read or list
	if synth[0].Name != "list_directory" && synth[0].Name != "read_file" {
		t.Fatalf("unexpected synthesized tool: %s", synth[0].Name)
	}
}
