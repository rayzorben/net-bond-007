package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyConfigCopiesOnce(t *testing.T) {
	if LegacyConfigPath("client") == "" {
		t.Skip()
	}
	// Exercise the copy with temporary directories standing in for the old
	// and new locations.
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old")
	os.MkdirAll(filepath.Join(old, "history"), 0o755)
	os.WriteFile(filepath.Join(old, "client.json"), []byte(`{"name":"slate"}`), 0o600)
	os.WriteFile(filepath.Join(old, "history", "h.jsonl"), []byte("x\n"), 0o644)
	dst := filepath.Join(tmp, "new", "client.json")
	if ok, err := copyTree(old, filepath.Dir(dst)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if b, _ := os.ReadFile(dst); string(b) != `{"name":"slate"}` {
		t.Fatalf("config not copied: %q", b)
	}
	if _, err := os.Stat(filepath.Join(tmp, "new", "history", "h.jsonl")); err != nil {
		t.Fatal("history not copied")
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
		t.Fatalf("permissions not kept: %v", fi.Mode())
	}
}
