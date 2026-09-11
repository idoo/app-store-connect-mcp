// Package skills embeds hamn's Agent Skills (skills/<name>/SKILL.md) so
// `hamn skills install` can copy them into an agent's user skills directory.
package skills

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed */SKILL.md
var files embed.FS

// FileName is the name of the skill file inside each skill directory.
const FileName = "SKILL.md"

// Skill is one embedded skill.
type Skill struct {
	Name        string // frontmatter name, equal to the directory name
	Description string // frontmatter description
	Content     []byte // the whole SKILL.md
	Body        string // the markdown after the frontmatter
}

// All returns every embedded skill sorted by name.
func All() ([]Skill, error) {
	dirs, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	out := make([]Skill, 0, len(dirs))
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		data, err := files.ReadFile(path.Join(d.Name(), FileName))
		if err != nil {
			return nil, err
		}
		s, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name(), err)
		}
		if s.Name != d.Name() {
			return nil, fmt.Errorf("%s: frontmatter name %q does not match the directory", d.Name(), s.Name)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Parse reads a SKILL.md: a "---" delimited frontmatter of single-line
// "key: value" pairs (name and description are required) followed by the body.
func Parse(data []byte) (Skill, error) {
	text := string(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Skill{}, errors.New("missing frontmatter")
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return Skill{}, errors.New("unterminated frontmatter")
	}
	s := Skill{Content: data, Body: body}
	for _, line := range strings.Split(front, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return Skill{}, fmt.Errorf("bad frontmatter line %q", line)
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "name":
			s.Name = val
		case "description":
			s.Description = val
		}
	}
	if s.Name == "" || s.Description == "" {
		return Skill{}, errors.New("frontmatter needs name and description")
	}
	return s, nil
}
