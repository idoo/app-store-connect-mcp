package mcpserver

import (
	"encoding/base64"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverDescription matches the description in server.json.
const serverDescription = "MCP server for App Store Connect: releases, TestFlight, reviews, reports. Read-only by default."

// serverWebsite is the project home advertised in serverInfo.
const serverWebsite = "https://github.com/idoo/app-store-connect-mcp"

// iconSVG is the server icon: a white upward arrow (shipping a release) on an
// App Store blue rounded square. It is served inline as a data: URI so clients
// never fetch a remote URL.
const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#0d84ff"/><path d="M32 12 50 34H38v18H26V34H14z" fill="#fff"/></svg>`

// serverIcons returns the icons advertised in serverInfo.
func serverIcons() []mcp.Icon {
	return []mcp.Icon{{
		Source:   "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(iconSVG)),
		MIMEType: "image/svg+xml",
		Sizes:    []string{"any"},
	}}
}

// serverImplementation is the serverInfo every app-store-connect-mcp server
// advertises.
func serverImplementation(version string) *mcp.Implementation {
	return &mcp.Implementation{
		Name:        "app-store-connect-mcp",
		Title:       "App Store Connect MCP",
		Description: serverDescription,
		Version:     version,
		WebsiteURL:  serverWebsite,
		Icons:       serverIcons(),
	}
}
