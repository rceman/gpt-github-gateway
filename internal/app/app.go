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

const GatewayVersion = "0.3.0"

type App struct {
	Config *config.Config
	Layout config.Layout
	Bus    *bus.Manager
	Runner *task.Runner

	mu            sync.RWMutex
	lastError     string
	ready         bool
	startedAt     time.Time
	activeTasks   map[string]string
	projectErrors map[string]string
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
	BusBranch  string `json:"bus_branch"`
	Status     string `json:"status"`
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
		Config: cfg, Layout: layout,
		Bus: bus.NewManager(cfg, layout), Runner: task.NewRunner(cfg, layout),
		startedAt: time.Now().UTC(), activeTasks: map[string]string{}, projectErrors: map[string]string{},
	}, nil
}

func (a *App) Once(ctx context.Context) error {
	if err := a.Bus.Ensure(ctx); err != nil {
		a.setError(err)
		return err
	}
	for _, projectID := range config.ProjectIDs(a.Config) {
		if err := a.OnceProject(ctx, projectID); err != nil {
			a.setProjectError(projectID, err)
			a.setError(err)
		}
	}
	a.mu.Lock()
	a.ready = true
	a.mu.Unlock()
	return a.PublishControl(ctx, "online")
}

func (a *App) OnceProject(ctx context.Context, projectID string) error {
	projectBus, err := a.Bus.Project(projectID)
	if err != nil {
		return err
	}
	if err := projectBus.Sync(ctx); err != nil {
		return err
	}
	remoteTasks, err := projectBus.Discover(a.Config.Gateway.MaxTaskFileBytes, a.Config.Gateway.MaxTaskAggregateBytes)
	if err != nil {
		return err
	}
	for _, remote := range remoteTasks {
		localRoot := a.Layout.TaskRoot(projectID, remote.Envelope.TaskID)
		if err := projectBus.Materialize(remote, localRoot, a.Config.Gateway.MaxTaskFileBytes, a.Config.Gateway.MaxTaskAggregateBytes); err != nil {
			return err
		}
		if blocksProjectQueue(localRoot) {
			return nil
		}
		if skipLocalTask(localRoot, a.Config.Gateway.TaskExecutionMode) {
			continue
		}
		a.setActive(projectID, remote.Envelope.TaskID)
		_ = a.PublishControl(ctx, "online")
		outcome, runErr := a.Runner.Run(ctx, projectID, remote.Envelope.TaskID)
		if runErr != nil {
			status := task.Status{SchemaVersion: 1, TaskID: remote.Envelope.TaskID, GatewayID: a.Config.Gateway.ID, ProjectID: projectID, State: "failed", UpdatedAt: time.Now().UTC(), Message: runErr.Error()}
			_ = task.WriteStatus(localRoot, status)
			_ = projectBus.PublishAtomicResult(ctx, remote, status, nil, nil)
			a.clearActive(projectID)
			_ = a.PublishControl(ctx, "degraded")
			return runErr
		}
		if err := projectBus.PublishAtomicResult(ctx, remote, outcome.Status, outcome.Result, outcome.Response); err != nil {
			a.clearActive(projectID)
			return err
		}
		a.clearActive(projectID)
		_ = a.PublishControl(ctx, "online")
	}
	a.clearProjectError(projectID)
	return nil
}

func (a *App) Run(ctx context.Context) error {
	if err := a.Bus.Ensure(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	a.ready = true
	a.mu.Unlock()
	if err := a.PublishControl(ctx, "online"); err != nil {
		return err
	}

	var workers sync.WaitGroup
	for _, projectID := range config.ProjectIDs(a.Config) {
		projectID := projectID
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(time.Duration(a.Config.Gateway.PollIntervalSeconds) * time.Second)
			defer ticker.Stop()
			for {
				if err := a.OnceProject(ctx, projectID); err != nil && ctx.Err() == nil {
					a.setProjectError(projectID, err)
					a.setError(err)
					_ = a.PublishControl(context.Background(), "degraded")
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	heartbeat := time.NewTicker(time.Duration(a.Config.Bus.HeartbeatIntervalSeconds) * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			offlineCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = a.PublishControl(offlineCtx, "offline")
			cancel()
			workers.Wait()
			return ctx.Err()
		case <-heartbeat.C:
			status := "online"
			if a.Snapshot().LastError != "" {
				status = "degraded"
			}
			_ = a.PublishControl(ctx, status)
		}
	}
}

func (a *App) PublishControl(ctx context.Context, status string) error {
	now := time.Now().UTC()
	state := bus.GatewayState{
		SchemaVersion: 1, GatewayID: a.Config.Gateway.ID, GatewayVersion: GatewayVersion,
		Status: status, ExecutionMode: a.Config.Gateway.TaskExecutionMode,
		StartedAt: a.startedAt, HeartbeatAt: now,
		LeaseExpiresAt: now.Add(time.Duration(a.Config.Bus.LeaseDurationSeconds) * time.Second),
		Capabilities:   []string{"gpt-review-planner-v1.2.0", "apply-patch-pack", "atomic-task-bundle-v2", "atomic-result-v2", "structured-json-task", "automatic-airelay-dispatch", "isolated-git-worktree", "airelay-session", "multi-branch-bus-v1"},
		Runtime:        bus.GatewayRuntime{PID: os.Getpid(), Readiness: readiness(a.Snapshot().Ready), Doctor: doctorState(a.Snapshot().LastError)},
	}
	a.mu.RLock()
	if a.lastError != "" {
		value := a.lastError
		state.LastError = &value
	}
	for _, projectID := range config.ProjectIDs(a.Config) {
		project := a.Config.Projects[projectID]
		branch, _ := a.Config.ProjectBranch(projectID)
		projectStatus := "ready"
		if project.ResumeSessionID == "" {
			projectStatus = "configuration_required"
		}
		if message := a.projectErrors[projectID]; message != "" {
			projectStatus = "degraded"
		}
		var active *string
		if id := a.activeTasks[projectID]; id != "" {
			copy := id
			active = &copy
		}
		state.Projects = append(state.Projects, bus.GatewayProject{ProjectID: projectID, Repository: project.Repository, DefaultBranch: project.DefaultBranch, Branch: branch, SessionKey: project.SessionKey, Status: projectStatus, ActiveTaskID: active})
	}
	a.mu.RUnlock()
	return a.Bus.PublishControl(ctx, state)
}

func readiness(value bool) string {
	if value {
		return "ready"
	}
	return "not_ready"
}
func doctorState(lastError string) string {
	if lastError == "" {
		return "passed"
	}
	return "failed"
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
	a.mu.RLock()
	for _, id := range config.ProjectIDs(a.Config) {
		project := a.Config.Projects[id]
		branch, _ := a.Config.ProjectBranch(id)
		status := "ready"
		if project.ResumeSessionID == "" {
			status = "configuration_required"
		}
		if a.projectErrors[id] != "" {
			status = "degraded"
		}
		projects = append(projects, ProjectView{ProjectID: id, Repository: project.Repository, SessionKey: project.SessionKey, BusBranch: branch, Status: status})
	}
	result := Snapshot{SchemaVersion: 1, GatewayID: a.Config.Gateway.ID, Ready: a.ready, LastError: a.lastError, Projects: projects}
	a.mu.RUnlock()
	result.Tasks = a.ListTasks()
	return result
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
			continue
		}
		if project.ResumeSessionID == "" {
			checks = append(checks, projectID+": configuration_required: no resume_session_id")
			continue
		}
		checks = append(checks, projectID+": ok")
	}
	return checks
}

func (a *App) setError(err error) { a.mu.Lock(); a.lastError = err.Error(); a.mu.Unlock() }
func (a *App) setProjectError(id string, err error) {
	a.mu.Lock()
	a.projectErrors[id] = err.Error()
	a.mu.Unlock()
}
func (a *App) clearProjectError(id string) {
	a.mu.Lock()
	delete(a.projectErrors, id)
	if len(a.projectErrors) == 0 {
		a.lastError = ""
	}
	a.mu.Unlock()
}
func (a *App) setActive(projectID, taskID string) {
	a.mu.Lock()
	a.activeTasks[projectID] = taskID
	a.mu.Unlock()
}
func (a *App) clearActive(projectID string) {
	a.mu.Lock()
	delete(a.activeTasks, projectID)
	a.mu.Unlock()
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
		return !(regularFile(filepath.Join(root, "agent-result.json")) && regularFile(filepath.Join(root, task.AgentResponseFilename)))
	default:
		return false
	}
}

func blocksProjectQueue(root string) bool {
	status, err := task.ReadStatus(root)
	if err != nil {
		return false
	}
	switch status.State {
	case "preparing_worktree", "applying_patch", "patch_repair_required", "agent_running":
		return !(regularFile(filepath.Join(root, "agent-result.json")) && regularFile(filepath.Join(root, task.AgentResponseFilename)))
	default:
		return false
	}
}
func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Retain JSON import as part of the stable application result surface.
var _ = json.Marshal
