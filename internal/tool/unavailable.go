package tool

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Stand-ins installed by [Env.complete] for nil members.
func unavail(what string) error { return fmt.Errorf("%s: %w", what, ErrUnavailable) }

type (
	noASC       struct{}
	noSpec      struct{}
	noResolver  struct{}
	noPlanner   struct{}
	noStore     struct{}
	noToolsets  struct{}
	noFetcher   struct{}
	noFiles     struct{}
	noArtifacts struct{}
)

func (noASC) Do(context.Context, Request) (*Response, error) {
	return nil, unavail("App Store Connect client")
}

func (noASC) Get(context.Context, string, url.Values) (*Response, error) {
	return nil, unavail("App Store Connect client")
}

func (noASC) List(context.Context, string, ListOptions) (*ListResult, error) {
	return nil, unavail("App Store Connect client")
}

func (noASC) Download(context.Context, Request) ([]byte, http.Header, error) {
	return nil, nil, unavail("App Store Connect client")
}
func (noASC) RateLimit() RateLimit             { return RateLimit{} }
func (noASC) ClockSkew() (time.Duration, bool) { return 0, false }

func (noSpec) Version() string                        { return "" }
func (noSpec) Search(string, int) []SearchHit         { return nil }
func (noSpec) Match(string, string) (Operation, bool) { return Operation{}, false }
func (noSpec) Validate(Request) error                 { return unavail("spec index") }
func (noSpec) Sensitive(string, string) bool          { return true }
func (noSpec) Describe(string) (*OpDescription, error) {
	return nil, unavail("spec index")
}

func (noResolver) App(context.Context, AppRef) (App, error) { return App{}, unavail("resolver") }
func (noResolver) Version(context.Context, string, Platform, VersionRef) (Version, error) {
	return Version{}, unavail("resolver")
}

func (noResolver) Build(context.Context, string, Platform, BuildRef) (Build, error) {
	return Build{}, unavail("resolver")
}

func (noResolver) Locales(context.Context, string, LocaleSet) ([]string, error) {
	return nil, unavail("resolver")
}

func (noResolver) Group(context.Context, string, GroupRef) (Group, error) {
	return Group{}, unavail("resolver")
}

func (noPlanner) Propose(context.Context, string, any, string, PlanPreview) (string, error) {
	return "", unavail("plan store")
}

func (noPlanner) Verify(context.Context, string, string, any, string) error {
	return unavail("plan store")
}

func (noPlanner) Claim(context.Context, string, any) error { return ErrPreviewRequired }

func (noStore) Cursor(string, string, string) (string, error)  { return "", unavail("state store") }
func (noStore) SetCursor(string, string, string, string) error { return unavail("state store") }
func (noStore) Load(string, string, any) (bool, error)         { return false, unavail("state store") }
func (noStore) Save(string, string, any) error                 { return unavail("state store") }
func (noStore) Delete(string, string) error                    { return unavail("state store") }

func (noToolsets) List() []ToolsetInfo                       { return nil }
func (noToolsets) Enable(context.Context, ...Toolset) error  { return unavail("toolsets") }
func (noToolsets) Disable(context.Context, ...Toolset) error { return unavail("toolsets") }

func (noFetcher) Fetch(context.Context, string, string, http.Header, io.Reader, int64) (*FetchResponse, error) {
	return nil, unavail("fetcher")
}

func (noFiles) Resolve(string) (string, error)              { return "", unavail("file access") }
func (noFiles) ReadFile(string) ([]byte, error)             { return nil, unavail("file access") }
func (noFiles) WriteFile(string, []byte, fs.FileMode) error { return unavail("file access") }
func (noFiles) Open(string) (*os.File, error)               { return nil, unavail("file access") }
func (noFiles) Stat(string) (fs.FileInfo, error)            { return nil, unavail("file access") }
func (noFiles) WalkDir(string, fs.WalkDirFunc) error        { return unavail("file access") }
func (noFiles) Rename(string, string) error                 { return unavail("file access") }

func (noArtifacts) Put(context.Context, string, string, []byte) (Link, error) {
	return Link{}, unavail("artifact store")
}
