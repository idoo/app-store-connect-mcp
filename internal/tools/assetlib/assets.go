package assetlib

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Asset is one image or video of an app's asset library.
type Asset struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // image or video
	FileName     string `json:"file_name,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	State        string `json:"state,omitempty"`
	StateDetails string `json:"state_details,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
}

// maxAssets caps one kind of library asset read.
const maxAssets = 5000

// usableStates are the asset states (AppAssetLibraryImage/Video state enum)
// that may fill a slot: processed (COMPLETE, PREPARE_FOR_SUBMISSION) and the
// review states after it. AWAITING_UPLOAD, UPLOAD_COMPLETE, FAILED, REJECTED
// and ARCHIVED never do.
var usableStates = []string{
	"COMPLETE", "PREPARE_FOR_SUBMISSION", "READY_FOR_REVIEW", "WAITING_FOR_REVIEW",
	"IN_REVIEW", "ACCEPTED", "APPROVED",
}

// Usable reports whether an asset in state may fill a slot.
func Usable(state string) bool { return slices.Contains(usableStates, state) }

// Assets reads the app's asset library id and its images and videos, both
// kinds concurrently (≤ 4 in flight), sorted by kind and id. Live, the
// library id equals the app id.
func Assets(ctx context.Context, env *tool.Env, appID string) (string, []Asset, error) {
	lib, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/assetLibrary", nil)
	if err != nil {
		return "", nil, fmt.Errorf("read asset library: %w", err)
	}
	r, ok := lib.First()
	if !ok {
		return "", nil, fmt.Errorf("app %s has no asset library", appID)
	}
	libID := r.ID
	kinds := []struct{ rel, kind string }{{"images", "image"}, {"videos", "video"}}
	results := make([][]Asset, len(kinds))
	errs := make([]error, len(kinds))
	done := make(chan struct{}, len(kinds))
	for i, k := range kinds {
		go func() {
			defer func() { done <- struct{}{} }()
			resp, err := env.ASC.List(ctx, "/v1/appAssetLibraries/"+libID+"/"+k.rel, tool.ListOptions{Limit: 200, MaxItems: maxAssets})
			if err != nil {
				errs[i] = fmt.Errorf("list asset library %s: %w", k.rel, err)
				return
			}
			for _, a := range resp.Data {
				size, _ := a.Int("fileSize")
				w, _ := a.Int("width")
				h, _ := a.Int("height")
				results[i] = append(results[i], Asset{
					ID: a.ID, Kind: k.kind, FileName: a.Str("fileName"), FileSize: int64(size),
					State: a.Str("state"), StateDetails: stateDetails(a.Attributes["stateDetails"]),
					Width: w, Height: h,
				})
			}
		}()
	}
	for range kinds {
		<-done
	}
	for _, e := range errs {
		if e != nil {
			return "", nil, e
		}
	}
	out := slices.Concat(results...)
	slices.SortFunc(out, func(a, b Asset) int { return strings.Compare(a.Kind+"\x00"+a.ID, b.Kind+"\x00"+b.ID) })
	return libID, out, nil
}

// stateDetails renders Apple's stateDetails (a string or {errors:[{code,
// message}]}) as text.
func stateDetails(v any) string {
	switch d := v.(type) {
	case nil:
		return ""
	case string:
		return d
	case map[string]any:
		var parts []string
		for _, key := range []string{"errors", "warnings"} {
			list, _ := d[key].([]any)
			for _, e := range list {
				m, _ := e.(map[string]any)
				code, _ := m["code"].(string)
				msg, _ := m["message"].(string)
				parts = append(parts, strings.TrimSpace(code+": "+msg))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return fmt.Sprint(v)
}
