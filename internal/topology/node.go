package topology

import (
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
	Reachable   bool   `json:"reachable,omitempty"`
	PhysicalID  string `json:"physical_id,omitempty"`
}

func (n *NetworkInterface) Address() string {
	return n.PrivateIP + ":" + strconv.Itoa(n.ServicePort)
}

// Node is an abstract participant in the mesh topology.
type Node struct {
	ID         string             `json:"id"`
	Zone       string             `json:"zone,omitempty"` // e.g. "us-east-1", "datacenter-south", "rack-4"
	Interfaces []NetworkInterface `json:"interfaces"`
	LastSeen   time.Time          `json:"last_seen"`
	StartedAt  *time.Time         `json:"started_at,omitempty"`
	Healthy    bool               `json:"healthy"`
	Version    string             `json:"version,omitempty"` // binary version (git tag or short commit), injected at build time

	// Labels allows injecting arbitrary metadata without changing the
	// struct (extensibility). E.g. {"provider": "aws", "role": "edge-proxy"}.
	Labels map[string]string `json:"labels,omitempty"`
}

func (n *Node) GetPrimaryIP() string {
	if len(n.Interfaces) > 0 {
		return n.Interfaces[0].PrivateIP
	}
	return ""
}

func (n *Node) HasRole(role string) bool {
	val, exists := n.Labels["role"]
	return exists && val == role
}
