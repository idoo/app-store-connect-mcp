package resolve

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func loc(id, locale string) res {
	return res{Type: "appStoreVersionLocalizations", ID: id, Attributes: map[string]any{"locale": locale}}
}

func TestLocalesExplicit(t *testing.T) {
	tests := []struct {
		in   tool.LocaleSet
		want []string
		kind string
	}{
		{in: tool.LocaleSet{"German", "ja-JP", "de", "pt_BR"}, want: []string{"de-DE", "ja", "pt-BR"}},
		{in: tool.LocaleSet{"en-US"}, want: []string{"en-US"}},
		{in: tool.LocaleSet{}, want: []string{}},
		{in: tool.LocaleSet{"de", "klingon"}, kind: "notfound"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.in, ","), func(t *testing.T) {
			f := tooltest.NewFakeASC(t) // explicit lists never call Apple
			got, err := New(f, Options{}).Locales(context.Background(), "a1", tt.in)
			wantErr(t, err, tt.kind)
			if tt.kind == "notfound" {
				var nf *tool.ErrNotFound
				_ = asNotFound(err, &nf)
				if nf.Ref != "klingon" || !strings.Contains(nf.Hint, "en-US") || !strings.Contains(nf.Hint, "zh-Hans") {
					t.Fatalf("error %+v must name the input and list valid codes", nf)
				}
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			if n := len(f.Requests()); n != 0 {
				t.Fatalf("explicit locales made %d requests", n)
			}
		})
	}
}

func TestLocalesAll(t *testing.T) {
	tests := []struct {
		name     string
		versions []res
		infos    []res
		want     []string
		kind     string
	}{
		{
			name:     "editable version",
			versions: []res{ver("live", "IOS", "1.0", "READY_FOR_DISTRIBUTION", ""), ver("edit", "IOS", "1.1", "PREPARE_FOR_SUBMISSION", "")},
			want:     []string{"de-DE", "en-US", "sv"},
		},
		{
			name:     "app info fallback",
			versions: []res{ver("live", "IOS", "1.0", "READY_FOR_DISTRIBUTION", "")},
			infos: []res{
				{Type: "appInfos", ID: "info-live", Attributes: map[string]any{"state": "READY_FOR_DISTRIBUTION"}},
				{Type: "appInfos", ID: "info-edit", Attributes: map[string]any{"state": "PREPARE_FOR_SUBMISSION"}},
			},
			want: []string{"en-US", "fr-FR"},
		},
		{name: "nothing", kind: "notfound"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tooltest.NewFakeASC(t)
			serveVersions(t, f, map[string][]res{"a1": tt.versions})
			f.Handle(http.MethodGet, "/v1/appStoreVersions/edit/appStoreVersionLocalizations", 200,
				body(t, loc("l1", "sv"), loc("l2", "en-US"), loc("l3", "de-DE")))
			f.Handle(http.MethodGet, "/v1/apps/a1/appInfos", 200, body(t, tt.infos...))
			f.Handle(http.MethodGet, "/v1/appInfos/info-edit/appInfoLocalizations", 200, body(t, loc("i1", "fr-FR"), loc("i2", "en-US")))

			got, err := New(f, Options{}).Locales(context.Background(), "a1", tool.LocaleSet{"ALL"})
			wantErr(t, err, tt.kind)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
