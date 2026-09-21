package web

import (
	"fmt"
	"m365-copilot2api/internal/chathub"
	"strings"
)

func flattenPromptMessagesBudgeted(messages []oaiMsg, attachments []chathub.Attachment, budget int) (string, []chathub.Attachment, bool, error) {
	truncatedMsgs, truncated, err := slidingWindow(messages, budget)
	if err != nil {
		return "", attachments, false, err
	}
	prompt, atts := flattenPromptMessages(truncatedMsgs, attachments)
	return prompt, atts, truncated, nil
}

// flattenAtoms delegates to context_budget flattenAtoms for atom-aware flattening.
func flattenAtomsAlias(atoms []contextAtom, attachments []chathub.Attachment) (string, []chathub.Attachment) {
	return flattenAtoms(atoms, attachments)
}

func flattenPromptMessages(messages []oaiMsg, attachments []chathub.Attachment) (string, []chathub.Attachment) {
	if len(messages) == 0 {
		return "", attachments
	}

	var instructionParts []string
	var rest []oaiMsg
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "system" || role == "developer" {
			txt, sysFiles := parseContent(m.Content)
			attachments = append(attachments, sysFiles...)
			txt = strings.TrimSpace(txt)
			if txt != "" {
				instructionParts = append(instructionParts, fmt.Sprintf("<turn role=\"%s\">\n%s\n</turn>", role, txt))
			}
		} else {
			rest = append(rest, m)
		}
	}

	// If there are no system instructions, no tool calls, and only a single simple user turn,
	// preserve it as a clean raw string without XML wrapping.
	if len(instructionParts) == 0 && len(rest) == 1 {
		m := rest[0]
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if (role == "user" || role == "") && len(m.ToolCalls) == 0 && m.ReasoningContent == "" {
			txt, files := parseContent(m.Content)
			attachments = append(attachments, files...)
			return strings.TrimSpace(txt), attachments
		}
	}

	var b strings.Builder
	if len(instructionParts) > 0 {
		b.WriteString(strings.Join(instructionParts, "\n"))
		b.WriteString("\n")
	}

	for _, m := range rest {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "" {
			role = "user"
		}
		content := m.Content
		if role == "tool" {
			switch v := content.(type) {
			case nil:
				content = ""
			case string:
			default:
				content = mustJSON(v)
			}
		}
		txt, files := parseContent(content)
		attachments = append(attachments, files...)
		txt = strings.TrimSpace(txt)

		if role == "tool" {
			idAttr := ""
			if m.ToolCallID != "" {
				idAttr = fmt.Sprintf(" tool_call_id=\"%s\"", m.ToolCallID)
			}
			b.WriteString(fmt.Sprintf("<turn role=\"tool\"%s>\n%s\n</turn>\n", idAttr, txt))
			continue
		}

		if role == "assistant" {
			var inner strings.Builder
			if m.ReasoningContent != "" {
				inner.WriteString(fmt.Sprintf("<thought>\n%s\n</thought>\n", strings.TrimSpace(m.ReasoningContent)))
			}
			if txt != "" {
				inner.WriteString(txt)
				inner.WriteString("\n")
			}
			if len(m.ToolCalls) > 0 {
				inner.WriteString(fmt.Sprintf("<tool_calls>\n%s\n</tool_calls>\n", mustJSON(m.ToolCalls)))
			}
			body := strings.TrimSpace(inner.String())
			if body != "" {
				b.WriteString(fmt.Sprintf("<turn role=\"assistant\">\n%s\n</turn>\n", body))
			}
			continue
		}

		// User or other turns
		if txt != "" {
			b.WriteString(fmt.Sprintf("<turn role=\"%s\">\n%s\n</turn>\n", role, txt))
		}
	}
	return strings.TrimSpace(b.String()), attachments
}

func extractImagePrompt(messages []oaiMsg, attachments []chathub.Attachment) (string, []chathub.Attachment) {
	var target *oaiMsg
	for i := len(messages) - 1; i >= 0; i-- {
		role := strings.ToLower(strings.TrimSpace(messages[i].Role))
		if role == "user" && messages[i].Content != nil {
			target = &messages[i]
			break
		}
	}
	if target == nil {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Content != nil {
				target = &messages[i]
				break
			}
		}
	}

	var userText string
	var msgFiles []chathub.Attachment
	if target != nil {
		userText, msgFiles = parseContent(target.Content)
	}

	merged := append(msgFiles, attachments...)
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return "", merged
	}

	prompt := fmt.Sprintf("Create an image: %s. Return the image directly.", userText)
	return prompt, merged
}
