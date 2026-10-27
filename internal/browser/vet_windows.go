//go:build windows

package browser

// vetCandidate accepts every candidate on Windows.
func vetCandidate(exe string) (string, bool) { return exe, true }
