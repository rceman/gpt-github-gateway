package airelay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/execx"
)

type Client struct {
	Binary string
}

type SessionStatus struct {
	SessionID           string `json:"sessionId"`
	Profile             string `json:"profile"`
	SessionKey          string `json:"sessionKey"`
	ControllerReachable bool   `json:"controllerReachable"`
	State               string `json:"state"`
}

func New(binary string) Client {
	return Client{Binary: binary}
}

func (c Client) Doctor(ctx context.Context) error {
	_, err := execx.Run(ctx, "", c.Binary, "doctor")
	return err
}

func (c Client) Status(ctx context.Context, key string) (SessionStatus, error) {
	result, err := execx.Run(ctx, "", c.Binary, "session-status", key, "--json", "--no-warn")
	if err != nil {
		return SessionStatus{}, err
	}
	var status SessionStatus
	if err := json.Unmarshal([]byte(result.Stdout), &status); err != nil {
		return SessionStatus{}, fmt.Errorf("decode airelay session status: %w", err)
	}
	if !status.ControllerReachable {
		return status, fmt.Errorf("airelay session %s controller is not reachable", key)
	}
	return status, nil
}

func (c Client) EnsureSession(ctx context.Context, project config.ProjectConfig, cwd, logPath string) error {
	if _, err := c.Status(ctx, project.SessionKey); err == nil {
		return nil
	}
	if strings.TrimSpace(project.ResumeSessionID) == "" {
		return fmt.Errorf("airelay session %s is not active and no local resume_session_id is configured", project.SessionKey)
	}
	args := []string{"start", project.AirelayProfile, "--key", project.SessionKey, "--", "resume", project.ResumeSessionID}
	args = append(args, project.LaunchArgs...)
	if _, err := execx.StartDetached(cwd, logPath, c.Binary, args...); err != nil {
		return err
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Status(ctx, project.SessionKey); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("airelay session %s did not become promptable", project.SessionKey)
}

func (c Client) Prompt(ctx context.Context, key, text string) error {
	if strings.ContainsAny(text, "\x00\r\n") {
		return fmt.Errorf("airelay prompt must be one line")
	}
	_, err := execx.Run(ctx, "", c.Binary, "prompt", key, "--text", text, "--no-sender")
	return err
}

func WaitForResult(ctx context.Context, resultPath, responsePath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resultInfo, resultErr := os.Stat(resultPath)
		responseInfo, responseErr := os.Stat(responsePath)
		if resultErr == nil && responseErr == nil && resultInfo.Mode().IsRegular() && responseInfo.Mode().IsRegular() {
			return nil
		}
		if resultErr != nil && !os.IsNotExist(resultErr) {
			return fmt.Errorf("stat agent result: %w", resultErr)
		}
		if responseErr != nil && !os.IsNotExist(responseErr) {
			return fmt.Errorf("stat agent response: %w", responseErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("agent result timed out after %s", timeout)
}

func AgentLogPath(taskRoot string) string {
	return filepath.Join(taskRoot, "airelay-session.log")
}
