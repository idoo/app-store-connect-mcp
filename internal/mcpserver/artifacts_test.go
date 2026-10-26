package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// listASC serves /v1/apps from List and counts the calls.
type listASC struct {
	tool.ASC
	lists atomic.Int32
}

func (a *listASC) List(context.Context, string, tool.ListOptions) (*tool.ListResult, error) {
	a.lists.Add(1)
	return &tool.ListResult{Data: []tool.Resource{
		{ID: "1", Type: "apps", Attributes: map[string]any{"name": "Hjul", "bundleId": "se.jvcr.hjul"}},
		{ID: "2", Type: "apps", Attributes: map[string]any{"name": "Sikt", "bundleId": "se.jvcr.sift"}},
	}}, nil
}

func readText(t *testing.T, cs *mcp.ClientSession, uri string) string {
	t.Helper()
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("read %s: %v", uri, err)
	}
	return res.Contents[0].Text
}

func TestArtifactRoundTripAndNotFound(t *testing.T) {
	h := connect(t, Deps{Tools: (&fixture{}).tools(), Profile: team(false)}, "test", nil)
	l, err := h.s.artifacts.Put(context.Background(), "rows.csv", "text/csv", []byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(l.URI, artifactPrefix) || len(strings.TrimPrefix(l.URI, artifactPrefix)) != 32 || l.Size != 8 {
		t.Fatalf("link = %+v", l)
	}
	if got := readText(t, h.cs, l.URI); got != "a,b\n1,2\n" {
		t.Errorf("content = %q", got)
	}
	_, err = h.cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: artifactPrefix + "00000000000000000000000000000000"})
	var we *jsonrpc.Error
	if !errors.As(err, &we) || we.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("missing artifact err = %v", err)
	}
}

func TestArtifactSessionIsolation(t *testing.T) {
	a := connect(t, Deps{Tools: (&fixture{}).tools(), Profile: team(false)}, "a", nil)
	b := connect(t, Deps{Tools: (&fixture{}).tools(), Profile: team(false)}, "b", nil)
	l, err := a.s.artifacts.Put(context.Background(), "x.json", "application/json", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: l.URI}); err == nil {
		t.Error("session b read session a's artifact")
	}
}

func TestArtifactLRUAndTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	a := newArtifactStore(func() time.Time { return now })
	ctx := context.Background()
	var first tool.Link
	for i := range artifactMaxEntries + 1 {
		l, err := a.Put(ctx, "n", "text/plain", []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = l
		}
	}
	if _, ok := a.get(strings.TrimPrefix(first.URI, artifactPrefix)); ok {
		t.Error("oldest entry survived the entry cap")
	}
	if a.order.Len() != artifactMaxEntries {
		t.Errorf("entries = %d", a.order.Len())
	}
	a.maxBytes = 4096 // the byte rule, without 32 MiB of redaction work
	big := make([]byte, a.maxBytes/2+1)
	if _, err := a.Put(ctx, "b1", "application/octet-stream", big); err != nil {
		t.Fatal(err)
	}
	l2, err := a.Put(ctx, "b2", "application/octet-stream", big)
	if err != nil {
		t.Fatal(err)
	}
	if a.bytes > a.maxBytes {
		t.Errorf("bytes = %d over cap", a.bytes)
	}
	if _, err := a.Put(ctx, "huge", "application/octet-stream", make([]byte, a.maxBytes+1)); !errors.Is(err, tool.ErrResponseTooLarge) {
		t.Errorf("oversize err = %v", err)
	}
	now = now.Add(artifactTTL)
	if _, ok := a.get(strings.TrimPrefix(l2.URI, artifactPrefix)); ok {
		t.Error("entry survived its TTL")
	}
}

func TestArtifactRedactsBeforePut(t *testing.T) {
	a := newArtifactStore(nil)
	pem := "-----BEGIN " + "NOTE-----MIGTAgEA-----END " + "NOTE-----"
	l, err := a.Put(context.Background(), "k", "text/plain", []byte("key: "+pem))
	if err != nil {
		t.Fatal(err)
	}
	art, _ := a.get(strings.TrimPrefix(l.URI, artifactPrefix))
	if strings.Contains(string(art.data), "MIGTAgEA") {
		t.Errorf("stored unredacted: %q", art.data)
	}
}

func TestRenderLinksAndTruncationArtifact(t *testing.T) {
	s := &session{deps: Deps{MaxResultBytes: 2048}, artifacts: newArtifactStore(nil)}
	items := make([]string, 200)
	for i := range items {
		items[i] = strings.Repeat("x", 40)
	}
	out := s.render(tool.OK(map[string]any{"items": items}).WithLink(tool.Link{URI: "app-store-connect-mcp://artifacts/a", Name: "a.csv", MIMEType: "text/csv", Size: 3}))
	var links []*mcp.ResourceLink
	for _, c := range out.Content {
		if rl, ok := c.(*mcp.ResourceLink); ok {
			links = append(links, rl)
		}
	}
	if _, ok := out.Content[0].(*mcp.TextContent); !ok {
		t.Fatalf("first content = %T, want text", out.Content[0])
	}
	if len(links) != 2 || links[0].Name != "a.csv" || *links[0].Size != 3 || !strings.HasPrefix(links[1].URI, artifactPrefix) {
		t.Fatalf("links = %+v", links)
	}
	art, ok := s.artifacts.get(strings.TrimPrefix(links[1].URI, artifactPrefix))
	if !ok || !json.Valid(art.data) || strings.Count(string(art.data), strings.Repeat("x", 40)) != 200 {
		t.Error("truncation artifact is not the full JSON")
	}
}

func TestCapNext(t *testing.T) {
	big := strings.Repeat("y", 5000)
	next := []tool.NextStep{{Tool: "metadata_apply", Args: map[string]any{"plan_id": "p1", "listing": map[string]any{"en-US": big}}}}
	got, omitted := capNext(next, nextCapBytes)
	if !omitted || got[0].Args["plan_id"] != "p1" {
		t.Fatalf("got %+v, omitted %v", got, omitted)
	}
	if v, _ := got[0].Args["listing"].(map[string]string); v["_omitted"] == "" {
		t.Errorf("listing = %v", got[0].Args["listing"])
	}
	if _, ok := next[0].Args["listing"].(map[string]any); !ok {
		t.Error("capNext mutated its input")
	}
	small := []tool.NextStep{{Tool: "t", Args: map[string]any{"a": strings.Repeat("z", 1500)}}}
	if _, omitted := capNext(small, nextCapBytes); omitted {
		t.Error("omitted although next fits under the cap")
	}

	s := &session{deps: Deps{MaxResultBytes: -1}}
	out := s.render(tool.OK(map[string]any{}).Then(next...))
	text := out.Content[0].(*mcp.TextContent).Text
	if strings.Contains(text, big) || !strings.Contains(text, "resend the same value") || !strings.Contains(text, "omitted") {
		t.Errorf("render text = %.300s", text)
	}
}

func TestCompletionUsesCachedApps(t *testing.T) {
	asc := &listASC{}
	deps := Deps{Tools: (&fixture{}).tools(), Profile: team(false)}
	deps.Env.ASC = asc
	h := connect(t, deps, "test", nil)
	ctx := context.Background()
	complete := func(ref *mcp.CompleteReference, name, value string) []string {
		t.Helper()
		res, err := h.cs.Complete(ctx, &mcp.CompleteParams{Ref: ref, Argument: mcp.CompleteParamsArgument{Name: name, Value: value}})
		if err != nil {
			t.Fatalf("complete %s=%q: %v", name, value, err)
		}
		return res.Completion.Values
	}
	prompt := &mcp.CompleteReference{Type: "ref/prompt", Name: "release_checklist"}
	if got := complete(prompt, "app", "hj"); len(got) != 1 || got[0] != "Hjul" {
		t.Errorf("app hj = %v", got)
	}
	if got := complete(prompt, "app", "se.jvcr"); len(got) != 2 {
		t.Errorf("app se.jvcr = %v", got)
	}
	if got := complete(prompt, "app", ""); len(got) != 4 {
		t.Errorf("app all = %v", got)
	}
	if n := asc.lists.Load(); n != 1 {
		t.Errorf("/v1/apps lists = %d, want 1", n)
	}
	if got := complete(&mcp.CompleteReference{Type: "ref/resource", URI: specOpPrefix + "{operationId}"}, "platform", "MAC"); len(got) != 1 || got[0] != "MAC_OS" {
		t.Errorf("platform = %v", got)
	}
}
