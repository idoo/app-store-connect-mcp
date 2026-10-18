package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// asoFetch serves canned iTunes responses by exact URL and counts calls.
type asoFetch struct {
	bodies map[string]string
	calls  int
}

func (f *asoFetch) Fetch(_ context.Context, _, rawURL string, _ http.Header, _ io.Reader, _ int64) (*tool.FetchResponse, error) {
	f.calls++
	b, ok := f.bodies[rawURL]
	if !ok {
		return &tool.FetchResponse{Status: http.StatusNotFound}, nil
	}
	return &tool.FetchResponse{Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(b)}, nil
}

// asoState is an in-memory tool.StateStore.
type asoState map[string][]byte

func (asoState) Cursor(string, string, string) (string, error)  { return "", nil }
func (asoState) SetCursor(string, string, string, string) error { return nil }

func (s asoState) Load(kind, key string, v any) (bool, error) {
	b, ok := s[kind+"/"+key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}

func (s asoState) Save(kind, key string, v any) error {
	b, err := json.Marshal(v)
	s[kind+"/"+key] = b
	return err
}

func (s asoState) Delete(kind, key string) error {
	delete(s, kind+"/"+key)
	return nil
}

// stubAsoEnv makes runAso use an env with fetch and an in-memory state.
func stubAsoEnv(t *testing.T, fetch *asoFetch) {
	t.Helper()
	env := &tool.Env{Fetch: fetch, State: asoState{}, Profile: tool.ProfileInfo{Name: "test"}}
	old := asoEnv
	t.Cleanup(func() { asoEnv = old })
	asoEnv = func(context.Context, string) (*tool.Env, error) { return env, nil }
}

func TestAsoUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"track"},
		{"report"},
		{"track", "--app", "1234567890"},
		{"report", "--app", "1", "extra"},
		{"track", "--nope"},
	} {
		var out, errOut bytes.Buffer
		err := runAso(context.Background(), args, &out, &errOut)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("aso %v: err = %v, want usage error", args, err)
		}
		if out.Len() != 0 {
			t.Errorf("aso %v printed %q", args, out.String())
		}
	}
}

func TestAsoTrackThenReport(t *testing.T) {
	const app = `{"trackId":1234567890,"trackName":"Hjul","bundleId":"se.jvcr.hjul"}`
	fetch := &asoFetch{bodies: map[string]string{
		"https://itunes.apple.com/search?country=se&entity=software&limit=20&term=bike": `{"resultCount":1,"results":[` + app + `]}`,
	}}
	stubAsoEnv(t, fetch)

	var out, errOut bytes.Buffer
	if err := runAso(context.Background(), []string{"track", "--app", "1234567890", "--keywords", "bike, ", "--storefronts", "SE", "--depth", "20"}, &out, &errOut); err != nil {
		t.Fatalf("track: %v (stderr %q, out %s)", err, errOut.String(), out.String())
	}
	var got struct {
		Data struct {
			Action   string `json:"action"`
			Measured int    `json:"measured"`
			Ranks    []struct {
				Keyword string `json:"keyword"`
				Country string `json:"country"`
				Rank    *int   `json:"rank"`
			} `json:"ranks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", out.String(), err)
	}
	if got.Data.Action != "track" || got.Data.Measured != 1 || len(got.Data.Ranks) != 1 ||
		got.Data.Ranks[0].Rank == nil || *got.Data.Ranks[0].Rank != 1 || got.Data.Ranks[0].Country != "se" {
		t.Fatalf("track out = %s", out.String())
	}

	calls := fetch.calls
	out.Reset()
	if err := runAso(context.Background(), []string{"report", "--app", "1234567890"}, &out, &errOut); err != nil {
		t.Fatalf("report: %v", err)
	}
	if !strings.Contains(out.String(), `"action": "report"`) || !strings.Contains(out.String(), `"keyword": "bike"`) || fetch.calls != calls {
		t.Fatalf("report out = %s", out.String())
	}
}

func TestAsoToolErrorExitsOne(t *testing.T) {
	stubAsoEnv(t, &asoFetch{})
	var out, errOut bytes.Buffer
	// Nothing tracked: report on a fresh history is fine, but an out-of-range
	// depth is a tool argument error (exit 1, not 2).
	err := runAso(context.Background(), []string{"track", "--app", "1234567890", "--keywords", "bike", "--depth", "500"}, &out, &errOut)
	var ue *usageError
	if err == nil || errors.As(err, &ue) {
		t.Fatalf("err = %v, want a tool error", err)
	}
	if !strings.Contains(out.String(), "invalid_arguments") {
		t.Errorf("out = %s", out.String())
	}
}
