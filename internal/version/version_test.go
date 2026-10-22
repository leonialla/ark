package version

import (
	"runtime/debug"
	"testing"
)

func TestOverride(t *testing.T) {
	old := Version
	Version = "v9.9.9"
	t.Cleanup(func() { Version = old })
	if got := String(); got != "v9.9.9" {
		t.Fatalf("String() = %q", got)
	}
}

func TestFromBuildInfo(t *testing.T) {
	cases := []struct {
		name string
		bi   *debug.BuildInfo
		want string
	}{
		{"tagged module", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}}, "v0.2.0"},
		{"pseudo-version", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.1-0.20261022070000-440b1b72ca4c"}}, "v0.2.1-0.20261022070000-440b1b72ca4c"},
		{"devel with vcs", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "440b1b72ca4c3347434b1450158e0d8e6cfcda82"}, {Key: "vcs.modified", Value: "true"}}}, "devel (440b1b7, modified)"},
		{"devel without vcs", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "devel"},
		{"no build info", nil, "unknown"},
	}
	for _, c := range cases {
		if got := fromBuildInfo(c.bi); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStringNeverEmpty(t *testing.T) {
	if String() == "" {
		t.Fatal("empty version")
	}
}
