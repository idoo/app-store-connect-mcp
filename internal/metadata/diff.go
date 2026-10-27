package metadata

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
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
	IssueLocaleMapped     = "locale_mapped"
	IssueLockedOnLive     = "field_locked_on_live"
	IssueUnknownSourceKey = "unknown_source_key"
	IssueKeywordsSpaces   = "keywords_spaces"
	IssueKeywordsDup      = "keywords_duplicate"
	IssueKeywordsEmpty    = "keywords_empty_token"
	IssueKeywordsInName   = "keywords_in_name"
	IssueDefaultApplied   = "fastlane_default_applied"
	IssueDefaultOnly      = "fastlane_default_only"
)

// DiffOptions tunes [Diff].
type DiffOptions struct {
	// Remove lists locales to delete. Nothing else is ever deleted.
	Remove []string
	// FirstVersion drops whatsNew (Apple rejects it on an app's first version).
	FirstVersion bool
	// PromoOnly is set when the version is live or in review: Apple accepts
	// only promotionalText there, so other version fields (and app-info
	// fields when no app info is editable) become warnings, not changes.
	PromoOnly bool
	// AppInfoState is the state of the live app info when none is editable.
	// If its localizations were pulled (read-only) unchanged app-info fields
	// are skipped and changes or new locales are errors; if not pulled, any
	// app-info field is an error.
	AppInfoState string
	// AppInfoLocked is the state of an app info in review or approved whose
	// localizations were pulled read-only: unchanged app-info fields are
	// skipped, changed ones and new locales are errors.
	AppInfoLocked string
	// Blocker names the version holding the app-info lock, e.g. "MAC_OS 3.6";
	// "" when unknown.
	Blocker string
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
	p := &Plan{Changes: []Change{}, Warnings: slices.Clone(desired.Notes), fingerprint: fp}

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

	// Union of desired and removed locales, built without capacity arithmetic
	// (len(want)+len(remove) was CodeQL go/allocation-size-overflow #1).
	codes := slices.Collect(maps.Keys(want))
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
		if applemeta.LocaleFolded(raw, code) {
			p.Warnings = append(p.Warnings, foldedIssue(raw, code))
		}
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
	if opts.PromoOnly {
		p.lockToPromo(remote, code, lf, rf, opts)
	}
	if p.requireName(remote, code, lf, opts) {
		return
	}
	if kw, ok := lf.Get("keywords"); ok {
		p.lintKeywords(code, kw, lf, rf)
	}
	for _, level := range Levels {
		if !lf.has(level) {
			continue
		}
		if !remote.pulled(level) {
			if level == LevelAppInfo && opts.AppInfoState != "" {
				p.errorf(code, "", IssueLevelUnavailable, "app info is %s, so name, subtitle and privacy fields of %s can't change until a new version is being prepared", opts.AppInfoState, code)
				continue
			}
			p.errorf(code, "", IssueLevelUnavailable, "%s-level fields given for %s but that level was not pulled", level, code)
			continue
		}
		if level == LevelAppInfo && (opts.AppInfoLocked != "" || opts.AppInfoState != "") {
			p.diffLockedInfo(remote, code, lf, rf, opts, touched)
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
			if id == "" && v == "" {
				continue // a new localization never sends empty fields
			}
			if id != "" && sameValue(rf, f.Key, v) {
				continue
			}
			fc := FieldChange{Field: f.Key, New: v}
			if old, had := rf.Get(f.Key); id != "" && had {
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

// checkComplete warns when a new version localization lacks fields App
// Review needs (a missing name is an error, see requireName).
func (p *Plan) checkComplete(code string, level Level, lf *LocaleFields) {
	if level != LevelVersion {
		return
	}
	for _, k := range []string{"description", "keywords"} {
		if v, ok := lf.Get(k); !ok || v == "" {
			p.warnf(code, k, IssueIncompleteLocale, "new %s localization %s has no %s; App Review requires it", level, code, k)
		}
	}
}

// requireName rejects a locale that would be created without a name: Apple
// needs name to create an appInfoLocalization, and a version localization
// without one leaves the locale with no store name. While app info is locked
// no locale can be added at all. It reports whether the locale is blocked.
func (p *Plan) requireName(remote *Listing, code string, lf *LocaleFields, opts DiffOptions) bool {
	if !remote.pulled(LevelAppInfo) || remote.LocalizationID(LevelAppInfo, code) != "" {
		return false
	}
	creating := false
	for _, level := range Levels {
		if lf.has(level) && remote.pulled(level) && remote.LocalizationID(level, code) == "" {
			creating = true
		}
	}
	if !creating {
		return false
	}
	if opts.AppInfoLocked != "" {
		wait := "the version in review"
		if opts.Blocker != "" {
			wait = opts.Blocker
		}
		p.errorf(code, "", IssueLevelUnavailable, "can't add %s while app info is %s; wait for %s review", code, opts.AppInfoLocked, wait)
		return true
	}
	if opts.AppInfoState != "" {
		p.errorf(code, "", IssueLevelUnavailable, "can't add %s while app info is %s; create a new version with release_prepare first", code, opts.AppInfoState)
		return true
	}
	if v, ok := lf.Get("name"); !ok || strings.TrimSpace(v) == "" {
		p.errorf(code, "name", IssueIncompleteLocale, "new locale %s needs a name (fastlane name.txt or default/name.txt, Expo title); without it the locale is created half-way", code)
	}
	return false
}

// diffLockedInfo compares app-info fields against a read-only app info
// (locked in review, or live with nothing editable): unchanged fields are
// skipped, each changed field is an error explaining the lock.
func (p *Plan) diffLockedInfo(remote *Listing, code string, lf, rf *LocaleFields, opts DiffOptions, touched map[Level]map[string]bool) {
	if remote.LocalizationID(LevelAppInfo, code) == "" {
		return // new locales are rejected by requireName
	}
	touched[LevelAppInfo][code] = true
	changed := false
	for _, f := range Fields {
		v, ok := lf.Get(f.Key)
		if f.Level != LevelAppInfo || !ok {
			continue
		}
		if sameValue(rf, f.Key, v) {
			continue
		}
		changed = true
		if opts.AppInfoLocked == "" {
			p.errorf(code, f.Key, IssueLevelUnavailable, "app info is %s, so %s of %s can't change until a new version is being prepared", opts.AppInfoState, f.Key, code)
			continue
		}
		why := "a version is in review"
		if opts.Blocker != "" {
			why = opts.Blocker + " is in review"
		}
		p.errorf(code, f.Key, IssueLevelUnavailable, "app info is %s because %s; %s can change when it is released or rejected", opts.AppInfoLocked, why, f.Key)
	}
	if !changed {
		p.Counts.Unchanged++
	}
}

// sameValue reports whether desired v matches the remote field key; an
// empty desired value equals an absent or null remote one, so exports with
// empty files (fastlane writes one per unset field) converge.
func sameValue(rf *LocaleFields, key, v string) bool {
	old, had := rf.Get(key)
	if !had {
		return v == ""
	}
	return old == v
}

// lockToPromo drops every field Apple refuses on a live or in-review
// version: version fields other than promotionalText, app-info fields when
// no app info is editable, and any new localization. Differing values are
// reported as one warning per locale.
func (p *Plan) lockToPromo(remote *Listing, code string, lf, rf *LocaleFields, opts DiffOptions) {
	var locked []string
	for _, f := range Fields {
		v, ok := lf.Get(f.Key)
		if !ok {
			continue
		}
		keep := false
		switch f.Level {
		case LevelVersion:
			keep = f.Key == "promotionalText" && remote.LocalizationID(LevelVersion, code) != ""
		case LevelAppInfo:
			keep = remote.pulled(LevelAppInfo) && opts.AppInfoLocked == "" && opts.AppInfoState == ""
		}
		if keep {
			continue
		}
		lf.Clear(f.Key)
		if !sameValue(rf, f.Key, v) {
			locked = append(locked, f.Key)
		}
	}
	if len(locked) > 0 {
		p.warnf(code, "", IssueLockedOnLive, "%s skipped: on a live or in-review version only promotionalText of an existing locale can change (%s)", code, strings.Join(locked, ", "))
	}
}

// lintKeywords warns about keyword characters wasted on spaces after commas,
// empty or duplicate tokens, and words the name or subtitle already index.
func (p *Plan) lintKeywords(code, v string, lf, rf *LocaleFields) {
	if v == "" {
		return
	}
	names := map[string]bool{}
	for _, k := range []string{"name", "subtitle"} {
		s, ok := lf.Get(k)
		if !ok {
			s, _ = rf.Get(k)
		}
		for _, w := range words(s) {
			names[w] = true
		}
	}
	seen := map[string]bool{}
	var kept, dups, inName []string
	spaces, empty := false, 0
	for _, tok := range strings.Split(v, ",") {
		t := strings.TrimSpace(tok)
		if t != tok {
			spaces = true
		}
		if t == "" {
			empty++
			continue
		}
		k := strings.ToLower(t)
		if seen[k] {
			dups = append(dups, t)
			continue
		}
		seen[k] = true
		kept = append(kept, t)
		if ws := words(t); len(ws) > 0 && !slices.ContainsFunc(ws, func(w string) bool { return !names[w] }) {
			inName = append(inName, t)
		}
	}
	norm := strings.Join(kept, ",")
	saved := utf8.RuneCountInString(v) - utf8.RuneCountInString(norm)
	if spaces {
		p.warnf(code, "keywords", IssueKeywordsSpaces, "spaces around commas waste characters; use %q (saves %d)", norm, saved)
	}
	if empty > 0 {
		p.warnf(code, "keywords", IssueKeywordsEmpty, "%d empty keyword(s) (double or trailing commas); use %q (saves %d characters)", empty, norm, saved)
	}
	if len(dups) > 0 {
		p.warnf(code, "keywords", IssueKeywordsDup, "duplicate keyword(s) %s; use %q (saves %d characters)", strings.Join(dups, ", "), norm, saved)
	}
	if len(inName) > 0 {
		n := 0
		for _, t := range inName {
			n += utf8.RuneCountInString(t) + 1
		}
		p.warnf(code, "keywords", IssueKeywordsInName, "%s already in the name or subtitle, which Apple indexes too; dropping frees %d characters", strings.Join(inName, ", "), n)
	}
}

// words splits s into lowercased letter/digit runs.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
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
