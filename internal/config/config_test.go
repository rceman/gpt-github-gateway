package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validTestConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Gateway:       GatewayConfig{ID: "home_pc", PollIntervalSeconds: 10, AgentTimeoutSeconds: 60, TaskExecutionMode: ExecutionModeAuto, MaxTaskFileBytes: 1024, MaxTaskAggregateBytes: 2048},
		Bus:           BusConfig{Repository: "rceman/typer", URL: "git@github.com:rceman/typer.git", TemplateBranch: "main", ControlBranchPattern: DefaultControlPattern, ProjectBranchPattern: DefaultProjectPattern, HeartbeatIntervalSeconds: 600, LeaseDurationSeconds: 1500},
		Server:        ServerConfig{Listen: DefaultListen}, Airelay: AirelayConfig{Binary: "airelay"}, Projects: map[string]ProjectConfig{},
	}
}

func TestApplyDefaultsUsesProjectMasterSession(t *testing.T) {
	cfg := validTestConfig()
	cfg.Projects["gpt-github-gateway"] = ProjectConfig{Path: filepath.Join(string(filepath.Separator), "tmp", "gateway"), Repository: "rceman/gpt-github-gateway"}
	cfg.ApplyDefaults()
	project := cfg.Projects["gpt-github-gateway"]
	if project.SessionKey != "gpt-github-gateway_master" {
		t.Fatalf("unexpected session key %q", project.SessionKey)
	}
	if project.AirelayProfile != "codex" {
		t.Fatalf("unexpected profile %q", project.AirelayProfile)
	}
	if cfg.Gateway.TaskExecutionMode != ExecutionModeAuto {
		t.Fatalf("unexpected execution mode %q", cfg.Gateway.TaskExecutionMode)
	}
}

func TestBranchPatternExpansion(t *testing.T) {
	control, err := ExpandBranchPattern("gateway/{gateway_id}", "home_pc", "")
	if err != nil || control != "gateway/home_pc" {
		t.Fatalf("control=%q err=%v", control, err)
	}
	project, err := ExpandBranchPattern("project/{gateway_id}/{project_id}", "home_pc", "airelay")
	if err != nil || project != "project/home_pc/airelay" {
		t.Fatalf("project=%q err=%v", project, err)
	}
}

func TestBranchPatternExpansionAllowsHyphenatedProjectIDs(t *testing.T) {
	branch, err := ExpandBranchPattern("project/{gateway_id}/{project_id}", "home_pc", "gpt-github-gateway")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "project/home_pc/gpt-github-gateway" {
		t.Fatalf("unexpected branch %q", branch)
	}
}

func TestRejectsInvalidBranchPatterns(t *testing.T) {
	for _, pattern := range []string{"project/{unknown}", "project/{gateway_id}/../x", "project/{gateway_id}\\x", "project/{gateway_id}/{project_id}"} {
		projectID := ""
		if strings.Contains(pattern, "{project_id}") {
			projectID = ""
		}
		if _, err := ExpandBranchPattern(pattern, "home_pc", projectID); err == nil {
			t.Fatalf("expected %q to fail", pattern)
		}
	}
}

func TestRejectsUnknownTaskExecutionMode(t *testing.T) {
	cfg := validTestConfig()
	cfg.Gateway.TaskExecutionMode = "remote"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid task execution mode to fail")
	}
}

func TestServerMustBindLoopback(t *testing.T) {
	cfg := validTestConfig()
	cfg.Server.Listen = "0.0.0.0:8787"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-loopback server address to fail")
	}
}

func TestLegacyConfigGetsMigrationGuidance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"schema_version":1,"gateway":{"id":"home_pc"},"bus":{"repository":"rceman/typer","url":"x","branch":"ai-workspace-bus"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "migrate-bus-multibranch.py") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDuplicateConfigKeyFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("unexpected error: %v", err)
	}
}
