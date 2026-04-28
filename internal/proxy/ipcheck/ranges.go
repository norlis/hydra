package ipcheck

import "net/netip"

// Usamos []netip.Prefix en lugar de []string
var builtInDeny = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("127.0.0.0/8"),     // loopback
	netip.MustParsePrefix("169.254.0.0/16"),  // link-local + AWS IMDS
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("224.0.0.0/4"),     // multicast
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("::1/128"),         // IPv6 loopback
	netip.MustParsePrefix("fc00::/7"),        // IPv6 ULA
	netip.MustParsePrefix("fe80::/10"),       // IPv6 link-local
	netip.MustParsePrefix("::ffff:0:0/96"),   // IPv4-mapped IPv6
}

var (
	cgnatRange     = netip.MustParsePrefix("100.64.0.0/10")
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour      = netip.MustParsePrefix("2002::/16")
	teredo         = netip.MustParsePrefix("2001::/32")
)

func isCGNAT(ip netip.Addr) bool {
	return cgnatRange.Contains(ip)
}

func hasIPv6Embedding(ip netip.Addr) bool {
	// netip provee Is4In6() para simplificar esto
	if ip.Is4() || ip.Is4In6() {
		return false // manejado por rutas de IPv4
	}
	return nat64WellKnown.Contains(ip) || sixToFour.Contains(ip) || teredo.Contains(ip)
}
