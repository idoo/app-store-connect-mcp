package tool

import "slices"

// Toolset groups tools that are enabled or disabled together.
type Toolset string

// Toolset names. The first six are enabled by default.
const (
	Core         Toolset = "core"
	Release      Toolset = "release"
	TestFlight   Toolset = "testflight"
	Metadata     Toolset = "metadata"
	Reviews      Toolset = "reviews"
	Reports      Toolset = "reports"
	Screenshots  Toolset = "screenshots"
	Uploads      Toolset = "uploads"
	Monetization Toolset = "monetization"
	Signing      Toolset = "signing"
	XcodeCloud   Toolset = "xcodecloud"
	Webhooks     Toolset = "webhooks"
	Admin        Toolset = "admin"
	Store        Toolset = "store"
)

// AllToolsets lists every toolset in display order.
var AllToolsets = []Toolset{
	Core, Release, TestFlight, Metadata, Reviews, Reports,
	Screenshots, Uploads, Monetization, Signing, XcodeCloud, Webhooks, Admin, Store,
}

// DefaultToolsets lists the toolsets enabled when no selection is given.
var DefaultToolsets = []Toolset{Core, Release, TestFlight, Metadata, Reviews, Reports}

// Valid reports whether ts is a known toolset.
func (ts Toolset) Valid() bool { return slices.Contains(AllToolsets, ts) }

// IsDefault reports whether ts is enabled by default.
func (ts Toolset) IsDefault() bool { return slices.Contains(DefaultToolsets, ts) }
