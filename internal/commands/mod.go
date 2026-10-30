package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ModCommands is the mod lifecycle as a frontend sees it: list, install, toggle,
// remove, and the questions a mod screen has to answer (updates, conflicts, load
// order, links).
type ModCommands struct {
	// Embedded so a caller holding one set can shut it down without the
	// aggregate.
	*base
}

// NewModCommands builds the mod command set. A nil client is tolerated and
// reported by every method, so a frontend can still start and render an error.
func NewModCommands(deps Deps) *ModCommands {
	return &ModCommands{base: newBase(deps)}
}

// newModCommands builds the set over a shared scope, so the aggregate in app.go
// gives every set one lifecycle.
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

// InstallMod installs and, when a game mods folder resolves, enables the mod in
// one step; the returned Enabled field says which happened.
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

// ImportLink installs a mod named by a Nexus or Vortex link.
//
// The link is resolved by pkg/domain, the same pure parser `dmm mod import-link`
// uses, so a link that works from a terminal works here. A Nexus link the website
// already authorised carries key, expires and user_id; those travel as SourceData
// because the Nexus API cannot issue a download URL without a paid entitlement.
//
// The caller's SourceData is merged under the grant, so a caller who does not
// know what the link means cannot discard the authorisation.
func (c *ModCommands) ImportLink(link string, opts api.InstallOptions) (*api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	sourceID, modID, game, grant, err := domain.ParseModLink(link)
	if err != nil {
		return nil, c.fail(err, "import link")
	}

	// The parser reports the game's sub-domain; only the registry knows which games
	// are installed, so the check happens here. Otherwise a link for a game this
	// build lacks would be answered with a real, different mod.
	if _, ok := games.BySourceDomain(sourceID, game); !ok {
		return nil, c.fail(appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%q is a link for the %q version of %s, which this manager does not have; "+
				"it can install links for %s",
			link, game, sourceID, knownGames(sourceID)), "import link")
	}

	sourceData := make(map[string]any, len(grant)+len(opts.SourceData))
	for key, value := range opts.SourceData {
		sourceData[key] = value
	}

	for key, value := range grant {
		sourceData[key] = value
	}

	mod, err := client.Install(ctx, sourceID, modID, api.InstallOptions{
		Enable:     opts.Enable,
		Force:      opts.Force,
		PurgeCache: opts.PurgeCache,
		SourceData: sourceData,
	})
	if err != nil {
		return nil, c.fail(err, "install %s/%s from link", sourceID, modID)
	}

	return mod, nil
}

// knownGames lists the sub-domains the installed games claim on a source, so a
// refusal can tell the user which links would work.
func knownGames(sourceID string) string {
	slugs := games.SourceDomains(sourceID)
	if len(slugs) == 0 {
		return "no game yet"
	}

	return strings.Join(slugs, ", ")
}

// ShareLinkForMod prints a link another machine can install this mod from. A
// manager that cannot print one forces the user into a browser to read out an id.
// A local folder has nothing to share, and the call says so.
func (c *ModCommands) ShareLinkForMod(id string) (string, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return "", err
	}
	defer cancel()

	mod, err := client.GetMod(ctx, strings.TrimSpace(id))
	if err != nil {
		return "", c.fail(err, "get mod %q", id)
	}

	link, err := domain.ModLinkForMod(*mod)
	if err != nil {
		return "", c.fail(err, "share mod %q", id)
	}

	return link, nil
}

// UninstallMod removes a mod's record, its payload and its links, keeping the
// downloaded archive in the cache.
func (c *ModCommands) UninstallMod(id string) error {
	return c.UninstallModWithOptions(id, api.UninstallOptions{})
}

// UninstallModWithOptions is UninstallMod with the purge flag, which also drops
// the cached archive. It sits beside the simple form because the archive is often
// wanted again on the next launch.
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

// TrashAvailable tells the delete dialog whether it can offer "move to trash".
func (c *ModCommands) TrashAvailable() bool {
	client, _, cancel, err := c.ready()
	if err != nil {
		return false
	}
	defer cancel()

	return client.TrashAvailable()
}

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

// UpdateModHooks replaces a mod's whole hook set, because the frontend sends back
// the whole set it is editing; a merge would make removing a hook impossible.
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
// source that cannot be reached degrades to "nothing known", so this is safe to
// call on a timer.
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
// nothing was running, because the caller wanted the update stopped either way.
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

// ResolveConflict settles one reported conflict. The resolution arrives as a
// string because that is what the bridge can carry, and an unrecognised one is
// refused here: "that is not a resolution I know" is actionable.
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

// ModFile is one entry inside a mod's installed folder. The Files tab asks what
// is in a mod; a mod folder holds plenty that is never linked.
type ModFile struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
}

// ListModFiles walks the mod's installed folder and reports what is in it.
// Entries are relative to the mod folder, and the walk is bounded: a listing that
// never ends is worse than a truncated one.
func (c *ModCommands) ListModFiles(id string) ([]ModFile, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	root, err := client.ModPath(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, c.fail(err, "path of mod %q", id)
	}

	files, err := collectModFiles(root)
	if err != nil {
		return nil, c.fail(err, "files of mod %q", id)
	}

	return files, nil
}

// maxModFileEntries caps a listing. The number is arbitrary; it is written down
// because an unbounded walk of a user-controlled directory can hang the UI.
const maxModFileEntries = 2000

func collectModFiles(root string) ([]ModFile, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return []ModFile{}, nil
	}

	files := make([]ModFile, 0, 64)

	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A file the manager cannot read is skipped: the listing is
			// informational and not worth emptying over one bad entry.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}

			return nil
		}

		if len(files) >= maxModFileEntries {
			return fs.SkipAll
		}

		if path == root {
			return nil
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}

		if entry.IsDir() {
			files = append(files, ModFile{Path: relative, Name: entry.Name(), IsDir: true})
			return nil
		}

		var size int64
		if fileInfo, statErr := entry.Info(); statErr == nil {
			size = fileInfo.Size()
		}

		files = append(files, ModFile{Path: relative, Name: entry.Name(), Size: size})

		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	return files, nil
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
