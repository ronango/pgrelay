// Command pgrelay is the dispatcher binary entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"
)

// Version and Commit are overridden at build time via -ldflags.
// Commit falls back to runtime/debug VCS info when ldflags aren't set.
var (
	Version = "dev"
	Commit  = ""
)

func main() {
	if err := dispatch(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// dispatch returns instead of calling os.Exit so deferred cleanup (signal
// notify, future OTel flush) actually runs on error paths.
func dispatch() error {
	// SIGINT/SIGTERM cancellation flows into long-running subcommands
	// (run, migrate) via the cli ctx; short ones (version) ignore it.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cmd := &cli.Command{
		Name:  "pgrelay",
		Usage: "Postgres-native transactional outbox dispatcher",
		Commands: []*cli.Command{
			versionCommand(),
		},
	}
	return cmd.Run(ctx, os.Args)
}
