package web

import (
	"strings"
	"testing"
)

func TestFlattenPromptMessagesPreservesInstructionRolesAndOrder(t *testing.T) {
	messages := []oaiMsg{
		{Role: "system", Content: "system directive"},
		{Role: "developer", Content: "developer directive"},
		{Role: "user", Content: "request"},
	}

	prompt, _ := flattenPromptMessages(messages, nil)
	want := "[system]\nsystem directive\n[developer]\ndeveloper directive\n\n[user]\nrequest"
	if prompt != want {
		t.Fatalf("prompt=%q want=%q", prompt, want)
	}
	if strings.Count(prompt, "system directive") != 1 || strings.Count(prompt, "developer directive") != 1 {
		t.Fatalf("instructions were lost or duplicated: %q", prompt)
	}
}
