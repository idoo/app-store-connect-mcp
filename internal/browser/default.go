package browser

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// defaultBrowser is the system's default web browser. Name is "" when it is
// unknown; Exe is "" when it could not be resolved.
type defaultBrowser struct {
	Name     string
	Exe      string
	Chromium bool
}

type knownBrowser struct {
	name     string
	chromium bool
}

// macBundleIDs maps lowercase bundle ids (LaunchServices may store them
// lowercased) to browsers.
var macBundleIDs = map[string]knownBrowser{
	"com.google.chrome":                   {"Google Chrome", true},
	"com.google.chrome.beta":              {"Google Chrome Beta", true},
	"com.google.chrome.dev":               {"Google Chrome Dev", true},
	"com.google.chrome.canary":            {"Google Chrome Canary", true},
	"com.brave.browser":                   {"Brave Browser", true},
	"com.brave.browser.beta":              {"Brave Browser Beta", true},
	"com.brave.browser.nightly":           {"Brave Browser Nightly", true},
	"com.microsoft.edgemac":               {"Microsoft Edge", true},
	"com.microsoft.edgemac.beta":          {"Microsoft Edge Beta", true},
	"com.microsoft.edgemac.dev":           {"Microsoft Edge Dev", true},
	"com.microsoft.edgemac.canary":        {"Microsoft Edge Canary", true},
	"org.chromium.chromium":               {"Chromium", true},
	"com.vivaldi.vivaldi":                 {"Vivaldi", true},
	"com.operasoftware.opera":             {"Opera", true},
	"company.thebrowser.browser":          {"Arc", true},
	"com.apple.safari":                    {"Safari", false},
	"com.apple.safaritechnologypreview":   {"Safari Technology Preview", false},
	"org.mozilla.firefox":                 {"Firefox", false},
	"org.mozilla.firefoxdeveloperedition": {"Firefox Developer Edition", false},
	"org.mozilla.nightly":                 {"Firefox Nightly", false},
}

func macBrowser(bundleID string) (knownBrowser, bool) {
	b, ok := macBundleIDs[strings.ToLower(strings.TrimSpace(bundleID))]
	return b, ok
}

// httpsHandlerFromPlistJSON returns the bundle id handling https from the
// JSON form of com.apple.launchservices.secure.plist.
func httpsHandlerFromPlistJSON(b []byte) string {
	var doc struct {
		LSHandlers []struct {
			Scheme string `json:"LSHandlerURLScheme"`
			Role   string `json:"LSHandlerRoleAll"`
		} `json:"LSHandlers"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return ""
	}
	for _, h := range doc.LSHandlers {
		if strings.EqualFold(h.Scheme, "https") && h.Role != "" && h.Role != "-" {
			return h.Role
		}
	}
	return ""
}

// winProgIDs maps Windows UserChoice ProgId prefixes to browsers. Some ProgIds
// carry a per-install suffix (ChromiumHTM.ABC123, FirefoxURL-308046B0AF4A39CB).
var winProgIDs = []struct {
	prefix string
	b      knownBrowser
}{
	{"ChromeHTML", knownBrowser{"Google Chrome", true}},
	{"ChromeBHTML", knownBrowser{"Google Chrome Beta", true}},
	{"ChromeDHTML", knownBrowser{"Google Chrome Dev", true}},
	{"ChromeSSHTM", knownBrowser{"Google Chrome Canary", true}},
	{"BraveHTML", knownBrowser{"Brave Browser", true}},
	{"BraveBHTML", knownBrowser{"Brave Browser Beta", true}},
	{"BraveDHTML", knownBrowser{"Brave Browser Dev", true}},
	{"BraveSSHTM", knownBrowser{"Brave Browser Nightly", true}},
	{"MSEdgeHTM", knownBrowser{"Microsoft Edge", true}},
	{"MSEdgeBHTML", knownBrowser{"Microsoft Edge Beta", true}},
	{"MSEdgeDHTML", knownBrowser{"Microsoft Edge Dev", true}},
	{"MSEdgeCHTML", knownBrowser{"Microsoft Edge Canary", true}},
	{"ChromiumHTM", knownBrowser{"Chromium", true}},
	{"VivaldiHTM", knownBrowser{"Vivaldi", true}},
	{"OperaStable", knownBrowser{"Opera", true}},
	{"Opera GXStable", knownBrowser{"Opera GX", true}},
	{"FirefoxURL", knownBrowser{"Firefox", false}},
	{"IE.HTTP", knownBrowser{"Internet Explorer", false}},
	{"AppXq0fevzme2pys62n3e0fbqa7peapykr8v", knownBrowser{"Microsoft Edge (legacy)", false}},
}

func progIDBrowser(progID string) (knownBrowser, bool) {
	best := -1
	for i, p := range winProgIDs {
		if strings.HasPrefix(strings.ToLower(progID), strings.ToLower(p.prefix)) && (best < 0 || len(p.prefix) > len(winProgIDs[best].prefix)) {
			best = i
		}
	}
	if best < 0 {
		return knownBrowser{}, false
	}
	return winProgIDs[best].b, true
}

// commandExe extracts the executable from a shell\open\command value such as
// `"C:\Program Files\...\brave.exe" --single-argument %1`.
func commandExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if i := strings.Index(cmd[1:], `"`); i >= 0 {
			return cmd[1 : 1+i]
		}
		return strings.Trim(cmd, `"`)
	}
	if i := strings.Index(strings.ToLower(cmd), ".exe"); i >= 0 {
		return cmd[:i+len(".exe")]
	}
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		return cmd[:i]
	}
	return cmd
}

// linuxDesktops maps .desktop ids (without the suffix) to browsers and the
// binaries that run them.
var linuxDesktops = map[string]struct {
	b    knownBrowser
	bins []string
}{
	"google-chrome":         {knownBrowser{"Google Chrome", true}, []string{"google-chrome", "google-chrome-stable"}},
	"google-chrome-beta":    {knownBrowser{"Google Chrome Beta", true}, []string{"google-chrome-beta"}},
	"brave-browser":         {knownBrowser{"Brave Browser", true}, []string{"brave-browser", "brave"}},
	"com.brave.browser":     {knownBrowser{"Brave Browser", true}, []string{"brave-browser", "brave"}},
	"microsoft-edge":        {knownBrowser{"Microsoft Edge", true}, []string{"microsoft-edge", "microsoft-edge-stable"}},
	"chromium":              {knownBrowser{"Chromium", true}, []string{"chromium", "chromium-browser"}},
	"chromium-browser":      {knownBrowser{"Chromium", true}, []string{"chromium-browser", "chromium"}},
	"org.chromium.chromium": {knownBrowser{"Chromium", true}, []string{"chromium", "chromium-browser"}},
	"vivaldi-stable":        {knownBrowser{"Vivaldi", true}, []string{"vivaldi-stable", "vivaldi"}},
	"opera":                 {knownBrowser{"Opera", true}, []string{"opera"}},
	"firefox":               {knownBrowser{"Firefox", false}, nil},
	"firefox-esr":           {knownBrowser{"Firefox", false}, nil},
	"org.mozilla.firefox":   {knownBrowser{"Firefox", false}, nil},
}

func desktopBrowser(desktop string) (knownBrowser, []string, bool) {
	id := strings.TrimSuffix(strings.TrimSpace(desktop), ".desktop")
	d, ok := linuxDesktops[strings.ToLower(id)]
	return d.b, d.bins, ok
}

// binaryNames gives display names for non-bundle executables.
var binaryNames = map[string]string{
	"chrome": "Google Chrome", "google-chrome": "Google Chrome", "google-chrome-stable": "Google Chrome",
	"brave": "Brave Browser", "brave-browser": "Brave Browser", "brave-browser-stable": "Brave Browser",
	"msedge": "Microsoft Edge", "microsoft-edge": "Microsoft Edge", "microsoft-edge-stable": "Microsoft Edge",
	"chromium": "Chromium", "chromium-browser": "Chromium",
	"vivaldi": "Vivaldi", "vivaldi-stable": "Vivaldi", "opera": "Opera",
}

// displayName names the browser behind an executable for progress lines.
func displayName(exe string) string {
	if b := appBundle(exe); b != "" {
		return strings.TrimSuffix(filepath.Base(b), ".app")
	}
	base := filepath.Base(strings.ReplaceAll(exe, `\`, "/"))
	key := strings.TrimSuffix(strings.ToLower(base), ".exe")
	if n, ok := binaryNames[key]; ok {
		return n
	}
	return base
}
