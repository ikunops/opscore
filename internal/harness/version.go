package harness

import "runtime/debug"

// Version is the build version, overridable via -ldflags
// "-X github.com/YuDong999/opscore/internal/harness.Version=vX.Y.Z". It is
// observability metadata only (A-7) — never a control or event-ownership channel.
var Version = "dev"

// versionInfo is the read-only build/version payload exposed by /versionz.
type versionInfo struct {
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
	Module    string `json:"module"`
	Schema    string `json:"configSchema"`
}

// buildVersionInfo collects build metadata for observability exposure (A-7).
func buildVersionInfo() versionInfo {
	info := versionInfo{
		Version: Version,
		Schema:  configSchemaVersion,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.GoVersion = bi.GoVersion
		info.Module = bi.Main.Path
	}
	return info
}
