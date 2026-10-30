// Package actions holds the registry and runner behind game-specific actions.
// A game declares an action, the registry holds the declarations and the runner
// decides what running one means.
package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// GameResolver is the slice of the game locator a run needs, narrow enough to
// test without a database.
type GameResolver interface {
	Active() (domain.GameDefinition, domain.GameSettings, error)
	Current(ctx context.Context) (*domain.GameInstallation, error)
	// GetModsPath resolves the mods folder, honouring the user's override.
	GetModsPath(install *domain.GameInstallation) (string, error)
}

// DefinitionLookup resolves a game definition by its registry id. It is separate
// from GameResolver because an action may belong to a game that is not active.
type DefinitionLookup interface {
	Get(id string) (domain.GameDefinition, bool)
}

// RunnerDeps is what a Runner needs. A nil Opener or Commands disables the
// actions that need it. A nil AllowCommands leaves command actions enabled.
type RunnerDeps struct {
	Definitions   DefinitionLookup
	Games         GameResolver
	Opener        domain.ActionOpener
	Commands      CommandRunner
	AllowCommands func() bool
	Echo          domain.ActionLogger
	Registry      *Registry
	Platform      domain.Platform
}

// Runner executes registered actions.
//
// It is safe for concurrent use: the registry is already guarded, and the
// runner keeps no mutable state of its own.
type Runner struct {
	definitions   DefinitionLookup
	games         GameResolver
	opener        domain.ActionOpener
	commands      CommandRunner
	allowCommands func() bool
	echo          domain.ActionLogger
	registry      *Registry
	platform      domain.Platform
}

// NewRunner builds a Runner, filling in the platform default. The error return
// covers a runner with no registry, which could only report every action missing.
func NewRunner(deps RunnerDeps) (*Runner, error) {
	if deps.Registry == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"an action runner needs a registry")
	}

	platform := deps.Platform
	if platform == "" {
		platform = domain.CurrentPlatform()
	}

	commands := deps.Commands
	if commands == nil {
		commands = discardCommandRunner{}
	}

	return &Runner{
		registry:      deps.Registry,
		definitions:   deps.Definitions,
		games:         deps.Games,
		opener:        deps.Opener,
		commands:      commands,
		allowCommands: deps.AllowCommands,
		platform:      platform,
		echo:          deps.Echo,
	}, nil
}

func (r *Runner) commandsAllowed() bool {
	return r.allowCommands == nil || r.allowCommands()
}

func (r *Runner) Platform() domain.Platform {
	if r == nil {
		return ""
	}

	return r.platform
}

// Run executes one action and returns what it reported.
//
// The failure modes stay distinct: ACTION_NOT_FOUND means the surface offered
// something stale, ACTION_UNAVAILABLE a platform filter or missing prerequisite,
// OPERATION_CANCELED the user, and ACTION_FAILED the action's own fault.
func (r *Runner) Run(
	ctx context.Context,
	actionID string,
	args map[string]any,
) (*domain.GameActionResult, error) {
	if r == nil || r.registry == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action runner is not initialised")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	id := domain.ActionID(strings.TrimSpace(actionID))

	action, ok := r.registry.Get(id)
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeActionNotFound,
			"no action is registered with id %q", actionID)
	}

	if !action.AvailableOn(r.platform) {
		return nil, appErrors.New(appErrors.ErrCodeActionUnavailable,
			"action %q targets %s and does not run on %s",
			action.ID, action.Platform.Normalize(), r.platform)
	}

	if err := ctx.Err(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeOperationCanceled,
			"action %q was cancelled before it started",
			action.ID,
		)
	}

	logger := newCaptureLogger(r.echo)

	actx, err := r.buildContext(ctx, action, args, logger)
	if err != nil {
		return nil, err
	}

	actx.Action = action.ID
	actx.CommandsAllowed = r.commandsAllowed()
	actx.StartCommand = func(ctx context.Context, command domain.ActionCommand) error {
		_, err := r.commands.Run(ctx, CommandSpec{
			Path:     command.Path,
			Args:     command.Args,
			Dir:      actx.WorkingDir,
			Action:   action.ID,
			Detached: true,
		})

		return err
	}

	if action.Script != nil {
		return r.runScript(ctx, action, actx)
	}

	result, err := action.Handler(ctx, actx)
	if err != nil {
		return nil, wrapActionError(action, err)
	}

	return normaliseResult(action, result, logger), nil
}

// buildContext assembles everything a handler is allowed to touch. Anything the
// handler does not need is not resolved: an action that only prints a sentence
// should run on a machine with no game installed.
func (r *Runner) buildContext(
	ctx context.Context,
	action domain.GameAction,
	args map[string]any,
	logger domain.ActionLogger,
) (domain.ActionContext, error) {
	coerced, err := coerceArgs(action.Options, args)
	if err != nil {
		return domain.ActionContext{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeInvalidArgument,
			"action %q was called with unusable arguments",
			action.ID,
		)
	}

	actx := domain.ActionContext{
		Args:     coerced,
		Logger:   logger,
		Platform: r.platform,
		GameID:   action.GameID,
		Opener:   r.opener,
	}

	def, err := r.definition(action)
	if err != nil {
		return domain.ActionContext{}, err
	}

	actx.Game = def

	if !action.RequiresGame {
		return actx, nil
	}

	if r.games == nil {
		return domain.ActionContext{}, appErrors.New(appErrors.ErrCodeActionUnavailable,
			"action %q needs a game installation but none can be located", action.ID)
	}

	actx, err = r.resolveInstallation(ctx, action, actx)
	if err != nil {
		return domain.ActionContext{}, err
	}

	return actx, nil
}

func (r *Runner) definition(action domain.GameAction) (domain.GameDefinition, error) {
	if r.definitions == nil {
		return nil, appErrors.New(appErrors.ErrCodeActionUnavailable,
			"action %q cannot be described: no game definitions are available", action.ID)
	}

	def, ok := r.definitions.Get(action.GameID)
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeGameNotRegistered,
			"game %q behind action %q is not registered", action.GameID, action.ID)
	}

	return def, nil
}

func (r *Runner) resolveInstallation(
	ctx context.Context,
	action domain.GameAction,
	actx domain.ActionContext,
) (domain.ActionContext, error) {
	def, settings, err := r.games.Active()
	if err != nil {
		return domain.ActionContext{}, appErrors.Annotate(
			err,
			appErrors.ErrCodeGameNotFound,
			"action %q could not read the active game",
			action.ID,
		)
	}

	if def == nil || def.ID() != action.GameID {
		return domain.ActionContext{}, appErrors.New(appErrors.ErrCodeActionUnavailable,
			"action %q belongs to %q but the active game is %q",
			action.ID, action.GameID, def.ID())
	}

	actx.GameSettings = settings

	install, err := r.games.Current(ctx)
	if err != nil {
		return domain.ActionContext{}, appErrors.Annotate(
			err,
			appErrors.ErrCodeGameNotFound,
			"action %q could not locate a %s installation",
			action.ID,
			def.DisplayName(),
		)
	}

	actx.InstallPath = install.Path
	actx.WorkingDir = install.Path

	modsPath, err := r.games.GetModsPath(install)
	if err != nil {
		// An action that only reads the game directory is still worth running, so
		// an unresolved mods folder degrades the context.
		actx.Logf("could not resolve the mods folder: %v", err)

		return actx, nil
	}

	actx.ModsPath = modsPath

	return actx, nil
}

// runScript dispatches a script-based action. Scripts never reach a shell: the
// command and its arguments are fixed by the game definition and executed
// directly, so a definition typo cannot become arbitrary code execution.
func (r *Runner) runScript(
	ctx context.Context,
	action domain.GameAction,
	actx domain.ActionContext,
) (*domain.GameActionResult, error) {
	command, ok := action.Script.CommandFor(r.platform)
	if !ok {
		if action.Script.SuccessWhenEmpty {
			// Reporting success keeps a caller that loops over every available
			// action from treating "not applicable" as a failure.
			return &domain.GameActionResult{
				Success:    true,
				Message:    fmt.Sprintf("%s has nothing to run on %s", action.ID, r.platform),
				MessageKey: "actions.noCommand",
				MessageArgs: map[string]any{
					"action":   string(action.ID),
					"platform": string(r.platform),
				},
			}, nil
		}

		return nil, appErrors.New(appErrors.ErrCodeActionUnavailable,
			"action %q has no command for %s", action.ID, r.platform)
	}

	if !r.commandsAllowed() {
		return &domain.GameActionResult{
			Success:    false,
			Message:    fmt.Sprintf("%s needs permission to execute commands", action.ID),
			MessageKey: "actions.commandsDisabled",
			MessageArgs: map[string]any{
				"action": string(action.ID),
			},
		}, nil
	}

	args, err := renderArgs(command.Args, actx.Args)
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInvalidArgument,
			"action %q has an unusable command",
			action.ID,
		)
	}

	if action.Script.Interpreter != "" {
		args = append([]string{command.Path}, args...)
		command.Path = action.Script.Interpreter
	}

	workDir, err := renderDir(action.Script.WorkDir, actx)
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInvalidArgument,
			"action %q has an unusable working directory",
			action.ID,
		)
	}

	if workDir == "" {
		workDir = actx.WorkingDir
	}

	actx.Logf("running %s %s", command.Path, strings.Join(args, " "))

	output, err := r.commands.Run(ctx, CommandSpec{
		Path:   command.Path,
		Args:   args,
		Dir:    workDir,
		Action: action.ID,
	})
	if err != nil {
		return nil, wrapActionError(action, err)
	}

	if !output.Succeeded() {
		return nil, appErrors.New(appErrors.ErrCodeActionFailed,
			"action %q: %s exited with code %d: %s",
			action.ID, command.Path, output.ExitCode, firstLine(output.Combined()))
	}

	actx.Logger.Logf("%s", output.Combined())

	return &domain.GameActionResult{
		Success:    true,
		Message:    fmt.Sprintf("%s finished with exit code %d", action.ID, output.ExitCode),
		MessageKey: "actions.exitCode",
		MessageArgs: map[string]any{
			"action": string(action.ID),
			"code":   output.ExitCode,
		},
		Output: output.Combined(),
		Data: map[string]any{
			"exit_code": output.ExitCode,
			"stdout":    output.Stdout,
			"stderr":    output.Stderr,
		},
	}, nil
}

// normaliseResult guarantees a handler's result is safe to hand to a surface. A
// handler that returns (nil, nil) is a bug, so an empty success is synthesised.
func normaliseResult(
	action domain.GameAction,
	result *domain.GameActionResult,
	logger domain.ActionLogger,
) *domain.GameActionResult {
	if result == nil {
		return &domain.GameActionResult{
			Success:    true,
			Message:    fmt.Sprintf("%s finished", action.ID),
			MessageKey: "actions.finished",
			MessageArgs: map[string]any{
				"action": string(action.ID),
			},
			Output: textOf(logger),
		}
	}

	if result.Output == "" {
		result.Output = textOf(logger)
	}

	if result.Message == "" {
		result.Message = fmt.Sprintf("%s finished", action.ID)
		result.MessageKey = "actions.finished"
		result.MessageArgs = map[string]any{"action": string(action.ID)}
	}

	return result
}

// wrapActionError keeps the code a lower layer chose. ExecRunner maps an exit
// status, a missing binary and a cancelled context onto distinct codes; replacing
// them with a plain ACTION_FAILED would throw that away.
func wrapActionError(action domain.GameAction, err error) error {
	if appErrors.CodeOf(err) != appErrors.CodeUnknown {
		return err
	}

	return appErrors.Wrap(err, appErrors.ErrCodeActionFailed, "action %q failed", action.ID)
}

func textOf(logger domain.ActionLogger) string {
	if capture, ok := logger.(*captureLogger); ok {
		return capture.Text()
	}

	return ""
}

// firstLine trims a command's output to something that fits an error message; a
// tool that prints a hundred lines of stack should not produce a hundred-line error.
func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "no output"
	}

	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[:idx]
	}

	return text
}

// renderArgs substitutes {{option}} placeholders in a declared argument list. A
// placeholder that names nothing declared is refused, because a typo then turns
// into a command running with a missing argument.
func renderArgs(args []string, values map[string]any) ([]string, error) {
	rendered := make([]string, 0, len(args))

	for _, arg := range args {
		expanded, err := renderTemplate(arg, values)
		if err != nil {
			return nil, err
		}

		rendered = append(rendered, expanded)
	}

	return rendered, nil
}

func renderDir(dir string, actx domain.ActionContext) (string, error) {
	if dir == "" {
		return "", nil
	}

	values := map[string]any{}
	for key, value := range actx.Args {
		values[key] = value
	}

	values["install_path"] = actx.InstallPath
	values["mods_path"] = actx.ModsPath

	return renderTemplate(dir, values)
}

// renderTemplate expands every {{name}} occurrence in text. Values are
// stringified here so one argument holding a placeholder plus a suffix stays a
// single argument.
func renderTemplate(text string, values map[string]any) (string, error) {
	var out strings.Builder

	for {
		start := strings.Index(text, "{{")
		if start < 0 {
			out.WriteString(text)

			break
		}

		end := strings.Index(text[start:], "}}")
		if end < 0 {
			return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
				"unterminated placeholder in %q", text)
		}

		end += start

		name := strings.TrimSpace(text[start+2 : end])

		value, ok := lookup(values, name)
		if !ok {
			return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
				"unknown placeholder {{%s}}", name)
		}

		out.WriteString(text[:start])
		out.WriteString(templateValue(value))

		text = text[end+2:]
	}

	return out.String(), nil
}

func templateValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "1"
		}

		return "0"
	case int:
		return fmt.Sprintf("%d", typed)
	case float64:
		return fmt.Sprintf("%v", typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}
