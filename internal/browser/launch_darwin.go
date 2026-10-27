//go:build darwin

package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// canLaunchApp: on macOS the interactive window is started through
// LaunchServices (`open -n -a`) so it comes to the front; a process we exec
// ourselves stays behind the terminal under cooperative activation.
const canLaunchApp = true

const portFileWait = 15 * time.Second

// launchApp starts the bundle with the profile and returns the browser's
// DevTools websocket URL. A browser still holding the dedicated profile (a
// window left behind by an interrupted sign-in; LaunchServices parents it to
// launchd, so it can't be told from a live one) is closed first: a second
// instance would hand off to it and never write a DevTools port.
func launchApp(ctx context.Context, bundle, profile string, url string) (string, error) {
	killProfile(profile)
	portFile := filepath.Join(profile, "DevToolsActivePort")
	if err := os.Remove(portFile); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("browser: remove stale DevToolsActivePort: %w", err)
	}
	args := []string{
		"-n", "-a", bundle, "--args",
		"--user-data-dir=" + profile,
		"--remote-debugging-port=0",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-blink-features=AutomationControlled",
	}
	// Same cookie encryption as the headless exec launch, or silent renewal
	// could not decrypt this profile's cookies.
	args = append(args, cookieKeyArgs("darwin", bundle)...)
	args = append(args, "--window-size="+windowSize, url)
	if out, err := exec.CommandContext(ctx, "open", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed program, argv array (no shell); bundle is the browser resolved by Find
		return "", fmt.Errorf("browser: open %s: %v: %s", bundle, err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(portFileWait)
	for {
		if b, err := os.ReadFile(portFile); err == nil { //nolint:gosec // fixed file name inside our own 0700 profile dir
			if ws, ok := parseDevToolsActivePort(b); ok {
				return ws, nil
			}
		}
		if time.Now().After(deadline) {
			killProfile(profile)
			return "", fmt.Errorf("browser: %s opened no DevTools port within %s", filepath.Base(bundle), portFileWait)
		}
		select {
		case <-ctx.Done():
			killProfile(profile)
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// tmExcludeAttr marks an item as excluded from Time Machine; its value is a
// binary plist of the string "com.apple.backupd". This is what
// `tmutil addexclusion` writes, minus the seconds that tool can take.
const tmExcludeAttr = "com.apple.metadata:com_apple_backup_excludeItem"

var tmExcludeValue = []byte("bplist00_\x10\x11com.apple.backupd\x08\x00\x00\x00\x00\x00\x00\x01\x01\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x1c")

// excludeFromBackup keeps the signed-in profile out of Time Machine. Best
// effort: errors are ignored.
func excludeFromBackup(dir string) {
	_ = unix.Setxattr(dir, tmExcludeAttr, tmExcludeValue, 0)
}
