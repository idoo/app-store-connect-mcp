//go:build unix

package browser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCftRunsReportsLoaderError(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "chrome")
	script := "#!/bin/sh\necho 'chrome: error while loading shared libraries: libnss3.so: cannot open shared object file' >&2\nexit 127\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	err := cftRuns(t.Context(), exe)
	if !errors.Is(err, ErrNoBrowser) || !strings.Contains(err.Error(), "libnss3.so") || !strings.Contains(err.Error(), "apt install") {
		t.Fatalf("cftRuns = %v", err)
	}
	ok := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(ok, []byte("#!/bin/sh\necho 'Google Chrome for Testing 156'\n"), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	if err := cftRuns(t.Context(), ok); err != nil {
		t.Fatalf("working chrome: %v", err)
	}
}
