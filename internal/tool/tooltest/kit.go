// Package tooltest provides in-memory fakes of every [tool.Env] dependency and
// helpers to call tools directly, so tool slices unit-test handlers without
// the MCP server or network.
//
//	k := tooltest.New(t)
//	k.ASC.HandleFile("GET", "/v1/apps", "testdata/apps.json")
//	res := tooltest.Call(t, myTool, k.Env, `{"app":"Hjul"}`)
//	tooltest.AssertGolden(t, "testdata/golden/apps_list.json", res)
//
// Run `go test ./... -update` to rewrite golden files.
package tooltest

import (
	"context"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

// Kit bundles a ready [tool.Env] with handles to its fakes.
type Kit struct {
	Env      *tool.Env
	ASC      *FakeASC
	Plans    *FakePlanner
	State    *FakeStore
	Files    *FakeFiles
	Fetch    *FakeFetcher
	Toolsets *FakeToolsets
	Clock    *FakeClock
	Resolve  *FakeResolver
}

// New returns a Kit whose Env uses fresh fakes and the profile
// {Name:"test", KeyKind:team, AllowWrites:true, VendorNumber:"12345678"}.
// Env.Spec is nil; tests needing the spec set it to the real index.
func New(t testing.TB) *Kit {
	t.Helper()
	k := &Kit{
		ASC:      NewFakeASC(t),
		Plans:    NewFakePlanner(),
		State:    NewFakeStore(),
		Files:    NewFakeFiles(t),
		Fetch:    NewFakeFetcher(t),
		Toolsets: NewFakeToolsets(),
		Clock:    NewFakeClock(),
		Resolve:  &FakeResolver{},
	}
	k.Env = &tool.Env{
		ASC:      k.ASC,
		Resolve:  k.Resolve,
		Plans:    k.Plans,
		State:    k.State,
		Toolsets: k.Toolsets,
		Fetch:    k.Fetch,
		Files:    k.Files,
		Clock:    k.Clock,
		Version:  "test",
		Profile: tool.ProfileInfo{
			Name:         "test",
			KeyKind:      tool.KeyTeam,
			VendorNumber: "12345678",
			AllowWrites:  true,
		},
	}
	return k
}

// ReadOnly switches the Kit's profile to read-only mode and returns k.
func (k *Kit) ReadOnly() *Kit {
	k.Env.Profile.AllowWrites = false
	return k
}

// Individual switches the Kit's profile to an Individual API key and returns k.
func (k *Kit) Individual() *Kit {
	k.Env.Profile.KeyKind = tool.KeyIndividual
	return k
}

// Elicit installs an elicitation handler that answers accept and records
// the questions asked.
func (k *Kit) Elicit(accept bool) *[]string {
	asked := &[]string{}
	k.Env.Elicit = func(_ context.Context, msg string) (bool, error) {
		*asked = append(*asked, msg)
		return accept, nil
	}
	return asked
}
