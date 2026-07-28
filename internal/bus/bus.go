package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/execx"
	"github.com/rceman/gpt-github-gateway/internal/task"
	"github.com/rceman/gpt-github-gateway/internal/taskbundle"
)

type Bus struct {
	Config config.BusConfig
	Root   string
	Git    string
}

type RemoteTask struct {
	Envelope        task.Envelope
	Root            string
	ProtocolVersion int
	Bundle          *taskbundle.Bundle
}

func New(cfg config.BusConfig, root string) *Bus {
	return &Bus{Config: cfg, Root: root, Git: "git"}
}

func (b *Bus) Ensure(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(b.Root, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(b.Root), 0o700); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, "", b.Git, "clone", "--branch", b.Config.Branch, "--single-branch", b.Config.URL, b.Root); err == nil {
		return nil
	}
	_ = os.RemoveAll(b.Root)
	if _, err := execx.Run(ctx, "", b.Git, "clone", "--no-checkout", b.Config.URL, b.Root); err != nil {
		return err
	}
	if err := b.initializeOrphanBranch(ctx); err == nil {
		return nil
	}
	_ = os.RemoveAll(b.Root)
	_, err := execx.Run(ctx, "", b.Git, "clone", "--branch", b.Config.Branch, "--single-branch", b.Config.URL, b.Root)
	return err
}

func (b *Bus) initializeOrphanBranch(ctx context.Context) error {
	if _, err := execx.Run(ctx, b.Root, b.Git, "checkout", "--orphan", b.Config.Branch); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "rm", "-rf", "--ignore-unmatch", "."); err != nil {
		return err
	}
	readme := []byte("# GPT GitHub Gateway Bus\n\nRuntime task and status branch managed by gpt-github-gateway.\n")
	if err := os.WriteFile(filepath.Join(b.Root, "README.md"), readme, 0o600); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "add", "--", "README.md"); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "commit", "-m", "gateway: initialize bus branch"); err != nil {
		return err
	}
	_, err := execx.Run(ctx, b.Root, b.Git, "push", "-u", "origin", b.Config.Branch)
	return err
}

func (b *Bus) Sync(ctx context.Context) error {
	if err := b.Ensure(ctx); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "fetch", "origin", b.Config.Branch); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "checkout", b.Config.Branch); err != nil {
		return err
	}
	_, err := execx.Run(ctx, b.Root, b.Git, "pull", "--ff-only", "origin", b.Config.Branch)
	return err
}

func (b *Bus) Discover(gatewayID string, maxFile, maxAggregate int64) ([]RemoteTask, error) {
	result := []RemoteTask{}
	legacyPattern := filepath.Join(b.Root, "tasks", gatewayID, "*", "*", "task.json")
	legacyPaths, err := filepath.Glob(legacyPattern)
	if err != nil {
		return nil, err
	}
	for _, filename := range legacyPaths {
		envelope, err := task.LoadEnvelope(filename)
		if err != nil {
			return nil, fmt.Errorf("load remote task %s: %w", filename, err)
		}
		if envelope.GatewayID != gatewayID {
			continue
		}
		result = append(result, RemoteTask{
			Envelope:        envelope,
			Root:            filepath.Dir(filename),
			ProtocolVersion: 1,
		})
	}

	bundlePattern := filepath.Join(b.Root, "inbox", gatewayID, "*", "*.taskbundle.json")
	bundlePaths, err := filepath.Glob(bundlePattern)
	if err != nil {
		return nil, err
	}
	for _, filename := range bundlePaths {
		bundle, err := taskbundle.Load(filename, maxAggregate)
		if err != nil {
			return nil, fmt.Errorf("load atomic task bundle %s: %w", filename, err)
		}
		if bundle.GatewayID != gatewayID {
			continue
		}
		projectDirectory := filepath.Base(filepath.Dir(filename))
		expectedFilename := bundle.TaskID + ".taskbundle.json"
		if projectDirectory != bundle.ProjectID || filepath.Base(filename) != expectedFilename {
			return nil, fmt.Errorf("atomic task bundle path does not match its routing identity: %s", filename)
		}
		result = append(result, RemoteTask{
			Envelope:        bundle.Envelope(),
			Root:            filename,
			ProtocolVersion: taskbundle.SchemaVersion,
			Bundle:          bundle,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Envelope.ProjectID != result[j].Envelope.ProjectID {
			return result[i].Envelope.ProjectID < result[j].Envelope.ProjectID
		}
		if result[i].Envelope.TaskID != result[j].Envelope.TaskID {
			return result[i].Envelope.TaskID < result[j].Envelope.TaskID
		}
		return result[i].ProtocolVersion < result[j].ProtocolVersion
	})
	for index := 1; index < len(result); index++ {
		previous := result[index-1].Envelope
		current := result[index].Envelope
		if previous.ProjectID == current.ProjectID && previous.TaskID == current.TaskID {
			return nil, fmt.Errorf("duplicate remote task identity %s/%s", current.ProjectID, current.TaskID)
		}
	}
	return result, nil
}

func (b *Bus) Materialize(remote RemoteTask, localRoot string, maxFile, maxAggregate int64) error {
	if remote.ProtocolVersion == taskbundle.SchemaVersion {
		if remote.Bundle == nil {
			return fmt.Errorf("atomic task bundle metadata is missing")
		}
		return remote.Bundle.Materialize(localRoot, maxFile, maxAggregate)
	}
	return materializeDirectory(remote.Root, localRoot, maxFile, maxAggregate)
}

func materializeDirectory(remoteRoot, localRoot string, maxFile, maxAggregate int64) error {
	if _, err := os.Stat(localRoot); err == nil {
		return nil
	}
	temp := localRoot + ".tmp"
	if err := os.RemoveAll(temp); err != nil {
		return err
	}
	var total int64
	err := filepath.WalkDir(remoteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(remoteRoot, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(temp, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("remote task contains symlink %s", relative)
		}
		destination := filepath.Join(temp, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("remote task contains non-regular file %s", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFile {
			return fmt.Errorf("remote task file %s exceeds size limit", relative)
		}
		total += info.Size()
		if total > maxAggregate {
			return fmt.Errorf("remote task exceeds aggregate size limit")
		}
		return copyFile(path, destination, maxFile)
	})
	if err != nil {
		_ = os.RemoveAll(temp)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(localRoot), 0o700); err != nil {
		return err
	}
	return os.Rename(temp, localRoot)
}

func (b *Bus) PublishGateway(ctx context.Context, gatewayID string, payload any) error {
	path := filepath.Join("gateways", gatewayID, "gateway.json")
	return b.publishJSON(ctx, path, payload, "gateway: update "+gatewayID)
}

func (b *Bus) PublishProject(ctx context.Context, gatewayID, projectID string, payload any) error {
	path := filepath.Join("gateways", gatewayID, "projects", projectID, "state.json")
	return b.publishJSON(ctx, path, payload, "gateway: update "+gatewayID+"/"+projectID)
}

func (b *Bus) PublishTask(ctx context.Context, gatewayID, projectID, taskID string, files map[string][]byte) error {
	paths := make([]string, 0, len(files))
	for name, data := range files {
		if err := task.ValidateRelativePath(name); err != nil {
			return err
		}
		relative := filepath.Join("tasks", gatewayID, projectID, taskID, "agent", filepath.FromSlash(name))
		absolute := filepath.Join(b.Root, relative)
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(absolute, data, 0o600); err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
	}
	return b.commitAndPush(ctx, paths, "gateway: report "+gatewayID+"/"+projectID+"/"+taskID)
}

func (b *Bus) PublishAtomicResult(ctx context.Context, remote RemoteTask, status task.Status, result *task.AgentResult, response []byte) error {
	if remote.ProtocolVersion != taskbundle.SchemaVersion || remote.Bundle == nil {
		return fmt.Errorf("remote task is not an atomic task bundle")
	}
	payload := AtomicTaskResult{
		SchemaVersion: 2,
		TaskID:        remote.Envelope.TaskID,
		GatewayID:     remote.Envelope.GatewayID,
		ProjectID:     remote.Envelope.ProjectID,
		BundleSHA256:  remote.Bundle.Archive.SHA256,
		State:         status.State,
		ResultBranch:  remote.Envelope.ResultBranch,
		Summary:       status.Message,
		HumanResponse: string(response),
		SubmittedAt:   remote.Envelope.SubmittedAt,
		CompletedAt:   status.UpdatedAt,
	}
	if result != nil {
		payload.ImplementationCommit = result.ImplementationCommit
		payload.EvidenceCommit = result.EvidenceCommit
		payload.Gates = result.Gates
		payload.Deviations = result.Deviations
		if result.Summary != "" {
			payload.Summary = result.Summary
		}
	}
	path := filepath.Join("results", remote.Envelope.GatewayID, remote.Envelope.ProjectID, remote.Envelope.TaskID+".result.json")
	return b.publishJSON(ctx, path, payload, "gateway: result "+remote.Envelope.GatewayID+"/"+remote.Envelope.ProjectID+"/"+remote.Envelope.TaskID)
}

func (b *Bus) publishJSON(ctx context.Context, relative string, payload any, message string) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	absolute := filepath.Join(b.Root, relative)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(absolute, data, 0o600); err != nil {
		return err
	}
	return b.commitAndPush(ctx, []string{filepath.ToSlash(relative)}, message)
}

func (b *Bus) commitAndPush(ctx context.Context, paths []string, message string) error {
	args := append([]string{"add", "--"}, paths...)
	if _, err := execx.Run(ctx, b.Root, b.Git, args...); err != nil {
		return err
	}
	status, err := execx.Run(ctx, b.Root, b.Git, "diff", "--cached", "--quiet", "--")
	if err == nil && status.ExitCode == 0 {
		return nil
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "commit", "-m", message); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "pull", "--rebase", "origin", b.Config.Branch); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "push", "origin", b.Config.Branch); err == nil {
		return nil
	}
	if _, pullErr := execx.Run(ctx, b.Root, b.Git, "pull", "--rebase", "origin", b.Config.Branch); pullErr != nil {
		return pullErr
	}
	_, err = execx.Run(ctx, b.Root, b.Git, "push", "origin", b.Config.Branch)
	return err
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
	SchemaVersion int       `json:"schema_version"`
	GatewayID     string    `json:"gateway_id"`
	Status        string    `json:"status"`
	UpdatedAt     time.Time `json:"updated_at"`
	Projects      []string  `json:"projects"`
	Capabilities  []string  `json:"capabilities"`
}

type ProjectState struct {
	SchemaVersion int       `json:"schema_version"`
	GatewayID     string    `json:"gateway_id"`
	ProjectID     string    `json:"project_id"`
	Repository    string    `json:"repository"`
	SessionKey    string    `json:"session_key"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func CommitIdentity(repository string) string {
	return strings.TrimSpace(repository)
}
