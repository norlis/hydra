package network

const (
	// DefaultServicePort is used when no base port is configured.
	DefaultServicePort = 3128
)

// BuildServicePort returns the service port for a given interface.
// basePort <= 0 falls back to DefaultServicePort. index is the position
// of the interface among the valid ones; consecutive interfaces get
// consecutive ports (base, base+1, base+2, …).
func BuildServicePort(basePort, index int) int {
	if basePort <= 0 {
		basePort = DefaultServicePort
	}
	return basePort + index
}
