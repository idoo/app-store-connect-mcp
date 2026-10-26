package all_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// TestFreeTextReadThroughText fails on any X.Str("<k>") with k in
// tool.FreeTextKeys under internal/tools: third-party text must be read
// with Resource.Text so it stays marked untrusted. A use that is truly
// trusted (a comparison, an id-like value) carries "// trusted: <reason>"
// on the same line.
func TestFreeTextReadThroughText(t *testing.T) {
	root := filepath.Join("..")
	fset := token.NewFileSet()
	var bad []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		trusted := map[int]bool{}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.HasPrefix(c.Text, "// trusted:") {
					trusted[fset.Position(c.Pos()).Line] = true
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Str" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key, err := strconv.Unquote(lit.Value)
			if err != nil || !slices.Contains(tool.FreeTextKeys, key) {
				return true
			}
			pos := fset.Position(call.Pos())
			if !trusted[pos.Line] {
				rel, _ := filepath.Rel(root, pos.Filename)
				bad = append(bad, "internal/tools/"+filepath.ToSlash(rel)+":"+strconv.Itoa(pos.Line)+" "+key)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Errorf("free-text keys read with Str (use Resource.Text, or add `// trusted: <reason>` on the line):\n%s", strings.Join(bad, "\n"))
	}
}
