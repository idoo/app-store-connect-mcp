package core

import (
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// kit returns a tooltest kit; its FakeASC validates every request against
// the pinned spec.
func kit(t *testing.T) *tooltest.Kit {
	t.Helper()
	return tooltest.New(t)
}

func mustTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

func data(t *testing.T, res *tool.Result) map[string]any {
	t.Helper()
	return tooltest.Decode[map[string]any](t, res)
}
