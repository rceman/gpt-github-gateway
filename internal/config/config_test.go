package config

import (
	"path/filepath"
	"testing"
)

func TestApplyDefaultsUsesProjectMasterSession(t *testing.T) {
	cfg := Config{
		SchemaVersion: 1,
		Gateway:       GatewayConfig{ID: "home_pc"},
		Bus: BusConfig{
			Repository: "rceman/typer",
			URL:        "git@github.com:rceman/typer.git",
			Branch:     "ai-workspace-bus",
		},
		Projects: map[string]ProjectConfig{
			"gpt-github-gateway": {
				Path:       filepath.Join(string(filepath.Separator), "tmp", "gateway"),
				Repository: "rceman/gpt-github-gateway",
			},
		},
	}
	cfg.ApplyDefaults()
	project := cfg.Projects["gpt-github-gateway"]
	if project.SessionKey != "gpt-github-gateway_master" {
		t.Fatalf("unexpected session key %q", project.SessionKey)
	}
	if project.AirelayProfile != "codex" {
		t.Fatalf("unexpected profile %q", project.AirelayProfile)
	}
}

func TestServerMustBindLoopback(t *testing.T) {
	cfg := Config{
		SchemaVersion: 1,
		Gateway: GatewayConfig{
			ID:                    "home_pc",
			PollIntervalSeconds:   10,
			AgentTimeoutSeconds:   60,
			MaxTaskFileBytes:      1024,
			MaxTaskAggregateBytes: 2048,
		},
		Bus: BusConfig{
			Repository: "rceman/typer",
			URL:        "git@github.com:rceman/typer.git",
			Branch:     "ai-workspace-bus",
		},
		Server:   ServerConfig{Listen: "0.0.0.0:8787"},
		Airelay:  AirelayConfig{Binary: "airelay"},
		Projects: map[string]ProjectConfig{},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-loopback server address to fail")
	}
}
