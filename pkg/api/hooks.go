package api

import (
	"context"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Aliases for the hook vocabulary. A caller building a hook editor needs the
// script and phase types without importing pkg/domain separately, and the
// lifecycle phase names are part of this package's contract rather than an
// implementation detail.
type (
	// HookScript is one hook: what to run, how long it may run, and the
	// environment it may see.
	HookScript = domain.HookScript
	// HookType names a kind of hook script: inline JavaScript, a file, a shell
	// command, or a compiled-in Go function.
	HookType = domain.HookType
	// HookPhase names a point in a mod's lifecycle where hooks run.
	HookPhase = domain.HookPhase
)

// The hook script types. HookTypeGo is only usable when the manager was built
// with compiled-in hooks; SupportsHookType reports what the running build
// actually has.
const (
	HookTypeInline  HookType = domain.HookTypeInline
	HookTypeFile    HookType = domain.HookTypeFile
	HookTypeCommand HookType = domain.HookTypeCommand
	HookTypeGo      HookType = domain.HookTypeGo
)

// HookTypes returns every hook script type the domain defines. It is not what
// the running build can execute; SupportsHookType answers that.
func HookTypes() []string {
	types := domain.HookTypes()

	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, string(t))
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

// SupportsHookType reports whether this build can execute the given hook type.
//
// A hook editor must ask before offering a choice: a manager built without a
// shell cannot run a command hook, and offering one would let a user author a
// mod that fails on their machine and works on a colleague's.
func (c *Client) SupportsHookType(ctx context.Context, hookType string) bool {
	if err := c.ready(); err != nil {
		return false
	}

	if c.deps.Hooks == nil {
		return false
	}

	return c.deps.Hooks.HasRunner(domain.HookType(strings.TrimSpace(hookType)))
}

// ValidateHookScript checks one hook script without storing it, so an editor can
// reject a bad script as it is typed instead of at enable time.
//
// mod may be nil, which checks everything that does not depend on the mod's
// folder. A script with a working directory outside the mod's and the game's
// folders cannot be checked that way, and is accepted here for the mod service to
// reject when it has the mod in hand.
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

// HookTimeout returns how long a script would be allowed to run: its own timeout
// when it declares one, otherwise the configured default. A zero result means the
// client has no hook executor.
func (c *Client) HookTimeout(ctx context.Context, script HookScript) time.Duration {
	if c == nil || c.deps.Hooks == nil {
		return 0
	}

	return c.deps.Hooks.Timeout(script)
}

// RunHookScripts executes scripts under the same containment rules the mod
// lifecycle uses.
//
// It is exposed because a caller maintaining a mod needs to run a hook by hand —
// "does this script actually work?" is the question a mod author asks first, and
// making them install the mod to find out is a poor answer.
//
// The containment is the point: a script's working directory is resolved against
// the mod's and the game's folders and refused if it escapes both, its
// environment is filtered to an allowlist, and it is killed at its timeout.
// Those rules are the reason this is a method on Client rather than a bare
// hook executor handed out.
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
