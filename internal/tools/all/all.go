// Package all links every domain tool package so their init functions
// register tools with internal/tool.
package all

import (
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/admin"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/core"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/metadata"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/monetization"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/release"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/reports"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/reviews"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/screenshots"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/signing"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/store"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/testflight"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/uploads"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/webhooks"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/xcodecloud"
)
