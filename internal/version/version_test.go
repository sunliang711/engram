package version

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	t.Run("explicit ldflags override wins", func(t *testing.T) {
		got := resolve(" v0.0.2 ", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, true
		})
		if got != "v0.0.2" {
			t.Fatalf("got %q, want %q", got, "v0.0.2")
		}
	})

	t.Run("uses module version when not devel", func(t *testing.T) {
		got := resolve("", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, true
		})
		if got != "v0.1.0" {
			t.Fatalf("got %q, want %q", got, "v0.1.0")
		}
	})

	t.Run("uses vcs revision fallback", func(t *testing.T) {
		got := resolve("", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abcdef1234567890"},
					{Key: "vcs.modified", Value: "false"},
				},
			}, true
		})
		if got != "dev-abcdef1" {
			t.Fatalf("got %q, want %q", got, "dev-abcdef1")
		}

		dirty := resolve("", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abcdef1234567890"},
					{Key: "vcs.modified", Value: "true"},
				},
			}, true
		})
		if dirty != "dev-abcdef1-dirty" {
			t.Fatalf("got %q, want %q", dirty, "dev-abcdef1-dirty")
		}
	})

	t.Run("falls back to dev when build info unavailable", func(t *testing.T) {
		if got := resolve("", func() (*debug.BuildInfo, bool) { return nil, false }); got != "dev" {
			t.Fatalf("got %q, want %q", got, "dev")
		}
	})
}
