//go:build !windows

package supervisor

import (
	"os"
	"syscall"
)

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func terminate(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}
