package browser

import (
	"strings"
	"testing"
	"time"
)

func TestPageLabel(t *testing.T) {
	cases := map[string]string{
		"https://idmsa.apple.com/appleauth/auth/signin?widgetKey=SECRET#x": "https://idmsa.apple.com/appleauth/auth/signin",
		"https://appstoreconnect.apple.com/login":                          "https://appstoreconnect.apple.com/login",
		"about:blank": "a sign-in page",
		"::bad":       "a sign-in page",
	}
	for in, want := range cases {
		if got := pageLabel(in); got != want {
			t.Errorf("pageLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCdpExpires(t *testing.T) {
	if !cdpExpires(-1).IsZero() || !cdpExpires(0).IsZero() {
		t.Error("session cookie should have zero expiry")
	}
	if got := cdpExpires(1700000000.5); !got.Equal(time.Unix(1700000000, 5e8)) {
		t.Errorf("cdpExpires = %v", got)
	}
}

func TestHeadfulUA(t *testing.T) {
	in := "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/141.0.0.0 Safari/537.36"
	if got := headfulUA(in); got != "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36" {
		t.Errorf("headfulUA = %q", got)
	}
}

func TestCandidates(t *testing.T) {
	mac := candidates("darwin", "/Users/u", nil)
	if mac[0] != "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" || mac[1] != "/Users/u/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" {
		t.Errorf("darwin candidates: %v", mac[:2])
	}
	if len(mac) != 6 {
		t.Errorf("darwin: %d candidates", len(mac))
	}
	for _, c := range mac {
		if strings.Contains(c, "Chromium") {
			t.Errorf("darwin auto-detects unsigned Chromium: %s", c)
		}
	}
	win := candidates("windows", "", func(k string) string {
		if k == "ProgramFiles" {
			return `C:\Program Files\`
		}
		return ""
	})
	if win[0] != `C:\Program Files\Google\Chrome\Application\chrome.exe` || len(win) != 4 {
		t.Errorf("windows candidates: %v", win)
	}
	if lin := candidates("linux", "", nil); lin[0] != "google-chrome" {
		t.Errorf("linux candidates: %v", lin)
	}
}
