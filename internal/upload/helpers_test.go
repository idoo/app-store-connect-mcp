package upload

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

// writeCtx obtains a write-capable context the only way tools can: from
// Tool.Call of a Write tool in a write-enabled profile.
func writeCtx(t *testing.T) context.Context {
	t.Helper()
	var got context.Context
	tl := tool.New(tool.Meta{
		Name: "upload_test_write", Toolset: tool.Uploads, Title: "Test",
		Description: "Test tool.", Class: tool.Write,
	}, func(ctx context.Context, _ *tool.Env, _ struct{}) (*tool.Result, error) {
		got = ctx
		return nil, nil
	})
	tl.Call(context.Background(), &tool.Env{Profile: tool.ProfileInfo{AllowWrites: true}}, json.RawMessage(`{}`))
	if got == nil || !tool.IsWrite(got) {
		t.Fatal("no write context")
	}
	return got
}

// fetchCall is one recorded part request.
type fetchCall struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

// recordingFetcher records part requests (headers and bytes) and delegates
// to a FakeFetcher, which enforces https and the forbidden-header rule.
// failures[url] answers that many leading requests with failStatus.
type recordingFetcher struct {
	inner      *tooltest.FakeFetcher
	mu         sync.Mutex
	calls      []fetchCall
	failures   map[string]int
	failStatus int
}

func newRecordingFetcher(t *testing.T) *recordingFetcher {
	return &recordingFetcher{inner: tooltest.NewFakeFetcher(t), failures: map[string]int{}, failStatus: http.StatusServiceUnavailable}
}

func (f *recordingFetcher) Fetch(ctx context.Context, method, rawURL string, header http.Header, body io.Reader, maxBytes int64) (*tool.FetchResponse, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, fetchCall{Method: method, URL: rawURL, Header: header.Clone(), Body: b})
	fail := f.failures[rawURL] > 0
	if fail {
		f.failures[rawURL]--
	}
	f.mu.Unlock()
	if fail {
		return &tool.FetchResponse{Status: f.failStatus, Header: http.Header{}}, nil
	}
	return f.inner.Fetch(ctx, method, rawURL, header, bytes.NewReader(b), maxBytes)
}

func (f *recordingFetcher) snapshot() []fetchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// partURL is the presigned URL of part i in fixtures.
func partURL(i int) string { return fmt.Sprintf("https://upload.example.com/part/%d?sig=x", i) }

// splitOps returns n operations covering size bytes, each asking for a
// Content-Type header plus a forbidden Authorization header.
func splitOps(size int64, n int) []Operation {
	ops := make([]Operation, n)
	per := size / int64(n)
	for i := range ops {
		length := per
		if i == n-1 {
			length = size - per*int64(n-1)
		}
		ops[i] = Operation{
			Method: "PUT", URL: partURL(i), Offset: per * int64(i), Length: length,
			RequestHeaders: []Header{{Name: "Content-Type", Value: "application/octet-stream"}, {Name: "Authorization", Value: "Bearer leak"}},
		}
	}
	return ops
}

// resourceJSON renders a JSON:API single-resource document.
func resourceJSON(t *testing.T, typ, id string, attrs map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"data": map[string]any{"type": typ, "id": id, "attributes": attrs}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// loggedRequest is the golden form of one ASC request.
type loggedRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query,omitempty"`
	Body   any    `json:"body,omitempty"`
}

func requestLog(reqs []tool.Request) []loggedRequest {
	out := make([]loggedRequest, len(reqs))
	for i, r := range reqs {
		out[i] = loggedRequest{Method: r.Method, Path: r.Path, Query: r.Query.Encode(), Body: r.Body}
	}
	return out
}

// bplistEncoder writes binary property lists for tests.
type bplistEncoder struct{ objs [][]byte }

func (e *bplistEncoder) add(t *testing.T, v any) byte {
	t.Helper()
	i := len(e.objs)
	e.objs = append(e.objs, nil)
	var b []byte
	switch v := v.(type) {
	case string:
		ascii := true
		for _, r := range v {
			if r > 0x7F {
				ascii = false
			}
		}
		if ascii {
			b = append(lengthMarker(0x50, len(v)), v...)
		} else {
			u := utf16.Encode([]rune(v))
			b = lengthMarker(0x60, len(u))
			for _, c := range u {
				b = binary.BigEndian.AppendUint16(b, c)
			}
		}
	case int64:
		b = binary.BigEndian.AppendUint64([]byte{0x13}, uint64(v)) //nolint:gosec // test data
	case bool:
		b = []byte{0x08}
		if v {
			b[0] = 0x09
		}
	case []any:
		refs := make([]byte, 0, len(v))
		for _, c := range v {
			refs = append(refs, e.add(t, c))
		}
		b = append(lengthMarker(0xA0, len(v)), refs...)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var krefs, vrefs []byte
		for _, k := range keys {
			krefs = append(krefs, e.add(t, k))
			vrefs = append(vrefs, e.add(t, v[k]))
		}
		b = append(append(lengthMarker(0xD0, len(v)), krefs...), vrefs...)
	default:
		t.Fatalf("bplist: unsupported %T", v)
	}
	e.objs[i] = b
	if i > 0xFF {
		t.Fatal("bplist: too many objects for 1-byte refs")
	}
	return byte(i)
}

func lengthMarker(kind byte, n int) []byte {
	if n < 15 {
		return []byte{kind | byte(n)}
	}
	return []byte{kind | 0x0F, 0x10, byte(n)}
}

func encodeBplist(t *testing.T, root any) []byte {
	t.Helper()
	e := &bplistEncoder{}
	e.add(t, root)
	out := []byte("bplist00")
	offsets := make([]int, len(e.objs))
	for i, o := range e.objs {
		offsets[i] = len(out)
		out = append(out, o...)
	}
	table := len(out)
	for _, off := range offsets {
		out = binary.BigEndian.AppendUint16(out, uint16(off)) //nolint:gosec // test data is small
	}
	out = append(out, 0, 0, 0, 0, 0, 0, 2, 1)
	out = binary.BigEndian.AppendUint64(out, uint64(len(e.objs)))
	out = binary.BigEndian.AppendUint64(out, 0)
	out = binary.BigEndian.AppendUint64(out, uint64(table)) //nolint:gosec // test data
	return out
}

const xmlInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleShortVersionString</key>
	<string>2.3.1</string>
	<key>CFBundleVersion</key>
	<string>417</string>
	<key>UIDeviceFamily</key>
	<array><integer>1</integer><integer>2</integer></array>
	<key>LSRequiresIPhoneOS</key>
	<true/>
	<key>Blob</key>
	<data>aGVs
	bG8=</data>
	<key>Scale</key>
	<real>2.5</real>
</dict>
</plist>
`

// makeIPA builds a zip with Payload/Hjul.app/Info.plist and a pseudo binary.
func makeIPA(t *testing.T, plist []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"Payload/", nil},
		{"Payload/Hjul.app/Info.plist", plist},
		{"Payload/Hjul.app/Hjul", bytes.Repeat([]byte("machO\x00\x01"), 900)},
		{"Payload/Hjul.app/PlugIns/Widget.appex/Info.plist", []byte("ignored")},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustContain(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("error = %v, want containing %q", err, sub)
	}
}
