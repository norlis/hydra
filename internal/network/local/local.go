package local

import (
	"fmt"
	"net"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/topology"
)

// Provider discovers network interfaces on the local host.
type Provider struct {
	basePort int
	allow    []string
}

func NewProvider(config *hydra.Config) *Provider {
	return &Provider{
		basePort: config.BasePort,
		allow:    config.Interfaces,
	}
}

// candidate decouples selection from net.Interface so it is testable
// without real NICs.
type candidate struct {
	name  string
	mac   string
	flags net.Flags
	ip    string // first IPv4, "" when none
	cidr  string
}

func (p *Provider) Discover() ([]topology.NetworkInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing interfaces: %w", err)
	}

	all := make([]candidate, 0, len(ifaces))
	for _, iface := range ifaces {
		ip, cidr, err := firstIPv4(iface)
		if err != nil {
			continue
		}
		all = append(all, candidate{name: iface.Name, mac: iface.HardwareAddr.String(), flags: iface.Flags, ip: ip, cidr: cidr})
	}
	return selectInterfaces(all, p.allow, p.basePort), nil
}

// With an allow list the port follows the list position, so a listed
// interface that is down (a VPN tunnel) never renumbers the others.
func selectInterfaces(all []candidate, allow []string, basePort int) []topology.NetworkInterface {
	usable := func(c candidate) bool {
		return c.flags&net.FlagUp != 0 && c.flags&net.FlagLoopback == 0 && c.ip != ""
	}
	build := func(c candidate, index int) topology.NetworkInterface {
		return topology.NetworkInterface{
			Name:        c.name,
			MAC:         c.mac,
			PrivateIP:   c.ip,
			SubnetCIDR:  c.cidr,
			ServicePort: network.BuildServicePort(basePort, index),
		}
	}

	var result []topology.NetworkInterface
	if len(allow) == 0 {
		for _, c := range all {
			if usable(c) {
				result = append(result, build(c, len(result)))
			}
		}
		return result
	}

	byName := make(map[string]candidate, len(all))
	for _, c := range all {
		byName[c.name] = c
	}
	for i, name := range allow {
		if c, ok := byName[name]; ok && usable(c) {
			result = append(result, build(c, i))
		}
	}
	return result
}

// firstIPv4 extracts the first valid IPv4 address from an interface.
func firstIPv4(iface net.Interface) (ip, cidr string, err error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return "", "", fmt.Errorf("listing addrs: %w", err)
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}

		v4 := ipNet.IP.To4()
		if v4 == nil {
			continue
		}

		return v4.String(), ipNet.String(), nil
	}

	return "", "", nil
}
