// Package buildinfo reports which build of the panel is running.
//
// This exists because confirming "is the deployed binary the commit I just
// pushed?" otherwise means ssh-ing to the panel host and running sha256sum by
// hand — an unreproducible side channel for a question the service should
// simply answer. A deploy is verifiable over HTTP like everything else.
package buildinfo

import "runtime/debug"

// Stamped by the linker; see the LDFLAGS in the Makefile.
var (
	version = ""
	commit  = ""
	date    = ""
)

// Info describes the running build.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date,omitempty"`
	Dirty   bool   `json:"dirty,omitempty"`
}

// Get returns the build stamp, falling back to the VCS data the Go toolchain
// embeds automatically. The fallback matters for `go build` without the
// Makefile — a bare build should still identify itself rather than claim to be
// an unknown binary.
func Get() Info {
	info := Info{Version: version, Commit: commit, Date: date}
	buildSettings, ok := debug.ReadBuildInfo()
	if !ok {
		return withDefaults(info)
	}
	for _, setting := range buildSettings.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = shortCommit(setting.Value)
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = setting.Value
			}
		case "vcs.modified":
			// A binary built from uncommitted changes cannot be reproduced
			// from git, so say so instead of reporting a commit that does not
			// describe what is actually running.
			info.Dirty = setting.Value == "true"
		}
	}
	return withDefaults(info)
}

func withDefaults(info Info) Info {
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	return info
}

func shortCommit(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
