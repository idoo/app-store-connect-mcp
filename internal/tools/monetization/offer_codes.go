package monetization

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	offerCodeOneTime = "one_time"
	offerCodeCustom  = "custom"
	// offerCodesDir is where one-time code values are written (confined files).
	offerCodesDir = "offer-codes"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "offer_codes_generate", Toolset: tool.Monetization, Class: tool.Write,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Generate subscription offer codes",
		Description: "Generate one-time codes or a custom code for an offer-code campaign (offers_create kind offer_code); " +
			"needs confirm:true and human approval. One-time code values are written to offer-codes/<batch_id>.csv " +
			"(path + count returned, never the values); batch_id re-downloads them.",
		Enums: map[string][]any{"type": {offerCodeOneTime, offerCodeCustom}, "environment": {"PRODUCTION", "SANDBOX"}},
	}, offerCodesGenerate))
}

type offerCodesIn struct {
	App            tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	OfferCodeID    string      `json:"offer_code_id,omitempty" jsonschema:"subscriptionOfferCodes id (offers_list); or product_id + campaign"`
	ProductID      string      `json:"product_id,omitempty" jsonschema:"subscription product id (with campaign)"`
	Campaign       string      `json:"campaign,omitempty" jsonschema:"offer-code campaign name"`
	Type           string      `json:"type,omitempty" jsonschema:"one_time or custom; omit with batch_id"`
	NumberOfCodes  int         `json:"number_of_codes,omitempty" jsonschema:"one_time: codes to generate; custom: redemption limit"`
	ExpirationDate string      `json:"expiration_date,omitempty" jsonschema:"YYYY-MM-DD; required for one_time"`
	CustomCode     string      `json:"custom_code,omitempty" jsonschema:"custom: the code customers type"`
	Environment    string      `json:"environment,omitempty" jsonschema:"one_time: default PRODUCTION"`
	BatchID        string      `json:"batch_id,omitempty" jsonschema:"existing one-time batch id: only download its values to a file"`
	tool.ConfirmFlag
}

type offerCodesResultOut struct {
	OfferCodeID    string `json:"offer_code_id,omitempty"`
	Type           string `json:"type"`
	ID             string `json:"id"`
	NumberOfCodes  int    `json:"number_of_codes,omitempty"`
	ExpirationDate string `json:"expiration_date,omitempty"`
	CustomCode     string `json:"custom_code,omitempty"`
	Active         *bool  `json:"active,omitempty"`
	Count          int    `json:"count,omitempty"`
	File           string `json:"file,omitempty"`
	ValuesPending  bool   `json:"values_pending,omitempty"`
	Sensitive      string `json:"sensitive,omitempty"`
	Note           string `json:"note,omitempty"`
}

func offerCodesGenerate(ctx context.Context, env *tool.Env, in offerCodesIn) (*tool.Result, error) {
	if in.BatchID != "" {
		out := offerCodesResultOut{Type: offerCodeOneTime, ID: in.BatchID}
		return offerCodeValues(ctx, env, &out)
	}
	if err := offerCodesValidate(in); err != nil {
		return nil, err
	}
	codeID, err := offerCodeCampaign(ctx, env, in)
	if err != nil {
		return nil, err
	}
	out := offerCodesResultOut{OfferCodeID: codeID, Type: in.Type, NumberOfCodes: in.NumberOfCodes, ExpirationDate: in.ExpirationDate}
	rel := map[string]any{"offerCode": offerOne("subscriptionOfferCodes", codeID)}
	preview := func(kind string, attrs map[string]any) tool.PlanPreview {
		return tool.PlanPreview{
			Summary:      fmt.Sprintf("generate %s offer code(s) on campaign %s", kind, codeID),
			Affected:     []string{"POST /v1/" + kind, "subscriptionOfferCodes " + codeID},
			Intended:     attrs,
			Irreversible: []string{"generated codes are redeemable by anyone who obtains them until they expire"},
		}
	}
	if in.Type == offerCodeCustom {
		out.CustomCode = in.CustomCode
		if r, ok, err := offerFindCustom(ctx, env, codeID, in.CustomCode); err != nil || ok {
			if err != nil {
				return nil, err
			}
			offerFillCustom(&out, r)
			return tool.Already(out), nil
		}
		attrs := map[string]any{"customCode": in.CustomCode, "numberOfCodes": in.NumberOfCodes}
		if in.ExpirationDate != "" {
			attrs["expirationDate"] = in.ExpirationDate
		}
		wctx, gres := tool.RequireConfirm(ctx, env, in.Confirm, preview("subscriptionOfferCodeCustomCodes", attrs))
		if gres != nil {
			return gres, nil
		}
		r, err := offerPostDoc(wctx, env, "/v1/subscriptionOfferCodeCustomCodes", "subscriptionOfferCodeCustomCodes",
			offerDoc("subscriptionOfferCodeCustomCodes", attrs, rel, nil))
		if err != nil {
			if tool.IsConflict(err) {
				if r, ok, rerr := offerFindCustom(ctx, env, codeID, in.CustomCode); rerr == nil && ok {
					offerFillCustom(&out, r)
					return tool.Already(out), nil
				}
			}
			return nil, fmt.Errorf("create custom code: %w", err)
		}
		offerFillCustom(&out, r)
		return tool.OK(out), nil
	}
	attrs := map[string]any{"numberOfCodes": in.NumberOfCodes, "expirationDate": in.ExpirationDate}
	if in.Environment != "" {
		attrs["environment"] = in.Environment
	}
	batches, err := offerOneTimeBatches(ctx, env, codeID)
	if err != nil {
		return nil, err
	}
	if b, ok := offerRecentBatch(batches, in, env.Clock.Now()); ok {
		// Same count, expiry and environment minted within the hour: most
		// likely an earlier call (or one whose outcome was unknown).
		out.ID = b.ID
		if a, ok := b.Bool("active"); ok {
			out.Active = &a
		}
		return tool.Already(out).Warn(fmt.Sprintf("batch %s with the same count and expiry was created at %s; change number_of_codes or "+
			"expiration_date for a second batch", b.ID, b.Str("createdDate"))).
			Then(tool.NextStep{Tool: "offer_codes_generate", Args: map[string]any{"batch_id": b.ID}}), nil
	}
	p := preview("subscriptionOfferCodeOneTimeUseCodes", attrs)
	p.Current = map[string]any{"existing_one_time_batches": len(batches)}
	wctx, gres := tool.RequireConfirm(ctx, env, in.Confirm, p)
	if gres != nil {
		return gres, nil
	}
	r, err := offerPostDoc(wctx, env, "/v1/subscriptionOfferCodeOneTimeUseCodes", "subscriptionOfferCodeOneTimeUseCodes",
		offerDoc("subscriptionOfferCodeOneTimeUseCodes", attrs, rel, nil))
	if err != nil {
		err = fmt.Errorf("create one-time codes: %w", err)
		if prcUnknown(err) && !errors.Is(err, tool.ErrOutcomeUnknown) {
			// Apple may have minted the batch: never retry blindly. A repeat
			// call finds a batch minted within the hour and returns it.
			err = fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, err)
		}
		return nil, tool.WithNext(err, tool.NextStep{Tool: "offers_list", Args: map[string]any{"product_id": in.ProductID}})
	}
	out.ID = r.ID
	if a, ok := r.Bool("active"); ok {
		out.Active = &a
	}
	return offerCodeValues(ctx, env, &out)
}

// offerOneTimeWindow is how far back a same-shaped batch counts as the
// result of an earlier generate call.
const offerOneTimeWindow = time.Hour

// offerOneTimeBatches lists the one-time code batches of a campaign.
func offerOneTimeBatches(ctx context.Context, env *tool.Env, codeID string) ([]tool.Resource, error) {
	res, err := env.ASC.List(ctx, "/v1/subscriptionOfferCodes/"+url.PathEscape(codeID)+"/oneTimeUseCodes", tool.ListOptions{
		Fields: map[string][]string{"subscriptionOfferCodeOneTimeUseCodes": {"numberOfCodes", "expirationDate", "createdDate", "environment", "active"}},
	})
	if err != nil {
		return nil, fmt.Errorf("list one-time code batches: %w", err)
	}
	return res.Data, nil
}

// offerRecentBatch finds a batch with the requested count, expiry and
// environment created within offerOneTimeWindow of now.
func offerRecentBatch(batches []tool.Resource, in offerCodesIn, now time.Time) (tool.Resource, bool) {
	envName := in.Environment
	if envName == "" {
		envName = "PRODUCTION"
	}
	for _, b := range batches {
		n, _ := b.Int("numberOfCodes")
		if n != in.NumberOfCodes || !strings.HasPrefix(b.Str("expirationDate"), in.ExpirationDate) {
			continue
		}
		if e := b.Str("environment"); e != "" && e != envName {
			continue
		}
		created, err := time.Parse(time.RFC3339, b.Str("createdDate"))
		if err != nil || now.Sub(created) > offerOneTimeWindow {
			continue
		}
		return b, true
	}
	return tool.Resource{}, false
}

// offerCodesValidate checks generate arguments before any request.
func offerCodesValidate(in offerCodesIn) error {
	if in.OfferCodeID == "" && (in.ProductID == "" || in.Campaign == "") {
		return tool.InvalidArgs("pass offer_code_id, or product_id and campaign")
	}
	if in.NumberOfCodes < 1 {
		return tool.InvalidArgs("number_of_codes must be ≥ 1")
	}
	if err := offerDate("expiration_date", in.ExpirationDate); err != nil {
		return err
	}
	switch in.Type {
	case offerCodeOneTime:
		if in.ExpirationDate == "" {
			return tool.InvalidArgs("expiration_date is required for one_time codes")
		}
		if in.CustomCode != "" {
			return tool.InvalidArgs("custom_code applies to type custom only")
		}
	case offerCodeCustom:
		if c := in.CustomCode; c == "" || strings.ContainsAny(c, " \t\r\n") {
			return tool.InvalidArgs("custom_code is required and must not contain whitespace")
		}
		if in.Environment != "" {
			return tool.InvalidArgs("environment applies to one_time codes only")
		}
	default:
		return tool.InvalidArgs("type must be one_time or custom")
	}
	return nil
}

// offerCodeCampaign resolves the offer-code campaign id.
func offerCodeCampaign(ctx context.Context, env *tool.Env, in offerCodesIn) (string, error) {
	if in.OfferCodeID != "" {
		return in.OfferCodeID, nil
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return "", err
	}
	sub, found, err := offerFindSub(ctx, env, app.ID, in.ProductID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", offerSubNotFound(in.ProductID)
	}
	r, _, ok, err := offerExisting(ctx, env, sub.ID, offerKindCode, func(r tool.Resource) bool { return r.Str("name") == in.Campaign }) // trusted: comparison only
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &tool.ErrNotFound{
			Kind: "offer code campaign", Ref: in.Campaign, Hint: "create it with offers_create kind offer_code",
			Next: []tool.NextStep{{Tool: "offers_list", Args: map[string]any{"product_id": in.ProductID}}},
		}
	}
	return r.ID, nil
}

// offerFindCustom looks up a campaign's custom code (case-insensitive).
func offerFindCustom(ctx context.Context, env *tool.Env, codeID, code string) (tool.Resource, bool, error) {
	res, err := env.ASC.List(ctx, "/v1/subscriptionOfferCodes/"+url.PathEscape(codeID)+"/customCodes", tool.ListOptions{
		Fields: map[string][]string{"subscriptionOfferCodeCustomCodes": {"customCode", "numberOfCodes", "expirationDate", "active"}},
	})
	if err != nil {
		return tool.Resource{}, false, fmt.Errorf("list custom codes: %w", err)
	}
	for _, r := range res.Data {
		if strings.EqualFold(r.Str("customCode"), code) {
			return r, true, nil
		}
	}
	return tool.Resource{}, false, nil
}

func offerFillCustom(out *offerCodesResultOut, r tool.Resource) {
	out.ID = r.ID
	if c := r.Str("customCode"); c != "" {
		out.CustomCode = c
	}
	if n, ok := r.Int("numberOfCodes"); ok {
		out.NumberOfCodes = n
	}
	if d := r.Str("expirationDate"); d != "" {
		out.ExpirationDate = d
	}
	if a, ok := r.Bool("active"); ok {
		out.Active = &a
	}
}

// offerCodeValues downloads a one-time batch's code values (CSV) into the
// confined file offer-codes/<batch_id>.csv; the values never reach the agent.
// Apple generates them asynchronously; a 404 or empty file means "not yet".
func offerCodeValues(ctx context.Context, env *tool.Env, out *offerCodesResultOut) (*tool.Result, error) {
	body, _, err := env.ASC.Download(ctx, tool.Request{
		Path: "/v1/subscriptionOfferCodeOneTimeUseCodes/" + url.PathEscape(out.ID) + "/values", Accept: "text/csv",
	})
	var ae *tool.APIError
	if err != nil && (!errors.As(err, &ae) || ae.Status != http.StatusNotFound) {
		return nil, fmt.Errorf("download code values: %w", err)
	}
	codes, perr := offerParseCodes(body)
	if perr != nil {
		return nil, perr
	}
	if len(codes) == 0 {
		out.ValuesPending = true
		return tool.OK(out).Then(tool.NextStep{Tool: "offer_codes_generate", Args: map[string]any{"batch_id": out.ID}}), nil
	}
	if strings.ContainsAny(out.ID, `/\`) || out.ID == "." || out.ID == ".." {
		return nil, tool.InvalidArgs("batch id %q is not a valid file name", out.ID)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"code"})
	for _, c := range codes {
		_ = w.Write([]string{c})
	}
	w.Flush()
	if err := ensureOfferCodesIgnored(env.Files); err != nil {
		return nil, err
	}
	name := offerCodesDir + "/" + out.ID + ".csv"
	if err := env.Files.WriteFile(name, buf.Bytes(), 0o600); err != nil {
		return nil, fmt.Errorf("write code values: %w", err)
	}
	if abs, err := env.Files.Resolve(name); err == nil {
		name = abs
	}
	out.Count = len(codes)
	out.File = name
	out.Sensitive = "the file holds redeemable codes; share it only with intended recipients and never paste the values into chat"
	out.Note = offerCodesDir + "/ is git-ignored"
	return tool.OK(out), nil
}

// ensureOfferCodesIgnored writes offer-codes/.gitignore ("*") before the first
// code file so redeemable codes are never committed by accident.
func ensureOfferCodesIgnored(files tool.Files) error {
	name := offerCodesDir + "/.gitignore"
	_, err := files.Stat(name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check %s: %w", name, err)
	}
	if err := files.WriteFile(name, []byte("*\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// offerParseCodes reads the first column of each CSV record, skipping a
// header row (codes are upper-case; a header has lower-case letters) and
// blanks.
func offerParseCodes(body []byte) ([]string, error) {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	var out []string
	for i := 0; ; i++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode code values: %w", err)
		}
		if len(rec) == 0 {
			continue
		}
		v := strings.TrimSpace(rec[0])
		if v == "" || (i == 0 && v != strings.ToUpper(v)) {
			continue
		}
		out = append(out, v)
	}
}
