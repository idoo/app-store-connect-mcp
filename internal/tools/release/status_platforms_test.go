package release

import (
	"slices"
	"strings"
	"testing"
)

func hasItem(items []string, sub string) bool {
	return slices.ContainsFunc(items, func(s string) bool { return strings.Contains(s, sub) })
}

// A build whose App bundle ships to Apple Watch needs a Watch screenshot.
func TestReleaseStatusWatchBundleRequiresWatchShots(t *testing.T) {
	f := readyFixture()
	f.devices = []string{"iPhone17,1", "iPad16,3-A", "Watch7,1", "Universal"}
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "screenshots"); c.Status != statusBlock || !hasItem(c.Items, "no screenshots for APP_WATCH_ULTRA or") {
		t.Errorf("watch bundle without watch shots: %+v", c)
	}

	f.extraDisplays = []string{"APP_WATCH_SERIES_10"}
	out, _ = call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "screenshots"); c.Status != statusOK {
		t.Errorf("watch bundle with watch shots: %+v", c)
	}
}

// A watch-only app is not asked for iPhone (or iPad) screenshots.
func TestReleaseStatusWatchOnlyNotBlockedForIPhone(t *testing.T) {
	f := fixture{locales: []string{"en-US"}, noIPhoneShots: true, noIPad: true, devices: []string{"Watch7,1", "Watch6,14"}, extraDisplays: []string{"APP_WATCH_ULTRA"}}
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "screenshots"); c.Status != statusOK || hasItem(c.Items, "APP_IPHONE") || hasItem(c.Items, "iPad") {
		t.Errorf("watch-only app: %+v", c)
	}
}

// An iMessage-only app (only iMessage sets) is not asked for app iPhone
// screenshots; its iMessage iPhone slot is required instead.
func TestReleaseStatusIMessageOnlyNotBlockedForIPhone(t *testing.T) {
	f := fixture{locales: []string{"en-US"}, noIPhoneShots: true, noIPad: true, extraDisplays: []string{"IMESSAGE_APP_IPHONE_67"}}
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "screenshots"); c.Status != statusOK || hasItem(c.Items, "APP_IPHONE_67") {
		t.Errorf("iMessage-only app: %+v", c)
	}

	f.extraDisplays = []string{"IMESSAGE_APP_IPAD_PRO_3GEN_129"}
	out, _ = call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "screenshots"); c.Status != statusBlock || !hasItem(c.Items, "IMESSAGE_APP_IPHONE_67") {
		t.Errorf("iMessage-only app without the iPhone iMessage slot: %+v", c)
	}
}

// Thinning lists iPad models for iPhone-only builds too (live: an iPhone-only
// version in review lists 84 iPad models), so they do not turn the iPad
// warning into a block.
func TestReleaseStatusIPadModelsStayWarn(t *testing.T) {
	f := readyFixture()
	f.noIPad = true
	f.devices = []string{"iPhone17,1", "iPad16,3-A"}
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "screenshots"); c.Status != statusWarn {
		t.Errorf("iPad models without iPad shots: %+v", c)
	}
}
