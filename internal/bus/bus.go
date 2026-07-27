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
)

type Bus struct {
	Config config.BusConfig
	Root   string
	Git    string
}

type RemoteTask struct {
	Envelope task.Envelope
	Root     string
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
	if _, err := execx.Run(ctx, "", b.Git, "clone", b.Config.URL, b.Root); err != nil {
		return err
	}
	if _, err := execx.Run(ctx, b.Root, b.Git, "checkout", "-b", b.Config.Branch); err != nil {
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

func (b *Bus) Discover(gatewayID string) ([]RemoteTask, error) {
	pattern := filepath.Join(b.Root, "tasks", gatewayID, "*", "*", "task.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	result := make([]RemoteTask, 0, len(paths))
	for _, path := range paths {
		envelope, err := task.LoadEnvelope(path)
		if err != nil {
			return nil, fmt.Errorf("load remote task %s: %w", path, err)
		}
		if envelope.GatewayID != gatewayID {
			continue
		}
		result = append(result, RemoteTask{Envelope: envelope, Root: filepath.Dir(path)})
	}
	return result, nil
}

func (b *Bus) Materialize(remote RemoteTask, localRoot string, maxFile, maxAggregate int64) error {
	if _, err := os.Stat(localRoot); err == nil {
		return nil
	}
	temp := localRoot + ".tmp"
	if err := os.RemoveAll(temp); err != nil {
		return err
	}
	var total int64
	err := filepath.WalkDir(remote.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(remote.Root, path)
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
