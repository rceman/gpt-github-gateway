package task

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	AgentResultFilename  = "agent-result.json"
	CompletionFilename   = ".completion.json"
	CompleteTaskFilename = "complete-task"
)

type Completion struct {
	SchemaVersion int       `json:"schema_version"`
	TaskID        string    `json:"task_id"`
	ResultSHA256  string    `json:"result_sha256"`
	CompletedAt   time.Time `json:"completed_at"`
}

func ResultPath(taskRoot string) string       { return filepath.Join(taskRoot, AgentResultFilename) }
func CompletionPath(taskRoot string) string   { return filepath.Join(taskRoot, CompletionFilename) }
func CompleteTaskPath(taskRoot string) string { return filepath.Join(taskRoot, CompleteTaskFilename) }

func WriteCompleteTaskScript(path, binary, configPath, projectID, taskID string) error {
	content := "#!/usr/bin/env bash\nset -euo pipefail\nexec " + shellQuote(binary) + " --config " + shellQuote(configPath) + " task complete " + shellQuote(projectID) + " " + shellQuote(taskID) + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o700); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func WriteCompletion(taskRoot, taskID string) error {
	resultPath := ResultPath(taskRoot)
	data, err := os.ReadFile(resultPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	return writeJSONAtomic(CompletionPath(taskRoot), Completion{SchemaVersion: 1, TaskID: taskID, ResultSHA256: hex.EncodeToString(sum[:]), CompletedAt: time.Now().UTC()})
}

func LoadCompletion(taskRoot, taskID string) (Completion, error) {
	var c Completion
	if err := decodeStrict(CompletionPath(taskRoot), &c); err != nil {
		return Completion{}, err
	}
	if c.SchemaVersion != 1 || c.TaskID != taskID {
		return Completion{}, fmt.Errorf("invalid completion identity")
	}
	data, err := os.ReadFile(ResultPath(taskRoot))
	if err != nil {
		return Completion{}, err
	}
	sum := sha256.Sum256(data)
	if c.ResultSHA256 != hex.EncodeToString(sum[:]) {
		return Completion{}, fmt.Errorf("completion result digest mismatch")
	}
	return c, nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
