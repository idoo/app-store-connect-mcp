package tool

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"time"
)

// KeyKind distinguishes Team API keys (issuer id) from Individual API keys.
type KeyKind string

// Key kinds.
const (
	KeyTeam       KeyKind = "team"
	KeyIndividual KeyKind = "individual"
)

// ProfileInfo describes the active credentials profile (never key material).
type ProfileInfo struct {
	Name         string  `json:"name"`
	KeyKind      KeyKind `json:"key_kind"`
	VendorNumber string  `json:"vendor_number,omitempty"`
	// DefaultApp is an app id or bundle id from the profile or project pin.
	DefaultApp  string `json:"default_app,omitempty"`
	AllowWrites bool   `json:"allow_writes"`
	// PinSource is the path of the .app-store-connect-mcp.toml pin that selected the profile, if any.
	PinSource string `json:"pin_source,omitempty"`
	// AllowNoElicit lets RequireHuman tools proceed on an agent confirmation
	// when the client cannot elicit (config opt-in).
	AllowNoElicit bool `json:"allow_no_elicit,omitempty"`
}

// PlanTTL is how long a proposed plan stays valid. Planner implementations
// must expire plans at exactly this age (see tooltest.PlannerConformance).
const PlanTTL = 15 * time.Minute

// Planner stores two-step plans (in memory, per session).
type Planner interface {
	// Propose records a plan for tool+args at the given state fingerprint and
	// returns its id. args are canonicalized with [CanonicalArgs].
	Propose(ctx context.Context, toolName string, args any, fingerprint string, preview PlanPreview) (string, error)
	// Verify checks that planID was proposed for the same tool, args and
	// fingerprint and has not expired; it consumes the plan on success. Failures
	// wrap [ErrStalePlan].
	Verify(ctx context.Context, planID, toolName string, args any, fingerprint string) error
	// Claim consumes the newest unexpired plan for toolName proposed at
	// [FlagFingerprint] whose canonical args equal args'. It is how a
	// [ConfirmByFlag] confirm proves a preview of the same call came first.
	// Failures wrap [ErrPreviewRequired].
	Claim(ctx context.Context, toolName string, args any) error
}

// StateStore is persistent local state (XDG state dir).
type StateStore interface {
	// Cursor returns the stored "since" cursor for profile/app/name, or "".
	Cursor(profile, app, name string) (string, error)
	// SetCursor stores a cursor value.
	SetCursor(profile, app, name, value string) error
	// Load decodes the JSON record kind/key into v, reporting whether it existed.
	Load(kind, key string, v any) (bool, error)
	// Save stores v as the JSON record kind/key.
	Save(kind, key string, v any) error
	// Delete removes a record; missing records are not an error.
	Delete(kind, key string) error
}

// ToolsetInfo describes one toolset for the toolsets tool.
type ToolsetInfo struct {
	Name    Toolset `json:"name"`
	Enabled bool    `json:"enabled"`
	Default bool    `json:"default"`
	// Tools counts tools visible in the current mode; Withheld counts tools
	// hidden by read-only mode or the key kind.
	Tools    int `json:"tools"`
	Withheld int `json:"withheld,omitempty"`
}

// Toolsets controls the session's enabled toolsets (sends tools/list_changed).
type Toolsets interface {
	List() []ToolsetInfo
	Enable(ctx context.Context, names ...Toolset) error
	Disable(ctx context.Context, names ...Toolset) error
}

// FetchResponse is the result of an unauthenticated fetch.
type FetchResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// Fetcher performs unauthenticated requests (report downloads, analytics
// segments, upload parts, feedback screenshots) under the URL policy: https,
// port 443, no userinfo, no private/loopback/link-local address after
// resolution, and no Authorization/Cookie/Host/Proxy-* headers.
type Fetcher interface {
	// Fetch performs method on rawURL; the response body is capped at maxBytes
	// (0 means the implementation default) and exceeding it is an error.
	Fetch(ctx context.Context, method, rawURL string, header http.Header, body io.Reader, maxBytes int64) (*FetchResponse, error)
}

// Files gives tools confined filesystem access: an explicit --root/ASC_MCP_ROOT,
// else the MCP client's roots, else the git root of the cwd. A fallback root
// of / or the home directory is refused. Symlink escapes are refused.
// All methods accept paths relative to the root or absolute paths inside it.
type Files interface {
	// Resolve returns the confined absolute path or an error.
	Resolve(path string) (string, error)
	ReadFile(path string) ([]byte, error)
	// WriteFile writes data, creating parent directories.
	WriteFile(path string, data []byte, perm fs.FileMode) error
	Open(path string) (*os.File, error)
	Stat(path string) (fs.FileInfo, error)
	// WalkDir walks root like [fs.WalkDir]; callback paths are absolute.
	WalkDir(root string, fn fs.WalkDirFunc) error
}

// Clock abstracts time so wait loops are testable.
type Clock interface {
	Now() time.Time
	// Sleep waits d or until ctx is done (returning ctx.Err()).
	Sleep(ctx context.Context, d time.Duration) error
}

// SystemClock is the real-time [Clock].
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// Sleep waits d or until ctx is done.
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Env is everything a tool handler may use. The server builds one per
// session; tests use tooltest.New. Inside a handler every member is non-nil:
// [Tool.Call] substitutes stand-ins that fail with [ErrUnavailable] (and a
// no-op Progress, an Elicit returning [ErrElicitationUnavailable], the
// system clock).
type Env struct {
	ASC      ASC
	Spec     Spec
	Resolve  Resolver
	Plans    Planner
	State    StateStore
	Toolsets Toolsets
	Fetch    Fetcher
	Files    Files
	Clock    Clock
	Profile  ProfileInfo
	// Version is the app-store-connect-mcp build version.
	Version string
	// NoConfirm skips the preview step (CI only: --no-confirm with ASC_MCP_CI=1).
	// RequireHuman tools still require approval.
	NoConfirm bool
	// SetWrites flips the session's write capability below the tool layer
	// (the ASC client's read-only guard). The server calls it when toolsets
	// enable_writes succeeds; nil is a no-op.
	SetWrites func(bool)
	// Roots lists the MCP client's roots (file:// URIs). nil means the client
	// declared no roots capability. [Tool.Call] passes it to a [RootedFiles]
	// Files, which asks the client only when a tool first touches a file.
	Roots func(ctx context.Context) ([]string, error)
	// Progress reports progress to the client.
	Progress func(done, total float64, msg string)
	// Elicit asks the human a yes/no question. It returns false when the human
	// declines and an error wrapping [ErrElicitationUnavailable] when the
	// client cannot ask.
	Elicit func(ctx context.Context, msg string) (bool, error)

	// noProgress records that Progress was nil before [Env.complete].
	noProgress bool
}

// HasProgress reports whether the client asked for progress notifications
// (sent a progressToken). Long waits clamp to [NoProgressWaitCap] without it,
// since such clients typically cancel a request after about 60 s.
func (e *Env) HasProgress() bool { return e.Progress != nil && !e.noProgress }

// NoProgressWaitCap bounds a tool's wait when the client sent no progress token.
const NoProgressWaitCap = 50 * time.Second

// complete returns a copy of e with every nil member replaced by a stand-in.
func (e *Env) complete() *Env {
	c := *e
	if c.ASC == nil {
		c.ASC = noASC{}
	}
	if c.Spec == nil {
		c.Spec = noSpec{}
	}
	if c.Resolve == nil {
		c.Resolve = noResolver{}
	}
	if c.Plans == nil {
		c.Plans = noPlanner{}
	}
	if c.State == nil {
		c.State = noStore{}
	}
	if c.Toolsets == nil {
		c.Toolsets = noToolsets{}
	}
	if c.Fetch == nil {
		c.Fetch = noFetcher{}
	}
	if c.Files == nil {
		c.Files = noFiles{}
	}
	if c.Clock == nil {
		c.Clock = SystemClock{}
	}
	if c.SetWrites == nil {
		c.SetWrites = func(bool) {}
	}
	if c.Progress == nil {
		c.noProgress = true
		c.Progress = func(float64, float64, string) {}
	}
	if c.Elicit == nil {
		c.Elicit = func(context.Context, string) (bool, error) {
			return false, ErrElicitationUnavailable
		}
	}
	return &c
}
