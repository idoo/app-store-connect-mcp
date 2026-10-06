package main

import "testing"

func TestModuleVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v0.1.3":                             "0.1.3",
		"v0.1.4-0.20261026120000-abcdef0123": "0.1.4-0.20261026120000-abcdef0123",
		"(devel)":                            "",
		"":                                   "",
	} {
		if got := moduleVersion(in); got != want {
			t.Errorf("moduleVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
