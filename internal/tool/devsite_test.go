package tool

import "testing"

func TestDevSiteIsRead(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "v1/bundleIds", true},
		{"GET", "v2/things/A1", true},
		{"POST", "v1/bundleIds", false},
		{"PATCH", "v1/bundleIds/A1", false},
		{"DELETE", "v1/bundleIds/A1", false},
		// The request history is the one v1 POST that only reads.
		{"POST", "v1/featureRequest", true},
		{"POST", "v1/featureRequests", false},
		{"POST", "v1/featureRequest/A1", false},
		{"PATCH", "v1/featureRequest", false},
		{"POST", "v2/featureRequest", false},
		{"POST", "QH65B2/account/auth/key/list", true},
		{"POST", "QH65B2/account/listTeams.action", true},
		{"POST", "QH65B2/account/getTeams", true},
		{"POST", "checkPermissions", true},
		{"POST", "QH65B2/account/ios/certificate/validateAsk.action", true},
		{"POST", "QH65B2/account/auth/key/create", false},
		{"POST", "QH65B2/account/auth/key/revoke", false},
		{"GET", "QH65B2/account/auth/key/download", false},
		{"POST", "QH65B2/account/listen", false},
		{"POST", "QH65B2/account/issue", false},
		{"PUT", "QH65B2/account/listTeams.action", false},
		{"DELETE", "QH65B2/account/getTeams", false},
	} {
		if got := DevSiteIsRead(tc.method, tc.path); got != tc.want {
			t.Errorf("DevSiteIsRead(%s, %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
