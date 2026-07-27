package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/execx"
)

func Start(layout config.Layout) error {
	if running, pid := Status(layout); running {
		return fmt.Errorf("gateway is already running with pid %d", pid)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	process, err := execx.StartDetached("", layout.LogFile, executable, "--config", layout.ConfigPath, "run")
	if err != nil {
		return err
	}
	if err := writePID(layout.PIDFile, process.Pid); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if running, _ := Status(layout); running {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("gateway process did not become visible")
}

func Stop(layout config.Layout) error {
	running, pid := Status(layout)
	if !running {
		_ = os.Remove(layout.PIDFile)
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := terminate(process); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if alive := processAlive(pid); !alive {
			_ = os.Remove(layout.PIDFile)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := process.Kill(); err != nil {
		return err
	}
	_ = os.Remove(layout.PIDFile)
	return nil
}

func Status(layout config.Layout) (bool, int) {
	data, err := os.ReadFile(layout.PIDFile)
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false, 0
	}
	if !processAlive(pid) {
		_ = os.Remove(layout.PIDFile)
		return false, pid
	}
	return true, pid
}

func Acquire(layout config.Layout) (func(), error) {
	if err := os.MkdirAll(layout.Root, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(layout.LockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if data, readErr := os.ReadFile(layout.LockFile); readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && !processAlive(pid) {
				_ = os.Remove(layout.LockFile)
				file, err = os.OpenFile(layout.LockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("another gateway process may be running: %w", err)
	}
	_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
	_ = file.Close()
	if err := writePID(layout.PIDFile, os.Getpid()); err != nil {
		_ = os.Remove(layout.LockFile)
		return nil, err
	}
	return func() {
		_ = os.Remove(layout.LockFile)
		_ = os.Remove(layout.PIDFile)
	}, nil
}

func writePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
