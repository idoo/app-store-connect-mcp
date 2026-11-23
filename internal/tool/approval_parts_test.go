package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	continues = "⟨continues in the next part⟩"
	continued = "⟨continued⟩ "
)

// formLines returns n lines of size bytes each, numbered so order shows.
func formLines(n, size int) []string {
	out := make([]string, n)
	for i := range out {
		p := fmt.Sprintf("answer %03d: ", i)
		out[i] = p + strings.Repeat("x", size-len(p))
	}
	return out
}

// changeOf decodes the Change array of one approval part (nil when the
// part has none).
func changeOf(t *testing.T, part string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(part, "\nChange:\n")
	if !ok {
		return nil
	}
	_, rest, _ = strings.Cut(rest, "json\n")
	line, _, _ := strings.Cut(rest, "\n")
	var got []string
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("change block %q: %v", line, err)
	}
	return got
}

func formPreview(lines []string) tool.PlanPreview {
	return tool.PlanPreview{
		Summary:      "send the request form",
		Affected:     []string{"POST https://developer.apple.com/contact/request/form"},
		Irreversible: []string{"Apple receives the form"},
		Untrusted:    []string{"first third-party note", "second third-party note"},
		Intended:     lines,
	}
}

func TestApprovalPromptsFits(t *testing.T) {
	p := formPreview([]string{"a", "b"})
	one, err := tool.ApprovalPrompt("form_submit", "work", p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tool.ApprovalPrompts("form_submit", "work", p)
	if err != nil || len(got) != 1 || got[0] != one {
		t.Fatalf("ApprovalPrompts = %q, %v; want [%q]", got, err, one)
	}
}

func TestApprovalPromptsParts(t *testing.T) {
	lines := formLines(30, 300) // ~9000 bytes
	p := formPreview(lines)
	if _, err := tool.ApprovalPrompt("form_submit", "work", p); err == nil {
		t.Fatal("test preview fits one prompt")
	}
	parts, err := tool.ApprovalPrompts("form_submit", "work", p)
	if err != nil {
		t.Fatal(err)
	}
	n := len(parts)
	if n < 2 {
		t.Fatalf("parts = %d, want several", n)
	}
	var all []string
	untrusted := 0
	for i, part := range parts {
		if len(part) > tool.MaxApprovalPrompt {
			t.Errorf("part %d is %d bytes", i+1, len(part))
		}
		head := "app-store-connect-mcp form_submit wants to: `send the request form`\nProfile: `work`\nIrreversible: `Apple receives the form`\nTargets: `POST https://developer.apple.com/contact/request/form`\n" +
			fmt.Sprintf("Part %d of %d: every part must be approved; nothing is sent before the last.", i+1, n)
		if !strings.HasPrefix(part, head) {
			t.Errorf("part %d head:\n%s", i+1, part)
		}
		want := fmt.Sprintf("\nApprove part %d of %d?", i+1, n)
		if i == n-1 {
			want = "\nApprove?"
		}
		if !strings.HasSuffix(part, want) || strings.Count(part, "Approve") != 1 {
			t.Errorf("part %d ends %q", i+1, part[max(0, len(part)-30):])
		}
		untrusted += strings.Count(part, "Untrusted third-party text")
		if u, c := strings.Index(part, "Untrusted"), strings.Index(part, "\nChange:"); u >= 0 && c >= 0 && u > c {
			t.Errorf("part %d: Untrusted after Change", i+1)
		}
		all = append(all, changeOf(t, part)...)
	}
	if untrusted != 2 || !strings.Contains(parts[0], "first third-party note") {
		t.Errorf("Untrusted blocks shown %d times, want 2 in the first part", untrusted)
	}
	if strings.Join(all, "\n") != strings.Join(lines, "\n") {
		t.Errorf("Change lines across parts differ from the input")
	}
}

func TestApprovalPromptsSplitsLongLine(t *testing.T) {
	line := ""
	for len(line) < 7000 {
		line += "я🙂ж" // 2-, 4- and 2-byte runes: every cut falls among them
	}
	parts, err := tool.ApprovalPrompts("form_submit", "work", formPreview([]string{"short", line, "after"}))
	if err != nil {
		t.Fatal(err)
	}
	var elems []string
	for i, part := range parts {
		if len(part) > tool.MaxApprovalPrompt {
			t.Errorf("part %d is %d bytes", i+1, len(part))
		}
		elems = append(elems, changeOf(t, part)...)
	}
	if len(elems) < 4 || elems[0] != "short" || elems[len(elems)-1] != "after" {
		t.Fatalf("elements = %d, first/last wrong", len(elems))
	}
	pieces := elems[1 : len(elems)-1]
	var b strings.Builder
	for i, s := range pieces {
		if !utf8.ValidString(s) {
			t.Fatalf("piece %d is not valid UTF-8", i)
		}
		if i > 0 {
			var ok bool
			if s, ok = strings.CutPrefix(s, continued); !ok {
				t.Fatalf("piece %d lacks %q", i, continued)
			}
		}
		if i < len(pieces)-1 {
			var ok bool
			if s, ok = strings.CutSuffix(s, continues); !ok {
				t.Fatalf("piece %d lacks %q", i, continues)
			}
		}
		b.WriteString(s)
	}
	if b.String() != line {
		t.Fatal("pieces do not reassemble the line")
	}
}

func TestApprovalPromptsRefused(t *testing.T) {
	p := formPreview(nil)
	p.Intended = map[string]string{"text": strings.Repeat("x", 9000)}
	if _, err := tool.ApprovalPrompts("form_submit", "work", p); !errors.Is(err, tool.ErrInvalidArgs) || !strings.Contains(err.Error(), "split the change") {
		t.Fatalf("non-list payload err = %v", err)
	}
	p = formPreview(formLines(13, 5000))
	_, err := tool.ApprovalPrompts("form_submit", "work", p)
	want := fmt.Sprintf("approval prompts (max %d)", tool.MaxApprovalParts)
	if !errors.Is(err, tool.ErrInvalidArgs) || !strings.Contains(err.Error(), want) {
		t.Fatalf("too many parts err = %v", err)
	}
}

// partsTool is a human-gated plan tool whose preview is the current lines;
// remote is its state fingerprint.
func partsTool(lines *[]string, remote *string) tool.Tool {
	return tool.New(tool.Meta{
		Name: "example_form", Toolset: tool.Metadata, Description: "Sends a form.",
		Class: tool.Write, Confirm: tool.ConfirmByPlan, RequireHuman: true,
	}, func(ctx context.Context, env *tool.Env, in planIn) (*tool.Result, error) {
		wctx, res := tool.Preview(ctx, env, in.PlanID, in, *remote, tool.PlanPreview{Summary: "send form", Intended: *lines})
		if res != nil {
			return res, nil
		}
		_, err := env.ASC.Do(wctx, tool.Request{Method: "PATCH", Path: "/v1/things/1", Write: true, Body: map[string]any{}})
		return tool.OK(map[string]any{"applied": true}), err
	})
}

func TestApprovalPartsRounds(t *testing.T) {
	setup := func(t *testing.T) (*tooltest.Kit, tool.Tool, *[]string, *string, func() string) {
		k := tooltest.New(t)
		k.ASC.SkipSpecCheck = true // /v1/things is a deliberately fake operation
		k.ASC.Handle("PATCH", "/v1/things/1", 200, `{"data":{"type":"things","id":"1"}}`)
		lines, remote := formLines(50, 300), "fp"
		tl := partsTool(&lines, &remote)
		if parts, err := tool.ApprovalPrompts("example_form", "test", tool.PlanPreview{Summary: "send form", Intended: lines}); err != nil || len(parts) != 3 {
			t.Fatalf("parts = %d, %v; want 3", len(parts), err)
		}
		plan := func() string {
			res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"text":"t"}`))
			id, _ := tooltest.Decode[map[string]any](t, res)["plan_id"].(string)
			if id == "" {
				t.Fatal("no plan_id")
			}
			return `{"text":"t","plan_id":"` + id + `"}`
		}
		return k, tl, &lines, &remote, plan
	}
	// round calls tl once, answering state with action (no reply when state is "").
	round := func(k *tooltest.Kit, tl tool.Tool, args, state, action string) *tool.Result {
		env := *k.Env
		if state != "" {
			env.Reply = &tool.ApprovalReply{State: state, Action: action}
		}
		return tl.Call(context.Background(), &env, json.RawMessage(args))
	}
	asks := func(t *testing.T, res *tool.Result, part int) string {
		t.Helper()
		if res.Ask == nil || !strings.Contains(res.Ask.Prompt, fmt.Sprintf("Part %d of 3:", part)) {
			t.Fatalf("result = %+v, want an ask for part %d", res, part)
		}
		return res.Ask.State
	}

	t.Run("all accepted", func(t *testing.T) {
		k, tl, _, _, plan := setup(t)
		args := plan()
		asked := k.Elicit(true)
		res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args))
		if res.Meta.ApprovedBy != tool.ApprovedHuman || len(k.ASC.Mutations()) != 1 || len(*asked) != 3 {
			t.Fatalf("approved_by = %q, mutations = %d, asked = %d", res.Meta.ApprovedBy, len(k.ASC.Mutations()), len(*asked))
		}
		for i, p := range *asked {
			if !strings.Contains(p, fmt.Sprintf("Part %d of 3:", i+1)) {
				t.Errorf("ask %d is not part %d", i, i+1)
			}
		}
	})
	t.Run("declined at part 2", func(t *testing.T) {
		k, tl, _, _, plan := setup(t)
		args := plan()
		asked := k.ElicitFunc(func(round int, _ string) string {
			if round == 1 {
				return "decline"
			}
			return "accept"
		})
		tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, args), "declined")
		if len(k.ASC.Mutations()) != 0 || len(*asked) != 2 {
			t.Fatalf("mutations = %d, asked = %d", len(k.ASC.Mutations()), len(*asked))
		}
	})
	t.Run("replayed part 1", func(t *testing.T) {
		k, tl, _, _, plan := setup(t)
		k.Env.CanElicit = true
		args := plan()
		s1 := asks(t, round(k, tl, args, "", ""), 1)
		s2 := asks(t, round(k, tl, args, s1, "accept"), 2)
		tooltest.MustFail(t, round(k, tl, args, s1, "accept"), "invalid_arguments")
		s3 := asks(t, round(k, tl, args, s2, "accept"), 3)
		tooltest.MustFail(t, round(k, tl, args, s2, "accept"), "invalid_arguments")
		if res := tooltest.MustOK(t, round(k, tl, args, s3, "accept")); res.Meta.ApprovedBy != tool.ApprovedHuman {
			t.Fatalf("approved_by = %q", res.Meta.ApprovedBy)
		}
		if n := len(k.ASC.Mutations()); n != 1 {
			t.Fatalf("mutations = %d", n)
		}
	})
	t.Run("line edited between rounds", func(t *testing.T) {
		k, tl, lines, _, plan := setup(t)
		k.Env.CanElicit = true
		args := plan()
		s1 := asks(t, round(k, tl, args, "", ""), 1)
		s2 := asks(t, round(k, tl, args, s1, "accept"), 2)
		(*lines)[len(*lines)-1] += "!"
		tooltest.MustFail(t, round(k, tl, args, s2, "accept"), "invalid_arguments")
		if n := len(k.ASC.Mutations()); n != 0 {
			t.Fatalf("mutations = %d", n)
		}
	})
	t.Run("state changed between rounds", func(t *testing.T) {
		k, tl, _, remote, plan := setup(t)
		k.Env.CanElicit = true
		args := plan()
		s1 := asks(t, round(k, tl, args, "", ""), 1)
		s2 := asks(t, round(k, tl, args, s1, "accept"), 2)
		*remote = "changed"
		tooltest.MustFail(t, round(k, tl, args, s2, "accept"), "stale_plan")
		if n := len(k.ASC.Mutations()); n != 0 {
			t.Fatalf("mutations = %d", n)
		}
	})
	t.Run("forged part", func(t *testing.T) {
		k, tl, _, _, plan := setup(t)
		k.Env.CanElicit = true
		args := plan()
		s1 := asks(t, round(k, tl, args, "", ""), 1)
		s2 := asks(t, round(k, tl, args, s1, "accept"), 2)
		last := "A"
		if s2[len(s2)-1] == 'A' {
			last = "B"
		}
		tooltest.MustFail(t, round(k, tl, args, s2[:len(s2)-1]+last, "accept"), "invalid_arguments")
		if n := len(k.ASC.Mutations()); n != 0 {
			t.Fatalf("mutations = %d", n)
		}
	})
	t.Run("no elicitation", func(t *testing.T) {
		k, tl, _, _, plan := setup(t)
		tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, plan()), "human_approval_required")
		if n := len(k.ASC.Mutations()); n != 0 {
			t.Fatalf("mutations = %d", n)
		}
	})
}
