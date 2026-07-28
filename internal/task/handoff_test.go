package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validHandoff(manifest Manifest) string {
	return strings.Join([]string{
		"# AGENT_HANDOFF",
		"",
		"## TASK_IDENTITY",
		manifest.PatchID + " " + manifest.Target.Repository + " " + manifest.Target.Branch + " " + manifest.Target.BaseRevision,
		"## AUTHORITY",
		manifest.Workflow.Repository + " " + manifest.Workflow.Version + " " + manifest.Workflow.Commit + " " + manifest.Workflow.Document,
		"## AGENT_ROLE",
		"Run gates and perform narrow integration repair only.",
		"## PROHIBITED_ACTIONS",
		"No redesign.",
		"## PATCH_APPLICATION",
		"Apply the supplied patch pack.",
		"## REQUIRED_RUNTIME_GATES",
		"Run every manifest gate exactly.",
		"## REPAIR_POLICY",
		"Only direct integration defects.",
		"## EVIDENCE_AND_COMMITS",
		"Preserve the implementation and evidence commits.",
		"## RESPONSE_CONTRACT",
		"Write the gateway-requested response files.",
		"",
	}, "\n")
}

func handoffManifest() Manifest {
	return Manifest{
		PatchID:  "patch-20260728-120000-runtime-handoff",
		Workflow: WorkflowPin{Repository: WorkflowRepository, Version: WorkflowVersion, Commit: WorkflowCommit, Document: WorkflowDocument},
		Target: Target{
			Repository:   "rceman/gpt-github-gateway",
			Branch:       "main",
			BaseRevision: "a3e33bc01cbeca892eb459f1ddc762c7c207906c",
		},
	}
}

func TestLoadAgentHandoffAcceptsCanonicalFile(t *testing.T) {
	root := t.TempDir()
	manifest := handoffManifest()
	if err := os.WriteFile(filepath.Join(root, AgentHandoffFilename), []byte(validHandoff(manifest)), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := LoadAgentHandoff(root, manifest)
	if err != nil {
		t.Fatalf("load handoff: %v", err)
	}
	if !strings.Contains(string(data), manifest.PatchID) {
		t.Fatal("handoff lost patch identity")
	}
}

func TestLoadAgentHandoffRejectsMissingHeading(t *testing.T) {
	root := t.TempDir()
	manifest := handoffManifest()
	text := strings.Replace(validHandoff(manifest), "## REPAIR_POLICY\n", "", 1)
	if err := os.WriteFile(filepath.Join(root, AgentHandoffFilename), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentHandoff(root, manifest); err == nil {
		t.Fatal("expected missing heading to fail")
	}
}

func TestLoadAgentHandoffRejectsPlaceholder(t *testing.T) {
	root := t.TempDir()
	manifest := handoffManifest()
	text := validHandoff(manifest) + "<TASK_ID>\n"
	if err := os.WriteFile(filepath.Join(root, AgentHandoffFilename), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentHandoff(root, manifest); err == nil {
		t.Fatal("expected unresolved placeholder to fail")
	}
}

func TestWriteRuntimeHandoffAddsAuthoritativePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), AgentHandoffFilename)
	manifest := handoffManifest()
	context := RuntimeHandoffContext{
		TaskID:       "task_001",
		ProjectID:    "gpt-github-gateway",
		Worktree:     "/tmp/worktree",
		PackRoot:     "/tmp/task/patch-pack",
		OwnerRequest: "/tmp/task/request.md",
		ResponsePath: "/tmp/task/AGENT_RESPONSE.md",
		ResultPath:   "/tmp/task/agent-result.json",
		ResultBranch: "agent/task_001",
		BaseRevision: manifest.Target.BaseRevision,
		EvidenceDir:  ".gpt-review/evidence/v0.1.0/example",
		PatchApplied: true,
	}
	if err := WriteRuntimeHandoff(path, []byte(validHandoff(manifest)), context); err != nil {
		t.Fatalf("write runtime handoff: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{context.Worktree, context.ResponsePath, "## GATEWAY_RUNTIME_CONTEXT"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("runtime handoff missing %q", expected)
		}
	}
}
