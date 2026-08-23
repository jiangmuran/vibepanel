// Package version carries build identity injected at link time.
//
// The values are set with -ldflags "-X github.com/jiangmuran/vibepanel/internal/version.Version=..."
// so that a binary someone downloaded from a release page can always answer
// "which build am I?" without a git checkout next to it.
package version

var (
	// Version is the release tag, e.g. "v0.3.1". "dev" for local builds.
	Version = "dev"
	// Commit is the short git SHA.
	Commit = "none"
	// Date is the RFC3339 build timestamp.
	Date = "unknown"
)

// String renders the one-line identity used by --version and the settings page.
func String() string {
	return Version + " (" + Commit + ", built " + Date + ")"
}
