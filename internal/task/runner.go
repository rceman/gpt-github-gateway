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
	return &Runner{
		Config:  cfg,
		Layout:  layout,
		Git:     gitx.New(),
		Airelay: airelay.New(cfg.Airelay.Binary),
	}
}

func (r *Runner) Run(ctx context.Context, projectID, taskID string) (RunOutcome, error) {
	taskRoot := r.Layout.TaskRoot(projectID, taskID)
	envelope, err := LoadEnvelope(filepath.Join(taskRoot, "task.json"))
	if err != nil {
		return RunOutcome{}, err
	}
	if envelope.GatewayID != r.Config.Gateway.ID || envelope.ProjectID != projectID || envelope.TaskID != taskID {
		return RunOutcome{}, fmt.Errorf("task identity does not match local routing")
	}
	project, exists := r.Config.Projects[projectID]
	if !exists {
		return RunOutcome{}, fmt.Errorf("project %s is not configured", projectID)
	}
	packRoot, err := ResolveInside(taskRoot, envelope.PatchPackPath)
	if err != nil {
		return RunOutcome{}, err
	}
	manifest, err := LoadManifest(filepath.Join(packRoot, "manifest.json"))
	if err != nil {
		return RunOutcome{}, err
	}
	if manifest.Target.Repository != project.Repository {
		return RunOutcome{}, fmt.Errorf("patch target repository %s does not match local project %s", manifest.Target.Repository, project.Repository)
	}
	if manifest.Target.Branch != project.DefaultBranch {
		return RunOutcome{}, fmt.Errorf("patch target branch %s does not match local project default %s", manifest.Target.Branch, project.DefaultBranch)
	}
	if err := r.Git.VerifyRepository(ctx, project.Path, project.Repository); err != nil {
		return RunOutcome{}, err
	}
	if err := r.Git.VerifyCommit(ctx, project.Path, manifest.Target.BaseRevision); err != nil {
		return RunOutcome{}, fmt.Errorf("verify patch base: %w", err)
	}
	if envelope.ApprovalRequired {
		approval, err := ReadApproval(taskRoot, taskID)
		if err != nil {
			return RunOutcome{}, err
		}
		if approval.Decision == "rejected" {
			status := r.status(envelope, "rejected", approval.Reason, "")
			_ = WriteStatus(taskRoot, status)
			return RunOutcome{Envelope: envelope, Manifest: manifest, Status: status}, nil
		}
		if approval.Decision != "approved" {
			status := r.status(envelope, "waiting_for_approval", "local owner approval is required", "")
			_ = WriteStatus(taskRoot, status)
			return RunOutcome{Envelope: envelope, Manifest: manifest, Status: status}, nil
		}
	}

	worktree := r.Layout.WorktreeRoot(projectID, taskID)
	responsePath := filepath.Join(taskRoot, "agent-response.md")
	resultPath := filepath.Join(taskRoot, "agent-result.json")
	if regularFile(resultPath) && regularFile(responsePath) {
		return r.finalizeAgentResult(ctx, envelope, manifest, taskRoot, worktree, resultPath, responsePath)
	}
	status := r.status(envelope, "preparing_worktree", "", worktree)
	if err := WriteStatus(taskRoot, status); err != nil {
		return RunOutcome{}, err
	}
	if err := r.Git.BackupRef(ctx, project.Path, taskID, manifest.Target.BaseRevision); err != nil {
		return RunOutcome{}, fmt.Errorf("create backup ref: %w", err)
	}
	if _, err := os.Stat(worktree); os.IsNotExist(err) {
		if err := r.Git.CreateWorktree(ctx, project.Path, worktree, envelope.ResultBranch, manifest.Target.BaseRevision); err != nil {
			return RunOutcome{}, fmt.Errorf("create task worktree: %w", err)
		}
	}

	patchApplied := false
	patchPath := filepath.Join(packRoot, "patch", "changes.patch")
	if info, statErr := os.Stat(patchPath); statErr == nil && info.Size() > 0 {
		status = r.status(envelope, "applying_patch", "", worktree)
		_ = WriteStatus(taskRoot, status)
		if err := r.Git.ApplyPatch(ctx, worktree, patchPath); err != nil {
			if !r.Config.Gateway.AllowPatchRepair {
				status = r.status(envelope, "needs_gpt_revision", err.Error(), worktree)
				_ = WriteStatus(taskRoot, status)
				return RunOutcome{Envelope: envelope, Manifest: manifest, Status: status}, nil
			}
			if _, resetErr := r.Git.Run(ctx, worktree, "reset", "--hard", manifest.Target.BaseRevision); resetErr != nil {
				return RunOutcome{}, fmt.Errorf("reset failed patch worktree: %w", resetErr)
			}
			status = r.status(envelope, "patch_repair_required", err.Error(), worktree)
			_ = WriteStatus(taskRoot, status)
		} else {
			patchApplied = true
		}
	}
	if err := applyOverlay(packRoot, worktree); err != nil {
		return RunOutcome{}, err
	}
	if err := applyDeletes(packRoot, worktree); err != nil {
		return RunOutcome{}, err
	}
	payloadReady := patchApplied || hasOverlay(packRoot) || len(manifest.FilesDeleted) > 0
	if payloadReady {
		actual, err := r.Git.ScopeFromBase(ctx, worktree, manifest.Target.BaseRevision)
		if err != nil {
			return RunOutcome{}, err
		}
		if err := CompareScope(manifest, actual); err != nil {
			status = r.status(envelope, "needs_gpt_revision", err.Error(), worktree)
			_ = WriteStatus(taskRoot, status)
			return RunOutcome{Envelope: envelope, Manifest: manifest, Status: status}, nil
		}
	}

	requestSource, err := ResolveInside(taskRoot, envelope.RequestPath)
	if err != nil {
		return RunOutcome{}, err
	}
	agentRequestPath := filepath.Join(taskRoot, "agent-request.md")
	_ = os.Remove(responsePath)
	_ = os.Remove(resultPath)
	if err := writeAgentRequest(agentRequestPath, agentRequest{
		TaskID:       taskID,
		ProjectID:    projectID,
		Worktree:     worktree,
		PackRoot:     packRoot,
		OwnerRequest: requestSource,
		ResponsePath: responsePath,
		ResultPath:   resultPath,
		PatchApplied: payloadReady,
		PatchRepair:  !payloadReady,
		ResultBranch: envelope.ResultBranch,
		BaseRevision: manifest.Target.BaseRevision,
		EvidenceDir:  manifest.EvidenceDirectory,
	}); err != nil {
		return RunOutcome{}, err
	}

	status = r.status(envelope, "waiting_for_agent", "", worktree)
	_ = WriteStatus(taskRoot, status)
	if err := r.Airelay.EnsureSession(ctx, project, worktree, airelay.AgentLogPath(taskRoot)); err != nil {
		status = r.status(envelope, "agent_unavailable", err.Error(), worktree)
		_ = WriteStatus(taskRoot, status)
		return RunOutcome{Envelope: envelope, Manifest: manifest, Status: status}, nil
	}
	prompt := "Read " + agentRequestPath + " and execute it. Write the requested result files."
	if err := r.Airelay.Prompt(ctx, project.SessionKey, prompt); err != nil {
		return RunOutcome{}, err
	}
	status = r.status(envelope, "agent_running", "", worktree)
	_ = WriteStatus(taskRoot, status)
	if err := airelay.WaitForResult(ctx, resultPath, responsePath, time.Duration(r.Config.Gateway.AgentTimeoutSeconds)*time.Second); err != nil {
		status = r.status(envelope, "agent_timeout", err.Error(), worktree)
		_ = WriteStatus(taskRoot, status)
		return RunOutcome{Envelope: envelope, Manifest: manifest, Status: status}, nil
	}

	return r.finalizeAgentResult(ctx, envelope, manifest, taskRoot, worktree, resultPath, responsePath)
}

func (r *Runner) finalizeAgentResult(ctx context.Context, envelope Envelope, manifest Manifest, taskRoot, worktree, resultPath, responsePath string) (RunOutcome, error) {
	result, err := LoadAgentResult(resultPath)
	if err != nil {
		return RunOutcome{}, err
	}
	if result.TaskID != envelope.TaskID {
		return RunOutcome{}, fmt.Errorf("agent result task_id mismatch")
	}
	if err := result.ValidateAgainst(manifest); err != nil {
		return RunOutcome{}, err
	}
	response, err := os.ReadFile(responsePath)
	if err != nil {
		return RunOutcome{}, err
	}
	if len(response) > 1<<20 {
		return RunOutcome{}, fmt.Errorf("agent response exceeds 1 MiB")
	}
	if result.Status == "succeeded" {
		if err := r.Git.VerifyCommit(ctx, worktree, result.ImplementationCommit); err != nil {
			return RunOutcome{}, fmt.Errorf("verify implementation commit: %w", err)
		}
		if err := r.Git.VerifyCommit(ctx, worktree, result.EvidenceCommit); err != nil {
			return RunOutcome{}, fmt.Errorf("verify evidence commit: %w", err)
		}
		packRoot, err := ResolveInside(taskRoot, envelope.PatchPackPath)
		if err != nil {
			return RunOutcome{}, err
		}
		if err := verifyEvidence(ctx, packRoot, worktree, result); err != nil {
			return RunOutcome{}, err
		}
	}
	status := r.status(envelope, result.Status, result.Summary, worktree)
	if err := WriteStatus(taskRoot, status); err != nil {
		return RunOutcome{}, err
	}
	return RunOutcome{Envelope: envelope, Manifest: manifest, Status: status, Result: &result, Response: response}, nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (r *Runner) Rollback(ctx context.Context, projectID, taskID string) error {
	project, exists := r.Config.Projects[projectID]
	if !exists {
		return fmt.Errorf("project %s is not configured", projectID)
	}
	taskRoot := r.Layout.TaskRoot(projectID, taskID)
	envelope, err := LoadEnvelope(filepath.Join(taskRoot, "task.json"))
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
	if _, err := r.Git.Run(ctx, project.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+envelope.ResultBranch); err == nil {
		if err := r.Git.DeleteBranch(ctx, project.Path, envelope.ResultBranch); err != nil {
			return err
		}
	}
	status := r.status(envelope, "rolled_back", "task worktree and branch removed", "")
	return WriteStatus(taskRoot, status)
}

func (r *Runner) status(envelope Envelope, state, message, worktree string) Status {
	return Status{
		SchemaVersion: 1,
		TaskID:        envelope.TaskID,
		GatewayID:     envelope.GatewayID,
		ProjectID:     envelope.ProjectID,
		State:         state,
		UpdatedAt:     time.Now().UTC(),
		Message:       message,
		Worktree:      worktree,
		ResultBranch:  envelope.ResultBranch,
	}
}

func WriteStatus(taskRoot string, status Status) error {
	return writeJSONAtomic(filepath.Join(taskRoot, "status.json"), status)
}

func ReadStatus(taskRoot string) (Status, error) {
	var status Status
	if err := decodeStrict(filepath.Join(taskRoot, "status.json"), &status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func ReadApproval(taskRoot, taskID string) (Approval, error) {
	path := filepath.Join(taskRoot, "approval.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return Approval{SchemaVersion: 1, TaskID: taskID, Decision: "pending"}, nil
	}
	var approval Approval
	if err := decodeStrict(path, &approval); err != nil {
		return Approval{}, err
	}
	if approval.SchemaVersion != 1 || approval.TaskID != taskID {
		return Approval{}, fmt.Errorf("invalid approval identity")
	}
	return approval, nil
}

func WriteApproval(taskRoot, taskID, decision, reason string) error {
	if decision != "approved" && decision != "rejected" {
		return fmt.Errorf("unsupported approval decision %q", decision)
	}
	return writeJSONAtomic(filepath.Join(taskRoot, "approval.json"), Approval{
		SchemaVersion: 1,
		TaskID:        taskID,
		Decision:      decision,
		Reason:        reason,
		DecidedAt:     time.Now().UTC(),
	})
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
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

type agentRequest struct {
	TaskID       string
	ProjectID    string
	Worktree     string
	PackRoot     string
	OwnerRequest string
	ResponsePath string
	ResultPath   string
	PatchApplied bool
	PatchRepair  bool
	ResultBranch string
	BaseRevision string
	EvidenceDir  string
}

func writeAgentRequest(path string, request agentRequest) error {
	patchState := "The GPT-authored patch has already been applied and its declared file scope has been verified."
	if request.PatchRepair {
		patchState = "Automatic patch application failed. Apply the supplied payload manually only within the manifest-declared scope, then continue. Do not redesign."
	}
	content := fmt.Sprintf(`# Local Agent Runtime Task

Task: %s
Project: %s
Worktree: %s
Result branch: %s
Base revision: %s

%s

Read only what is required:

1. Owner request: %s
2. Patch manifest: %s
3. Already changed files and direct runtime failures.

Mandatory workflow pin:

- repository: %s
- version: %s
- commit: %s
- document: %s

Your role is constrained to repository operation, runtime validation, and narrow integration repair.

Do not perform broad repository exploration. Do not redesign behavior. Do not add dependencies, expand scope, weaken tests, change acceptance criteria, or promote optional hardening into this task.

Required procedure:

1. Verify the worktree branch and base.
2. Preserve the manifest's exact created/modified/deleted file classes.
3. Run every manifest gate exactly as written.
4. Fix only directly observed compile, formatting, type, fixture, test-harness, or environment integration defects.
5. Add regression coverage only for a repair you actually make.
6. Run all gates again.
7. Create the implementation commit.
8. Create the evidence-only commit containing exactly:
   - %s/manifest.json
   - %s/evidence.json
9. Run the patch-pack evidence verifier.
10. Write a concise report to: %s
11. Write machine JSON matching protocol/v1/agent-result.schema.json to: %s

The JSON status must be one of succeeded, failed, or needs_gpt_revision. A successful result must list every manifest gate once with status=pass and exit=0, and must include both commit SHAs.
`, request.TaskID, request.ProjectID, request.Worktree, request.ResultBranch, request.BaseRevision, patchState, request.OwnerRequest, filepath.Join(request.PackRoot, "manifest.json"), WorkflowRepository, WorkflowVersion, WorkflowCommit, WorkflowDocument, request.EvidenceDir, request.EvidenceDir, request.ResponsePath, request.ResultPath)
	return os.WriteFile(path, []byte(content), 0o600)
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
		value := scanner.Text()
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		resolved, err := ResolveInside(worktree, value)
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
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer output.Close()
	_, err = io.Copy(output, input)
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
