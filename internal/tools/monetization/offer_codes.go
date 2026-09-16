package monetization

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/idoo/hamn/internal/tool"
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
	App            tool.AppRef `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
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
			if s12aConflict(err) {
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
	wctx, gres := tool.RequireConfirm(ctx, env, in.Confirm, preview("subscriptionOfferCodeOneTimeUseCodes", attrs))
	if gres != nil {
		return gres, nil
	}
	r, err := offerPostDoc(wctx, env, "/v1/subscriptionOfferCodeOneTimeUseCodes", "subscriptionOfferCodeOneTimeUseCodes",
		offerDoc("subscriptionOfferCodeOneTimeUseCodes", attrs, rel, nil))
	if err != nil {
		return nil, tool.WithNext(fmt.Errorf("create one-time codes: %w", err),
			tool.NextStep{Tool: "offers_list", Args: map[string]any{"product_id": in.ProductID}})
	}
	out.ID = r.ID
	if a, ok := r.Bool("active"); ok {
		out.Active = &a
	}
	return offerCodeValues(ctx, env, &out)
}

// offerCodesValidate checks generate arguments before any request.
func offerCodesValidate(in offerCodesIn) error {
	if in.OfferCodeID == "" && (in.ProductID == "" || in.Campaign == "") {
		return s12aInvalid("pass offer_code_id, or product_id and campaign")
	}
	if in.NumberOfCodes < 1 {
		return s12aInvalid("number_of_codes must be ≥ 1")
	}
	if err := offerDate("expiration_date", in.ExpirationDate); err != nil {
		return err
	}
	switch in.Type {
	case offerCodeOneTime:
		if in.ExpirationDate == "" {
			return s12aInvalid("expiration_date is required for one_time codes")
		}
		if in.CustomCode != "" {
			return s12aInvalid("custom_code applies to type custom only")
		}
	case offerCodeCustom:
		if c := in.CustomCode; c == "" || strings.ContainsAny(c, " \t\r\n") {
			return s12aInvalid("custom_code is required and must not contain whitespace")
		}
		if in.Environment != "" {
			return s12aInvalid("environment applies to one_time codes only")
		}
	default:
		return s12aInvalid("type must be one_time or custom")
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
	r, ok, err := offerExisting(ctx, env, sub.ID, offerKindCode, func(r tool.Resource) bool { return r.Str("name") == in.Campaign })
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
		return nil, s12aInvalid("batch id %q is not a valid file name", out.ID)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"code"})
	for _, c := range codes {
		_ = w.Write([]string{c})
	}
	w.Flush()
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
	return tool.OK(out), nil
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
