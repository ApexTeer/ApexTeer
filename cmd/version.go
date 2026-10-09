package cmd

import "strings"

// buildVersion and buildCommit are handed in by the root package: it embeds VERSION
// and carries the -X main.commit stamp, so this package reads them instead of
// keeping a second copy. A bare `go build` therefore reports the same version as a
// published release, with no -ldflags -X for the number and no second constant to
// drift.
var (
	buildVersion string
	buildCommit  string
)

// versionLine is the human-facing build string, including the short commit when
// the build stamped one.
func versionLine() string {
	v := resolveVersion()
	if len(buildCommit) >= 7 {
		return v + " (" + buildCommit[:7] + ")"
	}
	return v
}

func resolveVersion() string {
	return strings.TrimSpace(buildVersion)
}
