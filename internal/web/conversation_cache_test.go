package web

import "testing"

func TestConversationCacheIsolatedByNamespace(t *testing.T) {
	c := newConversationCache()
	c.Store("tenant-a\x00session-a", "account", "model", &cachedConversation{ConversationID: "conversation-a"})
	c.Store("tenant-a\x00session-b", "account", "model", &cachedConversation{ConversationID: "conversation-b"})

	if got := c.Lookup("tenant-a\x00session-a", "account", "model"); got == nil || got.ConversationID != "conversation-a" {
		t.Fatalf("session-a lookup=%#v", got)
	}
	if got := c.Lookup("tenant-a\x00session-b", "account", "model"); got == nil || got.ConversationID != "conversation-b" {
		t.Fatalf("session-b lookup=%#v", got)
	}
	if got := c.Lookup("tenant-b\x00session-a", "account", "model"); got != nil {
		t.Fatalf("cross-tenant cache leak=%#v", got)
	}
}

func TestConversationCacheKeyCannotCollideAcrossComponents(t *testing.T) {
	cache := newConversationCache()
	first := cache.key("tenant", "account|model", "suffix")
	second := cache.key("tenant|account", "model", "suffix")
	if first == second {
		t.Fatal("conversation cache key collided across tuple components")
	}
}

func TestMessagesHashStrictlyCoversFullContext(t *testing.T) {
	base := []oaiMsg{
		{Role: "system", Content: "follow policy"},
		{Role: "assistant", ToolCalls: []map[string]any{{
			"id":   "call-1",
			"type": "function",
			"function": map[string]any{
				"name":      "lookup",
				"arguments": `{"query":"alpha"}`,
			},
		}}},
		{Role: "tool", ToolCallID: "call-1", Content: "result"},
	}
	baseHash := messagesHash(base)

	tests := []struct {
		name     string
		messages []oaiMsg
	}{
		{
			name: "role",
			messages: []oaiMsg{
				{Role: "developer", Content: "follow policy"},
				base[1], base[2],
			},
		},
		{
			name: "content",
			messages: []oaiMsg{
				{Role: "system", Content: "different policy"},
				base[1], base[2],
			},
		},
		{
			name: "tool call id",
			messages: []oaiMsg{
				base[0], base[1],
				{Role: "tool", ToolCallID: "call-2", Content: "result"},
			},
		},
		{
			name: "tool arguments",
			messages: []oaiMsg{
				base[0],
				{Role: "assistant", ToolCalls: []map[string]any{{
					"id":   "call-1",
					"type": "function",
					"function": map[string]any{
						"name":      "lookup",
						"arguments": `{"query":"beta"}`,
					},
				}}},
				base[2],
			},
		},
		{
			name:     "message count",
			messages: base[:2],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messagesHash(tt.messages); got == baseHash {
				t.Fatalf("messagesHash did not change for %s", tt.name)
			}
		})
	}

	clone := cloneMessages(base)
	if got := messagesHash(clone); got != baseHash {
		t.Fatalf("identical full context hash=%q, want %q", got, baseHash)
	}
}
