// Package all links every domain tool package so their init functions
// register tools with internal/tool.
package all

import (
	_ "github.com/idoo/hamn/internal/tools/admin"
	_ "github.com/idoo/hamn/internal/tools/core"
	_ "github.com/idoo/hamn/internal/tools/metadata"
	_ "github.com/idoo/hamn/internal/tools/monetization"
	_ "github.com/idoo/hamn/internal/tools/release"
	_ "github.com/idoo/hamn/internal/tools/reports"
	_ "github.com/idoo/hamn/internal/tools/reviews"
	_ "github.com/idoo/hamn/internal/tools/screenshots"
	_ "github.com/idoo/hamn/internal/tools/signing"
	_ "github.com/idoo/hamn/internal/tools/store"
	_ "github.com/idoo/hamn/internal/tools/testflight"
	_ "github.com/idoo/hamn/internal/tools/uploads"
	_ "github.com/idoo/hamn/internal/tools/webhooks"
	_ "github.com/idoo/hamn/internal/tools/xcodecloud"
)
