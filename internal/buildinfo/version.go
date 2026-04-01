package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Version is set at build time via -ldflags.
// Default value is used for local/dev builds.
var Version = "dev"

// GitCommit is set at build time via -ldflags.
// Default empty value falls back to VCS metadata when available.
var GitCommit = ""

// VersionString returns a user-facing version string including commit metadata.
func VersionString() string {
	commit := commitSHA()
	if commit == "" {
		return Version
	}
	return Version + " (" + commit + ")"
}

func commitSHA() string {
	if sha := strings.TrimSpace(GitCommit); sha != "" {
		return sha
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return strings.TrimSpace(setting.Value)
		}
	}
	return ""
}
