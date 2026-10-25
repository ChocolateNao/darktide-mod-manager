package api

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Aliases for the service-layer value types that appear in this package's
// signatures.
type (
	// InstallOptions tunes InstallMod. The zero value enables the mod when a game
	// mods folder resolves, refuses to overwrite an existing install, and keeps
	// the downloaded archive.
	InstallOptions = services.InstallOptions
	// UninstallOptions tunes UninstallMod.
	UninstallOptions = services.UninstallOptions
	// ConflictResolution says how a caller wants a reported conflict settled.
	ConflictResolution = services.ConflictResolution
	// LinkStatus describes one mod's deployed links in the game folder.
	LinkStatus = services.ModLinkStatus
	// ConflictReport describes one problem found between installed mods.
	ConflictReport = domain.ConflictReport
	// ModUpdateInfo describes what is known about a mod's available update.
	ModUpdateInfo = domain.ModUpdateInfo
	// ModHooks is a mod's hook scripts, grouped by lifecycle phase.
	ModHooks = domain.ModHooks
	// SearchQuery describes what a caller wants from a source search.
	SearchQuery = domain.SearchQuery
)

// The conflict resolutions. A report names two mods, sorted, as "A" and "B"; which
// is which is available from ConflictReport.ModIDs, and a resolution refers to that
// sorted order so a caller need not remember which side it is looking at.
const (
	// ResolutionUnset is the zero value and is refused: a caller that meant to
	// resolve a conflict and did not say how should be told, not guessed at.
	ResolutionUnset ConflictResolution = services.ResolutionUnset
	// ResolutionKeepBoth accepts the conflict and changes nothing.
	ResolutionKeepBoth ConflictResolution = services.ResolutionKeepBoth
	// ResolutionDisableModA disables the first of the two mods in the report.
	ResolutionDisableModA ConflictResolution = services.ResolutionDisableModA
	// ResolutionDisableModB disables the second.
	ResolutionDisableModB ConflictResolution = services.ResolutionDisableModB
	// ResolutionUninstallModA uninstalls the first.
	ResolutionUninstallModA ConflictResolution = services.ResolutionUninstallModA
	// ResolutionUninstallModB uninstalls the second.
	ResolutionUninstallModB ConflictResolution = services.ResolutionUninstallModB
)

// ParseConflictResolution turns the textual form of a resolution back into the value,
// reporting false for anything it does not recognise — including the zero value. 
// It is re-exported here because both entry
// points that need it work in terms of this package: the GUI passes a resolution as a
// JSON string over the Wails bridge and the CLI takes it as a flag argument. It
// accepts the spellings ConflictResolution.String produces, with either a dash or an
// underscore and any surrounding space or case.
func ParseConflictResolution(text string) (ConflictResolution, bool) {
	return services.ParseConflictResolution(text)
}

// mods returns the mod service, or the error to report. A client built from services
// may legitimately have no mod service, and every operation here would otherwise
// nil-panic at the first call.
func (c *Client) mods() (*services.ModService, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.Mods == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a mod service")
	}

	return c.deps.Mods, nil
}

// ListMods returns every installed mod in load order.
func (c *Client) ListMods(ctx context.Context) ([]Mod, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.ListMods(ctx)
}

// GetMod returns one installed mod. An unknown id reports ErrCodeModNotFound.
func (c *Client) GetMod(ctx context.Context, id string) (*Mod, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.GetMod(ctx, id)
}

// InstallMod downloads and installs a mod, and enables it when a game mods folder
// resolves. The returned mod's Enabled field says which happened: installing on a
// machine with no detected game is a perfectly normal thing to do, and the caller
// should not have to re-read the config to find out.
func (c *Client) InstallMod(ctx context.Context, sourceID, modID string) (*Mod, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.Install(ctx, sourceID, modID, InstallOptions{})
}

// Install is InstallMod with explicit options.
func (c *Client) Install(
	ctx context.Context,
	sourceID, modID string,
	opts InstallOptions,
) (*Mod, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.Install(ctx, sourceID, modID, opts)
}

// UninstallMod removes a mod's database record, its deployed links and its
// payload. The downloaded archive is kept, because a reinstall is then a local
// copy; pass UninstallOptions{PurgeCache: true} to drop it too.
func (c *Client) UninstallMod(ctx context.Context, modID string) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.Uninstall(ctx, modID, UninstallOptions{})
}

// Uninstall is UninstallMod with explicit options.
func (c *Client) Uninstall(ctx context.Context, modID string, opts UninstallOptions) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.Uninstall(ctx, modID, opts)
}

// TrashAvailable reports whether this host has a desktop trash the manager can
// move an external mod into.
func (c *Client) TrashAvailable() bool {
	if c == nil || c.deps.Mods == nil {
		return false
	}

	return c.deps.Mods.TrashAvailable()
}

// EnableMod deploys a mod's files into the game folder. The mod's own
// pre-enable hooks run first and are fatal: a mod that cannot prepare itself
// should not be half enabled.
func (c *Client) EnableMod(ctx context.Context, modID string) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.EnableMod(ctx, modID)
}

// DisableMod removes a mod's files from the game folder and keeps its payload, so
// enabling it again is instant.
func (c *Client) DisableMod(ctx context.Context, modID string) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.DisableMod(ctx, modID)
}

// UpdateMod brings one mod up to date from its source. It reports
// ErrCodeModNotInstalled for a mod that is not installed, and does nothing when
// the source has nothing newer.
func (c *Client) UpdateMod(ctx context.Context, modID string) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.UpdateMod(ctx, modID)
}

// ApplyUpdate is UpdateMod but returns the installed mod afterwards, for a caller
// that wants to show the new version without a second lookup.
func (c *Client) ApplyUpdate(ctx context.Context, modID string) (*Mod, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.ApplyUpdate(ctx, modID)
}

// CancelUpdate stops an update of modID that is running now. Cancelling an update
// that is not running is not an error: the caller's intent — "not updating" —
// holds either way.
func (c *Client) CancelUpdate(ctx context.Context, modID string) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.CancelUpdate(ctx, modID)
}

// CheckModUpdates reports what is known about available updates without changing
// anything.
func (c *Client) CheckModUpdates(ctx context.Context) ([]ModUpdateInfo, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.CheckModUpdates(ctx)
}

// RefreshMods reconciles the database with what is on disk: a mod whose payload
// vanished is disabled, and the links of enabled mods are repaired. Call it after
// something else may have touched the game folder.
func (c *Client) RefreshMods(ctx context.Context) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.RefreshMods(ctx)
}

// DetectConflicts reports the problems the manager can see between installed
// mods: overlapping files, ids that would share a payload folder, declared
// incompatibilities, dependency and load-order problems, and unmet version
// requirements.
//
// Only enabled mods are examined. A disabled mod's files are not in the game
// folder, so reporting its overlaps would be noise the user cannot act on.
func (c *Client) DetectConflicts(ctx context.Context) ([]ConflictReport, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.DetectConflicts(ctx)
}

// ResolveConflict settles one reported conflict. An unknown id reports ErrCodeNotFound.
//
// how refers to the two mods in the report's sorted order. A report with only one mod
// — a missing dependency has no second mod to blame — reports ErrCodeInvalidArgument.
func (c *Client) ResolveConflict(
	ctx context.Context,
	reportID string,
	how ConflictResolution,
) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.ResolveConflict(ctx, reportID, how)
}

// GetModHooks returns a mod's hooks. The value is a copy, so mutating it does
// not change the mod until UpdateModHooks is called.
func (c *Client) GetModHooks(ctx context.Context, modID string) (ModHooks, error) {
	svc, err := c.mods()
	if err != nil {
		return ModHooks{}, err
	}

	return svc.GetModHooks(ctx, modID)
}

// UpdateModHooks replaces a mod's hooks wholesale. Every script is validated
// before anything is stored, so a rejected hook leaves the mod's existing hooks
// untouched.
func (c *Client) UpdateModHooks(ctx context.Context, modID string, hooks ModHooks) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.UpdateModHooks(ctx, modID, hooks)
}

// ReorderMods sets the load order. Ids that name no installed mod are refused
// with ErrCodeInvalidArgument, because a load order naming an absent mod is a
// caller bug that would otherwise show up later as a silently missing entry.
func (c *Client) ReorderMods(ctx context.Context, modIDs []string) error {
	svc, err := c.mods()
	if err != nil {
		return err
	}

	return svc.ReorderMods(ctx, modIDs)
}

// LoadOrder returns the current load order.
func (c *Client) LoadOrder(ctx context.Context) ([]string, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.LoadOrder(ctx)
}

// ModPath returns the folder holding one mod's payload. It is what a caller
// opens in a file browser when a user asks "where did this mod go?".
func (c *Client) ModPath(ctx context.Context, modID string) (string, error) {
	svc, err := c.mods()
	if err != nil {
		return "", err
	}

	return svc.ModPath(ctx, modID)
}

// SymlinkStatus reports, for every installed mod, whether its files are properly
// linked into the game folder. Damaged links are those that are broken or point
// somewhere else — the two states a user hits after moving a game install.
func (c *Client) SymlinkStatus(ctx context.Context) ([]LinkStatus, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.SymlinkStatus(ctx)
}

// RelinkMod rebuilds one mod's links and returns the entry names it had to
// repair. An empty slice means everything was already correct.
func (c *Client) RelinkMod(ctx context.Context, modID string) ([]string, error) {
	svc, err := c.mods()
	if err != nil {
		return nil, err
	}

	return svc.RelinkMod(ctx, modID)
}
