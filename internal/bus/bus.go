package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/task"
	"github.com/rceman/gpt-github-gateway/internal/taskbundle"
)

const (
	BootstrapSchemaVersion = 1
	TemplateVersion        = 1
	ProjectLayoutVersion   = 1
)

type Manager struct {
	Config *config.Config
	Layout config.Layout
	Git    string

	mu             sync.Mutex
	templateCommit string
	projects       map[string]*ProjectBus
}

type ProjectBus struct {
	manager   *Manager
	ProjectID string
	Branch    string
	Root      string
}

type RemoteTask struct {
	Envelope        task.Envelope
	Root            string
	ProtocolVersion int
	Bundle          *taskbundle.Bundle
}

type Bootstrap struct {
	SchemaVersion        int      `json:"schema_version"`
	TemplateVersion      int      `json:"template_version"`
	RepositoryRole       string   `json:"repository_role"`
	DefaultBranch        string   `json:"default_branch"`
	ControlBranchPattern string   `json:"control_branch_pattern"`
	ProjectBranchPattern string   `json:"project_branch_pattern"`
	ControlTree          []string `json:"control_tree"`
	ProjectLayoutVersion int      `json:"project_layout_version"`
}

type ProjectIdentity struct {
	SchemaVersion int    `json:"schema_version"`
	LayoutVersion int    `json:"layout_version"`
	GatewayID     string `json:"gateway_id"`
	ProjectID     string `json:"project_id"`
	Repository    string `json:"repository"`
	DefaultBranch string `json:"default_branch"`
	BusBranch     string `json:"bus_branch"`
	SessionKey    string `json:"session_key"`
	CreatedAt     string `json:"created_at"`
}

type ProjectCheckpoint struct {
	SchemaVersion         int     `json:"schema_version"`
	GatewayID             string  `json:"gateway_id"`
	ProjectID             string  `json:"project_id"`
	SourceRepository      string  `json:"source_repository"`
	SourceDefaultBranch   string  `json:"source_default_branch"`
	SourceHead            string  `json:"source_head"`
	LatestCompletedTaskID *string `json:"latest_completed_task_id"`
	LatestResultCommit    *string `json:"latest_result_commit"`
	ActiveTaskID          *string `json:"active_task_id"`
	NextAction            string  `json:"next_action"`
	UpdatedAt             string  `json:"updated_at"`
}

type AtomicTaskResult struct {
	SchemaVersion        int                   `json:"schema_version"`
	TaskID               string                `json:"task_id"`
	GatewayID            string                `json:"gateway_id"`
	ProjectID            string                `json:"project_id"`
	BundleSHA256         string                `json:"bundle_sha256"`
	State                string                `json:"state"`
	ResultBranch         string                `json:"result_branch"`
	ImplementationCommit string                `json:"implementation_commit,omitempty"`
	EvidenceCommit       string                `json:"evidence_commit,omitempty"`
	Gates                []task.AgentGate      `json:"gates,omitempty"`
	Deviations           []task.AgentDeviation `json:"deviations,omitempty"`
	Summary              string                `json:"summary,omitempty"`
	HumanResponse        string                `json:"human_response,omitempty"`
	SubmittedAt          string                `json:"submitted_at"`
	CompletedAt          time.Time             `json:"completed_at"`
}

type GatewayState struct {
	SchemaVersion  int              `json:"schema_version"`
	GatewayID      string           `json:"gateway_id"`
	GatewayVersion string           `json:"gateway_version"`
	TemplateBranch string           `json:"template_branch"`
	TemplateCommit string           `json:"template_commit"`
	Status         string           `json:"status"`
	ExecutionMode  string           `json:"execution_mode"`
	StartedAt      time.Time        `json:"started_at"`
	HeartbeatAt    time.Time        `json:"heartbeat_at"`
	LeaseExpiresAt time.Time        `json:"lease_expires_at"`
	Capabilities   []string         `json:"capabilities"`
	Projects       []GatewayProject `json:"projects"`
	Runtime        GatewayRuntime   `json:"runtime"`
	LastError      *string          `json:"last_error"`
}

type GatewayProject struct {
	ProjectID     string  `json:"project_id"`
	Repository    string  `json:"repository"`
	DefaultBranch string  `json:"default_branch"`
	Branch        string  `json:"branch"`
	SessionKey    string  `json:"session_key"`
	Status        string  `json:"status"`
	ActiveTaskID  *string `json:"active_task_id"`
}

type GatewayRuntime struct {
	PID       int    `json:"pid"`
	Readiness string `json:"readiness"`
	Doctor    string `json:"doctor"`
}

func NewManager(cfg *config.Config, layout config.Layout) *Manager {
	return &Manager{Config: cfg, Layout: layout, Git: "git", projects: map[string]*ProjectBus{}}
}

func (m *Manager) Ensure(ctx context.Context) error {
	release, err := m.acquireOperationLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureMirrorLocked(ctx); err != nil {
		return err
	}
	if err := m.fetchLocked(ctx); err != nil {
		return err
	}
	commit, err := m.validateTemplateLocked(ctx)
	if err != nil {
		return err
	}
	pinPath := filepath.Join(m.Layout.BusDir, "template.commit")
	if data, readErr := os.ReadFile(pinPath); readErr == nil {
		pinned := strings.TrimSpace(string(data))
		if pinned != commit {
			return fmt.Errorf("immutable template branch changed from %s to %s", pinned, commit)
		}
	} else if os.IsNotExist(readErr) {
		if err := writeFile(pinPath, []byte(commit+"\n")); err != nil {
			return err
		}
	} else {
		return readErr
	}
	m.templateCommit = commit
	for _, projectID := range config.ProjectIDs(m.Config) {
		if _, err := m.ensureProjectLocked(ctx, projectID); err != nil {
			return fmt.Errorf("bootstrap project %s: %w", projectID, err)
		}
	}
	return nil
}

func (m *Manager) TemplateCommit() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.templateCommit
}

func (m *Manager) Project(projectID string) (*ProjectBus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	project, ok := m.projects[projectID]
	if !ok {
		return nil, fmt.Errorf("project bus %s is not initialized", projectID)
	}
	return project, nil
}

func (m *Manager) ensureMirrorLocked(ctx context.Context) error {
	if info, err := os.Stat(m.Layout.BusMirrorDir); err == nil && info.IsDir() {
		return nil
	}
	if err := os.MkdirAll(m.Layout.BusDir, 0o700); err != nil {
		return err
	}
	if _, err := run(ctx, "", nil, m.Git, "init", "--bare", m.Layout.BusMirrorDir); err != nil {
		return err
	}
	if _, err := run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "remote", "add", "origin", m.Config.Bus.URL); err != nil {
		return err
	}
	commands := [][]string{
		{"--git-dir", m.Layout.BusMirrorDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"--git-dir", m.Layout.BusMirrorDir, "config", "user.name", "gpt-github-gateway"},
		{"--git-dir", m.Layout.BusMirrorDir, "config", "user.email", "gateway@localhost.invalid"},
	}
	for _, args := range commands {
		if _, err := run(ctx, "", nil, m.Git, args...); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) fetchLocked(ctx context.Context) error {
	_, err := run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "fetch", "--prune", "origin")
	return err
}

func (m *Manager) validateTemplateLocked(ctx context.Context) (string, error) {
	ref := "refs/remotes/origin/" + m.Config.Bus.TemplateBranch
	commit, err := m.revParseLocked(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve immutable template branch %s: %w", m.Config.Bus.TemplateBranch, err)
	}
	result, err := run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "show", ref+":bootstrap.json")
	if err != nil {
		return "", fmt.Errorf("read bootstrap.json from %s: %w", m.Config.Bus.TemplateBranch, err)
	}
	var bootstrap Bootstrap
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bootstrap); err != nil {
		return "", fmt.Errorf("decode bootstrap.json: %w", err)
	}
	if bootstrap.SchemaVersion != BootstrapSchemaVersion || bootstrap.TemplateVersion != TemplateVersion {
		return "", fmt.Errorf("unsupported bootstrap template schema=%d version=%d", bootstrap.SchemaVersion, bootstrap.TemplateVersion)
	}
	if bootstrap.RepositoryRole != "gpt-github-gateway-bus" || bootstrap.DefaultBranch != m.Config.Bus.TemplateBranch {
		return "", fmt.Errorf("bootstrap identity does not match configured template branch")
	}
	if bootstrap.ControlBranchPattern != m.Config.Bus.ControlBranchPattern || bootstrap.ProjectBranchPattern != m.Config.Bus.ProjectBranchPattern {
		return "", fmt.Errorf("bootstrap branch patterns do not match local configuration")
	}
	if len(bootstrap.ControlTree) != 1 || bootstrap.ControlTree[0] != "gateway.json" || bootstrap.ProjectLayoutVersion != ProjectLayoutVersion {
		return "", fmt.Errorf("unsupported bootstrap layout contract")
	}
	return commit, nil
}

func (m *Manager) ensureProjectLocked(ctx context.Context, projectID string) (*ProjectBus, error) {
	if project, ok := m.projects[projectID]; ok {
		return project, nil
	}
	branch, err := m.Config.ProjectBranch(projectID)
	if err != nil {
		return nil, err
	}
	remoteRef := "refs/remotes/origin/" + branch
	_, remoteErr := m.revParseLocked(ctx, remoteRef)
	branchExists := remoteErr == nil
	localRef := "refs/heads/" + branch
	if branchExists {
		remoteSHA, err := m.revParseLocked(ctx, remoteRef)
		if err != nil {
			return nil, err
		}
		if _, err := run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "update-ref", localRef, remoteSHA); err != nil {
			return nil, err
		}
	} else {
		if _, err := run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "update-ref", localRef, m.templateCommit); err != nil {
			return nil, err
		}
	}
	root := m.Layout.BusProjectDir(projectID)
	if err := m.ensureWorktreeLocked(ctx, branch, root); err != nil {
		return nil, err
	}
	project := &ProjectBus{manager: m, ProjectID: projectID, Branch: branch, Root: root}
	if !branchExists {
		if err := project.bootstrapLocked(ctx); err != nil {
			return nil, err
		}
	} else if err := project.verifyIdentityLocked(ctx); err != nil {
		return nil, err
	}
	m.projects[projectID] = project
	return project, nil
}

func (m *Manager) ensureWorktreeLocked(ctx context.Context, branch, root string) error {
	_ = os.MkdirAll(filepath.Dir(root), 0o700)
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		return nil
	}
	if _, err := os.Stat(root); err == nil {
		if err := os.RemoveAll(root); err != nil {
			return err
		}
	}
	_, _ = run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "worktree", "prune")
	_, err := run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "worktree", "add", "--force", root, branch)
	return err
}

func (m *Manager) revParseLocked(ctx context.Context, ref string) (string, error) {
	result, err := run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func (p *ProjectBus) bootstrapLocked(ctx context.Context) error {
	project := p.manager.Config.Projects[p.ProjectID]
	if _, err := run(ctx, p.Root, nil, p.manager.Git, "rm", "-rf", "--ignore-unmatch", "."); err != nil {
		return err
	}
	if err := clearWorktree(p.Root); err != nil {
		return err
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	identity := ProjectIdentity{
		SchemaVersion: 1, LayoutVersion: ProjectLayoutVersion,
		GatewayID: p.manager.Config.Gateway.ID, ProjectID: p.ProjectID,
		Repository: project.Repository, DefaultBranch: project.DefaultBranch,
		BusBranch: p.Branch, SessionKey: project.SessionKey, CreatedAt: createdAt,
	}
	sourceHead := sourceHead(project.Path)
	checkpoint := ProjectCheckpoint{
		SchemaVersion: 1, GatewayID: p.manager.Config.Gateway.ID, ProjectID: p.ProjectID,
		SourceRepository: project.Repository, SourceDefaultBranch: project.DefaultBranch,
		SourceHead: sourceHead, NextAction: "Await next GPT-authored task.", UpdatedAt: createdAt,
	}
	readme := fmt.Sprintf("# %s Gateway Project Bus\n\nDurable append-only coordination branch for `%s` on gateway `%s`.\n", p.ProjectID, project.Repository, p.manager.Config.Gateway.ID)
	contextText := fmt.Sprintf("# Project Context\n\n## Repository\n\n`%s`\n\n## Default branch\n\n`%s`\n\n## Gateway\n\n`%s`\n\n## Airelay session\n\n`%s`\n\n## Contract\n\nProduction source and normative documentation remain in the source repository. Read `state/checkpoint.json`, then execute only the supplied `AGENT_HANDOFF.md`.\n", project.Repository, project.DefaultBranch, p.manager.Config.Gateway.ID, project.SessionKey)
	files := map[string][]byte{
		"README.md": []byte(readme), "PROJECT_CONTEXT.md": []byte(contextText),
	}
	identityData, _ := json.MarshalIndent(identity, "", "  ")
	checkpointData, _ := json.MarshalIndent(checkpoint, "", "  ")
	files["project.json"] = append(identityData, '\n')
	files["state/checkpoint.json"] = append(checkpointData, '\n')
	files["inbox/.gitkeep"] = []byte{}
	files["results/.gitkeep"] = []byte{}
	files["archive/.gitkeep"] = []byte{}
	for path, data := range files {
		if err := writeFile(filepath.Join(p.Root, filepath.FromSlash(path)), data); err != nil {
			return err
		}
	}
	if _, err := run(ctx, p.Root, nil, p.manager.Git, "add", "--all"); err != nil {
		return err
	}
	if _, err := run(ctx, p.Root, nil, p.manager.Git, "commit", "-m", "gateway: initialize project "+p.ProjectID); err != nil {
		return err
	}
	if _, err := run(ctx, p.Root, nil, p.manager.Git, "push", "-u", "origin", p.Branch); err != nil {
		return err
	}
	return p.verifyIdentityLocked(ctx)
}

func (p *ProjectBus) verifyIdentityLocked(ctx context.Context) error {
	data, err := os.ReadFile(filepath.Join(p.Root, "project.json"))
	if err != nil {
		return fmt.Errorf("read project.json: %w", err)
	}
	var identity ProjectIdentity
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		return fmt.Errorf("decode project.json: %w", err)
	}
	project := p.manager.Config.Projects[p.ProjectID]
	if identity.SchemaVersion != 1 || identity.LayoutVersion != ProjectLayoutVersion || identity.GatewayID != p.manager.Config.Gateway.ID || identity.ProjectID != p.ProjectID || identity.Repository != project.Repository || identity.DefaultBranch != project.DefaultBranch || identity.BusBranch != p.Branch || identity.SessionKey != project.SessionKey {
		return fmt.Errorf("project.json identity mismatch for %s", p.ProjectID)
	}
	required := []string{"README.md", "PROJECT_CONTEXT.md", "project.json", "state/checkpoint.json", "inbox", "results", "archive"}
	for _, path := range required {
		if _, err := os.Stat(filepath.Join(p.Root, filepath.FromSlash(path))); err != nil {
			return fmt.Errorf("project branch missing %s", path)
		}
	}
	return nil
}

func (m *Manager) PublishControl(ctx context.Context, payload GatewayState) error {
	release, err := m.acquireOperationLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fetchLocked(ctx); err != nil {
		return err
	}
	mainSHA, err := m.revParseLocked(ctx, "refs/remotes/origin/"+m.Config.Bus.TemplateBranch)
	if err != nil {
		return err
	}
	if m.templateCommit != "" && mainSHA != m.templateCommit {
		return fmt.Errorf("immutable template branch changed from %s to %s", m.templateCommit, mainSHA)
	}
	branch, err := m.Config.ControlBranch()
	if err != nil {
		return err
	}
	payload.TemplateBranch = m.Config.Bus.TemplateBranch
	payload.TemplateCommit = mainSHA
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	blob, err := run(ctx, "", data, m.Git, "--git-dir", m.Layout.BusMirrorDir, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	entry := fmt.Sprintf("100644 blob %s\tgateway.json\n", strings.TrimSpace(blob.Stdout))
	tree, err := run(ctx, "", []byte(entry), m.Git, "--git-dir", m.Layout.BusMirrorDir, "mktree")
	if err != nil {
		return err
	}
	commit, err := run(ctx, "", []byte("gateway: snapshot "+m.Config.Gateway.ID+"\n"), m.Git, "--git-dir", m.Layout.BusMirrorDir, "commit-tree", strings.TrimSpace(tree.Stdout), "-p", mainSHA)
	if err != nil {
		return err
	}
	newSHA := strings.TrimSpace(commit.Stdout)
	remoteRef := "refs/remotes/origin/" + branch
	oldSHA := ""
	if value, parseErr := m.revParseLocked(ctx, remoteRef); parseErr == nil {
		oldSHA = value
	}
	lease := "--force-with-lease=refs/heads/" + branch + ":" + oldSHA
	if _, err := run(ctx, "", nil, m.Git, "--git-dir", m.Layout.BusMirrorDir, "push", lease, "origin", newSHA+":refs/heads/"+branch); err != nil {
		return fmt.Errorf("publish control snapshot with lease: %w", err)
	}
	return m.fetchLocked(ctx)
}

func (p *ProjectBus) Sync(ctx context.Context) error {
	release, err := p.manager.acquireOperationLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	p.manager.mu.Lock()
	defer p.manager.mu.Unlock()
	if err := p.manager.fetchLocked(ctx); err != nil {
		return err
	}
	remoteRef := "refs/remotes/origin/" + p.Branch
	remoteSHA, err := p.manager.revParseLocked(ctx, remoteRef)
	if err != nil {
		return err
	}
	result, err := run(ctx, p.Root, nil, p.manager.Git, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return fmt.Errorf("project bus worktree %s is dirty", p.ProjectID)
	}
	if _, err := run(ctx, p.Root, nil, p.manager.Git, "reset", "--hard", remoteSHA); err != nil {
		return err
	}
	return p.verifyIdentityLocked(ctx)
}

func (p *ProjectBus) Discover(maxFile, maxAggregate int64) ([]RemoteTask, error) {
	result := []RemoteTask{}
	bundlePaths, err := filepath.Glob(filepath.Join(p.Root, "inbox", "*.taskbundle.json"))
	if err != nil {
		return nil, err
	}
	for _, filename := range bundlePaths {
		bundle, err := taskbundle.Load(filename, maxAggregate)
		if err != nil {
			return nil, fmt.Errorf("load atomic task bundle %s: %w", filename, err)
		}
		if bundle.GatewayID != p.manager.Config.Gateway.ID || bundle.ProjectID != p.ProjectID || filepath.Base(filename) != bundle.TaskID+".taskbundle.json" {
			return nil, fmt.Errorf("atomic task bundle identity does not match project branch %s", p.Branch)
		}
		if _, err := os.Stat(filepath.Join(p.Root, "results", bundle.TaskID+".result.json")); err == nil {
			continue
		}
		result = append(result, RemoteTask{Envelope: bundle.Envelope(), Root: filename, ProtocolVersion: taskbundle.SchemaVersion, Bundle: bundle})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Envelope.SubmittedAt != result[j].Envelope.SubmittedAt {
			return result[i].Envelope.SubmittedAt < result[j].Envelope.SubmittedAt
		}
		return result[i].Envelope.TaskID < result[j].Envelope.TaskID
	})
	return result, nil
}

func (p *ProjectBus) Materialize(remote RemoteTask, localRoot string, maxFile, maxAggregate int64) error {
	if remote.ProtocolVersion != taskbundle.SchemaVersion || remote.Bundle == nil {
		return errors.New("project branches accept protocol-v2 task bundles only")
	}
	return remote.Bundle.Materialize(localRoot, maxFile, maxAggregate)
}

func (p *ProjectBus) PublishAtomicResult(ctx context.Context, remote RemoteTask, status task.Status, result *task.AgentResult, response []byte) error {
	if remote.ProtocolVersion != taskbundle.SchemaVersion || remote.Bundle == nil {
		return errors.New("remote task is not an atomic task bundle")
	}
	payload := AtomicTaskResult{SchemaVersion: 2, TaskID: remote.Envelope.TaskID, GatewayID: remote.Envelope.GatewayID, ProjectID: remote.Envelope.ProjectID, BundleSHA256: remote.Bundle.Archive.SHA256, State: status.State, ResultBranch: remote.Envelope.ResultBranch, Summary: status.Message, HumanResponse: string(response), SubmittedAt: remote.Envelope.SubmittedAt, CompletedAt: status.UpdatedAt}
	if result != nil {
		payload.ImplementationCommit = result.ImplementationCommit
		payload.EvidenceCommit = result.EvidenceCommit
		payload.Gates = result.Gates
		payload.Deviations = result.Deviations
		if result.Summary != "" {
			payload.Summary = result.Summary
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := p.publishResultAttempt(ctx, payload); err == nil {
			return nil
		} else if attempt == 2 {
			return err
		}
	}
	return errors.New("unreachable result publication state")
}

func (p *ProjectBus) publishResultAttempt(ctx context.Context, payload AtomicTaskResult) error {
	release, err := p.manager.acquireOperationLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	p.manager.mu.Lock()
	defer p.manager.mu.Unlock()
	if err := p.manager.fetchLocked(ctx); err != nil {
		return err
	}
	remoteSHA, err := p.manager.revParseLocked(ctx, "refs/remotes/origin/"+p.Branch)
	if err != nil {
		return err
	}
	if _, err := run(ctx, p.Root, nil, p.manager.Git, "reset", "--hard", remoteSHA); err != nil {
		return err
	}
	if err := p.verifyIdentityLocked(ctx); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	resultPath := filepath.Join(p.Root, "results", payload.TaskID+".result.json")
	if existing, err := os.ReadFile(resultPath); err == nil {
		if bytes.Equal(existing, append(data, '\n')) {
			return nil
		}
		return fmt.Errorf("task result identity %s already exists with different content", payload.TaskID)
	}
	if err := writeFile(resultPath, append(data, '\n')); err != nil {
		return err
	}
	checkpointPath := filepath.Join(p.Root, "state", "checkpoint.json")
	checkpointData, err := os.ReadFile(checkpointPath)
	if err != nil {
		return err
	}
	var checkpoint ProjectCheckpoint
	if err := json.Unmarshal(checkpointData, &checkpoint); err != nil {
		return err
	}
	checkpoint.LatestCompletedTaskID = &payload.TaskID
	checkpoint.ActiveTaskID = nil
	checkpoint.NextAction = "Await next GPT-authored task."
	checkpoint.UpdatedAt = payload.CompletedAt.UTC().Format(time.RFC3339Nano)
	checkpoint.SourceHead = sourceHead(p.manager.Config.Projects[p.ProjectID].Path)
	checkpoint.LatestResultCommit = nil
	updated, _ := json.MarshalIndent(checkpoint, "", "  ")
	if err := writeFile(checkpointPath, append(updated, '\n')); err != nil {
		return err
	}
	paths := []string{filepath.ToSlash(filepath.Join("results", payload.TaskID+".result.json")), "state/checkpoint.json"}
	args := append([]string{"add", "--"}, paths...)
	if _, err := run(ctx, p.Root, nil, p.manager.Git, args...); err != nil {
		return err
	}
	if _, err := run(ctx, p.Root, nil, p.manager.Git, "commit", "-m", "gateway: result "+payload.TaskID); err != nil {
		return err
	}
	if _, err := run(ctx, p.Root, nil, p.manager.Git, "push", "origin", "HEAD:refs/heads/"+p.Branch); err != nil {
		return err
	}
	return nil
}

func (m *Manager) acquireOperationLock(ctx context.Context) (func(), error) {
	lockDir := filepath.Join(m.Layout.BusDir, ".git-operation.lock")
	if err := os.MkdirAll(m.Layout.BusDir, 0o700); err != nil {
		return nil, err
	}
	for {
		err := os.Mkdir(lockDir, 0o700)
		if err == nil {
			owner := []byte(fmt.Sprintf("pid=%d\ncreated_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano)))
			_ = os.WriteFile(filepath.Join(lockDir, "owner"), owner, 0o600)
			return func() { _ = os.RemoveAll(lockDir) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(lockDir); statErr == nil && time.Since(info.ModTime()) > 5*time.Minute {
			_ = os.RemoveAll(lockDir)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("acquire shared Git operation lock: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func sourceHead(path string) string {
	result, err := run(context.Background(), path, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(result.Stdout)
}

func clearWorktree(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func run(ctx context.Context, cwd string, stdin []byte, name string, args ...string) (commandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := commandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, fmt.Errorf("%s %s exited %d: %s", name, strings.Join(args, " "), result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return result, fmt.Errorf("run %s: %w", name, err)
}

type commandResult struct {
	Stdout, Stderr string
	ExitCode       int
}

func copyFile(source, destination string, limit int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer output.Close()
	written, err := io.CopyN(output, input, limit+1)
	if err != nil && err != io.EOF {
		return err
	}
	if written > limit {
		return fmt.Errorf("file exceeds size limit")
	}
	return nil
}

func CommitIdentity(repository string) string { return strings.TrimSpace(repository) }
