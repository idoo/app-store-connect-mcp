// Command gen builds internal/spec/index.json.gz and PROVENANCE.json from
// Apple's App Store Connect OpenAPI document.
//
// Usage:
//
//	go run ./internal/spec/gen -in /path/to/openapi.oas.json
//
// It fails when any non-GET operation is not classified by spec.Classify.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/spec"
)

const defaultSourceURL = "https://developer.apple.com/sample-code/app-store-connect/app-store-connect-openapi-specification.zip"

// Provenance records where the embedded index came from.
type Provenance struct {
	SpecVersion string `json:"spec_version"`
	SpecSHA256  string `json:"spec_sha256"`
	ZipSHA256   string `json:"zip_sha256,omitempty"`
	SourceURL   string `json:"source_url"`
	Generated   string `json:"generated"`
	Paths       int    `json:"paths"`
	Operations  int    `json:"operations"`
}

func main() {
	in := flag.String("in", os.Getenv("ASC_MCP_SPEC"), "path to Apple's openapi.oas.json (default $ASC_MCP_SPEC)")
	out := flag.String("out", filepath.Join("internal", "spec"), "output directory")
	zipSHA := flag.String("zip-sha256", "", "sha256 of the downloaded spec zip (kept from PROVENANCE.json when the spec is unchanged)")
	source := flag.String("source-url", defaultSourceURL, "spec download URL")
	flag.Parse()
	log.SetFlags(0)
	if err := run(*in, *out, *zipSHA, *source); err != nil {
		log.Fatalf("gen: %v", err)
	}
}

func run(in, out, zipSHA, source string) error {
	if in == "" {
		return errors.New("-in or ASC_MCP_SPEC is required")
	}
	raw, err := os.ReadFile(filepath.Clean(in)) //nolint:gosec // dev tool: operator-supplied spec path
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	f, err := Build(raw)
	if err != nil {
		return err
	}
	if bad := unclassified(f); len(bad) > 0 {
		return fmt.Errorf("%d non-GET operations are not classified in internal/spec/sensitive.go:\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
	gz, err := encode(f)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "index.json.gz"), gz, 0o644); err != nil { //nolint:gosec // committed, embedded artifact
		return fmt.Errorf("write index: %w", err)
	}
	sum := sha256.Sum256(raw)
	prov := Provenance{
		SpecVersion: f.Version,
		SpecSHA256:  hex.EncodeToString(sum[:]),
		ZipSHA256:   zipSHA,
		SourceURL:   source,
		Generated:   time.Now().UTC().Format(time.DateOnly),
		Paths:       f.Paths,
		Operations:  len(f.Ops),
	}
	provPath := filepath.Join(out, "PROVENANCE.json")
	keepStable(&prov, provPath)
	pj, err := json.MarshalIndent(prov, "", "  ")
	if err != nil {
		return fmt.Errorf("encode provenance: %w", err)
	}
	if err := os.WriteFile(provPath, append(pj, '\n'), 0o644); err != nil { //nolint:gosec // committed artifact
		return fmt.Errorf("write provenance: %w", err)
	}
	log.Printf("spec %s: paths=%d ops=%d index=%d bytes", f.Version, f.Paths, len(f.Ops), len(gz))
	return nil
}

// keepStable preserves the generated date (and zip hash, when not given)
// for an unchanged spec, so regeneration is byte-for-byte reproducible.
func keepStable(p *Provenance, path string) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return
	}
	var old Provenance
	if json.Unmarshal(b, &old) != nil || old.SpecSHA256 != p.SpecSHA256 {
		return
	}
	p.Generated = old.Generated
	if p.ZipSHA256 == "" {
		p.ZipSHA256 = old.ZipSHA256
	}
}

func unclassified(f *spec.File) []string {
	var bad []string
	for _, op := range f.Ops {
		if _, ok := spec.Classify(op.Method, op.Path); !ok {
			bad = append(bad, op.Method+" "+op.Path+" ("+op.ID+")")
		}
	}
	return bad
}

func encode(f *spec.File) ([]byte, error) {
	j, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("encode index: %w", err)
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	if _, err := io.Copy(zw, bytes.NewReader(j)); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	return buf.Bytes(), nil
}
