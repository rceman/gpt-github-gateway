package app

import (
	"testing"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/task"
)

func TestAutoModeResumesWaitingForApproval(t *testing.T) {
	root := t.TempDir()
	s := task.Status{SchemaVersion: 1, TaskID: "task_001", GatewayID: "home_pc", ProjectID: "gpt-github-gateway", State: "waiting_for_approval", UpdatedAt: time.Now().UTC()}
	if err := task.WriteStatus(root, s); err != nil {
		t.Fatal(err)
	}
	if skipLocalTask(root, config.ExecutionModeAuto) {
		t.Fatal("auto mode must resume")
	}
	if !skipLocalTask(root, config.ExecutionModeManual) {
		t.Fatal("manual mode must wait")
	}
}
func TestAgentRunningIsResumedAfterRestart(t *testing.T) {
	root := t.TempDir()
	s := task.Status{SchemaVersion: 1, TaskID: "task_001", GatewayID: "home_pc", ProjectID: "gpt-github-gateway", State: "agent_running", UpdatedAt: time.Now().UTC()}
	if err := task.WriteStatus(root, s); err != nil {
		t.Fatal(err)
	}
	if skipLocalTask(root, config.ExecutionModeAuto) {
		t.Fatal("agent_running must re-enter runner for completion recovery")
	}
}
