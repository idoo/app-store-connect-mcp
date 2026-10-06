package tool_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func TestParallelBoundsConcurrency(t *testing.T) {
	var cur, peak atomic.Int32
	items := make([]int, 40)
	err := tool.Parallel(context.Background(), 3, items, func(_ context.Context, _ int, _ int) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		cur.Add(-1)
		return nil
	})
	if err != nil || peak.Load() > 3 || peak.Load() < 2 {
		t.Fatalf("err=%v peak=%d", err, peak.Load())
	}
	if err := tool.Parallel(context.Background(), 0, []int{1}, func(context.Context, int, int) error { return nil }); err != nil {
		t.Fatalf("n=0: %v", err)
	}
	if err := tool.Parallel[int](context.Background(), 2, nil, nil); err != nil {
		t.Fatalf("empty: %v", err)
	}
}

func TestParallelRecoversPanic(t *testing.T) {
	err := tool.Parallel(context.Background(), 2, []string{"a", "b", "c"}, func(_ context.Context, i int, _ string) error {
		if i == 1 {
			panic("boom")
		}
		return nil
	})
	if !errors.Is(err, tool.ErrInternal) || !strings.Contains(err.Error(), "item 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestParallelFirstErrorCancelsRest(t *testing.T) {
	boom := errors.New("boom")
	var started atomic.Int32
	err := tool.Parallel(context.Background(), 1, make([]int, 10), func(_ context.Context, i int, _ int) error {
		started.Add(1)
		if i == 2 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := started.Load(); n != 3 {
		t.Fatalf("started %d items after the error, want 3", n)
	}
}

func TestParallelSlotWaitHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var started atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- tool.Parallel(ctx, 1, make([]int, 5), func(context.Context, int, int) error {
			started.Add(1)
			<-release
			return nil
		})
	}()
	for started.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel() // the loop is blocked waiting for the only slot
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Parallel did not return after cancel")
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("started %d items after cancel, want 1", n)
	}
}

func TestSharedErrorHelpers(t *testing.T) {
	api := func(s int) error { return fmt.Errorf("wrapped: %w", &tool.APIError{Status: s}) }
	nf := fmt.Errorf("resolve: %w", &tool.ErrNotFound{Kind: "app", Ref: "x"})
	tests := []struct {
		name               string
		err                error
		status             int
		notFound, conflict bool
	}{
		{"nil", nil, 0, false, false},
		{"plain", errors.New("x"), 0, false, false},
		{"api404", api(404), 404, true, false},
		{"api409", api(409), 409, false, true},
		{"api500", api(500), 500, false, false},
		{"resolver_miss", nf, 0, true, false},
	}
	for _, tc := range tests {
		if got := tool.APIStatus(tc.err); got != tc.status {
			t.Errorf("%s: APIStatus = %d", tc.name, got)
		}
		if got := tool.IsNotFound(tc.err); got != tc.notFound {
			t.Errorf("%s: IsNotFound = %v", tc.name, got)
		}
		if got := tool.IsConflict(tc.err); got != tc.conflict {
			t.Errorf("%s: IsConflict = %v", tc.name, got)
		}
	}
	inner := errors.New("inner")
	err := tool.InvalidArgs("bad %s: %w", "limit", inner)
	if !errors.Is(err, tool.ErrInvalidArgs) || !errors.Is(err, inner) || err.Error() != "invalid arguments: bad limit: inner" {
		t.Fatalf("InvalidArgs = %q", err)
	}
	if b := tool.BodyFor(err); b.Error != "invalid_arguments" {
		t.Fatalf("code = %q", b.Error)
	}
}

func TestWritesDisabledHintPointsAtEnableWrites(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	tl := tool.New(exampleMeta, exampleHandler)
	b := tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"1"}`), "writes_disabled")
	if b.Hint != tool.EnableWritesHint || !strings.Contains(b.Hint, `"enable_writes"`) || strings.Contains(b.Hint, "--allow-writes") {
		t.Fatalf("hint = %q", b.Hint)
	}
}

// gateTool builds a tool whose handler runs gate and then mutates once.
func gateTool(m tool.Meta, gate func(ctx context.Context, env *tool.Env, in planIn) (context.Context, *tool.Result)) tool.Tool {
	return tool.New(m, func(ctx context.Context, env *tool.Env, in planIn) (*tool.Result, error) {
		wctx, res := gate(ctx, env, in)
		if res != nil {
			return res, nil
		}
		_, err := env.ASC.Do(wctx, tool.Request{Method: "DELETE", Path: "/v1/things/1", Write: true})
		return nil, err
	})
}

type flagPlanIn struct {
	tool.ConfirmFlag
	tool.ConfirmPlan
}

func TestGateMisuseFailsClosed(t *testing.T) {
	base := tool.Meta{Toolset: tool.Webhooks, Description: "Gate test.", Class: tool.Destructive}
	planMeta := base
	planMeta.Name, planMeta.Confirm = "gate_plan", tool.ConfirmByPlan
	humanFlag := base
	humanFlag.Name, humanFlag.Confirm, humanFlag.RequireHuman = "gate_human_flag", tool.ConfirmByFlag, true
	humanPlan := planMeta
	humanPlan.Name, humanPlan.RequireHuman = "gate_human_plan", true

	tests := []struct {
		name  string
		tool  func() tool.Tool
		setup func(k *tooltest.Kit)
		args  string
		code  string
	}{
		{"unconfirmed_on_require_human", func() tool.Tool {
			m := humanFlag
			return tool.New(m, func(ctx context.Context, env *tool.Env, _ exampleIn) (*tool.Result, error) {
				wctx, res := tool.Unconfirmed(ctx, env)
				if res != nil {
					return res, nil
				}
				_, err := env.ASC.Do(wctx, tool.Request{Method: "DELETE", Path: "/v1/things/1", Write: true})
				return nil, err
			})
		}, nil, `{"app":"1"}`, "internal_error"},
		{"flag_gate_in_plan_tool", func() tool.Tool {
			return gateTool(planMeta, func(ctx context.Context, env *tool.Env, _ planIn) (context.Context, *tool.Result) {
				return tool.RequireConfirm(ctx, env, true, tool.PlanPreview{Summary: "x"})
			})
		}, nil, `{"text":"a"}`, "internal_error"},
		{"plan_gate_in_flag_tool", func() tool.Tool {
			m := base
			m.Name, m.Confirm = "gate_flag", tool.ConfirmByFlag
			return tool.New(m, func(ctx context.Context, env *tool.Env, _ flagPlanIn) (*tool.Result, error) {
				_, res := tool.Preview(ctx, env, "", map[string]any{}, "fp", tool.PlanPreview{Summary: "x"})
				return res, nil
			})
		}, nil, `{}`, "internal_error"},
		{"preview_empty_fingerprint", func() tool.Tool {
			return gateTool(planMeta, func(ctx context.Context, env *tool.Env, in planIn) (context.Context, *tool.Result) {
				return tool.Preview(ctx, env, in.PlanID, in, "", tool.PlanPreview{Summary: "x"})
			})
		}, nil, `{"text":"a"}`, "internal_error"},
		{"noconfirm_require_human_flag", func() tool.Tool {
			return tool.New(humanFlag, exampleHandler)
		}, func(k *tooltest.Kit) { k.Env.NoConfirm = true }, `{"app":"1"}`, "human_approval_required"},
		{"noconfirm_require_human_plan_verify", func() tool.Tool {
			return gateTool(humanPlan, func(ctx context.Context, env *tool.Env, in planIn) (context.Context, *tool.Result) {
				return tool.Preview(ctx, env, in.PlanID, in, "fp", tool.PlanPreview{Summary: "x"})
			})
		}, func(k *tooltest.Kit) { k.Env.NoConfirm = true }, `PLAN`, "human_approval_required"},
		{"elicit_error_is_not_approval", func() tool.Tool {
			return tool.New(humanFlag, exampleHandler)
		}, func(k *tooltest.Kit) {
			k.Env.Elicit = func(context.Context, string) (bool, error) { return false, errors.New("transport closed") }
		}, `{"app":"1","confirm":true}`, "error"},
		{"elicit_error_non_human", func() tool.Tool {
			return tool.New(exampleMeta, exampleHandler)
		}, func(k *tooltest.Kit) {
			k.Env.Elicit = func(context.Context, string) (bool, error) { return false, errors.New("transport closed") }
		}, `{"app":"1","confirm":true}`, "error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := newKit(t)
			if tc.setup != nil {
				tc.setup(k)
			}
			tl := tc.tool()
			args := tc.args
			if args == "PLAN" {
				// NoConfirm does not skip the plan step for RequireHuman tools.
				res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"text":"a"}`))
				id, _ := tooltest.Decode[map[string]any](t, res)["plan_id"].(string)
				if id == "" {
					t.Fatal("RequireHuman plan tool skipped its plan under NoConfirm")
				}
				args = `{"text":"a","plan_id":"` + id + `"}`
			}
			tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, args), tc.code)
			if n := len(k.ASC.Mutations()); n != 0 {
				t.Fatalf("mutations = %d", n)
			}
		})
	}
}

func TestGateOutsideCallFailsClosed(t *testing.T) {
	k := newKit(t)
	ctx, res := tool.RequireConfirm(context.Background(), k.Env, true, tool.PlanPreview{Summary: "x"})
	if ctx != nil || res == nil || !res.IsError || tool.IsWrite(context.Background()) {
		t.Fatalf("gate outside Tool.Call granted writes: %+v", res)
	}
	if _, res := tool.Unconfirmed(context.Background(), k.Env); res == nil || !res.IsError {
		t.Fatal("Unconfirmed outside Tool.Call granted writes")
	}
}

func TestPlanExpiresWithKitClock(t *testing.T) {
	k := newKit(t)
	tl := gateTool(tool.Meta{Name: "gate_ttl", Toolset: tool.Webhooks, Description: "TTL test.", Class: tool.Destructive, Confirm: tool.ConfirmByPlan},
		func(ctx context.Context, env *tool.Env, in planIn) (context.Context, *tool.Result) {
			return tool.Preview(ctx, env, in.PlanID, in, "fp", tool.PlanPreview{Summary: "x"})
		})
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"text":"a"}`))
	id, _ := tooltest.Decode[map[string]any](t, res)["plan_id"].(string)
	k.Clock.Set(k.Clock.Now().Add(tool.PlanTTL))
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"text":"a","plan_id":"`+id+`"}`), "stale_plan")
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("mutations = %d", n)
	}
}

func TestMetaCheckPanics(t *testing.T) {
	ok := tool.Meta{Name: "meta_x", Toolset: tool.Webhooks, Description: "d", Class: tool.Destructive, Confirm: tool.ConfirmByFlag}
	tests := map[string]func(m *tool.Meta){
		"destructive_without_confirm": func(m *tool.Meta) { m.Confirm = tool.ConfirmNone },
		"human_without_confirm":       func(m *tool.Meta) { m.Class, m.Confirm, m.RequireHuman = tool.Write, tool.ConfirmNone, true },
		"read_with_confirm":           func(m *tool.Meta) { m.Class = tool.Read },
		"flag_without_field":          nil,
	}
	for name, mut := range tests {
		t.Run(name, func(t *testing.T) {
			m := ok
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			if mut == nil {
				tool.New(m, func(context.Context, *tool.Env, struct{}) (*tool.Result, error) { return nil, nil })
				return
			}
			mut(&m)
			tool.New(m, func(context.Context, *tool.Env, exampleIn) (*tool.Result, error) { return nil, nil })
		})
	}
}

// rootedFiles records the roots it was asked for.
type rootedFiles struct {
	tooltest.FakeFiles
	got   [][]string
	err   error
	calls int
}

func (r *rootedFiles) ForRoots(roots []string) (tool.Files, error) {
	r.calls++
	r.got = append(r.got, roots)
	if r.err != nil {
		return nil, r.err
	}
	return &r.FakeFiles, nil
}

func TestCallPassesClientRootsLazily(t *testing.T) {
	k := newKit(t)
	rf := &rootedFiles{FakeFiles: *k.Files}
	if err := os.WriteFile(k.Files.Root+"/a.txt", []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed := 0
	k.Env.Files = rf
	k.Env.Roots = func(context.Context) ([]string, error) {
		listed++
		return []string{"file:///proj"}, nil
	}
	read := tool.New(tool.Meta{Name: "roots_read", Toolset: tool.Metadata, Description: "Reads.", Class: tool.Read},
		func(_ context.Context, env *tool.Env, in struct {
			Read bool `json:"read,omitempty"`
		},
		) (*tool.Result, error) {
			if !in.Read {
				return nil, nil
			}
			if _, err := env.Files.ReadFile("a.txt"); err != nil {
				return nil, err
			}
			_, err := env.Files.Stat("a.txt")
			return nil, err
		})
	tooltest.MustOK(t, tooltest.Call(t, read, k.Env, `{}`))
	if listed != 0 {
		t.Fatal("roots listed by a call that touched no files")
	}
	tooltest.MustOK(t, tooltest.Call(t, read, k.Env, `{"read":true}`))
	if listed != 1 || rf.calls != 1 || rf.got[0][0] != "file:///proj" {
		t.Fatalf("listed=%d calls=%d got=%v", listed, rf.calls, rf.got)
	}
	// A listing failure still goes through ForRoots (which applies the safety
	// policy to the fallback root) rather than bypassing it.
	k.Env.Roots = func(context.Context) ([]string, error) { return nil, errors.New("no roots") }
	rf.err = fmt.Errorf("%w: refused", tool.ErrUnavailable)
	b := tooltest.MustFail(t, tooltest.Call(t, read, k.Env, `{"read":true}`), "unavailable")
	if rf.got[len(rf.got)-1] != nil || !strings.Contains(b.Message, "refused") {
		t.Fatalf("fallback roots = %v, %+v", rf.got, b)
	}
}
