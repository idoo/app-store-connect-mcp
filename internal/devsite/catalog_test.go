package devsite

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// sharedRefs are the reference rows that describe several calls at once.
var sharedRefs = []string{"N26", "N35", "J11", "J12", "J14"}

func TestCatalogParsesAndIsConsistent(t *testing.T) {
	ops := All()
	if len(ops) < 90 {
		t.Fatalf("operations = %d, want at least 90", len(ops))
	}
	idRE := regexp.MustCompile(`^[a-z_]+\.[a-z0-9_]+$`)
	refRE := regexp.MustCompile(`^[SKNACDJ][0-9]+$`)
	ids, refs := map[string]bool{}, map[string]int{}
	for _, o := range ops {
		if !idRE.MatchString(o.ID) || ids[o.ID] {
			t.Errorf("%s: id malformed or repeated", o.ID)
		}
		ids[o.ID] = true
		if !refRE.MatchString(o.Ref) {
			t.Errorf("%s: ref %q", o.ID, o.Ref)
		}
		refs[o.Ref]++
		if !slices.Contains(Groups, o.Group) || !strings.HasPrefix(o.ID, o.Group+".") {
			t.Errorf("%s: group %q", o.ID, o.Group)
		}
		if o.Title == "" || len(o.Title) > 60 {
			t.Errorf("%s: title of %d bytes", o.ID, len(o.Title))
		}
		if !slices.Contains([]string{"observed", "observed_live_by_source", "inferred"}, o.Status) {
			t.Errorf("%s: status %q", o.ID, o.Status)
		}
		if len(o.Source) == 0 {
			t.Errorf("%s: no source", o.ID)
		}
		for _, s := range o.Source {
			if !slices.Contains([]string{"fastlane", "asc-cli", "expo"}, s) {
				t.Errorf("%s: source %q", o.ID, s)
			}
		}
		if o.Verified != "" {
			t.Errorf("%s: verified %q, but nothing was verified live", o.ID, o.Verified)
		}
	}
	for ref, n := range refs {
		if n > 1 && !slices.Contains(sharedRefs, ref) {
			t.Errorf("ref %s is used by %d operations", ref, n)
		}
	}
	for _, g := range Groups {
		if len(Search("", g, 0)) == 0 {
			t.Errorf("group %s has no operation", g)
		}
	}
}

// The rule in tool.DevSiteIsRead decides; the catalog only repeats it.
func TestCatalogReadAgreesWithTheRule(t *testing.T) {
	for _, o := range All() {
		path := o.FilledPath(map[string]string{"id": "ABC123"})
		if got := tool.DevSiteIsRead(o.Method, path); got != o.Read {
			t.Errorf("%s: read = %v, DevSiteIsRead(%s, %s) = %v", o.ID, o.Read, o.Method, path, got)
		}
	}
}

func TestCatalogPathsWiresAndParams(t *testing.T) {
	paramIn := map[string][]string{
		"v1 GET": {"path", "query"}, "v1 DELETE": {"path", "query"}, "v1 POST": {"path", "json"}, "v1 PATCH": {"path", "json"},
		"form POST": {"path", "form"}, "json POST": {"path", "json"}, "download GET": {"path", "query"},
	}
	for _, o := range All() {
		if o.Path == "" || strings.HasPrefix(o.Path, "/") || strings.ContainsAny(o.Path, "?#% \\") || strings.Contains(o.Path, "://") {
			t.Errorf("%s: path %q is not clean and relative", o.ID, o.Path)
		}
		for _, seg := range strings.Split(o.Path, "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Errorf("%s: path %q has an empty or dot segment", o.ID, o.Path)
			}
			if strings.ContainsAny(seg, "{}") && seg != "{platform}" && seg != "{id}" {
				t.Errorf("%s: placeholder %q", o.ID, seg)
			}
		}
		if strings.HasPrefix(o.Path, "v1/") != (o.Wire == WireV1) {
			t.Errorf("%s: wire %q does not fit path %q", o.ID, o.Wire, o.Path)
		}
		allowed, ok := paramIn[o.Wire+" "+o.Method]
		if !ok {
			t.Errorf("%s: wire %q with method %q", o.ID, o.Wire, o.Method)
			continue
		}
		names := map[string]bool{}
		for _, p := range o.Params {
			if p.Name == "" || names[p.Name] {
				t.Errorf("%s: param %q empty or repeated", o.ID, p.Name)
			}
			names[p.Name] = true
			if p.Name == "teamId" {
				t.Errorf("%s: teamId is listed; the tools add it", o.ID)
			}
			if !slices.Contains(allowed, p.In) {
				t.Errorf("%s: param %s in %q on a %s %s operation", o.ID, p.Name, p.In, o.Wire, o.Method)
			}
			if p.In == "path" && !strings.Contains(o.Path, "{"+p.Name+"}") {
				t.Errorf("%s: path param %s is not in %q", o.ID, p.Name, o.Path)
			}
			for _, v := range p.Values {
				if v.Label == "" {
					t.Errorf("%s: param %s value %q has no label", o.ID, p.Name, v.Value)
				}
			}
		}
		for _, ph := range []string{"platform", "id"} {
			if strings.Contains(o.Path, "{"+ph+"}") && !names[ph] {
				t.Errorf("%s: {%s} has no path param", o.ID, ph)
			}
		}
	}
}

func TestFind(t *testing.T) {
	for ref, want := range map[string]string{
		"keys.revoke":                         "keys.revoke",
		"K1":                                  "keys.list",
		"k5":                                  "keys.download",
		"POST QH65B2/account/auth/key/revoke": "keys.revoke",
		"delete v1/websitepushIds/WP42":       "website_push_ids.delete_v1",
		"GET v1/profiles/P1/certificates":     "certificates.list_for_profile",
		"QH65B2/account/mac/certificate/revokeCertificate.action":           "certificates.revoke",
		"/services-account/v1/devices/D9":                                   "devices.update",
		"POST v1/bundleIdCapabilities":                                      "capabilities.create",
		"GET v1/merchantIds/M1?include=x":                                   "merchant_ids.get_v1",
		"https://developer.apple.com/services-account/v1/cloudContainers":   "cloud_containers.list_v1",
		"GET QH65B2/account/ios/identifiers/downloadDomainVerificationFile": "merchant_ids.download_domain_verification_file",
	} {
		got, ok := Find(ref)
		if !ok || got.ID != want {
			t.Errorf("Find(%q) = %q, %v; want %s", ref, got.ID, ok, want)
		}
	}
	for _, ref := range []string{"", "nope", "Z9", "PUT v1/bundleIds", "v1/bundleIds/a/b/c", "v1/devices/"} {
		if got, ok := Find(ref); ok {
			t.Errorf("Find(%q) = %s, want none", ref, got.ID)
		}
	}
}

func TestSearch(t *testing.T) {
	first := func(query, group string) string {
		t.Helper()
		got := Search(query, group, 5)
		if len(got) == 0 {
			t.Fatalf("Search(%q, %q) found nothing", query, group)
		}
		return got[0].ID
	}
	if id := first("revoke key", ""); id != "keys.revoke" {
		t.Errorf("revoke key: first = %s", id)
	}
	if id := first("Create certificate CSR", ""); id != "certificates.create" {
		t.Errorf("create certificate csr: first = %s", id)
	}
	if id := first("app groups", "app_groups"); id != "app_groups.list" {
		t.Errorf("app groups: first = %s", id)
	}
	if got := Search("zzzz", "", 0); len(got) != 0 {
		t.Errorf("no match: got %d", len(got))
	}
	if got := Search("list", "", 3); len(got) != 3 {
		t.Errorf("limit: got %d", len(got))
	}
	all := All()
	if got := Search("", "", 0); len(got) != len(all) || got[0].ID != all[0].ID {
		t.Errorf("empty query: got %d of %d", len(got), len(all))
	}
	for _, o := range Search("", "keys", 0) {
		if o.Group != "keys" {
			t.Errorf("group keys lists %s", o.ID)
		}
	}
}

func callJSON(t *testing.T, id string, values map[string]string) (string, string) {
	t.Helper()
	o, ok := Find(id)
	if !ok {
		t.Fatalf("no operation %s", id)
	}
	name, args := o.Call(values)
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return name, string(b)
}

func TestCall(t *testing.T) {
	for _, c := range []struct {
		id     string
		values map[string]string
		tool   string
		args   string
	}{
		{"services_ids.list", nil, "developer_api_get", `{"path":"v1/bundleIds","query":{"filter[platform]":"SERVICES"}}`},
		{"keys.get", nil, "developer_api_get", `{"form":{"keyId":"\u003ckeyId\u003e"},"path":"QH65B2/account/auth/key/get"}`},
		{"team.list_members", nil, "developer_api_get", `{"json":{},"path":"QH65B2/account/getTeamMembers"}`},
		{"app_ids.list", map[string]string{"platform": "mac"}, "developer_api_get", `{"path":"QH65B2/account/mac/identifiers/listAppIds.action"}`},
		{"bundle_ids.get", nil, "developer_api_get", `{"path":"v1/bundleIds/{id}"}`},
		{
			"keys.revoke",
			map[string]string{"keyId": "K1"},
			"developer_api_request",
			`{"form":{"keyId":"K1"},"method":"POST","path":"QH65B2/account/auth/key/revoke"}`,
		},
		{"bundle_ids.delete", map[string]string{"id": "B1"}, "developer_api_request", `{"method":"DELETE","path":"v1/bundleIds/B1"}`},
		{
			"cloud_containers.create_v1", nil, "developer_api_request",
			`{"json":{"data":{"attributes":{"identifier":"\u003cdata.attributes.identifier\u003e","name":"\u003cdata.attributes.name\u003e",` +
				`"teamId":"\u003cdata.attributes.teamId\u003e"},"type":"cloudContainers"}},"method":"POST","path":"v1/cloudContainers"}`,
		},
		{
			"certificates.download", nil, "developer_api_request",
			`{"method":"GET","path":"QH65B2/account/ios/certificate/downloadCertificateContent.action",` +
				`"query":{"certificateId":"\u003ccertificateId\u003e","type":"\u003ctype\u003e"},"save_to":"\u003cnew file\u003e"}`,
		},
	} {
		name, args := callJSON(t, c.id, c.values)
		if name != c.tool || args != c.args {
			t.Errorf("%s: %s %s\nwant %s %s", c.id, name, args, c.tool, c.args)
		}
	}
}
