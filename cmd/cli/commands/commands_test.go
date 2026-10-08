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
	// A nil writer is a mistake in a test or an embedder, and a panic there
	// says nothing about the command that was running.
	env := Env{}.withDefaults()
	require.NotNil(t, env.Out, "withDefaults left a field nil: %+v", env)
	require.NotNil(t, env.Err, "withDefaults left a field nil: %+v", env)
	require.NotNil(t, env.In, "withDefaults left a field nil: %+v", env)
	// Discarding is better than panicking, so writing to a defaulted field has
	// to work rather than merely not be nil.
	_, err := env.Out.Write([]byte("out"))
	assert.NoError(t, err, "writing to a defaulted Out failed: %v", err)

	_, err = env.Err.Write([]byte("err"))
	assert.NoError(t, err, "writing to a defaulted Err failed: %v", err)

	read, err := env.In.Read(make([]byte, 1))
	require.Error(t, err)
	assert.Equal(t, 0, read, "a defaulted In produced %d bytes, want an empty reader", read)

	// A supplied writer is not replaced, because a caller that captured output
	// and then found it went to io.Discard would have a very confusing test.
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
	// One invocation is one process: the database is opened once and the
	// start-up diagnostics run once. A container that could be rebuilt mid-run
	// would be able to disagree with itself — and rebuilding it is the way a
	// half-installed mod's state ends up in two places.
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
	// Each of these is a separate invocation, and each is a separate process
	// as far as the container is concerned: the counter has to go up once per
	// invocation, not once per run of the test binary.
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
	// A version query is the one command that has to work when the settings file
	// is unreadable, the disk is full, or the manager is misconfigured. Help is
	// the other: somebody who cannot start the manager is exactly who needs to
	// read it.
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
	// Env is built by whoever embeds this package. A caller that forgot the
	// factory gets an error that says so, not a nil dereference inside a
	// command.
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
	// Retrying a start-up that has already been shown to fail would produce the
	// same message twice, or — worse — a second, different failure after the
	// first one left something half-built.
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
	// "mod list" and "cache info" both need the container, and one
	// invocation only runs one of them — so the second acquire is exercised
	// through a command that asks for the client more than once.
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
	// The session is the unit that decides whether to build again, so it is
	// tested directly rather than only through a command.
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
	// The same error both times: a caller that reports it twice must not see two
	// different explanations of one problem.
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
	// Closing twice is how a deferred close looks when the run also reported an
	// error, and it must not fail.
	err = s.close()
	assert.NoError(t, err, "the second close reported %v", err)
}

func TestGlobalFlagsReachTheContainer(t *testing.T) {
	// A flag that is parsed but not forwarded is a flag that silently does
	// nothing, which is worse than one that does not exist.
	w := newWorkspace(t)
	// --game validates before it persists, so the directory has to be a real
	// installation. A workspace alone is not one.
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
		"--game", w.gameDir,
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
		w.gameDir,
		seen.GamePath,
		"--game reached the container as %q, want %q",
		seen.GamePath,
		w.gameDir,
	)

	assert.NotNil(
		t,
		seen.Logger,
		"no logger reached the container, so start-up diagnostics would be lost",
	)
	// The flags must not be silently trimmed into nothing by a path that
	// "normalises" them.
	assert.NotEqual(t, other, seen.ConfigPath, "the test is not checking what it claims to check")
}

func TestAnUnsetPathIsPassedThroughBlank(t *testing.T) {
	// di treats a blank path as "the user has no opinion" and falls back to the
	// settings file. Inventing a default here instead would override the
	// configuration file behind the user's back, which is the one thing a
	// default must never do.
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
	} {
		assert.Equal(t, "", got, "%s was invented as %q with no flag given", name, got)
	}
}

func TestQuietSilencesStartUpDiagnosticsButNotErrors(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	// A source that cannot be built produces a start-up diagnostic. It is
	// information, not a failure: one misconfigured optional source must not
	// stop the manager, and --quiet is how a person says they do not want to
	// read about it.
	// A run that succeeds logs nothing either way, because every source in this
	// workspace is configured. The difference --quiet makes is visible only in
	// a run that fails, so both halves are checked there.
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
	// --quiet silences start-up chatter, not the failure the user asked about.
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

func TestAnUnknownCommandIsRefusedWithASuggestion(t *testing.T) {
	// cobra prints its own message here, and the exit code is what a script
	// reads. SilenceErrors means this program prints the one line, so the
	// message has to come from us intact.
	var out, errOut strings.Builder

	env := Env{Out: &out, Err: &errOut}

	code := RunContext(context.Background(), env, []string{"mods", "list"})
	assert.Equal(t, ExitError, code, "a mistyped group exited %d, want %d", code, ExitError)

	assert.Contains(t, errOut.String(), "error:", "nothing was reported: %q", errOut.String())
}

func TestNewRootCommandHasTheWholeTree(t *testing.T) {
	// The tree is a contract with the user: a command named in the
	// documentation that does not exist is a bug that no other test finds,
	// because every other test uses a command that is there.
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
		// groups
		"mod", "source", "game", "cache", "config", "version",
		// mod
		"list", "show", "install", "uninstall", "enable", "disable", "update",
		"reorder", "path", "relink", "symlink-status", "conflicts", "share", "import-link",
		"hooks",
		// source
		"search", "auth", "test",
		// game
		"detect", "validate", "info",
		// cache
		"clear", "prune",
		// config
		"show", "get", "set", "edit",
	}
	for _, name := range want {
		assert.True(t, found[name], "the tree has no %q command", name)
	}
	// "list", "show" and "info" each appear under more than one group, so a
	// flat set of names cannot tell the two apart — which is why the groups
	// themselves are in the list above. What matters here is that no name is
	// missing and that the walk actually saw something: a tree that failed to
	// assemble would satisfy the loop by being empty.
	assert.GreaterOrEqual(t, len(found), len(want),
		"the walk found %d commands, fewer than the %d the contract names",
		len(found),
		len(want),
	)
	// Cobra adds "help" and "completion" during Execute rather than at
	// construction, so neither is asserted here. What is asserted is the root's
	// own identity, since everything in the tree hangs off it.
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
	// A group reached by name prints its own help rather than the root's, so a
	// person who has forgotten `mod install`'s flags can find them.
	for _, name := range []string{"mod", "source", "game", "cache", "config"} {
		var out, errOut strings.Builder

		env := Env{Out: &out, Err: &errOut}
		// The bare group prints help through its own RunE, not through a flag.
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
