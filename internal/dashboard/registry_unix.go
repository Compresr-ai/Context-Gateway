//go:build !windows

package dashboard

import "syscall"

// isPIDAlive returns true if a process with the given PID is still running.
// Uses kill(pid, 0) which checks process existence without delivering a signal.
// Returns true also for EPERM (process alive but no permission to signal it).
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
