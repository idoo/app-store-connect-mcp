package metadata

import (
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
)

// Op is a planned localization operation.
type Op string

// Plan operations.
const (
	OpUpdate Op = "update"
	OpCreate Op = "create"
	OpDelete Op = "delete"
)

// Issue codes reported in [Plan.Warnings] and [Plan.Errors].
const (
	IssueTooLong          = "too_long"
	IssueInvalidURL       = "invalid_url"
	IssueUnknownLocale    = "unknown_locale"
	IssueDuplicateLocale  = "duplicate_locale"
	IssueRemoveConflict   = "remove_conflict"
	IssueRemoveMissing    = "remove_missing"
	IssueLevelUnavailable = "level_unavailable"
	IssueIncompleteLocale = "incomplete_locale"
	IssueWhatsNewFirst    = "whats_new_first_version"
)

// DiffOptions tunes [Diff].
type DiffOptions struct {
	// Remove lists locales to delete. Nothing else is ever deleted.
	Remove []string
	// FirstVersion drops whatsNew (Apple rejects it on an app's first version).
	FirstVersion bool
}

// FieldChange is one field's old → new value. Old is nil when the remote
// field is absent or the localization is new.
type FieldChange struct {
	Field string  `json:"field"`
	Old   *string `json:"old,omitempty"`
	New   string  `json:"new"`
}

// Change is one localization operation.
type Change struct {
	Locale string `json:"locale"`
	Level  Level  `json:"level"`
	Op     Op     `json:"op"`
	// ID is the remote localization id (update and delete).
	ID     string        `json:"id,omitempty"`
	Fields []FieldChange `json:"fields,omitempty"`
}

// Issue is a validation finding.
type Issue struct {
	Locale  string `json:"locale,omitempty"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Counts summarizes a plan.
type Counts struct {
	Updates int `json:"updates"`
	Creates int `json:"creates"`
	Deletes int `json:"deletes"`
	// Unchanged counts remote localizations the plan leaves untouched.
	Unchanged int `json:"unchanged"`
}

// Plan is the result of [Diff].
type Plan struct {
	Changes  []Change `json:"changes"`
	Warnings []Issue  `json:"warnings,omitempty"`
	Errors   []Issue  `json:"errors,omitempty"`
	Counts   Counts   `json:"counts"`

	fingerprint string
}

// Fingerprint identifies the remote snapshot the plan was computed from; a
// re-pull with a different fingerprint means the plan is stale.
func (p *Plan) Fingerprint() string { return p.fingerprint }

// ErrInvalidListing is returned by [Diff] when the plan has errors; the plan
// is returned alongside it so callers can show [Plan.Errors].
var ErrInvalidListing = errors.New("metadata: desired listing is invalid")

// Diff compares desired with remote (from [Pull]). Absent desired fields and
// remote locales missing from desired are left unchanged; only locales in
// opts.Remove are deleted. Every value is checked against Apple's limits.
func Diff(remote, desired *Listing, opts DiffOptions) (*Plan, error) {
	if remote == nil || desired == nil {
		return nil, fmt.Errorf("%w: diff needs remote and desired listings", tool.ErrInternal)
	}
	fp, err := tool.Fingerprint(remote)
	if err != nil {
		return nil, err
	}
	p := &Plan{Changes: []Change{}, fingerprint: fp}

	want := normalizeDesired(desired, p)
	remove := map[string]bool{}
	for _, raw := range opts.Remove {
		code, err := applemeta.NormalizeLocale(raw)
		if err != nil {
			p.errorf(raw, "", IssueUnknownLocale, "%v", err)
			continue
		}
		remove[code] = true
	}

	codes := make([]string, 0, len(want)+len(remove))
	for c := range want {
		codes = append(codes, c)
	}
	for c := range remove {
		if _, ok := want[c]; !ok {
			codes = append(codes, c)
		}
	}
	slices.Sort(codes)

	touched := map[Level]map[string]bool{LevelAppInfo: {}, LevelVersion: {}}
	for _, code := range codes {
		if remove[code] {
			if _, ok := want[code]; ok {
				p.errorf(code, "", IssueRemoveConflict, "locale %s is both desired and listed in remove", code)
				continue
			}
			p.planDelete(remote, code, touched)
			continue
		}
		p.planLocale(remote, code, want[code], opts, touched)
	}
	for _, level := range Levels {
		for code := range remote.Locales {
			if remote.LocalizationID(level, code) != "" && !touched[level][code] {
				p.Counts.Unchanged++
			}
		}
	}
	if len(p.Errors) > 0 {
		return p, fmt.Errorf("%w: %d error(s), first: %s", ErrInvalidListing, len(p.Errors), p.Errors[0].Message)
	}
	return p, nil
}

// normalizeDesired maps desired locale keys to ASC codes, recording issues.
func normalizeDesired(desired *Listing, p *Plan) map[string]*LocaleFields {
	want := map[string]*LocaleFields{}
	from := map[string]string{}
	for _, raw := range desired.SortedLocales() {
		code, err := applemeta.NormalizeLocale(raw)
		if err != nil {
			p.errorf(raw, "", IssueUnknownLocale, "%v", err)
			continue
		}
		if prev, dup := from[code]; dup {
			p.errorf(raw, "", IssueDuplicateLocale, "locales %q and %q both map to %s", prev, raw, code)
			continue
		}
		from[code] = raw
		if lf := desired.Locales[raw]; lf != nil {
			cp := *lf
			want[code] = &cp
		} else {
			want[code] = &LocaleFields{}
		}
	}
	return want
}

func (p *Plan) planDelete(remote *Listing, code string, touched map[Level]map[string]bool) {
	found := false
	for _, level := range Levels {
		id := remote.LocalizationID(level, code)
		if id == "" {
			continue
		}
		found = true
		touched[level][code] = true
		p.Changes = append(p.Changes, Change{Locale: code, Level: level, Op: OpDelete, ID: id})
		p.Counts.Deletes++
	}
	if !found {
		p.warnf(code, "", IssueRemoveMissing, "locale %s is not on App Store Connect; nothing to remove", code)
	}
}

func (p *Plan) planLocale(remote *Listing, code string, lf *LocaleFields, opts DiffOptions, touched map[Level]map[string]bool) {
	if _, ok := lf.Get("whatsNew"); ok && opts.FirstVersion {
		lf.Clear("whatsNew")
		p.warnf(code, "whatsNew", IssueWhatsNewFirst, "whatsNew dropped: an app's first version has no release notes")
	}
	rf := remote.Locales[code]
	for _, level := range Levels {
		if !lf.has(level) {
			continue
		}
		if !remote.pulled(level) {
			p.errorf(code, "", IssueLevelUnavailable, "%s-level fields given for %s but that level was not pulled", level, code)
			continue
		}
		id := remote.LocalizationID(level, code)
		var changes []FieldChange
		for _, f := range Fields {
			v, ok := lf.Get(f.Key)
			if f.Level != level || !ok {
				continue
			}
			p.validate(code, f, v)
			old, had := rf.Get(f.Key)
			if id != "" && had && old == v {
				continue
			}
			fc := FieldChange{Field: f.Key, New: v}
			if id != "" && had {
				fc.Old = &old
			}
			changes = append(changes, fc)
		}
		if id == "" {
			p.checkComplete(code, level, lf)
			p.Changes = append(p.Changes, Change{Locale: code, Level: level, Op: OpCreate, Fields: changes})
			p.Counts.Creates++
			continue
		}
		touched[level][code] = true
		if len(changes) == 0 {
			p.Counts.Unchanged++
			continue
		}
		p.Changes = append(p.Changes, Change{Locale: code, Level: level, Op: OpUpdate, ID: id, Fields: changes})
		p.Counts.Updates++
	}
}

// checkComplete warns when a new localization lacks fields App Review needs.
func (p *Plan) checkComplete(code string, level Level, lf *LocaleFields) {
	required := []string{"description", "keywords"}
	if level == LevelAppInfo {
		required = []string{"name"}
	}
	for _, k := range required {
		if v, ok := lf.Get(k); !ok || v == "" {
			p.warnf(code, k, IssueIncompleteLocale, "new %s localization %s has no %s; App Review requires it", level, code, k)
		}
	}
}

func (p *Plan) validate(code string, f FieldSpec, v string) {
	if f.Limit != "" {
		if err := applemeta.Check(f.Limit, v); err != nil {
			p.errorf(code, f.Key, IssueTooLong, "%s %s: %v", code, f.Key, err)
		}
	}
	if f.URL && v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			p.errorf(code, f.Key, IssueInvalidURL, "%s %s: %q is not an absolute http(s) URL", code, f.Key, v)
		}
	}
}

func (p *Plan) errorf(locale, field, code, format string, args ...any) {
	p.Errors = append(p.Errors, Issue{Locale: locale, Field: field, Code: code, Message: fmt.Sprintf(format, args...)})
}

func (p *Plan) warnf(locale, field, code, format string, args ...any) {
	p.Warnings = append(p.Warnings, Issue{Locale: locale, Field: field, Code: code, Message: fmt.Sprintf(format, args...)})
}
