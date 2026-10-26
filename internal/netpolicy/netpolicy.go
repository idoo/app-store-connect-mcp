// Package netpolicy is the single "may this server connect here" address
// predicate shared by every unauthenticated egress path (the asc fetcher and
// the release_status URL probes). It is a stdlib-only leaf so tool packages
// can import it without reaching the ASC client.
package netpolicy

import "net/netip"

// blockedPrefixes are non-public ranges not covered by the netip predicates:
// "this network", CGNAT, IETF protocol assignments, benchmarking, reserved
// class E and NAT64 (which can embed any IPv4 address).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// PublicAddr reports whether addr is a valid, globally routable unicast
// address. IPv4-mapped IPv6 addresses are judged as their IPv4 form.
func PublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() || !addr.IsGlobalUnicast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}
