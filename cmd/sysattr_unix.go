//go:build !windows

package main

import (
	"os"
	"syscall"
)

// getSysProcAttr returns syscall attributes for detaching daemon process.
// On Unix, sets Setsid to create a new session.
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true,
	}
}

// getShutdownSignals returns signals to listen for graceful shutdown.
func getShutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

// terminateProcess sends SIGTERM to gracefully stop a process.
func terminateProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

// isProcessRunning checks if a process is still alive.
func isProcessRunning(p *os.Process) bool {
	return p.Signal(syscall.Signal(0)) == nil
}
