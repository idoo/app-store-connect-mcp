package browser

import "testing"

func TestHTTPSHandlerFromPlistJSON(t *testing.T) {
	cases := map[string]string{
		`{"LSHandlers":[{"LSHandlerURLScheme":"http","LSHandlerRoleAll":"com.apple.safari"},{"LSHandlerPreferredVersions":{"LSHandlerRoleAll":"-"},"LSHandlerRoleAll":"com.brave.browser","LSHandlerURLScheme":"https"}]}`: "com.brave.browser",
		`{"LSHandlers":[{"LSHandlerURLScheme":"HTTPS","LSHandlerRoleAll":"com.google.Chrome"}]}`:                                                                                                                           "com.google.Chrome",
		`{"LSHandlers":[{"LSHandlerURLScheme":"http","LSHandlerRoleAll":"com.google.chrome"}]}`:                                                                                                                            "",
		`{"LSHandlers":[{"LSHandlerContentType":"public.html","LSHandlerRoleAll":"x"}]}`:                                                                                                                                   "",
		`{}`:       "",
		`not json`: "",
	}
	for in, want := range cases {
		if got := httpsHandlerFromPlistJSON([]byte(in)); got != want {
			t.Errorf("httpsHandlerFromPlistJSON(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestMacBrowser(t *testing.T) {
	for id, want := range map[string]knownBrowser{
		"com.brave.browser":          {"Brave Browser", true},
		"com.brave.Browser":          {"Brave Browser", true},
		"com.google.Chrome.canary":   {"Google Chrome Canary", true},
		"com.microsoft.edgemac":      {"Microsoft Edge", true},
		"company.thebrowser.Browser": {"Arc", true},
		"com.apple.Safari":           {"Safari", false},
		"org.mozilla.firefox":        {"Firefox", false},
	} {
		got, ok := macBrowser(id)
		if !ok || got != want {
			t.Errorf("macBrowser(%q) = %+v,%v; want %+v", id, got, ok, want)
		}
	}
	if _, ok := macBrowser("com.example.unknown"); ok {
		t.Error("unknown bundle id recognised")
	}
}

func TestProgIDBrowser(t *testing.T) {
	for id, want := range map[string]knownBrowser{
		"ChromeHTML":                  {"Google Chrome", true},
		"BraveHTML":                   {"Brave Browser", true},
		"BraveBHTML":                  {"Brave Browser Beta", true},
		"MSEdgeHTM":                   {"Microsoft Edge", true},
		"ChromiumHTM.ABCDEF":          {"Chromium", true},
		"VivaldiHTM.XYZ":              {"Vivaldi", true},
		"OperaStable":                 {"Opera", true},
		"FirefoxURL-308046B0AF4A39CB": {"Firefox", false},
		"IE.HTTP":                     {"Internet Explorer", false},
	} {
		got, ok := progIDBrowser(id)
		if !ok || got != want {
			t.Errorf("progIDBrowser(%q) = %+v,%v; want %+v", id, got, ok, want)
		}
	}
	if _, ok := progIDBrowser("SomethingElse"); ok {
		t.Error("unknown ProgId recognised")
	}
}

func TestCommandExe(t *testing.T) {
	for in, want := range map[string]string{
		`"C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe" --single-argument %1`: `C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe --single-argument %1`:                `C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`  "%LOCALAPPDATA%\Chromium\Application\chrome.exe" -- "%1"`:                                `%LOCALAPPDATA%\Chromium\Application\chrome.exe`,
		`/usr/bin/foo --x`: `/usr/bin/foo`,
	} {
		if got := commandExe(in); got != want {
			t.Errorf("commandExe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDesktopBrowser(t *testing.T) {
	b, bins, ok := desktopBrowser("brave-browser.desktop\n")
	if !ok || b.name != "Brave Browser" || !b.chromium || bins[0] != "brave-browser" {
		t.Errorf("brave: %+v %v %v", b, bins, ok)
	}
	if b, _, ok := desktopBrowser("google-chrome.desktop"); !ok || !b.chromium {
		t.Error("chrome not recognised")
	}
	if b, _, ok := desktopBrowser("firefox.desktop"); !ok || b.chromium || b.name != "Firefox" {
		t.Error("firefox should be known, not chromium")
	}
	for _, d := range []string{"microsoft-edge.desktop", "chromium.desktop", "chromium-browser.desktop", "vivaldi-stable.desktop", "opera.desktop"} {
		if b, bins, ok := desktopBrowser(d); !ok || !b.chromium || len(bins) == 0 {
			t.Errorf("%s not mapped", d)
		}
	}
	if _, _, ok := desktopBrowser("epiphany.desktop"); ok {
		t.Error("unknown desktop recognised")
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser":       "Brave Browser",
		`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`: "Brave Browser",
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`:       "Microsoft Edge",
		"/usr/bin/google-chrome-stable":                                      "Google Chrome",
		"/opt/weird/browser":                                                 "browser",
	} {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}
