//go:build windows

package dashboard

// withFileLock executes fn while holding an in-process mutex lock.
// Windows does not support syscall.Flock, so cross-process locking is skipped.
func withFileLock(fn func()) {
	registryMu.Lock()
	defer registryMu.Unlock()
	fn()
}
