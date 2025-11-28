package asc

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func mustURL(t testing.TB, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func TestDecodeCursor(t *testing.T) {
	prod := "https://api.appstoreconnect.apple.com"
	tests := []struct {
		name    string
		base    string
		cursor  string // raw URL; encoded unless raw is set
		raw     bool
		path    string
		want    string
		wantErr bool
	}{
		{
			name: "valid", base: prod, path: "/v1/apps",
			cursor: "https://api.appstoreconnect.apple.com/v1/apps?limit=2&cursor=Mg&filter[bundleId]=a.b&fields[apps]=name",
			want:   "https://api.appstoreconnect.apple.com/v1/apps?cursor=Mg&fields%5Bapps%5D=name&filter%5BbundleId%5D=a.b&limit=2",
		},
		{
			name: "host case normalized", base: prod, path: "/v1/apps",
			cursor: "https://API.appstoreconnect.apple.com/v1/apps?cursor=Mg",
			want:   "https://api.appstoreconnect.apple.com/v1/apps?cursor=Mg",
		},
		{
			name: "all allowed keys", base: prod, path: "/v1/apps",
			cursor: "https://api.appstoreconnect.apple.com/v1/apps?include=builds&sort=name&limit[builds]=5&exists[x]=true",
			want:   "https://api.appstoreconnect.apple.com/v1/apps?exists%5Bx%5D=true&include=builds&limit%5Bbuilds%5D=5&sort=name",
		},
		{
			name: "loopback http allowed", base: "http://127.0.0.1:4567", path: "/v1/apps",
			cursor: "http://127.0.0.1:4567/v1/apps?cursor=x",
			want:   "http://127.0.0.1:4567/v1/apps?cursor=x",
		},
		{name: "host swap", base: prod, path: "/v1/apps", cursor: "https://evil.example/v1/apps?cursor=x", wantErr: true},
		{name: "port differs", base: prod, path: "/v1/apps", cursor: "https://api.appstoreconnect.apple.com:8443/v1/apps", wantErr: true},
		{name: "http on prod", base: prod, path: "/v1/apps", cursor: "http://api.appstoreconnect.apple.com/v1/apps", wantErr: true},
		{name: "userinfo", base: prod, path: "/v1/apps", cursor: "https://u:p@api.appstoreconnect.apple.com/v1/apps", wantErr: true},
		{name: "path differs", base: prod, path: "/v1/apps", cursor: "https://api.appstoreconnect.apple.com/v1/users", wantErr: true},
		{name: "dot segments", base: prod, path: "/v1/apps", cursor: "https://api.appstoreconnect.apple.com/v1/apps/../users", wantErr: true},
		{name: "encoded slash", base: prod, path: "/v1/apps", cursor: "https://api.appstoreconnect.apple.com/v1%2Fapps", wantErr: true},
		{name: "unknown key", base: prod, path: "/v1/apps", cursor: "https://api.appstoreconnect.apple.com/v1/apps?redirect=x", wantErr: true},
		{name: "empty bracket key", base: prod, path: "/v1/apps", cursor: "https://api.appstoreconnect.apple.com/v1/apps?filter[]=x", wantErr: true},
		{name: "fragment", base: prod, path: "/v1/apps", cursor: "https://api.appstoreconnect.apple.com/v1/apps#x", wantErr: true},
		{name: "relative", base: prod, path: "/v1/apps", cursor: "/v1/apps?cursor=x", wantErr: true},
		{name: "protocol relative", base: prod, path: "/v1/apps", cursor: "//evil.example/v1/apps", wantErr: true},
		{name: "not base64", base: prod, path: "/v1/apps", cursor: "%%%", raw: true, wantErr: true},
		{name: "empty", base: prod, path: "/v1/apps", cursor: "", raw: true, wantErr: true},
		{name: "too long", base: prod, path: "/v1/apps", cursor: strings.Repeat("a", maxCursorLen+1), raw: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.cursor
			if !tt.raw {
				c = EncodeCursor(c)
			}
			got, err := DecodeCursor(c, mustURL(t, tt.base), tt.path)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidCursor) {
					t.Fatalf("err = %v, want ErrInvalidCursor (got %q)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeCursorNilBase(t *testing.T) {
	_, err := DecodeCursor(EncodeCursor("https://a/v1/apps"), nil, "/v1/apps")
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("err = %v, want ErrInvalidCursor", err)
	}
}

func FuzzDecodeCursor(f *testing.F) {
	for _, s := range []string{
		"https://api.appstoreconnect.apple.com/v1/apps?cursor=Mg&limit=2",
		"https://evil.example/v1/apps",
		"https://u@api.appstoreconnect.apple.com/v1/apps",
		"https://api.appstoreconnect.apple.com/v1/apps?filter[x]=1&fields[apps]=a,b",
		"//evil/v1/apps",
		"https://api.appstoreconnect.apple.com\\@evil/v1/apps",
		"https://api.appstoreconnect.apple.com%2eevil/v1/apps",
	} {
		f.Add(EncodeCursor(s))
	}
	f.Add("not a cursor")
	base := mustURL(f, "https://api.appstoreconnect.apple.com")
	f.Fuzz(func(t *testing.T, cursor string) {
		got, err := DecodeCursor(cursor, base, "/v1/apps")
		if err != nil {
			return
		}
		u, perr := url.Parse(got)
		if perr != nil {
			t.Fatalf("returned unparsable URL %q: %v", got, perr)
		}
		if u.Host != base.Host || u.Scheme != "https" || u.User != nil || u.Path != "/v1/apps" {
			t.Fatalf("policy violated by %q", got)
		}
	})
}
