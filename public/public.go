// Package public carries the built Web panel front end. The files under dist/ are
// produced by EasySB-Panel's GitHub Actions and fetched by scripts/fetch-panel.sh;
// the bundle sits at the module root so the embedded console has one obvious home
// and internal/panel only has to read it.
package public

import "embed"

// Dist is the panel front end. It is embedded with all: so a host that ships no
// separate copy still serves the console from the binary; a newer bundle on disk
// (WebDir) still wins when it is present.
//
//go:embed all:dist
var Dist embed.FS
