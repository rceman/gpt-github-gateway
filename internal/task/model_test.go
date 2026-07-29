package task

import "testing"

func TestEnvelopeRejectsWrongGatewayPathInput(t *testing.T) {
	envelope := Envelope{
		SchemaVersion:    1,
		TaskID:           "task_001",
		GatewayID:        "home_pc",
		ProjectID:        "gateway",
		Operation:        "apply_patch_pack",
		SubmittedAt:      "2026-07-27T18:00:00Z",
		RequestPath:      "../request.md",
		PatchPackPath:    "patch-pack",
		ResultBranch:     "agent/task_001",
		ApprovalRequired: true,
	}
	if err := envelope.Validate(); err == nil {
		t.Fatal("expected path traversal to fail")
	}
}

func TestManifestRequiresPinnedPlanner(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: 2,
		Workflow: WorkflowPin{
			Repository: WorkflowRepository,
			Version:    "main",
			Commit:     WorkflowCommit,
			Document:   WorkflowDocument,
		},
		Target: Target{
			Repository:   "rceman/example",
			Branch:       "main",
			BaseRevision: "0123456789abcdef0123456789abcdef01234567",
		},
		EvidenceDirectory: ".gpt-review/evidence/v0.0.0/patch-20260727-180000-test",
		Requirements:      []Requirement{{ID: "REQ-001", Acceptance: []string{"AC-001"}}},
		Gates:             []Gate{{ID: "test", Kind: "command", Command: "go test ./..."}},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected mutable workflow version to fail")
	}
}

func TestPinnedPlannerIdentity(t *testing.T) {
	if WorkflowVersion != "v1.3.0" {
		t.Fatalf("unexpected workflow version %q", WorkflowVersion)
	}
	if WorkflowCommit != "b1a45b1e9475ab29dfd3e84d523b70897c7b8918" {
		t.Fatalf("unexpected workflow commit %q", WorkflowCommit)
	}
}

func TestAgentResultRequiresEveryGate(t *testing.T) {
	manifest := Manifest{Gates: []Gate{{ID: "test"}, {ID: "build"}}}
	result := AgentResult{
		Status: "succeeded",
		Gates:  []AgentGate{{ID: "test", Status: "pass", Exit: 0}},
	}
	if err := result.ValidateAgainst(manifest); err == nil {
		t.Fatal("expected missing gate to fail")
	}
}

func TestCanonicalFixturesLoad(t *testing.T) {
	envelope, err := LoadEnvelope("testdata/valid-task.json")
	if err != nil {
		t.Fatalf("load task fixture: %v", err)
	}
	if envelope.GatewayID != "home_pc" {
		t.Fatalf("unexpected gateway %q", envelope.GatewayID)
	}
	manifest, err := LoadManifest("testdata/valid-manifest.json")
	if err != nil {
		t.Fatalf("load manifest fixture: %v", err)
	}
	if manifest.Workflow.Commit != WorkflowCommit {
		t.Fatalf("unexpected workflow commit %q", manifest.Workflow.Commit)
	}
}
