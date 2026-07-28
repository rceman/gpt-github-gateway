package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/bus"
	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/task"
)

type App struct {
	Config *config.Config
	Layout config.Layout
	Bus    *bus.Bus
	Runner *task.Runner

	mu        sync.RWMutex
	lastError string
	ready     bool
}

type Snapshot struct {
	SchemaVersion int           `json:"schema_version"`
	GatewayID     string        `json:"gateway_id"`
	Ready         bool          `json:"ready"`
	LastError     string        `json:"last_error,omitempty"`
	Projects      []ProjectView `json:"projects"`
	Tasks         []TaskView    `json:"tasks"`
}

type ProjectView struct {
	ProjectID  string `json:"project_id"`
	Repository string `json:"repository"`
	SessionKey string `json:"session_key"`
}

type TaskView struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
}

func Open(configPath string) (*App, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	layout, err := cfg.Layout(configPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(layout.Root, 0o700); err != nil {
		return nil, err
	}
	return &App{
		Config: cfg,
		Layout: layout,
		Bus:    bus.New(cfg.Bus, layout.BusDir),
		Runner: task.NewRunner(cfg, layout),
	}, nil
}

func (a *App) Once(ctx context.Context) error {
	if err := a.Bus.Sync(ctx); err != nil {
		a.setError(err)
		return err
	}
	remoteTasks, err := a.Bus.Discover(
		a.Config.Gateway.ID,
		a.Config.Gateway.MaxTaskFileBytes,
		a.Config.Gateway.MaxTaskAggregateBytes,
	)
	if err != nil {
		a.setError(err)
		return err
	}
	for _, remote := range remoteTasks {
		projectID := remote.Envelope.ProjectID
		if _, exists := a.Config.Projects[projectID]; !exists {
			continue
		}
		localRoot := a.Layout.TaskRoot(projectID, remote.Envelope.TaskID)
		if err := a.Bus.Materialize(remote, localRoot, a.Config.Gateway.MaxTaskFileBytes, a.Config.Gateway.MaxTaskAggregateBytes); err != nil {
			a.setError(err)
			return err
		}
		if skipLocalTask(localRoot, a.Config.Gateway.TaskExecutionMode) {
			continue
		}
		outcome, err := a.Runner.Run(ctx, projectID, remote.Envelope.TaskID)
		if err != nil {
			status := task.Status{
				SchemaVersion: 1,
				TaskID:        remote.Envelope.TaskID,
				GatewayID:     a.Config.Gateway.ID,
				ProjectID:     projectID,
				State:         "failed",
				UpdatedAt:     time.Now().UTC(),
				Message:       err.Error(),
			}
			_ = task.WriteStatus(localRoot, status)
			_ = a.publishRemoteTask(ctx, remote, status, nil, nil)
			a.setError(err)
			continue
		}
		if err := a.publishRemoteTask(ctx, remote, outcome.Status, outcome.Result, outcome.Response); err != nil {
			a.setError(err)
			return err
		}
	}
	a.mu.Lock()
	a.ready = true
	a.lastError = ""
	a.mu.Unlock()
	return nil
}

func (a *App) PublishRegistry(ctx context.Context) error {
	projects := config.ProjectIDs(a.Config)
	state := bus.GatewayState{
		SchemaVersion: 1,
		GatewayID:     a.Config.Gateway.ID,
		Status:        "online",
		UpdatedAt:     time.Now().UTC(),
		Projects:      projects,
		Capabilities: []string{
			"gpt-review-planner-v1.2.0",
			"apply-patch-pack",
			"atomic-task-bundle-v2",
			"automatic-airelay-dispatch",
			"isolated-git-worktree",
			"airelay-session",
		},
	}
	if err := a.Bus.PublishGateway(ctx, a.Config.Gateway.ID, state); err != nil {
		return err
	}
	for _, projectID := range projects {
		project := a.Config.Projects[projectID]
		projectState := bus.ProjectState{
			SchemaVersion: 1,
			GatewayID:     a.Config.Gateway.ID,
			ProjectID:     projectID,
			Repository:    project.Repository,
			SessionKey:    project.SessionKey,
			UpdatedAt:     time.Now().UTC(),
		}
		if err := a.Bus.PublishProject(ctx, a.Config.Gateway.ID, projectID, projectState); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) Run(ctx context.Context) error {
	if err := a.Bus.Sync(ctx); err != nil {
		return err
	}
	if err := a.PublishRegistry(ctx); err != nil {
		return err
	}
	poll := time.NewTicker(time.Duration(a.Config.Gateway.PollIntervalSeconds) * time.Second)
	defer poll.Stop()
	registry := time.NewTicker(5 * time.Minute)
	defer registry.Stop()
	if err := a.Once(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
			_ = a.Once(ctx)
		case <-registry.C:
			_ = a.PublishRegistry(ctx)
		}
	}
}

func (a *App) Approve(projectID, taskID string) error {
	if _, exists := a.Config.Projects[projectID]; !exists {
		return fmt.Errorf("project %s is not configured", projectID)
	}
	return task.WriteApproval(a.Layout.TaskRoot(projectID, taskID), taskID, "approved", "")
}

func (a *App) Reject(projectID, taskID, reason string) error {
	if _, exists := a.Config.Projects[projectID]; !exists {
		return fmt.Errorf("project %s is not configured", projectID)
	}
	return task.WriteApproval(a.Layout.TaskRoot(projectID, taskID), taskID, "rejected", reason)
}

func (a *App) Rollback(ctx context.Context, projectID, taskID string) error {
	return a.Runner.Rollback(ctx, projectID, taskID)
}

func (a *App) Snapshot() Snapshot {
	projects := make([]ProjectView, 0, len(a.Config.Projects))
	for _, id := range config.ProjectIDs(a.Config) {
		project := a.Config.Projects[id]
		projects = append(projects, ProjectView{ProjectID: id, Repository: project.Repository, SessionKey: project.SessionKey})
	}
	tasks := a.ListTasks()
	a.mu.RLock()
	defer a.mu.RUnlock()
	return Snapshot{
		SchemaVersion: 1,
		GatewayID:     a.Config.Gateway.ID,
		Ready:         a.ready,
		LastError:     a.lastError,
		Projects:      projects,
		Tasks:         tasks,
	}
}

func (a *App) ListTasks() []TaskView {
	var result []TaskView
	for _, projectID := range config.ProjectIDs(a.Config) {
		root := filepath.Join(a.Layout.ProjectRoot(projectID), "tasks")
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			view := TaskView{ProjectID: projectID, TaskID: entry.Name(), State: "submitted"}
			if status, err := task.ReadStatus(filepath.Join(root, entry.Name())); err == nil {
				view.State = status.State
				view.Message = status.Message
			}
			result = append(result, view)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ProjectID == result[j].ProjectID {
			return result[i].TaskID < result[j].TaskID
		}
		return result[i].ProjectID < result[j].ProjectID
	})
	return result
}

func (a *App) Doctor(ctx context.Context) []string {
	checks := []string{}
	if err := a.Bus.Ensure(ctx); err != nil {
		checks = append(checks, "bus: failed: "+err.Error())
	} else {
		checks = append(checks, "bus: ok")
	}
	if err := a.Runner.Airelay.Doctor(ctx); err != nil {
		checks = append(checks, "airelay: failed: "+err.Error())
	} else {
		checks = append(checks, "airelay: ok")
	}
	for _, projectID := range config.ProjectIDs(a.Config) {
		project := a.Config.Projects[projectID]
		if err := a.Runner.Git.VerifyRepository(ctx, project.Path, project.Repository); err != nil {
			checks = append(checks, projectID+": failed: "+err.Error())
		} else {
			checks = append(checks, projectID+": ok")
		}
	}
	return checks
}

func (a *App) publishRemoteTask(ctx context.Context, remote bus.RemoteTask, status task.Status, result *task.AgentResult, response []byte) error {
	if remote.ProtocolVersion == 2 {
		return a.Bus.PublishAtomicResult(ctx, remote, status, result, response)
	}
	return a.publishTask(ctx, remote.Envelope.ProjectID, remote.Envelope.TaskID, status, result, response)
}

func (a *App) publishTask(ctx context.Context, projectID, taskID string, status task.Status, result *task.AgentResult, response []byte) error {
	files := map[string][]byte{}
	statusData, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	files["status.json"] = append(statusData, '\n')
	if result != nil {
		resultData, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		files["result.json"] = append(resultData, '\n')
	}
	if len(response) > 0 {
		files["response.md"] = response
	}
	return a.Bus.PublishTask(ctx, a.Config.Gateway.ID, projectID, taskID, files)
}

func (a *App) setError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastError = err.Error()
}

func skipLocalTask(root, executionMode string) bool {
	status, err := task.ReadStatus(root)
	if err != nil {
		return false
	}
	switch status.State {
	case "succeeded", "failed", "needs_gpt_revision", "rejected", "rolled_back", "agent_timeout", "agent_unavailable", "execution_disabled", "superseded":
		return true
	case "waiting_for_approval":
		if executionMode == config.ExecutionModeAuto {
			return false
		}
		approval, err := task.ReadApproval(root, status.TaskID)
		return err == nil && approval.Decision == "pending"
	case "agent_running":
		resultReady := regularFile(filepath.Join(root, "agent-result.json"))
		responseReady := regularFile(filepath.Join(root, task.AgentResponseFilename))
		return !(resultReady && responseReady)
	default:
		return false
	}
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
