package spec

import (
	"bytes"
	"compress/gzip"
	_ "embed" // index.json.gz
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/idoo/hamn/internal/tool"
)

// Regenerate with Apple's spec: HAMN_SPEC=/path/to/openapi.oas.json go generate ./internal/spec
//go:generate sh -c "go run ./gen -out . -in \"${HAMN_SPEC:?set HAMN_SPEC to the path of Apple's openapi.oas.json}\""

//go:embed index.json.gz
var indexGz []byte

// maxIndexBytes bounds the decompressed embedded index.
const maxIndexBytes = 64 << 20

// Index is the decoded, immutable spec index. It is safe for concurrent use.
type Index struct {
	file   *File
	byID   map[string]*Op
	bySegs map[int][]template
	search searchIndex
}

// template is a path template split into segments for matching.
type template struct {
	op       *Op
	segs     []string
	literals int
}

var _ tool.Spec = (*Index)(nil)

var (
	loadOnce sync.Once
	loaded   *Index
	errLoad  error
)

// Load decodes the embedded index once and returns it.
func Load() (*Index, error) {
	loadOnce.Do(func() { loaded, errLoad = parse(indexGz) })
	return loaded, errLoad
}

func parse(gz []byte) (*Index, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("spec index: %w", err)
	}
	defer func() { _ = zr.Close() }()
	raw, err := io.ReadAll(io.LimitReader(zr, maxIndexBytes+1))
	if err != nil {
		return nil, fmt.Errorf("spec index: %w", err)
	}
	if len(raw) > maxIndexBytes {
		return nil, fmt.Errorf("spec index: larger than %d bytes", maxIndexBytes)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("spec index: %w", err)
	}
	return newIndex(&f), nil
}

func newIndex(f *File) *Index {
	x := &Index{file: f, byID: make(map[string]*Op, len(f.Ops)), bySegs: map[int][]template{}}
	for i := range f.Ops {
		op := &f.Ops[i]
		x.byID[op.ID] = op
		segs := splitPath(op.Path)
		t := template{op: op, segs: segs}
		for _, s := range segs {
			if !isParam(s) {
				t.literals++
			}
		}
		x.bySegs[len(segs)] = append(x.bySegs[len(segs)], t)
	}
	x.search = buildSearch(f.Ops)
	return x
}

// Version returns the pinned spec version.
func (x *Index) Version() string { return x.file.Version }

// PathCount returns the number of path templates in the source spec.
func (x *Index) PathCount() int { return x.file.Paths }

// Operations returns every operation in index order.
func (x *Index) Operations() []tool.Operation {
	out := make([]tool.Operation, len(x.file.Ops))
	for i := range x.file.Ops {
		out[i] = toOperation(&x.file.Ops[i])
	}
	return out
}

// Match resolves a concrete method and path to its operation. A query string
// in path is ignored.
func (x *Index) Match(method, path string) (tool.Operation, bool) {
	op, ok := x.match(strings.ToUpper(method), path)
	if !ok {
		return tool.Operation{}, false
	}
	return toOperation(op), true
}

func (x *Index) match(method, path string) (*Op, bool) {
	var best *Op
	bestLiterals := -1
	for _, t := range x.candidates(path) {
		if t.op.Method == method && t.literals > bestLiterals {
			best, bestLiterals = t.op, t.literals
		}
	}
	return best, best != nil
}

// candidates returns templates whose segments match path for any method.
func (x *Index) candidates(path string) []template {
	path, _, _ = strings.Cut(path, "?")
	if !strings.HasPrefix(path, "/") {
		return nil
	}
	segs := splitPath(path)
	var out []template
	for _, t := range x.bySegs[len(segs)] {
		if segsMatch(t.segs, segs) {
			out = append(out, t)
		}
	}
	return out
}

func segsMatch(tmpl, segs []string) bool {
	for i, s := range tmpl {
		if segs[i] == "" {
			return false
		}
		if !isParam(s) && s != segs[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string { return strings.Split(strings.TrimPrefix(p, "/"), "/") }

func isParam(seg string) bool { return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") }

func toOperation(op *Op) tool.Operation {
	return tool.Operation{
		ID: op.ID, Method: op.Method, Path: op.Path, Summary: op.Summary,
		Tags: slices.Clone(op.Tags), Deprecated: op.Deprecated,
	}
}

// FreeTextAttributes lists the attributes of resource type t that carry
// human- or third-party-written text and must be wrapped as untrusted.
func (x *Index) FreeTextAttributes(t string) []string {
	return slices.Clone(x.file.FreeText[t])
}

// FieldNames lists the fields[t] names selectable for resource type t.
func (x *Index) FieldNames(t string) []string { return slices.Clone(x.file.Fields[t]) }

// EnumLookup returns the allowed values at an error source of operation
// opID: a JSON pointer into the request body ("/data/attributes/platform",
// "/data/type", "/data/relationships/app") or a query parameter name
// ("filter[platform]"). It returns nil when nothing is enumerated there.
func (x *Index) EnumLookup(opID, pointer string) []string {
	op, ok := x.byID[opID]
	if !ok {
		return nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return x.paramEnum(op, pointer)
	}
	if op.Body == nil {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "data" && parts[1] == "type":
		return []string{op.Body.Type}
	case len(parts) >= 3 && parts[0] == "data" && parts[1] == "attributes":
		return slices.Clone(op.Body.Attrs[parts[2]].Enum)
	case len(parts) >= 3 && parts[0] == "data" && parts[1] == "relationships":
		if r, ok := op.Body.Rels[parts[2]]; ok && r.Type != "" {
			return []string{r.Type}
		}
	}
	return nil
}

func (x *Index) paramEnum(op *Op, name string) []string {
	for _, p := range op.Params {
		if p.Name != name {
			continue
		}
		if t, ok := fieldsType(name); ok {
			return x.FieldNames(t)
		}
		return slices.Clone(p.Enum)
	}
	return nil
}

// fieldsType extracts t from "fields[t]".
func fieldsType(name string) (string, bool) {
	t, ok := strings.CutPrefix(name, "fields[")
	if !ok || !strings.HasSuffix(t, "]") {
		return "", false
	}
	return strings.TrimSuffix(t, "]"), true
}
