package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/app"
	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/server"
	"github.com/rceman/gpt-github-gateway/internal/supervisor"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	configPath, remaining, err := parseGlobal(arguments)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		printHelp()
		return nil
	}
	command := remaining[0]
	args := remaining[1:]
	switch command {
	case "help", "--help", "-h":
		printHelp()
		return nil
	case "version", "--version", "-v":
		fmt.Println("gpt-github-gateway", version)
		return nil
	case "init":
		return initConfig(configPath, args)
	case "project":
		return projectCommand(configPath, args)
	}
	application, err := app.Open(configPath)
	if err != nil {
		return err
	}
	switch command {
	case "once":
		release, err := supervisor.Acquire(application.Layout)
		if err != nil {
			return err
		}
		defer release()
		return application.Once(context.Background())
	case "run":
		return runDaemon(application)
	case "start":
		return supervisor.Start(application.Layout)
	case "stop":
		return supervisor.Stop(application.Layout)
	case "status":
		running, pid := supervisor.Status(application.Layout)
		if running {
			fmt.Printf("running pid=%d\n", pid)
		} else {
			fmt.Println("stopped")
		}
		return nil
	case "doctor":
		for _, check := range application.Doctor(context.Background()) {
			fmt.Println(check)
		}
		return nil
	case "projects":
		for _, project := range application.Snapshot().Projects {
			fmt.Printf("%s\t%s\t%s\n", project.ProjectID, project.Repository, project.SessionKey)
		}
		return nil
	case "tasks":
		for _, item := range application.ListTasks() {
			fmt.Printf("%s\t%s\t%s\t%s\n", item.ProjectID, item.TaskID, item.State, item.Message)
		}
		return nil
	case "task":
		return taskCommand(application, args)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func parseGlobal(arguments []string) (string, []string, error) {
	path, err := config.DefaultConfigPath()
	if err != nil {
		return "", nil, err
	}
	if len(arguments) >= 2 && arguments[0] == "--config" {
		absolute, err := filepath.Abs(arguments[1])
		if err != nil {
			return "", nil, err
		}
		return absolute, arguments[2:], nil
	}
	return path, arguments, nil
}

func initConfig(path string, args []string) error {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	gatewayID := set.String("gateway", "", "stable gateway ID")
	busRepository := set.String("bus-repository", "rceman/typer", "GitHub repository identity")
	busURL := set.String("bus-url", "git@github.com:rceman/typer.git", "Git clone URL")
	busBranch := set.String("bus-branch", "ai-workspace-bus", "Git bus branch")
	force := set.Bool("force", false, "replace existing config")
	if err := set.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*gatewayID) == "" {
		return errors.New("--gateway is required")
	}
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("config already exists: %s", path)
	}
	cfg := &config.Config{
		SchemaVersion: 1,
		Gateway: config.GatewayConfig{
			ID:                  *gatewayID,
			PollIntervalSeconds: 10,
			AgentTimeoutSeconds: 7200,
			AllowPatchRepair:    true,
		},
		Bus: config.BusConfig{
			Repository: *busRepository,
			URL:        *busURL,
			Branch:     *busBranch,
		},
		Server:   config.ServerConfig{Listen: "127.0.0.1:8787"},
		Airelay:  config.AirelayConfig{Binary: "airelay"},
		Projects: map[string]config.ProjectConfig{},
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Println("created", path)
	return nil
}

func projectCommand(configPath string, args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: gpt-github-gateway project add [options]")
	}
	set := flag.NewFlagSet("project add", flag.ContinueOnError)
	id := set.String("id", "", "project ID")
	path := set.String("path", "", "absolute project checkout path")
	repository := set.String("repository", "", "owner/name repository identity")
	branch := set.String("branch", "main", "default target branch")
	profile := set.String("airelay-profile", "codex", "local Airelay profile")
	sessionKey := set.String("session-key", "", "stable Airelay session key")
	resumeSession := set.String("resume-session", "", "Codex session ID to resume")
	noBypass := set.Bool("no-dangerous-bypass", false, "do not add the Codex bypass launch flag")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	if *id == "" || *path == "" || *repository == "" {
		return errors.New("--id, --path, and --repository are required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	key := *sessionKey
	if key == "" {
		key = *id + "_master"
	}
	launchArgs := []string{}
	if !*noBypass {
		launchArgs = append(launchArgs, "--dangerously-bypass-approvals-and-sandbox")
	}
	cfg.Projects[*id] = config.ProjectConfig{
		Path:            absolute,
		Repository:      *repository,
		DefaultBranch:   *branch,
		AirelayProfile:  *profile,
		SessionKey:      key,
		ResumeSessionID: *resumeSession,
		LaunchArgs:      launchArgs,
	}
	if err := config.Save(configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("added %s with session key %s\n", *id, key)
	return nil
}

func taskCommand(application *app.App, args []string) error {
	if len(args) < 3 {
		return errors.New("usage: gpt-github-gateway task <approve|reject|rollback> <project_id> <task_id> [reason]")
	}
	action, projectID, taskID := args[0], args[1], args[2]
	switch action {
	case "approve":
		return application.Approve(projectID, taskID)
	case "reject":
		reason := "rejected by owner"
		if len(args) > 3 {
			reason = strings.Join(args[3:], " ")
		}
		return application.Reject(projectID, taskID, reason)
	case "rollback":
		return application.Rollback(context.Background(), projectID, taskID)
	default:
		return fmt.Errorf("unsupported task action %q", action)
	}
}

func runDaemon(application *app.App) error {
	release, err := supervisor.Acquire(application.Layout)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	localServer := server.New(application, application.Config.Server.Listen)
	errorsChannel := make(chan error, 2)
	go func() {
		errorsChannel <- localServer.ListenAndServe()
	}()
	go func() {
		errorsChannel <- application.Run(ctx)
	}()
	select {
	case <-ctx.Done():
	case err := <-errorsChannel:
		if err != nil && !errors.Is(err, context.Canceled) {
			cancel()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = localServer.Shutdown(shutdownCtx)
			return err
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	return localServer.Shutdown(shutdownCtx)
}

func printHelp() {
	fmt.Print(`gpt-github-gateway - GPT Review Planner execution gateway

Usage:
  gpt-github-gateway [--config PATH] <command>

Commands:
  init
  project add
  projects
  once
  run
  start
  stop
  status
  doctor
  tasks
  task approve <project_id> <task_id>
  task reject <project_id> <task_id> [reason]
  task rollback <project_id> <task_id>
  version
`)
}
