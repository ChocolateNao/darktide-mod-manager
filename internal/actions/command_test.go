package actions

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// execNames maps a platform-neutral test intention onto a real command, because
// the package under test deliberately has no opinion about what a shell offers.
func execNames(t *testing.T, linux, windows string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		return windows
	}

	return linux
}

func TestExecRunnerRunsAProgram(t *testing.T) {
	name := execNames(t, "echo", "cmd")
	if name == "cmd" {
		t.Skip("the windows shell has different echo semantics")
	}

	runner := NewExecRunner()

	out, err := runner.Run(context.Background(), CommandSpec{
		Path: name,
		Args: []string{"hello"},
		Dir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !out.Succeeded() || out.ExitCode != 0 {
		t.Fatalf("output = %+v", out)
	}

	if strings.TrimSpace(out.Stdout) != "hello" {
		t.Fatalf("stdout = %q", out.Stdout)
	}
}

func TestExecRunnerFeedsStandardInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable stdin reader on windows")
	}

	runner := NewExecRunner()

	out, err := runner.Run(context.Background(), CommandSpec{
		Path:  "cat",
		Stdin: "piped\n",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if out.Stdout != "piped\n" {
		t.Fatalf("stdout = %q", out.Stdout)
	}
}

func TestExecRunnerReportsANonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable failing command on windows")
	}

	runner := NewExecRunner()

	out, err := runner.Run(context.Background(), CommandSpec{
		Path: "sh",
		Args: []string{"-c", "echo oops 1>&2; exit 3"},
	})
	if err == nil {
		t.Fatal("a failing command reported success")
	}

	if !appErrors.IsAppError(err, appErrors.ErrCodeActionFailed) {
		t.Fatalf("error = %v, want ACTION_FAILED", err)
	}

	// The exit code has to survive: a script that fails on a missing file and
	// one that fails on a bad flag are different problems.
	if out.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", out.ExitCode)
	}

	if strings.TrimSpace(out.Stderr) != "oops" {
		t.Fatalf("stderr = %q", out.Stderr)
	}
}

// A script that names a program the machine does not have is a configuration
// problem, and saying "not found" points the user at the fix rather than at the
// action.
func TestExecRunnerReportsAMissingProgram(t *testing.T) {
	runner := NewExecRunner()

	_, err := runner.Run(context.Background(), CommandSpec{Path: "dmm-does-not-exist-anywhere"})
	if !appErrors.IsAppError(err, appErrors.ErrCodeRunnerNotFound) {
		t.Fatalf("error = %v, want RUNNER_NOT_FOUND", err)
	}
}

func TestExecRunnerRefusesAnEmptyPath(t *testing.T) {
	runner := NewExecRunner()

	_, err := runner.Run(context.Background(), CommandSpec{})
	if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

// The runner is reachable from a UI handler, which has no request context; a nil
// context must behave like a background one rather than panic inside exec.
func TestExecRunnerAcceptsANilContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable command on windows")
	}

	runner := NewExecRunner()

	//nolint:staticcheck // exercising the nil-context guard on purpose
	if _, err := runner.Run(nil, CommandSpec{Path: "echo"}); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestExecRunnerHonoursACancelledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable sleeping command on windows")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := NewExecRunner()

	_, err := runner.Run(ctx, CommandSpec{Path: "sleep", Args: []string{"10"}})
	if !appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled) {
		t.Fatalf("error = %v, want OPERATION_CANCELED", err)
	}
}

func TestExecRunnerUsesTheInjectedLookup(t *testing.T) {
	caller := t.TempDir()
	binary := filepath.Join(caller, "dmm-fake-tool")

	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write fake tool: %v", err)
	}

	runner := &ExecRunner{LookPath: func(file string) (string, error) {
		return binary, nil
	}}

	out, err := runner.Run(context.Background(), CommandSpec{Path: "dmm-fake-tool"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if out.ExitCode != 0 {
		t.Fatalf("output = %+v", out)
	}
}

func TestDiscardCommandRunnerRefuses(t *testing.T) {
	var runner CommandRunner = discardCommandRunner{}

	_, err := runner.Run(context.Background(), CommandSpec{Path: "anything"})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestCaptureLoggerBoundsTheBuffer(t *testing.T) {
	echo := &echoLogger{}
	logger := newCaptureLogger(echo)

	for range 100 {
		logger.Printf("%s", strings.Repeat("x", 1024))
	}

	text := logger.Text()
	if len(text) > captureLogLimit {
		t.Fatalf("captured %d bytes, want at most %d", len(text), captureLogLimit)
	}

	if !strings.Contains(text, "output truncated") {
		t.Fatal("the truncation was not announced")
	}

	if !strings.Contains(echo.text(), strings.Repeat("x", 16)) {
		t.Fatal("the echo stopped early")
	}
}

// The truncation notice is announced once: a log that repeats it per line is
// worse than the log it replaced.
func TestCaptureLoggerAnnouncesTruncationOnce(t *testing.T) {
	logger := newCaptureLogger(nil)

	for range 100 {
		logger.Printf("%s", strings.Repeat("y", 1024))
	}

	if got := strings.Count(logger.Text(), "output truncated"); got != 1 {
		t.Fatalf("the notice appeared %d times", got)
	}
}

func TestCaptureLoggerTrimsTrailingNewlines(t *testing.T) {
	logger := newCaptureLogger(nil)

	logger.Printf("line\n")
	logger.Printf("no newline")

	if got := logger.Text(); got != "line\nno newline\n" {
		t.Fatalf("text = %q", got)
	}
}

func TestCommandOutputCombined(t *testing.T) {
	out := CommandOutput{Stdout: "out", Stderr: "err"}

	if out.Combined() != "out\nerr" {
		t.Fatalf("Combined = %q", out.Combined())
	}

	if (CommandOutput{ExitCode: 1}).Succeeded() {
		t.Fatal("a failing command reported success")
	}
}
