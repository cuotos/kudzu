package httpapi

import (
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"
)

// versionInfo is what GET /versionz reports. Everything comes from the binary's
// own build info, so nothing has to be threaded in at build time.
type versionInfo struct {
	// Version is the main module version: a semver tag for a `go install`ed
	// build, or "(devel)" for one built from a working tree.
	Version string `json:"version"`
	// Revision, Time and Dirty come from the VCS stamp the toolchain adds when
	// it can see the repository. They are absent when it cannot — notably in a
	// container built from a context with no .git.
	Revision string `json:"revision,omitempty"`
	Time     string `json:"time,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`

	Module string `json:"module,omitempty"`
	Go     string `json:"go"`
	OS     string `json:"os,omitempty"`
	Arch   string `json:"arch,omitempty"`
}

// buildVersion reads the build info once; it cannot change while running.
var buildVersion = sync.OnceValue(readBuildInfo)

func readBuildInfo() versionInfo {
	v := versionInfo{Version: "unknown", Go: runtime.Version()}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	v.Module = info.Main.Path
	if info.GoVersion != "" {
		v.Go = info.GoVersion
	}
	if info.Main.Version != "" {
		v.Version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = s.Value
		case "vcs.time":
			v.Time = s.Value
		case "vcs.modified":
			v.Dirty = s.Value == "true"
		case "GOOS":
			v.OS = s.Value
		case "GOARCH":
			v.Arch = s.Value
		}
	}
	return v
}

func (s *Server) handleVersionz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildVersion())
}
