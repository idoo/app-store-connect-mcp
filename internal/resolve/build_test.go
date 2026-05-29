package resolve

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

func build(id, number, state string) res {
	return res{Type: "builds", ID: id, Attributes: map[string]any{
		"version": number, "processingState": state, "expired": false, "uploadedDate": "2026-02-01T00:00:00-08:00",
	}}
}

func TestBuildRefForms(t *testing.T) {
	base := func(extra ...string) url.Values {
		q := url.Values{
			"filter[app]": {"a1"}, "sort": {"-uploadedDate"}, "fields[builds]": {buildFields}, "limit": {"1"},
			"filter[preReleaseVersion.platform]": {"IOS"},
		}
		for i := 0; i < len(extra); i += 2 {
			q.Set(extra[i], extra[i+1])
		}
		return q
	}
	tests := []struct {
		name      string
		platform  tool.Platform
		ref       tool.BuildRef
		reply     []res
		wantQuery url.Values // nil: no /v1/builds collection request
		want      tool.Build
		kind      string
		next      string
		cands     int
	}{
		{
			name: "default", reply: []res{build("b1", "42", "PROCESSING")}, wantQuery: base("filter[expired]", "false"),
			want: tool.Build{ID: "b1", Number: "42", ProcessingState: "PROCESSING"},
		},
		{
			name: "latest", ref: "LATEST", reply: []res{build("b1", "42", "VALID")}, wantQuery: base("filter[expired]", "false"),
			want: tool.Build{ID: "b1", Number: "42", ProcessingState: "VALID"},
		},
		{
			name: "latest-valid", ref: "latest-valid", reply: []res{build("b2", "41", "VALID")},
			wantQuery: base("filter[expired]", "false", "filter[processingState]", "VALID"),
			want:      tool.Build{ID: "b2", Number: "41", ProcessingState: "VALID"},
		},
		{
			name: "latest marketing", ref: "latest:2.1", reply: []res{build("b3", "7", "VALID")},
			wantQuery: base("filter[expired]", "false", "filter[preReleaseVersion.version]", "2.1"),
			want:      tool.Build{ID: "b3", Number: "7", ProcessingState: "VALID"},
		},
		{
			name: "explicit platform", platform: tool.MacOS, ref: "latest", reply: []res{build("b4", "9", "VALID")},
			wantQuery: base("filter[expired]", "false", "filter[preReleaseVersion.platform]", "MAC_OS"),
			want:      tool.Build{ID: "b4", Number: "9", ProcessingState: "VALID"},
		},
		{
			name: "number", ref: "42", reply: []res{build("b1", "42", "VALID")},
			wantQuery: base("filter[version]", "42", "limit", "5"),
			want:      tool.Build{ID: "b1", Number: "42", ProcessingState: "VALID"},
		},
		{
			name: "number ambiguous", ref: "1.0.3", reply: []res{build("b1", "1.0.3", "VALID"), build("b9", "1.0.3", "VALID")},
			wantQuery: base("filter[version]", "1.0.3", "limit", "5"), kind: "ambiguous", next: "builds_list", cands: 2,
		},
		{name: "none", ref: "latest", wantQuery: base("filter[expired]", "false"), kind: "notfound", next: "builds_list"},
		{name: "empty marketing", ref: "latest:", kind: "notfound", next: "builds_list"},
		{name: "id", ref: "8d6f-uuid", want: tool.Build{ID: "8d6f-uuid", Number: "5", ProcessingState: "VALID"}},
		{name: "id missing", ref: "gone-uuid", kind: "notfound", next: "builds_list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tooltest.NewFakeASC(t)
			serveVersions(t, f, map[string][]res{"a1": {ver("v", "IOS", "1.0", "READY_FOR_DISTRIBUTION", "")}})
			f.Handle(http.MethodGet, "/v1/builds", 200, body(t, tt.reply...))
			f.Handle(http.MethodGet, "/v1/builds/8d6f-uuid", 200, single(t, build("8d6f-uuid", "5", "VALID")))
			f.Handle(http.MethodGet, "/v1/builds/gone-uuid", 404, `{"errors":[{"status":"404"}]}`)

			got, err := New(f, Options{}).Build(context.Background(), "a1", tt.platform, tt.ref)
			next, n := wantErr(t, err, tt.kind)
			if next != tt.next || n != tt.cands {
				t.Fatalf("next=%q candidates=%d, want %q %d", next, n, tt.next, tt.cands)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			reqs := f.RequestsTo(http.MethodGet, "/v1/builds")
			switch {
			case tt.wantQuery == nil && len(reqs) != 0:
				t.Fatalf("unexpected builds list request %v", reqs[0].Query)
			case tt.wantQuery != nil && (len(reqs) != 1 || reqs[0].Query.Encode() != tt.wantQuery.Encode()):
				t.Fatalf("builds query = %v, want %v", reqs, tt.wantQuery.Encode())
			}
			platformReqs := len(f.RequestsTo(http.MethodGet, "/v1/apps/{id}/appStoreVersions"))
			if wantInfer := tt.platform == "" && tt.wantQuery != nil; (platformReqs == 1) != wantInfer {
				t.Fatalf("platform inference requests = %d, want inference %v", platformReqs, wantInfer)
			}
		})
	}
}
