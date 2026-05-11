package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// ParseToolsets resolves a toolset expression into toolsets in display order.
//
// The expression is a comma- or space-separated list. "" means the defaults.
// Bare names ("all", "default", or toolset names) form the base set; "+name"
// and "-name" then add or remove. When only +/- terms are given the base is
// the defaults. Core is always enabled and cannot be removed.
func ParseToolsets(expr string) ([]tool.Toolset, error) {
	terms := strings.FieldsFunc(expr, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	base := map[tool.Toolset]bool{}
	hasBase := false
	var adds, removes []tool.Toolset
	for _, raw := range terms {
		t := strings.ToLower(raw)
		op := byte(0)
		if t[0] == '+' || t[0] == '-' {
			op, t = t[0], t[1:]
		}
		names, err := expandToolset(t)
		if err != nil {
			return nil, err
		}
		switch op {
		case '+':
			adds = append(adds, names...)
		case '-':
			if slices.Contains(names, tool.Core) {
				return nil, errors.New("toolsets: core cannot be disabled")
			}
			removes = append(removes, names...)
		default:
			hasBase = true
			for _, n := range names {
				base[n] = true
			}
		}
	}
	if !hasBase {
		for _, n := range tool.DefaultToolsets {
			base[n] = true
		}
	}
	for _, n := range adds {
		base[n] = true
	}
	for _, n := range removes {
		delete(base, n)
	}
	base[tool.Core] = true
	out := make([]tool.Toolset, 0, len(base))
	for _, ts := range tool.AllToolsets {
		if base[ts] {
			out = append(out, ts)
		}
	}
	return out, nil
}

func expandToolset(name string) ([]tool.Toolset, error) {
	switch name {
	case "all":
		return tool.AllToolsets, nil
	case "default":
		return tool.DefaultToolsets, nil
	}
	ts := tool.Toolset(name)
	if !ts.Valid() {
		return nil, fmt.Errorf("toolsets: unknown toolset %q (known: all, default, %s)", name, joinToolsets(tool.AllToolsets))
	}
	return []tool.Toolset{ts}, nil
}

func joinToolsets(ts []tool.Toolset) string {
	s := make([]string, len(ts))
	for i, t := range ts {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}

// ValidateBaseURL accepts an API base URL override only when it points at a
// loopback host over http or https, with no credentials, query or fragment.
// It returns the URL without a trailing slash.
func ValidateBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("HAMN_BASE_URL: %w", err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("HAMN_BASE_URL: scheme %q must be http or https", u.Scheme)
	case u.User != nil:
		return "", errors.New("HAMN_BASE_URL: userinfo is not allowed")
	case u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return "", errors.New("HAMN_BASE_URL: query and fragment are not allowed")
	case !isLoopback(u.Hostname()):
		return "", fmt.Errorf("HAMN_BASE_URL: host %q is not loopback; the override exists only for local test servers", u.Hostname())
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
