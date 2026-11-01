package actions

import (
	"context"
	"errors"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRunner wires a runner over fakes so a test only states the part it cares
// about.
func newTestRunner(t *testing.T, action domain.GameAction, opts ...func(*RunnerDeps)) *Runner {
	t.Helper()

	reg := NewRegistry()
	require.NoError(t, reg.Register(action), "register")

	game := &fakeGame{id: action.GameID}
	deps := RunnerDeps{
		Registry:    reg,
		Definitions: stubGame{def: game},
		Games: &stubResolver{
			def:      game,
			modsPath: "/games/test/mods",
		},
		Opener:   &recordingOpener{},
		Platform: domain.PlatformLinux,
	}

	for _, opt := range opts {
		opt(&deps)
	}

	runner, err := NewRunner(deps)
	require.NoError(t, err, "new runner")

	return runner
}

func TestNewRunnerRequiresARegistry(t *testing.T) {
	_, err := NewRunner(RunnerDeps{})
	require.Error(t, err, "expected an error without a registry")
}

func TestNewRunnerDefaultsThePlatform(t *testing.T) {
	runner, err := NewRunner(RunnerDeps{Registry: NewRegistry()})
	require.NoError(t, err, "new runner")

	assert.Equal(
		t,
		domain.CurrentPlatform(),
		runner.Platform(),
		"Platform = %q, want %q",
		runner.Platform(),
		domain.CurrentPlatform(),
	)
}

func TestRunRejectsUnknownAction(t *testing.T) {
	runner := newTestRunner(t, okAction("test", "test:do"))

	_, err := runner.Run(context.Background(), "test:missing", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeActionNotFound),
		"error = %v, want ACTION_NOT_FOUND", err)
}

// The platform gate keeps a Linux box from running a Windows batch file, and its
// failure must be "unavailable" so the UI greys the entry out.
func TestRunRejectsActionOffPlatform(t *testing.T) {
	action := withPlatform(okAction("test", "test:do"), domain.ActionPlatformWindows)
	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeActionUnavailable),
		"error = %v, want ACTION_UNAVAILABLE", err)
}

func TestRunRejectsCancelledContext(t *testing.T) {
	runner := newTestRunner(t, okAction("test", "test:do"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.Run(ctx, "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"error = %v, want OPERATION_CANCELED", err)
}

func TestRunPassesCoercedArgumentsToTheHandler(t *testing.T) {
	var seen domain.ActionContext

	action := withOptions(okAction("test", "test:do"),
		domain.ActionOption{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
		domain.ActionOption{
			Name: "deep", Type: domain.ActionOptionBool, Label: "Deep",
			Default: false,
		},
	)
	action.RequiresGame = true
	action.Handler = func(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
		seen = actx

		return &domain.GameActionResult{Success: true}, nil
	}

	runner := newTestRunner(t, action)

	// The CLI sends the kebab-case flag name; the frontend sends the declared
	// name. Both must land in the same argument.
	result, err := runner.Run(context.Background(), "test:do", map[string]any{"deep": "yes"})
	require.NoError(t, err, "run")

	require.True(t, result.Success, "result was not successful")

	require.Truef(t, seen.Args["query"] == "" && seen.Args["deep"] == true,
		"args = %v", seen.Args)

	require.Truef(t, seen.GameID == "test" && seen.Platform == domain.PlatformLinux,
		"context = %+v", seen)

	require.Truef(t, seen.ModsPath == "/games/test/mods" && seen.InstallPath == "/games/test",
		"paths = %q / %q", seen.InstallPath, seen.ModsPath)
}

func TestRunRejectsUnknownArgumentType(t *testing.T) {
	action := withOptions(okAction("test", "test:do"),
		domain.ActionOption{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
	)
	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", map[string]any{"count": "many"})
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT", err)
}

// A handler that forgets to return a result is a bug in the handler; the run
// still succeeds and carries whatever it logged.
func TestRunSynthesisesResultWhenHandlerReturnsNil(t *testing.T) {
	action := okAction("test", "test:do")
	action.Handler = func(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
		actx.Logger.Logf("working")

		return nil, nil
	}

	runner := newTestRunner(t, action)

	result, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	require.True(t, result.Success, "result was not successful")

	require.Equalf(t, "working\n", result.Output, "Output = %q, want the captured log", result.Output)
}

func TestRunCapturesOutputAndEchoesIt(t *testing.T) {
	echo := &echoLogger{}

	action := okAction("test", "test:do")
	action.Handler = func(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
		actx.Logger.Logf("step one")

		return nil, nil
	}

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Echo = echo })

	result, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	require.Equalf(t, "step one\n", result.Output, "Output = %q", result.Output)

	require.Equalf(t, "step one\n", echo.text(), "echo = %q", echo.text())
}

func TestRunBoundsTheCapturedOutput(t *testing.T) {
	action := okAction("test", "test:do")
	action.Handler = func(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
		for range captureLogLimit {
			actx.Logger.Logf("0123456789")
		}

		return nil, nil
	}

	runner := newTestRunner(t, action)

	result, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	require.LessOrEqualf(t, len(result.Output), captureLogLimit+64,
		"captured %d bytes, want it bounded", len(result.Output))
}

// An action needing the game cannot run when the locator is absent; "unavailable"
// keeps the failure honest — the action exists, the machine is not ready.
func TestRunRequiresTheGameToBeInstalled(t *testing.T) {
	action := okAction("test", "test:do")
	action.RequiresGame = true

	runner := newTestRunner(t, action, func(d *RunnerDeps) {
		d.Games = &stubResolver{def: &fakeGame{id: "test"}, currentErr: errors.New("no game")}
	})

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound),
		"error = %v, want GAME_NOT_FOUND", err)
}

// An action whose game is not the active one cannot borrow the active game's
// paths — that is how one game ends up writing files into another.
func TestRunRejectsActionFromAnotherGame(t *testing.T) {
	action := okAction("test", "test:do")
	action.RequiresGame = true

	runner := newTestRunner(t, action, func(d *RunnerDeps) {
		d.Games = &stubResolver{def: &fakeGame{id: "other"}}
	})

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeActionUnavailable),
		"error = %v, want ACTION_UNAVAILABLE", err)
}

// An action that does not need the game must not touch the filesystem looking
// for it: a mod action should still work on a fresh install.
func TestRunSkipsTheLocatorWhenTheGameIsNotRequired(t *testing.T) {
	resolver := &stubResolver{def: &fakeGame{id: "test"}, currentErr: errors.New("no game")}
	runner := newTestRunner(
		t,
		okAction("test", "test:do"),
		func(d *RunnerDeps) { d.Games = resolver },
	)

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	require.Zero(t, resolver.currentCall,
		"the locator was consulted for an action that needs no game")
}

func TestRunKeepsTheHandlerErrorCode(t *testing.T) {
	action := okAction("test", "test:do")
	action.Handler = func(context.Context, domain.ActionContext) (*domain.GameActionResult, error) {
		return nil, appErrors.New(appErrors.ErrCodeModNotInstalled, "mod is disabled")
	}

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeModNotInstalled),
		"error = %v, want the handler's code preserved", err)
}

func TestRunLabelsAPlainError(t *testing.T) {
	action := okAction("test", "test:do")
	action.Handler = func(context.Context, domain.ActionContext) (*domain.GameActionResult, error) {
		return nil, errors.New("the drive went away")
	}

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeActionFailed),
		"error = %v, want ACTION_FAILED", err)
}

func TestRunRejectsUnknownDefinition(t *testing.T) {
	runner := newTestRunner(t, okAction("test", "test:do"), func(d *RunnerDeps) {
		d.Definitions = stubGame{}
	})

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeGameNotRegistered),
		"error = %v, want GAME_NOT_REGISTERED", err)
}

func scriptAction(commands ...domain.ActionCommand) domain.GameAction {
	action := domain.GameAction{ID: "test:do", GameID: "test"}
	action.Script = &domain.ActionScript{Commands: commands}

	return action
}

func TestRunRefusesCommandActionsWhenPermissionIsOff(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	runner := newTestRunner(t, scriptAction(domain.ActionCommand{Path: "run.sh"}),
		func(d *RunnerDeps) {
			d.Commands = commands
			d.AllowCommands = func() bool { return false }
		})

	result, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")
	require.NotNil(t, result, "result")
	assert.False(t, result.Success, "result was successful")
	assert.Equal(t, "actions.commandsDisabled", result.MessageKey, "message key")
	assert.Empty(t, commands.specs, "command ran despite the permission")
}

func TestRunGivesHandlersADetachedCommandStarter(t *testing.T) {
	commands := &recordingCommandRunner{}
	action := domain.GameAction{
		ID:       "test:start",
		GameID:   "test",
		Platform: domain.ActionPlatformUniversal,
		Handler: func(ctx context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
			return &domain.GameActionResult{Success: true}, actx.StartCommand(
				ctx, domain.ActionCommand{Path: "game"})
		},
	}

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	result, err := runner.Run(context.Background(), "test:start", nil)
	require.NoError(t, err, "run")
	require.NotNil(t, result, "result")
	assert.True(t, result.Success, "handler failed: %+v", result)
	require.Len(t, commands.specs, 1, "ran %d commands", len(commands.specs))
	assert.Equal(t, "game", commands.specs[0].Path)
	assert.True(t, commands.specs[0].Detached, "handler command was not detached")
}

func TestRunGivesHandlersACommandRunner(t *testing.T) {
	commands := &recordingCommandRunner{
		output: CommandOutput{Stdout: "patched", Stderr: "note", ExitCode: 0},
	}
	action := domain.GameAction{
		ID:           "test:patch",
		GameID:       "test",
		Platform:     domain.ActionPlatformUniversal,
		RequiresGame: true,
		Handler: func(ctx context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
			output, err := actx.RunCommand(ctx, domain.ActionCommand{
				Path: "dtkit-patch",
				Args: []string{"--patch", "bundle"},
			})

			return &domain.GameActionResult{Success: true, Output: output.Output}, err
		},
	}

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	result, err := runner.Run(context.Background(), "test:patch", nil)
	require.NoError(t, err, "run")
	require.NotNil(t, result, "result")
	assert.True(t, result.Success, "handler failed: %+v", result)
	assert.Equal(t, "patched\nnote", result.Output)

	require.Len(t, commands.specs, 1, "ran %d commands", len(commands.specs))
	assert.False(t, commands.specs[0].Detached, "handler command was detached")
	assert.Equal(t, "/games/test", commands.specs[0].Dir)
	assert.Equal(t, "test:patch", commands.specs[0].Action.String())
}

func TestRunTellsHandlersWhetherCommandsAreAllowed(t *testing.T) {
	var allowed bool

	action := domain.GameAction{
		ID:       "test:check",
		GameID:   "test",
		Platform: domain.ActionPlatformUniversal,
		Handler: func(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
			allowed = actx.CommandsAllowed

			return &domain.GameActionResult{Success: true}, nil
		},
	}

	runner := newTestRunner(t, action, func(d *RunnerDeps) {
		d.AllowCommands = func() bool { return false }
	})

	_, err := runner.Run(context.Background(), "test:check", nil)
	require.NoError(t, err, "run")
	assert.False(t, allowed, "handler saw commands as allowed")
}

func TestRunExecutesTheMatchingScript(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{Stdout: "done", ExitCode: 0}}
	action := scriptAction(
		domain.ActionCommand{
			Path:     "run.exe",
			Args:     []string{"--all"},
			Platform: domain.ActionPlatformWindows,
		},
		domain.ActionCommand{Path: "run.sh", Args: []string{"--all"}},
	)

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	result, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	require.Lenf(t, commands.specs, 1,
		"ran %d commands, want the universal one only", len(commands.specs))

	require.Equalf(t, "run.sh", commands.specs[0].Path, "ran %q", commands.specs[0].Path)

	require.Equalf(t, "done", result.Output, "Output = %q", result.Output)
}

func TestRunPrefersThePlatformSpecificCommand(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	action := scriptAction(
		domain.ActionCommand{Path: "fallback"},
		domain.ActionCommand{Path: "specific", Platform: domain.ActionPlatformLinux},
	)

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	require.Equalf(t, "specific", commands.specs[0].Path,
		"ran %q, want the linux command", commands.specs[0].Path)
}

func TestRunSubstitutesOptionsIntoScriptArguments(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	action := withOptions(
		scriptAction(domain.ActionCommand{Path: "echo", Args: []string{"{{query}}", "--flag"}}),
		domain.ActionOption{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
	)

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	_, err := runner.Run(
		context.Background(),
		"test:do",
		map[string]any{"query": "hello world"},
	)
	require.NoError(t, err, "run")

	args := commands.specs[0].Args
	require.Lenf(t, args, 2, "args = %q", args)

	require.Equalf(t, "hello world", args[0], "args = %q", args)

	require.Equalf(t, "--flag", args[1], "args = %q", args)
}

// An unterminated placeholder is a typo in the action definition; the literal
// braces must not reach a subprocess.
func TestRunRejectsUnterminatedPlaceholder(t *testing.T) {
	action := scriptAction(domain.ActionCommand{Path: "echo", Args: []string{"{{query"}})

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT", err)
}

func TestRunRejectsUndeclaredPlaceholder(t *testing.T) {
	action := scriptAction(domain.ActionCommand{Path: "echo", Args: []string{"{{nope}}"}})

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT", err)
}

func TestRunUsesTheInterpreter(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	action := scriptAction(domain.ActionCommand{Path: "script.js"})
	action.Script.Interpreter = "node"

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	spec := commands.specs[0]
	require.Equalf(t, "node", spec.Path, "spec = %+v", spec)

	require.Lenf(t, spec.Args, 1, "spec = %+v", spec)

	require.Equalf(t, "script.js", spec.Args[0], "spec = %+v", spec)
}

func TestRunUsesTheScriptWorkingDirectory(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	action := scriptAction(domain.ActionCommand{Path: "run.sh"})
	action.Script.WorkDir = "{{install_path}}/bin"

	action.RequiresGame = true

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	require.Equalf(t, "/games/test/bin", commands.specs[0].Dir,
		"dir = %q", commands.specs[0].Dir)
}

func TestRunWithoutAMatchingCommand(t *testing.T) {
	action := scriptAction(
		domain.ActionCommand{Path: "run.exe", Platform: domain.ActionPlatformWindows},
	)

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeActionUnavailable),
		"error = %v, want ACTION_UNAVAILABLE", err)
}

// Some actions are informational on platforms they cannot act on; the script
// says so explicitly.
func TestRunSucceedsWhenNoCommandIsExpected(t *testing.T) {
	action := scriptAction(
		domain.ActionCommand{Path: "run.exe", Platform: domain.ActionPlatformWindows},
	)
	action.Script.SuccessWhenEmpty = true

	runner := newTestRunner(t, action)

	result, err := runner.Run(context.Background(), "test:do", nil)
	require.NoError(t, err, "run")

	require.True(t, result.Success, "result was not successful")
}

func TestRunFailsOnNonZeroExit(t *testing.T) {
	commands := &recordingCommandRunner{
		output: CommandOutput{Stderr: "boom\nsecond line", ExitCode: 2},
	}
	runner := newTestRunner(
		t,
		scriptAction(domain.ActionCommand{Path: "run.sh"}),
		func(d *RunnerDeps) {
			d.Commands = commands
		},
	)

	_, err := runner.Run(context.Background(), "test:do", nil)
	require.Truef(t, appErrors.IsAppError(err, appErrors.ErrCodeActionFailed),
		"error = %v, want ACTION_FAILED", err)
}

func TestRunReportsAMissingInterpreter(t *testing.T) {
	runner := newTestRunner(t, scriptAction(domain.ActionCommand{Path: "run.sh"}))

	// No command runner was injected, so the default refuses.
	_, err := runner.Run(context.Background(), "test:do", nil)
	require.ErrorIs(t, err, ErrNoCommandRunner, "error = %v, want ErrNoCommandRunner", err)
}
