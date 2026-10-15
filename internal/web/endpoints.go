package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// APIEndpointInfo defines metadata for an exposed API route.
type APIEndpointInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Method        string `json:"method"`
	Category      string `json:"category"`
	Description   string `json:"description"`
	RequiresAuth  bool   `json:"requiresAuth"`
	PublicAllowed bool   `json:"publicAllowed"`
	Enabled       bool   `json:"enabled"`
	CurlExample   string `json:"curlExample"`
}

func getEndpointCatalog(s runtimeSettings) []APIEndpointInfo {
	imgModel := s.DefaultImageModel
	if strings.TrimSpace(imgModel) == "" {
		imgModel = "gpt-image-2"
	}

	imgAuthHeader := "  -H \"Authorization: Bearer $API_KEY\" \\\n"
	if s.EnableImageAPIPublic {
		imgAuthHeader = "  # 免授权公开访问 (无需 API Key)\n"
	}

	modelsAuthHeader := "  -H \"Authorization: Bearer $API_KEY\""
	if s.EnableModelsAPIPublic {
		modelsAuthHeader = "  # 免授权公开访问 (无需 API Key)"
	}

	chatAuthHeader := "  -H \"Authorization: Bearer $API_KEY\" \\\n"
	if s.EnableChatAPIPublic {
		chatAuthHeader = "  # 免授权公开访问 (无需 API Key)\n"
	}

	return []APIEndpointInfo{
		{
			ID:            "image_generations",
			Name:          "图片生成 (Image Generations)",
			Path:          "/v1/images/generations",
			Method:        "POST",
			Category:      "Images",
			Description:   "基于 Prompt 生成高质量图片，兼容 OpenAI /v1/images/generations 规范。支持多种图片模型。",
			RequiresAuth:  !s.EnableImageAPIPublic,
			PublicAllowed: true,
			Enabled:       s.EnableImageAPI,
			CurlExample: fmt.Sprintf("curl http://127.0.0.1:4141/v1/images/generations \\\n  -H \"Content-Type: application/json\" \\\n%s  -d '{\n    \"prompt\": \"A futuristic cyber city with neon lights\",\n    \"model\": \"%s\",\n    \"size\": \"1024x1024\"\n  }'", imgAuthHeader, imgModel),
		},
		{
			ID:            "image_edits",
			Name:          "图片编辑 (Image Edits)",
			Path:          "/v1/images/edits",
			Method:        "POST",
			Category:      "Images",
			Description:   "基于已有图片与指令进行编辑修图，兼容 OpenAI multipart/form-data 格式。",
			RequiresAuth:  !s.EnableImageAPIPublic,
			PublicAllowed: true,
			Enabled:       s.EnableImageAPI,
			CurlExample: fmt.Sprintf("curl http://127.0.0.1:4141/v1/images/edits \\\n%s  -F \"image=@sample.png\" \\\n  -F \"prompt=Add warm sunset glow to the sky\" \\\n  -F \"model=%s\"", imgAuthHeader, imgModel),
		},
		{
			ID:            "chat_completions",
			Name:          "对话补全 (Chat Completions)",
			Path:          "/v1/chat/completions",
			Method:        "POST",
			Category:      "Chat",
			Description:   "OpenAI 兼容对话补全接口，支持流式输出 (SSE)、工具调用 (Tool Calls / Function Calling) 与深度思考推理模型。",
			RequiresAuth:  !s.EnableChatAPIPublic,
			PublicAllowed: true,
			Enabled:       true,
			CurlExample: fmt.Sprintf("curl http://127.0.0.1:4141/v1/chat/completions \\\n  -H \"Content-Type: application/json\" \\\n%s  -d '{\n    \"model\": \"gpt-5.4\",\n    \"messages\": [{\"role\": \"user\", \"content\": \"你好，请用一句话介绍你自己\"}],\n    \"stream\": false\n  }'", chatAuthHeader),
		},
		{
			ID:            "responses",
			Name:          "Responses API (Codex / Responses)",
			Path:          "/v1/responses",
			Method:        "POST",
			Category:      "Chat",
			Description:   "OpenAI Responses API 规范兼容接口，原生支持 Codex CLI (wire_api = \"responses\")、会话连续上下文追溯 (previous_response_id) 与工具状态机流式调用。",
			RequiresAuth:  true,
			PublicAllowed: false,
			Enabled:       true,
			CurlExample:   "curl http://127.0.0.1:4141/v1/responses \\\n  -H \"Content-Type: application/json\" \\\n  -H \"Authorization: Bearer $API_KEY\" \\\n  -d '{\n    \"model\": \"gpt-5.4\",\n    \"input\": \"Hello Codex!\",\n    \"stream\": false\n  }'",
		},
		{
			ID:            "models",
			Name:          "模型列表 (Models List)",
			Path:          "/v1/models",
			Method:        "GET",
			Category:      "Models",
			Description:   "获取当前网关所支持的模型目录清单，供第三方客户端（NextChat、LobeChat、Cursor 等）自动发现可用模型。",
			RequiresAuth:  !s.EnableModelsAPIPublic,
			PublicAllowed: true,
			Enabled:       true,
			CurlExample: fmt.Sprintf("curl http://127.0.0.1:4141/v1/models \\\n%s", modelsAuthHeader),
		},
		{
			ID:            "claude_messages",
			Name:          "Claude 消息 (Anthropic Messages)",
			Path:          "/v1/messages",
			Method:        "POST",
			Category:      "Anthropic",
			Description:   "Anthropic Claude 协议兼容接口，供 Claude Code、Cursor、Roo Code 等高级 Agent 客户端原生接入。",
			RequiresAuth:  true,
			PublicAllowed: false,
			Enabled:       true,
			CurlExample: "curl http://127.0.0.1:4141/v1/messages \\\n  -H \"x-api-key: $API_KEY\" \\\n  -H \"anthropic-version: 2023-06-01\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\n    \"model\": \"claude-3-5-sonnet-20241022\",\n    \"max_tokens\": 1024,\n    \"messages\": [{\"role\": \"user\", \"content\": \"Hello Claude!\"}]\n  }'",
		},
		{
			ID:            "mcp_sse",
			Name:          "MCP 服务 (SSE & Message)",
			Path:          "/v1/mcp/sse",
			Method:        "GET",
			Category:      "MCP",
			Description:   "Model Context Protocol (MCP) 服务器端点，支持 Cursor、VS Code MCP 客户端的 Server-Sent Events 通信。",
			RequiresAuth:  true,
			PublicAllowed: false,
			Enabled:       true,
			CurlExample: "curl -N http://127.0.0.1:4141/v1/mcp/sse \\\n  -H \"Authorization: Bearer $API_KEY\"",
		},
	}
}

// isPublicEndpoint reports whether an incoming path is configured for public (unauthenticated) access.
func (s *Server) isPublicEndpoint(path string) bool {
	if s.settings == nil {
		return false
	}
	cfg := s.settings.get()
	if (path == "/v1/images/generations" || path == "/v1/images/edits") && cfg.EnableImageAPIPublic {
		return true
	}
	if path == "/v1/models" && cfg.EnableModelsAPIPublic {
		return true
	}
	if path == "/v1/chat/completions" && cfg.EnableChatAPIPublic {
		return true
	}
	return false
}

// adminEndpoints manages API route permissions and global parameters.
func (s *Server) adminEndpoints(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cur := s.settings.get()
		endpoints := getEndpointCatalog(cur)
		jsonOut(w, map[string]any{
			"ok":        true,
			"endpoints": endpoints,
			"config": map[string]any{
				"enableImageApi":       cur.EnableImageAPI,
				"enableImageApiPublic": cur.EnableImageAPIPublic,
				"enableChatApiPublic":  cur.EnableChatAPIPublic,
				"enableModelsApiPublic": cur.EnableModelsAPIPublic,
				"defaultImageModel":    firstNonEmpty(cur.DefaultImageModel, "gpt-image-2"),
				"availableImageModels": availableImageModels,
			},
		})
	case http.MethodPut:
		cur := s.settings.get()
		base, _ := json.Marshal(cur)
		var merged map[string]any
		if json.Unmarshal(base, &merged) != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "marshal settings")
			return
		}

		var patch map[string]any
		if json.NewDecoder(r.Body).Decode(&patch) != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
			return
		}

		for k, v := range patch {
			merged[k] = v
		}

		mergedJSON, _ := json.Marshal(merged)
		var v runtimeSettings
		if json.Unmarshal(mergedJSON, &v) != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
			return
		}

		if err := s.settings.save(v); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}

		jsonOut(w, map[string]any{
			"ok": true,
			"config": map[string]any{
				"enableImageApi":       v.EnableImageAPI,
				"enableImageApiPublic": v.EnableImageAPIPublic,
				"enableChatApiPublic":  v.EnableChatAPIPublic,
				"enableModelsApiPublic": v.EnableModelsAPIPublic,
				"defaultImageModel":    firstNonEmpty(v.DefaultImageModel, "gpt-image-2"),
				"availableImageModels": availableImageModels,
			},
			"endpoints": getEndpointCatalog(v),
		})
	default:
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
	}
}
