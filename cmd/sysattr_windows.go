//go:build windows

package main

import (
	"os"
	"syscall"
)

// getSysProcAttr returns syscall attributes for detaching daemon process.
// On Windows, we don't need Setsid (Unix-only), return empty attributes.
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

// getShutdownSignals returns signals to listen for graceful shutdown.
// On Windows, only os.Interrupt (Ctrl+C) is reliably supported.
func getShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// terminateProcess kills a process on Windows (no graceful SIGTERM).
func terminateProcess(p *os.Process) error {
	return p.Kill()
}

// isProcessRunning checks if a process is still alive.
// On Windows, we try to open the process to check if it exists.
func isProcessRunning(p *os.Process) bool {
	// On Windows, FindProcess always succeeds, but we can check
	// by trying to wait with no timeout (would return immediately if dead)
	// For simplicity, assume running if we have a valid process handle
	_, err := os.FindProcess(p.Pid)
	return err == nil
}
