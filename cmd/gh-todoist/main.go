// Command gh-todoist is a gh CLI extension that pushes hand-picked GitHub
// issues into Todoist.
//
// The sync is one-way by design: GitHub is the source of truth and this tool
// never writes to GitHub.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/albttx/gh-todoist/internal/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cmd.NewRoot(os.Stdout, os.Stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		cmd.ReportError(os.Stderr, err)
		os.Exit(1)
	}
}
