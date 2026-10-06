// Package tool is the contract every asc-mcp tool is written against: tool
// declaration (Meta, Class, Toolset, ConfirmMode), the registry, the Env of
// dependencies a handler may use (all interfaces, implemented elsewhere),
// result and error helpers, and the confirm/plan gates.
//
// Domain packages (internal/tools/<toolset>) import only this package (plus
// tooltest in tests). They never import the MCP SDK, jsonschema-go or
// internal/asc; depguard enforces it.
//
// # Writing a tool
//
// Declare the input as a plain struct. The JSON schema is inferred: json
// names, `jsonschema:"..."` tags become descriptions, fields without
// omitempty are required. Enums go in Meta.Enums. Register in init:
//
//	type listIn struct {
//		App   tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
//		State string      `json:"state,omitempty"`
//	}
//
//	func init() {
//		tool.Register(tool.New(tool.Meta{
//			Name: "builds_list", Toolset: tool.Release, Class: tool.Read, Idempotent: true,
//			Title: "List builds", Description: "Builds by app, version and processing state.",
//			Enums: map[string][]any{"state": {"PROCESSING", "VALID", "INVALID", "FAILED"}},
//		}, buildsList))
//	}
//
//	func buildsList(ctx context.Context, env *tool.Env, in listIn) (*tool.Result, error) {
//		app, err := env.Resolve.App(ctx, in.App)
//		if err != nil {
//			return nil, err // becomes an error result with candidates/next steps
//		}
//		res, err := env.ASC.List(ctx, "/v1/builds", tool.ListOptions{
//			Query: url.Values{"filter[app]": {app.ID}},
//		})
//		if err != nil {
//			return nil, fmt.Errorf("list builds: %w", err)
//		}
//		return tool.OK(res), nil
//	}
//
// Inside a handler every Env member is non-nil; missing capabilities fail with
// ErrUnavailable. Third-party text (review bodies, tester feedback) must be
// wrapped in [Untrusted]. Idempotent no-ops return [Already]; waits that time
// out return [StillProcessing]; follow-up suggestions go in Result.Next or
// via [WithNext] on errors.
//
// # Safety
//
// Write and Destructive tools are refused when writes are disabled, unless
// Meta.AvailableReadOnly (the tool then restricts itself, see ReadOnlyEnums).
// The write capability ([IsWrite]) that the ASC client requires for every
// non-GET is minted only inside this package:
//
//   - ConfirmNone Write tools receive it before the handler runs.
//   - ConfirmByFlag tools embed [ConfirmFlag] and call [RequireConfirm].
//     Without confirm:true the call returns a preview and changes nothing;
//     with it the human is asked via elicitation; without a human answer it
//     is accepted only after a preview of the same args ([Planner.Claim]).
//   - ConfirmByPlan tools embed [ConfirmPlan] and call [Preview] with a
//     [Fingerprint] of the remote state they depend on. The first call records
//     a plan and returns its plan_id; the second (plan_id:"<plan_id>", same
//     args) is refused if the state changed.
//   - Meta.RequireHuman (with either gate) makes the elicitation accept
//     mandatory; without elicitation the call is refused unless the profile
//     sets AllowNoElicit. Results report _meta.asc-mcp.approved_by.
//   - [Unconfirmed] lets a Flag/Plan tool write on a branch it has determined
//     is purely additive.
//
// A gated handler looks like:
//
//	type deleteIn struct {
//		tool.ConfirmFlag
//		ID string `json:"id"`
//	}
//
//	func deleteWebhook(ctx context.Context, env *tool.Env, in deleteIn) (*tool.Result, error) {
//		wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
//			Summary: "delete webhook " + in.ID, Affected: []string{in.ID},
//			Irreversible: []string{"deliveries stop immediately"},
//		})
//		if res != nil {
//			return res, nil
//		}
//		_, err := env.ASC.Do(wctx, tool.Request{Method: "DELETE", Path: "/v1/webhooks/" + in.ID, Write: true})
//		return tool.OK(map[string]any{"deleted": in.ID}), err
//	}
//
// The approval prompt is rendered by [ApprovalPrompt] from the preview's
// Summary, Intended, Affected and Irreversible fields, never from free text.
//
// # Testing
//
// Package tooltest provides fakes for every Env member and helpers:
//
//	k := tooltest.New(t)
//	k.ASC.HandleFile("GET", "/v1/builds", "testdata/builds.json")
//	res := tooltest.MustOK(t, tooltest.Call(t, myTool, k.Env, `{"app":"123"}`))
//	tooltest.AssertGolden(t, "testdata/golden/builds_list.json", res)
package tool
