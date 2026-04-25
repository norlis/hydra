package cluster

// SeedProvider defines the contract for dynamic discovery of initial
// cluster seeds.
type SeedProvider interface {
	Discover() ([]string, error)
}

// NoopSeedProvider is a no-op SeedProvider used when only static seeds
// (from configuration) should be trusted.
type NoopSeedProvider struct{}

// Discover always returns an empty list without error.
func (NoopSeedProvider) Discover() ([]string, error) { return nil, nil }
