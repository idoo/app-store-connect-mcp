package xcodecloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// Sparse fieldsets.
var (
	workflowFields = strings.Join([]string{
		"name", "description", "branchStartCondition", "tagStartCondition", "pullRequestStartCondition",
		"scheduledStartCondition", "manualBranchStartCondition", "manualTagStartCondition",
		"manualPullRequestStartCondition", "actions", "isEnabled", "isLockedForEditing", "clean", "product", "repository",
	}, ",")
	repoFields = "ownerName,repositoryName"
	runFields  = strings.Join([]string{
		"number", "createdDate", "startedDate", "finishedDate", "sourceCommit", "isPullRequestBuild", "issueCounts",
		"executionProgress", "completionStatus", "startReason", "cancelReason", "workflow", "sourceBranchOrTag", "pullRequest",
	}, ",")
)

// uuidRE matches Xcode Cloud resource ids.
var uuidRE = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// repoOut is a workflow's source repository.
type repoOut struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// workflowOut is a compact Xcode Cloud workflow.
type workflowOut struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	Locked          bool     `json:"locked,omitempty"`
	Clean           bool     `json:"clean,omitempty"`
	ProductID       string   `json:"product_id,omitempty"`
	Repository      *repoOut `json:"repository,omitempty"`
	StartConditions []string `json:"start_conditions"`
	Actions         []string `json:"actions,omitempty"`
}

// productOut is a compact Xcode Cloud product with its workflows.
type productOut struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Type      string        `json:"type,omitempty"`
	AppID     string        `json:"app_id,omitempty"`
	Workflows []workflowOut `json:"workflows"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{tool.ErrInvalidArgs}, args...)...)
}

func apiStatus(err error) int {
	var ae *tool.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func indexIncluded(inc []tool.Resource) map[string]tool.Resource {
	m := make(map[string]tool.Resource, len(inc))
	for _, r := range inc {
		m[r.Type+"/"+r.ID] = r
	}
	return m
}

// flattenWorkflow builds a workflowOut from a ciWorkflows resource.
func flattenWorkflow(r tool.Resource, inc map[string]tool.Resource) workflowOut {
	w := workflowOut{ID: r.ID, Name: r.Str("name"), ProductID: r.RelID("product"), StartConditions: startConditions(r)}
	w.Enabled, _ = r.Bool("isEnabled")
	w.Locked, _ = r.Bool("isLockedForEditing")
	w.Clean, _ = r.Bool("clean")
	if id := r.RelID("repository"); id != "" {
		w.Repository = &repoOut{ID: id}
		if repo, ok := inc["scmRepositories/"+id]; ok {
			w.Repository.Name = strings.Trim(repo.Str("ownerName")+"/"+repo.Str("repositoryName"), "/")
		}
	}
	acts, _ := tool.Unwrap(r.Attributes["actions"]).([]any)
	for _, a := range acts {
		am, _ := a.(map[string]any)
		typ, _ := am["actionType"].(string)
		name, _ := am["name"].(string)
		w.Actions = append(w.Actions, strings.TrimSpace(typ+" "+name))
	}
	return w
}

// startConditions summarizes the start conditions of a workflow, e.g.
// "branch: main, release/*" or "pull_request: any → main".
func startConditions(r tool.Resource) []string {
	out := []string{}
	add := func(attr, label string) {
		c, ok := tool.Unwrap(r.Attributes[attr]).(map[string]any)
		if !ok {
			return
		}
		s := label + ": " + patterns(c["source"])
		if dst, ok := c["destination"]; ok {
			s += " → " + patterns(dst)
		}
		if sch, ok := c["schedule"].(map[string]any); ok {
			if f, _ := sch["frequency"].(string); f != "" {
				s += " (" + f + ")"
			}
		}
		out = append(out, s)
	}
	add("branchStartCondition", "branch")
	add("tagStartCondition", "tag")
	add("pullRequestStartCondition", "pull_request")
	add("scheduledStartCondition", "scheduled")
	add("manualBranchStartCondition", "manual branch")
	add("manualTagStartCondition", "manual tag")
	add("manualPullRequestStartCondition", "manual pull_request")
	return out
}

// patterns renders CiBranchPatterns / CiTagPatterns.
func patterns(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return "any"
	}
	if all, _ := m["isAllMatch"].(bool); all {
		return "any"
	}
	list, _ := m["patterns"].([]any)
	parts := make([]string, 0, len(list))
	for _, p := range list {
		pm, _ := p.(map[string]any)
		s, _ := pm["pattern"].(string)
		if prefix, _ := pm["isPrefix"].(bool); prefix {
			s += "*"
		}
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "any"
	}
	return strings.Join(parts, ", ")
}

// listProducts returns the Xcode Cloud products of the app (or of every app
// when no app is given and none is pinned).
func listProducts(ctx context.Context, env *tool.Env, app tool.AppRef) ([]tool.Resource, string, error) {
	q := url.Values{"fields[ciProducts]": {"name,productType,app"}}
	appID := ""
	if app != "" || env.Profile.DefaultApp != "" {
		a, err := env.Resolve.App(ctx, app)
		if err != nil {
			return nil, "", err
		}
		appID = a.ID
		q.Set("filter[app]", a.ID)
	}
	res, err := env.ASC.List(ctx, "/v1/ciProducts", tool.ListOptions{Query: q, MaxItems: 200})
	if err != nil {
		return nil, "", fmt.Errorf("list Xcode Cloud products: %w", err)
	}
	return res.Data, appID, nil
}

// listWorkflows returns the workflows of a product.
func listWorkflows(ctx context.Context, env *tool.Env, productID string) ([]workflowOut, error) {
	q := url.Values{
		"fields[ciWorkflows]":     {workflowFields},
		"fields[scmRepositories]": {repoFields},
		"include":                 {"repository"},
	}
	res, err := env.ASC.List(ctx, "/v1/ciProducts/"+url.PathEscape(productID)+"/workflows", tool.ListOptions{Query: q, MaxItems: 200})
	if err != nil {
		return nil, fmt.Errorf("list workflows of product %s: %w", productID, err)
	}
	inc := indexIncluded(res.Included)
	out := make([]workflowOut, 0, len(res.Data))
	for _, r := range res.Data {
		w := flattenWorkflow(r, inc)
		if w.ProductID == "" {
			w.ProductID = productID
		}
		out = append(out, w)
	}
	return out, nil
}

// fanOut is how many Xcode Cloud reads run at once (the client's in-flight
// limit per profile).
const fanOut = 4

// productWorkflows lists the workflows of each product, fanOut at a time,
// in product order.
func productWorkflows(ctx context.Context, env *tool.Env, products []tool.Resource) ([][]workflowOut, error) {
	out := make([][]workflowOut, len(products))
	err := tool.Parallel(ctx, fanOut, products, func(ctx context.Context, i int, p tool.Resource) error {
		ws, err := listWorkflows(ctx, env, p.ID)
		out[i] = ws
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveWorkflow finds a workflow by id or by exact (case-insensitive) name
// among the app's products. An empty ref picks the only workflow there is.
// Ambiguity lists candidates labelled with their product (app) name and a
// nextTool call per candidate.
func resolveWorkflow(ctx context.Context, env *tool.Env, app tool.AppRef, ref, nextTool string) (workflowOut, error) {
	ref = strings.TrimSpace(ref)
	next := []tool.NextStep{{Tool: "xcc_workflows_list"}}
	if uuidRE.MatchString(ref) {
		resp, err := env.ASC.Get(ctx, "/v1/ciWorkflows/"+url.PathEscape(ref), url.Values{
			"fields[ciWorkflows]":     {workflowFields},
			"fields[scmRepositories]": {repoFields},
			"include":                 {"repository"},
		})
		if apiStatus(err) == http.StatusNotFound {
			return workflowOut{}, &tool.ErrNotFound{Kind: "workflow", Ref: ref, Next: next}
		}
		if err != nil {
			return workflowOut{}, fmt.Errorf("read workflow %s: %w", ref, err)
		}
		r, ok := resp.First()
		if !ok {
			return workflowOut{}, &tool.ErrNotFound{Kind: "workflow", Ref: ref, Next: next}
		}
		return flattenWorkflow(r, indexIncluded(resp.Included)), nil
	}
	products, _, err := listProducts(ctx, env, app)
	if err != nil {
		return workflowOut{}, err
	}
	if ref == "" && len(products) > maxProducts {
		return workflowOut{}, invalid("%d Xcode Cloud products; pass app or the workflow id", len(products))
	}
	lists, err := productWorkflows(ctx, env, products)
	if err != nil {
		return workflowOut{}, err
	}
	var hits []workflowOut
	var labels []string
	for i, ws := range lists {
		for _, w := range ws {
			if ref == "" || strings.EqualFold(w.Name, ref) {
				hits = append(hits, w)
				labels = append(labels, w.Name+" ("+products[i].Str("name")+")")
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		if ref == "" {
			return workflowOut{}, &tool.ErrNotFound{Kind: "workflow", Hint: "no Xcode Cloud workflow found; set up Xcode Cloud for the app in Xcode first", Next: next}
		}
		return workflowOut{}, &tool.ErrNotFound{Kind: "workflow", Ref: ref, Hint: "pass the exact workflow name or its id", Next: next}
	}
	if ref == "" {
		ref = "(any)"
	}
	amb := &tool.ErrAmbiguous{Kind: "workflow", Ref: ref}
	for i, w := range hits {
		amb.Candidates = append(amb.Candidates, tool.Candidate{ID: w.ID, Label: labels[i]})
		if i < 5 {
			amb.Next = append(amb.Next, tool.NextStep{Tool: nextTool, Args: map[string]any{"workflow": w.ID}})
		}
	}
	return workflowOut{}, amb
}
