package screenshots

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// TestPlanSkipsSymlinks reproduces L3 for screenshot sync: a linked file is
// reported as a problem and never opened or planned for upload.
func TestPlanSkipsSymlinks(t *testing.T) {
	k, _ := newKit(t)
	write(t, k, "shots/en-US/01-home.png", pngImage(t, 1290, 2796, false, 1))
	write(t, k, "other/02.png", pngImage(t, 1290, 2796, false, 2))
	root, err := k.Files.Resolve(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "other", "02.png"), filepath.Join(root, "shots", "en-US", "02-link.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	p := tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	found := false
	for _, pr := range p.Problems {
		found = found || (pr.Kind == problemSymlink && pr.File == "en-US/02-link.png")
	}
	if !found {
		t.Fatalf("problems = %+v", p.Problems)
	}
	for _, s := range p.Sets {
		for _, u := range s.Uploads {
			if u.Rel == "en-US/02-link.png" {
				t.Fatalf("symlink planned for upload: %+v", s)
			}
		}
	}
}
