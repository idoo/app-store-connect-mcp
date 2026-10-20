package metadata

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// TestFromFastlaneSkipsSymlinks reproduces L3: release_notes.txt linked to
// the project's .p8 put the private key into the planned What's New.
func TestFromFastlaneSkipsSymlinks(t *testing.T) {
	files := tooltest.NewFakeFiles(t)
	writeFile(t, files.Root, "AuthKey_K1.p8", "-----BEGIN PRIVATE KEY-----\nMIGTAgEA\n-----END PRIVATE KEY-----\n")
	writeFile(t, files.Root, "metadata/en-US/name.txt", "Hamn")
	if err := os.Symlink(filepath.Join(files.Root, "AuthKey_K1.p8"), filepath.Join(files.Root, "metadata/en-US/release_notes.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	l, err := FromFastlane(files, "metadata")
	if err != nil {
		t.Fatalf("FromFastlane: %v", err)
	}
	if lf := l.Locales["en-US"]; lf == nil || lf.WhatsNew != nil || lf.Name == nil {
		t.Fatalf("en-US = %+v", lf)
	}
	if !slices.ContainsFunc(l.Notes, func(i Issue) bool {
		return i.Code == IssueUnknownSourceKey && strings.Contains(i.Message, "en-US/release_notes.txt") && strings.Contains(i.Message, "symbolic link")
	}) {
		t.Errorf("notes = %+v", l.Notes)
	}
	for _, i := range l.Notes {
		if strings.Contains(i.Message, "BEGIN") {
			t.Fatalf("key leaked into notes: %q", i.Message)
		}
	}
}

func TestFromExpoRefusesSymlink(t *testing.T) {
	files := tooltest.NewFakeFiles(t)
	writeFile(t, files.Root, "real.json", `{}`)
	if err := os.Symlink(filepath.Join(files.Root, "real.json"), filepath.Join(files.Root, "store.config.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := FromExpo(files, "store.config.json"); !errors.Is(err, tool.ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}
