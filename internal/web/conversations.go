package web

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	jsonOut(w, map[string]any{"conversations": s.sessions.list()})
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.ID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
		return
	}
	s.conversationManager.Delete(body.ID)
	s.sessions.delete(body.ID)
	jsonOut(w, map[string]string{"status": "deleted"})
}

func (s *Server) conversationCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body struct {
		Mode  string `json:"mode"`
		KeepN int    `json:"keep_n"`
	}
	if json.NewDecoder(r.Body).Decode(&body) == nil {
		if body.Mode != "" {
			s.conversationManager.SetMode(ConversationCleanupMode(body.Mode))
		}
	}
	cleaned := s.conversationManager.Cleanup()
	jsonOut(w, map[string]any{
		"status":    "cleaned",
		"mode":      string(s.conversationManager.Mode()),
		"deleted":   cleaned,
		"remaining": len(s.conversationManager.List()),
	})
}

// publicSessionID returns the identifier a client uses to refer to a binding:
// its own explicit X-M365-Session-Id when it set one, otherwise the internal
// session id. The tenant hash and stored conversation history are never
// exposed through the API.
func publicSessionID(sess sessionBinding) string {
	if sess.ExplicitID != "" {
		return sess.ExplicitID
	}
	return sess.SessionID
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		sessions := s.sessionResolver.ListSessionsForTenant(tenant)
		data := make([]map[string]any, 0, len(sessions))
		for _, sess := range sessions {
			data = append(data, map[string]any{
				"id":              publicSessionID(sess),
				"conversation_id": sess.ConversationID,
				"created":         sess.CreatedAt.Unix(),
				"last_used":       sess.LastUsedAt.Unix(),
				"messages":        len(sess.ContextHistory),
			})
		}
		jsonOut(w, map[string]any{
			"object": "list",
			"data":   data,
		})
	case http.MethodPost:
		var body struct {
			SessionID string `json:"session_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		sess, ok := s.sessionResolver.GetSession(tenant, body.SessionID)
		if !ok {
			jsonOut(w, map[string]any{
				"object":     "session",
				"id":         body.SessionID,
				"created":    time.Now().Unix(),
				"expires_in": 1800,
				"status":     "created",
			})
			return
		}
		jsonOut(w, map[string]any{
			"object":          "session",
			"id":              publicSessionID(sess),
			"conversation_id": sess.ConversationID,
			"created":         sess.CreatedAt.Unix(),
			"status":          "active",
		})
	default:
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
	}
}

func (s *Server) handleCacheStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	stats := cacheStats.GetStats()
	jsonOut(w, map[string]any{
		"object":     "cache_stats",
		"stats":      stats,
		"conv_cache": s.convCache.Stats(),
	})
}

func (s *Server) handleCacheStatsReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	cacheStats.Reset()
	jsonOut(w, map[string]any{"status": "reset"})
}

func (s *Server) handleM365Conversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	if (s.cloudClients == nil || s.cloudClients.len() == 0) && len(s.sessionResolver.ListSessions()) == 0 {
		writeOpenAIError(w, http.StatusServiceUnavailable, "m365_not_configured", "m365_cloud_not_configured")
		return
	}
	rows := make(map[string]map[string]any)
	var cloudErr error
	if s.cloudClients != nil {
		for accountID, client := range s.cloudClients.list() {
			chats, err := client.ListConversations()
			if err != nil {
				cloudErr = err
				continue
			}
			for _, chat := range chats {
				conversationID, _ := chat["conversationId"].(string)
				if conversationID != "" {
					chat["accountId"] = accountID
					if account, found := s.tokens.Get(accountID); found {
						chat["accountEmail"] = account.Email
					}
					rows[accountID+"\x00"+conversationID] = chat
				}
			}
		}
	}
	if cloudErr != nil && len(s.sessionResolver.ListSessions()) == 0 {
		err := cloudErr
		writeOpenAIError(w, http.StatusBadGateway, "m365_error", err.Error())
		return
	}
	for _, session := range s.sessionResolver.ListSessions() {
		key := session.AccountID + "\x00" + session.ConversationID
		row, ok := rows[key]
		if !ok {
			row = map[string]any{}
			rows[key] = row
		}
		row["conversationId"] = session.ConversationID
		row["sessionId"] = session.SessionID
		row["accountId"] = session.AccountID
		row["createTimeUtc"] = session.CreatedAt.UnixMilli()
		row["updateTimeUtc"] = session.LastUsedAt.UnixMilli()
		row["messageCount"] = len(session.ContextHistory)
		row["historyAvailable"] = len(session.ContextHistory) > 0
		row["source"] = "gateway"
		if account, found := s.tokens.Get(session.AccountID); found {
			row["accountEmail"] = account.Email
		}
		if name, _ := row["chatName"].(string); strings.TrimSpace(name) == "" {
			row["chatName"] = conversationTitle(session.ContextHistory)
		}
	}

	data := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		data = append(data, row)
	}
	sort.Slice(data, func(i, j int) bool {
		return conversationTimestamp(data[i]) > conversationTimestamp(data[j])
	})
	response := map[string]any{"object": "list", "data": data, "count": len(data)}
	if cloudErr != nil {
		response["warning"] = cloudErr.Error()
	}
	jsonOut(w, response)
}

func (s *Server) handleM365ConversationDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	conversationID := strings.TrimSpace(r.URL.Query().Get("id"))
	if conversationID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "conversation id is required")
		return
	}
	session, found := s.sessionResolver.GetConversation(tenantFromRequest(r), conversationID)
	if !found {
		session, found = s.sessionResolver.GetConversationByID(conversationID)
	}
	if found {
		accountEmail := ""
		if account, ok := s.tokens.Get(session.AccountID); ok {
			accountEmail = account.Email
		}
		var parsedTools []map[string]any
		for _, t := range session.Tools {
			var fn map[string]any
			if len(t.Function) > 0 {
				_ = json.Unmarshal(t.Function, &fn)
			}
			parsedTools = append(parsedTools, map[string]any{
				"type":     t.Type,
				"function": fn,
			})
		}
		jsonOut(w, map[string]any{
			"object":         "conversation",
			"conversationId": session.ConversationID,
			"sessionId":      session.SessionID,
			"accountId":      session.AccountID,
			"accountEmail":   accountEmail,
			"chatName":       conversationTitle(session.ContextHistory),
			"createdAt":      session.CreatedAt,
			"updatedAt":      session.LastUsedAt,
			"messageCount":   len(session.ContextHistory),
			"messages":       session.ContextHistory,
			"tools":          parsedTools,
			"skills":         extractSkillsFromMessages(session.ContextHistory),
			"router":         getRouterDirectivesInfo(s.toolPlanningMode(), len(session.Tools) > 0),
		})
		return
	}

	// Fallback 1: sessionStore (s.sessions)
	if s.sessions != nil {
		for _, conv := range s.sessions.list() {
			if conv.ConversationID == conversationID || conv.ID == conversationID || conv.SessionID == conversationID {
				accountEmail := ""
				if account, ok := s.tokens.Get(conv.AccountID); ok {
					accountEmail = account.Email
				}
				title := conv.Title
				if title == "" {
					title = "M365 对话"
				}
				jsonOut(w, map[string]any{
					"object":         "conversation",
					"conversationId": conv.ConversationID,
					"sessionId":      conv.SessionID,
					"accountId":      conv.AccountID,
					"accountEmail":   accountEmail,
					"chatName":       title,
					"createdAt":      conv.CreatedAt,
					"updatedAt":      conv.UpdatedAt,
					"messageCount":   0,
					"messages":       []any{},
					"tools":          []any{},
					"skills":         []any{},
					"router":         getRouterDirectivesInfo(s.toolPlanningMode(), false),
				})
				return
			}
		}
	}

	// Fallback 2: conversationManager
	if s.conversationManager != nil {
		s.conversationManager.mu.Lock()
		managed, ok := s.conversationManager.data[conversationID]
		s.conversationManager.mu.Unlock()
		if ok {
			accountEmail := ""
			if account, ok := s.tokens.Get(managed.AccountID); ok {
				accountEmail = account.Email
			}
			title := managed.Title
			if title == "" {
				title = "M365 对话"
			}
			jsonOut(w, map[string]any{
				"object":         "conversation",
				"conversationId": managed.ID,
				"sessionId":      managed.ID,
				"accountId":      managed.AccountID,
				"accountEmail":   accountEmail,
				"chatName":       title,
				"createdAt":      managed.CreatedAt,
				"updatedAt":      managed.LastUsedAt,
				"messageCount":   0,
				"messages":       []any{},
				"tools":          []any{},
				"skills":         []any{},
				"router":         getRouterDirectivesInfo(s.toolPlanningMode(), false),
			})
			return
		}
	}

	// Fallback 3: Generic fallback for any M365 conversation ID so detail UI never 404s
	jsonOut(w, map[string]any{
		"object":         "conversation",
		"conversationId": conversationID,
		"sessionId":      conversationID,
		"accountId":      "-",
		"accountEmail":   "-",
		"chatName":       "M365 对话",
		"createdAt":      time.Now().UTC(),
		"updatedAt":      time.Now().UTC(),
		"messageCount":   0,
		"messages":       []any{},
		"tools":          []any{},
		"skills":         []any{},
		"router":         getRouterDirectivesInfo(s.toolPlanningMode(), false),
	})
}

var titleXMLTagRegex = regexp.MustCompile(`<[^>]+>`)
var titleAnthropicHeaderRegex = regexp.MustCompile(`(?i)x-anthropic-[^;]+;`)

func cleanTitleCandidate(s string) string {
	s = titleAnthropicHeaderRegex.ReplaceAllString(s, " ")
	s = titleXMLTagRegex.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func conversationTitle(messages []oaiMsg) string {
	for _, message := range messages {
		if message.Role != "user" && message.Role != "" {
			continue
		}
		raw := strings.TrimSpace(contentToString(message.Content))
		if raw == "" {
			continue
		}
		candidate := raw
		if idx := strings.Index(candidate, `<turn role="user">`); idx >= 0 {
			sub := candidate[idx+len(`<turn role="user">`):]
			if end := strings.Index(sub, "</turn>"); end >= 0 {
				candidate = sub[:end]
			} else {
				candidate = sub
			}
		} else if idx := strings.Index(candidate, `[user]`); idx >= 0 {
			sub := candidate[idx+len(`[user]`):]
			if end := strings.Index(sub, "\n["); end >= 0 {
				candidate = sub[:end]
			} else {
				candidate = sub
			}
		}
		text := cleanTitleCandidate(candidate)
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 60 {
			return string(runes[:60]) + "..."
		}
		return text
	}
	// Fallback: search non-system messages
	for _, message := range messages {
		if message.Role == "system" || message.Role == "developer" {
			continue
		}
		text := cleanTitleCandidate(contentToString(message.Content))
		if text != "" {
			runes := []rune(text)
			if len(runes) > 60 {
				return string(runes[:60]) + "..."
			}
			return text
		}
	}
	return "未命名对话"
}

func conversationTimestamp(row map[string]any) int64 {
	for _, key := range []string{"updateTimeUtc", "createTimeUtc"} {
		switch value := row[key].(type) {
		case float64:
			return int64(value)
		case int64:
			return value
		case int:
			return int64(value)
		}
	}
	return 0
}

func (s *Server) handleM365Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body struct {
		ConversationID string `json:"conversation_id"`
		AccountID      string `json:"account_id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.ConversationID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
		return
	}
	accountID := strings.TrimSpace(body.AccountID)
	if accountID == "" {
		session, ok := s.sessionResolver.GetConversationByID(body.ConversationID)
		if !ok {
			writeOpenAIError(w, http.StatusConflict, "account_required", "account_id is required when the conversation owner is unknown or ambiguous")
			return
		}
		accountID = session.AccountID
	}
	client, ok := s.cloudClients.get(accountID)
	if !ok {
		writeOpenAIError(w, http.StatusServiceUnavailable, "m365_not_configured", "m365_cloud_not_configured")
		return
	}
	if err := client.DeleteConversation(body.ConversationID); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "m365_error", err.Error())
		return
	}
	s.dropConversation(body.ConversationID)
	jsonOut(w, map[string]any{"status": "deleted", "conversation_id": body.ConversationID})
}

func (s *Server) handleM365Cleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body struct {
		MaxAgeHours int    `json:"max_age_hours"`
		KeepN       int    `json:"keep_n"`
		AccountID   string `json:"account_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	maxAge := time.Duration(body.MaxAgeHours) * time.Hour
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	keepN := body.KeepN
	if keepN <= 0 {
		keepN = 5
	}

	client, ok := s.cloudClients.get(strings.TrimSpace(body.AccountID))
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "account_required", "valid account_id is required")
		return
	}
	deleted, err := client.CleanupOldConversations(maxAge, keepN)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "m365_error", err.Error())
		return
	}
	jsonOut(w, map[string]any{"status": "cleaned", "deleted": deleted})
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	sessionID := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	if sessionID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "session_id required")
		return
	}
	if s.sessionResolver.DeleteSession(tenantFromRequest(r), sessionID) {
		jsonOut(w, map[string]any{"status": "deleted", "session_id": sessionID})
	} else {
		writeOpenAIError(w, http.StatusNotFound, "not_found", "session not found")
	}
}

type conversationWhitelistRequest struct {
	ConversationID string `json:"conversation_id"`
	Add            bool   `json:"add"`
}

func (s *Server) conversationWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var body conversationWhitelistRequest
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.ConversationID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "bad json")
		return
	}
	if body.Add {
		s.conversationManager.Whitelist(body.ConversationID)
	} else {
		s.conversationManager.Unwhitelist(body.ConversationID)
	}
	jsonOut(w, map[string]any{"status": "updated", "conversation_id": body.ConversationID, "whitelisted": body.Add})
}

var (
	reSkillsTag    = regexp.MustCompile(`(?is)<skills>(.*?)</skills>`)
	reSkillDefLine = regexp.MustCompile(`^[-*]\s+([a-zA-Z0-9_\-\.]+)\s*(?:\(([^)]+)\))?\s*:\s*(.+)$`)
)

func extractSkillsFromMessages(messages []oaiMsg) []map[string]string {
	skills := make([]map[string]string, 0)
	seen := make(map[string]bool)

	for _, msg := range messages {
		contentStr := ""
		switch c := msg.Content.(type) {
		case string:
			contentStr = c
		}
		if contentStr == "" {
			continue
		}

		section := ""
		if m := reSkillsTag.FindStringSubmatch(contentStr); len(m) > 1 {
			if idx := strings.Index(m[1], "Available skills:"); idx != -1 {
				section = m[1][idx:]
			} else {
				section = m[1]
			}
		} else if idx := strings.Index(contentStr, "Available skills:"); idx != -1 {
			section = contentStr[idx:]
		}

		if section == "" {
			continue
		}

		lines := strings.Split(section, "\n")
		var currentSkill map[string]string
		for _, rawLine := range lines {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "Available skills:") || strings.HasPrefix(line, "## Available skills") {
				continue
			}
			if match := reSkillDefLine.FindStringSubmatch(line); len(match) > 3 {
				name := strings.TrimSpace(match[1])
				lower := strings.ToLower(name)
				if lower == "scripts" || lower == "examples" || lower == "resources" || lower == "references" || lower == "skill.md" {
					continue
				}
				path := strings.TrimSpace(match[2])
				desc := strings.TrimSpace(match[3])
				if !seen[name] {
					seen[name] = true
					currentSkill = map[string]string{
						"name":        name,
						"path":        path,
						"description": desc,
					}
					skills = append(skills, currentSkill)
				}
			} else if currentSkill != nil {
				if strings.HasPrefix(line, "</skills>") || strings.HasPrefix(line, "```") {
					currentSkill = nil
					continue
				}
				currentSkill["description"] = currentSkill["description"] + "\n" + line
			}
		}
	}
	return skills
}

func getRouterDirectivesInfo(planningMode string, hasTools bool) map[string]any {
	rules := []string{
		"本地宿主权限：所有工具均直接在调用者的本地操作系统运行，具备本地工作区、相对路径与绝对路径的直接读写执行权限。",
		"严禁沙箱幻觉：网关是内部调度器，严禁声称处于云端沙箱或无本地权限，严禁索取 ZIP 压缩包上传。",
		"技能优先读取：当提示词包含 <skills> 或 Available skills: 且任务相关时，必须优先调用 view_file / Read 工具读取对应 SKILL.md。",
		"充分上下文保障：在得出结论或答复 NO_TOOL_NEEDED 之前，必须先读取所有必要的项目上下文文件。",
		"独立决策通道：网关在后台通过独立瞬态通道前置执行工具调度判定，完成后释放瞬态会话，以避免污染客户端历史上下文。",
	}
	return map[string]any{
		"planningMode": planningMode,
		"enabled":      hasTools && planningMode == "router",
		"title":        "网关智能路由守则 (Gateway Tool Router)",
		"description":  "网关采用两阶段规划架构：在模型返回结果前，先通过独立瞬态通道进行工具选择决策，既确保工具调用的严谨性，又完全避免污染客户端的原生对话历史。",
		"rules":        rules,
	}
}

func (s *Server) toolPlanningMode() string {
	if s != nil && s.settings != nil {
		return s.settings.get().ToolPlanningMode
	}
	return "router"
}

