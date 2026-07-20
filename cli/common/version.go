package common

import "runtime/debug"

// version is injected via -ldflags "-X ...common.version=..." by GoReleaser
// at release-build time (see .goreleaser.yaml), combining the released
// semver tag and short commit hash, e.g. "0.1.1+a1b2c3d". Local `go build`/
// `go run` leave it empty, and ReadBuildInfo falls back to devVersion.
var version string

// nmilatModulePath is the module path of ncli's core Nostr library, whose
// version we surface separately since it evolves independently of ncli.
const nmilatModulePath = "github.com/ohstr/nmilat"

// BuildInfo describes the running binary.
type BuildInfo struct {
	Software      string // e.g. "git+https://github.com/ohstr/ncli"
	Version       string // e.g. "0.1.1+a1b2c3d" (release) or "dev+a1b2c3d4e5f6" (local build)
	NmilatVersion string // resolved version of the github.com/ohstr/nmilat dependency
}

// ReadBuildInfo returns the release version injected by GoReleaser, or,
// for local builds where nothing was injected, a version derived from the
// VCS metadata Go embeds automatically (commit hash + dirty flag).
func ReadBuildInfo() BuildInfo {
	v := version
	if v == "" {
		v = devVersion()
	}
	return BuildInfo{Software: softwarePath(), Version: v, NmilatVersion: nmilatVersion()}
}

// nmilatVersion reports the resolved version of the nmilat dependency, e.g.
// "v1.2.3" for a pinned release build, or "(devel)" when running against a
// local checkout via a go.work workspace replace.
func nmilatVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path != nmilatModulePath {
			continue
		}
		if dep.Replace != nil {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return "unknown"
}

func softwarePath() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Path == "" {
		return "unknown"
	}
	return "git+https://" + info.Main.Path
}

func devVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	revision := "unknown"
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
			if len(revision) > 12 {
				revision = revision[:12]
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	v := "dev+" + revision
	if dirty {
		v += "-dirty"
	}
	return v
}
