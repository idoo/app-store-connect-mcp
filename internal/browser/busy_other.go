//go:build !windows

package browser

// isBusy is always false: unix deletes and renames do not fail on open files.
func isBusy(error) bool { return false }
