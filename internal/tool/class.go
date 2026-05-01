package tool

// Class is the safety class of a tool. It is declared explicitly per tool and
// drives MCP annotations, read-only withholding and the confirm requirement.
type Class int

const (
	// Read tools never mutate Apple-side state.
	Read Class = iota + 1
	// Write tools create or update state without deleting or publishing.
	Write
	// Destructive tools delete, expire, revoke, or make something public.
	Destructive
)

// String returns the one-letter class code used in docs and snapshots.
func (c Class) String() string {
	switch c {
	case Read:
		return "R"
	case Write:
		return "W"
	case Destructive:
		return "D"
	default:
		return "?"
	}
}

// Valid reports whether c is one of the declared classes.
func (c Class) Valid() bool { return c >= Read && c <= Destructive }

// MarshalText encodes the class as its one-letter code.
func (c Class) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

// Annotations mirrors MCP ToolAnnotations (field names and JSON match), so
// only internal/mcpserver depends on the SDK.
type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// Annotations derives the MCP tool annotations from the tool's metadata.
// readOnlyHint follows the class, destructiveHint is explicit for non-read
// tools, idempotentHint comes from Meta.Idempotent and openWorldHint is true
// unless the tool is Local (never calls Apple or the network).
func (m Meta) Annotations() Annotations {
	a := Annotations{
		Title:          m.Title,
		ReadOnlyHint:   m.Class == Read,
		IdempotentHint: m.Idempotent,
		OpenWorldHint:  boolPtr(!m.Local),
	}
	if m.Class != Read {
		a.DestructiveHint = boolPtr(m.Class == Destructive)
	}
	return a
}

// boolPtr is kept until the go directive reaches 1.26 (new(expr)).
func boolPtr(b bool) *bool { return &b }
