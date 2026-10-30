package actions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// CommandSpec is one external program invocation: directly, through an
// interpreter, or from a working directory.
type CommandSpec struct {
	Path  string
	Dir   string
	Stdin string
	Args  []string
	// Action is the game action that requested this command, when there is one.
	Action domain.ActionID
	// Detached starts the process and returns without waiting for it, so a game
	// outlives the action that launched it.
	Detached bool
}

// CommandOutput is what an external program produced.
type CommandOutput struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func (o CommandOutput) Succeeded() bool { return o.ExitCode == 0 }

// Combined returns stdout and stderr in one string, so a user need not know
// which stream a tool preferred.
func (o CommandOutput) Combined() string {
	switch {
	case strings.TrimSpace(o.Stderr) == "":
		return o.Stdout
	case strings.TrimSpace(o.Stdout) == "":
		return o.Stderr
	default:
		return o.Stdout + "\n" + o.Stderr
	}
}

// CommandRunner executes a script action's command line. Tests substitute it to
// avoid launching processes.
type CommandRunner interface {
	Run(ctx context.Context, spec CommandSpec) (CommandOutput, error)
}

// LookPathFunc is the signature of exec.LookPath, injected so a test can pretend
// a program exists without creating it.
type LookPathFunc func(file string) (string, error)

// ExecRunner runs commands as real child processes.
type ExecRunner struct {
	// LookPath resolves a program name. Nil means exec.LookPath.
	LookPath LookPathFunc
	// Logger records each process's start and finish. Nil discards.
	Logger *zerolog.Logger
}

// nopLogger is where process events go when the runner was built without one.
var nopLogger = zerolog.Nop()

func (r *ExecRunner) logger() *zerolog.Logger {
	if r == nil || r.Logger == nil {
		return &nopLogger
	}

	return r.Logger
}

func NewExecRunner() *ExecRunner { return &ExecRunner{} }

// Run executes the command and captures both output streams.
//
// No shell is involved, so a mod name in an argument cannot become a command.
// A non-zero exit returns both an error and the output, whose diagnostic text
// is usually the only useful part of a failure.
func (r *ExecRunner) Run(ctx context.Context, spec CommandSpec) (CommandOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return CommandOutput{}, appErrors.Wrap(err,
			appErrors.ErrCodeOperationCanceled, "action command %q canceled", spec.Path)
	}

	if strings.TrimSpace(spec.Path) == "" {
		return CommandOutput{}, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action command has no program to run")
	}

	look := r.LookPath
	if look == nil {
		look = exec.LookPath
	}

	resolved, err := look(spec.Path)
	if err != nil {
		return CommandOutput{}, appErrors.Wrap(err, appErrors.ErrCodeRunnerNotFound,
			"action command %q was not found on PATH", spec.Path)
	}

	var cmd *exec.Cmd
	if spec.Detached {
		// Not CommandContext: a cancelled request would kill the game.
		cmd = exec.Command(resolved, spec.Args...)
	} else {
		cmd = exec.CommandContext(ctx, resolved, spec.Args...)
	}

	cmd.Dir = spec.Dir

	var stdout, stderr bytes.Buffer

	if !spec.Detached {
		if spec.Stdin != "" {
			cmd.Stdin = strings.NewReader(spec.Stdin)
		}

		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	start := time.Now()

	startEvent := r.logger().Debug().
		Str("op", "action-process").
		Str("action", spec.Action.String()).
		Str("cmd", resolved).
		Strs("args", spec.Args)

	if spec.Dir != "" {
		startEvent = startEvent.Str("dir", spec.Dir)
	}

	startEvent.Msg("action process start")

	if spec.Detached {
		if err := cmd.Start(); err != nil {
			return CommandOutput{}, appErrors.Wrap(err, appErrors.ErrCodeActionFailed,
				"action command %q could not be started", spec.Path)
		}

		// Release hands the child to the OS; without it Go reaps it on exit.
		_ = cmd.Process.Release()

		r.logger().Debug().
			Str("op", "action-process").
			Str("action", spec.Action.String()).
			Str("cmd", resolved).
			Dur("duration", time.Since(start)).
			Msg("action process detached")

		return CommandOutput{}, nil
	}

	runErr := cmd.Run()

	output := CommandOutput{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}

	r.logger().Debug().
		Str("op", "action-process").
		Str("action", spec.Action.String()).
		Str("cmd", resolved).
		Strs("args", spec.Args).
		Int("exit_code", output.ExitCode).
		Dur("duration", time.Since(start)).
		Msg("action process finish")

	if runErr == nil {
		return output, nil
	}

	// A cancelled context means the user pressed stop, which is a user choice and
	// should not be reported as a command failure.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return output, appErrors.Wrap(ctxErr, appErrors.ErrCodeOperationCanceled,
			"action command %q canceled", spec.Path)
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return output, appErrors.Wrap(runErr, appErrors.ErrCodeActionFailed,
			"action command %q exited with code %d", spec.Path, output.ExitCode)
	}

	if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, os.ErrNotExist) {
		return output, appErrors.Wrap(runErr, appErrors.ErrCodeRunnerNotFound,
			"action command %q could not be started", spec.Path)
	}

	return output, appErrors.Wrap(runErr, appErrors.ErrCodeActionFailed,
		"action command %q could not be run", spec.Path)
}

// discardCommandRunner refuses to run anything. It backs a runner built without
// one, so a mis-wired composition root gets a clear "no runner" error.
type discardCommandRunner struct{}

// ErrNoCommandRunner is returned when a script action is run without a runner.
var ErrNoCommandRunner = appErrors.New(appErrors.ErrCodeRunnerNotFound,
	"script actions need a command runner")

func (discardCommandRunner) Run(context.Context, CommandSpec) (CommandOutput, error) {
	return CommandOutput{}, ErrNoCommandRunner
}

// captureLogger collects what a handler logged so the result can carry it. The
// buffer is bounded because the caller controls what runs.
type captureLogger struct {
	sb   *strings.Builder
	Echo domain.ActionLogger
	// limit is the number of characters retained; later lines are echoed but
	// dropped.
	limit int
	// dropped counts discarded characters, so a shortened log does not read as
	// complete.
	dropped   int
	announced bool
}

const captureLogLimit = 64 << 10

func newCaptureLogger(echo domain.ActionLogger) *captureLogger {
	return &captureLogger{sb: &strings.Builder{}, Echo: echo, limit: captureLogLimit}
}

// Logf implements domain.ActionLogger.
func (l *captureLogger) Logf(format string, args ...any) {
	if l == nil {
		return
	}

	line := strings.TrimSuffix(fmt.Sprintf(format, args...), "\n")

	if l.Echo != nil {
		l.Echo.Logf("%s", line)
	}

	if l.sb.Len()+len(line)+1 > l.limit {
		l.dropped += len(line) + 1

		if !l.announced {
			l.announced = true

			l.sb.WriteString("[output truncated]")
			l.sb.WriteString("\n")
		}

		return
	}

	l.sb.WriteString(line)
	l.sb.WriteString("\n")
}

func (l *captureLogger) Text() string {
	if l == nil {
		return ""
	}

	return l.sb.String()
}
