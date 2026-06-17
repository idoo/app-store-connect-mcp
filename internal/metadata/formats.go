package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
)

// FromJSON parses a desired listing. The shape is
//
//	{"locales": {"en-US": {"description": "…", "keywords": "a,b", "whatsNew": "…"}}}
//
// with the field keys of [LocaleFields]; a bare {"en-US": {...}} map is also
// accepted. Unknown field keys are errors; locale keys are normalized.
func FromJSON(b []byte) (*Listing, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, fmt.Errorf("%w: metadata JSON: %v", tool.ErrInvalidArgs, err)
	}
	raw := top
	if loc, ok := top["locales"]; ok {
		if len(top) != 1 {
			return nil, fmt.Errorf("%w: metadata JSON: only the \"locales\" key is allowed beside it", tool.ErrInvalidArgs)
		}
		raw = nil
		if err := json.Unmarshal(loc, &raw); err != nil {
			return nil, fmt.Errorf("%w: metadata JSON locales: %v", tool.ErrInvalidArgs, err)
		}
	}
	l := &Listing{Locales: make(map[string]*LocaleFields, len(raw))}
	for code, v := range raw {
		lf := &LocaleFields{}
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.DisallowUnknownFields()
		if err := dec.Decode(lf); err != nil {
			return nil, fmt.Errorf("%w: metadata JSON locale %q: %v", tool.ErrInvalidArgs, code, err)
		}
		l.Locales[code] = lf
	}
	n, err := l.Normalize()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
	}
	return n, nil
}

// fastlaneDefault is deliver's directory of values for every locale.
const fastlaneDefault = "default"

// fastlaneNonLocale lists deliver metadata subdirectories that are not locales.
var fastlaneNonLocale = []string{"review_information", "trade_representative_contact_information"}

// FromFastlane reads a fastlane deliver metadata directory:
// <dir>/<locale>/{name,subtitle,description,keywords,release_notes,
// promotional_text,marketing_url,support_url,privacy_url,
// apple_tv_privacy_policy}.txt. Locale directory names are normalized; values
// in <dir>/default fill fields a locale lacks. A present file sets its field
// (trailing whitespace trimmed; an empty file clears it); a missing file
// keeps the remote value.
func FromFastlane(files tool.Files, dir string) (*Listing, error) {
	root, err := files.Resolve(dir)
	if err != nil {
		return nil, fmt.Errorf("metadata: fastlane dir: %w", err)
	}
	byFile := map[string]string{}
	for _, f := range Fields {
		if f.Fastlane != "" {
			byFile[f.Fastlane+".txt"] = f.Key
		}
	}
	raw := map[string]*LocaleFields{}
	err = files.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		switch {
		case rel == ".":
			return nil
		case len(parts) == 1 && d.IsDir():
			if slices.Contains(fastlaneNonLocale, parts[0]) {
				return fs.SkipDir
			}
			return nil
		case len(parts) == 1:
			return nil // copyright.txt, primary_category.txt …
		case d.IsDir():
			return fs.SkipDir
		}
		key, ok := byFile[parts[1]]
		if !ok {
			return nil
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		lf, ok := raw[parts[0]]
		if !ok {
			lf = &LocaleFields{}
			raw[parts[0]] = lf
		}
		return lf.Set(key, strings.TrimRight(string(b), " \t\r\n"))
	})
	if err != nil {
		return nil, fmt.Errorf("metadata: read fastlane dir %s: %w", dir, err)
	}
	def := raw[fastlaneDefault]
	delete(raw, fastlaneDefault)
	l := &Listing{Locales: raw}
	if def != nil {
		for _, lf := range raw {
			for _, f := range Fields {
				if v, ok := def.Get(f.Key); ok {
					if _, has := lf.Get(f.Key); !has {
						_ = lf.Set(f.Key, v) // key comes from Fields
					}
				}
			}
		}
	}
	return l.Normalize()
}

// ToFastlane writes l in deliver layout under dir and returns the written
// paths. Fields without a deliver file (privacyChoicesUrl) are skipped;
// existing files of other fields are left untouched.
func ToFastlane(files tool.Files, dir string, l *Listing) ([]string, error) {
	var written []string
	for _, code := range l.SortedLocales() {
		if !applemeta.IsLocale(code) {
			return written, fmt.Errorf("metadata: %q is not an ASC locale code", code)
		}
		lf := l.Locales[code]
		for _, f := range Fields {
			v, ok := lf.Get(f.Key)
			if !ok || f.Fastlane == "" {
				continue
			}
			p := filepath.Join(dir, code, f.Fastlane+".txt")
			if err := files.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
				return written, fmt.Errorf("metadata: write %s: %w", p, err)
			}
			written = append(written, p)
		}
	}
	return written, nil
}

// FromExpo reads EAS Metadata's store.config.json at path:
// apple.info.<locale>.{title,subtitle,description,keywords[],releaseNotes,
// promoText,marketingUrl,supportUrl,privacyPolicyUrl,privacyChoicesUrl,
// privacyPolicyText}. Keywords are joined with commas; other keys are ignored.
func FromExpo(files tool.Files, path string) (*Listing, error) {
	b, err := files.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("metadata: read %s: %w", path, err)
	}
	var doc struct {
		Apple struct {
			Info map[string]map[string]json.RawMessage `json:"info"`
		} `json:"apple"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", tool.ErrInvalidArgs, path, err)
	}
	if doc.Apple.Info == nil {
		return nil, fmt.Errorf("%w: %s has no apple.info", tool.ErrInvalidArgs, path)
	}
	l := &Listing{Locales: map[string]*LocaleFields{}}
	for code, info := range doc.Apple.Info {
		lf := &LocaleFields{}
		for _, f := range Fields {
			v, ok := info[f.Expo]
			if !ok || f.Expo == "" {
				continue
			}
			s, err := expoValue(f.Key, v)
			if err != nil {
				return nil, fmt.Errorf("%w: %s apple.info.%s.%s: %v", tool.ErrInvalidArgs, path, code, f.Expo, err)
			}
			_ = lf.Set(f.Key, s) // key comes from Fields
		}
		l.Locales[code] = lf
	}
	return l.Normalize()
}

func expoValue(key string, v json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s, nil
	}
	if key == "keywords" {
		var kws []string
		if err := json.Unmarshal(v, &kws); err == nil {
			return strings.Join(kws, ","), nil
		}
	}
	return "", errors.New("expected a string")
}
