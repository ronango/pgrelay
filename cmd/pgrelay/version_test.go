package main

import (
	"runtime"
	"strings"
	"testing"
)

func withVersion(t *testing.T, v, c string) {
	t.Helper()
	prevV, prevC := Version, Commit
	t.Cleanup(func() { Version = prevV; Commit = prevC })
	Version, Commit = v, c
}

func TestVersionString_WithExplicitCommit(t *testing.T) {
	withVersion(t, "1.2.3", "abcdef1")
	got := versionString()
	for _, want := range []string{"pgrelay", "1.2.3", "abcdef1", runtime.Version()} {
		if !strings.Contains(got, want) {
			t.Errorf("versionString = %q, missing %q", got, want)
		}
	}
}

func TestVersionString_EmptyCommitFallsBackToVCSOrUnknown(t *testing.T) {
	withVersion(t, "dev", "")
	got := versionString()
	// Either go test injected vcs info (run from a git checkout) or
	// it didn't (run from a tarball) — both branches must produce a
	// non-empty string that mentions pgrelay + the Go runtime.
	if !strings.Contains(got, "pgrelay") || !strings.Contains(got, runtime.Version()) {
		t.Errorf("versionString = %q, want pgrelay + go runtime tag", got)
	}
}
