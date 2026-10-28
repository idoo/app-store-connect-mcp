package browser

import (
	"os"
	"path/filepath"
	"time"
)

// RemoveProfile terminates every process still using dir as its
// --user-data-dir (an interactive window left open, a headless browser
// orphaned by a killed server), so no survivor rewrites its cookie database
// or holds its SingletonLock, then deletes dir. On Windows a just-killed
// browser (or a virus scanner) can hold files open for a moment, so the
// delete is retried for about 2 s.
func RemoveProfile(dir string) error {
	if dir == "" {
		return nil
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	killProfile(dir)
	return retryBusy(func() error { return os.RemoveAll(dir) })
}

// busyRetryFor bounds retryBusy.
const busyRetryFor = 2 * time.Second

// retryBusy runs fn, retrying while it fails with a transient Windows
// sharing/access error (never elsewhere: isBusy is false off Windows).
func retryBusy(fn func() error) error {
	return retryWhile(fn, isBusy, busyRetryFor, 100*time.Millisecond)
}

func retryWhile(fn func() error, retry func(error) bool, total, step time.Duration) error {
	end := time.Now().Add(total)
	for {
		err := fn()
		if err == nil || !retry(err) || time.Now().After(end) {
			return err
		}
		time.Sleep(step)
	}
}
