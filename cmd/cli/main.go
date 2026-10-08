// Command dmm is the darktide-mod-manager command line interface.
//
// The whole program is the one call to run at the bottom of this file. Building
// the container needs a context, the command line needs a place to write, and the
// exit code is the only thing the shell wants back. Everything a test would want
// to assert about behaviour lives in package commands, which can be driven
// without a process; what is tested here is the wiring between the two — that the
// entry point's context reaches the container, that the streams reach the
// commands, and that the exit code comes back.
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

// build is overridden in tests of this package. It is a variable rather than a
// call to di.NewContainer directly so the entry point's own wiring — the part
// that has to be right even though it is three lines — can be checked without
// standing up a container.
var build = di.NewContainer

func main() {
	// Ctrl-C cancels the command in progress rather than killing the process
	// outright. The difference matters: a killed download leaves a half-written
	// staging file, and the manager would report a crash where a cancelled
	// operation is an ordinary outcome. A second interrupt is left to the
	// default handler, so a command that ignores the signal can still be
	// stopped.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main's body with its inputs as parameters. The streams are interfaces
// rather than *os.File so a test can hand it buffers; main passes the real files,
// which satisfy them.
func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	env := commands.Env{
		Out: out,
		Err: errOut,
		In:  in,
		NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
			// The signal context is not passed to the container: the container
			// outlives any one command's context when the command tree builds
			// it once, and a container whose start-up was cancelled by a
			// Ctrl-C would stay cancelled for the rest of the run. Each command
			// derives its own cancellable context from this one instead.
			return build(ctx, opts)
		},
	}

	return commands.RunContext(ctx, env, args)
}
