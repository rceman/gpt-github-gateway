package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentResultV2Contracts(t *testing.T) {
	base := AgentResult{SchemaVersion: 2, TaskID: "task_001", Status: "failed", Summary: "bounded", Details: []string{}, Gates: []AgentGate{}, Deviations: []AgentDeviation{}}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	revision := base
	revision.Status = "needs_gpt_revision"
	if err := revision.Validate(); err == nil {
		t.Fatal("expected next_action requirement")
	}
	success := base
	success.Status = "succeeded"
	success.ImplementationCommit = strings.Repeat("1", 40)
	success.EvidenceCommit = strings.Repeat("2", 40)
	success.Gates = []AgentGate{{ID: "one", Status: "pass", Exit: 0, Summary: "ok"}}
	manifest := Manifest{Gates: []Gate{{ID: "one"}}}
	if err := success.ValidateAgainst(manifest); err != nil {
		t.Fatal(err)
	}
	success.Gates[0].ID = "other"
	if err := success.ValidateAgainst(manifest); err == nil {
		t.Fatal("expected ordered gate mismatch")
	}
}

func TestStrictJSONRejectsDuplicateAndTrailingData(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentResult(path); err == nil {
		t.Fatal("expected duplicate-key rejection")
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":2} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentResult(path); err == nil {
		t.Fatal("expected trailing-data rejection")
	}
}
