package task

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/airelay"
	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/execx"
	"github.com/rceman/gpt-github-gateway/internal/gitx"
)

type Runner struct {
	Config  *config.Config
	Layout  config.Layout
	Git     gitx.Git
	Airelay airelay.Client
}
type RunOutcome struct {
	Envelope Envelope
	Manifest Manifest
	Status   Status
	Result   *AgentResult
	Response []byte
}

func NewRunner(cfg *config.Config, layout config.Layout) *Runner {
	return &Runner{Config: cfg, Layout: layout, Git: gitx.New(), Airelay: airelay.New(cfg.Airelay.Binary)}
}

func (r *Runner) Run(ctx context.Context, projectID, taskID string) (RunOutcome, error) {
	taskRoot := r.Layout.TaskRoot(projectID, taskID)
	envelope, manifest, project, packRoot, err := r.loadTask(ctx, projectID, taskID)
	if err != nil {
		return RunOutcome{}, err
	}
	switch r.Config.Gateway.TaskExecutionMode {
	case config.ExecutionModeDisabled:
		s := r.status(envelope, "execution_disabled", "local task execution is disabled", "")
		_ = WriteStatus(taskRoot, s)
		return RunOutcome{Envelope: envelope, Manifest: manifest, Status: s}, nil
	case config.ExecutionModeManual:
		approval, err := ReadApproval(taskRoot, taskID)
		if err != nil {
			return RunOutcome{}, err
		}
		if approval.Decision == "rejected" {
			s := r.status(envelope, "rejected", approval.Reason, "")
			_ = WriteStatus(taskRoot, s)
			return RunOutcome{Envelope: envelope, Manifest: manifest, Status: s}, nil
		}
		if approval.Decision != "approved" {
			s := r.status(envelope, "waiting_for_approval", "local execution mode requires owner approval", "")
			_ = WriteStatus(taskRoot, s)
			return RunOutcome{Envelope: envelope, Manifest: manifest, Status: s}, nil
		}
	case config.ExecutionModeAuto:
	default:
		return RunOutcome{}, fmt.Errorf("unsupported task execution mode %q", r.Config.Gateway.TaskExecutionMode)
	}
	worktree := r.Layout.WorktreeRoot(projectID, taskID)
	resultPath := ResultPath(taskRoot)
	completionPath := CompletionPath(taskRoot)
	if regularFile(resultPath) && regularFile(completionPath) {
		return r.finalizeAgentResult(ctx, envelope, manifest, taskRoot, worktree, resultPath)
	}
	previous, _ := ReadStatus(taskRoot)
	resuming := previous.State == "agent_running"
	if !resuming {
		status := r.status(envelope, "preparing_worktree", "", worktree)
		if err := WriteStatus(taskRoot, status); err != nil {
			return RunOutcome{}, err
		}
		if err := r.Git.BackupRef(ctx, project.Path, taskID, manifest.Target.BaseRevision); err != nil {
			return RunOutcome{}, fmt.Errorf("create backup ref: %w", err)
		}
	}
	if _, err := os.Stat(worktree); os.IsNotExist(err) {
		if err := r.Git.CreateWorktree(ctx, project.Path, worktree, envelope.ResultBranch, manifest.Target.BaseRevision); err != nil {
			return RunOutcome{}, fmt.Errorf("create task worktree: %w", err)
		}
	}
	if !resuming {
		payloadReady, runErr := r.applyPayload(ctx, envelope, manifest, packRoot, worktree)
		if runErr != nil {
			return RunOutcome{}, runErr
		}
		requestSource, err := ResolveInside(taskRoot, envelope.RequestPath)
		if err != nil {
			return RunOutcome{}, err
		}
		handoffPath := filepath.Join(taskRoot, AgentHandoffFilename)
		completePath := CompleteTaskPath(taskRoot)
		binary, err := os.Executable()
		if err != nil {
			return RunOutcome{}, err
		}
		if err := WriteCompleteTaskScript(completePath, binary, r.Layout.ConfigPath, projectID, taskID); err != nil {
			return RunOutcome{}, err
		}
		_ = os.Remove(resultPath)
		_ = os.Remove(completionPath)
		handoffSource, err := LoadAgentHandoff(packRoot, manifest)
		if err != nil {
			return RunOutcome{}, err
		}
		if err := WriteRuntimeHandoff(handoffPath, handoffSource, RuntimeHandoffContext{TaskID: taskID, ProjectID: projectID, Worktree: worktree, PackRoot: packRoot, OwnerRequest: requestSource, ResultPath: resultPath, CompleteTaskPath: completePath, PatchApplied: payloadReady, PatchRepair: !payloadReady, ResultBranch: envelope.ResultBranch, BaseRevision: manifest.Target.BaseRevision, EvidenceDir: manifest.EvidenceDirectory}); err != nil {
			return RunOutcome{}, err
		}
		s := r.status(envelope, "waiting_for_agent", "", worktree)
		_ = WriteStatus(taskRoot, s)
		if err := r.Airelay.EnsureSession(ctx, project, worktree, airelay.AgentLogPath(taskRoot)); err != nil {
			s = r.status(envelope, "agent_unavailable", err.Error(), worktree)
			_ = WriteStatus(taskRoot, s)
			return RunOutcome{Envelope: envelope, Manifest: manifest, Status: s}, nil
		}
		if err := r.Airelay.Prompt(ctx, project.SessionKey, "Read "+handoffPath+" and execute it exactly."); err != nil {
			return RunOutcome{}, err
		}
		s = r.status(envelope, "agent_running", "", worktree)
		_ = WriteStatus(taskRoot, s)
	} else {
		if err := r.Airelay.EnsureSession(ctx, project, worktree, airelay.AgentLogPath(taskRoot)); err != nil {
			return RunOutcome{}, err
		}
	}
	if err := r.waitForCompletion(ctx, envelope, manifest, project.SessionKey, taskRoot, worktree, resuming); err != nil {
		return RunOutcome{}, err
	}
	return r.finalizeAgentResult(ctx, envelope, manifest, taskRoot, worktree, resultPath)
}

func (r *Runner) loadTask(ctx context.Context, projectID, taskID string) (Envelope, Manifest, config.ProjectConfig, string, error) {
	taskRoot := r.Layout.TaskRoot(projectID, taskID)
	e, err := LoadEnvelope(filepath.Join(taskRoot, "task.json"))
	if err != nil {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", err
	}
	if e.GatewayID != r.Config.Gateway.ID || e.ProjectID != projectID || e.TaskID != taskID {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", fmt.Errorf("task identity does not match local routing")
	}
	project, ok := r.Config.Projects[projectID]
	if !ok {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", fmt.Errorf("project %s is not configured", projectID)
	}
	packRoot, err := ResolveInside(taskRoot, e.PatchPackPath)
	if err != nil {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", err
	}
	m, err := LoadManifest(filepath.Join(packRoot, "manifest.json"))
	if err != nil {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", err
	}
	if _, err := LoadAgentHandoff(packRoot, m); err != nil {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", err
	}
	if m.Target.Repository != project.Repository || m.Target.Branch != project.DefaultBranch {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", fmt.Errorf("patch target does not match configured project")
	}
	if err := r.Git.VerifyRepository(ctx, project.Path, project.Repository); err != nil {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", err
	}
	if err := r.Git.VerifyCommit(ctx, project.Path, m.Target.BaseRevision); err != nil {
		return Envelope{}, Manifest{}, config.ProjectConfig{}, "", fmt.Errorf("verify patch base: %w", err)
	}
	return e, m, project, packRoot, nil
}

func (r *Runner) applyPayload(ctx context.Context, e Envelope, m Manifest, packRoot, worktree string) (bool, error) {
	patchApplied := false
	patchPath := filepath.Join(packRoot, "patch", "changes.patch")
	if info, err := os.Stat(patchPath); err == nil && info.Size() > 0 {
		_ = WriteStatus(r.Layout.TaskRoot(e.ProjectID, e.TaskID), r.status(e, "applying_patch", "", worktree))
		if err := r.Git.ApplyPatch(ctx, worktree, patchPath); err != nil {
			if !r.Config.Gateway.AllowPatchRepair {
				return false, err
			}
			if _, resetErr := r.Git.Run(ctx, worktree, "reset", "--hard", m.Target.BaseRevision); resetErr != nil {
				return false, resetErr
			}
			_ = WriteStatus(r.Layout.TaskRoot(e.ProjectID, e.TaskID), r.status(e, "patch_repair_required", err.Error(), worktree))
		} else {
			patchApplied = true
		}
	}
	if err := applyOverlay(packRoot, worktree); err != nil {
		return false, err
	}
	if err := applyDeletes(packRoot, worktree); err != nil {
		return false, err
	}
	ready := patchApplied || hasOverlay(packRoot) || len(m.FilesDeleted) > 0
	if ready {
		actual, err := r.Git.ScopeFromBase(ctx, worktree, m.Target.BaseRevision)
		if err != nil {
			return false, err
		}
		if err := CompareScope(m, actual); err != nil {
			_ = WriteStatus(r.Layout.TaskRoot(e.ProjectID, e.TaskID), r.status(e, "needs_gpt_revision", err.Error(), worktree))
			return false, err
		}
	}
	return ready, nil
}

func (r *Runner) CompleteTask(ctx context.Context, projectID, taskID string) error {
	e, m, _, _, err := r.loadTask(ctx, projectID, taskID)
	if err != nil {
		return err
	}
	taskRoot := r.Layout.TaskRoot(projectID, taskID)
	worktree := r.Layout.WorktreeRoot(projectID, taskID)
	result, err := LoadAgentResult(ResultPath(taskRoot))
	if err != nil {
		return err
	}
	if result.TaskID != taskID {
		return fmt.Errorf("agent result task_id mismatch")
	}
	if err := result.ValidateAgainst(m); err != nil {
		return err
	}
	if err := r.validateResultState(ctx, e, m, taskRoot, worktree, result); err != nil {
		return err
	}
	return WriteCompletion(taskRoot, taskID)
}

func (r *Runner) waitForCompletion(ctx context.Context, e Envelope, m Manifest, sessionKey, taskRoot, worktree string, resuming bool) error {
	deadline := time.Now().Add(time.Duration(r.Config.Gateway.AgentTimeoutSeconds) * time.Second)
	initialGrace := time.Now().Add(15 * time.Second)
	if resuming {
		initialGrace = time.Now()
	}
	corrected := false
	var promptableSince time.Time
	for time.Now().Before(deadline) {
		if regularFile(ResultPath(taskRoot)) && regularFile(CompletionPath(taskRoot)) {
			if _, err := LoadCompletion(taskRoot, e.TaskID); err == nil {
				return nil
			}
		}
		status, err := r.Airelay.Status(ctx, sessionKey)
		if err == nil && isPromptableState(status.State) && time.Now().After(initialGrace) {
			if promptableSince.IsZero() {
				promptableSince = time.Now()
			}
			if !corrected {
				prompt := "Task is not finalized. Write strict JSON to " + ResultPath(taskRoot) + " and run " + CompleteTaskPath(taskRoot) + ". Do not reply only in chat."
				if err := r.Airelay.Prompt(ctx, sessionKey, prompt); err != nil {
					return err
				}
				corrected = true
				promptableSince = time.Now()
			} else if time.Since(promptableSince) >= 30*time.Second {
				return r.syntheticFailure(ctx, e, m, taskRoot, worktree, "agent session became promptable without running complete-task")
			}
		} else if err == nil && !isPromptableState(status.State) {
			promptableSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return r.syntheticFailure(ctx, e, m, taskRoot, worktree, fmt.Sprintf("agent result timed out after %s", time.Duration(r.Config.Gateway.AgentTimeoutSeconds)*time.Second))
}
func isPromptableState(state string) bool {
	return state == "free" || state == "idle" || state == "promptable"
}
func (r *Runner) syntheticFailure(ctx context.Context, e Envelope, m Manifest, taskRoot, worktree, reason string) error {
	result := AgentResult{SchemaVersion: 2, TaskID: e.TaskID, Status: "failed", Summary: "Gateway synthetic failure: " + reason, Details: []string{"The agent session ended or became promptable without completing the mandatory JSON finalizer."}, Gates: []AgentGate{}, Deviations: []AgentDeviation{}}
	if err := writeJSONAtomic(ResultPath(taskRoot), result); err != nil {
		return err
	}
	if err := result.ValidateAgainst(m); err != nil {
		return err
	}
	return WriteCompletion(taskRoot, e.TaskID)
}

func (r *Runner) finalizeAgentResult(ctx context.Context, e Envelope, m Manifest, taskRoot, worktree, resultPath string) (RunOutcome, error) {
	if _, err := LoadCompletion(taskRoot, e.TaskID); err != nil {
		return RunOutcome{}, err
	}
	result, err := LoadAgentResult(resultPath)
	if err != nil {
		return RunOutcome{}, err
	}
	if result.TaskID != e.TaskID {
		return RunOutcome{}, fmt.Errorf("agent result task_id mismatch")
	}
	if err := result.ValidateAgainst(m); err != nil {
		return RunOutcome{}, err
	}
	if err := r.validateResultState(ctx, e, m, taskRoot, worktree, result); err != nil {
		return RunOutcome{}, err
	}
	s := r.status(e, result.Status, result.Summary, worktree)
	if err := WriteStatus(taskRoot, s); err != nil {
		return RunOutcome{}, err
	}
	return RunOutcome{Envelope: e, Manifest: m, Status: s, Result: &result}, nil
}
func (r *Runner) validateResultState(ctx context.Context, e Envelope, m Manifest, taskRoot, worktree string, result AgentResult) error {
	if result.Status != "succeeded" {
		return nil
	}
	if err := r.Git.VerifyCommit(ctx, worktree, result.ImplementationCommit); err != nil {
		return fmt.Errorf("verify implementation commit: %w", err)
	}
	if err := r.Git.VerifyCommit(ctx, worktree, result.EvidenceCommit); err != nil {
		return fmt.Errorf("verify evidence commit: %w", err)
	}
	head, err := r.Git.Run(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(head.Stdout) != result.EvidenceCommit {
		return fmt.Errorf("worktree HEAD does not equal evidence commit")
	}
	parent, err := r.Git.Run(ctx, worktree, "rev-parse", result.EvidenceCommit+"^")
	if err != nil {
		return err
	}
	if strings.TrimSpace(parent.Stdout) != result.ImplementationCommit {
		return fmt.Errorf("evidence commit is not a direct child of implementation commit")
	}
	implParent, err := r.Git.Run(ctx, worktree, "rev-parse", result.ImplementationCommit+"^")
	if err != nil {
		return err
	}
	if strings.TrimSpace(implParent.Stdout) != m.Target.BaseRevision {
		return fmt.Errorf("implementation commit is not a direct child of manifest base")
	}
	branch, err := r.Git.Run(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return err
	}
	if strings.TrimSpace(branch.Stdout) != e.ResultBranch {
		return fmt.Errorf("worktree branch mismatch")
	}
	dirty, err := r.Git.Run(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(dirty.Stdout) != "" {
		return fmt.Errorf("worktree is not clean")
	}
	remote, err := r.Git.Run(ctx, worktree, "ls-remote", "origin", "refs/heads/"+e.ResultBranch)
	if err != nil {
		return err
	}
	fields := strings.Fields(remote.Stdout)
	if len(fields) != 2 || fields[0] != result.EvidenceCommit {
		return fmt.Errorf("remote result branch does not equal evidence commit")
	}
	scope, err := r.Git.ScopeFromBase(ctx, worktree, m.Target.BaseRevision)
	if err != nil {
		return err
	}
	evidencePrefix := strings.TrimSuffix(m.EvidenceDirectory, "/") + "/"
	scope.Created = filterEvidence(scope.Created, evidencePrefix)
	if err := CompareScope(m, scope); err != nil {
		return err
	}
	packRoot, err := ResolveInside(taskRoot, e.PatchPackPath)
	if err != nil {
		return err
	}
	return verifyEvidence(ctx, packRoot, worktree, result)
}
func filterEvidence(paths []string, prefix string) []string {
	out := paths[:0]
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

func (r *Runner) Rollback(ctx context.Context, projectID, taskID string) error {
	project, ok := r.Config.Projects[projectID]
	if !ok {
		return fmt.Errorf("project %s is not configured", projectID)
	}
	taskRoot := r.Layout.TaskRoot(projectID, taskID)
	e, err := LoadEnvelope(filepath.Join(taskRoot, "task.json"))
	if err != nil {
		return err
	}
	worktree := r.Layout.WorktreeRoot(projectID, taskID)
	if _, err := os.Stat(worktree); err == nil {
		_, _ = r.Git.Run(ctx, worktree, "reset", "--hard", "refs/gpt-gateway/backups/"+taskID)
		if err := r.Git.RemoveWorktree(ctx, project.Path, worktree); err != nil {
			return err
		}
	}
	if _, err := r.Git.Run(ctx, project.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+e.ResultBranch); err == nil {
		if err := r.Git.DeleteBranch(ctx, project.Path, e.ResultBranch); err != nil {
			return err
		}
	}
	return WriteStatus(taskRoot, r.status(e, "rolled_back", "task worktree and branch removed", ""))
}
func (r *Runner) status(e Envelope, state, message, worktree string) Status {
	return Status{SchemaVersion: 1, TaskID: e.TaskID, GatewayID: e.GatewayID, ProjectID: e.ProjectID, State: state, UpdatedAt: time.Now().UTC(), Message: message, Worktree: worktree, ResultBranch: e.ResultBranch}
}
func WriteStatus(root string, s Status) error {
	return writeJSONAtomic(filepath.Join(root, "status.json"), s)
}
func ReadStatus(root string) (Status, error) {
	var s Status
	if err := decodeStrict(filepath.Join(root, "status.json"), &s); err != nil {
		return Status{}, err
	}
	return s, nil
}
func ReadApproval(root, taskID string) (Approval, error) {
	path := filepath.Join(root, "approval.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return Approval{SchemaVersion: 1, TaskID: taskID, Decision: "pending"}, nil
	}
	var a Approval
	if err := decodeStrict(path, &a); err != nil {
		return Approval{}, err
	}
	if a.SchemaVersion != 1 || a.TaskID != taskID {
		return Approval{}, fmt.Errorf("invalid approval identity")
	}
	return a, nil
}
func WriteApproval(root, taskID, decision, reason string) error {
	if decision != "approved" && decision != "rejected" {
		return fmt.Errorf("unsupported approval decision %q", decision)
	}
	return writeJSONAtomic(filepath.Join(root, "approval.json"), Approval{SchemaVersion: 1, TaskID: taskID, Decision: decision, Reason: reason, DecidedAt: time.Now().UTC()})
}
func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
func applyOverlay(packRoot, worktree string) error {
	overlay := filepath.Join(packRoot, "overlay")
	if _, err := os.Stat(overlay); os.IsNotExist(err) {
		return nil
	}
	return filepath.Walk(overlay, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("overlay contains symlink %s", path)
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(overlay, path)
		if err != nil {
			return err
		}
		if info.Name() == ".gitkeep" {
			return nil
		}
		destination := filepath.Join(worktree, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return copyRegular(path, destination, info.Mode().Perm())
	})
}
func hasOverlay(packRoot string) bool {
	found := false
	_ = filepath.Walk(filepath.Join(packRoot, "overlay"), func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && info.Mode().IsRegular() && info.Name() != ".gitkeep" {
			found = true
		}
		return nil
	})
	return found
}
func applyDeletes(packRoot, worktree string) error {
	path := filepath.Join(packRoot, "patch", "delete-paths.txt")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		v := scanner.Text()
		if v == "" || strings.HasPrefix(v, "#") {
			continue
		}
		resolved, err := ResolveInside(worktree, v)
		if err != nil {
			return err
		}
		if err := os.Remove(resolved); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return scanner.Err()
}
func copyRegular(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
func verifyEvidence(ctx context.Context, packRoot, worktree string, result AgentResult) error {
	script := filepath.Join(packRoot, "scripts", "verify-agent-evidence.py")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("missing pinned evidence verifier: %w", err)
	}
	_, err := execx.Run(ctx, worktree, "python3", script, "committed", "--pack", packRoot, "--repo", worktree, "--implementation-commit", result.ImplementationCommit, "--evidence-commit", result.EvidenceCommit)
	if err != nil {
		return fmt.Errorf("agent evidence verification failed: %w", err)
	}
	return nil
}
