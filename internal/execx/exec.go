package execx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func Run(ctx context.Context, cwd string, name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if !strings.Contains(err.Error(), "executable file not found") && errorAs(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, fmt.Errorf("%s %s exited %d: %s", name, strings.Join(args, " "), result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	result.ExitCode = -1
	return result, fmt.Errorf("run %s: %w", name, err)
}

func StartDetached(cwd, logPath, name string, args ...string) (*os.Process, error) {
	if err := os.MkdirAll(filepathDir(logPath), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	_ = logFile.Close()
	if err := cmd.Process.Release(); err != nil {
		return nil, fmt.Errorf("release %s process: %w", name, err)
	}
	return cmd.Process, nil
}

func CopyStream(dst io.Writer, src io.Reader, limit int64) error {
	written, err := io.CopyN(dst, src, limit+1)
	if err != nil && err != io.EOF {
		return err
	}
	if written > limit {
		return fmt.Errorf("stream exceeds %d bytes", limit)
	}
	return nil
}

func filepathDir(path string) string {
	index := strings.LastIndexAny(path, `/\\`)
	if index < 0 {
		return "."
	}
	return path[:index]
}

func errorAs(err error, target any) bool {
	return errorsAs(err, target)
}
