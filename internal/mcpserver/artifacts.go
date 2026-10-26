package mcpserver

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Artifact store limits, per session.
const (
	artifactMaxBytes   = 32 << 20
	artifactMaxEntries = 64
	artifactTTL        = 60 * time.Minute
	artifactPrefix     = "app-store-connect-mcp://artifacts/"
)

// artifact is one stored output.
type artifact struct {
	id, name, mime string
	data           []byte
	at             time.Time
}

// artifactStore is a per-session LRU of redacted tool outputs served as the
// resource template app-store-connect-mcp://artifacts/{id}. Ids are 128-bit
// random, so one session can't guess another's links; each session also
// owns its own store.
type artifactStore struct {
	now   func() time.Time
	mu    sync.Mutex
	order *list.List // front = most recently used; values are *artifact
	byID  map[string]*list.Element
	bytes int
	// maxBytes caps the stored total (artifactMaxBytes; smaller in tests).
	maxBytes int
}

var _ tool.Artifacts = (*artifactStore)(nil)

func newArtifactStore(now func() time.Time) *artifactStore {
	if now == nil {
		now = time.Now
	}
	return &artifactStore{now: now, order: list.New(), byID: map[string]*list.Element{}, maxBytes: artifactMaxBytes}
}

// Put redacts b, stores it and returns its resource link.
func (a *artifactStore) Put(_ context.Context, name, mimeType string, b []byte) (tool.Link, error) {
	tooLarge := func(n int) error {
		return fmt.Errorf("artifact %q: %d bytes over the %d-byte session limit: %w", name, n, a.maxBytes, tool.ErrResponseTooLarge)
	}
	if len(b) > a.maxBytes {
		return tool.Link{}, tooLarge(len(b))
	}
	data := asc.RedactBytes(append([]byte(nil), b...))
	if len(data) > a.maxBytes {
		return tool.Link{}, tooLarge(len(data))
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return tool.Link{}, fmt.Errorf("artifact id: %w", err)
	}
	art := &artifact{id: hex.EncodeToString(raw[:]), name: name, mime: mimeType, data: data, at: a.now()}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	a.byID[art.id] = a.order.PushFront(art)
	a.bytes += len(data)
	for a.order.Len() > artifactMaxEntries || a.bytes > a.maxBytes {
		a.removeLocked(a.order.Back())
	}
	return tool.Link{URI: artifactPrefix + art.id, Name: name, MIMEType: mimeType, Size: int64(len(data))}, nil
}

// get returns a live artifact and marks it recently used.
func (a *artifactStore) get(id string) (*artifact, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	el, ok := a.byID[id]
	if !ok {
		return nil, false
	}
	a.order.MoveToFront(el)
	return el.Value.(*artifact), true
}

func (a *artifactStore) expireLocked() {
	cutoff := a.now().Add(-artifactTTL)
	for el := a.order.Back(); el != nil; {
		prev := el.Prev()
		if !el.Value.(*artifact).at.After(cutoff) {
			a.removeLocked(el)
		}
		el = prev
	}
}

func (a *artifactStore) removeLocked(el *list.Element) {
	art := el.Value.(*artifact)
	a.order.Remove(el)
	delete(a.byID, art.id)
	a.bytes -= len(art.data)
}

// readArtifact serves app-store-connect-mcp://artifacts/{id}.
func (s *session) readArtifact(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	id := strings.TrimPrefix(uri, artifactPrefix)
	if id == uri || id == "" {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	art, ok := s.artifacts.get(id)
	if !ok {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	c := &mcp.ResourceContents{URI: uri, MIMEType: art.mime}
	if isTextMIME(art.mime) {
		c.Text = string(art.data)
	} else {
		c.Blob = art.data
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{c}}, nil
}

func isTextMIME(m string) bool {
	return strings.HasPrefix(m, "text/") || strings.HasSuffix(m, "json") || strings.HasSuffix(m, "+xml") || m == "application/xml"
}
