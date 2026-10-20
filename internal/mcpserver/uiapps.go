package mcpserver

import (
	"context"
	"embed"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP Apps (spec 2026-01-26): interactive HTML views for tools whose
// tool.Meta.UI names a screen. Offered only to clients that advertise the
// io.modelcontextprotocol/ui extension with the mcp-app mime type; every
// other client sees tools/list and resources/list exactly as before.
const (
	uiExtension = "io.modelcontextprotocol/ui"
	uiMIMEType  = "text/html;profile=mcp-app"
	uiURIPrefix = "ui://app-store-connect-mcp/"
	// uiProtocolVersion is the MCP Apps protocol version the views send in
	// ui/initialize (ext-apps LATEST_PROTOCOL_VERSION, checked 2026-10-26);
	// TestUIProtocolVersion keeps ui/*.html in step with it.
	uiProtocolVersion = "2026-01-26"
)

// uiScreens are the embedded views, one file per screen in ui/<name>.html.
var uiScreens = []string{"release", "metadata", "screenshots", "reviews", "funnel"}

//go:embed ui/*.html
var uiFS embed.FS

func uiResourceURI(screen string) string { return uiURIPrefix + screen }

// uiResourceMeta declares a CSP with no external origins: views never fetch,
// load or frame anything outside the sandboxed document.
func uiResourceMeta() mcp.Meta {
	return mcp.Meta{"ui": map[string]any{
		"csp":           map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}},
		"prefersBorder": true,
	}}
}

// clientSupportsUI reports whether the client's initialize request advertised
// the MCP Apps extension with the mcp-app HTML mime type. go-sdk v1.8 exposes
// the extensions map as ClientCapabilities.Extensions (raw JSON decoded to
// map[string]any).
func clientSupportsUI(p *mcp.InitializeParams) bool {
	if p == nil || p.Capabilities == nil {
		return false
	}
	ext, ok := p.Capabilities.Extensions[uiExtension].(map[string]any)
	if !ok {
		return false
	}
	types, _ := ext["mimeTypes"].([]any)
	return slices.ContainsFunc(types, func(v any) bool { s, _ := v.(string); return s == uiMIMEType })
}

func requestSupportsUI(req mcp.Request) bool {
	ss, ok := req.GetSession().(*mcp.ServerSession)
	return ok && ss != nil && clientSupportsUI(ss.InitializeParams())
}

// addUIResources registers one resource per screen. resources/list hides them
// from clients without UI support (uiMiddleware).
func (s *session) addUIResources() {
	for _, name := range uiScreens {
		body, err := uiFS.ReadFile("ui/" + name + ".html")
		if err != nil {
			panic("mcpserver: missing embedded ui/" + name + ".html")
		}
		uri, html := uiResourceURI(name), string(body)
		s.srv.AddResource(&mcp.Resource{
			URI: uri, Name: "ui-" + name, Title: "app-store-connect-mcp " + name + " view",
			MIMEType: uiMIMEType, Meta: uiResourceMeta(),
		}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI: uri, MIMEType: uiMIMEType, Text: html, Meta: uiResourceMeta(),
			}}}, nil
		})
	}
}

// uiMiddleware adds _meta.ui.resourceUri to listed tools with a screen for
// UI clients and hides ui:// resources from everyone else. Results are copied,
// never mutated: the SDK shares tool pointers across sessions.
func (s *session) uiMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	screens := map[string]string{}
	for _, t := range s.tools {
		if ui := t.Meta().UI; ui != "" && slices.Contains(uiScreens, ui) {
			screens[t.Name()] = ui
		}
	}
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil || (method != "tools/list" && method != "resources/list") {
			return res, err
		}
		ui := requestSupportsUI(req)
		switch r := res.(type) {
		case *mcp.ListToolsResult:
			if !ui {
				return res, nil
			}
			out := *r
			out.Tools = make([]*mcp.Tool, len(r.Tools))
			for i, t := range r.Tools {
				out.Tools[i] = t
				if screen, ok := screens[t.Name]; ok {
					c := *t
					c.Meta = maps.Clone(t.Meta)
					if c.Meta == nil {
						c.Meta = mcp.Meta{}
					}
					c.Meta["ui"] = map[string]any{"resourceUri": uiResourceURI(screen)}
					out.Tools[i] = &c
				}
			}
			return &out, nil
		case *mcp.ListResourcesResult:
			if ui {
				return res, nil
			}
			out := *r
			out.Resources = slices.DeleteFunc(slices.Clone(r.Resources), func(x *mcp.Resource) bool {
				return strings.HasPrefix(x.URI, uiURIPrefix)
			})
			return &out, nil
		}
		return res, nil
	}
}
