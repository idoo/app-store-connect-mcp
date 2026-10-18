package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const asoUsage = `usage: app-store-connect-mcp aso track --app APP --keywords a,b [--storefronts us,se] [--depth N]
       app-store-connect-mcp aso report --app APP [--keywords a,b] [--storefronts us,se] [--days N]
  runs store_ranks_report (public App Store search, no ASC key) and prints JSON; for cron.
  track measures now and appends to the local rank history; report reads that history only.
  exit 0 ok, 1 error, 2 usage.`

// asoEnv builds the tool environment for aso; tests replace it.
var asoEnv = func(ctx context.Context, profile string) (*tool.Env, error) {
	env, _, err := guardEnv(ctx, profile, false)
	return env, err
}

// asoOut is the aso JSON output: the tool's data plus any artifact links and
// warnings the tool reported.
type asoOut struct {
	Data     any       `json:"data"`
	Links    []asoLink `json:"links,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

type asoLink struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}

func runAso(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "track" && args[0] != "report") {
		return usageErr("aso: want track or report\n" + asoUsage)
	}
	action := args[0]
	fs := flag.NewFlagSet("aso "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile to use (only for the default app)")
	appRef := fs.String("app", "", "App Store id or bundle id")
	keywords := fs.String("keywords", "", "comma-separated search terms")
	storefronts := fs.String("storefronts", "", "comma-separated two-letter storefronts (default us)")
	depth := fs.Int("depth", 0, "search depth 1-200 (track; default 100)")
	days := fs.Int("days", 0, "report window in days 1-365 (report; default 30)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageErr(err.Error())
	}
	if fs.NArg() > 0 {
		return usageErr(fmt.Sprintf("aso %s: unexpected argument %q\n%s", action, fs.Arg(0), asoUsage))
	}
	if *appRef == "" {
		return usageErr("aso: --app is required\n" + asoUsage)
	}
	in := map[string]any{"action": action, "app": *appRef}
	if k := splitList(*keywords); len(k) > 0 {
		in["keywords"] = k
	} else if action == "track" {
		return usageErr("aso track: --keywords is required\n" + asoUsage)
	}
	if c := splitList(*storefronts); len(c) > 0 {
		in["countries"] = c
	}
	if *depth != 0 {
		in["depth"] = *depth
	}
	if *days != 0 {
		in["days"] = *days
	}
	t, ok := tool.Lookup("store_ranks_report")
	if !ok {
		return errors.New("store_ranks_report is not registered")
	}
	env, err := asoEnv(ctx, *profile)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	res := t.Call(ctx, env, raw)
	out := asoOut{Data: res.Data, Warnings: res.Meta.Warnings}
	for _, l := range res.Links {
		out.Links = append(out.Links, asoLink{URI: l.URI, Name: l.Name})
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("aso %s failed", action)
	}
	return nil
}

// splitList splits a comma-separated flag value, dropping empty items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
