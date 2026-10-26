package mcpserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

var (
	//go:embed docs/limits.md
	limitsDoc string
	//go:embed docs/rejections.md
	rejectionsDoc string
)

// staticDocURIs are the embedded, never-changing documentation resources.
var staticDocURIs = map[string]bool{
	limitsURI:     true,
	rejectionsURI: true,
}

const (
	limitsURI     = "app-store-connect-mcp://docs/limits"
	rejectionsURI = "app-store-connect-mcp://docs/rejections"
)

const specOpPrefix = "app-store-connect-mcp://spec/operations/"

func (s *session) addResources() {
	staticDoc := func(uri, name, title, text string) {
		s.srv.AddResource(&mcp.Resource{URI: uri, Name: name, Title: title, MIMEType: "text/markdown"},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/markdown", Text: text}}}, nil
			})
	}
	staticDoc(limitsURI, "limits", "App Store Connect limits", limitsDoc)
	staticDoc(rejectionsURI, "rejections", "Common App Review rejections", rejectionsDoc)

	s.srv.AddResource(&mcp.Resource{URI: "app-store-connect-mcp://apps", Name: "apps", Title: "Apps", MIMEType: "application/json"}, s.readApps)
	s.srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: specOpPrefix + "{operationId}", Name: "spec-operation",
		Title: "App Store Connect API operation", MIMEType: "application/json",
	}, s.readSpecOp)
	s.srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: artifactPrefix + "{id}", Name: "artifact",
		Title: "Tool output artifact (this session)",
	}, s.readArtifact)
}

func (s *session) readApps(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.deps.Env.ASC == nil {
		return nil, fmt.Errorf("app-store-connect-mcp://apps: %w", tool.ErrUnavailable)
	}
	res, err := s.deps.Env.ASC.List(ctx, "/v1/apps", tool.ListOptions{
		Fields:   map[string][]string{"apps": {"name", "bundleId", "sku", "primaryLocale"}},
		MaxItems: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("app-store-connect-mcp://apps: %w", err)
	}
	return jsonContents(req.Params.URI, res)
}

func (s *session) readSpecOp(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	id := strings.TrimPrefix(uri, specOpPrefix)
	if id == uri || id == "" || s.deps.Env.Spec == nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	d, err := s.deps.Env.Spec.Describe(id)
	if err != nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	return jsonContents(uri, d)
}

func jsonContents(uri string, v any) (*mcp.ReadResourceResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%s: encode: %w", uri, err)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: uri, MIMEType: "application/json", Text: string(asc.RedactBytes(b)),
	}}}, nil
}

// Cache TTLs advertised to clients (2026-07-28 ttlMs). The tool list is kept
// short because toolsets_enable and enable_writes change it at runtime.
const (
	toolsListTTLMs = 5_000
	listTTLMs      = 60_000
	staticDocTTLMs = 300_000
)

// setCacheable marks every cacheable result private (results depend on the
// profile) with a short TTL. Reads other than the embedded docs stay
// immediately stale. It must not call back into the server: the SDK holds
// the server lock while it runs for list methods.
func setCacheable(_ context.Context, req mcp.Request, c *mcp.Cacheable) {
	c.CacheScope = "private"
	switch r := req.(type) {
	case *mcp.ListToolsRequest:
		c.TTLMs = toolsListTTLMs
	case *mcp.ListResourcesRequest, *mcp.ListResourceTemplatesRequest, *mcp.ListPromptsRequest:
		c.TTLMs = listTTLMs
	case *mcp.ReadResourceRequest:
		if r.Params != nil && staticDocURIs[r.Params.URI] {
			c.TTLMs = staticDocTTLMs
		}
	}
}
