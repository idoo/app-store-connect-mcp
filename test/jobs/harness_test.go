// Package jobs replays the top indie-developer jobs (challenge-indiedev #12)
// as scripted MCP transcripts against fake App Store Connect data. Each job
// asserts its call budget and that every call before the first mutating call
// is a Read tool, so reads never prompt the human.
package jobs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/mcpserver"
	"github.com/idoo/app-store-connect-mcp/internal/resolve"
	"github.com/idoo/app-store-connect-mcp/internal/spec"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/all" // real registry
)

// hjul is the app every job resolves by name.
var hjul = tool.App{ID: "app1", BundleID: "se.jvcr.hjul", Name: "Hjul", Platforms: []tool.Platform{tool.IOS}}

// resolver resolves apps and builds from fake data and beta groups through
// the real resolver against the FakeASC.
type resolver struct {
	*tooltest.FakeResolver
	real *resolve.Resolver
}

func (r resolver) Group(ctx context.Context, appID string, ref tool.GroupRef) (tool.Group, error) {
	return r.real.Group(ctx, appID, ref)
}

// call is one recorded tool call and the elicitation prompts it raised.
type call struct {
	name    string
	class   tool.Class
	prompts int
}

// job is a connected MCP client over the real registry and fake ASC.
type job struct {
	t     *testing.T
	kit   *tooltest.Kit
	cs    *mcp.ClientSession
	calls []call
}

// newJob returns a kit with the real spec index (every request is checked
// against it on cleanup) and the Hjul app resolvable by name.
func newKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	idx, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	k.Env.Spec = idx
	k.Resolve.Apps = []tool.App{hjul}
	k.Env.Resolve = resolver{FakeResolver: k.Resolve, real: resolve.New(k.ASC, resolve.Options{})}
	t.Cleanup(func() {
		for _, r := range k.ASC.Requests() {
			if _, ok := idx.Match(r.Method, r.Path); !ok {
				t.Errorf("%s %s is not a spec operation", r.Method, r.Path)
				continue
			}
			if err := idx.Validate(r); err != nil {
				t.Errorf("%s %s: %v", r.Method, r.Path, err)
			}
		}
	})
	return k
}

// connect starts the MCP server in write mode with every toolset and a
// client that approves elicitations.
func connect(t *testing.T, k *tooltest.Kit) *job {
	t.Helper()
	ctx := context.Background()
	srv, err := mcpserver.Build(ctx, mcpserver.Deps{
		Env:      *k.Env,
		Profile:  k.Env.Profile,
		Toolsets: tool.AllToolsets,
		Version:  "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := &job{t: t, kit: k}
	opts := &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		if n := len(j.calls); n > 0 {
			j.calls[n-1].prompts++
		}
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}, nil
	}}
	// 2025-11-25 allows in-request elicitation, which human-approved tools need.
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "jobs", Version: "1"}, opts).Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	j.cs = cs
	return j
}

// call invokes a tool, requires a non-error result and returns its data.
func (j *job) call(name, args string) map[string]any {
	j.t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		j.t.Fatalf("unknown tool %s", name)
	}
	j.calls = append(j.calls, call{name: name, class: tl.Meta().Class})
	res, err := j.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(args)})
	if err != nil {
		j.t.Fatalf("%s: %v", name, err)
	}
	var body map[string]any
	if b, err := json.Marshal(res.StructuredContent); err == nil {
		_ = json.Unmarshal(b, &body)
	}
	if res.IsError {
		j.t.Fatalf("%s failed: %v", name, body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		data = body
	}
	return data
}

// done asserts the call budget and that Read tools never prompted the human.
func (j *job) done(maxCalls int) {
	j.t.Helper()
	if len(j.calls) > maxCalls {
		j.t.Errorf("job used %d calls, budget %d: %v", len(j.calls), maxCalls, j.calls)
	}
	for _, c := range j.calls {
		if c.class == tool.Read && c.prompts > 0 {
			j.t.Errorf("read tool %s prompted the human %d times", c.name, c.prompts)
		}
	}
}
