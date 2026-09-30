package web

import (
	"strings"

	"m365-copilot2api/internal/chathub"
)

type toolStreamState struct {
	hasTools      bool
	toolNames     map[string]bool
	inToolBlock   bool
	emittedOffset int
}

func newToolStreamState(tools []map[string]any) *toolStreamState {
	if len(tools) == 0 {
		return &toolStreamState{hasTools: false}
	}
	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		if fn, ok := t["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok && n != "" {
				names[strings.ToLower(n)] = true
			}
		}
	}
	for _, s := range []string{"bash", "sh", "powershell", "pwsh", "cmd", "shell", "workspace_shell"} {
		names[s] = true
	}
	return &toolStreamState{
		hasTools:    true,
		toolNames:   names,
		inToolBlock: false,
	}
}

// processStreamChunk processes an incoming stream event. If tools are configured,
// it suppresses tool call payloads (XML <tool_calls>, markdown fenced code blocks for tools)
// from being emitted as raw text content deltas.
func (s *toolStreamState) processStreamChunk(ev chathub.StreamEvent, text *strings.Builder, emitText func(string) error) error {
	text.WriteString(ev.Text)
	if !s.hasTools {
		return emitText(ev.Text)
	}

	full := text.String()
	if s.emittedOffset >= len(full) {
		return nil
	}
	unprocessed := full[s.emittedOffset:]

	lower := strings.ToLower(unprocessed)
	toolBlockIdx := -1
	for _, marker := range []string{"<tool_calls>", "<tool_call>", "<tool_call ", "call_tool:", "```"} {
		if idx := strings.Index(lower, marker); idx >= 0 {
			if marker == "```" {
				after := strings.TrimSpace(unprocessed[idx+3:])
				endLine := strings.Index(after, "\n")
				lang := after
				if endLine >= 0 {
					lang = strings.TrimSpace(after[:endLine])
				}
				lang = strings.ToLower(lang)
				if s.toolNames[lang] {
					if toolBlockIdx < 0 || idx < toolBlockIdx {
						toolBlockIdx = idx
					}
				}
			} else {
				if toolBlockIdx < 0 || idx < toolBlockIdx {
					toolBlockIdx = idx
				}
			}
		}
	}

	if toolBlockIdx >= 0 {
		preText := unprocessed[:toolBlockIdx]
		if len(preText) > 0 {
			s.emittedOffset += len(preText)
			if err := emitText(preText); err != nil {
				return err
			}
		}
		s.inToolBlock = true
		return nil
	}

	if s.inToolBlock {
		return nil
	}

	safeEnd := len(unprocessed)
	for _, suffix := range []string{"<tool_calls", "<tool_call", "<tool", "<", "```", "``", "`", "call_tool", "call_"} {
		if strings.HasSuffix(strings.ToLower(unprocessed), suffix) {
			safeEnd = len(unprocessed) - len(suffix)
			break
		}
	}

	if safeEnd > 0 {
		toEmit := unprocessed[:safeEnd]
		s.emittedOffset += len(toEmit)
		return emitText(toEmit)
	}

	return nil
}

// flushRemaining flushes any remaining text that was held back if no tool calls were detected.
func (s *toolStreamState) flushRemaining(text *strings.Builder, emitText func(string) error) error {
	if !s.hasTools {
		return nil
	}
	full := text.String()
	if s.emittedOffset < len(full) {
		remaining := full[s.emittedOffset:]
		s.emittedOffset = len(full)
		return emitText(remaining)
	}
	return nil
}

func streamEmitText(ev chathub.StreamEvent, text, pending *strings.Builder, emitText func(string) error) error {
	text.WriteString(ev.Text)
	pending.Reset()
	return emitText(ev.Text)
}
