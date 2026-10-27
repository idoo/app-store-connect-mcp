//go:build darwin || windows

package browser

// HasDisplay reports whether an interactive browser window can open; always
// true on macOS and Windows.
func HasDisplay() bool { return true }
