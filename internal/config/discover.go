package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Candidate is a credentials profile found on disk or in the environment for
// `app-store-connect-mcp auth import`. Missing lists the fields still to ask the user for.
type Candidate struct {
	// Sources describes where the fields came from, e.g. "fastlane/api_key.json".
	Sources []string
	Profile Profile
	Key     KeyRef
	Missing []string
}

var authKeyName = regexp.MustCompile(`^AuthKey_([A-Z0-9]{6,20})\.p8$`)

// keyDirs are the directories altool and xcrun search for AuthKey_<ID>.p8,
// relative to home.
var keyDirs = []string{
	filepath.Join(".appstoreconnect", "private_keys"),
	"private_keys",
	".private_keys",
}

// Discover finds importable credentials: fastlane/api_key.json in cwd,
// ASC_* and fastlane environment variables, and AuthKey_<ID>.p8 files in the
// altool key directories under home and in cwd/private_keys. Candidates with
// the same key id are merged, earlier sources winning. Results are sorted by
// key id. Unreadable sources are skipped.
func Discover(home, cwd string, getenv func(string) string) []Candidate {
	var found []Candidate
	if c, ok := fastlaneCandidate(cwd); ok {
		found = append(found, c)
	}
	if c, ok, err := envCredentials(getenv); err == nil && ok {
		found = append(found, Candidate{Sources: []string{"environment"}, Profile: c.Profile, Key: c.Key})
	}
	dirs := make([]string, 0, len(keyDirs)+1)
	for _, d := range keyDirs {
		dirs = append(dirs, filepath.Join(home, d))
	}
	dirs = append(dirs, filepath.Join(cwd, "private_keys"))
	for _, d := range dirs {
		found = append(found, keyDirCandidates(d)...)
	}
	return mergeCandidates(found)
}

func fastlaneCandidate(cwd string) (Candidate, bool) {
	path := filepath.Join(cwd, "fastlane", "api_key.json")
	f, err := readFastlaneJSON(path)
	if err != nil || f.KeyID == "" {
		return Candidate{}, false
	}
	c := Candidate{Sources: []string{path}, Profile: Profile{KeyID: f.KeyID, IssuerID: f.IssuerID}}
	switch {
	case f.KeyFilepath != "":
		kp := f.KeyFilepath
		if !filepath.IsAbs(kp) {
			kp = filepath.Join(cwd, kp)
		}
		c.Profile.KeyPath = kp
		c.Key = KeyRef{Source: KeyFromFile, Path: kp, KeyID: f.KeyID}
	case f.Key != "":
		c.Key = KeyRef{Source: KeyFromFastlaneJSON, Path: path, KeyID: f.KeyID}
	}
	return c, true
}

func keyDirCandidates(dir string) []Candidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Candidate
	for _, e := range entries {
		m := authKeyName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		out = append(out, Candidate{
			Sources: []string{path},
			Profile: Profile{KeyID: m[1], KeyPath: path},
			Key:     KeyRef{Source: KeyFromFile, Path: path, KeyID: m[1]},
		})
	}
	return out
}

func mergeCandidates(found []Candidate) []Candidate {
	byID := map[string]*Candidate{}
	var ids []string
	for _, c := range found {
		cur, ok := byID[c.Profile.KeyID]
		if !ok {
			cp := c
			byID[c.Profile.KeyID] = &cp
			ids = append(ids, c.Profile.KeyID)
			continue
		}
		cur.Sources = append(cur.Sources, c.Sources...)
		fill(&cur.Profile.IssuerID, c.Profile.IssuerID)
		fill((*string)(&cur.Profile.KeyKind), string(c.Profile.KeyKind))
		fill(&cur.Profile.VendorNumber, c.Profile.VendorNumber)
		if cur.Key.Source == "" {
			cur.Key = c.Key
			cur.Profile.KeyPath = c.Profile.KeyPath
		}
	}
	sort.Strings(ids)
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		c := byID[id]
		c.Missing = missing(*c)
		out = append(out, *c)
	}
	return out
}

func fill(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

func missing(c Candidate) []string {
	var m []string
	if c.Key.Source == "" {
		m = append(m, "private_key")
	}
	if c.Profile.IssuerID == "" && c.Profile.KeyKind == "" {
		m = append(m, "issuer_id or key_kind")
	}
	if strings.TrimSpace(c.Profile.VendorNumber) == "" {
		m = append(m, "vendor_number")
	}
	return m
}
