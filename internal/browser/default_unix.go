//go:build !darwin && !windows

package browser

import (
	"os/exec"
	"strings"
)

var detectDefault = func() defaultBrowser {
	out, err := exec.Command("xdg-settings", "get", "default-web-browser").Output()
	if err != nil {
		return defaultBrowser{}
	}
	desktop := strings.TrimSpace(string(out))
	if desktop == "" {
		return defaultBrowser{}
	}
	kb, bins, ok := desktopBrowser(desktop)
	if !ok {
		return defaultBrowser{Name: strings.TrimSuffix(desktop, ".desktop")}
	}
	d := defaultBrowser{Name: kb.name, Chromium: kb.chromium}
	for _, b := range bins {
		if p, err := exec.LookPath(b); err == nil && !isSnap(p) {
			d.Exe = p
			break
		}
	}
	return d
}
