package webhooks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// eventTypes is WebhookEventType of App Store Connect spec 4.5.1.
var eventTypes = []any{
	"ALTERNATIVE_DISTRIBUTION_PACKAGE_AVAILABLE_UPDATED", "ALTERNATIVE_DISTRIBUTION_PACKAGE_VERSION_CREATED",
	"ALTERNATIVE_DISTRIBUTION_TERRITORY_AVAILABILITY_UPDATED", "APP_STORE_VERSION_APP_VERSION_STATE_UPDATED",
	"BACKGROUND_ASSET_VERSION_APP_STORE_RELEASE_STATE_UPDATED", "BACKGROUND_ASSET_VERSION_EXTERNAL_BETA_RELEASE_STATE_UPDATED",
	"BACKGROUND_ASSET_VERSION_INTERNAL_BETA_RELEASE_CREATED", "BACKGROUND_ASSET_VERSION_STATE_UPDATED",
	"BETA_FEEDBACK_CRASH_SUBMISSION_CREATED", "BETA_FEEDBACK_SCREENSHOT_SUBMISSION_CREATED",
	"BUILD_BETA_DETAIL_EXTERNAL_BUILD_STATE_UPDATED", "BUILD_UPLOAD_STATE_UPDATED",
}

var (
	webhookFields  = []string{"enabled", "eventTypes", "name", "url"}
	deliveryFields = "createdDate,deliveryState,errorMessage,redelivery,sentDate,response"
)

// deliveriesShown is the number of recent deliveries listed per webhook.
const deliveriesShown = "10"

// secretsDir is where generated webhook secrets are written (confined files).
const secretsDir = "webhook-secrets"

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "webhooks_list", Toolset: tool.Webhooks, Class: tool.Read, Idempotent: true,
		Title: "List webhooks",
		Description: "An app's App Store Connect webhooks: name, URL host, event types, enabled, and the last 10 " +
			"deliveries with state and HTTP status. Use before webhooks_upsert/webhooks_delete. Full URLs and secrets are never shown.",
	}, list))
	tool.Register(tool.New(tool.Meta{
		Name: "webhooks_upsert", Toolset: tool.Webhooks, Class: tool.Destructive, Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Create or update a webhook",
		Description: "Create a webhook (name, https url, event_types) or update one matched by id or name; optional ping. " +
			"Without a secret one is generated into webhook-secrets/<id>.txt (path returned, never the value). " +
			"Needs confirm:true and human approval.",
		Enums: map[string][]any{"event_types": eventTypes},
	}, upsert))
	tool.Register(tool.New(tool.Meta{
		Name: "webhooks_delete", Toolset: tool.Webhooks, Class: tool.Destructive, Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Delete a webhook",
		Description: "Delete an app's webhook by id or name; deliveries stop immediately and the secret is lost. " +
			"Needs confirm:true and human approval.",
	}, deleteWebhook))
}

// hook is a webhook without its secret.
type hook struct {
	ID         string         `json:"id"`
	Name       tool.Untrusted `json:"name"`
	URLHost    string         `json:"url_host"`
	EventTypes []string       `json:"event_types"`
	Enabled    bool           `json:"enabled"`
	Deliveries []delivery     `json:"recent_deliveries,omitempty"`

	url string
}

// delivery is one webhook delivery attempt.
type delivery struct {
	ID         string         `json:"id"`
	State      string         `json:"state"`
	HTTPStatus int            `json:"http_status,omitempty"`
	Created    string         `json:"created,omitempty"`
	Sent       string         `json:"sent,omitempty"`
	Redelivery bool           `json:"redelivery,omitempty"`
	Error      tool.Untrusted `json:"error,omitempty"`
}

// host returns the host of raw, or "" when unparsable.
func host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// showHost renders a host for an approval form: a non-ASCII host (a possible
// look-alike such as Cyrillic "аpple.com") is shown escaped, never as glyphs.
func showHost(h string) string {
	for i := range len(h) {
		if h[i] >= 0x80 {
			return strconv.QuoteToASCII(h)
		}
	}
	return h
}

// hostWarnings returns a fixed note per non-ASCII host, for PlanPreview.Warnings.
func hostWarnings(hosts ...string) []string {
	var w []string
	for _, h := range hosts {
		if s := showHost(h); s != h {
			w = append(w, "non-ASCII host "+s+": check it is the domain you expect, not a look-alike")
		}
	}
	return w
}

// checkURL enforces an absolute https URL without credentials.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return tool.InvalidArgs("url: %v", err)
	case u.Scheme != "https" || u.Host == "":
		return tool.InvalidArgs("url must be an absolute https:// URL")
	case u.User != nil:
		return tool.InvalidArgs("url must not contain credentials")
	}
	return nil
}

func toHook(r tool.Resource) hook {
	h := hook{ID: r.ID, Name: r.Text("name"), url: r.Str("url"), EventTypes: []string{}}
	h.URLHost = host(h.url)
	h.Enabled, _ = r.Bool("enabled")
	if evs, ok := r.Attributes["eventTypes"].([]any); ok {
		for _, e := range evs {
			if s, ok := e.(string); ok {
				h.EventTypes = append(h.EventTypes, s)
			}
		}
	}
	slices.Sort(h.EventTypes)
	return h
}

// appHooks lists the webhooks of an app.
func appHooks(ctx context.Context, env *tool.Env, appID string) ([]hook, error) {
	res, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/webhooks", tool.ListOptions{
		Fields: map[string][]string{"webhooks": webhookFields},
	})
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	hooks := make([]hook, 0, len(res.Data))
	for _, r := range res.Data {
		hooks = append(hooks, toHook(r))
	}
	return hooks, nil
}

// find matches ref against hook ids, then names (case-insensitive).
func find(hooks []hook, ref string) (hook, error) {
	for _, h := range hooks {
		if h.ID == ref {
			return h, nil
		}
	}
	var hits []hook
	for _, h := range hooks {
		if strings.EqualFold(string(h.Name), ref) {
			hits = append(hits, h)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return hook{}, &tool.ErrNotFound{Kind: "webhook", Ref: ref, Next: []tool.NextStep{{Tool: "webhooks_list"}}}
	}
	amb := &tool.ErrAmbiguous{Kind: "webhook", Ref: ref}
	for _, h := range hits {
		amb.Candidates = append(amb.Candidates, tool.Candidate{ID: h.ID, Label: tool.SanitizeLine(string(h.Name), 80) + " (" + showHost(h.URLHost) + ")"})
	}
	return hook{}, amb
}

type listIn struct {
	App tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
}

type listOut struct {
	App      string `json:"app_id"`
	Webhooks []hook `json:"webhooks"`
}

func list(ctx context.Context, env *tool.Env, in listIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	hooks, err := appHooks(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	for i := range hooks {
		res, err := env.ASC.Get(ctx, "/v1/webhooks/"+hooks[i].ID+"/deliveries", url.Values{
			"fields[webhookDeliveries]": {deliveryFields},
			"limit":                     {deliveriesShown},
		})
		if err != nil {
			return nil, fmt.Errorf("list deliveries of webhook %s: %w", hooks[i].ID, err)
		}
		for _, r := range res.Data {
			d := delivery{ID: r.ID, State: r.Str("deliveryState"), Created: r.Str("createdDate"), Sent: r.Str("sentDate")}
			d.Redelivery, _ = r.Bool("redelivery")
			d.Error = tool.Untrusted(r.Str("errorMessage"))
			if resp, ok := r.Attributes["response"].(map[string]any); ok {
				if code, ok := resp["httpStatusCode"].(float64); ok {
					d.HTTPStatus = int(code)
				}
			}
			hooks[i].Deliveries = append(hooks[i].Deliveries, d)
		}
	}
	return tool.OK(listOut{App: app.ID, Webhooks: hooks}), nil
}

type upsertIn struct {
	tool.ConfirmFlag
	App        tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Webhook    string      `json:"webhook,omitempty" jsonschema:"id or name of an existing webhook to update; default: match by name, else create"`
	Name       string      `json:"name,omitempty" jsonschema:"webhook name (required to create)"`
	URL        string      `json:"url,omitempty" jsonschema:"https endpoint (required to create)"`
	EventTypes []string    `json:"event_types,omitempty" jsonschema:"events to deliver (required to create)"`
	Enabled    *bool       `json:"enabled,omitempty" jsonschema:"default true on create, unchanged on update"`
	Secret     string      `json:"secret,omitempty" jsonschema:"signing secret; omit to generate one on create (written to webhook-secrets/), set to rotate on update"`
	Ping       bool        `json:"ping,omitempty" jsonschema:"send a test ping after saving"`
}

type upsertOut struct {
	Action  string `json:"action"`
	Webhook hook   `json:"webhook"`
	// SecretFile is the confined file holding a generated secret; the value
	// itself never reaches the agent.
	SecretFile  string `json:"secret_file,omitempty"`
	SecretNote  string `json:"secret_note,omitempty"`
	PingID      string `json:"ping_id,omitempty"`
	PingWarning string `json:"ping_warning,omitempty"`
}

// change is the attribute set a create or update would send (secret masked).
type change struct {
	Name       string   `json:"name,omitempty"`
	URL        string   `json:"url,omitempty"`
	EventTypes []string `json:"event_types,omitempty"`
	Enabled    *bool    `json:"enabled,omitempty"`
	Secret     string   `json:"secret,omitempty"`
	Ping       bool     `json:"ping,omitempty"`
}

func upsert(ctx context.Context, env *tool.Env, in upsertIn) (*tool.Result, error) {
	if in.URL != "" {
		if err := checkURL(in.URL); err != nil {
			return nil, err
		}
	}
	events := slices.Clone(in.EventTypes)
	slices.Sort(events)
	events = slices.Compact(events)
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	hooks, err := appHooks(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	var cur *hook
	switch {
	case in.Webhook != "":
		h, err := find(hooks, in.Webhook)
		if err != nil {
			return nil, err
		}
		cur = &h
	case in.Name != "":
		if h, err := find(hooks, in.Name); err == nil {
			cur = &h
		} else if !errors.As(err, new(*tool.ErrNotFound)) {
			return nil, err
		}
	}
	if cur == nil {
		return create(ctx, env, app, in, events)
	}
	return update(ctx, env, *cur, in, events)
}

func create(ctx context.Context, env *tool.Env, app tool.App, in upsertIn, events []string) (*tool.Result, error) {
	switch {
	case in.Name == "":
		return nil, tool.InvalidArgs("name is required to create a webhook")
	case in.URL == "":
		return nil, tool.InvalidArgs("url is required to create a webhook")
	case len(events) == 0:
		return nil, tool.InvalidArgs("event_types is required to create a webhook")
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	secretNote := "provided (hidden)"
	if in.Secret == "" {
		secretNote = "generated by app-store-connect-mcp, written to " + secretsDir + "/<webhook-id>.txt"
		// A tampered ignore file must stop the call before anything is approved.
		if err := checkSecretsIgnore(env.Files); err != nil {
			return nil, err
		}
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
		Summary:  fmt.Sprintf("create webhook %q for %s (%s) posting to host %s", in.Name, app.Name, app.ID, showHost(host(in.URL))),
		Intended: change{Name: in.Name, URL: showHost(in.URL), EventTypes: events, Enabled: &enabled, Secret: secretNote, Ping: in.Ping},
		Affected: []string{showHost(in.URL)},
		Warnings: hostWarnings(host(in.URL)),
	})
	if res != nil {
		return res, nil
	}
	secret, pending := in.Secret, ""
	if secret == "" {
		// The secret is on disk before Apple stores it, so a lost response
		// never leaves a webhook whose secret nobody has.
		if err := ensureSecretsIgnored(env.Files); err != nil {
			if errors.Is(err, tool.ErrInvalidArgs) {
				return nil, err
			}
			return nil, tool.InvalidArgs("pass secret, or open a project root (%v)", err)
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate secret: %w", err)
		}
		secret = hex.EncodeToString(b)
		sum := sha256.Sum256([]byte(secret))
		pending = secretsDir + "/pending-" + hex.EncodeToString(sum[:6]) + ".txt"
		if err := env.Files.WriteFile(pending, []byte(secret+"\n"), 0o600); err != nil {
			return nil, tool.InvalidArgs("pass secret, or open a project root (write %s: %v)", pending, err)
		}
	}
	resp, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodPost, Path: "/v1/webhooks", Write: true, Body: map[string]any{
		"data": map[string]any{
			"type": "webhooks",
			"attributes": map[string]any{
				"name": in.Name, "url": in.URL, "eventTypes": events, "enabled": enabled, "secret": secret,
			},
			"relationships": map[string]any{"app": map[string]any{"data": map[string]any{"type": "apps", "id": app.ID}}},
		},
	}})
	if err != nil {
		return pendingFailure(env.Files, pending, fmt.Errorf("create webhook: %w", err)), nil
	}
	r, ok := resp.First()
	if !ok {
		return pendingFailure(env.Files, pending, fmt.Errorf("create webhook: %w: empty response", tool.ErrOutcomeUnknown)), nil
	}
	out := upsertOut{Action: "created", Webhook: toHook(r)}
	if pending != "" {
		name := secretsDir + "/" + r.ID + ".txt"
		if err := env.Files.Rename(pending, name); err != nil {
			return nil, fmt.Errorf("webhook %s created; its generated secret is in %s but could not be renamed (%w)", r.ID, pending, err)
		}
		if abs, err := env.Files.Resolve(name); err == nil {
			name = abs
		}
		out.SecretFile = name
		out.SecretNote = "sensitive: configure your receiver with this file's secret to verify X-Apple-Signature; Apple never returns it"
	}
	ping(wctx, env, in.Ping, &out)
	return tool.OK(out), nil
}

// pendingFailure is the error result of a failed create. A generated secret
// stays in its pending file, which the hint names: if the webhook turns out
// to exist, that file holds the secret Apple stored.
func pendingFailure(files tool.Files, pending string, err error) *tool.Result {
	res := tool.Fail(err)
	if pending == "" {
		return res
	}
	if abs, rerr := files.Resolve(pending); rerr == nil {
		pending = abs
	}
	b := tool.BodyFor(err)
	b.Hint = fmt.Sprintf("the generated secret is kept in %s; read the app's webhooks back: if this one exists, rename that file to <webhook-id>.txt, otherwise delete it. %s", pending, b.Hint)
	res.Data = b
	return res
}

// secretsIgnore is the only accepted content of webhook-secrets/.gitignore.
const secretsIgnore = "*\n"

// checkSecretsIgnore fails with invalid_arguments when webhook-secrets/.gitignore
// exists with any content but "*\n" (e.g. a "!*" that would let secrets be
// committed). A missing file is fine; it is written before the secret.
func checkSecretsIgnore(files tool.Files) error {
	name := secretsDir + "/.gitignore"
	b, err := files.ReadFile(name)
	if err != nil {
		return nil //nolint:nilerr // missing or unreadable: ensureSecretsIgnored decides after confirm
	}
	if string(b) != secretsIgnore {
		return tool.InvalidArgs("%s must contain exactly %q so generated secrets are never committed; fix or delete it, or pass secret", name, secretsIgnore)
	}
	return nil
}

// ensureSecretsIgnored writes webhook-secrets/.gitignore ("*") so secrets
// are never committed; it also proves the directory is writable. An existing
// file with other content fails with invalid_arguments.
func ensureSecretsIgnored(files tool.Files) error {
	name := secretsDir + "/.gitignore"
	_, err := files.Stat(name)
	if err == nil {
		return checkSecretsIgnore(files)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check %s: %w", name, err)
	}
	if err := files.WriteFile(name, []byte(secretsIgnore), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func update(ctx context.Context, env *tool.Env, cur hook, in upsertIn, events []string) (*tool.Result, error) {
	attrs := map[string]any{}
	var ch change
	if in.Name != "" && in.Name != string(cur.Name) {
		attrs["name"], ch.Name = in.Name, in.Name
	}
	if in.URL != "" && in.URL != cur.url {
		attrs["url"], ch.URL = in.URL, in.URL
	}
	if len(events) > 0 && !slices.Equal(events, cur.EventTypes) {
		attrs["eventTypes"], ch.EventTypes = events, events
	}
	if in.Enabled != nil && *in.Enabled != cur.Enabled {
		attrs["enabled"], ch.Enabled = *in.Enabled, in.Enabled
	}
	if in.Secret != "" {
		attrs["secret"], ch.Secret = in.Secret, "rotated to the provided value (hidden)"
	}
	if len(attrs) == 0 && !in.Ping {
		return tool.Already(upsertOut{Action: "unchanged", Webhook: cur}), nil
	}
	ch.Ping = in.Ping
	summary := fmt.Sprintf("update webhook %q (%s) posting to %s", cur.Name, cur.ID, showHost(cur.URLHost))
	if ch.URL != "" {
		summary = fmt.Sprintf("update webhook %q (%s), redirecting deliveries from host %s to new host %s", cur.Name, cur.ID, showHost(cur.URLHost), showHost(host(ch.URL)))
	}
	if len(attrs) == 0 {
		summary = fmt.Sprintf("send a test ping to webhook %q (%s) at %s", cur.Name, cur.ID, showHost(cur.URLHost))
	}
	affected := []string{cur.ID}
	var warnings []string
	if ch.URL != "" {
		ch.URL = showHost(ch.URL) // display only: the PATCH sends attrs
		affected = append(affected, ch.URL)
		warnings = hostWarnings(host(in.URL))
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
		Summary:  summary,
		Current:  cur,
		Intended: ch,
		Affected: affected,
		Warnings: warnings,
	})
	if res != nil {
		return res, nil
	}
	out := upsertOut{Action: "pinged", Webhook: cur}
	if len(attrs) > 0 {
		resp, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodPatch, Path: "/v1/webhooks/" + cur.ID, Write: true, Body: map[string]any{
			"data": map[string]any{"type": "webhooks", "id": cur.ID, "attributes": attrs},
		}})
		if err != nil {
			return nil, fmt.Errorf("update webhook %s: %w", cur.ID, err)
		}
		out.Action = "updated"
		if r, ok := resp.First(); ok {
			out.Webhook = toHook(r)
		}
	}
	ping(wctx, env, in.Ping, &out)
	return tool.OK(out), nil
}

// ping sends a test event; a failure is reported, not fatal, because the
// webhook itself was saved.
func ping(wctx context.Context, env *tool.Env, want bool, out *upsertOut) {
	if !want {
		return
	}
	resp, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodPost, Path: "/v1/webhookPings", Write: true, Body: map[string]any{
		"data": map[string]any{
			"type":          "webhookPings",
			"relationships": map[string]any{"webhook": map[string]any{"data": map[string]any{"type": "webhooks", "id": out.Webhook.ID}}},
		},
	}})
	if err != nil {
		out.PingWarning = "ping failed: " + err.Error()
		return
	}
	if r, ok := resp.First(); ok {
		out.PingID = r.ID
	}
}

type deleteIn struct {
	tool.ConfirmFlag
	App     tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Webhook string      `json:"webhook" jsonschema:"webhook id or name"`
}

func deleteWebhook(ctx context.Context, env *tool.Env, in deleteIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	hooks, err := appHooks(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	h, err := find(hooks, in.Webhook)
	if err != nil {
		return nil, err
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
		Summary:      fmt.Sprintf("delete webhook %q (%s) of %s posting to %s", h.Name, h.ID, app.Name, showHost(h.URLHost)),
		Current:      h,
		Affected:     []string{h.ID, showHost(h.URLHost)},
		Irreversible: []string{"deliveries stop immediately", "the webhook and its secret cannot be restored"},
	})
	if res != nil {
		return res, nil
	}
	if _, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodDelete, Path: "/v1/webhooks/" + h.ID, Write: true}); err != nil {
		if tool.APIStatus(err) == http.StatusNotFound {
			return tool.Already(map[string]any{"deleted": h.ID}), nil
		}
		return nil, fmt.Errorf("delete webhook %s: %w", h.ID, err)
	}
	return tool.OK(map[string]any{"deleted": h.ID, "name": h.Name}), nil
}
