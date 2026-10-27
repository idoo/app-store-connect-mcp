package websession

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestImportFormats(t *testing.T) {
	future := time.Now().Add(48 * time.Hour)
	cases := []struct {
		name, in, format string
		want             map[string]string // name -> value
		domain           map[string]string
	}{
		{
			name:   "raw header",
			in:     "Cookie: myacinfo=MYAC1; dqsid=DQ1; itctx=IT1\n",
			format: FormatHeader,
			want:   map[string]string{"myacinfo": "MYAC1", "dqsid": "DQ1", "itctx": "IT1"},
			domain: map[string]string{"myacinfo": "appstoreconnect.apple.com"},
		},
		{
			name:   "bare line",
			in:     "myacinfo=MYAC2;dqsid=DQ2",
			format: FormatHeader,
			want:   map[string]string{"myacinfo": "MYAC2", "dqsid": "DQ2"},
		},
		{
			name: "curl bash with other headers",
			in: `curl 'https://appstoreconnect.apple.com/iris/v1/apps?limit=1' \
  -H 'accept: application/json' \
  -H 'cookie: myacinfo=MYAC3; dqsid=DQ3; itctx="quoted=val"' \
  -H 'user-agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)' \
  -H "x-csrf-itc: [asc-ui]" \
  --compressed`,
			format: FormatCurl,
			want:   map[string]string{"myacinfo": "MYAC3", "dqsid": "DQ3", "itctx": `"quoted=val"`},
		},
		{
			name:   "curl -b and --cookie, zsh $'' quoting",
			in:     `curl $'https://appstoreconnect.apple.com/olympus/v1/session' -b 'myacinfo=MYAC4' --cookie "dqsid=DQ4; q=\"v\"" -H $'Accept: */*'`,
			format: FormatCurl,
			want:   map[string]string{"myacinfo": "MYAC4", "dqsid": "DQ4", "q": `"v"`},
		},
		{
			name: "fastlane session",
			in: `---
- !ruby/object:HTTP::Cookie
  name: myacinfo
  value: MYAC5
  domain: apple.com
  for_domain: true
  path: "/"
  secure: true
  httponly: true
  expires:
  max_age:
  created_at: 2026-10-20 10:00:00.000000000 +02:00
  accessed_at: 2026-10-20 10:00:00.000000000 +02:00
- !ruby/object:HTTP::Cookie
  name: dqsid
  value: 'DQ5'
  domain: appstoreconnect.apple.com
  for_domain: false
  path: "/"
  secure: true
  httponly: true
  expires: ` + future.Format(time.RFC3339) + `
- !ruby/object:HTTP::Cookie
  name: other
  value: X
  domain: example.com
  path: "/"
`,
			format: FormatFastlane,
			want:   map[string]string{"myacinfo": "MYAC5", "dqsid": "DQ5"},
			domain: map[string]string{"myacinfo": "apple.com"},
		},
		{
			name: "cookie editor json",
			in: `[{"domain":".apple.com","expirationDate":` + jsonNum(future) + `,"hostOnly":false,"httpOnly":true,"name":"myacinfo","path":"/","secure":true,"value":"MYAC6"},
{"domain":"appstoreconnect.apple.com","httpOnly":true,"name":"dqsid","path":"/","secure":true,"session":true,"value":"DQ6"},
{"domain":".google.com","name":"NID","value":"G"},
{"domain":".apple.com","expirationDate":1000,"name":"old","value":"O"}]`,
			format: FormatJSON,
			want:   map[string]string{"myacinfo": "MYAC6", "dqsid": "DQ6"},
			domain: map[string]string{"myacinfo": ".apple.com"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, format, err := Import([]byte(tc.in))
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if format != tc.format || s.Source != "import:"+tc.format {
				t.Fatalf("format %q source %q, want %q", format, s.Source, tc.format)
			}
			got := map[string]string{}
			for _, c := range s.Cookies {
				got[c.Name] = c.Value
				if !isAppleDomain(c.Domain) {
					t.Errorf("non-apple cookie kept: %s %s", c.Name, c.Domain)
				}
				if d, ok := tc.domain[c.Name]; ok && c.Domain != d {
					t.Errorf("%s domain %q, want %q", c.Name, c.Domain, d)
				}
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("cookie %s = %q, want %q", k, got[k], v)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("cookies %v, want %v", keys(got), keys(tc.want))
			}
			if h := s.CookieHeader("appstoreconnect.apple.com", "/iris/v1/apps", time.Now()); !strings.Contains(h, "myacinfo=") {
				t.Errorf("myacinfo not sent to ASC: %q", h)
			}
		})
	}
}

// jsonNum renders t as Cookie-Editor's fractional expirationDate.
func jsonNum(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) + ".25" }

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestImportRejects(t *testing.T) {
	cases := map[string]string{
		"no myacinfo header": "dqsid=SECRETDQ; itctx=SECRETIT",
		"no myacinfo curl":   `curl 'https://appstoreconnect.apple.com/' -H 'cookie: dqsid=SECRETDQ'`,
		"myacinfo elsewhere": `[{"domain":".example.com","name":"myacinfo","value":"SECRETMY"}]`,
		"curl cookie file":   `curl https://appstoreconnect.apple.com -b cookies.txt`,
		"curl no cookie":     `curl https://appstoreconnect.apple.com -H 'accept: SECRETACC'`,
		"garbage":            "hello SECRET world",
		"bad json":           `[{"name": "SECRETJ"`,
		"empty":              "   ",
		"unterminated":       `curl -H 'cookie: myacinfo=SECRETU`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Import([]byte(in))
			if err == nil {
				t.Fatal("want error")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaks input: %v", err)
			}
		})
	}
}
