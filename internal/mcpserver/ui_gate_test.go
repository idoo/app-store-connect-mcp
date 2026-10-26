package mcpserver

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/all" // real registry
)

var (
	gatedListRE   = regexp.MustCompile(`const GATED=\[([^\]]*)\]`)
	actionToolRE  = regexp.MustCompile(`action\(\{tool:"([a-z_]+)"`)
	writeButtonRE = regexp.MustCompile(`writeButton\(`)
	externalURLRE = regexp.MustCompile(`https?://[^"'\s)]+`)
	utCallRE      = regexp.MustCompile(`[^A-Za-z0-9_$]ut\(`)
)

func readView(t *testing.T, name string) string {
	t.Helper()
	b, err := uiFS.ReadFile("ui/" + name + ".html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func registryMeta() map[string]tool.Meta {
	m := map[string]tool.Meta{}
	for _, t := range tool.All() {
		m[t.Name()] = t.Meta()
	}
	return m
}

// TestViewButtonsMatchGates: a view may only offer a second "Confirm" click
// for tools the server really gates; every other button says "(runs now)".
func TestViewButtonsMatchGates(t *testing.T) {
	reg := registryMeta()
	for _, name := range uiScreens {
		v := readView(t, name)
		m := gatedListRE.FindStringSubmatch(v)
		if m == nil {
			t.Fatalf("%s: no GATED list", name)
		}
		gated := map[string]bool{}
		for _, q := range strings.Split(m[1], ",") {
			n := strings.Trim(q, `" `)
			meta, ok := reg[n]
			if !ok {
				t.Errorf("%s: GATED names unknown tool %q", name, n)
			} else if meta.Confirm == tool.ConfirmNone {
				t.Errorf("%s: GATED claims ungated tool %q previews first", name, n)
			}
			gated[n] = true
		}
		for _, am := range actionToolRE.FindAllStringSubmatch(v, -1) {
			meta, ok := reg[am[1]]
			if !ok {
				t.Errorf("%s: action names unknown tool %q", name, am[1])
			} else if meta.Confirm != tool.ConfirmNone && !gated[am[1]] {
				t.Errorf("%s: gated tool %q is missing from GATED (would be mislabelled \"(runs now)\")", name, am[1])
			}
		}
		for _, want := range []string{
			`now=!GATED.includes(tool)`, `(now?" (runs now)":"")`, // ungated label
			`if(d.plan_id||d.preview){`, // Confirm only after a preview/plan
			`b.textContent="Done: "+label`,
		} {
			if !strings.Contains(v, want) {
				t.Errorf("%s: writeButton lacks %q", name, want)
			}
		}
		// writeButton is only reached through action(), which applies the label rule.
		if n := len(writeButtonRE.FindAllStringIndex(v, -1)); n != 2 {
			t.Errorf("%s: writeButton( appears %d times, want definition + action()", name, n)
		}
	}
}

// TestViewsIsolateUntrustedText: third-party fields render through ut() (a
// labelled <bdi> block), never str(); views load nothing external.
func TestViewsIsolateUntrustedText(t *testing.T) {
	keys := map[string]bool{}
	for _, k := range tool.FreeTextKeys {
		keys[k] = true
	}
	member := regexp.MustCompile(`\.([A-Za-z_]+)`)
	for _, name := range uiScreens {
		v := readView(t, name)
		for _, want := range []string{`function ut(v,cls)`, `el("bdi",null,t)`, `"third-party text"`, `.ut{`} {
			if !strings.Contains(v, want) {
				t.Errorf("%s: lacks %q", name, want)
			}
		}
		if n := len(utCallRE.FindAllString(v, -1)); n < 2 { // definition + ≥ 1 use
			t.Errorf("%s: ut() is never used", name)
		}
		if strings.Contains(v, "innerHTML") {
			t.Errorf("%s: uses innerHTML", name)
		}
		for _, u := range externalURLRE.FindAllString(v, -1) {
			if u != "http://www.w3.org/2000/svg" {
				t.Errorf("%s: external URL %s", name, u)
			}
		}
		for i := 0; ; {
			j := strings.Index(v[i:], "str(")
			if j < 0 {
				break
			}
			start := i + j + len("str(")
			i = start
			if start >= 5 && isIdent(v[start-5]) { // e.g. "substr("
				continue
			}
			arg := callArg(v[start:])
			if strings.Contains(arg, "/*trusted*/") {
				continue
			}
			for _, mm := range member.FindAllStringSubmatch(arg, -1) {
				if keys[mm[1]] {
					t.Errorf("%s: str(%s) renders free-text %q; use ut()", name, arg, mm[1])
				}
			}
		}
	}
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// callArg returns the text up to the parenthesis closing a call.
func callArg(s string) string {
	depth := 0
	for i := range len(s) {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return s[:i]
			}
			depth--
		}
	}
	return s
}

// untrustedASC serves /v1/apps with names wrapped the way the client wraps
// third-party text.
type untrustedASC struct{ tool.ASC }

func (untrustedASC) List(context.Context, string, tool.ListOptions) (*tool.ListResult, error) {
	return &tool.ListResult{Data: []tool.Resource{
		{ID: "1", Type: "apps", Attributes: map[string]any{"name": tool.Untrusted("Sikt"), "bundleId": "se.jvcr.sikt"}},
	}}, nil
}

func TestCompletionUnwrapsUntrustedNames(t *testing.T) {
	deps := Deps{Tools: (&fixture{}).tools(), Profile: team(false)}
	deps.Env.ASC = untrustedASC{}
	h := connect(t, deps, "test", nil)
	res, err := h.cs.Complete(context.Background(), &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "release_checklist"},
		Argument: mcp.CompleteParamsArgument{Name: "app", Value: "si"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Completion.Values; len(got) != 1 || got[0] != "Sikt" {
		t.Errorf("app si = %v, want [Sikt]", got)
	}
}

func promptText(t *testing.T, name string, args map[string]string) string {
	t.Helper()
	for _, d := range prompts {
		if d.name == name {
			res, err := d.handle(context.Background(), &mcp.GetPromptRequest{Params: &mcp.GetPromptParams{Name: name, Arguments: args}})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return res.Messages[0].Content.(*mcp.TextContent).Text
		}
	}
	t.Fatalf("prompt %s missing", name)
	return ""
}

func TestPromptsMarkUntrustedAndWaitForApproval(t *testing.T) {
	args := map[string]string{"app": "Hjul", "version": "2.1", "from_locale": "en-US", "to_locales": "de-DE"}
	for _, n := range []string{"weekly_report", "post_release_check", "rejection_triage", "translate_metadata", "release_checklist"} {
		if got := promptText(t, n, args); !strings.Contains(got, untrustedLine) {
			t.Errorf("%s lacks the untrusted-data sentence: %s", n, got)
		}
	}
	r := promptText(t, "reply_to_reviews", args)
	if strings.Contains(r, "then call again with confirm:true") || !strings.Contains(r, "show it to me and wait") || !strings.Contains(r, "only for replies I approve") {
		t.Errorf("reply_to_reviews does not wait for approval: %s", r)
	}
	p := promptText(t, "post_release_check", args)
	if !strings.Contains(p, "pausing:true") || !strings.Contains(p, "never pause without my explicit approval") {
		t.Errorf("post_release_check pause rule: %s", p)
	}
}
