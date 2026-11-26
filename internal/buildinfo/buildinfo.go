// Package buildinfo exposes version-control and release metadata embedded in vox binaries.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// These values are replaced by GoReleaser through -ldflags. Fallback values
// remain informative for local builds and `go install`.
var (
	version   = "dev"
	commit    = ""
	date      = ""
	treeState = ""
)

// Info describes the source revision and toolchain used to build a vox binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	Dirty     bool   `json:"dirty"`
	GoVersion string `json:"go_version"`
}

// Current returns linker-injected metadata supplemented by Go's VCS build settings.
func Current() Info {
	info := Info{
		Version:   normalizeVersion(version),
		Commit:    commit,
		Date:      date,
		Dirty:     treeState == "dirty",
		GoVersion: runtime.Version(),
	}

	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if info.Version == "dev" && build.Main.Version != "" && build.Main.Version != "(devel)" {
		info.Version = normalizeVersion(build.Main.Version)
	}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = setting.Value
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = setting.Value
			}
		case "vcs.modified":
			if treeState == "" {
				info.Dirty, _ = strconv.ParseBool(setting.Value)
			}
		}
	}
	return info
}

// String returns a concise, human-readable version line suitable for --version.
func (info Info) String() string {
	result := "vox version " + info.Version
	if info.Commit != "" {
		result += " (commit " + shortCommit(info.Commit)
		if info.Dirty {
			result += ", dirty"
		}
		result += ")"
	} else if info.Dirty {
		result += " (dirty)"
	}
	if info.Date != "" {
		result += ", built " + info.Date
	}
	return result
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "(devel)" {
		return "dev"
	}
	return strings.TrimPrefix(value, "v")
}

func shortCommit(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
