package version

// Version is set at build time via:
//
//	-ldflags="-X github.com/jptrs93/opsagent/backend/util/version.Version=..."
//
// v0.0.0 is the self-built/local default.
var Version = "v0.0.0"
