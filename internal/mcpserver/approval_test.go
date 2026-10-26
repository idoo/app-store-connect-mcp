package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// approvalDeps returns deps with two gated tools that DELETE a beta tester
// on a fake ASC once their confirm gate passes.
func approvalDeps(t *testing.T, profile string, human bool) (Deps, *tooltest.FakeASC, *tooltest.FakeClock) {
	t.Helper()
	fake := tooltest.NewFakeASC(t)
	fake.Handle(http.MethodDelete, "/v1/betaTesters/{id}", http.StatusNoContent, "")
	clock := tooltest.NewFakeClock()
	mk := func(name string) tool.Tool {
		m := tool.Meta{
			Name: name, Toolset: tool.Release, Title: name, Description: "stub " + name,
			Class: tool.Destructive, Confirm: tool.ConfirmByFlag, RequireHuman: human,
		}
		return tool.New(m, func(ctx context.Context, env *tool.Env, in stubIn) (*tool.Result, error) {
			wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{Summary: "remove tester " + in.App})
			if res != nil {
				return res, nil
			}
			if _, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodDelete, Path: "/v1/betaTesters/" + in.App, Write: true}); err != nil {
				return nil, err
			}
			return tool.OK(map[string]any{"removed": in.App}), nil
		})
	}
	p := team(true)
	p.Name = profile
	deps := Deps{Tools: []tool.Tool{mk("release_submit"), mk("release_cancel")}, Profile: p}
	deps.Env.ASC = fake
	deps.Env.Clock = clock
	return deps, fake, clock
}

func answerWith(action string, asked *int) func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	return func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		*asked++
		return &mcp.ElicitResult{Action: action, Content: map[string]any{}}, nil
	}
}

func approvedBy(body map[string]any) any {
	meta, _ := body["_meta"].(map[string]any)
	m, _ := meta["app-store-connect-mcp"].(map[string]any)
	return m["approved_by"]
}

func TestApprovalRoundTrip(t *testing.T) {
	confirm := map[string]any{"app": "t1", "confirm": true}
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version+"/accept", func(t *testing.T) {
			deps, fake, _ := approvalDeps(t, "work", false)
			asked := 0
			h := connect(t, deps, "test", answerWith("accept", &asked), version)
			if got := h.cs.InitializeResult().ProtocolVersion; got != version {
				t.Fatalf("negotiated %q, want %q", got, version)
			}
			res, body := h.call(t, "release_submit", confirm)
			if res.IsError || dataOf(body)["removed"] != "t1" || approvedBy(body) != tool.ApprovedHuman {
				t.Fatalf("result = %v", body)
			}
			if asked != 1 || len(fake.Mutations()) != 1 {
				t.Fatalf("asked = %d, mutations = %d; want 1, 1", asked, len(fake.Mutations()))
			}
		})
		t.Run(version+"/decline", func(t *testing.T) {
			deps, fake, _ := approvalDeps(t, "work", false)
			asked := 0
			h := connect(t, deps, "test", answerWith("decline", &asked), version)
			res, body := h.call(t, "release_submit", confirm)
			if !res.IsError || dataOf(body)["error"] != tool.ErrDeclined.Code {
				t.Fatalf("result = %v", body)
			}
			if asked != 1 || len(fake.Mutations()) != 0 {
				t.Fatalf("asked = %d, mutations = %d; want 1, 0", asked, len(fake.Mutations()))
			}
		})
		t.Run(version+"/no elicitation", func(t *testing.T) {
			deps, fake, _ := approvalDeps(t, "work", true)
			h := connect(t, deps, "test", nil, version)
			res, body := h.call(t, "release_submit", confirm)
			if !res.IsError || dataOf(body)["error"] != tool.ErrHumanRequired.Code || len(fake.Mutations()) != 0 {
				t.Fatalf("result = %v, mutations = %d", body, len(fake.Mutations()))
			}
		})
	}
}

// rawHarness connects a 2026-07-28 client that returns input-required
// results instead of answering them, so tests can craft retries.
func rawHarness(t *testing.T, deps Deps) *harness {
	t.Helper()
	opts := &mcp.ClientOptions{
		ElicitationHandler: answerWith("accept", new(int)),
		MultiRoundTrip:     &mcp.MultiRoundTripOptions{Disabled: true},
	}
	return connectOpts(t, deps, "test", opts, "2026-07-28")
}

// ask makes the first round of a confirmed call and returns its requestState.
func ask(t *testing.T, h *harness, name string, args map[string]any) string {
	t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := res.InputRequests[approveInput].(*mcp.ElicitParams)
	if !res.NeedsInput() || !ok || res.RequestState == "" || len(res.Content) != 0 {
		t.Fatalf("first round = %+v, want an approval input request", res)
	}
	if props, _ := p.RequestedSchema.(map[string]any)["properties"].(map[string]any); len(props) != 0 {
		t.Fatalf("approval form has fields: %v", props)
	}
	return res.RequestState
}

// retry sends a confirmed call with an approval reply.
func retry(t *testing.T, h *harness, name string, args map[string]any, state, action string) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	params := &mcp.CallToolParams{Name: name, Arguments: args, RequestState: state}
	if action != "" {
		params.InputResponses = mcp.InputResponseMap{approveInput: &mcp.ElicitResult{Action: action}}
	}
	res, err := h.cs.CallTool(context.Background(), params)
	if err != nil {
		t.Fatalf("retry %s: %v", name, err)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	return res, body
}

func TestApprovalStateRefused(t *testing.T) {
	args := map[string]any{"app": "t1", "confirm": true}
	refused := func(t *testing.T, res *mcp.CallToolResult, body map[string]any, fake *tooltest.FakeASC, wantMutations int) {
		t.Helper()
		if !res.IsError || dataOf(body)["error"] != tool.ErrInvalidArgs.Code {
			t.Fatalf("result = %v, want invalid_arguments", body)
		}
		if n := len(fake.Mutations()); n != wantMutations {
			t.Fatalf("mutations = %d, want %d", n, wantMutations)
		}
	}

	t.Run("forged and tampered", func(t *testing.T) {
		deps, fake, _ := approvalDeps(t, "work", false)
		h := rawHarness(t, deps)
		state := ask(t, h, "release_submit", args)
		last := "A"
		if state[len(state)-1] == 'A' {
			last = "B"
		}
		tampered := state[:len(state)-1] + last
		for _, s := range []string{"", "e30.forged", "not-a-token", tampered} {
			res, body := retry(t, h, "release_submit", args, s, "accept")
			refused(t, res, body, fake, 0)
		}
	})
	t.Run("other args and tool", func(t *testing.T) {
		deps, fake, _ := approvalDeps(t, "work", false)
		h := rawHarness(t, deps)
		state := ask(t, h, "release_submit", args)
		res, body := retry(t, h, "release_submit", map[string]any{"app": "t2", "confirm": true}, state, "accept")
		refused(t, res, body, fake, 0)
		res, body = retry(t, h, "release_cancel", args, state, "accept")
		refused(t, res, body, fake, 0)
	})
	t.Run("other profile", func(t *testing.T) {
		deps, _, _ := approvalDeps(t, "work", false)
		state := ask(t, rawHarness(t, deps), "release_submit", args)
		other, fake, _ := approvalDeps(t, "client-acme", false)
		res, body := retry(t, rawHarness(t, other), "release_submit", args, state, "accept")
		refused(t, res, body, fake, 0)
	})
	t.Run("expired", func(t *testing.T) {
		deps, fake, clock := approvalDeps(t, "work", false)
		h := rawHarness(t, deps)
		state := ask(t, h, "release_submit", args)
		clock.Set(clock.Now().Add(tool.ApprovalTTL + time.Second))
		res, body := retry(t, h, "release_submit", args, state, "accept")
		refused(t, res, body, fake, 0)
	})
	t.Run("missing response", func(t *testing.T) {
		deps, fake, _ := approvalDeps(t, "work", false)
		h := rawHarness(t, deps)
		state := ask(t, h, "release_submit", args)
		res, body := retry(t, h, "release_submit", args, state, "")
		refused(t, res, body, fake, 0)
	})
	t.Run("replayed accept", func(t *testing.T) {
		deps, fake, _ := approvalDeps(t, "work", false)
		h := rawHarness(t, deps)
		state := ask(t, h, "release_submit", args)
		res, body := retry(t, h, "release_submit", args, state, "accept")
		if res.IsError || approvedBy(body) != tool.ApprovedHuman || len(fake.Mutations()) != 1 {
			t.Fatalf("accepted retry = %v, mutations = %d", body, len(fake.Mutations()))
		}
		res, body = retry(t, h, "release_submit", args, state, "accept")
		refused(t, res, body, fake, 1)
	})
}
