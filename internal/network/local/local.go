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
}

// NewProvider builds the provider with a base port. Passing 0 falls back
// to the package default via BuildServicePort.
func NewProvider(config *hydra.Config) *Provider {
	return &Provider{
		basePort: config.BasePort,
	}
}

// Discover returns every active (UP) non-loopback interface and assigns
// sequential service ports starting from the base port.
func (p *Provider) Discover() ([]topology.NetworkInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing interfaces: %w", err)
	}

	result := make([]topology.NetworkInterface, 0, len(ifaces))
	// Counter only for interfaces that pass our filters.
	validIndex := 0

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		privateIP, subnetCIDR, err := firstIPv4(iface)
		if err != nil || privateIP == "" {
			continue
		}

		calculatedPort := network.BuildServicePort(p.basePort, validIndex)

		result = append(result, topology.NetworkInterface{
			Name:        iface.Name,
			MAC:         iface.HardwareAddr.String(),
			PrivateIP:   privateIP,
			SubnetCIDR:  subnetCIDR,
			ServicePort: calculatedPort,
		})

		validIndex++
	}

	return result, nil
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
