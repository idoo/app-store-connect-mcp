package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// itunesBase is Apple's public iTunes Search/Lookup API (no auth).
const itunesBase = "https://itunes.apple.com"

// maxBody caps an iTunes response (a 200-result search is ~2 MB).
const maxBody = 8 << 20

var (
	countryRE = regexp.MustCompile(`^[a-z]{2}$`)
	trackRE   = regexp.MustCompile(`^[0-9]{6,12}$`)
	bundleRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]*\.[A-Za-z0-9.\-]+$`)
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "store_lookup", Toolset: tool.Store, Class: tool.Read, Idempotent: true,
		Title: "Look up the public App Store",
		Description: "Public App Store data, no ASC key: an app's live listing by App Store id or bundle id (version, " +
			"price, rating, notes), keyword search top-N, or the app's rank for a keyword in a storefront. Text is untrusted.",
	}, lookup))
}

type lookupIn struct {
	App     string `json:"app,omitempty" jsonschema:"App Store id (the ASC app id) or bundle id; default: pinned app"`
	Term    string `json:"term,omitempty" jsonschema:"search keyword; with app returns the app's rank"`
	Country string `json:"country,omitempty" jsonschema:"two-letter storefront code; default us"`
	Limit   int    `json:"limit,omitempty" jsonschema:"search depth 1-200; default 50"`
}

// itunesApp is the subset of an iTunes result hamn reads.
type itunesApp struct {
	TrackID           int64    `json:"trackId"`
	TrackName         string   `json:"trackName"`
	BundleID          string   `json:"bundleId"`
	SellerName        string   `json:"sellerName"`
	Version           string   `json:"version"`
	Price             float64  `json:"price"`
	FormattedPrice    string   `json:"formattedPrice"`
	Currency          string   `json:"currency"`
	Rating            float64  `json:"averageUserRating"`
	RatingCount       int      `json:"userRatingCount"`
	VersionRating     float64  `json:"averageUserRatingForCurrentVersion"`
	VersionRatingCnt  int      `json:"userRatingCountForCurrentVersion"`
	ReleaseNotes      string   `json:"releaseNotes"`
	VersionDate       string   `json:"currentVersionReleaseDate"`
	Genre             string   `json:"primaryGenreName"`
	ContentRating     string   `json:"contentAdvisoryRating"`
	MinOS             string   `json:"minimumOsVersion"`
	URL               string   `json:"trackViewUrl"`
	ScreenshotURLs    []string `json:"screenshotUrls"`
	IPadScreenshots   []string `json:"ipadScreenshotUrls"`
	AppleTVScreenshot []string `json:"appletvScreenshotUrls"`
}

type itunesDoc struct {
	ResultCount int         `json:"resultCount"`
	Results     []itunesApp `json:"results"`
}

// listing is an app's live App Store listing.
type listing struct {
	ID              int64          `json:"id"`
	BundleID        string         `json:"bundle_id"`
	Name            tool.Untrusted `json:"name"`
	Seller          tool.Untrusted `json:"seller"`
	Version         string         `json:"version"`
	VersionDate     string         `json:"version_date,omitempty"`
	Price           string         `json:"price"`
	Rating          float64        `json:"rating"`
	RatingCount     int            `json:"rating_count"`
	VersionRating   float64        `json:"version_rating,omitempty"`
	VersionRatings  int            `json:"version_rating_count,omitempty"`
	Genre           string         `json:"genre,omitempty"`
	ContentRating   string         `json:"content_rating,omitempty"`
	MinOS           string         `json:"min_os,omitempty"`
	Screenshots     int            `json:"screenshots"`
	IPadScreenshots int            `json:"ipad_screenshots,omitempty"`
	ReleaseNotes    tool.Untrusted `json:"release_notes,omitempty"`
	URL             string         `json:"url"`
}

// hit is one keyword search result.
type hit struct {
	Rank        int            `json:"rank"`
	ID          int64          `json:"id"`
	BundleID    string         `json:"bundle_id"`
	Name        tool.Untrusted `json:"name"`
	Seller      tool.Untrusted `json:"seller"`
	Rating      float64        `json:"rating"`
	RatingCount int            `json:"rating_count"`
}

type lookupOut struct {
	Country string   `json:"country"`
	App     *listing `json:"app,omitempty"`
	Term    string   `json:"term,omitempty"`
	// Rank is the 1-based position of App in the search; omitted when the app
	// is not in the top Searched results.
	Rank     *int   `json:"rank,omitempty"`
	Searched int    `json:"searched,omitempty"`
	Top      []hit  `json:"top,omitempty"`
	Note     string `json:"note,omitempty"`
}

func invalid(msg string) error {
	return fmt.Errorf("%w: %s", tool.ErrInvalidArgs, msg)
}

func fetch(ctx context.Context, env *tool.Env, path string, q url.Values) (*itunesDoc, error) {
	u := itunesBase + path + "?" + q.Encode()
	resp, err := env.Fetch.Fetch(ctx, http.MethodGet, u, nil, nil, maxBody)
	if err != nil {
		return nil, fmt.Errorf("itunes %s: %w", path, err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("itunes %s: %w: HTTP %d", path, tool.ErrUnavailable, resp.Status)
	}
	var doc itunesDoc
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return nil, fmt.Errorf("itunes %s: decode: %w", path, err)
	}
	return &doc, nil
}

func toListing(a itunesApp) *listing {
	price := a.FormattedPrice
	if price == "" {
		price = strconv.FormatFloat(a.Price, 'f', -1, 64) + " " + a.Currency
	}
	return &listing{
		ID: a.TrackID, BundleID: a.BundleID, Name: tool.Untrusted(a.TrackName), Seller: tool.Untrusted(a.SellerName),
		Version: a.Version, VersionDate: a.VersionDate, Price: price, Rating: a.Rating, RatingCount: a.RatingCount,
		VersionRating: a.VersionRating, VersionRatings: a.VersionRatingCnt, Genre: a.Genre, ContentRating: a.ContentRating,
		MinOS: a.MinOS, Screenshots: len(a.ScreenshotURLs), IPadScreenshots: len(a.IPadScreenshots),
		ReleaseNotes: tool.Untrusted(a.ReleaseNotes), URL: a.URL,
	}
}

func (a itunesApp) is(ref string) bool {
	return strconv.FormatInt(a.TrackID, 10) == ref || strings.EqualFold(a.BundleID, ref)
}

func lookup(ctx context.Context, env *tool.Env, in lookupIn) (*tool.Result, error) {
	country := strings.ToLower(strings.TrimSpace(in.Country))
	if country == "" {
		country = "us"
	}
	if !countryRE.MatchString(country) {
		return nil, invalid("country must be a two-letter storefront code such as us or se")
	}
	limit := in.Limit
	switch {
	case limit == 0:
		limit = 50
	case limit < 1 || limit > 200:
		return nil, invalid("limit must be 1-200")
	}
	term := strings.TrimSpace(in.Term)
	ref := strings.TrimSpace(in.App)
	if ref == "" && term == "" {
		ref = env.Profile.DefaultApp
	}
	if ref == "" && term == "" {
		return nil, invalid("set app (App Store id or bundle id) and/or term")
	}
	out := lookupOut{Country: country}
	if ref != "" {
		q := url.Values{"country": {country}, "entity": {"software"}}
		switch {
		case trackRE.MatchString(ref):
			q.Set("id", ref)
		case bundleRE.MatchString(ref):
			q.Set("bundleId", ref)
		default:
			return nil, invalid("app must be a numeric App Store id or a bundle id; use apps_list to find them")
		}
		doc, err := fetch(ctx, env, "/lookup", q)
		if err != nil {
			return nil, err
		}
		if len(doc.Results) == 0 {
			out.Note = "not on the " + strings.ToUpper(country) + " App Store (unreleased, removed or not available in this storefront)"
		} else {
			out.App = toListing(doc.Results[0])
		}
	}
	if term != "" {
		doc, err := fetch(ctx, env, "/search", url.Values{
			"term": {term}, "entity": {"software"}, "country": {country}, "limit": {strconv.Itoa(limit)},
		})
		if err != nil {
			return nil, err
		}
		out.Term, out.Searched = term, len(doc.Results)
		top := 10
		if ref == "" {
			top = limit
		}
		for i, a := range doc.Results {
			if ref != "" && out.Rank == nil && a.is(ref) {
				r := i + 1
				out.Rank = &r
			}
			if i < top {
				out.Top = append(out.Top, hit{
					Rank: i + 1, ID: a.TrackID, BundleID: a.BundleID, Name: tool.Untrusted(a.TrackName),
					Seller: tool.Untrusted(a.SellerName), Rating: a.Rating, RatingCount: a.RatingCount,
				})
			}
		}
		if ref != "" && out.Rank == nil {
			out.Note = strings.TrimSpace(out.Note + fmt.Sprintf(" not in the top %d for %q", out.Searched, term))
		}
	}
	return tool.OK(out), nil
}
