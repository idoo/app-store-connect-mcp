package core

import (
	"testing"

	"github.com/idoo/hamn/internal/spec"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

// kit returns a tooltest kit whose Env uses the real embedded spec index.
func kit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	idx, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	k.Env.Spec = idx
	t.Cleanup(func() { assertConformant(t, k) })
	return k
}

// assertConformant checks every request the tools sent against the spec.
func assertConformant(t *testing.T, k *tooltest.Kit) {
	t.Helper()
	for _, r := range k.ASC.Requests() {
		if _, ok := k.Env.Spec.Match(r.Method, r.Path); !ok {
			continue // unsafe_unlisted tests
		}
		if err := k.Env.Spec.Validate(r); err != nil {
			t.Errorf("%s %s?%s violates the spec: %v", r.Method, r.Path, r.Query.Encode(), err)
		}
	}
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
