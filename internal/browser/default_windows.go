//go:build windows

package browser

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

var detectDefault = func() defaultBrowser {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice`, registry.QUERY_VALUE)
	if err != nil {
		return defaultBrowser{}
	}
	progID, _, err := k.GetStringValue("ProgId")
	k.Close()
	if err != nil || progID == "" {
		return defaultBrowser{}
	}
	kb, ok := progIDBrowser(progID)
	if !ok {
		return defaultBrowser{Name: progID}
	}
	d := defaultBrowser{Name: kb.name, Chromium: kb.chromium}
	if !kb.chromium {
		return d
	}
	ck, err := registry.OpenKey(registry.CLASSES_ROOT, progID+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return d
	}
	cmd, _, err := ck.GetStringValue("")
	ck.Close()
	if err != nil {
		return d
	}
	exe, err := registry.ExpandString(commandExe(cmd))
	if err != nil {
		return d
	}
	if fi, err := os.Stat(exe); err == nil && !fi.IsDir() {
		d.Exe = exe
	}
	return d
}
