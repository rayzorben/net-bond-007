// Package version is the single source of truth for the Net-Bond-007 version.
// Bump internal/version/VERSION; every build (go build, scripts/build.sh,
// Docker, deploy scripts) reads it, so binaries can never report "dev".
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Version is the release version, e.g. "0.3.0".
var Version = strings.TrimSpace(raw)
