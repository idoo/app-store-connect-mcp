package spec

// File is the serialized index embedded as index.json.gz. It is produced by
// the generator in ./gen from Apple's OpenAPI document and is not meant to be
// edited by hand.
type File struct {
	// Version is the pinned spec version (info.version).
	Version string `json:"version"`
	// Paths is the number of path templates in the source document.
	Paths int `json:"paths"`
	// Ops lists every operation, sorted by path then method.
	Ops []Op `json:"ops"`
	// Fields maps a resource type to its selectable fields[type] names. The
	// per-operation fields[...] parameters omit their enum to keep the index
	// small; it is restored from here on load.
	Fields map[string][]string `json:"fields"`
	// FreeText maps a resource type to attributes holding human- or
	// third-party-written text, which callers wrap as untrusted.
	FreeText map[string][]string `json:"free_text"`
}

// Op is one operation of the index.
type Op struct {
	ID          string   `json:"id"`
	Method      string   `json:"m"`
	Path        string   `json:"p"`
	Tags        []string `json:"tags,omitempty"`
	Summary     string   `json:"s,omitempty"`
	Deprecated  bool     `json:"dep,omitempty"`
	Params      []Param  `json:"params,omitempty"`
	Body        *Body    `json:"body,omitempty"`
	BodyNeeded  bool     `json:"body_req,omitempty"`
	RespType    string   `json:"resp,omitempty"`
	RespMany    bool     `json:"resp_many,omitempty"`
	ContentType string   `json:"ct,omitempty"`
}

// Param is a path or query parameter.
type Param struct {
	Name     string   `json:"n"`
	In       string   `json:"in"`
	Type     string   `json:"t,omitempty"`
	Array    bool     `json:"a,omitempty"`
	Required bool     `json:"r,omitempty"`
	Enum     []string `json:"e,omitempty"`
}

// Body describes a JSON:API request document.
type Body struct {
	// Type is the required data.type value.
	Type string `json:"type"`
	// Linkage is true for relationship endpoints whose data is resource
	// identifiers ({type,id}) rather than a resource object.
	Linkage bool `json:"link,omitempty"`
	// Many is true when data is an array (to-many linkage).
	Many bool `json:"many,omitempty"`
	// ID is true when data.id is required (updates).
	ID    bool            `json:"id,omitempty"`
	Attrs map[string]Attr `json:"attrs,omitempty"`
	Rels  map[string]Rel  `json:"rels,omitempty"`
}

// Attr is one data.attributes member.
type Attr struct {
	Type     string   `json:"t,omitempty"`
	Format   string   `json:"f,omitempty"`
	Required bool     `json:"r,omitempty"`
	Enum     []string `json:"e,omitempty"`
}

// Rel is one data.relationships member.
type Rel struct {
	Type     string `json:"t"`
	Many     bool   `json:"many,omitempty"`
	Required bool   `json:"r,omitempty"`
}
