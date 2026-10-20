package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExitCodeTellsCancellationFromFailure(t *testing.T) {
	tests := []struct {
		err  error
		name string
		note string
		want int
	}{
		{
			name: "success",
			err:  nil,
			want: ExitOK,
		},
		{
			name: "an ordinary failure",
			err:  appErrors.New(appErrors.ErrCodeModNotFound, "no such mod"),
			want: ExitError,
		},
		{
			name: "a manager cancellation",
			err:  appErrors.New(appErrors.ErrCodeOperationCanceled, "the operation was canceled"),
			want: ExitCanceled,
			note: "\"I pressed ctrl-c\" is not the same event as \"this failed\"",
		},
		{
			name: "a wrapped cancellation",
			err: appErrors.Wrap(appErrors.New(appErrors.ErrCodeInstallFailed, "install failed"),
				appErrors.ErrCodeOperationCanceled, "the update was canceled"),
			want: ExitCanceled,
			note: "the code travels through a wrap, and the cause is still the original failure",
		},
		{
			name: "a bare context cancellation",
			err:  context.Canceled,
			want: ExitCanceled,
			note: "a context error that never became an AppError is still a cancellation",
		},
		{
			name: "a wrapped bare cancellation",
			err:  errors.Join(errors.New("while saving"), context.Canceled),
			want: ExitCanceled,
			note: "several failures can arrive joined, and one of them being a cancellation is enough",
		},
		{
			name: "an untyped error",
			err:  errors.New("something went wrong"),
			want: ExitError,
			note: "an error with no code is a failure, not a cancellation",
		},
		{
			name: "a deadline is not a cancellation",
			err:  appErrors.New(appErrors.ErrCodeSourceUnavailable, "the source timed out"),
			want: ExitError,
			note: "a timeout is a failure the user did not ask for; only they can cancel",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := exitCode(test.err)
			assert.Equal(t, test.want, got, "exitCode(%v) = %d, want %d", test.err, got, test.want)
		})
	}
}

func TestOneLineFoldsAMultiLineError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
		note string
	}{
		{
			name: "a single line is untouched",
			err:  errors.New("mod \"x\" is not installed"),
			want: "mod \"x\" is not installed",
		},
		{
			name: "a newline becomes a space",
			err:  errors.New("first\nsecond"),
			want: "first second",
			note: "one failure must not become a paragraph a caller has to parse",
		},
		{
			name: "windows line endings too",
			err:  errors.New("first\r\nsecond"),
			want: "first second",
			note: "a message that came from Windows on a Unix host would otherwise keep both characters",
		},
		{
			name: "runs of spaces collapse",
			err:  errors.New("a    b  c"),
			want: "a b c",
			note: "indented multi-line text leaves the indentation behind when it is folded",
		},
		{
			name: "surrounding whitespace goes",
			err:  errors.New("\n  message  \n"),
			want: "message",
		},
		{
			name: "the code prefix is kept",
			err:  appErrors.New(appErrors.ErrCodeGameNotFound, "no installation was found"),
			want: "GAME_NOT_FOUND: no installation was found",
			note: "the code is the part a script branches on, and it is the only place a script sees it",
		},
		{
			name: "the cause is kept too",
			err: appErrors.Wrap(
				errors.New("no rows"),
				appErrors.ErrCodeModNotFound,
				"mod %q is not installed",
				"x",
			),
			want: "MOD_NOT_FOUND: mod \"x\" is not installed: no rows",
			note: "one line that still says everything beats a readable paragraph",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := oneLine(test.err)
			assert.Equal(t, test.want, got, "oneLine(%v) = %q, want %q", test.err, got, test.want)
		})
	}
}

func TestEnvWithDefaultsFillsOnlyWhatIsMissing(t *testing.T) {
	// A nil writer is a caller mistake; a panic here would say nothing about the
	// command.
	env := Env{}.withDefaults()
	require.NotNil(t, env.Out, "withDefaults left a field nil: %+v", env)
	require.NotNil(t, env.Err, "withDefaults left a field nil: %+v", env)
	require.NotNil(t, env.In, "withDefaults left a field nil: %+v", env)
	_, err := env.Out.Write([]byte("out"))
	assert.NoError(t, err, "writing to a defaulted Out failed: %v", err)

	_, err = env.Err.Write([]byte("err"))
	assert.NoError(t, err, "writing to a defaulted Err failed: %v", err)

	read, err := env.In.Read(make([]byte, 1))
	require.Error(t, err)
	assert.Equal(t, 0, read, "a defaulted In produced %d bytes, want an empty reader", read)

	// A supplied writer must survive, or a caller's captured output would vanish.
	var out, errOut strings.Builder

	given := Env{Out: &out, Err: &errOut, In: strings.NewReader("x")}.withDefaults()
	_, err = given.Out.Write([]byte("kept"))
	require.NoError(t, err, "write")

	assert.Equal(
		t,
		"kept",
		out.String(),
		"withDefaults replaced a supplied writer: %q",
		out.String(),
	)

	assert.Equal(
		t,
		"",
		errOut.String(),
		"withDefaults wrote to the supplied writer by itself: %q",
		errOut.String(),
	)
}

func TestOneInvocationBuildsOneContainer(t *testing.T) {
	// One invocation is one process; a container rebuilt mid-run could disagree
	// with itself.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	builds := 0

	var out, errOut strings.Builder

	env := Env{
		Out: &out, Err: &errOut,
		NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
			builds++
			return di.NewContainer(ctx, opts)
		},
	}
	// Each is a separate invocation, so the counter goes up once per invocation.
	invocations := [][]string{
		w.args("mod", "list"),
		w.args("source", "list"),
		w.args("cache", "info"),
	}
	for i, args := range invocations {
		code := RunContext(context.Background(), env, args)
		require.Equal(
			t,
			ExitOK,
			code,
			"%v exited %d, want 0 (stderr: %s)",
			args,
			code,
			errOut.String(),
		)

		require.Equal(
			t,
			i+1,
			builds,
			"after %d invocation(s) %d containers were built, want %d",
			i+1,
			builds,
			i+1,
		)

		errOut.Reset()
	}
}

func TestVersionAndHelpBuildNoContainer(t *testing.T) {
	// Version and help must work when the settings file is unreadable.
	for _, args := range [][]string{{"version"}, {"--help"}, {"mod", "--help"}, {"mod", "list", "--help"}} {
		builds := 0

		var out, errOut strings.Builder

		env := Env{
			Out: &out, Err: &errOut,
			NewContainer: func(context.Context, di.Options) (*di.Container, error) {
				builds++
				return nil, errors.New("the container must not be built for this command")
			},
		}
		code := RunContext(context.Background(), env, args)
		assert.Equal(
			t,
			ExitOK,
			code,
			"%v exited %d, want 0 (stderr: %s)",
			args,
			code,
			errOut.String(),
		)

		assert.Equal(t, 0, builds, "%v built a container", args)

		assert.NotEmpty(t, out.String(), "%v printed nothing at all", args)
	}
}

func TestAMissingContainerFactoryIsReportedNotPanicked(t *testing.T) {
	// A caller that forgot the factory gets an error naming it, never a nil
	// dereference inside a command.
	var out, errOut strings.Builder

	env := Env{Out: &out, Err: &errOut}

	code := RunContext(context.Background(), env, []string{"mod", "list"})
	assert.Equal(t, ExitError, code, "exited %d, want %d", code, ExitError)

	assert.Contains(
		t,
		errOut.String(),
		"no container factory",
		"the diagnostic does not say what is missing: %q",
		errOut.String(),
	)
}

func TestABuildFailureIsRememberedAndReportedOnce(t *testing.T) {
	// Retrying a start-up already shown to fail would repeat or replace its message.
	builds := 0

	var out, errOut strings.Builder

	env := Env{
		Out: &out, Err: &errOut,
		NewContainer: func(context.Context, di.Options) (*di.Container, error) {
			builds++

			return nil, appErrors.New(
				appErrors.ErrCodeConfigInvalid,
				"the settings file is not JSON",
			)
		},
	}
	// `mod list` asks for the client more than once, exercising the second acquire.
	code := RunContext(context.Background(), env, []string{"mod", "list"})
	assert.Equal(t, ExitError, code, "exited %d, want %d", code, ExitError)

	assert.Equal(t, 1, builds, "built %d containers after a failure, want 1", builds)

	assert.Contains(
		t,
		errOut.String(),
		"the settings file is not JSON",
		"the underlying reason was lost: %q",
		errOut.String(),
	)
}

func TestSessionAcquireRemembersAFailure(t *testing.T) {
	builds := 0
	s := &session{env: Env{
		NewContainer: func(context.Context, di.Options) (*di.Container, error) {
			builds++
			return nil, appErrors.New(appErrors.ErrCodeConfigInvalid, "nope")
		},
	}}
	first, firstErr := s.acquire(context.Background())
	second, secondErr := s.acquire(context.Background())

	assert.Equal(t, 1, builds, "acquire built %d times, want 1", builds)

	assert.Nil(t, first, "acquire returned a container for a failing factory: %v %v", first, second)
	assert.Nil(
		t,
		second,
		"acquire returned a container for a failing factory: %v %v",
		first,
		second,
	)

	require.Error(t, firstErr, "acquire reported success for a failing factory")
	require.Error(t, secondErr, "acquire reported success for a failing factory")
	// The same error both times: two reports of one problem must not disagree.
	assert.Equal(
		t,
		firstErr.Error(),
		secondErr.Error(),
		"the remembered failure changed:\nfirst  %v\nsecond %v",
		firstErr,
		secondErr,
	)
}

func TestSessionCloseIsSafeWithoutAContainer(t *testing.T) {
	s := &session{}
	err := s.close()
	assert.NoError(t, err, "closing a session that built nothing reported %v", err)
	// Closing twice is what a deferred close looks like after a failure.
	err = s.close()
	assert.NoError(t, err, "the second close reported %v", err)
}

func TestGlobalFlagsReachTheContainer(t *testing.T) {
	// A flag parsed but not forwarded silently does nothing.
	w := newWorkspace(t)
	// --game takes a registry id, resolved at start-up during the build below.
	w.makeGame(t)
	other := t.TempDir()

	var (
		seen        di.Options
		out, errOut strings.Builder
	)

	env := Env{
		Out: &out, Err: &errOut,
		NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
			seen = opts
			return di.NewContainer(ctx, opts)
		},
	}

	args := []string{
		"--config", w.configPath,
		"--cache", w.cacheRoot,
		"--storage", w.storageRoot,
		"--game", "darktide",
		"cache", "info",
	}
	code := RunContext(context.Background(), env, args)
	require.Equal(t, ExitOK, code, "exited %d, want 0 (stderr: %s)", code, errOut.String())

	assert.Equal(
		t,
		w.configPath,
		seen.ConfigPath,
		"--config reached the container as %q, want %q",
		seen.ConfigPath,
		w.configPath,
	)

	assert.Equal(
		t,
		w.cacheRoot,
		seen.CachePath,
		"--cache reached the container as %q, want %q",
		seen.CachePath,
		w.cacheRoot,
	)

	assert.Equal(
		t,
		w.storageRoot,
		seen.StoragePath,
		"--storage reached the container as %q, want %q",
		seen.StoragePath,
		w.storageRoot,
	)

	assert.Equal(
		t,
		"darktide",
		seen.GameID,
		"--game reached the container as %q, want %q",
		seen.GameID,
		"darktide",
	)

	assert.NotNil(
		t,
		seen.Logger,
		"no logger reached the container, so start-up diagnostics would be lost",
	)
	// The flags must not be trimmed into nothing by a path that "normalises" them.
	assert.NotEqual(t, other, seen.ConfigPath, "the test is not checking what it claims to check")
}

func TestAnUnsetPathIsPassedThroughBlank(t *testing.T) {
	// di treats a blank path as "no opinion" and falls back to the settings file.
	var (
		seen        di.Options
		out, errOut strings.Builder
	)

	env := Env{
		Out: &out, Err: &errOut,
		NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
			seen = opts
			return nil, appErrors.New(appErrors.ErrCodeConfigInvalid, "stop here")
		},
	}
	RunContext(context.Background(), env, []string{"mod", "list"})

	for name, got := range map[string]string{
		"ConfigPath":  seen.ConfigPath,
		"CachePath":   seen.CachePath,
		"StoragePath": seen.StoragePath,
		"GamePath":    seen.GamePath,
		"GameID":      seen.GameID,
	} {
		assert.Equal(t, "", got, "%s was invented as %q with no flag given", name, got)
	}
}

func TestQuietSilencesStartUpDiagnosticsButNotErrors(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	// A source that cannot be built produces a diagnostic, so --quiet shows only
	// in the failing run below.
	var errOut strings.Builder

	env := Env{
		Out: &strings.Builder{}, Err: &errOut,
		NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
			return di.NewContainer(ctx, opts)
		},
	}

	args := append(
		[]string{"--config", w.configPath, "--cache", w.cacheRoot, "--storage", w.storageRoot},
		"config",
		"set",
		"ui.theme",
		"dark",
	)
	code := RunContext(context.Background(), env, args)
	require.Equal(
		t,
		ExitOK,
		code,
		"a successful command exited %d, want 0 (stderr: %s)",
		code,
		errOut.String(),
	)

	assert.Equal(t, 0, errOut.Len(), "a quiet run printed a diagnostic: %q", errOut.String())

	errOut.Reset()

	failing := append(
		[]string{
			"--quiet",
			"--config",
			w.configPath,
			"--cache",
			w.cacheRoot,
			"--storage",
			w.storageRoot,
		},
		"mod",
		"show",
		"nothing-here",
	)
	code = RunContext(context.Background(), env, failing)
	assert.Equal(t, ExitError, code, "a failing command exited %d, want %d", code, ExitError)
	// --quiet silences start-up chatter.
	assert.Contains(
		t,
		errOut.String(),
		"error:",
		"--quiet swallowed the error a failed command must report: %q",
		errOut.String(),
	)

	assert.NotContains(
		t,
		errOut.String(),
		"warning:",
		"--quiet left a warning in place: %q",
		errOut.String(),
	)
}

func TestTheGameFlagNamesAGameAndRefusesUnknownOnes(t *testing.T) {
	// An unknown --game fails start-up: a typo that runs the wrong game is worse
	// than one that stops.
	w := newWorkspace(t)
	w.makeGame(t)

	run := func(extra ...string) (int, string, string) {
		var out, errOut strings.Builder

		env := Env{
			Out: &out, Err: &errOut,
			NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
				return di.NewContainer(ctx, opts)
			},
		}

		args := append(
			[]string{
				"--config", w.configPath,
				"--cache", w.cacheRoot,
				"--storage", w.storageRoot,
			},
			extra...,
		)
		code := RunContext(context.Background(), env, append(args, "game", "current"))

		return code, out.String(), errOut.String()
	}

	{
		code, out, errOut := run("--game", "darktide")
		require.Equal(
			t,
			ExitOK,
			code,
			"--game darktide exited %d, want 0 (stderr: %s)",
			code,
			errOut,
		)
		assert.Contains(t, out, "darktide", "`game current` did not report the named game: %q", out)
	}

	{
		code, _, errOut := run("--game", "not-a-game")
		assert.Equal(t, ExitError, code, "--game not-a-game exited %d, want %d", code, ExitError)
		assert.Contains(t, errOut, "not registered", "nothing explained the refusal: %q", errOut)
	}
}

func TestAnUnknownCommandIsRefusedWithASuggestion(t *testing.T) {
	// SilenceErrors means this program prints the one line, so cobra's message
	// has to reach the terminal intact.
	var out, errOut strings.Builder

	env := Env{Out: &out, Err: &errOut}

	code := RunContext(context.Background(), env, []string{"mods", "list"})
	assert.Equal(t, ExitError, code, "a mistyped group exited %d, want %d", code, ExitError)

	assert.Contains(t, errOut.String(), "error:", "nothing was reported: %q", errOut.String())
}

func TestNewRootCommandHasTheWholeTree(t *testing.T) {
	// The tree is a contract with the user: a documented command that does not
	// exist is a bug no other test finds.
	root := NewRootCommand(Env{})
	found := map[string]bool{}

	var walk func(*cobra.Command)

	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			found[child.Name()] = true
			walk(child)
		}
	}
	walk(root)

	want := []string{
		"mod", "source", "game", "cache", "config", "manifest", "version",
		"list", "show", "install", "uninstall", "enable", "disable", "update",
		"reorder", "path", "relink", "symlink-status", "conflicts", "share", "import-link",
		"hooks",
		"search", "auth", "test",
		"detect", "validate", "info", "use", "current",
		"clear", "prune",
		"show", "get", "set", "edit",
		"init",
	}
	for _, name := range want {
		assert.True(t, found[name], "the tree has no %q command", name)
	}
	// "list", "show" and "info" appear under more than one group. The count
	// catches a tree that failed to assemble.
	assert.GreaterOrEqual(t, len(found), len(want),
		"the walk found %d commands, fewer than the %d the contract names",
		len(found),
		len(want),
	)
	// Cobra adds "help" and "completion" during Execute, so neither is asserted.
	assert.Equal(t, "dmm", root.Use, "the root command is %q, want \"dmm\"", root.Use)

	assert.Equal(
		t,
		Version,
		root.Version,
		"the root reports version %q, want %q",
		root.Version,
		Version,
	)
}

func TestEveryGroupHasHelp(t *testing.T) {
	// A group reached by name prints its own help.
	for _, name := range []string{"mod", "source", "game", "cache", "config", "manifest"} {
		var out, errOut strings.Builder

		env := Env{Out: &out, Err: &errOut}
		code := RunContext(context.Background(), env, []string{name})
		assert.Equal(t, ExitOK, code, "`dmm %s` exited %d, want 0", name, code)

		assert.Contains(
			t,
			out.String(),
			"Available Commands",
			"`dmm %s` printed no command list:\n%s",
			name,
			out.String(),
		)
	}
}
