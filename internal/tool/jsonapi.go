package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

// Resource is a flattened JSON:API resource. It marshals as
// {"id":..,"type":..,...attributes,"rel":{name: id | [ids]}}. Link-only
// relationships and per-resource links are dropped.
type Resource struct {
	ID         string
	Type       string
	Attributes map[string]any
	// Rel maps relationship name to a string id, a []string of ids, or nil
	// (explicit empty to-one).
	Rel map[string]any
}

// MarshalJSON emits the flattened form. Attribute keys "id", "type" and "rel"
// are shadowed by the resource identity.
func (r Resource) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(r.Attributes)+3)
	maps.Copy(m, r.Attributes)
	m["id"] = r.ID
	m["type"] = r.Type
	if len(r.Rel) > 0 {
		m["rel"] = r.Rel
	} else {
		delete(m, "rel")
	}
	return json.Marshal(m)
}

// UnmarshalJSON parses the flattened form produced by MarshalJSON.
func (r *Resource) UnmarshalJSON(b []byte) error {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("resource: %w", err)
	}
	r.ID, _ = m["id"].(string)
	r.Type, _ = m["type"].(string)
	r.Rel = nil
	if rel, ok := m["rel"].(map[string]any); ok {
		r.Rel = make(map[string]any, len(rel))
		for k, v := range rel {
			if arr, ok := v.([]any); ok {
				ids := make([]string, 0, len(arr))
				for _, x := range arr {
					s, _ := x.(string)
					ids = append(ids, s)
				}
				r.Rel[k] = ids
				continue
			}
			r.Rel[k] = v
		}
	}
	delete(m, "id")
	delete(m, "type")
	delete(m, "rel")
	r.Attributes = m
	return nil
}

// Str returns a string attribute, or "". Values wrapped as [Untrusted] by the
// client are unwrapped: callers that echo third-party text back to the agent
// must re-wrap it with Untrusted themselves.
func (r Resource) Str(key string) string {
	switch v := r.Attributes[key].(type) {
	case string:
		return v
	case Untrusted:
		return string(v)
	}
	return ""
}

// Text returns a free-text attribute (see [FreeTextKeys]) wrapped as
// [Untrusted], so third-party text stays marked when echoed to the agent.
func (r Resource) Text(key string) Untrusted {
	return Untrusted(r.Str(key))
}

// Bool returns a boolean attribute and whether it was present as a bool.
func (r Resource) Bool(key string) (v, ok bool) {
	v, ok = r.Attributes[key].(bool)
	return v, ok
}

// Int returns a numeric attribute as int and whether it was numeric.
func (r Resource) Int(key string) (int, bool) {
	switch v := r.Attributes[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		n, err := strconv.Atoi(string(v))
		return n, err == nil
	}
	return 0, false
}

// RelID returns the id of a to-one relationship, or "".
func (r Resource) RelID(name string) string {
	s, _ := r.Rel[name].(string)
	return s
}

// RelIDs returns the ids of a to-many relationship (or the to-one id as a
// one-element slice).
func (r Resource) RelIDs(name string) []string {
	switch v := r.Rel[name].(type) {
	case []string:
		return v
	case string:
		return []string{v}
	}
	return nil
}

// Document is a parsed JSON:API document.
type Document struct {
	Data       []Resource
	Collection bool
	Included   []Resource
	Meta       map[string]any
	Links      map[string]string
	Errors     []APIError
}

type rawIdentifier struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type rawRelationship struct {
	Data json.RawMessage `json:"data"`
}

type rawResource struct {
	Type          string                     `json:"type"`
	ID            string                     `json:"id"`
	Attributes    map[string]any             `json:"attributes"`
	Relationships map[string]rawRelationship `json:"relationships"`
}

type rawError struct {
	Status string `json:"status"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Source *struct {
		Pointer   string `json:"pointer"`
		Parameter string `json:"parameter"`
	} `json:"source"`
}

type rawDocument struct {
	Data     json.RawMessage `json:"data"`
	Included []rawResource   `json:"included"`
	Meta     map[string]any  `json:"meta"`
	Links    map[string]any  `json:"links"`
	Errors   []rawError      `json:"errors"`
}

// ParseDocument decodes a JSON:API document and flattens its resources.
// An empty body yields an empty document.
func ParseDocument(b []byte) (*Document, error) {
	doc := &Document{}
	if len(bytes.TrimSpace(b)) == 0 {
		return doc, nil
	}
	var raw rawDocument
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse JSON:API document: %w", err)
	}
	doc.Meta = raw.Meta
	if len(raw.Links) > 0 {
		doc.Links = make(map[string]string, len(raw.Links))
		for k, v := range raw.Links {
			if s, ok := v.(string); ok {
				doc.Links[k] = s
			}
		}
	}
	data := bytes.TrimSpace(raw.Data)
	switch {
	case len(data) == 0 || bytes.Equal(data, []byte("null")):
	case data[0] == '[':
		doc.Collection = true
		var items []rawResource
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("parse JSON:API data: %w", err)
		}
		doc.Data = make([]Resource, 0, len(items))
		for _, it := range items {
			doc.Data = append(doc.Data, flatten(it))
		}
	default:
		var it rawResource
		if err := json.Unmarshal(data, &it); err != nil {
			return nil, fmt.Errorf("parse JSON:API data: %w", err)
		}
		doc.Data = []Resource{flatten(it)}
	}
	for _, it := range raw.Included {
		doc.Included = append(doc.Included, flatten(it))
	}
	for _, e := range raw.Errors {
		ae := APIError{Code: e.Code, Title: e.Title, Detail: e.Detail}
		ae.Status, _ = strconv.Atoi(e.Status)
		if e.Source != nil {
			ae.Pointer = e.Source.Pointer
			if ae.Pointer == "" {
				ae.Pointer = e.Source.Parameter
			}
		}
		doc.Errors = append(doc.Errors, ae)
	}
	return doc, nil
}

// Err returns the document's errors as one *APIError (first entry, others in
// More) with the given HTTP status, or nil if there are none.
func (d *Document) Err(status int) *APIError {
	if len(d.Errors) == 0 {
		return nil
	}
	e := d.Errors[0]
	if e.Status == 0 {
		e.Status = status
	}
	if len(d.Errors) > 1 {
		e.More = slices.Clone(d.Errors[1:])
	}
	return &e
}

// Total returns meta.paging.total, or 0 when absent (see [PagingTotal]).
func (d *Document) Total() int { return PagingTotal(d.Meta) }

// Total returns meta.paging.total, or 0 when absent (see [PagingTotal]).
func (r *Response) Total() int { return PagingTotal(r.Meta) }

// PagingTotal reads meta.paging.total as decoded by encoding/json (float64),
// by a json.Decoder with UseNumber (json.Number) or built in Go (int); it
// returns 0 when absent.
func PagingTotal(meta map[string]any) int {
	paging, _ := meta["paging"].(map[string]any)
	switch v := paging["total"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

// IndexIncluded indexes included resources by "type/id".
func IndexIncluded(inc []Resource) map[string]Resource {
	m := make(map[string]Resource, len(inc))
	for _, r := range inc {
		m[r.Type+"/"+r.ID] = r
	}
	return m
}

// RelData is a JSON:API to-one relationship body {"data":{"type","id"}}.
func RelData(typ, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id}}
}

// IsDigits reports whether s is a non-empty run of ASCII digits, the shape
// of App Store Connect numeric ids (apps, users).
func IsDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func flatten(r rawResource) Resource {
	out := Resource{ID: r.ID, Type: r.Type, Attributes: r.Attributes}
	if out.Attributes == nil {
		out.Attributes = map[string]any{}
	}
	for name, rel := range r.Relationships {
		d := bytes.TrimSpace(rel.Data)
		if len(d) == 0 {
			continue // link-only relationship
		}
		if out.Rel == nil {
			out.Rel = map[string]any{}
		}
		switch d[0] {
		case '[':
			var ids []rawIdentifier
			if json.Unmarshal(d, &ids) != nil {
				continue
			}
			list := make([]string, 0, len(ids))
			for _, id := range ids {
				list = append(list, id.ID)
			}
			out.Rel[name] = list
		case 'n':
			out.Rel[name] = nil
		default:
			var id rawIdentifier
			if json.Unmarshal(d, &id) != nil {
				continue
			}
			out.Rel[name] = id.ID
		}
	}
	return out
}
