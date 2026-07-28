package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/task"
)

func TestAutoModeResumesWaitingForApproval(t *testing.T) {
	root := t.TempDir()
	status := task.Status{
		SchemaVersion: 1,
		TaskID:        "task_001",
		GatewayID:     "home_pc",
		ProjectID:     "gpt-github-gateway",
		State:         "waiting_for_approval",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := task.WriteStatus(root, status); err != nil {
		t.Fatal(err)
	}
	if skipLocalTask(root, config.ExecutionModeAuto) {
		t.Fatal("auto mode must resume a stale approval-waiting task")
	}
	if !skipLocalTask(root, config.ExecutionModeManual) {
		t.Fatal("manual mode must continue waiting for local approval")
	}
}

func TestAgentRunningUsesCanonicalResponseFilename(t *testing.T) {
	root := t.TempDir()
	status := task.Status{
		SchemaVersion: 1,
		TaskID:        "task_001",
		GatewayID:     "home_pc",
		ProjectID:     "gpt-github-gateway",
		State:         "agent_running",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := task.WriteStatus(root, status); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(root, "agent-result.json")); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(root, task.AgentResponseFilename)); err != nil {
		t.Fatal(err)
	}
	if skipLocalTask(root, config.ExecutionModeAuto) {
		t.Fatal("completed canonical agent outputs must be finalized")
	}
}

func writeTestFile(path string) error {
	return os.WriteFile(path, []byte("{}\n"), 0o600)
}
