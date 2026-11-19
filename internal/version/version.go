package version

import (
	"runtime/debug"
	"strings"
)

// Version can be overridden at build time via -ldflags "-X engram/internal/version.Version=...".
var Version = ""

// Current returns the resolved server version string.
func Current() string {
	return resolve(Version, debug.ReadBuildInfo)
}

func resolve(override string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	if readBuildInfo != nil {
		if info, ok := readBuildInfo(); ok && info != nil {
			if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
				return v
			}
			var revision string
			var modified bool
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs.revision":
					revision = strings.TrimSpace(setting.Value)
				case "vcs.modified":
					modified = strings.EqualFold(strings.TrimSpace(setting.Value), "true")
				}
			}
			if revision != "" {
				if len(revision) > 7 {
					revision = revision[:7]
				}
				if modified {
					return "dev-" + revision + "-dirty"
				}
				return "dev-" + revision
			}
		}
	}
	return "dev"
}
