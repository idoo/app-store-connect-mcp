package upload

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// MaxUploadHosts is the maximum number of distinct hosts one reservation's
// upload operations may target.
const MaxUploadHosts = 16

// Header is one request header Apple asks to send with an upload part.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Operation is one part of a reserved upload: send Length bytes at
// Offset of the file to URL with Method and RequestHeaders. It mirrors the
// spec's UploadOperation / DeliveryFileUploadOperation schemas.
type Operation struct {
	Method         string   `json:"method"`
	URL            string   `json:"url"`
	Length         int64    `json:"length"`
	Offset         int64    `json:"offset"`
	RequestHeaders []Header `json:"requestHeaders,omitempty"`
	PartNumber     int64    `json:"partNumber,omitempty"`
}

// ParseOperations extracts attributes.uploadOperations from a reservation
// response resource (flattened, so attributes are top-level keys).
func ParseOperations(r tool.Resource) ([]Operation, error) {
	raw, ok := r.Attributes["uploadOperations"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("%s %s: response has no uploadOperations", r.Type, r.ID)
	}
	b, err := json.Marshal(tool.Unwrap(raw))
	if err != nil {
		return nil, fmt.Errorf("%s %s: encode uploadOperations: %w", r.Type, r.ID, err)
	}
	var ops []Operation
	if err := json.Unmarshal(b, &ops); err != nil {
		return nil, fmt.Errorf("%s %s: decode uploadOperations: %w", r.Type, r.ID, err)
	}
	return ops, nil
}

// ValidateOperations checks ops against the upload URL policy and the file:
// every operation is an https PUT or POST to port 443 without userinfo, at
// most [MaxUploadHosts] distinct hosts are used, and the byte ranges cover
// [0, size) exactly once.
func ValidateOperations(ops []Operation, size int64) error {
	if len(ops) == 0 {
		return fmt.Errorf("upload: no upload operations")
	}
	if size <= 0 {
		return fmt.Errorf("upload: file is empty")
	}
	hosts := map[string]bool{}
	for i, op := range ops {
		if err := checkOperationURL(op); err != nil {
			return fmt.Errorf("upload operation %d: %w", i, err)
		}
		u, _ := url.Parse(op.URL) // parsed successfully by checkOperationURL
		hosts[strings.ToLower(u.Hostname())] = true
		if op.Offset < 0 || op.Length <= 0 || op.Length > size-op.Offset {
			return fmt.Errorf("upload operation %d: range offset=%d length=%d outside file of %d bytes", i, op.Offset, op.Length, size)
		}
	}
	if len(hosts) > MaxUploadHosts {
		return fmt.Errorf("upload: operations target %d hosts (max %d)", len(hosts), MaxUploadHosts)
	}
	idx := make([]int, len(ops))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int { return cmp.Compare(ops[a].Offset, ops[b].Offset) })
	var next int64
	for _, i := range idx {
		if ops[i].Offset != next {
			return fmt.Errorf("upload: operations leave a gap or overlap at byte %d", next)
		}
		next = ops[i].Offset + ops[i].Length
	}
	if next != size {
		return fmt.Errorf("upload: operations cover %d of %d bytes", next, size)
	}
	return nil
}

func checkOperationURL(op Operation) error {
	switch strings.ToUpper(op.Method) {
	case http.MethodPut, http.MethodPost:
	default:
		return fmt.Errorf("method %q refused (PUT or POST only)", op.Method)
	}
	u, err := url.Parse(op.URL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	switch {
	case u.Scheme != "https":
		return fmt.Errorf("url scheme %q refused (https only)", u.Scheme)
	case u.User != nil:
		return fmt.Errorf("url with userinfo refused")
	case u.Hostname() == "":
		return fmt.Errorf("url without host refused")
	case u.Port() != "" && u.Port() != "443":
		return fmt.Errorf("url port %s refused (443 only)", u.Port())
	}
	return nil
}

// partHeader builds the request headers of op, dropping any header the
// unauthenticated fetch policy forbids (Authorization, Cookie, Host, Proxy-*).
func partHeader(op Operation) http.Header {
	h := http.Header{}
	for _, rh := range op.RequestHeaders {
		if forbiddenHeader(rh.Name) {
			continue
		}
		h.Add(rh.Name, rh.Value)
	}
	return h
}

func forbiddenHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "" || n == "authorization" || n == "cookie" || n == "host" || strings.HasPrefix(n, "proxy-")
}
