package web

import "testing"

func TestCallIDIsUniqueAcrossRepeatedCalls(t *testing.T) {
	const attempts = 1000
	seen := make(map[string]struct{}, attempts)
	for i := 0; i < attempts; i++ {
		id := callID("bash", `{"command":"go test ./..."}`, 0)
		if id == "" {
			t.Fatal("callID returned an empty ID")
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("callID returned duplicate ID %q", id)
		}
		seen[id] = struct{}{}
	}
}
