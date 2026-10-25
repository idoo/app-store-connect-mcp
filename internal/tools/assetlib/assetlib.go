// Package assetlib reads the App Store Asset Library placements of version
// localizations. Screenshots placed through the library do not show up in
// appScreenshotSets, so release_status and screenshots_list read both.
//
// Live shape (2026-10-26): GET /v1/appStoreVersionLocalizations/{id}/placements
// returns appAssetLibraryPlacements with placementGroup (e.g. MAC_PROFILE),
// placementType (APP_SCREENSHOT), mediaType (IMAGE), state (ACTIVE) and an
// image or video relationship; the image carries its own state (APPROVED,
// IN_REVIEW, ARCHIVED) and fileName.
package assetlib

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Placement types for store screenshots and previews; any other type (header
// or search-result creatives, event cards) is a creative.
const (
	TypeScreenshot = "APP_SCREENSHOT"
	TypePreview    = "APP_PREVIEW"
)

// Limits of one read.
const (
	maxPerLocalization = 200
	concurrency        = 4
)

// Placement is one library asset placed on a localization.
type Placement struct {
	ID         string         `json:"id"`
	Group      string         `json:"group"`
	Type       string         `json:"type"`
	Media      string         `json:"media,omitempty"`
	State      string         `json:"state,omitempty"`
	AssetID    string         `json:"asset_id,omitempty"`
	AssetState string         `json:"asset_state,omitempty"`
	FileName   tool.Untrusted `json:"file_name,omitempty"`
}

// Creative reports whether p is not a store screenshot or preview.
func (p Placement) Creative() bool { return p.Type != TypeScreenshot && p.Type != TypePreview }

// Failed reports a failed placement or asset.
func (p Placement) Failed() bool { return p.State == "FAILED" || p.AssetState == "FAILED" }

// Processing reports a placement whose asset is still being processed.
func (p Placement) Processing() bool { return p.State == "ASSET_PROCESSING" }

// Library is the placements of a set of localizations, keyed by
// localization id, each sorted by group, type and id.
type Library struct {
	ByLocalization map[string][]Placement
}

// Placed returns the ids of the version's localizations that have library
// placements, in one request (include=placements, one per localization).
func Placed(ctx context.Context, env *tool.Env, versionID string) ([]string, error) {
	resp, err := env.ASC.Get(ctx, "/v1/appStoreVersions/"+versionID+"/appStoreVersionLocalizations", url.Values{
		"fields[appStoreVersionLocalizations]": {"locale,placements"},
		"include":                              {"placements"},
		"limit[placements]":                    {"1"},
		"limit":                                {"200"},
	})
	if err != nil {
		return nil, fmt.Errorf("list placed localizations: %w", err)
	}
	var ids []string
	for _, l := range resp.Data {
		if len(l.RelIDs("placements")) > 0 {
			ids = append(ids, l.ID)
		}
	}
	return ids, nil
}

// ForLocalizations reads the placements of the given localizations with
// their assets, at most 4 requests in flight and 200 placements each.
func ForLocalizations(ctx context.Context, env *tool.Env, locIDs []string) (*Library, error) {
	lib := &Library{ByLocalization: make(map[string][]Placement, len(locIDs))}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	sem := make(chan struct{}, concurrency)
	for _, id := range locIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer func() { <-sem; wg.Done() }()
			ps, err := localizationPlacements(ctx, env, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			lib.ByLocalization[id] = ps
		}(id)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return lib, nil
}

func localizationPlacements(ctx context.Context, env *tool.Env, locID string) ([]Placement, error) {
	resp, err := env.ASC.List(ctx, "/v1/appStoreVersionLocalizations/"+locID+"/placements", tool.ListOptions{
		Include:  []string{"image", "video"},
		Limit:    50,
		MaxItems: maxPerLocalization,
	})
	if err != nil {
		return nil, fmt.Errorf("list asset library placements: %w", err)
	}
	assets := map[string]tool.Resource{}
	for _, r := range resp.Included {
		assets[r.Type+":"+r.ID] = r
	}
	out := make([]Placement, 0, len(resp.Data))
	for _, r := range resp.Data {
		p := Placement{ID: r.ID, Group: r.Str("placementGroup"), Type: r.Str("placementType"), Media: r.Str("mediaType"), State: r.Str("state")}
		for rel, typ := range map[string]string{"image": "appAssetLibraryImages", "video": "appAssetLibraryVideos"} {
			if id := r.RelID(rel); id != "" {
				p.AssetID = id
				if a, ok := assets[typ+":"+id]; ok {
					p.AssetState = a.Str("state")
					p.FileName = tool.Untrusted(a.Str("fileName"))
				}
			}
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Placement) int {
		return strings.Compare(a.Group+"\x00"+a.Type+"\x00"+a.ID, b.Group+"\x00"+b.Type+"\x00"+b.ID)
	})
	return out, nil
}

// groupDisplays maps Apple's APP_SCREENSHOT placementProfileGroups
// (GET /v1/appAssetLibraryRefData, 2026-10-26; testdata/refdata.json) to the
// screenshot display types they fill. IMESSAGE_* and DEFAULT_PROFILE groups
// never fill an app screenshot slot.
var groupDisplays = map[string][]applemeta.DisplayType{
	"IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE":  {applemeta.AppIPhone67},
	"IPHONE_FACE_ID_LARGE_PROFILE":         {applemeta.AppIPhone65},
	"IPHONE_DYNAMIC_ISLAND_MEDIUM_PROFILE": {applemeta.AppIPhone61},
	"IPHONE_FACE_ID_MEDIUM_PROFILE":        {applemeta.AppIPhone58, applemeta.AppIPhone61}, // 2532x1170 is a 6.1" size too
	"IPHONE_HOME_BUTTON_LARGE_PROFILE":     {applemeta.AppIPhone55},
	"IPHONE_HOME_BUTTON_MEDIUM_PROFILE":    {applemeta.AppIPhone47},
	"IPHONE_HOME_BUTTON_40_PROFILE":        {applemeta.AppIPhone40},
	"IPHONE_HOME_BUTTON_35_PROFILE":        {applemeta.AppIPhone35},
	"IPHONE_DUO_PROFILE":                   {applemeta.AppIPhoneDuo},
	"IPAD_13_PROFILE":                      {applemeta.AppIPadPro3Gen129},
	"IPAD_129_PROFILE":                     {applemeta.AppIPadPro129},
	"IPAD_11_PROFILE":                      {applemeta.AppIPadPro3Gen11},
	"IPAD_105_PROFILE":                     {applemeta.AppIPad105},
	"IPAD_97_PROFILE":                      {applemeta.AppIPad97},
	"MAC_PROFILE":                          {applemeta.AppDesktop},
	"TV_PROFILE":                           {applemeta.AppAppleTV},
	"VISION_PRO_PROFILE":                   {applemeta.AppAppleVisionPro},
	"WATCH_ULTRA_PROFILE":                  {applemeta.WatchUltra},
	"WATCH_SERIES_10_PROFILE":              {applemeta.WatchSeries10},
	"WATCH_SERIES_7_PROFILE":               {applemeta.WatchSeries7},
	"WATCH_SERIES_4_PROFILE":               {applemeta.WatchSeries4},
	"WATCH_SERIES_3_PROFILE":               {applemeta.WatchSeries3},
}

// Displays returns the display types an APP_SCREENSHOT placement group
// fills, and false for a group missing from Apple's table.
func Displays(group string) ([]applemeta.DisplayType, bool) {
	d, ok := groupDisplays[group]
	return d, ok
}

// GroupsForDisplay returns, sorted, the APP_SCREENSHOT placement groups that
// fill display d per Apple's group table (none for an unmapped display).
func GroupsForDisplay(d applemeta.DisplayType) []string {
	var out []string
	for g, ds := range groupDisplays {
		if slices.Contains(ds, d) {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}

// IPad reports a screenshot placement in an iPad group.
func (p Placement) IPad() bool {
	ds, _ := Displays(p.Group)
	return p.Type == TypeScreenshot && slices.ContainsFunc(ds, func(d applemeta.DisplayType) bool {
		return strings.HasPrefix(string(d), "APP_IPAD")
	})
}

// UnknownGroups lists the screenshot placement groups in ps that Apple's
// table does not map (they never count as covering a slot).
func UnknownGroups(ps []Placement) []string {
	var out []string
	for _, p := range ps {
		if _, ok := groupDisplays[p.Group]; !ok && p.Type == TypeScreenshot && !slices.Contains(out, p.Group) {
			out = append(out, p.Group)
		}
	}
	return out
}

// Placement states (AppAssetLibraryPlacement state). Live, Apple also
// returns ACTIVE, which the spec does not list.
const (
	PlacementActive     = "ACTIVE"
	PlacementProcessing = "ASSET_PROCESSING"
	PlacementFailed     = "FAILED"
	parentStatePrefix   = "PARENT_"
)

// knownPlacementState reports a placement state from the spec enum or ACTIVE.
func knownPlacementState(s string) bool {
	switch s {
	case PlacementActive, PlacementProcessing, PlacementFailed,
		"PARENT_PREPARE_FOR_SUBMISSION", "PARENT_READY_FOR_REVIEW", "PARENT_WAITING_FOR_REVIEW", "PARENT_IN_REVIEW", "PARENT_APPROVED":
		return true
	}
	return false
}

// Blocker returns why p cannot fill a slot ("" when it can): a failed or
// processing placement, a placement without its asset, or an asset whose
// state is not [Usable] (AWAITING_UPLOAD, UPLOAD_COMPLETE, FAILED, REJECTED,
// ARCHIVED, …). Callers report the reason as an item.
func (p Placement) Blocker() string {
	switch {
	case p.State == PlacementFailed:
		return "placement " + p.ID + " FAILED"
	case p.State == PlacementProcessing:
		return "placement " + p.ID + " asset still processing"
	case p.AssetID == "" || p.AssetState == "":
		return "placement " + p.ID + " has no readable asset"
	case !Usable(p.AssetState):
		return fmt.Sprintf("placement %s asset %s is %s", p.ID, p.AssetID, p.AssetState)
	}
	return ""
}

// Warning returns a note for a placement state hamn does not know (it still
// covers when its asset is usable), else "".
func (p Placement) Warning() string {
	if p.State == "" || knownPlacementState(p.State) || strings.HasPrefix(p.State, parentStatePrefix) {
		return ""
	}
	return fmt.Sprintf("placement %s has unknown state %s", p.ID, p.State)
}

// Covers reports whether an APP_SCREENSHOT placement in ps whose asset is
// usable ([Placement.Blocker] empty) fills display d per Apple's group table.
func Covers(ps []Placement, d applemeta.DisplayType) bool {
	return slices.ContainsFunc(ps, func(p Placement) bool {
		ds, _ := Displays(p.Group)
		return p.Type == TypeScreenshot && p.Blocker() == "" && slices.Contains(ds, d)
	})
}
