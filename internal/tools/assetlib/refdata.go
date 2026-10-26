package assetlib

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// FileInfo describes a local file offered to the library.
type FileInfo struct {
	Name          string
	Size          int64
	Width, Height int
	Alpha         bool
	Video         bool
	Ext           string
}

var videoExts = []string{".mp4", ".m4v", ".mov"}

// Inspect reads a PNG or JPEG header for its pixel size and alpha channel
// (PNG color types 4 and 6 or a tRNS chunk). Videos (.mp4, .m4v, .mov) are
// recognised by extension and report only their size.
func Inspect(name string, r io.ReaderAt, size int64) (FileInfo, error) {
	f := FileInfo{Name: filepath.Base(name), Size: size, Ext: strings.ToLower(filepath.Ext(name))}
	if slices.Contains(videoExts, f.Ext) {
		f.Video = true
		return f, nil
	}
	var head [33]byte
	n, err := r.ReadAt(head[:], 0)
	if n < 4 {
		return f, fmt.Errorf("%w: %s: file too short: %w", tool.ErrInvalidArgs, f.Name, errOr(err, io.ErrUnexpectedEOF))
	}
	switch {
	case n == len(head) && string(head[:8]) == "\x89PNG\r\n\x1a\n":
		return inspectPNG(f, r, head)
	case head[0] == 0xFF && head[1] == 0xD8:
		return inspectJPEG(f, r)
	}
	return f, fmt.Errorf("%w: %s is neither PNG, JPEG nor a video (.mp4, .m4v, .mov)", tool.ErrInvalidArgs, f.Name)
}

func errOr(err, def error) error {
	if err != nil {
		return err
	}
	return def
}

func inspectPNG(f FileInfo, r io.ReaderAt, head [33]byte) (FileInfo, error) {
	if string(head[12:16]) != "IHDR" {
		return f, fmt.Errorf("%w: %s: png header: first chunk is not IHDR", tool.ErrInvalidArgs, f.Name)
	}
	f.Width, f.Height = int(binary.BigEndian.Uint32(head[16:20])), int(binary.BigEndian.Uint32(head[20:24]))
	if f.Width <= 0 || f.Height <= 0 || f.Width > 1<<16 || f.Height > 1<<16 {
		return f, fmt.Errorf("%w: %s: png size %dx%d", tool.ErrInvalidArgs, f.Name, f.Width, f.Height)
	}
	if ct := head[25]; ct == 4 || ct == 6 {
		f.Alpha = true
		return f, nil
	}
	// Walk chunks up to IDAT looking for tRNS.
	off := int64(8)
	var hdr [8]byte
	for range 1 << 12 {
		if _, err := r.ReadAt(hdr[:], off); err != nil {
			return f, fmt.Errorf("%w: %s: read png: %w", tool.ErrInvalidArgs, f.Name, err)
		}
		switch string(hdr[4:8]) {
		case "tRNS":
			f.Alpha = true
			return f, nil
		case "IDAT", "IEND":
			return f, nil
		}
		off += 12 + int64(binary.BigEndian.Uint32(hdr[:4]))
	}
	return f, nil
}

func inspectJPEG(f FileInfo, r io.ReaderAt) (FileInfo, error) {
	off := int64(2)
	var b [9]byte
	for range 1 << 12 {
		if _, err := r.ReadAt(b[:4], off); err != nil {
			return f, fmt.Errorf("%w: %s: read jpeg: %w", tool.ErrInvalidArgs, f.Name, err)
		}
		if b[0] != 0xFF {
			return f, fmt.Errorf("%w: %s: jpeg: bad marker", tool.ErrInvalidArgs, f.Name)
		}
		m := b[1]
		if m == 0xFF {
			off++
			continue
		}
		if m == 0xD8 || (m >= 0xD0 && m <= 0xD7) || m == 0x01 {
			off += 2
			continue
		}
		seg := int64(binary.BigEndian.Uint16(b[2:4]))
		if m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			if _, err := r.ReadAt(b[:9], off); err != nil {
				return f, fmt.Errorf("%w: %s: read jpeg: %w", tool.ErrInvalidArgs, f.Name, err)
			}
			f.Height, f.Width = int(binary.BigEndian.Uint16(b[5:7])), int(binary.BigEndian.Uint16(b[7:9]))
			return f, nil
		}
		if m == 0xD9 || m == 0xDA || seg < 2 {
			break
		}
		off += 2 + seg
	}
	return f, fmt.Errorf("%w: %s: jpeg has no frame header", tool.ErrInvalidArgs, f.Name)
}

type refSpec struct {
	ID      string
	Short   string
	MinW    int
	MaxW    int
	MinH    int
	MaxH    int
	Alpha   bool
	Video   bool
	Exts    []string
	MaxSize int64
}

func (s refSpec) fitsDims(f FileInfo) bool {
	if f.Video && f.Width == 0 {
		return true // video dimensions are not read locally
	}
	return f.Width >= s.MinW && f.Width <= s.MaxW && f.Height >= s.MinH && f.Height <= s.MaxH
}

func (s refSpec) dims() string {
	if s.MinW == s.MaxW && s.MinH == s.MaxH {
		return fmt.Sprintf("%dx%d", s.MinW, s.MinH)
	}
	return fmt.Sprintf("%d-%dx%d-%d", s.MinW, s.MaxW, s.MinH, s.MaxH)
}

// RefData is Apple's parsed appAssetLibraryRefData: placement groups, the
// image/video specs each placement type and group accepts, and the per
// feature placement limits.
type RefData struct {
	specs    map[string]refSpec
	mappings map[string]map[string][]string       // placementType → group → specIDs
	limits   map[string]map[string]map[string]int // feature → placementType → group → maxCount
	groups   map[string]string                    // group → displayClassId
	stores   map[string]string                    // group → platform (store), e.g. MAC_APP_STORE, ANY
}

type rawDims struct {
	MinWidth  int `json:"minWidth"`
	MaxWidth  int `json:"maxWidth"`
	MinHeight int `json:"minHeight"`
	MaxHeight int `json:"maxHeight"`
}

type rawSpec struct {
	SpecID         string   `json:"specId"`
	ShortName      string   `json:"shortName"`
	Dimensions     rawDims  `json:"dimensions"`
	AlphaAllowed   bool     `json:"alphaAllowed"`
	FileExtensions []string `json:"fileExtensions"`
	MaxFileSize    int64    `json:"maxFileSize"`
}

type rawRefAttrs struct {
	Features []struct {
		FeatureID         string `json:"featureId"`
		PlacementPolicies []struct {
			PlacementType string `json:"placementType"`
			GroupLimits   []struct {
				GroupIDs []string `json:"groupIds"`
				MaxCount int      `json:"maxCount"`
			} `json:"groupLimits"`
		} `json:"placementPolicies"`
	} `json:"features"`
	PlacementProfileGroups []struct {
		ID             string `json:"placementProfileGroupId"`
		DisplayClassID string `json:"displayClassId"`
		Platform       string `json:"platform"`
	} `json:"placementProfileGroups"`
	ImageSpecs     []rawSpec `json:"imageSpecs"`
	VideoSpecs     []rawSpec `json:"videoSpecs"`
	PlacementTypes []struct {
		ID           string `json:"placementTypeId"`
		SpecMappings []struct {
			Group string   `json:"placementGroupId"`
			Specs []string `json:"specs"`
		} `json:"specMappings"`
	} `json:"placementTypes"`
}

// ParseRefData parses a GET /v1/appAssetLibraryRefData body (a collection
// or single-resource document).
func ParseRefData(body []byte) (*RefData, error) {
	var doc struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse asset library ref data: %w", err)
	}
	type res struct {
		Attributes rawRefAttrs `json:"attributes"`
	}
	var list []res
	if err := json.Unmarshal(doc.Data, &list); err != nil {
		var one res
		if err := json.Unmarshal(doc.Data, &one); err != nil {
			return nil, fmt.Errorf("parse asset library ref data: %w", err)
		}
		list = []res{one}
	}
	r := &RefData{
		specs:    map[string]refSpec{},
		mappings: map[string]map[string][]string{},
		limits:   map[string]map[string]map[string]int{},
		groups:   map[string]string{},
		stores:   map[string]string{},
	}
	for _, it := range list {
		a := it.Attributes
		add := func(ss []rawSpec, video bool) {
			for _, s := range ss {
				exts := make([]string, len(s.FileExtensions))
				for i, e := range s.FileExtensions {
					exts[i] = strings.ToLower(e)
				}
				r.specs[s.SpecID] = refSpec{
					ID: s.SpecID, Short: s.ShortName, Alpha: s.AlphaAllowed && !video, Video: video, Exts: exts, MaxSize: s.MaxFileSize,
					MinW: s.Dimensions.MinWidth, MaxW: s.Dimensions.MaxWidth, MinH: s.Dimensions.MinHeight, MaxH: s.Dimensions.MaxHeight,
				}
			}
		}
		add(a.ImageSpecs, false)
		add(a.VideoSpecs, true)
		for _, g := range a.PlacementProfileGroups {
			r.groups[g.ID] = g.DisplayClassID
			r.stores[g.ID] = g.Platform
		}
		for _, pt := range a.PlacementTypes {
			m := r.mappings[pt.ID]
			if m == nil {
				m = map[string][]string{}
				r.mappings[pt.ID] = m
			}
			for _, sm := range pt.SpecMappings {
				m[sm.Group] = append(m[sm.Group], sm.Specs...)
			}
		}
		for _, f := range a.Features {
			fm := r.limits[f.FeatureID]
			if fm == nil {
				fm = map[string]map[string]int{}
				r.limits[f.FeatureID] = fm
			}
			for _, pp := range f.PlacementPolicies {
				gm := fm[pp.PlacementType]
				if gm == nil {
					gm = map[string]int{}
					fm[pp.PlacementType] = gm
				}
				for _, gl := range pp.GroupLimits {
					for _, g := range gl.GroupIDs {
						gm[g] = gl.MaxCount
					}
				}
			}
		}
	}
	if len(r.mappings) == 0 && len(r.limits) == 0 {
		return nil, errors.New("parse asset library ref data: no placement types or features")
	}
	return r, nil
}

// Groups returns, sorted, the placement groups of placementType with a spec
// that accepts f.
func (r *RefData) Groups(placementType string, f FileInfo) []string {
	var out []string
	for g := range r.mappings[placementType] {
		if id, _ := r.Check(placementType, g, f); id != "" {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}

// GroupPlatform returns the store platform of a placement group as Apple
// lists it (IPHONE_APP_STORE, MAC_APP_STORE, ANY, …), or "" for an unknown
// group.
func (r *RefData) GroupPlatform(group string) string { return r.stores[group] }

// Check returns the id of the first spec of placementType/group that accepts
// f, or "" and the problems against the closest spec (dimensions, alpha,
// extension, maximum size).
func (r *RefData) Check(placementType, group string, f FileInfo) (string, []string) {
	ids, ok := r.mappings[placementType][group]
	if !ok {
		return "", []string{fmt.Sprintf("placement group %s does not take %s", group, placementType)}
	}
	var best []string
	bestDims := false
	var sizes []string
	for _, id := range ids {
		s, ok := r.specs[id]
		if !ok || s.Video != f.Video {
			continue
		}
		sizes = append(sizes, s.dims())
		var probs []string
		dimsOK := s.fitsDims(f)
		if !dimsOK {
			probs = append(probs, fmt.Sprintf("%dx%d is not %s", f.Width, f.Height, s.dims()))
		}
		if f.Alpha && !s.Alpha {
			probs = append(probs, "alpha channel not allowed for "+group)
		}
		if len(s.Exts) > 0 && !slices.Contains(s.Exts, f.Ext) {
			probs = append(probs, fmt.Sprintf("extension %q not accepted (%s)", f.Ext, strings.Join(s.Exts, ", ")))
		}
		if s.MaxSize > 0 && f.Size > s.MaxSize {
			probs = append(probs, "file larger than "+strconv.FormatInt(s.MaxSize, 10)+" bytes")
		}
		if len(probs) == 0 {
			return s.ID, nil
		}
		if best == nil || (dimsOK && !bestDims) || (dimsOK == bestDims && len(probs) < len(best)) {
			best, bestDims = probs, dimsOK
		}
	}
	if best == nil {
		kind := "image"
		if f.Video {
			kind = "video"
		}
		return "", []string{fmt.Sprintf("%s %s takes no %s", placementType, group, kind)}
	}
	if !bestDims {
		slices.Sort(sizes)
		best[0] = fmt.Sprintf("%dx%d is not an accepted %s size for %s (%s)", f.Width, f.Height, placementType, group, strings.Join(slices.Compact(sizes), ", "))
	}
	return "", best
}

// MaxCount returns the placement limit of group for placementType within
// feature (e.g. APP_STORE_VERSIONS), or 0 when Apple lists none.
func (r *RefData) MaxCount(feature, placementType, group string) int {
	return r.limits[feature][placementType][group]
}

// AlphaAllowed reports whether any image spec of group accepts an alpha
// channel (Mac screenshots do; iPhone and iPad ones do not).
func (r *RefData) AlphaAllowed(group string) bool {
	for _, m := range r.mappings {
		for _, id := range m[group] {
			if s := r.specs[id]; !s.Video && s.Alpha {
				return true
			}
		}
	}
	return false
}

// refCache holds parsed ref data per profile for the process lifetime.
var refCache sync.Map // profile name → *RefData

// LoadRefData GETs /v1/appAssetLibraryRefData (raw, uncapped, no limit) once
// per process and profile.
func LoadRefData(ctx context.Context, env *tool.Env) (*RefData, error) {
	key := env.Profile.Name
	if v, ok := refCache.Load(key); ok {
		return v.(*RefData), nil
	}
	// Download skips the paging limit and the response cap; Apple answers 406
	// to its default gzip Accept, so ask for JSON.
	body, _, err := env.ASC.Download(ctx, tool.Request{Method: "GET", Path: "/v1/appAssetLibraryRefData", Accept: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("read asset library ref data: %w", err)
	}
	r, err := ParseRefData(body)
	if err != nil {
		return nil, err
	}
	v, _ := refCache.LoadOrStore(key, r)
	return v.(*RefData), nil
}
