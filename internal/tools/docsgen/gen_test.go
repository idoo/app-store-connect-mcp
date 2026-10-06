package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedDocsAreFresh(t *testing.T) {
	j, md, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"tools.json": j, "TOOLS.md": md} {
		got, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", name))
		if err != nil {
			t.Fatalf("%s: %v (run go run ./internal/tools/docsgen)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("docs/%s is stale; run go run ./internal/tools/docsgen", name)
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	j1, m1, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	j2, m2, err := Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(j1, j2) || !bytes.Equal(m1, m2) {
		t.Fatal("docsgen output differs between runs")
	}
}
