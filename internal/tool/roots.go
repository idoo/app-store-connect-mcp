package tool

import (
	"context"
	"io/fs"
	"os"
	"sync"
)

// RootedFiles is a [Files] that can confine itself to the MCP client's roots.
// ForRoots receives the client's root URIs (possibly empty) and returns the
// Files to use for one call; it fails when no safe root is available.
type RootedFiles interface {
	Files
	ForRoots(roots []string) (Files, error)
}

// callFiles resolves a [RootedFiles] against the client's roots on the first
// file access of one tool call, so calls that never touch files never ask the
// client. Errors (including a refused root) are returned by every method.
type callFiles struct {
	ctx   context.Context //nolint:containedctx // bound to one tool call
	base  RootedFiles
	roots func(context.Context) ([]string, error)

	once  sync.Once
	files Files
	err   error
}

func withRoots(ctx context.Context, f Files, roots func(context.Context) ([]string, error)) Files {
	rf, ok := f.(RootedFiles)
	if !ok || roots == nil {
		return f
	}
	return &callFiles{ctx: ctx, base: rf, roots: roots}
}

func (c *callFiles) get() (Files, error) {
	c.once.Do(func() {
		uris, err := c.roots(c.ctx)
		if err != nil {
			// A client that advertised roots but cannot list them falls back
			// to the default root, which ForRoots still checks for safety.
			uris = nil
		}
		c.files, c.err = c.base.ForRoots(uris)
	})
	return c.files, c.err
}

func (c *callFiles) Resolve(p string) (string, error) {
	f, err := c.get()
	if err != nil {
		return "", err
	}
	return f.Resolve(p)
}

func (c *callFiles) ReadFile(p string) ([]byte, error) {
	f, err := c.get()
	if err != nil {
		return nil, err
	}
	return f.ReadFile(p)
}

func (c *callFiles) WriteFile(p string, data []byte, perm fs.FileMode) error {
	f, err := c.get()
	if err != nil {
		return err
	}
	return f.WriteFile(p, data, perm)
}

func (c *callFiles) Open(p string) (*os.File, error) {
	f, err := c.get()
	if err != nil {
		return nil, err
	}
	return f.Open(p)
}

func (c *callFiles) Stat(p string) (fs.FileInfo, error) {
	f, err := c.get()
	if err != nil {
		return nil, err
	}
	return f.Stat(p)
}

func (c *callFiles) WalkDir(root string, fn fs.WalkDirFunc) error {
	f, err := c.get()
	if err != nil {
		return err
	}
	return f.WalkDir(root, fn)
}
