package mcpserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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

const specOpPrefix = "app-store-connect-mcp://spec/operations/"

func (s *session) addResources() {
	staticDoc := func(uri, name, title, text string) {
		s.srv.AddResource(&mcp.Resource{URI: uri, Name: name, Title: title, MIMEType: "text/markdown"},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/markdown", Text: text}}}, nil
			})
	}
	staticDoc("app-store-connect-mcp://docs/limits", "limits", "App Store Connect limits", limitsDoc)
	staticDoc("app-store-connect-mcp://docs/rejections", "rejections", "Common App Review rejections", rejectionsDoc)

	s.srv.AddResource(&mcp.Resource{URI: "app-store-connect-mcp://apps", Name: "apps", Title: "Apps", MIMEType: "application/json"}, s.readApps)
	s.srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: specOpPrefix + "{operationId}", Name: "spec-operation",
		Title: "App Store Connect API operation", MIMEType: "application/json",
	}, s.readSpecOp)
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
		return nil, resourceNotFound(uri)
	}
	d, err := s.deps.Env.Spec.Describe(id)
	if err != nil {
		return nil, resourceNotFound(uri)
	}
	return jsonContents(uri, d)
}

// resourceNotFound is the -32002 error that protocol versions up to
// 2025-11-25 (our session cap) specify; the SDK's ResourceNotFoundError uses
// -32602 for newer versions. Revisit when the protocol cap lifts.
func resourceNotFound(uri string) error {
	data, _ := json.Marshal(map[string]string{"uri": uri})
	return &jsonrpc.Error{Code: -32002, Message: "Resource not found", Data: data}
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
