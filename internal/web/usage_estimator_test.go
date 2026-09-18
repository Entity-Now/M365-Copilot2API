package web

import "testing"

func TestEstimateTokensUsesGPTTokenizer(t *testing.T) {
	count, source := tokenEstimator("gpt-5")
	if count == nil {
		t.Fatal("expected GPT token estimator")
	}
	if source != usageSourceTiktoken {
		t.Fatalf("expected %q source, got %q", usageSourceTiktoken, source)
	}

	for _, text := range []string{
		"hello world",
		"你好，世界",
		"function_call({\"value\":42})",
	} {
		want := int64(count(text))
		if got := EstimateTokens(text); got != want {
			t.Fatalf("EstimateTokens(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestEstimateTokensEmptyAndUnicode(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("EstimateTokens(empty) = %d, want 0", got)
	}
	if got := EstimateTokens("🙂"); got <= 0 {
		t.Fatalf("EstimateTokens(unicode) = %d, want positive count", got)
	}
}
