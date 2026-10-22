// Package version reports the ark version, taken from the build info Go embeds
// at compile time: the tag for `go install …@vX.Y.Z` or a build at a tag, a
// pseudo-version for other commits.
package version

import (
	"runtime/debug"
	"strings"
)

// Version overrides the build info when set, e.g. with
// -ldflags "-X github.com/leonialla/ark/internal/version.Version=v1.2.3".
var Version = ""

func String() string {
	if Version != "" {
		return Version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return fromBuildInfo(nil)
	}
	return fromBuildInfo(bi)
}

func fromBuildInfo(bi *debug.BuildInfo) string {
	if bi == nil {
		return "unknown"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev string
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev == "" {
		return "devel"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	details := []string{rev}
	if modified {
		details = append(details, "modified")
	}
	return "devel (" + strings.Join(details, ", ") + ")"
}
