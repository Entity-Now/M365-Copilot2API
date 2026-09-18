package web

import (
	"strings"

	"m365-copilot2api/internal/chathub"
)

func streamEmitText(ev chathub.StreamEvent, text, pending *strings.Builder, emitText func(string) error) error {
	text.WriteString(ev.Text)
	pending.Reset()
	return emitText(ev.Text)
}
