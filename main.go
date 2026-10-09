// Command easysb is the panel. The command line lives in the cmd package; this
// file exists only to own the two build-stamped values and hand them over. It
// embeds VERSION and carries the -X main.commit stamp, so the release number has a
// single source (the VERSION file) and a bare `go build` reports the same version
// as a published release.
package main

import (
	_ "embed"

	"github.com/EasySBTeam/EasySB/cmd"
)

// version is compiled in from the repository's VERSION file, the single source of
// truth for the release number. There is no -ldflags -X for it and no second
// constant to drift.
//
//go:embed VERSION
var version string

// commit is stamped at build time (-X main.commit=<sha>), empty in a bare build.
var commit = ""

func main() {
	cmd.Execute(version, commit)
}
