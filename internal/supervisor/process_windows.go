//go:build windows

package supervisor

import "os"

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(os.Signal(nil)) == nil
}

func terminate(process *os.Process) error {
	return process.Kill()
}
