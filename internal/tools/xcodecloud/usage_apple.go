package xcodecloud

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// appleUsagePlan is the plan of the usage summary.
type appleUsagePlan struct {
	Name          string `json:"name"`
	Available     int    `json:"available"`
	Used          int    `json:"used"`
	Total         int    `json:"total"`
	ResetDate     string `json:"reset_date"`
	ResetDateTime string `json:"reset_date_time"`
}

// appleUsageShare is one product's or one workflow's share of a days answer.
type appleUsageShare struct {
	ProductID       string `json:"product_id"`
	WorkflowID      string `json:"workflow_id"`
	Minutes         int    `json:"usage_in_minutes"`
	Builds          int    `json:"number_of_builds"`
	PreviousMinutes int    `json:"previous_usage_in_minutes"`
	PreviousBuilds  int    `json:"previous_number_of_builds"`
}

// appleUsageDays is a days answer: the team's carries product_usage, one
// product's carries workflow_usage.
type appleUsageDays struct {
	Usage []struct {
		Date    string `json:"date"`
		Minutes int    `json:"minutes"`
		Builds  int    `json:"number_of_builds"`
	} `json:"usage"`
	ProductUsage  []appleUsageShare `json:"product_usage"`
	WorkflowUsage []appleUsageShare `json:"workflow_usage"`
}

// usageFromApple reads the usage page's numbers through the web session:
// the team's plan, and the window by day and by product, or one product's
// by day and by workflow.
func usageFromApple(ctx context.Context, env *tool.Env, days int, now time.Time, prod *productRef) (usageOut, error) {
	team, err := ciTeam(ctx, env)
	if err != nil {
		return usageOut{}, err
	}
	base := "/ci/api/teams/" + team
	var sum struct {
		Plan appleUsagePlan `json:"plan"`
	}
	if err := env.Portal.JSON(ctx, http.MethodGet, base+"/usage/summary", nil, &sum); err != nil {
		return usageOut{}, fmt.Errorf("read Xcode Cloud usage summary: %w", err)
	}
	from, to := appleUsageWindow(now, days, sum.Plan.ResetDateTime)
	out := usageOut{Source: usageApple, Plan: applePlan(sum.Plan), From: usageDate(from), To: usageDate(to)}
	query := appleDaysQuery(from, to)

	var ans appleUsageDays
	if prod == nil {
		if err := env.Portal.JSON(ctx, http.MethodGet, base+"/usage/days"+query, nil, &ans); err != nil {
			return usageOut{}, fmt.Errorf("read Xcode Cloud usage by day: %w", err)
		}
		res, err := env.ASC.List(ctx, "/v1/ciProducts", tool.ListOptions{
			Query: url.Values{"fields[ciProducts]": {"name"}}, MaxItems: 200,
		})
		if err != nil {
			return usageOut{}, fmt.Errorf("list Xcode Cloud products: %w", err)
		}
		// Apple's two APIs write the same id in either letter case.
		names := make(map[string]tool.Untrusted, len(res.Data))
		for _, r := range res.Data {
			names[strings.ToLower(r.ID)] = r.Text("name")
		}
		for _, p := range ans.ProductUsage {
			out.Products = append(out.Products, usageProduct{
				ID: p.ProductID, Name: names[strings.ToLower(p.ProductID)], Minutes: p.Minutes, Builds: p.Builds,
				PreviousMinutes: p.PreviousMinutes, PreviousBuilds: p.PreviousBuilds,
			})
		}
		slices.SortFunc(out.Products, func(a, b usageProduct) int {
			return cmp.Or(cmp.Compare(b.Minutes, a.Minutes), cmp.Compare(a.ID, b.ID))
		})
	} else {
		err := env.Portal.JSON(ctx, http.MethodGet, base+"/products/"+prod.ID+"/usage/days"+query, nil, &ans)
		var pe *tool.PortalError
		if errors.As(err, &pe) && pe.Status == http.StatusNotFound {
			return usageOut{}, &tool.ErrNotFound{
				Kind: "Xcode Cloud product", Ref: prod.ID,
				Hint: "not in the web session's team; switch it with `app-store-connect-mcp web team`",
			}
		}
		if err != nil {
			return usageOut{}, fmt.Errorf("read Xcode Cloud usage of product %s: %w", prod.ID, err)
		}
		ws, err := listWorkflows(ctx, env, prod.ID)
		if err != nil {
			return usageOut{}, err
		}
		names := make(map[string]tool.Untrusted, len(ws))
		for _, w := range ws {
			names[strings.ToLower(w.ID)] = w.Name
		}
		for _, w := range ans.WorkflowUsage {
			out.Workflows = append(out.Workflows, usageWorkflow{
				ID: w.WorkflowID, Name: names[strings.ToLower(w.WorkflowID)], Minutes: w.Minutes, Builds: w.Builds,
				PreviousMinutes: w.PreviousMinutes, PreviousBuilds: w.PreviousBuilds,
			})
		}
		slices.SortFunc(out.Workflows, func(a, b usageWorkflow) int {
			return cmp.Or(cmp.Compare(b.Minutes, a.Minutes), cmp.Compare(a.ID, b.ID))
		})
	}

	for _, d := range ans.Usage {
		if d.Minutes == 0 && d.Builds == 0 {
			continue
		}
		out.Days = append(out.Days, usageDay{Date: d.Date, Minutes: d.Minutes, Builds: d.Builds})
		out.Minutes += d.Minutes
		out.Builds += d.Builds
	}
	slices.SortFunc(out.Days, func(a, b usageDay) int { return cmp.Compare(a.Date, b.Date) })
	if prod != nil {
		out.Products = []usageProduct{{ID: prod.ID, Name: prod.Name, Minutes: out.Minutes, Builds: out.Builds}}
	}
	return out, nil
}

// applePlan turns the summary's plan into the output's.
//
// Unverified: available, used and total are taken as minutes. This is the
// one place to change if Apple counts them in another unit.
func applePlan(p appleUsagePlan) *usagePlan {
	return &usagePlan{
		Name:             p.Name,
		TotalMinutes:     p.Total,
		UsedMinutes:      p.Used,
		AvailableMinutes: p.Available,
		ResetDate:        cmp.Or(p.ResetDateTime, p.ResetDate),
		Exhausted:        p.Total > 0 && p.Available <= 0,
	}
}

// appleDaysQuery is the window of a days call.
//
// Unverified: start and end are taken as YYYY-MM-DD. This is the one place
// to change if Apple wants another date format.
func appleDaysQuery(from, to time.Time) string {
	return "?start=" + usageDate(from) + "&end=" + usageDate(to)
}

// appleUsageWindow returns the first and last day to ask for: the given
// days, or from the plan's last reset (a month before the next one) to
// today. A next reset that does not parse, or a last reset after today or
// more than maxUsageDays days back, gives the longest window instead.
func appleUsageWindow(now time.Time, days int, nextReset string) (from, to time.Time) {
	if days > 0 {
		return usageWindow(now, days)
	}
	from, to = usageWindow(now, maxUsageDays)
	next, err := time.Parse(time.RFC3339, nextReset)
	if err != nil {
		return from, to
	}
	last := next.UTC().AddDate(0, -1, 0)
	last = time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
	if last.After(to) || last.Before(to.AddDate(0, 0, -maxUsageDays)) {
		return from, to
	}
	return last, to
}
