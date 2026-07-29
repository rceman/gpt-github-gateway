package bus

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/config"
)

func TestMultiBranchBootstrapAndRollingControl(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	initTemplateRemote(t, remote)
	source := initSourceRepo(t, filepath.Join(root, "source"))
	cfg := testConfig(remote, source, "home_pc")
	layout := testLayout(root, "home_pc")
	manager := NewManager(cfg, layout)
	if err := manager.Ensure(ctx); err != nil {
		t.Fatal(err)
	}

	projectBranch, _ := cfg.ProjectBranch("demo")
	projectTree := gitOutput(t, "--git-dir", remote, "ls-tree", "-r", "--name-only", "refs/heads/"+projectBranch)
	for _, expected := range []string{"README.md", "PROJECT_CONTEXT.md", "project.json", "state/checkpoint.json", "inbox/.gitkeep", "results/.gitkeep", "archive/.gitkeep"} {
		if !containsLine(projectTree, expected) {
			t.Fatalf("project tree missing %s:\n%s", expected, projectTree)
		}
	}

	state := GatewayState{SchemaVersion: 1, GatewayID: "home_pc", GatewayVersion: "0.3.0", Status: "online", ExecutionMode: "auto", StartedAt: time.Now().UTC(), HeartbeatAt: time.Now().UTC(), LeaseExpiresAt: time.Now().UTC().Add(25 * time.Minute)}
	if err := manager.PublishControl(ctx, state); err != nil {
		t.Fatal(err)
	}
	first := gitOutput(t, "--git-dir", remote, "rev-parse", "refs/heads/gateway/home_pc")
	state.HeartbeatAt = state.HeartbeatAt.Add(10 * time.Minute)
	state.LeaseExpiresAt = state.LeaseExpiresAt.Add(10 * time.Minute)
	if err := manager.PublishControl(ctx, state); err != nil {
		t.Fatal(err)
	}
	second := gitOutput(t, "--git-dir", remote, "rev-parse", "refs/heads/gateway/home_pc")
	if first == second {
		t.Fatal("control snapshot did not advance")
	}
	if count := strings.TrimSpace(gitOutput(t, "--git-dir", remote, "rev-list", "--count", "refs/heads/gateway/home_pc")); count != "2" {
		t.Fatalf("control history must remain two commits, got %s", count)
	}
	if tree := strings.TrimSpace(gitOutput(t, "--git-dir", remote, "ls-tree", "-r", "--name-only", "refs/heads/gateway/home_pc")); tree != "gateway.json" {
		t.Fatalf("unexpected control tree: %q", tree)
	}
	parent := strings.Fields(gitOutput(t, "--git-dir", remote, "rev-list", "--parents", "-n", "1", "refs/heads/gateway/home_pc"))
	mainSHA := strings.TrimSpace(gitOutput(t, "--git-dir", remote, "rev-parse", "refs/heads/main"))
	if len(parent) != 2 || parent[1] != mainSHA {
		t.Fatalf("control snapshot parent mismatch: %v main=%s", parent, mainSHA)
	}
}

func TestTemplateCommitPinRejectsChangedMain(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	initTemplateRemote(t, remote)
	source := initSourceRepo(t, filepath.Join(root, "source"))
	cfg := testConfig(remote, source, "home_pc")
	layout := testLayout(root, "home_pc")
	manager := NewManager(cfg, layout)
	if err := manager.Ensure(ctx); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(root, "mutate-main")
	gitRun(t, "clone", remote, work)
	gitRun(t, "-C", work, "config", "user.name", "test")
	gitRun(t, "-C", work, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", work, "add", "README.md")
	gitRun(t, "-C", work, "commit", "-m", "change template")
	gitRun(t, "-C", work, "push", "origin", "main")

	fresh := NewManager(cfg, layout)
	if err := fresh.Ensure(ctx); err == nil || !strings.Contains(err.Error(), "immutable template branch changed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIndependentGatewayBranches(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	initTemplateRemote(t, remote)
	for _, gatewayID := range []string{"home_pc", "work_pc"} {
		source := initSourceRepo(t, filepath.Join(root, gatewayID+"-source"))
		cfg := testConfig(remote, source, gatewayID)
		manager := NewManager(cfg, testLayout(root, gatewayID))
		if err := manager.Ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := manager.PublishControl(context.Background(), GatewayState{SchemaVersion: 1, GatewayID: gatewayID, GatewayVersion: "0.3.0", Status: "online", ExecutionMode: "auto", StartedAt: time.Now(), HeartbeatAt: time.Now(), LeaseExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range []string{"refs/heads/gateway/home_pc", "refs/heads/gateway/work_pc", "refs/heads/project/home_pc/demo", "refs/heads/project/work_pc/demo"} {
		_ = gitOutput(t, "--git-dir", remote, "rev-parse", "--verify", ref)
	}
}

func testConfig(remote, source, gatewayID string) *config.Config {
	cfg := &config.Config{SchemaVersion: 2, Gateway: config.GatewayConfig{ID: gatewayID, PollIntervalSeconds: 10, AgentTimeoutSeconds: 60, TaskExecutionMode: "auto", MaxTaskFileBytes: 1024, MaxTaskAggregateBytes: 1 << 20}, Bus: config.BusConfig{Repository: "rceman/typer", URL: remote, TemplateBranch: "main", ControlBranchPattern: "gateway/{gateway_id}", ProjectBranchPattern: "project/{gateway_id}/{project_id}", HeartbeatIntervalSeconds: 600, LeaseDurationSeconds: 1500}, Server: config.ServerConfig{Listen: config.DefaultListen}, Airelay: config.AirelayConfig{Binary: "airelay"}, Projects: map[string]config.ProjectConfig{"demo": {Path: source, Repository: "rceman/demo", DefaultBranch: "main", AirelayProfile: "codex", SessionKey: "demo_master"}}}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func testLayout(root, gatewayID string) config.Layout {
	busRoot := filepath.Join(root, gatewayID, "bus")
	return config.Layout{Root: filepath.Join(root, gatewayID), BusDir: busRoot, BusMirrorDir: filepath.Join(busRoot, "mirror.git"), BusControlDir: filepath.Join(busRoot, "control"), BusProjectsDir: filepath.Join(busRoot, "projects"), GatewayID: gatewayID}
}

func initTemplateRemote(t *testing.T, remote string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "template")
	gitRun(t, "init", "-b", "main", work)
	gitRun(t, "-C", work, "config", "user.name", "test")
	gitRun(t, "-C", work, "config", "user.email", "test@example.invalid")
	bootstrap := Bootstrap{SchemaVersion: 1, TemplateVersion: 1, RepositoryRole: "gpt-github-gateway-bus", DefaultBranch: "main", ControlBranchPattern: "gateway/{gateway_id}", ProjectBranchPattern: "project/{gateway_id}/{project_id}", ControlTree: []string{"gateway.json"}, ProjectLayoutVersion: 1}
	data, _ := json.MarshalIndent(bootstrap, "", "  ")
	files := map[string][]byte{"README.md": []byte("# Bus\n"), ".gitignore": []byte("*.tmp\n"), ".gitattributes": []byte("* text=auto eol=lf\n"), "bootstrap.json": append(data, '\n')}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(work, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, "-C", work, "add", "--all")
	gitRun(t, "-C", work, "commit", "-m", "bootstrap")
	gitRun(t, "clone", "--bare", work, remote)
}

func initSourceRepo(t *testing.T, path string) string {
	t.Helper()
	gitRun(t, "init", "-b", "main", path)
	gitRun(t, "-C", path, "config", "user.name", "test")
	gitRun(t, "-C", path, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", path, "add", "README.md")
	gitRun(t, "-C", path, "commit", "-m", "initial")
	gitRun(t, "-C", path, "remote", "add", "origin", "git@github.com:rceman/demo.git")
	return path
}

func gitRun(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
func gitOutput(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
func containsLine(text, expected string) bool {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == expected {
			return true
		}
	}
	return false
}
