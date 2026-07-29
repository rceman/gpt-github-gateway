package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeHandoffDefinesJSONOnlyFinalizer(t *testing.T) {
	m := Manifest{PatchID: "patch-001", Workflow: WorkflowPin{Repository: WorkflowRepository, Version: WorkflowVersion, Commit: WorkflowCommit, Document: WorkflowDocument}, Target: Target{Repository: "rceman/example", Branch: "main", BaseRevision: strings.Repeat("1", 40)}}
	source := `# AGENT_HANDOFF
## TASK_IDENTITY
patch-001 rceman/example main ` + strings.Repeat("1", 40) + ` ` + WorkflowRepository + ` ` + WorkflowVersion + ` ` + WorkflowCommit + ` ` + WorkflowDocument + `
## AUTHORITY
x
## AGENT_ROLE
x
## PROHIBITED_ACTIONS
x
## PATCH_APPLICATION
x
## REQUIRED_RUNTIME_GATES
x
## REPAIR_POLICY
x
## EVIDENCE_AND_COMMITS
x
## TERMINAL_OUTPUT_PROTOCOL
x
## RESPONSE_CONTRACT
x
`
	path := filepath.Join(t.TempDir(), AgentHandoffFilename)
	ctx := RuntimeHandoffContext{TaskID: "task_001", ProjectID: "example", Worktree: "/tmp/worktree", PackRoot: "/tmp/pack", OwnerRequest: "/tmp/request", ResultPath: "/tmp/agent-result.json", CompleteTaskPath: "/tmp/complete-task", ResultBranch: "agent/task-001", BaseRevision: m.Target.BaseRevision, EvidenceDir: ".gpt-review/evidence/x", PatchApplied: true}
	if err := WriteRuntimeHandoff(path, []byte(source), ctx); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, want := range []string{"Strict JSON result", "Mandatory finalizer command", "Interactive session text does not complete"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(text, "Human response") {
		t.Fatal("legacy human response must not be generated")
	}
}
