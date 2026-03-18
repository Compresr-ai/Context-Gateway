//go:build !windows

package dashboard

import (
	"os"
	"syscall"

	"github.com/rs/zerolog/log"
)

// lockFile returns the path to the lock file for cross-process synchronization.
func lockFile() string {
	return registryFile() + ".lock"
}

// withFileLock executes fn while holding an exclusive file lock.
func withFileLock(fn func()) {
	registryMu.Lock()
	defer registryMu.Unlock()

	// Create lock file for cross-process synchronization
	lf, err := os.OpenFile(lockFile(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		log.Warn().Err(err).Msg("dashboard: failed to create lock file, proceeding without lock")
		fn()
		return
	}
	defer lf.Close()

	fd := lf.Fd()
	const maxIntVal = uintptr(^uint(0) >> 1)
	if fd > maxIntVal {
		log.Warn().Msg("dashboard: file descriptor value too large, proceeding without lock")
		fn()
		return
	}

	// Acquire exclusive lock (blocking)
	if err := syscall.Flock(int(fd), syscall.LOCK_EX); err != nil {
		log.Warn().Err(err).Msg("dashboard: failed to acquire file lock, proceeding without lock")
		fn()
		return
	}
	defer syscall.Flock(int(fd), syscall.LOCK_UN) //nolint:errcheck

	fn()
}
