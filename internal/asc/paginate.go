package asc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Pagination bounds for [Paginate].
const (
	DefaultMaxItems = 200
	MaxMaxItems     = 5000
)

// ErrPaginationLoop reports a collection whose links.next repeats a URL that
// was already fetched.
var ErrPaginationLoop = errors.New("asc: pagination loop")

// PageFetcher GETs one collection page and returns the parsed document.
type PageFetcher func(ctx context.Context, rawURL string) (*tool.Document, error)

// Paginate fetches firstURL and follows links.next until the collection ends
// or maxItems primary items are collected (≤ 0 means [DefaultMaxItems];
// values above [MaxMaxItems] are clamped). Every next link must stay on
// base's host and firstURL's path under the cursor policy of [DecodeCursor];
// a repeated next link fails with [ErrPaginationLoop]. Included resources are
// merged and deduplicated by type+id; Total comes from meta.paging.total.
//
// To stop on a page boundary, the limit query parameter of each follow-up
// page is lowered to the number of items still wanted; NextCursor then
// resumes exactly after the last returned item, with the original limit. If a
// server ignores the lowered limit and the page is cut mid-way, NextCursor
// resumes at the start of that cut page, so a resumed listing repeats the
// items already returned from it rather than skipping any.
func Paginate(ctx context.Context, firstURL string, base *url.URL, maxItems int, fetch PageFetcher) (*tool.ListResult, error) {
	if fetch == nil {
		return nil, errors.New("asc: paginate: nil fetcher")
	}
	switch {
	case maxItems <= 0:
		maxItems = DefaultMaxItems
	case maxItems > MaxMaxItems:
		maxItems = MaxMaxItems
	}
	first, err := url.Parse(firstURL)
	if err != nil {
		return nil, fmt.Errorf("asc: paginate: parse first URL: %w", err)
	}
	if base != nil && !first.IsAbs() {
		first = base.ResolveReference(first)
	}
	wantPath := first.Path
	cur, err := checkPageURL(first, base, wantPath)
	if err != nil {
		return nil, fmt.Errorf("asc: paginate: first URL: %w", err)
	}
	origLimit := first.Query().Get("limit")

	out := &tool.ListResult{Data: []tool.Resource{}}
	seenURL := map[string]bool{cur: true}
	seenIncl := map[string]bool{}
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("asc: paginate: %w", err)
		}
		remaining := maxItems - len(out.Data)
		doc, err := fetch(ctx, pageWithLimit(cur, remaining))
		if err != nil {
			return nil, fmt.Errorf("asc: paginate: page %d: %w", page, err)
		}
		if doc == nil {
			doc = &tool.Document{}
		}
		if page == 1 {
			out.Total = doc.Total()
		}
		for _, r := range doc.Included {
			k := r.Type + "\x00" + r.ID
			if !seenIncl[k] {
				seenIncl[k] = true
				out.Included = append(out.Included, r)
			}
		}
		if len(doc.Data) > remaining {
			out.Data = append(out.Data, doc.Data[:remaining]...)
			out.NextCursor = EncodeCursor(pageSetLimit(cur, origLimit))
			break
		}
		out.Data = append(out.Data, doc.Data...)
		next := doc.Links["next"]
		if next == "" {
			break
		}
		ref, err := url.Parse(next)
		if err != nil {
			return nil, fmt.Errorf("asc: paginate: page %d next link: %w", page, err)
		}
		cur0, _ := url.Parse(cur)
		nextURL, err := checkPageURL(cur0.ResolveReference(ref), base, wantPath)
		if err != nil {
			return nil, fmt.Errorf("asc: paginate: page %d next link: %w", page, err)
		}
		if seenURL[nextURL] {
			return nil, fmt.Errorf("%w: page %d repeats %s", ErrPaginationLoop, page, nextURL)
		}
		seenURL[nextURL] = true
		if len(out.Data) >= maxItems || len(doc.Data) == 0 {
			out.NextCursor = EncodeCursor(pageSetLimit(nextURL, origLimit))
			break
		}
		cur = nextURL
	}
	out.Returned = len(out.Data)
	return out, nil
}

// pageWithLimit lowers an existing limit query parameter of the canonical URL
// u to n when n is smaller; other URLs are returned unchanged.
func pageWithLimit(u string, n int) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	q := p.Query()
	cur, err := strconv.Atoi(q.Get("limit"))
	if err != nil || cur <= n {
		return u
	}
	q.Set("limit", strconv.Itoa(n))
	p.RawQuery = q.Encode()
	return p.String()
}

// pageSetLimit sets the limit parameter of the canonical URL u to limit, or
// removes it when limit is empty.
func pageSetLimit(u, limit string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	q := p.Query()
	if limit == "" {
		q.Del("limit")
	} else {
		q.Set("limit", limit)
	}
	p.RawQuery = q.Encode()
	return p.String()
}
