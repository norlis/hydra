package network

import "github.com/norlis/hydra/internal/topology"

// Provider is the contract for discovering network interfaces
// regardless of the underlying environment (Local, Docker, AWS, …).
type Provider interface {
	Discover() ([]topology.NetworkInterface, error)
}
