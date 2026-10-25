package api

import (
	"context"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Aliases for the hook vocabulary.
type (
	// HookScript is one hook: what to run, how long it may run, and the
	// environment it may see.
	HookScript = domain.HookScript
	// ScriptLanguage names the language a hook script is written in.
	ScriptLanguage = domain.ScriptLanguage
	// HookPhase names a point in a mod's lifecycle where hooks run.
	HookPhase = domain.HookPhase
	// ScriptPermissions is the capability set a hook run is granted: the
	// filesystem, config, storage and environment switches plus the
	// timeout_seconds cap.
	ScriptPermissions = domain.ScriptPermissions
)

// The script languages the sandbox runs.
const (
	LangLua        ScriptLanguage = domain.LangLua
	LangJavascript ScriptLanguage = domain.LangJS
	// LangJS is the short spelling of LangJavascript.
	LangJS = LangJavascript
)

// HookTypes returns every hook script language the domain defines. The name is
// kept for the frontend contract; the values are the languages. It is not what
// the running build can execute; SupportsHookType answers that.
func HookTypes() []string {
	langs := domain.ScriptLanguages()

	out := make([]string, 0, len(langs))
	for _, l := range langs {
		out = append(out, string(l))
	}

	return out
}

// HookPhases returns every lifecycle phase hooks can run in, in the order they
// occur during an install.
func HookPhases() []string {
	phases := domain.HookPhases()

	out := make([]string, 0, len(phases))
	for _, p := range phases {
		out = append(out, string(p))
	}

	return out
}

// SupportsHookType reports whether this build can execute the given script language.
// A hook editor must ask before offering a choice: it must not let a user author a
// mod in a language this build cannot run.
func (c *Client) SupportsHookType(ctx context.Context, hookType string) bool {
	if err := c.ready(); err != nil {
		return false
	}

	if c.deps.Hooks == nil {
		return false
	}

	lang, err := domain.ParseScriptLanguage(strings.TrimSpace(hookType))
	if err != nil {
		return false
	}

	return c.deps.Hooks.HasRunner(lang)
}

// ValidateHookScript checks one hook script without storing it, so an editor can
// reject a bad script as it is typed. mod may be nil, which checks everything not
// dependent on the mod's folder; a script with a working directory outside the
// mod's and game's folders is accepted here for the mod service to reject.
func (c *Client) ValidateHookScript(ctx context.Context, script HookScript, mod *Mod) error {
	if err := c.ready(); err != nil {
		return err
	}

	if c.deps.Hooks == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a hook executor")
	}

	return c.deps.Hooks.Validate(script, mod)
}

// HookTimeout returns how long a script would be allowed to run for the given
// mod: its own timeout clamped into the effective floor and ceiling. A zero result
// means the client has no hook executor.
func (c *Client) HookTimeout(ctx context.Context, script HookScript, mod *Mod) time.Duration {
	if c == nil || c.deps.Hooks == nil {
		return 0
	}

	return c.deps.Hooks.Timeout(script, mod)
}

// HookPermissions returns the effective capability set for one mod under one game:
// the mod's scope over the game's over the default, first true win per capability,
// first non-zero timeout. gameID and modID are explicit because the caller is
// usually asking about something other than the live game.
func (c *Client) HookPermissions(
	ctx context.Context,
	gameID, modID string,
) (ScriptPermissions, error) {
	if err := c.ready(); err != nil {
		return ScriptPermissions{}, err
	}

	if ctx != nil && ctx.Err() != nil {
		return ScriptPermissions{}, appErrors.New(appErrors.ErrCodeOperationCanceled,
			"operation stopped before permissions could be resolved")
	}

	store, err := c.configStore()
	if err != nil {
		return ScriptPermissions{}, err
	}

	cfg, err := store.Load()
	if err != nil || cfg == nil {
		return ScriptPermissions{}, err
	}

	return cfg.Hooks.Permissions.For(strings.TrimSpace(gameID), strings.TrimSpace(modID)), nil
}

// RunHookScripts executes scripts under the same containment rules the mod lifecycle
// uses.
func (c *Client) RunHookScripts(ctx context.Context, scripts []HookScript, mod *Mod) error {
	if err := c.ready(); err != nil {
		return err
	}

	if c.deps.Hooks == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a hook executor")
	}

	return c.deps.Hooks.Execute(ctx, scripts, mod)
}
