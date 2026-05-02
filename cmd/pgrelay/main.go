// Command pgrelay is the dispatcher binary entrypoint.
package main

import (
	"fmt"
	"runtime/debug"
)

// Version and Commit are overridden at build time via -ldflags.
// Commit falls back to runtime/debug VCS info when ldflags aren't set.
var (
	Version = "dev"
	Commit  = ""
)

func main() {
	fmt.Println(versionString())
}

func versionString() string {
	commit := Commit
	suffix := ""
	if commit == "" {
		commit, suffix = vcsRevision()
	}
	if commit == "" {
		return fmt.Sprintf("pgrelay %s (commit unknown — built without --build-arg COMMIT)", Version)
	}
	return fmt.Sprintf("pgrelay %s (commit %s%s)", Version, commit, suffix)
}

func vcsRevision() (revision, suffix string) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				revision = s.Value[:min(len(s.Value), 7)]
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if modified {
		suffix = "+dirty"
	}
	return revision, suffix
}
