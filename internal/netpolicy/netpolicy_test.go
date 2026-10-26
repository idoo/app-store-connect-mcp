package netpolicy

import (
	"net/netip"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "17.1.2.3", "93.184.216.34", "2001:4860:4860::8888", "::ffff:8.8.8.8"} {
		if !PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("PublicAddr(%s) = false", s)
		}
	}
	for _, s := range []string{
		"127.0.0.1", "::1", "10.0.0.7", "192.168.1.5", "172.16.0.1", "fd00::1",
		"169.254.169.254", "fe80::1", "0.0.0.0", "::", "0.1.2.3", "100.64.0.1",
		"192.0.0.8", "198.18.0.1", "240.0.0.1", "255.255.255.255", "224.0.0.1",
		"ff02::1", "64:ff9b::a00:1", "64:ff9b:1::1", "::ffff:10.0.0.1",
	} {
		if PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("PublicAddr(%s) = true", s)
		}
	}
	if PublicAddr(netip.Addr{}) {
		t.Error("PublicAddr(zero) = true")
	}
}
