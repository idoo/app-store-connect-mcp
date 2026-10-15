package metadata

import (
	"slices"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func TestFromFastlaneDefaultNotes(t *testing.T) {
	hasNote := func(l *Listing, code string) bool {
		return slices.ContainsFunc(l.Notes, func(i Issue) bool { return i.Code == code })
	}

	files := tooltest.NewFakeFiles(t)
	writeFile(t, files.Root, "metadata/default/support_url.txt", "https://x.test/s\n")
	writeFile(t, files.Root, "metadata/en-US/name.txt", "Hamn")
	writeFile(t, files.Root, "metadata/sv/name.txt", "Hamn")
	l, err := FromFastlane(files, "metadata")
	if err != nil {
		t.Fatalf("FromFastlane: %v", err)
	}
	if !hasNote(l, IssueDefaultApplied) || hasNote(l, IssueDefaultOnly) {
		t.Errorf("notes = %+v, want default applied to 2 dirs", l.Notes)
	}

	only := tooltest.NewFakeFiles(t)
	writeFile(t, only.Root, "metadata/default/name.txt", "Hamn")
	l, err = FromFastlane(only, "metadata")
	if err != nil {
		t.Fatalf("FromFastlane default only: %v", err)
	}
	if !hasNote(l, IssueDefaultOnly) || len(l.Locales) != 0 {
		t.Errorf("notes = %+v locales = %d, want no-locale-dirs note", l.Notes, len(l.Locales))
	}
}

func TestDiffLockedAppInfo(t *testing.T) {
	remote := &Listing{
		App: AppInfoFields{
			VersionID: "v", AppInfoID: "locked",
			VersionLocalizationIDs: map[string]string{"en-US": "vl-en"},
			AppInfoLocalizationIDs: map[string]string{"en-US": "il-en"},
		},
		Locales: map[string]*LocaleFields{"en-US": {Name: ptr("Hamn"), Subtitle: ptr("Ship"), Description: ptr("d")}},
	}
	opts := DiffOptions{AppInfoLocked: "WAITING_FOR_REVIEW", Blocker: "MAC_OS 3.6"}

	same := &Listing{Locales: map[string]*LocaleFields{"en-US": {Name: ptr("Hamn"), Subtitle: ptr("Ship"), Description: ptr("new")}}}
	p, err := Diff(remote, same, opts)
	if err != nil || len(p.Changes) != 1 || p.Changes[0].Level != LevelVersion {
		t.Fatalf("unchanged app info: err = %v changes = %+v", err, p.Changes)
	}

	changed := &Listing{Locales: map[string]*LocaleFields{"en-US": {Name: ptr("Hamn 2")}}}
	if p, err = Diff(remote, changed, opts); err == nil || len(p.Errors) != 1 || p.Errors[0].Field != "name" {
		t.Fatalf("changed name: err = %v errors = %+v", err, p.Errors)
	}

	added := &Listing{Locales: map[string]*LocaleFields{"sv": {Name: ptr("Hamn"), Description: ptr("d")}}}
	if p, err = Diff(remote, added, opts); err == nil || p.Counts.Creates != 0 {
		t.Fatalf("new locale while locked: err = %v counts = %+v", err, p.Counts)
	}
}
