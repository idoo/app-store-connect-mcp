package skills_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/all" // register every tool
	"github.com/idoo/app-store-connect-mcp/skills"
)

var wantSkills = []string{
	"app-store-connect-mcp-metadata-translate", "app-store-connect-mcp-release", "app-store-connect-mcp-reviews",
	"app-store-connect-mcp-sales", "app-store-connect-mcp-screenshots", "app-store-connect-mcp-testflight",
}

// vocabulary lists snake_case words skills may put in backticks that are
// neither tool names nor argument names: result statuses, error codes and
// result fields.
var vocabulary = map[string]bool{
	"plan_id": true, "still_processing": true, "needs_input": true, "resume_hint": true,
	"enable_writes": true, "writes_disabled": true, "writes_locked": true,
	"team_key_required": true, "individual_key_unsupported": true, "human_approval_required": true,
	"stale_plan": true, "outcome_unknown": true, "rate_limited": true, "already_submitted": true,
	"vendor_number_missing": true, "analytics_not_set_up": true, "no_editable_version": true,
	"missing_localization": true, "upload_failed": true, "reply_locale": true,
	"last_7_days": true, "month_to_date": true, "this_week": true,
	"last_week": true, "last_month": true, "last_seen": true, "review_id": true, // review_id: field of reviews_reply.replies
}

var (
	codeSpan   = regexp.MustCompile("`([^`\n]+)`")
	identifier = regexp.MustCompile(`^"?([a-z][a-z0-9]*(?:_[a-z0-9]+)+)"?[:,.]?$`)
)

func TestSkills(t *testing.T) {
	all, err := skills.All()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range all {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != strings.Join(wantSkills, ",") {
		t.Fatalf("skills = %v, want %v", names, wantSkills)
	}

	known := map[string]string{} // word -> kind
	for _, tl := range tool.All() {
		known[tl.Name()] = "tool"
		if sch := tl.InputSchema(); sch != nil {
			for p := range sch.Properties {
				if _, ok := known[p]; !ok {
					known[p] = "arg"
				}
			}
		}
	}
	if known["release_status"] != "tool" {
		t.Fatal("tool registry is empty")
	}

	for _, s := range all {
		if n := len(s.Description); n > 300 {
			t.Errorf("%s: description is %d chars (max 300)", s.Name, n)
		}
		if strings.Contains(s.Description, "\n") {
			t.Errorf("%s: description must be one line", s.Name)
		}
		if n := strings.Count(strings.TrimRight(s.Body, "\n"), "\n") + 1; n > 120 {
			t.Errorf("%s: body is %d lines (max 120)", s.Name, n)
		}
		tools := 0
		for _, m := range codeSpan.FindAllStringSubmatch(s.Body, -1) {
			word := strings.Fields(m[1])[0]
			id := identifier.FindStringSubmatch(word)
			if id == nil {
				continue
			}
			switch {
			case known[id[1]] == "tool":
				tools++
			case known[id[1]] != "" || vocabulary[id[1]]:
			default:
				t.Errorf("%s: `%s` is not a registered tool, argument or known result word", s.Name, id[1])
			}
		}
		if tools < 3 {
			t.Errorf("%s: mentions only %d registered tools", s.Name, tools)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{
		"no frontmatter",
		"---\nname: x\n",
		"---\nname: x\n---\nbody",
		"---\ndescription: d\n---\nbody",
		"---\nname x\ndescription: d\n---\nbody",
	} {
		if _, err := skills.Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) succeeded", in)
		}
	}
	s, err := skills.Parse([]byte("---\r\nname: a\r\ndescription: when: b\r\n---\r\nbody\r\n"))
	if err != nil || s.Name != "a" || s.Description != "when: b" || s.Body != "body\n" {
		t.Fatalf("Parse = %+v, %v", s, err)
	}
}
