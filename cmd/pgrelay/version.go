package main

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/urfave/cli/v3"
)

func versionCommand() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print version, commit, and Go runtime",
		Action: func(_ context.Context, _ *cli.Command) error {
			fmt.Println(versionString())
			return nil
		},
	}
}

func versionString() string {
	commit := Commit
	suffix := ""
	if commit == "" {
		commit, suffix = vcsRevision()
	}
	if commit == "" {
		return fmt.Sprintf("pgrelay %s (commit unknown, %s)", Version, runtime.Version())
	}
	return fmt.Sprintf("pgrelay %s (commit %s%s, %s)", Version, commit, suffix, runtime.Version())
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
