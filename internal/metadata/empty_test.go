package metadata

import "testing"

// MF-1: desired "" equals a remote null/absent field, a new localization
// never sends empty fields, and re-planning after apply converges.
func TestDiffEmptyMatchesAbsent(t *testing.T) {
	remote := &Listing{
		App: AppInfoFields{
			VersionID: "v", AppInfoID: "info",
			VersionLocalizationIDs: map[string]string{"en-US": "vl-en"},
			AppInfoLocalizationIDs: map[string]string{"en-US": "il-en", "sv": "il-sv"},
		},
		Locales: map[string]*LocaleFields{
			"en-US": {Name: ptr("Hamn"), Description: ptr("d"), Keywords: ptr("k")},
			"sv":    {Name: ptr("Hamn")},
		},
	}
	desired := &Listing{Locales: map[string]*LocaleFields{
		"en-US": {Name: ptr("Hamn"), Description: ptr("d"), Keywords: ptr("k"), MarketingURL: ptr(""), PromotionalText: ptr("")},
		"sv":    {Description: ptr("b"), Keywords: ptr("k"), MarketingURL: ptr(""), PromotionalText: ptr("")},
	}}
	p, err := Diff(remote, desired, DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v (errors %+v)", err, p.Errors)
	}
	if p.Counts.Updates != 0 || p.Counts.Creates != 1 || len(p.Changes) != 1 {
		t.Fatalf("counts = %+v changes = %+v", p.Counts, p.Changes)
	}
	create := p.Changes[0]
	if create.Op != OpCreate || create.Locale != "sv" {
		t.Fatalf("change = %+v", create)
	}
	for _, f := range create.Fields {
		if f.New == "" {
			t.Errorf("create sends empty %s", f.Field)
		}
	}

	// After apply: the new localization holds only its non-empty fields.
	remote.App.VersionLocalizationIDs["sv"] = "vl-sv"
	remote.Locales["sv"].Description, remote.Locales["sv"].Keywords = ptr("b"), ptr("k")
	if p, err = Diff(remote, desired, DiffOptions{}); err != nil || len(p.Changes) != 0 {
		t.Fatalf("re-plan: err = %v changes = %+v", err, p.Changes)
	}

	// Clearing a field that has a value is still a change.
	cleared := &Listing{Locales: map[string]*LocaleFields{"en-US": {Keywords: ptr("")}}}
	if p, err = Diff(remote, cleared, DiffOptions{}); err != nil || p.Counts.Updates != 1 {
		t.Fatalf("clear: err = %v counts = %+v", err, p.Counts)
	}
}
