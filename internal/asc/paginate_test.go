package asc

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const testBase = "https://api.appstoreconnect.apple.com"

// fakeCollection serves items 0..n-1 with offset cursors and records the
// requested URLs.
type fakeCollection struct {
	n           int
	ignoreLimit bool
	nextHost    string // overrides the host of next links
	loop        bool   // next link always points at offset 1
	urls        []string
}

func (f *fakeCollection) fetch(_ context.Context, raw string) (*tool.Document, error) {
	f.urls = append(f.urls, raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	off, _ := strconv.Atoi(q.Get("cursor"))
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil {
		limit = 2
	}
	pageSize := limit
	if f.ignoreLimit {
		pageSize = 2
	}
	doc := &tool.Document{
		Collection: true,
		Meta:       map[string]any{"paging": map[string]any{"total": float64(f.n), "limit": float64(limit)}},
		Links:      map[string]string{},
	}
	end := min(off+pageSize, f.n)
	for i := off; i < end; i++ {
		doc.Data = append(doc.Data, tool.Resource{ID: strconv.Itoa(i), Type: "apps"})
		doc.Included = append(doc.Included,
			tool.Resource{ID: "shared", Type: "builds"},
			tool.Resource{ID: "b" + strconv.Itoa(i), Type: "builds"},
		)
	}
	if end < f.n {
		next := *u
		if f.nextHost != "" {
			next.Host = f.nextHost
		}
		nq := url.Values{"cursor": {strconv.Itoa(end)}, "limit": {strconv.Itoa(limit)}}
		if f.loop {
			nq.Set("cursor", "1")
		}
		next.RawQuery = nq.Encode()
		doc.Links["next"] = next.String()
	}
	return doc, nil
}

func ids(rs []tool.Resource) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Type+"/"+r.ID)
	}
	return out
}

func TestPaginate(t *testing.T) {
	base := mustURL(t, testBase)
	first := testBase + "/v1/apps?limit=2"
	tests := []struct {
		name       string
		coll       fakeCollection
		maxItems   int
		wantIDs    []string
		wantIncl   int
		wantFetch  int
		wantCursor string // decoded next cursor, "" for none
	}{
		{
			name: "three pages to the end", coll: fakeCollection{n: 5}, maxItems: 200,
			wantIDs: []string{"apps/0", "apps/1", "apps/2", "apps/3", "apps/4"}, wantIncl: 6, wantFetch: 3,
		},
		{
			name: "max items on lowered page boundary", coll: fakeCollection{n: 5}, maxItems: 3,
			wantIDs: []string{"apps/0", "apps/1", "apps/2"}, wantIncl: 4, wantFetch: 2,
			wantCursor: testBase + "/v1/apps?cursor=3&limit=2",
		},
		{
			name: "max items on natural boundary", coll: fakeCollection{n: 5}, maxItems: 4,
			wantIDs: []string{"apps/0", "apps/1", "apps/2", "apps/3"}, wantIncl: 5, wantFetch: 2,
			wantCursor: testBase + "/v1/apps?cursor=4&limit=2",
		},
		{
			name: "server ignores limit, page cut mid-way", coll: fakeCollection{n: 5, ignoreLimit: true}, maxItems: 3,
			wantIDs: []string{"apps/0", "apps/1", "apps/2"}, wantIncl: 5, wantFetch: 2,
			wantCursor: testBase + "/v1/apps?cursor=2&limit=2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coll := tt.coll
			got, err := Paginate(context.Background(), first, base, tt.maxItems, coll.fetch)
			if err != nil {
				t.Fatalf("Paginate: %v", err)
			}
			if diff := cmp.Diff(tt.wantIDs, ids(got.Data)); diff != "" {
				t.Errorf("data (-want +got):\n%s", diff)
			}
			if got.Returned != len(tt.wantIDs) || got.Total != coll.n {
				t.Errorf("returned=%d total=%d", got.Returned, got.Total)
			}
			if len(got.Included) != tt.wantIncl {
				t.Errorf("included = %v, want %d unique", ids(got.Included), tt.wantIncl)
			}
			if len(coll.urls) != tt.wantFetch {
				t.Errorf("fetched %v, want %d pages", coll.urls, tt.wantFetch)
			}
			if tt.wantCursor == "" {
				if got.NextCursor != "" {
					t.Errorf("unexpected next cursor")
				}
				return
			}
			dec, err := DecodeCursor(got.NextCursor, base, "/v1/apps")
			if err != nil || dec != tt.wantCursor {
				t.Errorf("next cursor = %q (%v), want %q", dec, err, tt.wantCursor)
			}
		})
	}
}

func TestPaginateResumeCoversEverything(t *testing.T) {
	base := mustURL(t, testBase)
	coll := fakeCollection{n: 7}
	var all []string
	next := testBase + "/v1/apps?limit=2"
	for range 10 {
		got, err := Paginate(context.Background(), next, base, 3, coll.fetch)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, ids(got.Data)...)
		if got.NextCursor == "" {
			break
		}
		if next, err = DecodeCursor(got.NextCursor, base, "/v1/apps"); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"apps/0", "apps/1", "apps/2", "apps/3", "apps/4", "apps/5", "apps/6"}
	if diff := cmp.Diff(want, all); diff != "" {
		t.Errorf("resumed listing (-want +got):\n%s", diff)
	}
}

func TestPaginateErrors(t *testing.T) {
	base := mustURL(t, testBase)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	boom := errors.New("boom")
	tests := []struct {
		name    string
		ctx     context.Context
		first   string
		fetch   PageFetcher
		wantIs  error
		wantMax int // max fetches allowed
	}{
		{
			name: "loop", ctx: context.Background(), first: testBase + "/v1/apps?limit=1",
			fetch: (&fakeCollection{n: 9, loop: true}).fetch, wantIs: ErrPaginationLoop, wantMax: 3,
		},
		{
			name: "host swap in next link", ctx: context.Background(), first: testBase + "/v1/apps?limit=2",
			fetch: (&fakeCollection{n: 5, nextHost: "evil.example"}).fetch, wantMax: 1,
		},
		{
			name: "first URL off host", ctx: context.Background(), first: "https://evil.example/v1/apps",
			fetch: (&fakeCollection{n: 5}).fetch,
		},
		{
			name: "fetch error", ctx: context.Background(), first: testBase + "/v1/apps",
			fetch: func(context.Context, string) (*tool.Document, error) { return nil, boom }, wantIs: boom, wantMax: 1,
		},
		{
			name: "canceled", ctx: canceled, first: testBase + "/v1/apps",
			fetch: (&fakeCollection{n: 5}).fetch, wantIs: context.Canceled,
		},
		{name: "nil fetcher", ctx: context.Background(), first: testBase + "/v1/apps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			fetch := tt.fetch
			if fetch != nil {
				inner := fetch
				fetch = func(ctx context.Context, u string) (*tool.Document, error) {
					calls++
					if mustURL(t, u).Host != base.Host {
						t.Fatalf("fetched off-host URL %q", u)
					}
					return inner(ctx, u)
				}
			}
			got, err := Paginate(tt.ctx, tt.first, base, 0, fetch)
			if err == nil {
				t.Fatalf("want error, got %+v", got)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("err = %v, want %v", err, tt.wantIs)
			}
			if calls > tt.wantMax {
				t.Errorf("fetched %d pages, want ≤ %d", calls, tt.wantMax)
			}
		})
	}
}

func TestPaginateRelativeFirstURL(t *testing.T) {
	coll := fakeCollection{n: 1}
	got, err := Paginate(context.Background(), "/v1/apps?limit=2", mustURL(t, testBase), 10, coll.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if got.Returned != 1 || coll.urls[0] != testBase+"/v1/apps?limit=2" {
		t.Fatalf("got %+v via %v", got, coll.urls)
	}
}
