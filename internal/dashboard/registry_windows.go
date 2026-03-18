//go:build windows

package dashboard

import "os"

// isPIDAlive returns true if a process with the given PID is still running.
// On Windows, FindProcess always succeeds for any PID, so this is a best-effort check.
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}
