package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompleteTaskScriptUsesAuthoritativeArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), CompleteTaskFilename)
	if err := WriteCompleteTaskScript(path, "/opt/gateway binary", "/tmp/config path.json", "gpt-review-planner", "task_001"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"task complete", "gpt-review-planner", "task_001", "config path.json"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}
func TestCompletionBindsResultDigest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(ResultPath(root), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteCompletion(root, "task_001"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCompletion(root, "task_001"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ResultPath(root), []byte("{ }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCompletion(root, "task_001"); err == nil {
		t.Fatal("expected digest mismatch")
	}
}
