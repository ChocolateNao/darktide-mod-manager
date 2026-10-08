package commands

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ModCommands is the mod lifecycle as a frontend sees it: list, install, toggle,
// remove, and the questions a mod screen has to answer (updates, conflicts, load
// order, links).
//
// A mod list that cannot show a conflict, an update badge or a broken link is
// not a mod list, and each of those is one call into pkg/api with no logic of
// its own.
type ModCommands struct {
	// base is embedded rather than held as a named field so the lifecycle
	// methods reach a set directly: a caller holding a single set needs to be
	// able to shut it down without knowing the shape of the aggregate.
	*base
}

// NewModCommands builds the mod command set. A nil client is tolerated here and
// reported by every method, so a frontend can still render an error instead of
// failing to start.
func NewModCommands(deps Deps) *ModCommands {
	return &ModCommands{base: newBase(deps)}
}

// newModCommands builds the set over a shared scope, which is how the aggregate
// in app.go gives every set one lifecycle without this set having to know about
// its siblings.
func newModCommands(b *base) *ModCommands {
	return &ModCommands{base: b}
}

// ListMods returns every installed mod.
func (c *ModCommands) ListMods() ([]api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	mods, err := client.ListMods(ctx)
	if err != nil {
		return nil, c.fail(err, "list mods")
	}

	return mods, nil
}

// GetMod returns one installed mod.
func (c *ModCommands) GetMod(id string) (*api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	mod, err := client.GetMod(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, c.fail(err, "get mod %q", id)
	}

	return mod, nil
}

// InstallMod installs and, when a game mods folder resolves, enables a mod in
// one step. The returned mod's Enabled field is what says which happened.
func (c *ModCommands) InstallMod(sourceID, modID string) (*api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	mod, err := client.InstallMod(ctx, strings.TrimSpace(sourceID), strings.TrimSpace(modID))
	if err != nil {
		return nil, c.fail(err, "install %s/%s", sourceID, modID)
	}

	return mod, nil
}

// UninstallMod removes a mod's record, its payload and its links, keeping the
// downloaded archive in the cache.
func (c *ModCommands) UninstallMod(id string) error {
	return c.UninstallModWithOptions(id, api.UninstallOptions{})
}

// UninstallModWithOptions is UninstallMod with the purge flag, which also drops
// the cached archive. It exists beside the simple form rather than replacing it
// because the archive is often wanted again on the next launch, and a frontend
// asking for "remove" should not have to know that.
func (c *ModCommands) UninstallModWithOptions(id string, opts api.UninstallOptions) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	trimmed := strings.TrimSpace(id)
	if err := client.Uninstall(ctx, trimmed, opts); err != nil {
		return c.fail(err, "uninstall mod %q", id)
	}

	return nil
}

// EnableMod deploys a mod's files into the game folder.
func (c *ModCommands) EnableMod(id string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	trimmed := strings.TrimSpace(id)
	if err := client.EnableMod(ctx, trimmed); err != nil {
		return c.fail(err, "enable mod %q", id)
	}

	return nil
}

// DisableMod removes a mod's files from the game folder and keeps its payload,
// so re-enabling costs nothing.
func (c *ModCommands) DisableMod(id string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	trimmed := strings.TrimSpace(id)
	if err := client.DisableMod(ctx, trimmed); err != nil {
		return c.fail(err, "disable mod %q", id)
	}

	return nil
}

// GetModHooks returns a mod's hook scripts, grouped by phase.
func (c *ModCommands) GetModHooks(id string) (api.ModHooks, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return api.ModHooks{}, err
	}
	defer cancel()

	hooks, err := client.GetModHooks(ctx, strings.TrimSpace(id))
	if err != nil {
		return api.ModHooks{}, c.fail(err, "read hooks of mod %q", id)
	}

	return hooks, nil
}

// UpdateModHooks replaces a mod's whole hook set.
//
// It replaces rather than merges because the frontend holds the set it is
// editing and sends it back whole; a merge would make removing a hook through
// the UI impossible.
func (c *ModCommands) UpdateModHooks(id string, hooks api.ModHooks) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	trimmed := strings.TrimSpace(id)
	if err := client.UpdateModHooks(ctx, trimmed, hooks); err != nil {
		return c.fail(err, "write hooks of mod %q", id)
	}

	return nil
}

// CheckModUpdates reports what is known about each mod's available update. A
// source that cannot be reached degrades to "nothing known" rather than failing
// the whole sweep, so this is safe to call on a timer.
func (c *ModCommands) CheckModUpdates() ([]api.ModUpdateInfo, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	updates, err := client.CheckModUpdates(ctx)
	if err != nil {
		return nil, c.fail(err, "check for mod updates")
	}

	return updates, nil
}

// UpdateMod applies the newest published version of an installed mod.
func (c *ModCommands) UpdateMod(id string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	trimmed := strings.TrimSpace(id)
	if err := client.UpdateMod(ctx, trimmed); err != nil {
		return c.fail(err, "update mod %q", id)
	}

	return nil
}

// CancelUpdate stops an update already in flight. It reports success when
// nothing was running, because the caller wanted the update to stop and it is
// stopped either way.
func (c *ModCommands) CancelUpdate(id string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	trimmed := strings.TrimSpace(id)
	if err := client.CancelUpdate(ctx, trimmed); err != nil {
		return c.fail(err, "cancel update of mod %q", id)
	}

	return nil
}

// RefreshMods re-checks the payload folder and the deployed links of every mod,
// disabling one whose payload has gone and repairing links that are broken.
func (c *ModCommands) RefreshMods() error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.RefreshMods(ctx); err != nil {
		return c.fail(err, "refresh mods")
	}

	return nil
}

// DetectConflicts reports the problems found between the enabled mods.
func (c *ModCommands) DetectConflicts() ([]api.ConflictReport, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	reports, err := client.DetectConflicts(ctx)
	if err != nil {
		return nil, c.fail(err, "detect conflicts")
	}

	return reports, nil
}

// ResolveConflict settles one reported conflict.
//
// The resolution arrives as a string because that is what the bridge can carry,
// and an unrecognised one is refused here rather than reaching the service as an
// unset value: "that is not a resolution I know" is the complaint a user can act
// on, where "no resolution was given" sends them looking for a field they already
// filled in.
func (c *ModCommands) ResolveConflict(reportID, how string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	resolution, ok := api.ParseConflictResolution(how)
	if !ok {
		return appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"%q is not a conflict resolution; expected keep-both, disable-mod-a, disable-mod-b, uninstall-mod-a or uninstall-mod-b",
			how,
		)
	}

	id := strings.TrimSpace(reportID)
	if err := client.ResolveConflict(ctx, id, resolution); err != nil {
		return c.fail(err, "resolve conflict %q", reportID)
	}

	return nil
}

// ReorderMods sets the load order from a complete list of mod ids.
func (c *ModCommands) ReorderMods(ids []string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.ReorderMods(ctx, ids); err != nil {
		return c.fail(err, "reorder mods")
	}

	return nil
}

// LoadOrder returns the current load order.
func (c *ModCommands) LoadOrder() ([]string, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	order, err := client.LoadOrder(ctx)
	if err != nil {
		return nil, c.fail(err, "read load order")
	}

	return order, nil
}

// ModPath returns where a mod's payload is kept.
func (c *ModCommands) ModPath(id string) (string, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return "", err
	}
	defer cancel()

	path, err := client.ModPath(ctx, strings.TrimSpace(id))
	if err != nil {
		return "", c.fail(err, "path of mod %q", id)
	}

	return path, nil
}

// SymlinkStatus reports the state of each enabled mod's deployed links, and how
// many of them need repair.
func (c *ModCommands) SymlinkStatus() ([]api.LinkStatus, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	status, err := client.SymlinkStatus(ctx)
	if err != nil {
		return nil, c.fail(err, "read link status")
	}

	return status, nil
}

// RelinkMod rebuilds one mod's links and returns the entries it rewrote.
func (c *ModCommands) RelinkMod(id string) ([]string, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	trimmed := strings.TrimSpace(id)

	repaired, err := client.RelinkMod(ctx, trimmed)
	if err != nil {
		return nil, c.fail(err, "relink mod %q", id)
	}

	return repaired, nil
}
