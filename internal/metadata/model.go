// Package metadata is the App Store listing-metadata engine shared by
// release_prepare (What's New) and the metadata_* tools: it pulls version and
// app-info localizations, diffs a desired listing against them, applies the
// resulting plan, and reads/writes the fastlane, Expo and JSON formats.
//
// Absent fields always mean "keep the remote value"; a locale is deleted only
// when it is listed explicitly in [DiffOptions.Remove].
package metadata

import (
	"fmt"
	"slices"
	"sort"

	"github.com/idoo/hamn/internal/applemeta"
)

// Level is the App Store Connect resource a localized field lives on.
type Level string

// Localization levels.
const (
	// LevelAppInfo is appInfoLocalizations (name, subtitle, privacy).
	LevelAppInfo Level = "appInfo"
	// LevelVersion is appStoreVersionLocalizations (description, keywords...).
	LevelVersion Level = "version"
)

// Levels lists the levels in plan order.
var Levels = []Level{LevelAppInfo, LevelVersion}

// FieldSpec describes one localized listing field.
type FieldSpec struct {
	// Key is the ASC attribute name and the JSON key of [LocaleFields].
	Key   string
	Level Level
	// Limit is the applemeta length limit ("" when Apple sets none).
	Limit applemeta.Field
	// URL fields must be absolute http(s) URLs when non-empty.
	URL bool
	// Fastlane is the deliver file name without ".txt" ("" when deliver has none).
	Fastlane string
	// Expo is the key under apple.info.<locale> in EAS store.config.json.
	Expo string
}

// Fields is the localized field table, in display order.
var Fields = []FieldSpec{
	{Key: "name", Level: LevelAppInfo, Limit: applemeta.FieldName, Fastlane: "name", Expo: "title"},
	{Key: "subtitle", Level: LevelAppInfo, Limit: applemeta.FieldSubtitle, Fastlane: "subtitle", Expo: "subtitle"},
	{Key: "privacyPolicyUrl", Level: LevelAppInfo, URL: true, Fastlane: "privacy_url", Expo: "privacyPolicyUrl"},
	{Key: "privacyChoicesUrl", Level: LevelAppInfo, URL: true, Expo: "privacyChoicesUrl"},
	{Key: "privacyPolicyText", Level: LevelAppInfo, Fastlane: "apple_tv_privacy_policy", Expo: "privacyPolicyText"},
	{Key: "description", Level: LevelVersion, Limit: applemeta.FieldDescription, Fastlane: "description", Expo: "description"},
	{Key: "keywords", Level: LevelVersion, Limit: applemeta.FieldKeywords, Fastlane: "keywords", Expo: "keywords"},
	{Key: "marketingUrl", Level: LevelVersion, URL: true, Fastlane: "marketing_url", Expo: "marketingUrl"},
	{Key: "promotionalText", Level: LevelVersion, Limit: applemeta.FieldPromotionalText, Fastlane: "promotional_text", Expo: "promoText"},
	{Key: "supportUrl", Level: LevelVersion, URL: true, Fastlane: "support_url", Expo: "supportUrl"},
	{Key: "whatsNew", Level: LevelVersion, Limit: applemeta.FieldWhatsNew, Fastlane: "release_notes", Expo: "releaseNotes"},
}

// LocaleFields holds one locale's listing fields. A nil field is absent
// (keep the remote value); a non-nil empty string clears the field.
type LocaleFields struct {
	// App-info level.
	Name              *string `json:"name,omitempty"`
	Subtitle          *string `json:"subtitle,omitempty"`
	PrivacyPolicyURL  *string `json:"privacyPolicyUrl,omitempty"`
	PrivacyChoicesURL *string `json:"privacyChoicesUrl,omitempty"`
	PrivacyPolicyText *string `json:"privacyPolicyText,omitempty"`
	// Version level.
	Description     *string `json:"description,omitempty"`
	Keywords        *string `json:"keywords,omitempty"`
	MarketingURL    *string `json:"marketingUrl,omitempty"`
	PromotionalText *string `json:"promotionalText,omitempty"`
	SupportURL      *string `json:"supportUrl,omitempty"`
	WhatsNew        *string `json:"whatsNew,omitempty"`
}

// slot returns the storage of the field key, or nil for an unknown key.
func (l *LocaleFields) slot(key string) **string {
	switch key {
	case "name":
		return &l.Name
	case "subtitle":
		return &l.Subtitle
	case "privacyPolicyUrl":
		return &l.PrivacyPolicyURL
	case "privacyChoicesUrl":
		return &l.PrivacyChoicesURL
	case "privacyPolicyText":
		return &l.PrivacyPolicyText
	case "description":
		return &l.Description
	case "keywords":
		return &l.Keywords
	case "marketingUrl":
		return &l.MarketingURL
	case "promotionalText":
		return &l.PromotionalText
	case "supportUrl":
		return &l.SupportURL
	case "whatsNew":
		return &l.WhatsNew
	}
	return nil
}

// Get returns the value of field key and whether it is present.
func (l *LocaleFields) Get(key string) (string, bool) {
	if l == nil {
		return "", false
	}
	p := l.slot(key)
	if p == nil || *p == nil {
		return "", false
	}
	return **p, true
}

// Set stores value for field key. It returns an error for an unknown key.
func (l *LocaleFields) Set(key, value string) error {
	p := l.slot(key)
	if p == nil {
		return fmt.Errorf("metadata: unknown field %q", key)
	}
	*p = &value
	return nil
}

// Clear removes field key (making it absent).
func (l *LocaleFields) Clear(key string) {
	if p := l.slot(key); p != nil {
		*p = nil
	}
}

// has reports whether any field of level is present.
func (l *LocaleFields) has(level Level) bool {
	for _, f := range Fields {
		if _, ok := l.Get(f.Key); ok && f.Level == level {
			return true
		}
	}
	return false
}

// AppInfoFields identifies the remote resources a pulled [Listing] came from.
// It is empty for desired listings read from files or JSON.
type AppInfoFields struct {
	// VersionID is the appStoreVersion pulled ("" when the level was skipped).
	VersionID string `json:"versionId,omitempty"`
	// AppInfoID is the appInfo pulled ("" when the level was skipped).
	AppInfoID string `json:"appInfoId,omitempty"`
	// VersionLocalizationIDs maps locale to appStoreVersionLocalization id.
	VersionLocalizationIDs map[string]string `json:"versionLocalizationIds,omitempty"`
	// AppInfoLocalizationIDs maps locale to appInfoLocalization id.
	AppInfoLocalizationIDs map[string]string `json:"appInfoLocalizationIds,omitempty"`
}

// Listing is a localized App Store listing: remote (from [Pull]) or desired
// (from a reader or a tool argument).
type Listing struct {
	App     AppInfoFields            `json:"app,omitzero"`
	Locales map[string]*LocaleFields `json:"locales"`
	// Notes are reader findings (unknown keys, folded locales) that [Diff]
	// reports as plan warnings.
	Notes []Issue `json:"-"`
}

// LocalizationID returns the remote localization id of locale at level.
func (l *Listing) LocalizationID(level Level, locale string) string {
	if level == LevelAppInfo {
		return l.App.AppInfoLocalizationIDs[locale]
	}
	return l.App.VersionLocalizationIDs[locale]
}

// pulled reports whether the remote level was loaded.
func (l *Listing) pulled(level Level) bool {
	if level == LevelAppInfo {
		return l.App.AppInfoID != ""
	}
	return l.App.VersionID != ""
}

// SortedLocales returns the listing's locale codes in order.
func (l *Listing) SortedLocales() []string {
	out := make([]string, 0, len(l.Locales))
	for k := range l.Locales {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// locale returns the fields for code, creating them when missing.
func (l *Listing) locale(code string) *LocaleFields {
	if l.Locales == nil {
		l.Locales = map[string]*LocaleFields{}
	}
	lf, ok := l.Locales[code]
	if !ok {
		lf = &LocaleFields{}
		l.Locales[code] = lf
	}
	return lf
}

// Normalize returns a copy of l whose locale keys are ASC codes. Unknown
// locales and keys that collide after normalization are errors; regional
// variants folded into another locale (de-CH → de-DE) add a note.
func (l *Listing) Normalize() (*Listing, error) {
	out := &Listing{App: l.App, Locales: make(map[string]*LocaleFields, len(l.Locales)), Notes: slices.Clone(l.Notes)}
	from := map[string]string{}
	for _, raw := range l.SortedLocales() {
		code, err := applemeta.NormalizeLocale(raw)
		if err != nil {
			return nil, fmt.Errorf("metadata: %w", err)
		}
		if prev, dup := from[code]; dup {
			return nil, fmt.Errorf("metadata: locales %q and %q both map to %s", prev, raw, code)
		}
		from[code] = raw
		if applemeta.LocaleFolded(raw, code) {
			out.Notes = append(out.Notes, foldedIssue(raw, code))
		}
		lf := *l.Locales[raw]
		out.Locales[code] = &lf
	}
	return out, nil
}

// foldedIssue is the warning for a regional locale folded into code.
func foldedIssue(raw, code string) Issue {
	return Issue{Locale: code, Code: IssueLocaleMapped, Message: fmt.Sprintf("%s → %s: App Store Connect has no %s listing, so this writes the %s one", raw, code, raw, code)}
}
