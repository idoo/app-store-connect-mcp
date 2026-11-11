package xcodecloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "xcc_build_number", Toolset: tool.Web, Class: tool.Write, Idempotent: true, AvailableReadOnly: true,
		Confirm: tool.ConfirmByFlag,
		Title:   "Xcode Cloud next build number",
		Description: "Next build number of an app's Xcode Cloud product (web session) and the app's highest uploaded build. " +
			"next sets it: preview, then confirm:true. Below the current value or not above the highest upload needs allow_lower and a human.",
		ReadOnlyDescription: "Next build number of an app's Xcode Cloud product (web session) and the app's highest uploaded build. " +
			"Setting it (next) needs writes.",
		Ranges: map[string][2]int{"next": {1, maxBuildNumber}},
	}, buildNumber))
}

// maxBuildNumber is the largest next build number accepted (Apple's own
// limit is unknown; ten digits keep the portal path pattern).
const maxBuildNumber = 999999999

// buildScan is how many of the app's newest uploads are searched for the
// highest build number.
const buildScan = 200

type buildNumberIn struct {
	App        tool.AppRef `json:"app,omitempty" jsonschema:"app whose Xcode Cloud product to use; default: pinned app"`
	Product    string      `json:"product,omitempty" jsonschema:"Xcode Cloud product id, instead of app"`
	Next       int         `json:"next,omitempty" jsonschema:"set the next build number to this"`
	AllowLower bool        `json:"allow_lower,omitempty" jsonschema:"allow a value below the current one or not above the highest uploaded build (needs a human)"`
	tool.ConfirmFlag
}

type productRef struct {
	ID   string         `json:"id"`
	Name tool.Untrusted `json:"name,omitempty"`
}

type uploadedBuild struct {
	ID           string `json:"id"`
	BuildNumber  string `json:"build_number"`
	Version      string `json:"version,omitempty"`
	Platform     string `json:"platform,omitempty"`
	UploadedDate string `json:"uploaded_date,omitempty"`
	number       int
}

type buildNumberOut struct {
	Product  productRef `json:"product"`
	AppID    string     `json:"app_id,omitempty"`
	Next     int        `json:"next_build_number"`
	Previous *int       `json:"previous_next_build_number,omitempty"`
	// Status is "set" after a write, "already" when next equals the
	// current value, empty for a read.
	Status  string         `json:"status,omitempty"`
	Highest *uploadedBuild `json:"highest_uploaded,omitempty"`
	// Scanned is how many of the app's newest builds were searched.
	Scanned int `json:"builds_scanned"`
}

func buildNumber(ctx context.Context, env *tool.Env, in buildNumberIn) (*tool.Result, error) {
	in.Product = strings.TrimSpace(in.Product)
	if in.Product != "" && in.App != "" {
		return nil, tool.InvalidArgs("pass app or product, not both")
	}
	if in.Next < 0 {
		return nil, tool.InvalidArgs("next must be positive")
	}
	prod, appID, err := resolveProduct(ctx, env, in.App, in.Product)
	if err != nil {
		return nil, err
	}
	team, err := ciTeam(ctx, env)
	if err != nil {
		return nil, err
	}
	path := "/ci/api/teams/" + team + "/products/" + prod.ID + "/next-build-number"
	cur, err := readNextBuildNumber(ctx, env, path, prod.ID)
	if err != nil {
		return nil, err
	}
	out := buildNumberOut{Product: prod, AppID: appID, Next: cur}
	if appID != "" {
		if out.Highest, out.Scanned, err = highestUpload(ctx, env, appID); err != nil {
			return nil, err
		}
	}
	read := tool.NextStep{Tool: "xcc_build_number", Args: map[string]any{"product": prod.ID}}
	if in.Next == 0 {
		return readResult(out), nil
	}
	if in.Next == cur {
		out.Status = "already"
		return readResult(out), nil
	}
	floor := cur
	if h := out.Highest; h != nil {
		floor = max(floor, h.number+1)
	}
	var risks []string
	if in.Next < cur {
		risks = append(risks, fmt.Sprintf("%d is below the current next build number %d", in.Next, cur))
	}
	if h := out.Highest; h != nil && in.Next <= h.number {
		risks = append(risks, fmt.Sprintf("%d is not above uploaded build %s; a cloud upload with a used build number fails", in.Next, h.BuildNumber))
	}
	if len(risks) > 0 && !in.AllowLower {
		return nil, tool.WithNext(tool.InvalidArgs("%s; pass next ≥ %d, or allow_lower:true with a human's approval", strings.Join(risks, "; "), floor),
			tool.NextStep{Tool: "xcc_build_number", Args: map[string]any{"product": prod.ID, "next": floor}})
	}
	p := tool.PlanPreview{
		Summary:  fmt.Sprintf("set the next Xcode Cloud build number of product %q from %d to %d", prod.Name, cur, in.Next),
		Affected: []string{"Xcode Cloud product " + prod.ID},
		Warnings: risks,
	}
	var wctx context.Context
	var res *tool.Result
	if len(risks) > 0 {
		wctx, res = tool.RequireHumanConfirm(ctx, env, in.Confirm, p)
	} else {
		// Bound to the current value: a counter that moved since the
		// preview needs a new one.
		wctx, res = tool.RequireConfirmTarget(ctx, env, in.Confirm, map[string]any{"product": prod.ID, "current": cur}, p)
	}
	if res != nil {
		return res, nil
	}
	q := path + "?next_build_number=" + strconv.Itoa(in.Next)
	if err := env.Portal.JSON(wctx, http.MethodPut, q, nil, nil); err != nil {
		err = fmt.Errorf("set next build number of product %s: %w", prod.ID, err)
		var pe *tool.PortalError
		if !errors.As(err, &pe) && !errors.Is(err, tool.ErrWritesDisabled) && !errors.Is(err, tool.ErrWebSessionRequired) {
			// Network failure: the value may have changed; read it back.
			err = tool.WithNext(err, read)
		}
		return nil, err
	}
	got, err := readNextBuildNumber(ctx, env, path, prod.ID)
	if err != nil {
		return nil, tool.WithNext(fmt.Errorf("next build number set to %d, but reading it back failed: %w", in.Next, err), read)
	}
	out.Previous, out.Next, out.Status = &cur, got, "set"
	r := tool.OK(out)
	if got != in.Next {
		r.Warn(fmt.Sprintf("Apple accepted %d but now reports %d", in.Next, got))
	}
	return r, nil
}

// readResult is the read answer, warning when the next cloud build would
// reuse an uploaded build number.
func readResult(out buildNumberOut) *tool.Result {
	r := tool.OK(out)
	if h := out.Highest; h != nil && out.Next <= h.number {
		r.Warn(fmt.Sprintf("the next Xcode Cloud build would get %d, not above uploaded build %s; its upload would fail", out.Next, h.BuildNumber))
		r.Then(tool.NextStep{Tool: "xcc_build_number", Args: map[string]any{"product": out.Product.ID, "next": h.number + 1}})
	}
	return r
}

// resolveProduct finds the Xcode Cloud product by id, or the only product
// of the app (default: pinned app), and returns it with its app id.
func resolveProduct(ctx context.Context, env *tool.Env, app tool.AppRef, id string) (productRef, string, error) {
	next := []tool.NextStep{{Tool: "xcc_workflows_list"}}
	if id != "" {
		if !uuidRE.MatchString(id) {
			return productRef{}, "", tool.InvalidArgs("product must be an Xcode Cloud product id (UUID)")
		}
		resp, err := env.ASC.Get(ctx, "/v1/ciProducts/"+url.PathEscape(id), url.Values{
			"fields[ciProducts]": {"name,app"},
			"include":            {"app"},
		})
		if tool.APIStatus(err) == http.StatusNotFound {
			return productRef{}, "", &tool.ErrNotFound{Kind: "Xcode Cloud product", Ref: id, Next: next}
		}
		if err != nil {
			return productRef{}, "", fmt.Errorf("read Xcode Cloud product %s: %w", id, err)
		}
		r, ok := resp.First()
		if !ok {
			return productRef{}, "", &tool.ErrNotFound{Kind: "Xcode Cloud product", Ref: id, Next: next}
		}
		return productRef{ID: r.ID, Name: r.Text("name")}, r.RelID("app"), nil
	}
	products, appID, err := listProducts(ctx, env, app)
	if err != nil {
		return productRef{}, "", err
	}
	switch len(products) {
	case 0:
		return productRef{}, "", &tool.ErrNotFound{
			Kind: "Xcode Cloud product", Ref: string(app),
			Hint: "the app has no Xcode Cloud product; set one up in Xcode first", Next: next,
		}
	case 1:
		p := products[0]
		if appID == "" {
			appID = p.RelID("app")
		}
		return productRef{ID: p.ID, Name: p.Text("name")}, appID, nil
	}
	amb := &tool.ErrAmbiguous{Kind: "Xcode Cloud product", Ref: string(app), Total: len(products)}
	if amb.Ref == "" {
		amb.Ref = "(any)"
	}
	for i, p := range products {
		if i == 10 {
			break
		}
		amb.Candidates = append(amb.Candidates, tool.Candidate{ID: p.ID, Label: fmt.Sprintf("%q", p.Text("name"))})
		if i < 5 {
			amb.Next = append(amb.Next, tool.NextStep{Tool: "xcc_build_number", Args: map[string]any{"product": p.ID}})
		}
	}
	return productRef{}, "", amb
}

// ciTeam returns the team id the Xcode Cloud web API is keyed by: the
// public provider id (UUID) of the web session's team.
func ciTeam(ctx context.Context, env *tool.Env) (string, error) {
	return tool.PublicProviderID(ctx, env.Portal)
}

// readNextBuildNumber GETs the product's next build number.
func readNextBuildNumber(ctx context.Context, env *tool.Env, path, productID string) (int, error) {
	var v struct {
		Next *int `json:"next_build_number"`
	}
	err := env.Portal.JSON(ctx, http.MethodGet, path, nil, &v)
	var pe *tool.PortalError
	if errors.As(err, &pe) && pe.Status == http.StatusNotFound {
		return 0, &tool.ErrNotFound{
			Kind: "Xcode Cloud product", Ref: productID,
			Hint: "not in the web session's team; switch it with `app-store-connect-mcp web team`",
		}
	}
	if err != nil {
		return 0, fmt.Errorf("read next build number of product %s: %w", productID, err)
	}
	if v.Next == nil {
		return 0, fmt.Errorf("read next build number of product %s: answer has no next_build_number", productID)
	}
	return *v.Next, nil
}

// highestUpload returns the uploaded build with the highest build number
// among the app's buildScan newest (all platforms, expired included) and
// how many were scanned. Build numbers compare by their first numeric
// component ("66", "66.1" → 66); others are skipped.
func highestUpload(ctx context.Context, env *tool.Env, appID string) (*uploadedBuild, int, error) {
	resp, err := env.ASC.Get(ctx, "/v1/builds", url.Values{
		"filter[app]":                {appID},
		"sort":                       {"-uploadedDate"},
		"limit":                      {strconv.Itoa(buildScan)},
		"fields[builds]":             {"version,uploadedDate,preReleaseVersion"},
		"include":                    {"preReleaseVersion"},
		"fields[preReleaseVersions]": {"version,platform"},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list builds: %w", err)
	}
	inc := tool.IndexIncluded(resp.Included)
	var best *uploadedBuild
	for _, r := range resp.Data {
		num := r.Str("version")
		n, err := strconv.Atoi(strings.SplitN(num, ".", 2)[0])
		if err != nil || (best != nil && n <= best.number) {
			continue
		}
		b := &uploadedBuild{ID: r.ID, BuildNumber: num, UploadedDate: r.Str("uploadedDate"), number: n}
		if pv, ok := inc["preReleaseVersions/"+r.RelID("preReleaseVersion")]; ok {
			b.Version, b.Platform = pv.Str("version"), pv.Str("platform")
		}
		best = b
	}
	return best, len(resp.Data), nil
}
