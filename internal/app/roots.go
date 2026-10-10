package app

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// ProjectFiles is the session's [tool.Files]. Its root is, in order: the
// explicit root (--root or ASC_MCP_ROOT), the MCP client's first file:// root,
// or the git root of the cwd. A root picked automatically that is the
// filesystem root or the home directory is refused, because desktop clients
// start servers there and the whole disk would be readable.
type ProjectFiles struct {
	explicit *Files
	def      *Files
	defErr   error
	home     string

	mu    sync.Mutex
	roots map[string]*Files
}

var _ tool.RootedFiles = (*ProjectFiles)(nil)

// NewProjectFiles builds the session files. explicit is --root/ASC_MCP_ROOT
// ("" for none) and must be an existing directory. Without it the default root
// is the git root of cwd; an unsafe default is not an error here, file tools
// report it when used.
func NewProjectFiles(explicit, cwd, home string) (*ProjectFiles, error) {
	pf := &ProjectFiles{home: home, roots: map[string]*Files{}}
	if explicit != "" {
		f, err := NewFiles(explicit)
		if err != nil {
			return nil, fmt.Errorf("app: --root: %w", err)
		}
		pf.explicit = f
		return pf, nil
	}
	pf.def, pf.defErr = pf.open(DefaultRoot(cwd))
	return pf, nil
}

// open confines to dir unless it is / or the home directory.
func (pf *ProjectFiles) open(dir string) (*Files, error) {
	if unsafeRoot(dir, pf.home) {
		return nil, fmt.Errorf("%w: file access is off because the project folder resolved to %s (the disk root, a top-level folder, or your home folder or one containing it); start app-store-connect-mcp with --root <project folder> or set ASC_MCP_ROOT",
			tool.ErrUnavailable, dir)
	}
	f, err := NewFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", tool.ErrUnavailable, err)
	}
	return f, nil
}

// ForRoots implements [tool.RootedFiles]: the explicit root wins; otherwise
// the first client root that is an existing directory is used (refused when
// it is / or home); otherwise the default root.
func (pf *ProjectFiles) ForRoots(uris []string) (tool.Files, error) {
	if pf.explicit != nil {
		return pf.explicit, nil
	}
	for _, u := range uris {
		dir, ok := rootPath(u)
		if !ok {
			continue
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		return pf.cached(dir)
	}
	if pf.defErr != nil {
		return nil, pf.defErr
	}
	return pf.def, nil
}

func (pf *ProjectFiles) cached(dir string) (*Files, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if f, ok := pf.roots[dir]; ok {
		return f, nil
	}
	f, err := pf.open(dir)
	if err != nil {
		return nil, err
	}
	pf.roots[dir] = f
	return f, nil
}

// Root returns the root used without client roots, or "" when refused.
func (pf *ProjectFiles) Root() string {
	f, err := pf.ForRoots(nil)
	if err != nil {
		return ""
	}
	return f.(*Files).Root()
}

// Close releases every root opened by the session.
func (pf *ProjectFiles) Close() error {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	var errs []error
	for _, f := range []*Files{pf.explicit, pf.def} {
		if f != nil {
			errs = append(errs, f.Close())
		}
	}
	for dir, f := range pf.roots {
		errs = append(errs, f.Close())
		delete(pf.roots, dir)
	}
	return errors.Join(errs...)
}

// rootPath converts a file:// root URI to a local path.
func rootPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" || (u.Host != "" && u.Host != "localhost") {
		return "", false
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/") // file:///C:/x → C:/x
	}
	return filepath.Clean(filepath.FromSlash(p)), true
}

// unsafeRoot reports whether dir is the filesystem root, a top-level folder
// (fewer than 2 path components, e.g. /Users, before or after resolving
// symlinks: macOS /home is an automount link), the home directory or any
// ancestor of it (symlinks resolved).
func unsafeRoot(dir, home string) bool {
	d := canonical(dir)
	lexical := dir
	if abs, err := filepath.Abs(dir); err == nil {
		lexical = abs
	}
	if filepath.Dir(d) == d || pathDepth(d) < 2 || pathDepth(filepath.Clean(lexical)) < 2 {
		return true
	}
	return home != "" && within(d, canonical(home))
}

// pathDepth counts the components of a clean absolute path below its volume.
func pathDepth(p string) int {
	rest := strings.Trim(filepath.ToSlash(strings.TrimPrefix(p, filepath.VolumeName(p))), "/")
	if rest == "" {
		return 0
	}
	return strings.Count(rest, "/") + 1
}

func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

func (pf *ProjectFiles) base() (tool.Files, error) { return pf.ForRoots(nil) }

// Resolve implements tool.Files over the default root.
func (pf *ProjectFiles) Resolve(p string) (string, error) {
	f, err := pf.base()
	if err != nil {
		return "", err
	}
	return f.Resolve(p)
}

// ReadFile implements tool.Files over the default root.
func (pf *ProjectFiles) ReadFile(p string) ([]byte, error) {
	f, err := pf.base()
	if err != nil {
		return nil, err
	}
	return f.ReadFile(p)
}

// WriteFile implements tool.Files over the default root.
func (pf *ProjectFiles) WriteFile(p string, data []byte, perm fs.FileMode) error {
	f, err := pf.base()
	if err != nil {
		return err
	}
	return f.WriteFile(p, data, perm)
}

// Open implements tool.Files over the default root.
func (pf *ProjectFiles) Open(p string) (*os.File, error) {
	f, err := pf.base()
	if err != nil {
		return nil, err
	}
	return f.Open(p)
}

// Stat implements tool.Files over the default root.
func (pf *ProjectFiles) Stat(p string) (fs.FileInfo, error) {
	f, err := pf.base()
	if err != nil {
		return nil, err
	}
	return f.Stat(p)
}

// WalkDir implements tool.Files over the default root.
func (pf *ProjectFiles) WalkDir(root string, fn fs.WalkDirFunc) error {
	f, err := pf.base()
	if err != nil {
		return err
	}
	return f.WalkDir(root, fn)
}

// Rename implements tool.Files over the default root.
func (pf *ProjectFiles) Rename(oldPath, newPath string) error {
	f, err := pf.base()
	if err != nil {
		return err
	}
	return f.Rename(oldPath, newPath)
}
