// Package notify posts desktop notifications to the human.
package notify

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Desktop shows a notification with title and body: osascript on macOS,
// notify-send on Linux when installed, nothing elsewhere. Failures are
// ignored: a notification is a courtesy, never a requirement.
func Desktop(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		script := "display notification " + appleScriptString(body) + " with title " + appleScriptString(title)
		_ = exec.CommandContext(ctx, "osascript", "-e", script).Run() //nolint:gosec // fixed program; text is a quoted AppleScript literal
	case "linux":
		if path, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.CommandContext(ctx, path, title, body).Run() //nolint:gosec // notify-send from PATH; text is plain args
		}
	}
}

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
