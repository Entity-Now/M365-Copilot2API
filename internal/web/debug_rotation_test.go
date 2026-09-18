package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDebugStoreRotatesAndBoundsBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.jsonl")
	if err := os.WriteFile(path, make([]byte, maxDebugLogBytes), 0600); err != nil {
		t.Fatal(err)
	}
	store := &debugStore{path: path}
	store.rotateLocked()
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("first backup missing: %v", err)
	}
	for i := 0; i < maxDebugLogBackups+1; i++ {
		if err := os.WriteFile(path, make([]byte, maxDebugLogBytes), 0600); err != nil {
			t.Fatal(err)
		}
		store.rotateLocked()
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatalf("unexpected backup beyond retention limit: %v", err)
	}
}
