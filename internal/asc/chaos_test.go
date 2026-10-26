package asc

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// A POST applied by Apple (201) whose body is a proxy page or truncated JSON
// is outcome_unknown and upstream_invalid, and is never replayed.
func TestChaosPost201GarbageBodyIsOutcomeUnknown(t *testing.T) {
	for _, body := range []string{"<html>Bad Gateway</html>", `{"data":{"type":"inAppPurchases","id":"1"`} {
		hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, body)
		}, nil)
		_, err := hs.c.Do(writeCtx(t), tool.Request{
			Method: http.MethodPost, Path: "/v1/inAppPurchases",
			Body: map[string]any{"data": map[string]any{"type": "inAppPurchases"}}, Write: true,
		})
		if !errors.Is(err, tool.ErrOutcomeUnknown) || !errors.Is(err, tool.ErrUpstreamInvalid) {
			t.Fatalf("body %q: err = %v, want outcome_unknown wrapping upstream_invalid", body, err)
		}
		if got := tool.BodyFor(err).Error; got != "outcome_unknown" {
			t.Errorf("body %q: code = %q, want outcome_unknown", body, got)
		}
		if n := hs.hits.Load(); n != 1 {
			t.Errorf("body %q: requests = %d, want 1", body, n)
		}
	}
}

// A DELETE applied before the connection dropped is replayed; the replay's
// 404 means the object is already gone.
func TestChaosDeleteReplay404IsAlreadyGone(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "no such resource", "")
	}, nil)
	_, err := hs.c.Do(writeCtx(t), tool.Request{Method: http.MethodDelete, Path: "/v1/betaGroups/G1", Write: true})
	if !errors.Is(err, tool.ErrAlreadyGone) {
		t.Fatalf("err = %v, want already_gone", err)
	}
	if errors.Is(err, tool.ErrOutcomeUnknown) {
		t.Errorf("err = %v, want not outcome_unknown", err)
	}
	if got := n.Load(); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}

// A DELETE replayed after a 503 that then 404s is already gone too.
func TestChaosDelete503Then404IsAlreadyGone(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "x", "")
			return
		}
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "no such resource", "")
	}, nil)
	_, err := hs.c.Do(writeCtx(t), tool.Request{Method: http.MethodDelete, Path: "/v1/betaGroups/G1", Write: true})
	if !errors.Is(err, tool.ErrAlreadyGone) {
		t.Fatalf("err = %v, want already_gone", err)
	}
}

// A first-try DELETE 404 is a wrong id, not success.
func TestChaosDeleteFirstTry404StaysError(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "no such resource", "")
	}, nil)
	_, err := hs.c.Do(writeCtx(t), tool.Request{Method: http.MethodDelete, Path: "/v1/betaGroups/G1", Write: true})
	if err == nil || errors.Is(err, tool.ErrAlreadyGone) {
		t.Fatalf("err = %v, want a plain 404", err)
	}
	if tool.APIStatus(err) != http.StatusNotFound {
		t.Errorf("status = %d, want 404", tool.APIStatus(err))
	}
	if hs.hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hs.hits.Load())
	}
}

// Replay-safe PUT/DELETE that exhaust their 5xx retries have an unknown
// outcome; a GET does not.
func TestChaosIdempotent5xxExhaustedIsOutcomeUnknown(t *testing.T) {
	for _, m := range []string{http.MethodDelete, http.MethodPut, http.MethodGet} {
		hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "x", "")
		}, nil)
		req := tool.Request{Method: m, Path: "/v1/betaGroups/G1"}
		if m != http.MethodGet {
			req.Write = true
		}
		_, err := hs.c.Do(writeCtx(t), req)
		if err == nil {
			t.Fatalf("%s: err = nil", m)
		}
		if want := m != http.MethodGet; errors.Is(err, tool.ErrOutcomeUnknown) != want {
			t.Errorf("%s: err = %v, outcome_unknown want %v", m, err, want)
		}
		if n := hs.hits.Load(); n != 4 {
			t.Errorf("%s: requests = %d, want 4", m, n)
		}
	}
}

// An oversized response is response_too_large, not retried, and not
// outcome_unknown on a GET.
func TestChaosResponseTooLarge(t *testing.T) {
	old := maxResponseBytes
	maxResponseBytes = 1024
	t.Cleanup(func() { maxResponseBytes = old })
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[],"pad":"`+strings.Repeat("x", 2048)+`"}`)
	}, nil)
	_, err := hs.c.Get(writeCtx(t), "/v1/apps", nil)
	if !errors.Is(err, tool.ErrResponseTooLarge) {
		t.Fatalf("err = %v, want response_too_large", err)
	}
	if errors.Is(err, tool.ErrInvalidArgs) || errors.Is(err, tool.ErrOutcomeUnknown) {
		t.Errorf("err = %v, want neither invalid_arguments nor outcome_unknown", err)
	}
	if got := tool.BodyFor(err).Error; got != "response_too_large" {
		t.Errorf("code = %q, want response_too_large", got)
	}
	if hs.hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hs.hits.Load())
	}
}

// A GET 200 with an HTML maintenance page is retried like a 502.
func TestChaosGetHTML200Retried(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>maintenance</html>")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"type":"apps","id":"1"}]}`)
	}, nil)
	resp, err := hs.c.Get(writeCtx(t), "/v1/apps", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !resp.Collection || len(resp.Data) != 1 {
		t.Errorf("response = collection %v with %d items, want 1", resp.Collection, len(resp.Data))
	}
	if got := n.Load(); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}

// A GET that keeps returning HTML 200 ends as upstream_invalid, never
// outcome_unknown.
func TestChaosGetHTML200Exhausted(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>maintenance</html>")
	}, nil)
	_, err := hs.c.Get(writeCtx(t), "/v1/apps", nil)
	if !errors.Is(err, tool.ErrUpstreamInvalid) || errors.Is(err, tool.ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want upstream_invalid only", err)
	}
	if got := tool.BodyFor(err).Error; got != "upstream_invalid" {
		t.Errorf("code = %q, want upstream_invalid", got)
	}
	if n := hs.hits.Load(); n != 4 {
		t.Errorf("requests = %d, want 4", n)
	}
}

// A PUT 200 with garbage is not replayed (only GET/HEAD are) and is
// outcome_unknown.
func TestChaosPut200GarbageNotReplayed(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>oops</html>")
	}, nil)
	_, err := hs.c.Do(writeCtx(t), tool.Request{Method: http.MethodPut, Path: "/v1/betaGroups/G1", Body: map[string]any{}, Write: true})
	if !errors.Is(err, tool.ErrOutcomeUnknown) || !errors.Is(err, tool.ErrUpstreamInvalid) {
		t.Fatalf("err = %v, want outcome_unknown wrapping upstream_invalid", err)
	}
	if hs.hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hs.hits.Load())
	}
}

// PATCH 401 refreshes once, then a 500 is outcome_unknown without replay.
func TestChaosPatch401Then500(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			writeAPIError(w, http.StatusUnauthorized, "NOT_AUTHORIZED", "expired", "")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "x", "")
	}, nil)
	_, err := hs.c.Do(writeCtx(t), tool.Request{
		Method: http.MethodPatch, Path: "/v1/appStoreVersionLocalizations/L1",
		Body: map[string]any{"data": map[string]any{"type": "appStoreVersionLocalizations", "id": "L1"}}, Write: true,
	})
	if !errors.Is(err, tool.ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want outcome_unknown", err)
	}
	if got := n.Load(); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}
