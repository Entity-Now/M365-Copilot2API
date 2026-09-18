package chathub

import "encoding/json"

type Tool struct {
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function,omitempty"`
}

func clientPlugins(tools []Tool, mcpServerURL string) []any {
	// The audited HAR set verifies only the built-in BingWebSearch declaration.
	// It does not verify the former API-plugin or synthetic MCPServer payload
	// shapes, so client tools must stay on the gateway's validated router path.
	_ = tools
	_ = mcpServerURL
	return []any{map[string]any{"Id": "BingWebSearch", "Source": "BuiltIn"}}
}
