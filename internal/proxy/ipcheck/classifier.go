// Package ipcheck classifies destination IPs to prevent SSRF attacks.
// Adapted from Stripe's smokescreen (pkg/smokescreen/smokescreen.go::classifyAddr).
//
// The Classifier is consulted from net.Dialer.Control AFTER DNS resolves a
// hostname to an IP and BEFORE the kernel emits the SYN packet. Returning a
// non-nil error from Validate() blocks the connect.
package ipcheck

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
)

// Decision describes what the classifier decided about an address.
type Decision int

const (
	Allow Decision = iota
	DenyNotGlobalUnicast
	DenyPrivateRange
	DenyLoopback
	DenyLinkLocal
	DenyMulticast
	DenyCGNAT
	DenyIPv6Embedding // NAT64 / 6to4 / Teredo
	DenyConfigured    // matched user-configured deny CIDR
	DenySelf          // proxy connecting back to itself
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case DenyNotGlobalUnicast:
		return "not_global_unicast"
	case DenyPrivateRange:
		return "private_range"
	case DenyLoopback:
		return "loopback"
	case DenyLinkLocal:
		return "link_local"
	case DenyMulticast:
		return "multicast"
	case DenyCGNAT:
		return "cgnat"
	case DenyIPv6Embedding:
		return "ipv6_embedding"
	case DenyConfigured:
		return "configured_deny"
	case DenySelf:
		return "self_connection"
	default:
		return "unknown"
	}
}

// Classifier evaluates whether a destination IP is safe to dial.
type Classifier struct {
	denyNets  []netip.Prefix
	allowNets []netip.Prefix // user-configured exceptions to the built-in denies
	localIPs  []netip.Addr   // proxy's own IPs; deny if dest matches
}

// New builds a classifier with the standard denylist plus the supplied
// extras. allow is consulted first; an IP that matches an allow CIDR
// is permitted even if it would otherwise be denied (e.g. permitting a
// specific RFC1918 target during development).
func New(extraDeny, allow []string, localIPs []netip.Addr) (*Classifier, error) {
	extra, err := parsePrefixes(extraDeny)
	if err != nil {
		return nil, fmt.Errorf("ipcheck: invalid extra deny: %w", err)
	}

	allowNets, err := parsePrefixes(allow)
	if err != nil {
		return nil, fmt.Errorf("ipcheck: invalid allow: %w", err)
	}

	deny := make([]netip.Prefix, 0, len(builtInDeny)+len(extra))
	deny = append(deny, builtInDeny...)
	deny = append(deny, extra...)

	return &Classifier{
		denyNets:  deny,
		allowNets: allowNets,
		localIPs:  localIPs,
	}, nil
}

// Classify returns a Decision for an IP.
func (c *Classifier) Classify(ip netip.Addr) Decision {
	// Desempaquetamos IPv4 mapeadas en IPv6 (ej. ::ffff:192.168.1.1 -> 192.168.1.1)
	ip = ip.Unmap()

	for _, n := range c.allowNets {
		if n.Contains(ip) { // Mucho más rápido que net.IPNet.Contains
			return Allow
		}
	}

	// ¡Ya no necesitamos slices.ContainsFunc!
	// Como netip.Addr es comparable, podemos usar slices.Contains directamente
	if slices.Contains(c.localIPs, ip) {
		return DenySelf
	}

	if !ip.IsGlobalUnicast() {
		switch {
		case ip.IsLoopback():
			return DenyLoopback
		case ip.IsMulticast():
			return DenyMulticast
		case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
			return DenyLinkLocal
		default:
			return DenyNotGlobalUnicast
		}
	}
	if ip.IsPrivate() {
		return DenyPrivateRange
	}
	if isCGNAT(ip) {
		return DenyCGNAT
	}
	if hasIPv6Embedding(ip) {
		return DenyIPv6Embedding
	}
	for _, n := range c.denyNets {
		if n.Contains(ip) {
			return DenyConfigured
		}
	}
	return Allow
}

// Validate is the convenience entry point for net.Dialer.Control. It
// returns nil when the IP is allowed, or an error describing the deny
// reason otherwise. The address argument has the form "ip:port".
func (c *Classifier) Validate(network, address string) error {
	// Usamos netip.ParseAddrPort para parsear directamente sin alojamientos extra
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("ipcheck: bad address %q: %w", address, err)
	}

	d := c.Classify(addrPort.Addr())
	if d == Allow {
		return nil
	}
	return &DeniedError{IP: addrPort.Addr(), Reason: d}
}

// DeniedError is returned by Validate when an address is rejected.
type DeniedError struct {
	IP     netip.Addr // Actualizado a netip.Addr
	Reason Decision
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("ipcheck: %s denied (%s)", e.IP, e.Reason)
}

func IsDenied(err error) bool {
	_, ok := errors.AsType[*DeniedError](err) // Go 1.26 generic As
	return ok
}

// Reason returns the deny reason string when err is a policy denial from this
// classifier, or "" otherwise. Feeds the deny_reason log field.
func Reason(err error) string {
	if de, ok := errors.AsType[*DeniedError](err); ok {
		return de.Reason.String()
	}
	return ""
}

// parsePrefixes convierte un slice de strings en formato CIDR a un slice de netip.Prefix.
func parsePrefixes(cidrs []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}
