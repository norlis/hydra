package version

// GitHash and Date are injected at build time via -ldflags.
// See LDFLAGS in the Makefile.
var (
	GitHash = "dev"
	Date    = "unknown"
)
