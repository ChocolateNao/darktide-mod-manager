package actions

import (
	"context"
	"errors"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// newTestRunner wires a runner over fakes so a test only states the part it
// cares about. Everything a real runner gets from the wire — the registry, the
// game lookup, the opener, the platform — is explicit here because "works on my
// machine" is the usual reason a platform-gated action misbehaves.
func newTestRunner(t *testing.T, action domain.GameAction, opts ...func(*RunnerDeps)) *Runner {
	t.Helper()

	reg := NewRegistry()

	if err := reg.Register(action); err != nil {
		t.Fatalf("register: %v", err)
	}

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
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}

	return runner
}

func TestNewRunnerRequiresARegistry(t *testing.T) {
	if _, err := NewRunner(RunnerDeps{}); err == nil {
		t.Fatal("expected an error without a registry")
	}
}

func TestNewRunnerDefaultsThePlatform(t *testing.T) {
	runner, err := NewRunner(RunnerDeps{Registry: NewRegistry()})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}

	if runner.Platform() != domain.CurrentPlatform() {
		t.Fatalf("Platform = %q, want %q", runner.Platform(), domain.CurrentPlatform())
	}
}

func TestRunRejectsUnknownAction(t *testing.T) {
	runner := newTestRunner(t, okAction("test", "test:do"))

	_, err := runner.Run(context.Background(), "test:missing", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeActionNotFound) {
		t.Fatalf("error = %v, want ACTION_NOT_FOUND", err)
	}
}

// The platform gate exists because a Linux box must not be offered a Windows
// batch file; the failure has to be "unavailable", not "not found", so the UI
// can grey the entry out instead of claiming the action does not exist.
func TestRunRejectsActionOffPlatform(t *testing.T) {
	action := withPlatform(okAction("test", "test:do"), domain.ActionPlatformWindows)
	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeActionUnavailable) {
		t.Fatalf("error = %v, want ACTION_UNAVAILABLE", err)
	}
}

func TestRunRejectsCancelledContext(t *testing.T) {
	runner := newTestRunner(t, okAction("test", "test:do"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.Run(ctx, "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled) {
		t.Fatalf("error = %v, want OPERATION_CANCELED", err)
	}
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
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !result.Success {
		t.Fatal("result was not successful")
	}

	if seen.Args["query"] != "" || seen.Args["deep"] != true {
		t.Fatalf("args = %v", seen.Args)
	}

	if seen.GameID != "test" || seen.Platform != domain.PlatformLinux {
		t.Fatalf("context = %+v", seen)
	}

	if seen.ModsPath != "/games/test/mods" || seen.InstallPath != "/games/test" {
		t.Fatalf("paths = %q / %q", seen.InstallPath, seen.ModsPath)
	}
}

func TestRunRejectsUnknownArgumentType(t *testing.T) {
	action := withOptions(okAction("test", "test:do"),
		domain.ActionOption{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
	)
	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", map[string]any{"count": "many"})
	if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

// A handler that forgets to return a result is a bug in the handler, not a
// crash in the caller: the run still succeeds and carries whatever it logged.
func TestRunSynthesisesResultWhenHandlerReturnsNil(t *testing.T) {
	action := okAction("test", "test:do")
	action.Handler = func(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
		actx.Logger.Printf("working")

		return nil, nil
	}

	runner := newTestRunner(t, action)

	result, err := runner.Run(context.Background(), "test:do", nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !result.Success {
		t.Fatal("result was not successful")
	}

	if result.Output != "working\n" {
		t.Fatalf("Output = %q, want the captured log", result.Output)
	}
}

func TestRunCapturesOutputAndEchoesIt(t *testing.T) {
	echo := &echoLogger{}

	action := okAction("test", "test:do")
	action.Handler = func(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
		actx.Logger.Printf("step one")

		return nil, nil
	}

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Echo = echo })

	result, err := runner.Run(context.Background(), "test:do", nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Output != "step one\n" {
		t.Fatalf("Output = %q", result.Output)
	}

	if echo.text() != "step one\n" {
		t.Fatalf("echo = %q", echo.text())
	}
}

func TestRunBoundsTheCapturedOutput(t *testing.T) {
	action := okAction("test", "test:do")
	action.Handler = func(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
		for range captureLogLimit {
			actx.Logger.Printf("0123456789")
		}

		return nil, nil
	}

	runner := newTestRunner(t, action)

	result, err := runner.Run(context.Background(), "test:do", nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(result.Output) > captureLogLimit+64 {
		t.Fatalf("captured %d bytes, want it bounded", len(result.Output))
	}
}

// An action that needs the game installed cannot run when the locator is
// absent. Saying "unavailable" keeps the failure honest: the action exists, the
// machine is not ready.
func TestRunRequiresTheGameToBeInstalled(t *testing.T) {
	action := okAction("test", "test:do")
	action.RequiresGame = true

	runner := newTestRunner(t, action, func(d *RunnerDeps) {
		d.Games = &stubResolver{def: &fakeGame{id: "test"}, currentErr: errors.New("no game")}
	})

	_, err := runner.Run(context.Background(), "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound) {
		t.Fatalf("error = %v, want GAME_NOT_FOUND", err)
	}
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
	if !appErrors.IsAppError(err, appErrors.ErrCodeActionUnavailable) {
		t.Fatalf("error = %v, want ACTION_UNAVAILABLE", err)
	}
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

	if _, err := runner.Run(context.Background(), "test:do", nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	if resolver.currentCall != 0 {
		t.Fatal("the locator was consulted for an action that needs no game")
	}
}

func TestRunKeepsTheHandlerErrorCode(t *testing.T) {
	action := okAction("test", "test:do")
	action.Handler = func(context.Context, domain.ActionContext) (*domain.GameActionResult, error) {
		return nil, appErrors.New(appErrors.ErrCodeModNotInstalled, "mod is disabled")
	}

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeModNotInstalled) {
		t.Fatalf("error = %v, want the handler's code preserved", err)
	}
}

func TestRunLabelsAPlainError(t *testing.T) {
	action := okAction("test", "test:do")
	action.Handler = func(context.Context, domain.ActionContext) (*domain.GameActionResult, error) {
		return nil, errors.New("the drive went away")
	}

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeActionFailed) {
		t.Fatalf("error = %v, want ACTION_FAILED", err)
	}
}

func TestRunRejectsUnknownDefinition(t *testing.T) {
	runner := newTestRunner(t, okAction("test", "test:do"), func(d *RunnerDeps) {
		d.Definitions = stubGame{}
	})

	_, err := runner.Run(context.Background(), "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeGameNotRegistered) {
		t.Fatalf("error = %v, want GAME_NOT_REGISTERED", err)
	}
}

func scriptAction(commands ...domain.ActionCommand) domain.GameAction {
	action := domain.GameAction{ID: "test:do", GameID: "test"}
	action.Script = &domain.ActionScript{Commands: commands}

	return action
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
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(commands.specs) != 1 {
		t.Fatalf("ran %d commands, want the universal one only", len(commands.specs))
	}

	if commands.specs[0].Path != "run.sh" {
		t.Fatalf("ran %q", commands.specs[0].Path)
	}

	if result.Output != "done" {
		t.Fatalf("Output = %q", result.Output)
	}
}

func TestRunPrefersThePlatformSpecificCommand(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	action := scriptAction(
		domain.ActionCommand{Path: "fallback"},
		domain.ActionCommand{Path: "specific", Platform: domain.ActionPlatformLinux},
	)

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	if _, err := runner.Run(context.Background(), "test:do", nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	if commands.specs[0].Path != "specific" {
		t.Fatalf("ran %q, want the linux command", commands.specs[0].Path)
	}
}

func TestRunSubstitutesOptionsIntoScriptArguments(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	action := withOptions(
		scriptAction(domain.ActionCommand{Path: "echo", Args: []string{"{{query}}", "--flag"}}),
		domain.ActionOption{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
	)

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	if _, err := runner.Run(
		context.Background(),
		"test:do",
		map[string]any{"query": "hello world"},
	); err != nil {
		t.Fatalf("run: %v", err)
	}

	args := commands.specs[0].Args
	if len(args) != 2 || args[0] != "hello world" || args[1] != "--flag" {
		t.Fatalf("args = %q", args)
	}
}

// An unterminated placeholder is a typo in the action definition. Passing it
// through would hand the literal braces to a subprocess, so it fails instead.
func TestRunRejectsUnterminatedPlaceholder(t *testing.T) {
	action := scriptAction(domain.ActionCommand{Path: "echo", Args: []string{"{{query"}})

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestRunRejectsUndeclaredPlaceholder(t *testing.T) {
	action := scriptAction(domain.ActionCommand{Path: "echo", Args: []string{"{{nope}}"}})

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestRunUsesTheInterpreter(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	action := scriptAction(domain.ActionCommand{Path: "script.js"})
	action.Script.Interpreter = "node"

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	if _, err := runner.Run(context.Background(), "test:do", nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	spec := commands.specs[0]
	if spec.Path != "node" || len(spec.Args) != 1 || spec.Args[0] != "script.js" {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestRunUsesTheScriptWorkingDirectory(t *testing.T) {
	commands := &recordingCommandRunner{output: CommandOutput{}}
	action := scriptAction(domain.ActionCommand{Path: "run.sh"})
	action.Script.WorkDir = "{{install_path}}/bin"

	action.RequiresGame = true

	runner := newTestRunner(t, action, func(d *RunnerDeps) { d.Commands = commands })

	if _, err := runner.Run(context.Background(), "test:do", nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	if commands.specs[0].Dir != "/games/test/bin" {
		t.Fatalf("dir = %q", commands.specs[0].Dir)
	}
}

func TestRunWithoutAMatchingCommand(t *testing.T) {
	action := scriptAction(
		domain.ActionCommand{Path: "run.exe", Platform: domain.ActionPlatformWindows},
	)

	runner := newTestRunner(t, action)

	_, err := runner.Run(context.Background(), "test:do", nil)
	if !appErrors.IsAppError(err, appErrors.ErrCodeActionUnavailable) {
		t.Fatalf("error = %v, want ACTION_UNAVAILABLE", err)
	}
}

// Some actions are informational on platforms they cannot act on; the script
// says so explicitly instead of every caller special-casing it.
func TestRunSucceedsWhenNoCommandIsExpected(t *testing.T) {
	action := scriptAction(
		domain.ActionCommand{Path: "run.exe", Platform: domain.ActionPlatformWindows},
	)
	action.Script.SuccessWhenEmpty = true

	runner := newTestRunner(t, action)

	result, err := runner.Run(context.Background(), "test:do", nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !result.Success {
		t.Fatal("result was not successful")
	}
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
	if !appErrors.IsAppError(err, appErrors.ErrCodeActionFailed) {
		t.Fatalf("error = %v, want ACTION_FAILED", err)
	}
}

func TestRunReportsAMissingInterpreter(t *testing.T) {
	runner := newTestRunner(t, scriptAction(domain.ActionCommand{Path: "run.sh"}))

	// No command runner was injected, so the default refuses rather than
	// silently doing nothing and reporting success.
	_, err := runner.Run(context.Background(), "test:do", nil)
	if !errors.Is(err, ErrNoCommandRunner) {
		t.Fatalf("error = %v, want ErrNoCommandRunner", err)
	}
}
