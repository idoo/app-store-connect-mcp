package applemeta

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

func loadSpecEnums(t *testing.T) (displays, previews []string) {
	t.Helper()
	b, err := os.ReadFile("testdata/spec_enums.json")
	if err != nil {
		t.Fatalf("read spec enums: %v", err)
	}
	var e struct {
		ScreenshotDisplayType []string
		PreviewType           []string
	}
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("decode spec enums: %v", err)
	}
	if len(e.ScreenshotDisplayType) == 0 || len(e.PreviewType) == 0 {
		t.Fatal("spec enum fixture is empty")
	}
	return e.ScreenshotDisplayType, e.PreviewType
}

func TestDisplayTableMatchesSpec(t *testing.T) {
	displays, previews := loadSpecEnums(t)
	var got []string
	for _, d := range Displays() {
		got = append(got, string(d.Type))
		if len(d.Sizes) == 0 {
			t.Errorf("%s has no pixel sizes", d.Type)
		}
		if d.Preview != "" && !slices.Contains(previews, string(d.Preview)) {
			t.Errorf("%s preview %s not in spec", d.Type, d.Preview)
		}
	}
	slices.Sort(got)
	slices.Sort(displays)
	if !slices.Equal(got, displays) {
		t.Errorf("display types differ from spec:\n got %v\nwant %v", got, displays)
	}
	var gotP []string
	for _, p := range PreviewTypes() {
		gotP = append(gotP, string(p))
	}
	slices.Sort(gotP)
	slices.Sort(previews)
	if !slices.Equal(gotP, previews) {
		t.Errorf("preview types differ from spec:\n got %v\nwant %v", gotP, previews)
	}
}

func TestDisplayForPixels(t *testing.T) {
	tests := []struct {
		name     string
		w, h     int
		platform tool.Platform
		want     []DisplayType
	}{
		{"6.9 portrait", 1320, 2868, tool.IOS, []DisplayType{AppIPhone67}},
		{"6.7 landscape", 2796, 1290, tool.IOS, []DisplayType{AppIPhone67}},
		{"6.5", 1242, 2688, tool.IOS, []DisplayType{AppIPhone65}},
		{"5.8 vs 6.1 ambiguous", 1125, 2436, tool.IOS, []DisplayType{AppIPhone61, AppIPhone58}},
		{"13 inch iPad new", 2064, 2752, tool.IOS, []DisplayType{AppIPadPro3Gen129}},
		{"12.9 ambiguous legacy", 2048, 2732, tool.IOS, []DisplayType{AppIPadPro3Gen129, AppIPadPro129}},
		{"mac", 2880, 1800, tool.MacOS, []DisplayType{AppDesktop}},
		{"mac portrait rejected", 1800, 2880, tool.MacOS, nil},
		{"4K tv", 3840, 2160, tool.TVOS, []DisplayType{AppAppleTV}},
		{"4K any platform", 3840, 2160, "", []DisplayType{AppAppleTV, AppAppleVisionPro}},
		{"watch ultra", 410, 502, tool.IOS, []DisplayType{WatchUltra}},
		{"wrong platform", 1320, 2868, tool.MacOS, nil},
		{"unknown size", 1000, 1000, "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DisplayForPixels(tc.w, tc.h, tc.platform)
			if !slices.Equal(got, tc.want) || ok != (len(tc.want) > 0) {
				t.Errorf("DisplayForPixels(%d,%d,%q) = %v,%v; want %v", tc.w, tc.h, tc.platform, got, ok, tc.want)
			}
		})
	}
}

func TestValidPixels(t *testing.T) {
	tests := []struct {
		d    DisplayType
		w, h int
		want bool
	}{
		{AppIPhone67, 1260, 2736, true},
		{AppIPhone67, 1242, 2688, false},
		{IMessageIPhone67, 1290, 2796, true},
		{AppAppleVisionPro, 3840, 2160, true},
		{AppAppleVisionPro, 2160, 3840, false},
		{WatchSeries10, 416, 496, true},
		{"APP_IPHONE_69", 1320, 2868, false},
	}
	for _, tc := range tests {
		if got := ValidPixels(tc.d, tc.w, tc.h); got != tc.want {
			t.Errorf("ValidPixels(%s,%d,%d) = %v; want %v", tc.d, tc.w, tc.h, got, tc.want)
		}
	}
}

func TestRequiredDisplays(t *testing.T) {
	tests := []struct {
		platform tool.Platform
		ipad     bool
		want     [][]DisplayType
	}{
		{tool.IOS, false, [][]DisplayType{{AppIPhone67, AppIPhone65}}},
		{tool.IOS, true, [][]DisplayType{{AppIPhone67, AppIPhone65}, {AppIPadPro3Gen129}}},
		{tool.MacOS, false, [][]DisplayType{{AppDesktop}}},
		{tool.TVOS, false, [][]DisplayType{{AppAppleTV}}},
		{tool.VisionOS, false, [][]DisplayType{{AppAppleVisionPro}}},
		{"WATCH_OS", false, nil},
	}
	for _, tc := range tests {
		got := RequiredDisplays(tc.platform, tc.ipad)
		if !slices.EqualFunc(got, tc.want, slices.Equal[[]DisplayType]) {
			t.Errorf("RequiredDisplays(%s,%v) = %v; want %v", tc.platform, tc.ipad, got, tc.want)
		}
	}
}

func TestParseDisplay(t *testing.T) {
	tests := []struct {
		in   string
		want DisplayType
	}{
		{"APP_IPHONE_67", AppIPhone67},
		{"app_iphone_65", AppIPhone65},
		{"IPHONE_61", AppIPhone61},
		{"6.9", AppIPhone67},
		{`6.7"`, AppIPhone67},
		{"iphone-6.7", AppIPhone67},
		{"iPhone 6.9 inch", AppIPhone67},
		{"ipad-13", AppIPadPro3Gen129},
		{"iPad 12.9", AppIPadPro3Gen129},
		{"11", AppIPadPro3Gen11},
		{"mac", AppDesktop},
		{"Apple TV", AppAppleTV},
		{"vision-pro", AppAppleVisionPro},
		{"watch-ultra", WatchUltra},
		{"imessage-iphone-6.7", IMessageIPhone67},
		{"IMESSAGE_APP_IPAD_97", IMessageIPad97},
		{"APP_IPHONE_69", AppIPhone67}, // the common agent mistake maps to the 6.9" slot
	}
	for _, tc := range tests {
		got, err := ParseDisplay(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseDisplay(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "APP_IPHONE_70", "7.2", "watch"} {
		if got, err := ParseDisplay(bad); err == nil {
			t.Errorf("ParseDisplay(%q) = %q; want error", bad, got)
		}
	}
}
