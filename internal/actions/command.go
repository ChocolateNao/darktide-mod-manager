package actions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// CommandSpec is one external program invocation.
//
// It is a struct rather than a long argument list because the three ways a
// command can be run — directly, through an interpreter, from a working
// directory — are variations of one call.
type CommandSpec struct {
	Path  string
	Dir   string
	Stdin string
	Args  []string
}

// CommandOutput is what an external program produced.
type CommandOutput struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Succeeded reports whether the program exited zero.
func (o CommandOutput) Succeeded() bool { return o.ExitCode == 0 }

// Combined returns stdout and stderr in one string, which is what a result
// message usually wants: a user should not have to know which stream a tool
// preferred.
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

// CommandRunner executes a script action's command line.
//
// The interface exists so the runner can be tested without launching anything.
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
}

// NewExecRunner returns a runner backed by os/exec.
func NewExecRunner() *ExecRunner { return &ExecRunner{} }

// Run executes the command and captures both output streams.
//
// No shell, so a mod name in an argument can never become a command; and a
// non-zero exit is reported as an error *and* returned as output, because a
// tool's diagnostic text is usually the only useful part of its failure.
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

	cmd := exec.CommandContext(ctx, resolved, spec.Args...)
	cmd.Dir = spec.Dir

	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	output := CommandOutput{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}

	if runErr == nil {
		return output, nil
	}

	// A cancelled context means the user pressed stop, which is not a failure of
	// the command and should not be reported as one.
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

// discardCommandRunner refuses to run anything.
//
// It is the default for a runner built without one, so a mis-wired composition
// root produces a clear "no runner" error rather than a panic.
type discardCommandRunner struct{}

// ErrNoCommandRunner is returned when a script action is run without a runner.
var ErrNoCommandRunner = appErrors.New(appErrors.ErrCodeRunnerNotFound,
	"script actions need a command runner")

func (discardCommandRunner) Run(context.Context, CommandSpec) (CommandOutput, error) {
	return CommandOutput{}, ErrNoCommandRunner
}

// captureLogger collects what a handler logged so the result can carry it.
//
// A handler's progress text is otherwise lost: the CLI writes it to the terminal
// and the frontend has nowhere to put it. The buffer is bounded because the
// caller controls what runs.
type captureLogger struct {
	sb   *strings.Builder
	Echo domain.ActionLogger
	// limit is the number of characters retained; lines past it are echoed but
	// dropped, and the truncation is announced once.
	limit int
	// dropped counts the discarded characters, so the result can say so rather
	// than presenting a silently shortened log as complete.
	dropped int
	// announced records that the truncation notice has already been logged.
	announced bool
}

// captureLogLimit bounds a captured handler log.
const captureLogLimit = 64 << 10

func newCaptureLogger(echo domain.ActionLogger) *captureLogger {
	return &captureLogger{sb: &strings.Builder{}, Echo: echo, limit: captureLogLimit}
}

// Printf implements domain.ActionLogger.
func (l *captureLogger) Printf(format string, args ...any) {
	if l == nil {
		return
	}

	line := strings.TrimSuffix(fmt.Sprintf(format, args...), "\n")

	if l.Echo != nil {
		l.Echo.Printf("%s", line)
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

// Text returns everything logged so far.
func (l *captureLogger) Text() string {
	if l == nil {
		return ""
	}

	return l.sb.String()
}
