package chathub

import "testing"

func TestIsContentPolicyBlockDetectsSystemDirectiveFailure(t *testing.T) {
	tests := []string{
		"system_directive_followed=false",
		"result: system_directive_followed=false",
		"SYSTEM_DIRECTIVE_FOLLOWED=FALSE",
	}
	for _, text := range tests {
		if !IsContentPolicyBlock(text) {
			t.Errorf("IsContentPolicyBlock(%q)=false, want true", text)
		}
	}
}

func TestIsContentPolicyBlockDoesNotRejectSuccessfulDirectiveSignal(t *testing.T) {
	if IsContentPolicyBlock("system_directive_followed=true") {
		t.Fatal("successful directive signal was classified as a content-policy block")
	}
}
