package applemeta

import (
	"fmt"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// DisplayType is an App Store Connect ScreenshotDisplayType enum value.
type DisplayType string

// Screenshot display types, exactly as in the pinned spec (4.5.1). There is no
// APP_IPHONE_69: the 6.9" slot is APP_IPHONE_67, and the 13" iPad slot is
// APP_IPAD_PRO_3GEN_129.
const (
	AppIPhone67            DisplayType = "APP_IPHONE_67"
	AppIPhone61            DisplayType = "APP_IPHONE_61"
	AppIPhone65            DisplayType = "APP_IPHONE_65"
	AppIPhone58            DisplayType = "APP_IPHONE_58"
	AppIPhone55            DisplayType = "APP_IPHONE_55"
	AppIPhone47            DisplayType = "APP_IPHONE_47"
	AppIPhone40            DisplayType = "APP_IPHONE_40"
	AppIPhone35            DisplayType = "APP_IPHONE_35"
	AppIPadPro3Gen129      DisplayType = "APP_IPAD_PRO_3GEN_129"
	AppIPadPro3Gen11       DisplayType = "APP_IPAD_PRO_3GEN_11"
	AppIPadPro129          DisplayType = "APP_IPAD_PRO_129"
	AppIPad105             DisplayType = "APP_IPAD_105"
	AppIPad97              DisplayType = "APP_IPAD_97"
	AppDesktop             DisplayType = "APP_DESKTOP"
	WatchUltra             DisplayType = "APP_WATCH_ULTRA"
	WatchSeries10          DisplayType = "APP_WATCH_SERIES_10"
	WatchSeries7           DisplayType = "APP_WATCH_SERIES_7"
	WatchSeries4           DisplayType = "APP_WATCH_SERIES_4"
	WatchSeries3           DisplayType = "APP_WATCH_SERIES_3"
	AppAppleTV             DisplayType = "APP_APPLE_TV"
	AppAppleVisionPro      DisplayType = "APP_APPLE_VISION_PRO"
	IMessageIPhone67       DisplayType = "IMESSAGE_APP_IPHONE_67"
	IMessageIPhone61       DisplayType = "IMESSAGE_APP_IPHONE_61"
	IMessageIPhone65       DisplayType = "IMESSAGE_APP_IPHONE_65"
	IMessageIPhone58       DisplayType = "IMESSAGE_APP_IPHONE_58"
	IMessageIPhone55       DisplayType = "IMESSAGE_APP_IPHONE_55"
	IMessageIPhone47       DisplayType = "IMESSAGE_APP_IPHONE_47"
	IMessageIPhone40       DisplayType = "IMESSAGE_APP_IPHONE_40"
	IMessageIPadPro3Gen129 DisplayType = "IMESSAGE_APP_IPAD_PRO_3GEN_129"
	IMessageIPadPro3Gen11  DisplayType = "IMESSAGE_APP_IPAD_PRO_3GEN_11"
	IMessageIPadPro129     DisplayType = "IMESSAGE_APP_IPAD_PRO_129"
	IMessageIPad105        DisplayType = "IMESSAGE_APP_IPAD_105"
	IMessageIPad97         DisplayType = "IMESSAGE_APP_IPAD_97"
)

// PreviewType is an App Store Connect PreviewType enum value (app preview
// video slot).
type PreviewType string

// Family is the device family a display type belongs to.
type Family string

// Device families.
const (
	FamilyIPhone Family = "iphone"
	FamilyIPad   Family = "ipad"
	FamilyMac    Family = "mac"
	FamilyTV     Family = "appletv"
	FamilyVision Family = "vision"
	FamilyWatch  Family = "watch"
)

// Size is a pixel size in the orientation Apple publishes it.
type Size struct {
	W int `json:"w"`
	H int `json:"h"`
}

// String formats the size as "WxH".
func (s Size) String() string { return fmt.Sprintf("%dx%d", s.W, s.H) }

// DisplaySpec describes one screenshot display type.
type DisplaySpec struct {
	Type     DisplayType   `json:"type"`
	Family   Family        `json:"family"`
	Platform tool.Platform `json:"platform"`
	// Diagonal is the marketing size, e.g. `6.9"`.
	Diagonal string `json:"diagonal"`
	// Sizes are Apple's accepted pixel sizes in published orientation.
	Sizes []Size `json:"sizes"`
	// Rotates reports whether the swapped (landscape/portrait) size is also
	// accepted. Mac, TV and Vision are landscape-only; watch is portrait-only.
	Rotates bool `json:"rotates"`
	// IMessage marks iMessage-extension slots. They are never inferred from
	// pixels, because they share sizes with the app slots.
	IMessage bool `json:"imessage,omitempty"`
	// Legacy marks slots superseded by a newer slot with the same family.
	Legacy bool `json:"legacy,omitempty"`
	// Preview is the matching app preview slot; empty when none exists.
	Preview PreviewType `json:"preview,omitempty"`
	// aliases are friendly names accepted by ParseDisplay (without prefix).
	aliases []string
}

// Pixel sizes per Apple's "Screenshot specifications" (App Store Connect
// Help), portrait for iOS/watch, landscape for Mac/TV/Vision.
var (
	sizesIPhone67  = []Size{{1320, 2868}, {1290, 2796}, {1260, 2736}}
	sizesIPhone65  = []Size{{1284, 2778}, {1242, 2688}}
	sizesIPhone61  = []Size{{1206, 2622}, {1179, 2556}, {1170, 2532}, {1125, 2436}, {1080, 2340}}
	sizesIPhone58  = []Size{{1125, 2436}, {1170, 2532}, {1080, 2340}}
	sizesIPhone55  = []Size{{1242, 2208}}
	sizesIPhone47  = []Size{{750, 1334}}
	sizesIPhone40  = []Size{{640, 1136}, {640, 1096}}
	sizesIPhone35  = []Size{{640, 960}, {640, 920}}
	sizesIPad129   = []Size{{2064, 2752}, {2048, 2732}}
	sizesIPad129v2 = []Size{{2048, 2732}}
	sizesIPad11    = []Size{{1668, 2420}, {1668, 2388}, {1640, 2360}, {1488, 2266}}
	sizesIPad105   = []Size{{1668, 2224}}
	sizesIPad97    = []Size{{1536, 2048}, {1536, 2008}, {768, 1024}, {768, 1004}}
)

func iphone(t DisplayType, diag string, sizes []Size, legacy bool, p PreviewType, aliases ...string) DisplaySpec {
	return DisplaySpec{Type: t, Family: FamilyIPhone, Platform: tool.IOS, Diagonal: diag, Sizes: sizes, Rotates: true, Legacy: legacy, Preview: p, aliases: aliases}
}

func ipad(t DisplayType, diag string, sizes []Size, legacy bool, p PreviewType, aliases ...string) DisplaySpec {
	return DisplaySpec{Type: t, Family: FamilyIPad, Platform: tool.IOS, Diagonal: diag, Sizes: sizes, Rotates: true, Legacy: legacy, Preview: p, aliases: aliases}
}

func watch(t DisplayType, diag string, sizes []Size, aliases ...string) DisplaySpec {
	return DisplaySpec{Type: t, Family: FamilyWatch, Platform: tool.IOS, Diagonal: diag, Sizes: sizes, aliases: aliases}
}

func imessage(base DisplaySpec, t DisplayType) DisplaySpec {
	s := base
	s.Type, s.IMessage, s.Preview, s.aliases = t, true, "", nil
	return s
}

// displaySpecs is ordered newest/preferred first within each family, so
// DisplayForPixels lists the modern slot before a legacy one.
var displaySpecs = func() []DisplaySpec {
	phones := []DisplaySpec{
		iphone(AppIPhone67, `6.9"`, sizesIPhone67, false, "IPHONE_67", "6.9", "6.7", "69", "67"),
		iphone(AppIPhone65, `6.5"`, sizesIPhone65, false, "IPHONE_65", "6.5", "65"),
		iphone(AppIPhone61, `6.3"`, sizesIPhone61, false, "IPHONE_61", "6.3", "6.1", "63", "61"),
		iphone(AppIPhone58, `5.8"`, sizesIPhone58, true, "IPHONE_58", "5.8", "58"),
		iphone(AppIPhone55, `5.5"`, sizesIPhone55, true, "IPHONE_55", "5.5", "55"),
		iphone(AppIPhone47, `4.7"`, sizesIPhone47, true, "IPHONE_47", "4.7", "47"),
		iphone(AppIPhone40, `4"`, sizesIPhone40, true, "IPHONE_40", "4.0", "4", "40"),
		iphone(AppIPhone35, `3.5"`, sizesIPhone35, true, "IPHONE_35", "3.5", "35"),
	}
	pads := []DisplaySpec{
		ipad(AppIPadPro3Gen129, `13"`, sizesIPad129, false, "IPAD_PRO_3GEN_129", "13", "12.9", "129"),
		ipad(AppIPadPro3Gen11, `11"`, sizesIPad11, false, "IPAD_PRO_3GEN_11", "11"),
		ipad(AppIPadPro129, `12.9" (2nd gen)`, sizesIPad129v2, true, "IPAD_PRO_129", "12.9-2gen", "pro-129"),
		ipad(AppIPad105, `10.5"`, sizesIPad105, true, "IPAD_105", "10.5", "105"),
		ipad(AppIPad97, `9.7"`, sizesIPad97, true, "IPAD_97", "9.7", "97"),
	}
	out := slices.Concat(phones, pads, []DisplaySpec{
		{Type: AppDesktop, Family: FamilyMac, Platform: tool.MacOS, Diagonal: "16:10", Sizes: []Size{{1280, 800}, {1440, 900}, {2560, 1600}, {2880, 1800}}, Preview: "DESKTOP", aliases: []string{"mac", "macos", "desktop"}},
		{Type: AppAppleTV, Family: FamilyTV, Platform: tool.TVOS, Diagonal: "16:9", Sizes: []Size{{1920, 1080}, {3840, 2160}}, Preview: "APPLE_TV", aliases: []string{"tv", "appletv", "apple-tv", "tvos"}},
		{Type: AppAppleVisionPro, Family: FamilyVision, Platform: tool.VisionOS, Diagonal: "16:9", Sizes: []Size{{3840, 2160}}, Preview: "APPLE_VISION_PRO", aliases: []string{"vision", "visionpro", "vision-pro", "apple-vision-pro", "visionos", "avp"}},
		watch(WatchUltra, "49mm", []Size{{422, 514}, {410, 502}}, "watch-ultra", "ultra"),
		watch(WatchSeries10, "46mm", []Size{{416, 496}}, "watch-series-10", "watch-10", "watch-46mm"),
		watch(WatchSeries7, "45mm", []Size{{396, 484}}, "watch-series-7", "watch-7", "watch-45mm"),
		watch(WatchSeries4, "44mm", []Size{{368, 448}}, "watch-series-4", "watch-4", "watch-44mm"),
		watch(WatchSeries3, "42mm", []Size{{312, 390}}, "watch-series-3", "watch-3", "watch-42mm"),
	})
	for _, p := range slices.Concat(phones, pads) {
		if p.Type == AppIPhone35 {
			continue // the spec has no IMESSAGE_APP_IPHONE_35
		}
		out = append(out, imessage(p, "IMESSAGE_"+p.Type))
	}
	return out
}()

var displayByType = func() map[DisplayType]DisplaySpec {
	m := make(map[DisplayType]DisplaySpec, len(displaySpecs))
	for _, d := range displaySpecs {
		m[d.Type] = d
	}
	return m
}()

// previewTypes lists every PreviewType enum value from the pinned spec.
var previewTypes = []PreviewType{
	"IPHONE_67", "IPHONE_61", "IPHONE_65", "IPHONE_58", "IPHONE_55", "IPHONE_47", "IPHONE_40", "IPHONE_35",
	"IPAD_PRO_3GEN_129", "IPAD_PRO_3GEN_11", "IPAD_PRO_129", "IPAD_105", "IPAD_97",
	"DESKTOP", "APPLE_TV", "APPLE_VISION_PRO",
}

// Displays returns every screenshot display spec, preferred slots first.
func Displays() []DisplaySpec { return slices.Clone(displaySpecs) }

// PreviewTypes returns every app preview slot.
func PreviewTypes() []PreviewType { return slices.Clone(previewTypes) }

// Preview video limits per Apple's "App preview specifications".
const (
	PreviewMinSeconds = 15
	PreviewMaxSeconds = 30
)

// previewSizes are the accepted app preview resolutions per slot, in either
// orientation. Slots without an entry are not checked.
var previewSizes = map[PreviewType][]Size{
	"IPHONE_67": {{886, 1920}}, "IPHONE_65": {{886, 1920}}, "IPHONE_61": {{886, 1920}}, "IPHONE_58": {{886, 1920}},
	"IPHONE_55": {{1080, 1920}}, "IPHONE_47": {{750, 1334}}, "IPHONE_40": {{1080, 1920}},
	"IPAD_PRO_3GEN_129": {{1200, 1600}}, "IPAD_PRO_3GEN_11": {{1200, 1600}}, "IPAD_PRO_129": {{1200, 1600}},
	"IPAD_105": {{1200, 1600}}, "IPAD_97": {{1200, 1600}, {900, 1200}},
	"DESKTOP": {{1920, 1080}}, "APPLE_TV": {{1920, 1080}}, "APPLE_VISION_PRO": {{3840, 2160}},
}

// PreviewSizes returns the accepted resolutions of an app preview slot
// (either orientation), or nil when the slot is not checked.
func PreviewSizes(p PreviewType) []Size { return slices.Clone(previewSizes[p]) }

// LookupDisplay returns the spec for a display type.
func LookupDisplay(d DisplayType) (DisplaySpec, bool) {
	s, ok := displayByType[d]
	return s, ok
}

// Accepts reports whether w×h is an accepted pixel size for the slot.
func (s DisplaySpec) Accepts(w, h int) bool {
	for _, z := range s.Sizes {
		if (z.W == w && z.H == h) || (s.Rotates && z.W == h && z.H == w) {
			return true
		}
	}
	return false
}

// ValidPixels reports whether w×h is an accepted pixel size for d.
func ValidPixels(d DisplayType, w, h int) bool {
	s, ok := displayByType[d]
	return ok && s.Accepts(w, h)
}

// DisplayForPixels returns the non-iMessage display types that accept w×h on
// platform (empty platform means any). When exactly one current (non-legacy)
// slot accepts the size, legacy slots are dropped: 1125x2436 is a 6.3" shot,
// not a 5.8" one, unless a <display> dir or file-name hint says otherwise.
// More than one result means the size is ambiguous between current slots and
// the caller must ask, not guess; the preferred slot is first.
func DisplayForPixels(w, h int, platform tool.Platform) ([]DisplayType, bool) {
	var all, current []DisplayType
	for _, s := range displaySpecs {
		if s.IMessage || (platform != "" && s.Platform != platform) {
			continue
		}
		if s.Accepts(w, h) {
			all = append(all, s.Type)
			if !s.Legacy {
				current = append(current, s.Type)
			}
		}
	}
	if len(current) == 1 {
		return current, true
	}
	return all, len(all) > 0
}

// displayHint maps a file-name token to a slot.
type displayHint struct {
	tok string
	d   DisplayType
}

// fileNameHints maps fastlane snapshot device-name fragments (lowercased) to
// slots. Longer, more specific fragments come first.
var fileNameHints = []displayHint{
	{"ipad pro (12.9-inch) (3rd generation)", AppIPadPro3Gen129},
	{"ipad pro (12.9-inch) (4th generation)", AppIPadPro3Gen129},
	{"ipad pro (12.9-inch) (5th generation)", AppIPadPro3Gen129},
	{"ipad pro (12.9-inch) (6th generation)", AppIPadPro3Gen129},
	{"ipad pro 13-inch", AppIPadPro3Gen129},
	{"ipad pro (13-inch)", AppIPadPro3Gen129},
	{"ipad pro (12.9-inch)", AppIPadPro129},
	{"ipad pro 11-inch", AppIPadPro3Gen11},
	{"ipad pro (11-inch)", AppIPadPro3Gen11},
	{"ipad pro (10.5-inch)", AppIPad105},
	{"ipad pro (9.7-inch)", AppIPad97},
	{"iphone xs max", AppIPhone65},
	{"iphone 11 pro max", AppIPhone65},
	{"iphone 11 pro", AppIPhone58},
	{"iphone xs", AppIPhone58},
	{"iphone x", AppIPhone58},
	{"iphone 8 plus", AppIPhone55},
	{"iphone 7 plus", AppIPhone55},
	{"iphone 6s plus", AppIPhone55},
}

// enumHints lists display enum tokens (with and without the APP_ prefix),
// longest first so IMESSAGE_APP_IPHONE_67 wins over IPHONE_67.
var enumHints = func() []displayHint {
	var out []displayHint
	for _, s := range displaySpecs {
		out = append(out, displayHint{string(s.Type), s.Type})
		if t, ok := strings.CutPrefix(string(s.Type), "APP_"); ok {
			out = append(out, displayHint{t, s.Type})
		}
	}
	slices.SortStableFunc(out, func(a, b displayHint) int { return len(b.tok) - len(a.tok) })
	return out
}()

// DisplayFromFileName reads a display hint from a screenshot file name: an
// enum token such as IPHONE_58 or APP_IPAD_PRO_3GEN_129 (delimited by
// non-alphanumerics), or a fastlane snapshot device name such as
// "iPad Pro (12.9-inch) (3rd generation)-01.png". Callers must still check
// the pixels with [ValidPixels].
func DisplayFromFileName(name string) (DisplayType, bool) {
	up := strings.ToUpper(name)
	for _, h := range enumHints {
		for i := 0; ; {
			j := strings.Index(up[i:], h.tok)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+len(h.tok)
			if (start == 0 || !isAlnum(up[start-1])) && (end == len(up) || !isAlnum(up[end])) {
				return h.d, true
			}
			i = start + 1
		}
	}
	low := strings.ToLower(name)
	for _, h := range fileNameHints {
		if k := strings.Index(low, h.tok); k >= 0 {
			end := k + len(h.tok)
			if end == len(low) || !isAlnum(low[end]) {
				return h.d, true
			}
		}
	}
	return "", false
}

func isAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// RequiredDisplays returns the screenshot slots App Store review requires for
// a platform. Each inner slice is "at least one of".
func RequiredDisplays(platform tool.Platform, supportsIPad bool) [][]DisplayType {
	switch platform {
	case tool.IOS:
		req := [][]DisplayType{{AppIPhone67, AppIPhone65}}
		if supportsIPad {
			req = append(req, []DisplayType{AppIPadPro3Gen129})
		}
		return req
	case tool.MacOS:
		return [][]DisplayType{{AppDesktop}}
	case tool.TVOS:
		return [][]DisplayType{{AppAppleTV}}
	case tool.VisionOS:
		return [][]DisplayType{{AppAppleVisionPro}}
	default:
		return nil
	}
}

// displayAliases maps normalized friendly names to display types.
var displayAliases = func() map[string]DisplayType {
	m := map[string]DisplayType{}
	for _, s := range displaySpecs {
		m[normDisplay(string(s.Type))] = s.Type
		if s.IMessage {
			continue
		}
		for _, a := range s.aliases {
			switch s.Family {
			case FamilyIPhone, FamilyIPad:
				m[a] = s.Type
				m[string(s.Family)+"-"+a] = s.Type
				m["imessage-"+string(s.Family)+"-"+a] = "IMESSAGE_" + s.Type
			default:
				m[a] = s.Type
			}
		}
	}
	return m
}()

func normDisplay(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(`"`, "", "”", "", "″", "", "inches", "", "inch", "", "_", "-", " ", "-").Replace(s)
	s = strings.TrimSuffix(strings.Trim(s, "-"), "-in")
	s = strings.TrimPrefix(s, "app-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.TrimPrefix(s, "app-")
}

// ParseDisplay accepts an enum value (case-insensitive, with or without the
// APP_ prefix) or a friendly name such as "6.9", "iphone-6.7", "ipad-13",
// "mac", "tv", "vision", "watch-ultra" or "imessage-iphone-6.7".
func ParseDisplay(s string) (DisplayType, error) {
	key := normDisplay(s)
	if d, ok := displayAliases[key]; ok {
		return d, nil
	}
	if d, ok := displayAliases[normDisplay("app_"+s)]; ok {
		return d, nil
	}
	return "", fmt.Errorf("unknown display type %q: use an ASC enum such as APP_IPHONE_67 or a size such as 6.9, ipad-13, mac", s)
}
