//go:build !darwin && !windows

package browser

import "os"

// HasDisplay reports whether an interactive browser window can open: an X11
// or Wayland display must be set.
func HasDisplay() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}
