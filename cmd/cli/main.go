// Package dmm is the darktide-mod-manager command line interface.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ChocolateNao/darktide-mod-manager/cmd/cli/commands"
	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
)

// build is overridden in tests of this package so the entry point's wiring can
// be checked without standing up a container.
var build = di.NewContainer

func main() {
	// Ctrl-C cancels the command in progress so it does not leave a half-written
	// staging file; a second interrupt is fatal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	env := commands.Env{
		Out: out,
		Err: errOut,
		In:  in,
		NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
			// The container outlives any one command's context, so a start-up
			// cancelled by Ctrl-C would stay cancelled.
			return build(ctx, opts)
		},
	}

	return commands.RunContext(ctx, env, args)
}
