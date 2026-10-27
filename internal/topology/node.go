package topology

import (
	"net"
	"strconv"
	"time"
)

// NetworkInterface describes a generic network interface for any node
// in the cluster.
type NetworkInterface struct {
	Name        string `json:"name"` // e.g. "eth0", "en0"
	MAC         string `json:"mac,omitempty"`
	PrivateIP   string `json:"private_ip"`
	PublicIP    string `json:"public_ip,omitempty"`
	SubnetCIDR  string `json:"subnet_cidr,omitempty"`
	ServicePort int    `json:"service_port"`
	PhysicalID  string `json:"physical_id,omitempty"`
}

func (n *NetworkInterface) Address() string {
	return n.PrivateIP + ":" + strconv.Itoa(n.ServicePort)
}

// Node is an abstract participant in the mesh topology.
type Node struct {
	ID string `json:"id"`
	// Gossip address: interface IPs may be unreachable from peers (VPN
	// tunnels) or shared by several hosts (VM bridges).
	Addr       string             `json:"addr,omitempty"`
	Interfaces []NetworkInterface `json:"interfaces"`
	LastSeen   time.Time          `json:"last_seen"`
	StartedAt  *time.Time         `json:"started_at,omitempty"`
	Healthy    bool               `json:"healthy"`
	Version    string             `json:"version,omitempty"` // binary version (git tag or short commit), injected at build time
}

// ProxyAddress pairs the node address with the interface port: proxies
// listen on every address, so the relay still lands on that interface.
func (n Node) ProxyAddress(iface NetworkInterface) string {
	host := n.Addr
	if host == "" {
		host = iface.PrivateIP
	}
	return net.JoinHostPort(host, strconv.Itoa(iface.ServicePort))
}
