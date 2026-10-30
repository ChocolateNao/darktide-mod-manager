package actions

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// execNames maps a platform-neutral intent onto a real command; the package
// under test has no opinion about what a shell offers.
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
	require.NoError(t, err, "run: %v", err)

	assert.True(t, out.Succeeded(), "output = %+v", out)
	assert.Equal(t, 0, out.ExitCode, "output = %+v", out)

	assert.Equal(t, "hello", strings.TrimSpace(out.Stdout), "stdout = %q", out.Stdout)
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
	require.NoError(t, err, "run: %v", err)

	assert.Equal(t, "piped\n", out.Stdout, "stdout = %q", out.Stdout)
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
	require.Error(t, err, "a failing command reported success")

	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeActionFailed), "error = %v, want ACTION_FAILED", err)

	// The exit code has to survive: a missing file and a bad flag are different
	// problems.
	assert.Equal(t, 3, out.ExitCode, "exit code = %d, want 3", out.ExitCode)

	assert.Equal(t, "oops", strings.TrimSpace(out.Stderr), "stderr = %q", out.Stderr)
}

func TestExecRunnerDoesNotWaitForADetachedProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable failing command on windows")
	}

	runner := NewExecRunner()

	// The command fails after it starts; a waiter would surface that exit code.
	out, err := runner.Run(context.Background(), CommandSpec{
		Path:     "sh",
		Args:     []string{"-c", "sleep 0.2; exit 17"},
		Detached: true,
	})
	require.NoError(t, err, "detached run: %v", err)
	assert.Equal(t, 0, out.ExitCode, "detached run waited: %+v", out)
}

func TestExecRunnerReportsAMissingProgram(t *testing.T) {
	runner := NewExecRunner()

	_, err := runner.Run(context.Background(), CommandSpec{Path: "dmm-does-not-exist-anywhere"})
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeRunnerNotFound), "error = %v, want RUNNER_NOT_FOUND", err)
}

func TestExecRunnerRefusesAnEmptyPath(t *testing.T) {
	runner := NewExecRunner()

	_, err := runner.Run(context.Background(), CommandSpec{})
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument), "error = %v, want INVALID_ARGUMENT", err)
}

// A UI handler has no request context; nil must behave like a background one.
func TestExecRunnerAcceptsANilContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable command on windows")
	}

	runner := NewExecRunner()

	//nolint:staticcheck // exercising the nil-context guard on purpose
	_, err := runner.Run(nil, CommandSpec{Path: "echo"})
	assert.NoError(t, err, "run: %v", err)
}

func TestExecRunnerHonoursACancelledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable sleeping command on windows")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := NewExecRunner()

	_, err := runner.Run(ctx, CommandSpec{Path: "sleep", Args: []string{"10"}})
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled), "error = %v, want OPERATION_CANCELED", err)
}

func TestExecRunnerUsesTheInjectedLookup(t *testing.T) {
	caller := t.TempDir()
	binary := filepath.Join(caller, "dmm-fake-tool")

	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700),
		"write fake tool")

	runner := &ExecRunner{LookPath: func(file string) (string, error) {
		return binary, nil
	}}

	out, err := runner.Run(context.Background(), CommandSpec{Path: "dmm-fake-tool"})
	require.NoError(t, err, "run: %v", err)

	assert.Equal(t, 0, out.ExitCode, "output = %+v", out)
}

func TestDiscardCommandRunnerRefuses(t *testing.T) {
	var runner CommandRunner = discardCommandRunner{}

	_, err := runner.Run(context.Background(), CommandSpec{Path: "anything"})
	assert.Error(t, err, "expected an error")
}

func TestCaptureLoggerBoundsTheBuffer(t *testing.T) {
	echo := &echoLogger{}
	logger := newCaptureLogger(echo)

	for range 100 {
		logger.Logf("%s", strings.Repeat("x", 1024))
	}

	text := logger.Text()
	assert.LessOrEqual(t, len(text), captureLogLimit, "captured %d bytes, want at most %d", len(text), captureLogLimit)

	assert.Contains(t, text, "output truncated", "the truncation was not announced")

	assert.Contains(t, echo.text(), strings.Repeat("x", 16), "the echo stopped early")
}

// Repeating the truncation notice per line would be worse than the log it replaced.
func TestCaptureLoggerAnnouncesTruncationOnce(t *testing.T) {
	logger := newCaptureLogger(nil)

	for range 100 {
		logger.Logf("%s", strings.Repeat("y", 1024))
	}

	got := strings.Count(logger.Text(), "output truncated")
	assert.Equal(t, 1, got, "the notice appeared %d times", got)
}

func TestCaptureLoggerTrimsTrailingNewlines(t *testing.T) {
	logger := newCaptureLogger(nil)

	logger.Logf("line\n")
	logger.Logf("no newline")

	assert.Equal(t, "line\nno newline\n", logger.Text(), "text = %q", logger.Text())
}

func TestCommandOutputCombined(t *testing.T) {
	out := CommandOutput{Stdout: "out", Stderr: "err"}

	assert.Equal(t, "out\nerr", out.Combined(), "Combined = %q", out.Combined())

	assert.False(t, (CommandOutput{ExitCode: 1}).Succeeded(), "a failing command reported success")
}
