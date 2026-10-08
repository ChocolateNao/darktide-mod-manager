// ModService is the mod lifecycle orchestrator and the single source of truth
// for installed mods. The GUI, the CLI and the public API all go through it, so
// there is exactly one implementation of "what does enabling a mod do".
//
// # Dependency shape
//
// ModService depends on narrow interfaces, not on the concrete cache, registry,
// locator or deployer. That is what lets the whole lifecycle — including the
// rollback paths — be exercised against fakes without a game install, a network
// or a real archive.
//
// # Where mod files live
//
// A mod's payload is extracted into manager storage, never into the game folder:
// <storage>/<mod id>/<version>. The game folder only ever receives symlinks (or
// copies, where symlinks are unavailable) pointing at that payload.
//
// # Hook policy
//
// A Pre* hook failure is fatal: the operation rolls back and reports the hook
// error. A Post* hook failure is advisory — it is published as EventHookFailed
// and otherwise ignored. By the time a Post* hook runs the database row is
// already committed, so reporting "install failed" for a mod that is in fact
// installed would be worse than the log noise.
package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// SourceProvider resolves a source id to a live source. services.SourceRegistry
// satisfies it.
type SourceProvider interface {
	Get(ctx context.Context, id string) (domain.ModSource, error)
}

// ModCache is the slice of CacheManager the mod lifecycle needs. CacheKey is the
// package-level function of the same name, not a method, so it is not listed.
type ModCache interface {
	GetOrDownload(ctx context.Context, mod *domain.Mod) (string, error)
	// Release drops one reference to a cached blob.
	Release(ctx context.Context, hash string) error
	// Remove deletes a cached blob outright.
	Remove(ctx context.Context, hash string) error
}

// PhaseRunner is the slice of HookExecutor the lifecycle needs. It is not the
// same thing as HookRunner: a HookRunner executes one script, while this runs a
// whole phase of a mod's hooks.
type PhaseRunner interface {
	ExecutePhase(
		ctx context.Context,
		phase domain.HookPhase,
		hooks domain.ModHooks,
		mod *domain.Mod,
	) error
}

// ModIndex is the persistence contract for installed mods. storage.ModRepository
// satisfies it; it is restated here so a test can supply a much smaller fake.
type ModIndex interface {
	Save(ctx context.Context, m *domain.Mod) error
	GetByID(ctx context.Context, id string) (*domain.Mod, error)
	List(ctx context.Context) ([]domain.Mod, error)
	ListEnabled(ctx context.Context) ([]domain.Mod, error)
	UpdateEnabled(ctx context.Context, id string, enabled bool) error
	UpdatePriority(ctx context.Context, id string, priority int) error
	SaveFiles(ctx context.Context, modID string, files []domain.ModFile) error
	GetFilesByMod(ctx context.Context, modID string) ([]domain.ModFile, error)
	Delete(ctx context.Context, id string) error
	SetLoadOrder(ctx context.Context, ids []string) error
	LoadOrder(ctx context.Context) ([]string, error)
	Count(ctx context.Context) (int, error)
}

// ModLocator is the slice of GameLocator the lifecycle needs.
type ModLocator interface {
	// Current returns the game installation in use, or ErrGameNotFound.
	Current(ctx context.Context) (*domain.GameInstallation, error)
	// GetModsPath resolves the mods folder for an installation.
	GetModsPath(install *domain.GameInstallation) (string, error)
}

// FileDeployer is the slice of platform.Deployer the lifecycle needs.
type FileDeployer interface {
	Mode() platform.DeployMode
	DeployAs(srcDir, dstDir string, mode platform.DeployMode) error
	UndeployEntries(dstDir string, names []string) (int, error)
	VerifyFolder(dstDir string) (map[string]platform.LinkState, int, error)
	Repair(srcDir, dstDir string) ([]string, error)
	Owns(dstDir, name string) bool
}

// ArchiveExtractor is the slice of storage.ArchiveExtractor the lifecycle needs.
type ArchiveExtractor interface {
	Extract(ctx context.Context, archivePath, destDir string) (storage.ExtractionResult, error)
}

// ModServiceDeps wires the orchestrator. Every field is optional except Index,
// which is the only thing without a sensible in-memory default.
type ModServiceDeps struct {
	Extractor   ArchiveExtractor
	Cache       ModCache
	Index       ModIndex
	Hooks       PhaseRunner
	Locator     ModLocator
	Deployer    FileDeployer
	Sources     SourceProvider
	Config      ConfigStore
	Events      EventSink
	Clock       Clock
	Logger      *zerolog.Logger
	Platform    domain.Platform
	StoragePath string
}

// InstallOptions tunes Install and Reinstall.
type InstallOptions struct {
	Enable     *bool
	SourceData map[string]any
	Force      bool
	PurgeCache bool
}

// mergeSourceData copies entries from extra into a mod's SourceData, leaving
// values the source already published in place.
//
// A source's own record of a mod is the authority on that mod: it is what the
// platform said, and a caller supplying the same key is either repeating it or
// contradicting it. The second is not allowed to happen silently, so an
// existing key is kept. What this is for is the opposite case — a key the
// platform cannot know, such as the one-time authorization a keyed link
// carries, which only the caller that parsed the link has.
func mergeSourceData(mod *domain.Mod, extra map[string]any) {
	if mod == nil || len(extra) == 0 {
		return
	}

	if mod.SourceData == nil {
		mod.SourceData = make(map[string]any, len(extra))
	}

	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}

		if _, exists := mod.SourceData[key]; exists {
			continue
		}

		mod.SourceData[key] = value
	}
}

// UninstallOptions tunes UninstallMod.
type UninstallOptions struct {
	// PurgeCache deletes the downloaded archive as well.
	PurgeCache bool
}

// ModLinkStatus describes one mod's deployed links in the game folder.
type ModLinkStatus struct {
	Entries map[string]platform.LinkState
	ModID   string
	Damaged int
}

// ConflictResolution says how a caller wants a reported conflict settled.
type ConflictResolution int

const (
	// ResolutionUnset is the zero value and is rejected.
	ResolutionUnset ConflictResolution = iota
	// ResolutionKeepBoth accepts the conflict as intentional and does nothing.
	ResolutionKeepBoth
	// ResolutionDisableModA / B disable the corresponding mod.
	ResolutionDisableModA
	ResolutionDisableModB
	// ResolutionUninstallModA / B uninstall the corresponding mod.
	ResolutionUninstallModA
	ResolutionUninstallModB
)

// String renders the resolution for error messages and CLI output.
func (r ConflictResolution) String() string {
	switch r {
	case ResolutionKeepBoth:
		return "keep-both"
	case ResolutionDisableModA:
		return "disable-mod-a"
	case ResolutionDisableModB:
		return "disable-mod-b"
	case ResolutionUninstallModA:
		return "uninstall-mod-a"
	case ResolutionUninstallModB:
		return "uninstall-mod-b"
	default:
		return "unset"
	}
}

// ParseConflictResolution turns the textual form of a resolution back into the
// value, reporting false for anything it does not recognise.
//
// It exists because the value crosses a boundary as a string: over the Wails
// bridge it arrives as a JSON string, and on the command line as a flag
// argument. Both entry points need the same table, so it lives next to the type
// rather than in one of them. The spellings accepted are the ones String
// produces, with either a dash or an underscore and any surrounding space or
// case, because a flag argument typed by hand is the most likely place for a
// hyphen to have been typed as an underscore.
func ParseConflictResolution(text string) (ConflictResolution, bool) {
	normalized := strings.ToLower(strings.TrimSpace(text))

	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "keep-both":
		return ResolutionKeepBoth, true
	case "disable-mod-a":
		return ResolutionDisableModA, true
	case "disable-mod-b":
		return ResolutionDisableModB, true
	case "uninstall-mod-a":
		return ResolutionUninstallModA, true
	case "uninstall-mod-b":
		return ResolutionUninstallModB, true
	default:
		// "unset" and everything unrecognised both land here. ResolutionUnset is
		// rejected downstream anyway, so accepting it here would only mean
		// ResolveConflict reported a less specific complaint than "that is not a
		// resolution I know".
		return ResolutionUnset, false
	}
}

// ModService is the orchestrator described in the package comment.
type ModService struct {
	now    Clock
	log    *zerolog.Logger
	ops    *opTable
	active map[string]*updateRun
	deps   ModServiceDeps
	mu     sync.Mutex
}

// updateRun tracks an in-flight ApplyUpdate so CancelUpdate has something to
// cancel.
type updateRun struct {
	// ctx is the context the update runs under; it is set by updateContext right
	// after beginUpdate, so it is nil for the instant between the two.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// cancelled records a CancelUpdate that arrived before updateContext ran, so
	// a cancel landing in that window is honoured instead of dropped.
	cancelled bool
}

// NewModService builds the orchestrator. A nil Index is rejected: without it
// there is no state to orchestrate, and failing here beats nil-panicking later.
func NewModService(deps ModServiceDeps) (*ModService, error) {
	if deps.Index == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "mod service needs a mod index")
	}

	if deps.Platform == "" {
		deps.Platform = domain.CurrentPlatform()
	}

	if deps.Clock == nil {
		deps.Clock = time.Now
	}

	return &ModService{
		deps:   deps,
		now:    deps.Clock,
		log:    loggerOf(deps.Logger),
		ops:    newOpTable(),
		active: make(map[string]*updateRun),
	}, nil
}

// ListMods returns every installed mod in the index's canonical order.
func (s *ModService) ListMods(ctx context.Context) ([]domain.Mod, error) {
	if s == nil || s.deps.Index == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"mod service is not initialised",
		)
	}

	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	return s.deps.Index.List(ctx)
}

// GetMod returns one installed mod, or ErrModNotFound.
func (s *ModService) GetMod(ctx context.Context, id string) (*domain.Mod, error) {
	if s == nil || s.deps.Index == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"mod service is not initialised",
		)
	}

	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	return s.deps.Index.GetByID(ctx, id)
}

// InstallMod installs a mod and, unless told otherwise, enables it when a game
// mods folder resolves.
func (s *ModService) InstallMod(ctx context.Context, sourceID, modID string) (*domain.Mod, error) {
	return s.Install(ctx, sourceID, modID, InstallOptions{})
}

// Install is InstallMod with explicit options.
func (s *ModService) Install(
	ctx context.Context,
	sourceID, modID string,
	opts InstallOptions,
) (*domain.Mod, error) {
	if s == nil || s.deps.Index == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"mod service is not initialised",
		)
	}

	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	sourceID = strings.TrimSpace(sourceID)
	modID = strings.TrimSpace(modID)

	if sourceID == "" {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"source id is required to install a mod",
		)
	}

	if modID == "" {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"mod id is required to install a mod",
		)
	}

	if s.deps.Sources == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeSourceUnavailable,
			"no source registry is configured",
		)
	}

	src, err := s.deps.Sources.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}

	if src == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeSourceNotFound,
			"source %q resolved to nothing",
			sourceID,
		)
	}

	remote, err := src.GetMod(ctx, modID)
	if err != nil {
		return nil, err
	}

	if remote == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeModNotFound,
			"source %q has no mod %q",
			sourceID,
			modID,
		)
	}
	// The source's id is authoritative; a source that returns a mod stamped
	// with a different one would make every later update lookup miss.
	installed := remote.Clone()
	installed.SourceID = sourceID

	installed.ID = strings.TrimSpace(installed.ID)
	if installed.ID == "" {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidMod,
			"source %q returned a mod with no id",
			sourceID,
		)
	}

	mergeSourceData(installed, opts.SourceData)
	// Apply install_mode from SourceData if provided.
	if modeStr, ok := installed.SourceData["install_mode"].(string); ok {
		mode := domain.ModInstallMode(strings.TrimSpace(modeStr))
		if mode.Valid() {
			installed.InstallMode = mode
		}
	}

	release, err := s.ops.acquire(ctx, installed.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	s.log.Debug().
		Str("op", "install").
		Str("source", sourceID).
		Str("mod", installed.ID).
		Str("version", installed.Version).
		Msg("installing mod")

	existing, err := s.deps.Index.GetByID(ctx, installed.ID)
	switch {
	case err == nil && !opts.Force:
		return nil, appErrors.New(appErrors.ErrCodeModAlreadyExists,
			"mod %q is already installed as version %s", existing.ID, existing.Version)
	case err != nil && appErrors.CodeOf(err) != appErrors.ErrCodeModNotFound:
		return nil, err
	}

	root, err := s.storageRoot()
	if err != nil {
		return nil, err
	}

	target := domain.ModVersionPath(root, installed.ID, installed.Version)

	// Pre-install hooks see a mod that is not yet on disk, so their LocalPath is
	// empty rather than pointing at a folder that is about to be replaced.
	s.publish(Event{
		Type: EventModInstallStarted, ModID: installed.ID, SourceID: sourceID,
		Data: installEventData{Version: installed.Version, Path: target},
	})

	if err := s.runHooks(ctx, domain.PhasePreInstall, installed); err != nil {
		return nil, err
	}

	archive, hash, err := s.fetchArchive(ctx, installed)
	if err != nil {
		return nil, err
	}

	if opts.PurgeCache {
		defer s.discardArchive(ctx, installed, hash)
	}

	files, err := s.unpack(ctx, archive, target)
	if err != nil {
		return nil, err
	}

	// Determine the mod's folder name in the mods folder based on its
	// install mode and the archive structure. Only mods deployed to the
	// mods folder get a folder name; game root mods do not appear in
	// mod_load_order.txt.
	if installed.InstallMode != domain.InstallModeGameRoot {
		installed.ModFolderName = primaryModFolderName(files)
	}

	now := s.now()
	installed.LocalPath = target
	installed.Hash = hash
	installed.InstalledAt = now

	installed.UpdatedAt = now
	if existing != nil {
		installed.InstalledAt = existing.InstalledAt
		if installed.InstalledAt.IsZero() {
			installed.InstalledAt = now
		}

		installed.Priority = existing.Priority
	}

	if opts.Enable != nil {
		installed.Enabled = *opts.Enable
	} else {
		// Enabled stays false until the deploy below proves the game folder is
		// there; claiming otherwise would show the user a mod the game cannot
		// load.
		installed.Enabled = false
	}

	// The mod row is written before its files. mod_files.mod_id is a foreign key
	// onto mods.id and the database enforces it, so recording the inventory first
	// fails on every install — and a fake index that does not enforce referential
	// integrity cannot catch it. A record whose file list has not been written yet
	// is the recoverable state (a reinstall rewrites both); a file list pointing
	// at a mod that does not exist is not representable at all.
	if err := s.deps.Index.Save(ctx, installed); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInstallFailed,
			"record mod %q",
			installed.ID,
		)
	}

	if err := s.deps.Index.SaveFiles(ctx, installed.ID, files); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInstallFailed,
			"record the file inventory of mod %q", installed.ID)
	}

	wantEnabled := opts.Enable != nil && *opts.Enable
	if opts.Enable == nil {
		// Auto: enable only when a game mods folder actually resolves.
		if _, err := s.modsPath(ctx); err == nil {
			wantEnabled = true
		}
	}

	if wantEnabled {
		if err := s.enableLocked(ctx, installed); err != nil {
			return nil, err
		}
	}

	s.publish(Event{
		Type:     EventModInstallCompleted,
		ModID:    installed.ID,
		SourceID: sourceID,
		Data: installEventData{
			Version: installed.Version,
			Path:    target,
			Files:   len(files),
			Enabled: installed.Enabled,
		},
	})
	s.log.Info().
		Str("op", "install").
		Str("source", sourceID).
		Str("mod", installed.ID).
		Str("version", installed.Version).
		Int("files", len(files)).
		Bool("enabled", installed.Enabled).
		Msg("mod installed")
	s.runHooksAdvisory(ctx, domain.PhasePostInstall, installed)

	return installed, nil
}

// ReinstallMod reinstalls a mod over an existing installation, keeping the
// installed timestamp and load priority.
func (s *ModService) ReinstallMod(
	ctx context.Context,
	sourceID, modID string,
	opts InstallOptions,
) (*domain.Mod, error) {
	opts.Force = true
	return s.Install(ctx, sourceID, modID, opts)
}

// installEventData is the payload attached to install lifecycle events.
type installEventData struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	Files   int    `json:"files,omitempty"`
	Enabled bool   `json:"enabled"`
}

// fetchArchive asks the cache for the payload and returns both the archive path
// and the cache key that identifies it, so a later uninstall can purge it.
func (s *ModService) fetchArchive(ctx context.Context, mod *domain.Mod) (string, string, error) {
	key, err := CacheKey(mod)
	if err != nil {
		return "", "", err
	}

	if s.deps.Cache == nil {
		return "", "", appErrors.New(appErrors.ErrCodeCacheMiss,
			"no download cache is configured, cannot fetch mod %q", modLabel(mod))
	}

	path, err := s.deps.Cache.GetOrDownload(ctx, mod)
	if err != nil {
		return "", "", err
	}

	if strings.TrimSpace(path) == "" {
		return "", "", appErrors.New(appErrors.ErrCodeDownloadFailed,
			"cache returned no archive for mod %q", modLabel(mod))
	}

	return path, key, nil
}

// discardArchive drops the cached blob after a successful extraction.
func (s *ModService) discardArchive(ctx context.Context, mod *domain.Mod, key string) {
	if s.deps.Cache == nil || key == "" {
		return
	}

	if err := s.deps.Cache.Remove(ctx, key); err != nil {
		s.publish(
			Event{
				Type:  EventLog,
				ModID: modLabel(mod),
				Data:  fmt.Sprintf("purge cache %s: %v", key, err),
			},
		)
	}
}

// unpack extracts the archive into destDir and returns the inventory to record.
//
// Extraction goes into a staging directory first and is moved into place only
// once it has succeeded: a half-extracted mod in the live folder would look
// installed to the game, and re-installing over it would compound the damage.
func (s *ModService) unpack(
	ctx context.Context,
	archive, destDir string,
) ([]domain.ModFile, error) {
	if s.deps.Extractor == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"no archive extractor is configured",
		)
	}

	parent := filepath.Dir(destDir)
	if err := platform.MkdirAll(parent); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInstallFailed,
			"create the storage folder %q",
			parent,
		)
	}

	staging, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInstallFailed,
			"create a staging folder in %q",
			parent,
		)
	}
	// Cleanup covers every failure path below; a successful rename makes the
	// staging path disappear and RemoveAll then finds nothing.
	defer func() { _ = os.RemoveAll(staging) }()

	result, err := s.deps.Extractor.Extract(ctx, archive, staging)
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInstallFailed,
			"extract mod archive %q",
			archive,
		)
	}

	if err := os.RemoveAll(destDir); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInstallFailed,
			"clear the old payload at %q",
			destDir,
		)
	}

	if err := os.Rename(staging, destDir); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInstallFailed,
			"move the payload into %q",
			destDir,
		)
	}
	// The rename moved the directory; removing the (now stale) staging path is
	// a no-op, but on Windows renaming onto an existing path can leave the
	// source behind.
	defer func() { _ = os.RemoveAll(staging) }()

	files := make([]domain.ModFile, 0, len(result.Files))
	for _, f := range result.Files {
		rel := strings.TrimSpace(f.RelPath)
		if rel == "" || platform.IsManifestName(rel) {
			// Manifests live in the payload folder for the manager's own use;
			// recording them would make undeployment try to remove something the
			// deployer never created.
			continue
		}

		f.ModID = ""
		f.StoredPath = filepath.Join(destDir, filepath.FromSlash(rel))
		files = append(files, f)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })

	return files, nil
}

// EnableMod deploys a mod's files into the game folder and marks it enabled.
//
// A mod that is already enabled is still redeployed: that is what heals a
// deleted or half-created set of links, and an idempotent enable should leave
// the mod usable rather than merely labelled enabled.
func (s *ModService) EnableMod(ctx context.Context, modID string) error {
	if s == nil || s.deps.Index == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod service is not initialised")
	}

	if err := s.precheck(ctx); err != nil {
		return err
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	release, err := s.ops.acquire(ctx, modID)
	if err != nil {
		return err
	}
	defer release()

	mod, err := s.deps.Index.GetByID(ctx, modID)
	if err != nil {
		return err
	}

	return s.enableLocked(ctx, mod)
}

// enableLocked performs the enable while the caller already holds the mod's
// operation lock.
func (s *ModService) enableLocked(ctx context.Context, mod *domain.Mod) error {
	if strings.TrimSpace(mod.LocalPath) == "" {
		return appErrors.New(appErrors.ErrCodeInvalidMod,
			"mod %q has no payload folder; reinstall it", modLabel(mod))
	}

	if !platform.IsDir(platform.RealFS(), mod.LocalPath) {
		return appErrors.New(appErrors.ErrCodeInvalidMod,
			"the payload folder of mod %q is gone; reinstall it", modLabel(mod))
	}

	// PreEnable hooks see the mod as it is about to become: still disabled.
	pre := mod.Clone()

	pre.Enabled = false
	if err := s.runHooks(ctx, domain.PhasePreEnable, pre); err != nil {
		return err
	}

	dst, err := s.deploymentPath(ctx, mod)
	if err != nil {
		return err
	}

	if s.deps.Deployer == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "no file deployer is configured")
	}

	if err := s.deps.Deployer.DeployAs(mod.LocalPath, dst, s.deps.Deployer.Mode()); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInstallFailed,
			"deploy mod %q into %q", modLabel(mod), dst)
	}

	mod.Enabled = true

	mod.UpdatedAt = s.now()
	if err := s.deps.Index.Save(ctx, mod); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "mark mod %q enabled", modLabel(mod))
	}

	if err := s.syncLoadOrderFile(ctx); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "sync load order file after enable")
	}

	s.publish(Event{
		Type: EventModEnabled, ModID: mod.ID, SourceID: mod.SourceID,
		Data: installEventData{Version: mod.Version, Path: mod.LocalPath, Enabled: true},
	})
	s.log.Info().
		Str("op", "enable").
		Str("mod", mod.ID).
		Str("path", dst).
		Msg("mod enabled")
	s.runHooksAdvisory(ctx, domain.PhasePostEnable, mod)

	return nil
}

// DisableMod removes a mod's files from the game folder and marks it disabled.
// The payload stays in manager storage: disabling is reversible and cheap, and
// deleting a user's files behind their back is not something a toggle should do.
func (s *ModService) DisableMod(ctx context.Context, modID string) error {
	if s == nil || s.deps.Index == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod service is not initialised")
	}

	if err := s.precheck(ctx); err != nil {
		return err
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	release, err := s.ops.acquire(ctx, modID)
	if err != nil {
		return err
	}
	defer release()

	mod, err := s.deps.Index.GetByID(ctx, modID)
	if err != nil {
		return err
	}

	pre := mod.Clone()

	pre.Enabled = true
	if err := s.runHooks(ctx, domain.PhasePreDisable, pre); err != nil {
		return err
	}
	// A game that is not configured cannot hold the links, so failing here would
	// make a disable impossible on a machine where the game was never set up.
	if err := s.undeploy(ctx, mod); err != nil {
		return err
	}

	mod.Enabled = false

	mod.UpdatedAt = s.now()
	if err := s.deps.Index.Save(ctx, mod); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "mark mod %q disabled", modLabel(mod))
	}

	if err := s.syncLoadOrderFile(ctx); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "sync load order file after disable")
	}

	s.publish(Event{
		Type: EventModDisabled, ModID: mod.ID, SourceID: mod.SourceID,
		Data: installEventData{Version: mod.Version, Path: mod.LocalPath},
	})
	s.log.Info().
		Str("op", "disable").
		Str("mod", mod.ID).
		Msg("mod disabled")
	s.runHooksAdvisory(ctx, domain.PhasePostDisable, mod)

	return nil
}

// uninstallRemovesLinks removes the mod's entries from the game folder. It is
// best effort in the sense that a missing game folder is not an error, but a
// deployer that fails is: the mod would otherwise stay in the game while the
// manager believes it is gone.
func (s *ModService) undeploy(ctx context.Context, mod *domain.Mod) error {
	dst, err := s.deploymentPath(ctx, mod)
	if err != nil {
		return nil
	}

	if s.deps.Deployer == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "no file deployer is configured")
	}

	files, err := s.deps.Index.GetFilesByMod(ctx, mod.ID)
	if err != nil {
		return err
	}

	if _, err := s.deps.Deployer.UndeployEntries(dst, topLevelNames(files)); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeUninstallFailed,
			"remove the deployed files of mod %q", modLabel(mod))
	}

	return nil
}

// UninstallMod removes a mod's payload, its record and (optionally) its
// archive.
func (s *ModService) UninstallMod(ctx context.Context, modID string) error {
	return s.Uninstall(ctx, modID, UninstallOptions{})
}

// Uninstall is UninstallMod with explicit options.
func (s *ModService) Uninstall(ctx context.Context, modID string, opts UninstallOptions) error {
	if s == nil || s.deps.Index == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod service is not initialised")
	}

	if err := s.precheck(ctx); err != nil {
		return err
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	release, err := s.ops.acquire(ctx, modID)
	if err != nil {
		return err
	}
	defer release()

	mod, err := s.deps.Index.GetByID(ctx, modID)
	if err != nil {
		return err
	}

	s.publish(Event{Type: EventModUninstallStarted, ModID: mod.ID, SourceID: mod.SourceID})
	// PostUninstall still gets a hook context: the mod is still known, it is
	// just about to stop being.
	if err := s.runHooks(ctx, domain.PhasePreUninstall, mod); err != nil {
		return err
	}

	if err := s.undeploy(ctx, mod); err != nil {
		return err
	}

	if err := s.deps.Index.Delete(ctx, mod.ID); err != nil {
		return appErrors.Wrap(
			err,
			appErrors.ErrCodeUninstallFailed,
			"delete the record of mod %q",
			modLabel(mod),
		)
	}

	if err := s.removePayload(mod); err != nil {
		return err
	}

	if opts.PurgeCache {
		s.discardArchive(ctx, mod, cacheKeyFor(mod))
	}

	if err := s.syncLoadOrderFile(ctx); err != nil {
		return appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"sync load order file after uninstall",
		)
	}

	s.publish(Event{Type: EventModUninstallCompleted, ModID: mod.ID, SourceID: mod.SourceID})
	s.log.Info().
		Str("op", "uninstall").
		Str("mod", mod.ID).
		Str("source", mod.SourceID).
		Bool("purged_cache", opts.PurgeCache).
		Msg("mod uninstalled")
	s.runHooksAdvisory(ctx, domain.PhasePostUninstall, mod)

	return nil
}

// removePayload deletes the mod's version folder and any now-empty parent.
//
// The folder is only removed when it really is inside the configured storage
// root: LocalPath comes from the database, and a database edited by hand (or
// restored from someone else's backup) could point anywhere.
func (s *ModService) removePayload(mod *domain.Mod) error {
	payload := strings.TrimSpace(mod.LocalPath)
	if payload == "" {
		return nil
	}

	root, err := s.storageRoot()
	if err != nil {
		// Without a root there is nothing to prove containment against, so the
		// safest reading of "I do not know where storage is" is "delete nothing".
		s.publish(Event{
			Type: EventLog, ModID: modLabel(mod),
			Data: fmt.Sprintf("kept payload %q: storage root is unknown", payload),
		})

		return nil
	}

	if !domain.PathWithin(root, payload) {
		s.publish(Event{
			Type: EventLog, ModID: modLabel(mod),
			Data: fmt.Sprintf("kept payload %q: outside the storage root %q", payload, root),
		})

		return nil
	}

	if err := os.RemoveAll(payload); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeUninstallFailed,
			"remove the payload folder %q of mod %q", payload, modLabel(mod))
	}

	_ = platform.RemoveEmptyDir(filepath.Dir(payload))

	return nil
}

// cacheKeyFor recomputes the cache key of a stored mod. The key is derived, not
// stored, because it is a hash of the download location and the version — both
// of which the record already has. A mod whose SourceData was stripped cannot
// yield a key, which is not worth an error on an uninstall.
func cacheKeyFor(mod *domain.Mod) string {
	key, err := CacheKey(mod)
	if err != nil {
		return ""
	}

	return key
}

// topLevelNames reduces a file inventory to the names that actually appear in
// the game folder.
//
// The deployer creates one entry per top-level entry of the payload folder, so a
// mod holding "textures/hud.dds" puts a single "textures" entry in the game.
// Passing the full relative paths to UndeployEntries would match nothing and the
// mod would silently stay deployed after an uninstall.
func topLevelNames(files []domain.ModFile) []string {
	seen := make(map[string]bool, len(files))

	out := make([]string, 0, len(files))
	for _, f := range files {
		rel := strings.ReplaceAll(strings.TrimSpace(f.RelPath), "\\", "/")
		if rel == "" {
			continue
		}

		name := rel
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			name = rel[:i]
		}

		switch name {
		case "", ".", "..":
			continue
		}

		if strings.ContainsRune(name, filepath.Separator) || seen[name] {
			continue
		}

		seen[name] = true
		out = append(out, name)
	}

	return out
}

// primaryModFolderName returns the primary folder name for a mod deployed to
// the mods folder. It uses the first top-level directory from the file inventory.
// Returns empty string if there are no top-level directories (e.g. flat archives).
// primaryModFolderName returns the primary folder name for a mod deployed to
// the mods folder. It uses the first top-level directory from the file
// inventory. Returns empty string if there are no top-level directories
// (e.g. flat archives with only files at the root).
func primaryModFolderName(files []domain.ModFile) string {
	names := topLevelNames(files)
	if len(names) == 0 {
		return ""
	}
	// Build a map of rel path -> file for quick lookup
	fileMap := make(map[string]domain.ModFile, len(files))
	for _, f := range files {
		fileMap[f.RelPath] = f
	}
	// Return the first name that corresponds to a directory (SizeBytes == 0)
	// or has children (other files under it).
	for _, name := range names {
		// Check if this name is a directory entry
		if f, ok := fileMap[name]; ok && f.SizeBytes == 0 && f.Checksum == "" {
			return name
		}
		// Check if any file has this name as a prefix (meaning it's a parent folder)
		prefix := name + "/"
		for _, f := range files {
			if strings.HasPrefix(f.RelPath, prefix) {
				return name
			}
		}
	}
	// No directory found at top level
	return ""
}

// GetModHooks returns the hooks recorded for a mod.
func (s *ModService) GetModHooks(ctx context.Context, modID string) (domain.ModHooks, error) {
	mod, err := s.GetMod(ctx, modID)
	if err != nil {
		return domain.ModHooks{}, err
	}

	return mod.Hooks.Clone(), nil
}

// UpdateModHooks replaces the hooks recorded for a mod.
//
// The mod's own files are not touched by a hook edit, so this does not take the
// mod's operation lock: it changes what will run next time, not what is
// deployed now. Concurrent enable/disable still serialise against each other.
func (s *ModService) UpdateModHooks(
	ctx context.Context,
	modID string,
	hooks domain.ModHooks,
) error {
	if s == nil || s.deps.Index == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod service is not initialised")
	}

	if err := s.precheck(ctx); err != nil {
		return err
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	for _, phase := range domain.HookPhases() {
		for i, script := range hooks.For(phase) {
			// i+1: the position is shown to the user, who counts hooks from one.
			if err := s.validateHook(script, phase, i+1); err != nil {
				return err
			}
		}
	}

	mod, err := s.deps.Index.GetByID(ctx, modID)
	if err != nil {
		return err
	}

	mod.Hooks = hooks.Clone()

	mod.UpdatedAt = s.now()
	if err := s.deps.Index.Save(ctx, mod); err != nil {
		return appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"record the hooks of mod %q",
			modLabel(mod),
		)
	}

	return nil
}

// ReorderMods stores an explicit load order. It validates the set against the
// index first so a typo cannot silently drop a mod from the order.
func (s *ModService) ReorderMods(ctx context.Context, modIDs []string) error {
	if s == nil || s.deps.Index == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod service is not initialised")
	}

	if err := s.precheck(ctx); err != nil {
		return err
	}

	ordered := make([]string, 0, len(modIDs))

	seen := make(map[string]bool, len(modIDs))
	for _, raw := range modIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}

		if seen[id] {
			return appErrors.New(
				appErrors.ErrCodeInvalidArgument,
				"mod %q appears twice in the load order",
				id,
			)
		}

		seen[id] = true
		ordered = append(ordered, id)
	}

	if err := s.deps.Index.SetLoadOrder(ctx, ordered); err != nil {
		return err
	}

	if err := s.syncLoadOrderFile(ctx); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "sync load order file")
	}

	return nil
}

// LoadOrder returns the explicit load order, or nil when none was ever set.
func (s *ModService) LoadOrder(ctx context.Context) ([]string, error) {
	if s == nil || s.deps.Index == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"mod service is not initialised",
		)
	}

	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	return s.deps.Index.LoadOrder(ctx)
}

// ModPath returns the payload folder of an installed mod.
func (s *ModService) ModPath(ctx context.Context, modID string) (string, error) {
	mod, err := s.GetMod(ctx, modID)
	if err != nil {
		return "", err
	}

	return mod.LocalPath, nil
}

// RelinkMod recreates the mod's links in the game folder without changing its
// enabled state. It is the repair action for a broken or retargeted set of
// links.
func (s *ModService) RelinkMod(ctx context.Context, modID string) ([]string, error) {
	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	release, err := s.ops.acquire(ctx, modID)
	if err != nil {
		return nil, err
	}
	defer release()

	mod, err := s.deps.Index.GetByID(ctx, modID)
	if err != nil {
		return nil, err
	}

	if s.deps.Deployer == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"no file deployer is configured",
		)
	}

	dst, err := s.deploymentPath(ctx, mod)
	if err != nil {
		return nil, err
	}

	repaired, err := s.deps.Deployer.Repair(mod.LocalPath, dst)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInstallFailed,
			"repair the links of mod %q", modLabel(mod))
	}

	return repaired, nil
}

// SymlinkStatus reports the state of every mod's entries in the game folder.
//
// A mod whose game folder cannot be resolved still appears in the result with an
// empty map: the caller wants to know about all its mods, and "no game folder"
// is different from "no links".
func (s *ModService) SymlinkStatus(ctx context.Context) ([]ModLinkStatus, error) {
	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	mods, err := s.deps.Index.List(ctx)
	if err != nil {
		return nil, err
	}

	var (
		modsFolder string
		gameRoot   string
	)

	if install, err := s.gameInstall(ctx); err == nil {
		modsFolder, _ = s.modsPathFor(install)
		gameRoot = filepath.Dir(modsFolder)
	}

	out := make([]ModLinkStatus, 0, len(mods))
	for i := range mods {
		mod := mods[i]

		status := ModLinkStatus{ModID: mod.ID}
		if s.deps.Deployer == nil {
			out = append(out, status)
			continue
		}
		// Determine the correct folder to check based on the mod's install mode.
		var folder string
		if mod.InstallMode == domain.InstallModeGameRoot {
			folder = gameRoot
		} else {
			folder = modsFolder
		}

		if folder != "" {
			entries, _, err := s.deps.Deployer.VerifyFolder(folder)
			if err == nil && len(entries) > 0 {
				owned := s.ownedEntries(ctx, mod, entries)
				if len(owned) > 0 {
					status.Entries = owned
					for _, state := range owned {
						if state.NeedsRepair() {
							status.Damaged++
						}
					}
				}
			}
		}

		out = append(out, status)
	}

	return out, nil
}

// ownedEntries narrows a folder-wide link report down to the entries this mod
// owns, judged by which top-level names its inventory says it deployed.
func (s *ModService) ownedEntries(
	ctx context.Context,
	mod domain.Mod,
	all map[string]platform.LinkState,
) map[string]platform.LinkState {
	files, err := s.deps.Index.GetFilesByMod(ctx, mod.ID)
	if err != nil {
		return nil
	}

	out := make(map[string]platform.LinkState)

	for _, name := range topLevelNames(files) {
		if state, ok := all[name]; ok {
			out[name] = state
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// RefreshMods repairs the links of every enabled mod and disables any mod whose
// payload has disappeared from under it.
//
// Disabling rather than deleting is the deliberate choice: a payload the user
// moved or deleted by hand should not cost them the mod's identity, hooks or
// load order, and re-installing is one command away.
func (s *ModService) RefreshMods(ctx context.Context) error {
	if s == nil || s.deps.Index == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod service is not initialised")
	}

	if err := s.precheck(ctx); err != nil {
		return err
	}

	mods, err := s.deps.Index.List(ctx)
	if err != nil {
		return err
	}

	for i := range mods {
		if err := ctx.Err(); err != nil {
			return canceledError(err)
		}

		mod := mods[i]
		if !platform.IsDir(platform.RealFS(), mod.LocalPath) {
			// Warn: the mod is still listed, so this is a state the user has to
			// know about rather than a crash. It is worth a log line even though
			// an event already reports it, because refresh can run unattended.
			s.log.Warn().
				Str("op", "refresh").
				Str("mod", mod.ID).
				Str("missing_path", mod.LocalPath).
				Bool("enabled", mod.Enabled).
				Msg("mod payload is missing")

			if mod.Enabled {
				if err := s.DisableMod(ctx, mod.ID); err != nil {
					return err
				}

				s.publish(Event{
					Type: EventLog, ModID: mod.ID, SourceID: mod.SourceID,
					Data: fmt.Sprintf("disabled: payload %q is missing", mod.LocalPath),
				})
			}

			continue
		}

		if !mod.Enabled || s.deps.Deployer == nil {
			continue
		}

		dst, err := s.deploymentPath(ctx, &mod)
		if err != nil {
			// If the game folder isn't configured, skip this mod but continue
			// with the rest rather than failing the whole sweep.
			continue
		}

		if _, err := s.deps.Deployer.Repair(mod.LocalPath, dst); err != nil {
			return appErrors.Wrap(err, appErrors.ErrCodeInstallFailed,
				"refresh the links of mod %q", modLabel(&mod))
		}
	}

	return nil
}

// CheckModUpdates asks every installed mod's source what the latest version is.
//
// One source being unreachable or wrong about one mod does not fail the whole
// sweep: the result then simply says "no update known" for that mod, which is
// what the UI would have shown anyway had the request never been made.
func (s *ModService) CheckModUpdates(ctx context.Context) ([]domain.ModUpdateInfo, error) {
	if s == nil || s.deps.Index == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"mod service is not initialised",
		)
	}

	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	mods, err := s.deps.Index.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]domain.ModUpdateInfo, 0, len(mods))
	for i := range mods {
		if err := ctx.Err(); err != nil {
			return nil, canceledError(err)
		}

		out = append(out, s.updateInfoFor(ctx, mods[i]))
	}

	return out, nil
}

// updateInfoFor builds the update record for one mod, tolerating every failure
// mode of the lookup.
func (s *ModService) updateInfoFor(ctx context.Context, mod domain.Mod) domain.ModUpdateInfo {
	info := domain.ModUpdateInfo{
		ModID:        mod.ID,
		LocalVersion: mod.Version,
		SourceID:     mod.SourceID,
	}

	remote, err := s.remoteMod(ctx, mod)
	if err != nil || remote == nil {
		return info
	}

	info.RemoteVersion = remote.Version
	info.ReleaseNotes = remote.Description
	info.DownloadURL = DownloadLocation(remote)
	info.Available = domain.CompareVersions(remote.Version, mod.Version) > 0

	return info
}

// remoteMod fetches a mod's current listing from the source it came from.
func (s *ModService) remoteMod(ctx context.Context, mod domain.Mod) (*domain.Mod, error) {
	if s.deps.Sources == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeSourceUnavailable,
			"no source registry is configured",
		)
	}

	src, err := s.deps.Sources.Get(ctx, mod.SourceID)
	if err != nil {
		return nil, err
	}

	if src == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeSourceNotFound,
			"source %q is not registered",
			mod.SourceID,
		)
	}

	return src.GetMod(ctx, mod.ID)
}

// ApplyUpdate reinstalls a mod from its source and removes the old payload once
// the new one is in place.
//
// The update keeps the mod's installed timestamp and load priority: to a user
// this is the same mod, updated, not a fresh install that happens to share an id.
// A failure leaves the old payload and the old record untouched.
func (s *ModService) ApplyUpdate(ctx context.Context, modID string) (*domain.Mod, error) {
	if s == nil || s.deps.Index == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"mod service is not initialised",
		)
	}

	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	if err := s.beginUpdate(modID); err != nil {
		return nil, err
	}
	defer s.endUpdate(modID)
	// Everything below runs under a context CancelUpdate can abort, so a cancel
	// lands during the download or the extraction rather than after them.
	ctx = s.updateContext(ctx, modID)

	// No per-mod lock here: Install takes it below, and taking it twice for the
	// same id would deadlock, since the lock is a token the holder must return.
	mod, err := s.deps.Index.GetByID(ctx, modID)
	if err != nil {
		return nil, err
	}

	if mod.SourceID == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidMod,
			"mod %q has no source recorded, cannot update it", modLabel(mod))
	}

	remote, err := s.remoteMod(ctx, *mod)
	if err != nil {
		return nil, err
	}

	if remote == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeModNotFound,
			"source %q no longer has mod %q",
			mod.SourceID,
			mod.ID,
		)
	}

	if domain.CompareVersions(remote.Version, mod.Version) <= 0 {
		// Already current. Reporting the installed mod keeps the caller from
		// having to special-case "nothing to do".
		return mod, nil
	}

	oldPath := mod.LocalPath
	wasEnabled := mod.Enabled
	keepOld := s.keepOldVersions(ctx)

	s.log.Info().
		Str("op", "update").
		Str("mod", mod.ID).
		Str("source", mod.SourceID).
		Str("from_version", mod.Version).
		Str("to_version", remote.Version).
		Msg("updating mod")

	s.publish(Event{
		Type: EventModUpdateStarted, ModID: mod.ID, SourceID: mod.SourceID,
		Data: installEventData{Version: remote.Version},
	})

	// An update that goes wrong must not leave the mod half-written, so the
	// enable state is restored from the record rather than assumed: DisableMod
	// would need the operation lock this goroutine already holds.
	opts := InstallOptions{Force: true, Enable: &wasEnabled}

	updated, err := s.Install(ctx, mod.SourceID, mod.ID, opts)
	if err != nil {
		return nil, err
	}
	// Install kept the record's priority because Force reinstalls read the
	// existing row; InstalledAt is preserved inside Install for the same reason.
	// The previous payload lives in its own version folder, so retiring it only
	// when the user asked us not to keep old copies is what makes the retention
	// setting mean anything; the archive in the cache is a separate decision and
	// is left alone.
	if !keepOld && oldPath != "" && !domain.PathsEqual(oldPath, updated.LocalPath) {
		if err := os.RemoveAll(oldPath); err != nil {
			s.log.Warn().
				Err(err).
				Str("op", "update").
				Str("mod", mod.ID).
				Str("old_path", oldPath).
				Msg("could not remove the superseded payload")
			s.publish(Event{
				Type: EventLog, ModID: mod.ID,
				Data: fmt.Sprintf("could not remove the old payload %q: %v", oldPath, err),
			})
		} else {
			_ = platform.RemoveEmptyDir(filepath.Dir(oldPath))
		}
	}

	s.publish(Event{
		Type:     EventModUpdateCompleted,
		ModID:    updated.ID,
		SourceID: updated.SourceID,
		Data: installEventData{
			Version: updated.Version,
			Path:    updated.LocalPath,
			Enabled: updated.Enabled,
		},
	})
	s.publish(Event{
		Type: EventModUpdated, ModID: updated.ID, SourceID: updated.SourceID,
		Data: installEventData{Version: updated.Version},
	})
	s.log.Info().
		Str("op", "update").
		Str("mod", updated.ID).
		Str("version", updated.Version).
		Str("path", updated.LocalPath).
		Msg("mod updated")

	return updated, nil
}

// UpdateMod is ApplyUpdate for callers that do not want the resulting mod back.
func (s *ModService) UpdateMod(ctx context.Context, modID string) error {
	_, err := s.ApplyUpdate(ctx, modID)
	return err
}

// keepOldVersions reports whether the previous version's payload should be kept
// after an update. A retention of zero days is the user saying "do not keep old
// copies"; the default (7 days) keeps them.
func (s *ModService) keepOldVersions(_ context.Context) bool {
	if s.deps.Config == nil {
		return true
	}

	cfg, err := s.deps.Config.Load()
	if err != nil || cfg == nil {
		return true
	}

	return cfg.Storage.BackupRetentionDays != 0
}

// beginUpdate registers an in-flight update so CancelUpdate can reach it.
func (s *ModService) beginUpdate(modID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.active[modID]; ok {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"an update of mod %q is already running", modID)
	}

	s.active[modID] = &updateRun{}

	return nil
}

// updateContext derives the context the update runs under, wiring it to the
// registry entry CancelUpdate cancels.
func (s *ModService) updateContext(ctx context.Context, modID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	run := s.active[modID]
	if run == nil {
		return ctx
	}

	run.ctx, run.cancel = context.WithCancel(ctx)
	if run.cancelled {
		// A cancel that landed a moment ago would otherwise be silently dropped.
		run.cancel()
	}

	return run.ctx
}

// endUpdate clears the registration. The cancel is always called so the
// detached context does not leak its timer.
func (s *ModService) endUpdate(modID string) {
	s.mu.Lock()
	run := s.active[modID]
	delete(s.active, modID)
	s.mu.Unlock()

	if run == nil {
		return
	}

	if run.cancel != nil {
		run.cancel()
	}

	if run.done != nil {
		close(run.done)
	}
}

// CancelUpdate cancels an in-flight update of modID. Cancelling an update that
// is not running is not an error: the caller wanted it to stop, and it is
// stopped.
func (s *ModService) CancelUpdate(ctx context.Context, modID string) error {
	if s == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod service is not initialised")
	}

	if err := s.precheck(ctx); err != nil {
		return err
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	s.mu.Lock()

	run := s.active[modID]
	if run == nil {
		s.mu.Unlock()
		return nil
	}

	if run.cancel == nil {
		// The update has registered but has not wired its context yet. Record
		// the request; updateContext honours it as soon as it runs.
		run.cancelled = true
		s.mu.Unlock()

		return nil
	}

	cancel := run.cancel
	s.mu.Unlock()
	cancel()

	return nil
}

// storageRoot resolves the folder that holds mod payloads.
func (s *ModService) storageRoot() (string, error) {
	raw := strings.TrimSpace(s.deps.StoragePath)
	if raw == "" {
		if s.deps.Config == nil {
			return "", appErrors.New(
				appErrors.ErrCodeNoStoragePath,
				"no configuration store is wired",
			)
		}

		cfg, err := s.deps.Config.Load()
		if err != nil {
			return "", appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "load the configuration")
		}

		raw = strings.TrimSpace(cfg.Storage.Path)
		if raw == "" {
			dirs, err := platform.CurrentDirs()
			if err != nil {
				return "", appErrors.Wrap(
					err,
					appErrors.ErrCodeNoStoragePath,
					"resolve the default storage folder",
				)
			}

			raw = dirs.StorageRoot()
		}
	}

	if !filepath.IsAbs(raw) {
		return "", appErrors.New(appErrors.ErrCodeNoStoragePath,
			"the storage path %q must be absolute", raw)
	}

	return filepath.Clean(raw), nil
}

// gameInstall resolves the game installation in use, tolerating a missing
// locator by reporting "no game".
func (s *ModService) gameInstall(ctx context.Context) (*domain.GameInstallation, error) {
	if s.deps.Locator == nil {
		return nil, appErrors.New(appErrors.ErrCodeGameNotFound, "no game locator is configured")
	}

	install, err := s.deps.Locator.Current(ctx)
	if err != nil {
		return nil, err
	}

	if install == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeGameNotFound,
			"no game installation is configured",
		)
	}

	return install, nil
}

// modsPath resolves the game's mods folder.
func (s *ModService) modsPath(ctx context.Context) (string, error) {
	install, err := s.gameInstall(ctx)
	if err != nil {
		return "", err
	}

	return s.modsPathFor(install)
}

// modsPathFor resolves the mods folder of a known installation.
func (s *ModService) modsPathFor(install *domain.GameInstallation) (string, error) {
	if s.deps.Locator == nil {
		return "", appErrors.New(appErrors.ErrCodeGameNotFound, "no game locator is configured")
	}

	path, err := s.deps.Locator.GetModsPath(install)
	if err != nil {
		return "", err
	}

	path = strings.TrimSpace(path)
	if path == "" {
		return "", appErrors.New(
			appErrors.ErrCodeModsPathNotDir,
			"the mods folder is not configured",
		)
	}

	return path, nil
}

// gameRootPath resolves the game root folder (the parent of the mods folder).
func (s *ModService) gameRootPath(ctx context.Context) (string, error) {
	modsPath, err := s.modsPath(ctx)
	if err != nil {
		return "", err
	}

	return filepath.Dir(modsPath), nil
}

// deploymentPath returns the destination path for a mod based on its
// InstallMode. Mods with InstallModeGameRoot deploy to the game root;
// all others deploy to the mods folder.
func (s *ModService) deploymentPath(ctx context.Context, mod *domain.Mod) (string, error) {
	if mod.InstallMode == domain.InstallModeGameRoot {
		return s.gameRootPath(ctx)
	}

	return s.modsPath(ctx)
}

// syncLoadOrderFile writes the current load order to the mods folder's
// mod_load_order.txt file. Only mods with InstallModeModsFolder and a
// non-empty ModFolderName are included. The file is written atomically.
func (s *ModService) syncLoadOrderFile(ctx context.Context) error {
	modsFolder, err := s.modsPath(ctx)
	if err != nil {
		// If the mods folder cannot be resolved, there's nothing to sync.
		// This is not an error: a user without a game configured should not
		// have the operation fail because of a missing load order file.
		return nil
	}

	loadOrder, err := s.deps.Index.LoadOrder(ctx)
	if err != nil {
		return err
	}

	var lines []string

	for _, modID := range loadOrder {
		mod, err := s.deps.Index.GetByID(ctx, modID)
		if err != nil {
			// Skip mods that no longer exist in the index.
			continue
		}

		if mod.InstallMode == domain.InstallModeGameRoot {
			// Game root mods don't appear in the mods folder load order.
			continue
		}

		if mod.ModFolderName == "" {
			// Mod has no folder name recorded; skip it.
			continue
		}

		lines = append(lines, mod.ModFolderName)
	}
	// Also include enabled mods that are not in the explicit load order,
	// falling back to their priority order.
	enabledMods, err := s.deps.Index.ListEnabled(ctx)
	if err != nil {
		return err
	}

	explicitSet := make(map[string]bool, len(loadOrder))
	for _, id := range loadOrder {
		explicitSet[id] = true
	}
	// Sort enabled mods by priority (descending) then by ID for stability.
	sort.SliceStable(enabledMods, func(i, j int) bool {
		if enabledMods[i].Priority != enabledMods[j].Priority {
			return enabledMods[i].Priority > enabledMods[j].Priority
		}

		return enabledMods[i].ID < enabledMods[j].ID
	})

	for _, mod := range enabledMods {
		if explicitSet[mod.ID] {
			continue
		}

		if mod.InstallMode == domain.InstallModeGameRoot {
			continue
		}

		if mod.ModFolderName == "" {
			continue
		}

		lines = append(lines, mod.ModFolderName)
	}

	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}

	loadOrderPath := filepath.Join(modsFolder, "mod_load_order.txt")

	return platform.AtomicWriteFile(loadOrderPath, []byte(content), 0o644)
}

// runHooks runs the scripts for a phase and treats any failure as fatal.
func (s *ModService) runHooks(ctx context.Context, phase domain.HookPhase, mod *domain.Mod) error {
	if s.deps.Hooks == nil || mod == nil {
		return nil
	}

	scripts := mod.Hooks.For(phase)
	if len(scripts) == 0 {
		return nil
	}

	if err := s.deps.Hooks.ExecutePhase(ctx, phase, mod.Hooks, mod); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"%s hook of mod %q failed", phase, modLabel(mod))
	}

	return nil
}

// runHooksAdvisory runs a Post* phase and reports failure as an event instead of
// returning it. See the package comment for why.
func (s *ModService) runHooksAdvisory(
	ctx context.Context,
	phase domain.HookPhase,
	mod *domain.Mod,
) {
	if s.deps.Hooks == nil || mod == nil {
		return
	}

	if len(mod.Hooks.For(phase)) == 0 {
		return
	}

	if err := s.deps.Hooks.ExecutePhase(ctx, phase, mod.Hooks, mod); err != nil {
		// Warn, not error: the operation itself succeeded, and the user is told
		// about this through the event. A log line saying "failed" next to an
		// install that worked would send them looking in the wrong place.
		s.log.Warn().
			Err(err).
			Str("op", "hook").
			Str("phase", string(phase)).
			Str("mod", mod.ID).
			Msg("advisory hook phase failed")
		s.publish(Event{
			Type: EventLog, ModID: mod.ID, SourceID: mod.SourceID,
			Data: fmt.Sprintf("%s hook of mod %q failed: %v", phase, modLabel(mod), err),
		})
	}
}

// validateHook checks one hook script before it is recorded, so a typo is
// reported at edit time instead of at the next enable.
//
// position is the hook's 1-based index within its phase, because that is the
// numbering the user editing the mod sees.
func (s *ModService) validateHook(
	script domain.HookScript,
	phase domain.HookPhase,
	position int,
) error {
	if !script.Type.Valid() {
		return appErrors.New(appErrors.ErrCodeHookInvalid,
			"%s hook %d has unknown type %q", phase, position, script.Type)
	}

	if strings.TrimSpace(script.Content) == "" {
		return appErrors.New(
			appErrors.ErrCodeHookInvalid,
			"%s hook %d has no script body",
			phase,
			position,
		)
	}

	if script.Timeout < 0 {
		return appErrors.New(appErrors.ErrCodeHookInvalid,
			"%s hook %d has a negative timeout (%d)", phase, position, script.Timeout)
	}

	for _, entry := range script.Env {
		if !strings.Contains(entry, "=") ||
			strings.TrimSpace(strings.SplitN(entry, "=", 2)[0]) == "" {
			return appErrors.New(appErrors.ErrCodeHookInvalid,
				"%s hook %d has a malformed environment entry %q", phase, position, entry)
		}
	}

	return nil
}

// precheck normalises a nil context and rejects a cancelled one before any
// work starts, so a cancelled caller never leaves a half-applied change.
func (s *ModService) precheck(ctx context.Context) error {
	if ctx == nil {
		return nil
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	return nil
}

// publish forwards an event, tolerating a nil sink.
func (s *ModService) publish(e Event) {
	if s.deps.Events == nil {
		return
	}

	s.deps.Events.Publish(e)
}

// opTable serialises operations per mod.
//
// Two enables of the same mod racing would each deploy and each write the row;
// two operations on *different* mods must not block each other, because the
// common case is a bulk toggle. So the lock is per id, and a waiter can give up
// on a cancelled context.
// opTable serialises operations per mod id.
//
// A per-mod lock (rather than one service-wide mutex) matters because enabling
// two different mods is independent work, while enabling and uninstalling the
// same mod at the same time is not. Each lock is a channel holding a single
// token: taking the token acquires, returning it releases. A token channel
// (rather than a closed channel) is what makes "wait, unless the context is
// done" expressible without leaking a goroutine per waiter.
type opTable struct {
	locks map[string]*opLock
	mu    sync.Mutex
}

// opLock is one mod's token channel.
type opLock struct {
	ch chan struct{}
}

func newOpTable() *opTable { return &opTable{locks: make(map[string]*opLock)} }

// acquire blocks until the mod's lock is free, or until ctx is done. The
// returned function releases the lock and is safe to call more than once, so a
// deferred release next to an early return cannot corrupt the table.
//
// A nil context means "no way to give up", so it waits on the bare channel.
//
// Entries are never removed. A mod id that is touched once costs a map entry
// and a one-slot channel for the life of the process, and removing them would
// introduce a race: a waiter that already holds a pointer to the entry must
// still be woken by the releaser, which cannot know the entry has been
// unlinked.
func (t *opTable) acquire(ctx context.Context, id string) (func(), error) {
	t.mu.Lock()
	if t.locks == nil {
		t.locks = make(map[string]*opLock)
	}

	lock := t.locks[id]
	if lock == nil {
		lock = &opLock{ch: make(chan struct{}, 1)}
		lock.ch <- struct{}{}

		t.locks[id] = lock
	}
	t.mu.Unlock()

	if ctx == nil {
		<-lock.ch
		return t.releaseFunc(lock), nil
	}

	select {
	case <-lock.ch:
		return t.releaseFunc(lock), nil
	case <-ctx.Done():
		return nil, canceledError(ctx.Err())
	}
}

// releaseFunc builds the idempotent release closure handed to the holder. The
// token goes back into a channel of capacity one, so a release with no waiter
// simply leaves the lock free for the next acquirer.
func (t *opTable) releaseFunc(lock *opLock) func() {
	var once sync.Once

	return func() {
		once.Do(func() { lock.ch <- struct{}{} })
	}
}
