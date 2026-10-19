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
	order := []string{"app-store-connect-mcp reviews_reply wants to:", "Profile: main", "Targets:", "Untrusted third-party text", "Change:", "Irreversible:", "Notes: Reply contains link/email", "Approve?"}
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
	if !strings.HasPrefix(lines[0], "app-store-connect-mcp users_invite wants to: invite ann@example.com⏎⏎") ||
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
