package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"m365-copilot2api/internal/chathub"
	"sync"
	"time"
)

type cachedConversation struct {
	ConversationID string
	SessionID      string
	Tone           string
	TurnCount      int
	MessageCount   int
	CreatedAt      time.Time
	LastUsedAt     time.Time
	SystemPrompt   string
	MessagesHash   string
}

type conversationCache struct {
	mu      sync.Mutex
	entries map[string]*cachedConversation
	maxAge  time.Duration
}

func newConversationCache() *conversationCache {
	return &conversationCache{
		entries: make(map[string]*cachedConversation),
		maxAge:  2 * time.Hour,
	}
}

func (c *conversationCache) key(namespace, accountID, model string) string {
	return namespace + "\x00" + accountID + "\x00" + model
}

func (c *conversationCache) Lookup(namespace, accountID, model string) *cachedConversation {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[c.key(namespace, accountID, model)]
	if entry == nil {
		return nil
	}
	if time.Since(entry.LastUsedAt) > c.maxAge {
		delete(c.entries, c.key(namespace, accountID, model))
		return nil
	}
	return entry
}

func (c *conversationCache) Store(namespace, accountID, model string, conv *cachedConversation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conv.LastUsedAt = time.Now()
	c.entries[c.key(namespace, accountID, model)] = conv
}

func (c *conversationCache) Invalidate(namespace, accountID, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, c.key(namespace, accountID, model))
}

func (c *conversationCache) GC() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.entries {
		if now.Sub(v.LastUsedAt) > c.maxAge {
			delete(c.entries, k)
		}
	}
}

func (c *conversationCache) Stats() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{"cached_conversations": len(c.entries)}
}

func systemPromptHash(messages []oaiMsg) string {
	for _, m := range messages {
		if m.Role == "system" || m.Role == "developer" {
			text := contentToString(m.Content)
			h := sha256.Sum256([]byte(text))
			return hex.EncodeToString(h[:])
		}
	}
	return ""
}

func messagesHash(messages []oaiMsg) string {
	h := sha256.New()
	for _, m := range messages {
		h.Write([]byte(m.Role + "\x00"))
		h.Write([]byte(contentToString(m.Content) + "\x00"))
		if m.ToolCallID != "" {
			h.Write([]byte(m.ToolCallID + "\x00"))
		}
		if len(m.ToolCalls) > 0 {
			b, _ := json.Marshal(m.ToolCalls)
			h.Write(b)
			h.Write([]byte("\x00"))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func extractLastUserMessage(messages []oaiMsg) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return contentToString(messages[i].Content)
		}
	}
	return ""
}

func (s *Server) storeConvCache(namespace, accID, model string, res chathub.Result, tone string, messages []oaiMsg, reused bool) {
	if res.ConversationID == "" {
		return
	}
	cached := s.convCache.Lookup(namespace, accID, model)
	entry := &cachedConversation{
		ConversationID: res.ConversationID,
		SessionID:      res.SessionID,
		Tone:           tone,
		MessageCount:   len(messages),
		SystemPrompt:   systemPromptHash(messages),
		MessagesHash:   messagesHash(messages),
	}
	if cached != nil && cached.ConversationID == res.ConversationID {
		entry.TurnCount = cached.TurnCount + 1
	} else {
		entry.TurnCount = 1
	}
	s.convCache.Store(namespace, accID, model, entry)
}

func (s *Server) invalidateConvCache(namespace, accID, model string) {
	s.convCache.Invalidate(namespace, accID, model)
}
