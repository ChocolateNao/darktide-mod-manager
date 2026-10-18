package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateScenario installs better_ui at an old version and then publishes 2.0.0
// from the same source, which is the shape every update test needs.
func updateScenario(t *testing.T, opts ...func(*harnessOpts)) *harness {
	t.Helper()
	h := newHarness(t, opts...)
	old := modFixture("better_ui", "1.0.0")
	old.SourceData = map[string]any{"download_url": "https://example.test/better_ui-1.0.0.zip"}
	h.withArchive(t, old)

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install the old version: %v", err)
	}

	fresh := old.Clone()
	fresh.Version = "2.0.0"
	fresh.SourceData = map[string]any{"download_url": "https://example.test/better_ui-2.0.0.zip"}
	h.withArchive(t, fresh)

	return h
}

func TestCheckModUpdatesReportsAVersionBump(t *testing.T) {
	h := updateScenario(t)

	infos, err := h.svc.CheckModUpdates(context.Background())
	require.NoError(t, err, "CheckModUpdates")

	require.Len(t, infos, 1, "got %d update records, want 1", len(infos))

	info := infos[0]
	assert.False(
		t,
		info.ModID != "better_ui" || info.LocalVersion != "1.0.0" || info.RemoteVersion != "2.0.0",
		"unexpected record: %+v",
		info,
	)

	assert.True(t, info.Available, "2.0.0 is newer than 1.0.0, so an update is available")

	assert.Equal(t, "vortex", info.SourceID, "SourceID = %q, want vortex", info.SourceID)

	{
		want := "https://example.test/better_ui-2.0.0.zip"
		assert.Equal(t, want, info.DownloadURL, "DownloadURL = %q, want %q", info.DownloadURL, want)
	}
}

func TestCheckModUpdatesWithNothingInstalled(t *testing.T) {
	h := newHarness(t)

	infos, err := h.svc.CheckModUpdates(context.Background())
	require.NoError(t, err, "CheckModUpdates")

	assert.Empty(t, infos, "got %d records, want none", len(infos))
}

func TestCheckModUpdatesIgnoresADowngrade(t *testing.T) {
	h := updateScenario(t)
	h.source.mods["better_ui"].Version = "0.5.0"

	infos, err := h.svc.CheckModUpdates(context.Background())
	require.NoError(t, err, "CheckModUpdates")

	assert.False(t, infos[0].Available, "an older remote version is not an update")

	assert.Equal(
		t,
		"0.5.0",
		infos[0].RemoteVersion,
		"the remote version should still be reported, got %q",
		infos[0].RemoteVersion,
	)
}

func TestCheckModUpdatesSurvivesAnUnreachableSource(t *testing.T) {
	h := updateScenario(t)
	h.source.failGet("better_ui", errBoom)

	infos, err := h.svc.CheckModUpdates(context.Background())
	require.NoError(t, err, "one unreachable source must not fail the sweep")

	require.Len(t, infos, 1, "got %d records, want 1", len(infos))

	assert.False(
		t,
		infos[0].RemoteVersion != "" || infos[0].Available,
		"an unreachable source means no update is known: %+v",
		infos[0],
	)

	assert.Equal(
		t,
		"1.0.0",
		infos[0].LocalVersion,
		"the local version is still known: %+v",
		infos[0],
	)
}

func TestCheckModUpdatesWithoutARegistry(t *testing.T) {
	h := updateScenario(t)

	svc, err := NewModService(ModServiceDeps{Index: h.index, Clock: modClock()})
	require.NoError(t, err, "build service")

	infos, err := svc.CheckModUpdates(context.Background())
	require.NoError(t, err, "a missing registry degrades to no update known")

	assert.Empty(t, infos[0].RemoteVersion, "got %+v, want no remote version", infos[0])
}

func TestCheckModUpdatesPropagatesIndexFailure(t *testing.T) {
	h := updateScenario(t)

	h.index.listErr = errBoom
	{
		_, err := h.svc.CheckModUpdates(context.Background())
		assert.Error(t, err, "a failing index must fail the sweep")
	}
}

func TestCheckModUpdatesCanceledContext(t *testing.T) {
	h := updateScenario(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		_, err := h.svc.CheckModUpdates(
			ctx,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "cancelled: %v, want OPERATION_CANCELED", err)
	}
}

func TestCheckModUpdatesNilServiceAndMissingIndex(t *testing.T) {
	ctx := context.Background()
	{
		_, err := (*ModService)(
			nil,
		).CheckModUpdates(
			ctx,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "nil service: %v, want INVALID_ARGUMENT", err)
	}

	{
		_, err := (&ModService{}).CheckModUpdates(
			ctx,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "no index: %v, want INVALID_ARGUMENT", err)
	}
}

func TestApplyUpdateReplacesThePayloadAndTheRecord(t *testing.T) {
	h := updateScenario(t)
	oldPath := h.installed(t, "better_ui").LocalPath
	installedAt := h.installed(t, "better_ui").InstalledAt

	updated, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
	require.NoError(t, err, "ApplyUpdate")

	assert.Equal(t, "2.0.0", updated.Version, "Version = %q, want 2.0.0", updated.Version)

	assert.True(t, updated.Enabled, "an update must not silently disable a mod")

	assert.Equal(
		t,
		h.payloadPath("better_ui", "2.0.0"),
		updated.LocalPath,
		"LocalPath = %q, want the new version folder",
		updated.LocalPath,
	)

	assert.True(
		t,
		updated.InstalledAt.Equal(installedAt),
		"InstalledAt moved from %v to %v; an update is not a fresh install",
		installedAt,
		updated.InstalledAt,
	)

	{
		_, err := os.Stat(updated.LocalPath)
		assert.NoError(t, err, "the new payload is missing: %v", err)
	}
	// Default retention keeps the old copy so a bad update can be rolled back.
	{
		_, err := os.Stat(oldPath)
		assert.NoError(t, err, "the default retention should keep the old payload: %v", err)
	}

	assert.False(t, !hasEvent(h.events.types(), EventModUpdateStarted) ||
		!hasEvent(
			h.events.types(),
			EventModUpdateCompleted,
		), "events = %v, want started and completed", h.events.types())
}

func TestApplyUpdateWithZeroRetentionRemovesTheOldPayload(t *testing.T) {
	h := updateScenario(t, func(o *harnessOpts) {
		cfg := domain.DefaultConfig()
		cfg.Storage.Path = "" // filled in by newHarness when unset
		cfg.Storage.BackupRetentionDays = 0
		o.config = newStaticConfig(&cfg)
	})
	// newHarness only substitutes the storage path for a nil config.
	svc, err := NewModService(ModServiceDeps{
		Sources: h.sources, Cache: h.cache, Index: h.index, Hooks: h.hooks,
		Locator: h.locator, Deployer: h.deployer, Extractor: storage.NewArchiveExtractor(),
		Config: h.config, Events: h.events, Clock: modClock(), StoragePath: h.storage,
	})
	require.NoError(t, err, "build service")

	oldPath := h.installed(t, "better_ui").LocalPath

	updated, err := svc.ApplyUpdate(context.Background(), "better_ui")
	require.NoError(t, err, "ApplyUpdate")

	{
		_, err := os.Stat(oldPath)
		assert.True(
			t,
			os.IsNotExist(err),
			"zero retention should remove %q, stat error = %v",
			oldPath,
			err,
		)
	}

	{
		_, err := os.Stat(updated.LocalPath)
		assert.NoError(t, err, "the new payload is missing: %v", err)
	}
}

func TestApplyUpdateWithNothingNewerReturnsTheInstalledMod(t *testing.T) {
	h := updateScenario(t)
	h.source.mods["better_ui"].Version = "1.0.0"
	before := len(h.events.types())

	got, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
	require.NoError(t, err, "ApplyUpdate")

	assert.Equal(t, "1.0.0", got.Version, "Version = %q, want the installed 1.0.0", got.Version)

	assert.Len(
		t,
		h.events.types(),
		before,
		"nothing was updated, so no update events should fire: %v",
		h.events.types()[before:],
	)
}

func TestApplyUpdateRejectsADowngrade(t *testing.T) {
	h := updateScenario(t)
	h.source.mods["better_ui"].Version = "0.1.0"

	got, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
	require.NoError(t, err, "ApplyUpdate")

	assert.Equal(
		t,
		"1.0.0",
		got.Version,
		"Version = %q; a downgrade must not be applied",
		got.Version,
	)
}

func TestApplyUpdateUnknownMod(t *testing.T) {
	h := updateScenario(t)
	{
		_, err := h.svc.ApplyUpdate(
			context.Background(),
			"ghost",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "unknown mod: %v, want MOD_NOT_FOUND", err)
	}
}

func TestApplyUpdateBlankID(t *testing.T) {
	h := updateScenario(t)
	{
		_, err := h.svc.ApplyUpdate(
			context.Background(),
			"   ",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "blank id: %v, want INVALID_ARGUMENT", err)
	}
}

func TestApplyUpdateModWithoutASource(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "orphan", Version: "1.0.0", Enabled: true})

	_, err := h.svc.ApplyUpdate(context.Background(), "orphan")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod),
		"a mod with no source cannot be updated: %v",
		err,
	)
}

func TestApplyUpdateKeepsADisabledModDisabled(t *testing.T) {
	h := updateScenario(t)
	{
		err := h.svc.DisableMod(context.Background(), "better_ui")
		require.NoError(t, err, "DisableMod: %v", err)
	}

	updated, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
	require.NoError(t, err, "ApplyUpdate")

	assert.False(t, updated.Enabled, "an update must not enable a mod the user disabled")

	assert.Len(
		t,
		h.deployer.deployTargets(),
		1,
		"exactly the original install should have deployed, got %v",
		h.deployer.deployTargets(),
	)
}

func TestApplyUpdateFailsWithoutTouchingTheOldPayload(t *testing.T) {
	h := updateScenario(t)
	oldPath := h.installed(t, "better_ui").LocalPath
	old := h.installed(t, "better_ui")

	h.cache.getErr = errBoom
	{
		_, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
		require.Error(t, err, "a download failure must fail the update")
	}

	{
		_, err := os.Stat(oldPath)
		assert.NoError(t, err, "the old payload must survive a failed update: %v", err)
	}

	now := h.installed(t, "better_ui")
	assert.False(
		t,
		now.Version != old.Version || now.LocalPath != old.LocalPath,
		"the record changed on a failed update: %+v -> %+v",
		old,
		now,
	)
}

func TestApplyUpdateRejectsASecondConcurrentUpdate(t *testing.T) {
	// The first update is held inside its own PreInstall hook, well after
	// beginUpdate registered it, so the second call is unambiguously concurrent.
	h := updateScenario(t)
	release := make(chan struct{})
	entered := make(chan struct{})

	var once sync.Once

	h.hooks.block = release
	h.hooks.onEnter = func(domain.HookPhase) { once.Do(func() { close(entered) }) }

	done := make(chan error, 1)
	go func() { done <- h.svc.UpdateMod(context.Background(), "better_ui") }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		require.FailNow(t, "the first update never reached its hooks")
	}

	secondErr := h.svc.UpdateMod(context.Background(), "better_ui")
	assert.True(
		t,
		appErrors.IsAppError(secondErr, appErrors.ErrCodeInvalidArgument),
		"a concurrent update should be refused, got %v",
		secondErr,
	)

	close(release)

	select {
	case err := <-done:
		assert.NoError(t, err, "the first update should succeed")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the first update never returned")
	}
	// And the registration is free again afterwards.
	h.hooks.block = nil
	{
		err := h.svc.UpdateMod(context.Background(), "better_ui")
		assert.NoError(t, err, "after the first update finished, another should run: %v", err)
	}
}

func TestApplyUpdateCanBeRerunAfterAFailure(t *testing.T) {
	h := updateScenario(t)
	h.source.failGet("better_ui", errBoom)

	{
		_, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
		require.Error(t, err, "setup: the first update should fail")
	}

	h.source.failGet("better_ui", nil)

	{
		_, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
		assert.NoError(t, err, "the failed update must not stay registered: %v", err)
	}
}

func TestUpdateModIsApplyUpdateWithoutTheResult(t *testing.T) {
	h := updateScenario(t)
	{
		err := h.svc.UpdateMod(context.Background(), "better_ui")
		require.NoError(t, err, "UpdateMod: %v", err)
	}

	{
		got := h.installed(t, "better_ui")
		assert.Equal(t, "2.0.0", got.Version, "Version = %q, want 2.0.0", got.Version)
	}

	{
		err := h.svc.UpdateMod(
			context.Background(),
			"ghost",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "UpdateMod should surface the same error: %v", err)
	}
}

func TestApplyUpdateCanceledContext(t *testing.T) {
	h := updateScenario(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		_, err := h.svc.ApplyUpdate(
			ctx,
			"better_ui",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "cancelled: %v, want OPERATION_CANCELED", err)
	}
}

func TestApplyUpdateNilContext(t *testing.T) {
	h := updateScenario(t)
	//nolint:staticcheck // SA1012: tolerating a nil context is the contract under test.
	{
		_, err := h.svc.ApplyUpdate(nil, "better_ui")
		assert.NoError(t, err, "a nil context should be tolerated: %v", err)
	}
}

func TestApplyUpdateNilServiceAndMissingIndex(t *testing.T) {
	ctx := context.Background()
	{
		_, err := (*ModService)(
			nil,
		).ApplyUpdate(
			ctx,
			"x",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "nil service: %v, want INVALID_ARGUMENT", err)
	}

	{
		_, err := (&ModService{}).ApplyUpdate(
			ctx,
			"x",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "no index: %v, want INVALID_ARGUMENT", err)
	}
}

func TestApplyUpdateWhenTheSourceDroppedTheMod(t *testing.T) {
	h := updateScenario(t)
	h.source.failGet("better_ui", appErrors.New(appErrors.ErrCodeModNotFound, "gone"))

	_, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
		"a mod the source no longer has: %v, want MOD_NOT_FOUND",
		err,
	)
}

func TestKeepOldVersionsFollowsRetention(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.Storage.BackupRetentionDays = 7

	keep := &ModService{deps: ModServiceDeps{Config: newStaticConfig(&cfg)}}
	assert.True(
		t,
		keep.keepOldVersions(context.Background()),
		"a positive retention keeps old payloads",
	)

	cfg.Storage.BackupRetentionDays = 0

	drop := &ModService{deps: ModServiceDeps{Config: newStaticConfig(&cfg)}}
	assert.False(
		t,
		drop.keepOldVersions(context.Background()),
		"zero retention means do not keep old payloads",
	)

	bare := &ModService{}
	assert.True(
		t,
		bare.keepOldVersions(context.Background()),
		"with no config there is nothing saying to delete, so keep",
	)

	brokenStore := newStaticConfig(nil)
	brokenStore.loadErr = errBoom

	broken := &ModService{deps: ModServiceDeps{Config: brokenStore}}
	assert.True(
		t,
		broken.keepOldVersions(context.Background()),
		"an unreadable config must not become a delete",
	)
}

func TestCancelUpdateStopsAnUpdateInFlight(t *testing.T) {
	// The hooks block, where an update spends most of its wall clock.
	h := updateScenario(t)
	release := make(chan struct{})
	h.hooks.block = release
	entered := make(chan struct{})

	var once sync.Once

	h.hooks.onEnter = func(domain.HookPhase) {
		once.Do(func() { close(entered) })
	}

	done := make(chan error, 1)

	go func() {
		_, err := h.svc.ApplyUpdate(context.Background(), "better_ui")
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		require.FailNow(t, "the update never reached its first hook")
	}

	{
		err := h.svc.CancelUpdate(context.Background(), "better_ui")
		require.NoError(t, err, "CancelUpdate: %v", err)
	}

	select {
	case err := <-done:
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"a cancelled update should report OPERATION_CANCELED, got %v",
			err,
		)
	case <-time.After(5 * time.Second):
		close(release)
		require.FailNow(t, "CancelUpdate did not stop the update")
	}

	close(release)
	// A cancelled update changes nothing.
	{
		_, err := os.Stat(h.payloadPath("better_ui", "1.0.0"))
		assert.NoError(t, err, "the installed payload must survive a cancelled update: %v", err)
	}

	{
		got := h.installed(t, "better_ui")
		assert.Equal(t, "1.0.0", got.Version, "Version = %q, want the installed 1.0.0", got.Version)
	}
}

func TestCancelUpdateBeforeTheContextIsWired(t *testing.T) {
	// The window between beginUpdate and updateContext: a cancel landing there
	// must not be dropped, or a user pressing stop would see nothing happen.
	h := updateScenario(t)
	{
		err := h.svc.beginUpdate("better_ui")
		require.NoError(t, err, "beginUpdate: %v", err)
	}

	{
		err := h.svc.CancelUpdate(context.Background(), "better_ui")
		require.NoError(t, err, "CancelUpdate: %v", err)
	}

	ctx := h.svc.updateContext(context.Background(), "better_ui")
	assert.NotNil(t, ctx.Err(), "the cancel that arrived early should already have taken effect")

	h.svc.endUpdate("better_ui")
}

func TestCancelUpdateWhenNothingIsRunning(t *testing.T) {
	h := updateScenario(t)
	{
		err := h.svc.CancelUpdate(context.Background(), "better_ui")
		assert.NoError(
			t,
			err,
			"there is nothing to stop, which is what the caller asked for: %v",
			err,
		)
	}
}

func TestCancelUpdateBlankIDAndNilService(t *testing.T) {
	h := updateScenario(t)
	{
		err := h.svc.CancelUpdate(
			context.Background(),
			"  ",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "blank id: %v, want INVALID_ARGUMENT", err)
	}

	{
		err := (*ModService)(
			nil,
		).CancelUpdate(
			context.Background(),
			"x",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "nil service: %v, want INVALID_ARGUMENT", err)
	}
}

func TestCancelUpdateCanceledContext(t *testing.T) {
	h := updateScenario(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		err := h.svc.CancelUpdate(
			ctx,
			"better_ui",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "cancelled: %v, want OPERATION_CANCELED", err)
	}
}

func TestEndUpdateClearsTheRegistration(t *testing.T) {
	svc := &ModService{active: map[string]*updateRun{}}
	{
		err := svc.beginUpdate("m")
		require.NoError(t, err, "beginUpdate: %v", err)
	}

	{
		err := svc.beginUpdate("m")
		assert.Error(t, err, "a second beginUpdate for the same mod should be refused")
	}

	svc.endUpdate("m")

	{
		err := svc.beginUpdate("m")
		assert.NoError(t, err, "endUpdate should have cleared the registration: %v", err)
	}
	// endUpdate on an unknown id is harmless, and on a run whose context was
	// never wired it must not panic on a nil cancel.
	svc.endUpdate("never-started")
	svc.endUpdate("m")
}

func TestUpdateContextWithoutARegisteredRun(t *testing.T) {
	svc := &ModService{active: map[string]*updateRun{}}

	got := svc.updateContext(context.TODO(), "ghost")
	require.NotNil(t, got, "updateContext must always hand back a usable context")

	assert.Nil(t, got.Err(), "a context nobody registered should be untouched: %v", got.Err())
}

func TestTopLevelNames(t *testing.T) {
	files := []domain.ModFile{
		{RelPath: "better_ui.dll"},
		{RelPath: `textures\hud.dds`},
		{RelPath: "textures/logo.dds"},
		{RelPath: "readme.txt"},
		{RelPath: ""},
		{RelPath: "."},
		{RelPath: ".."},
		{RelPath: "better_ui.dll"},
	}
	got := topLevelNames(files)

	want := []string{"better_ui.dll", "textures", "readme.txt"}
	require.Len(t, got, len(want), "got %v, want %v", got, want)

	for i := range want {
		require.Equal(t, want[i], got[i], "got %v, want %v", got, want)
	}

	{
		got := topLevelNames(nil)
		assert.Empty(t, got, "no files: %v, want none", got)
	}
}

func TestOwnedEntriesNarrowsToWhatAModOwns(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "m", Version: "1.0.0", Enabled: true})
	h.index.setFiles("m", []domain.ModFile{
		{RelPath: "a.dll"},
		{RelPath: "textures/hud.dds"},
	})

	all := map[string]platform.LinkState{
		"a.dll":       platform.LinkOK,
		"textures":    platform.LinkBroken,
		"mod.toml":    platform.LinkNotAManagedLink,
		"someone.dll": platform.LinkBroken,
	}

	got := h.svc.ownedEntries(context.Background(), domain.Mod{ID: "m"}, all)
	assert.Len(t, got, 2, "got %v, want only a.dll and textures", got)

	{
		_, ok := got["someone.dll"]
		assert.False(t, ok, "another mod's entry must not be reported as ours")
	}

	{
		_, ok := got["mod.toml"]
		assert.False(t, ok, "a manifest is never a deployed entry")
	}

	assert.Equal(
		t,
		platform.LinkBroken,
		got["textures"],
		"the reported state should be carried through, got %q",
		got["textures"],
	)

	// A mod whose inventory cannot be read owns nothing, rather than everything.
	h.index.filesErr = errBoom
	{
		got := h.svc.ownedEntries(context.Background(), domain.Mod{ID: "m"}, all)
		assert.Nil(t, got, "an unreadable inventory should own nothing, got %v", got)
	}

	// Nothing of ours is present in the folder.
	empty := h.svc.ownedEntries(context.Background(), domain.Mod{ID: "m"},
		map[string]platform.LinkState{"someone.dll": platform.LinkOK})
	assert.Nil(t, empty, "got %v, want nil when none of our entries are there", empty)
}

func TestModPathEdgeCases(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.0.0")
	mod.SourceData = map[string]any{"download_url": "https://example.test/x.zip"}
	h.withArchive(t, mod)

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	got, err := h.svc.ModPath(context.Background(), "better_ui")
	require.NoError(t, err, "ModPath")

	assert.Equal(t, h.payloadPath("better_ui", "1.0.0"), got, "ModPath = %q", got)

	{
		_, err := h.svc.ModPath(context.Background(), "  better_ui  ")
		assert.NoError(t, err, "ModPath should trim the id: %v", err)
	}

	{
		_, err := h.svc.ModPath(
			context.Background(),
			"  ",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "blank id: %v, want INVALID_ARGUMENT", err)
	}

	{
		_, err := h.svc.ModPath(
			context.Background(),
			"ghost",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "unknown mod: %v, want MOD_NOT_FOUND", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		_, err := h.svc.ModPath(
			ctx,
			"better_ui",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "cancelled: %v, want OPERATION_CANCELED", err)
	}
}

func TestValidateHook(t *testing.T) {
	svc := &ModService{}

	good := domain.HookScript{Language: domain.LangJS, Content: "print(1)"}
	{
		err := svc.validateHook(good, domain.PhasePreInstall, 0)
		assert.NoError(t, err, "a plain inline hook should be valid: %v", err)
	}

	cases := []struct {
		name string
		hook domain.HookScript
	}{
		{"unknown type", domain.HookScript{Language: "powershell", Content: "x"}},
		{"blank type", domain.HookScript{Content: "x"}},
		{"blank content", domain.HookScript{Language: domain.LangJS}},
		{"whitespace content", domain.HookScript{Language: domain.LangJS, Content: "  \t\n"}},
		{
			"negative timeout",
			domain.HookScript{Language: domain.LangJS, Content: "x", Timeout: -1},
		},
		{
			"env without a separator",
			domain.HookScript{Language: domain.LangJS, Content: "x", Env: []string{"TOKEN"}},
		},
		{
			"env with a blank key",
			domain.HookScript{Language: domain.LangJS, Content: "x", Env: []string{"=secret"}},
		},
	}
	for _, tc := range cases {
		{
			err := svc.validateHook(
				tc.hook,
				domain.PhasePreEnable,
				2,
			)
			assert.True(t, appErrors.IsAppError(
				err,
				appErrors.ErrCodeHookInvalid,
			), "%s: %v, want HOOK_INVALID", tc.name, err)
		}

		{
			err := svc.validateHook(tc.hook, domain.PhasePreEnable, 2)
			assert.False(t, err != nil &&
				!strings.Contains(
					err.Error(),
					"pre_enable hook 2",
				), "%s: the message should name the phase and position, got %q", tc.name, err)
		}
	}

	withEnv := domain.HookScript{
		Language: domain.LangJS,
		Content:  "x",
		Env:      []string{"TOKEN=a=b"},
	}
	{
		err := svc.validateHook(withEnv, domain.PhasePostInstall, 1)
		assert.NoError(t, err, "a value containing '=' is fine: %v", err)
	}
}

func TestOpTableSerialisesPerMod(t *testing.T) {
	var table opTable

	release, err := table.acquire(context.Background(), "m")
	require.NoError(t, err, "first acquire")

	acquired := make(chan struct{})

	go func() {
		{
			_, err := table.acquire(context.Background(), "m")
			assert.NoError(t, err, "second acquire: %v", err)
		}

		close(acquired)
		// The second holder must release, or the lock leaks.
		{
			_, err := table.acquire(context.Background(), "m")
			assert.NoError(t, err, "third acquire: %v", err)
		}
	}()

	select {
	case <-acquired:
		require.FailNow(t, "the second acquire returned while the first was still held")
	case <-time.After(50 * time.Millisecond):
	}

	release()

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "releasing did not hand the lock over")
	}
	// A different mod is never blocked by this one.
	other, err := table.acquire(context.Background(), "other")
	assert.NoError(t, err, "a different mod must not wait")

	other()
}

func TestOpTableAcquireRespectsCancellation(t *testing.T) {
	var table opTable

	release, err := table.acquire(context.Background(), "m")
	require.NoError(t, err, "first acquire")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	{
		_, err := table.acquire(
			ctx,
			"m",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "waiting past the deadline: %v, want OPERATION_CANCELED", err)
	}
	// The abandoned waiter must not have left the lock held.
	release()

	again, err := table.acquire(context.Background(), "m")
	require.NoError(t, err, "the lock should be free")

	again()
}

func TestOpTableReleaseIsIdempotent(t *testing.T) {
	var table opTable

	release, err := table.acquire(context.Background(), "m")
	require.NoError(t, err, "acquire")

	release()
	release()
	release()
	// Two holders' releases were called twice each; the lock must still work.
	next, err := table.acquire(context.Background(), "m")
	require.NoError(t, err, "acquire after a double release")

	next()
}

func TestCacheKeyIsDerivedFromLocationAndVersion(t *testing.T) {
	a := &domain.Mod{
		ID:         "x",
		Version:    "1.0.0",
		SourceData: map[string]any{"download_url": "https://h/u"},
	}
	b := &domain.Mod{
		ID:         "x",
		Version:    "2.0.0",
		SourceData: map[string]any{"download_url": "https://h/u"},
	}

	keyA, err := CacheKey(a)
	require.NoError(t, err, "CacheKey")

	keyB, err := CacheKey(b)
	require.NoError(t, err, "CacheKey")

	assert.NotEqual(
		t,
		keyB,
		keyA,
		"the version is part of the key, so two versions must not share a blob",
	)

	same, err := CacheKey(a.Clone())
	require.NoError(t, err, "CacheKey")

	assert.Equal(t, keyA, same, "the same mod must always produce the same key")
}

func TestCacheKeyForRecomputesFromTheRecord(t *testing.T) {
	h := updateScenario(t)
	mod := h.installed(t, "better_ui")

	want, err := CacheKey(mod)
	require.NoError(t, err, "CacheKey")

	{
		got := cacheKeyFor(mod)
		assert.Equal(t, want, got, "cacheKeyFor = %q, want %q", got, want)
	}

	bare := cacheKeyFor(&domain.Mod{ID: "x"})
	assert.Empty(t, bare, "a record with no download location has no key, got %q", bare)
}

func TestDownloadLocationAcceptsTheUsualSpellings(t *testing.T) {
	for _, key := range []string{"download_url", "downloadUrl", "file_url", "url", "download_id", "file_id", "local_path"} {
		mod := &domain.Mod{SourceData: map[string]any{key: "value"}}
		{
			got := DownloadLocation(mod)
			assert.Equal(t, "value", got, "key %q: got %q", key, got)
		}
	}

	mod := &domain.Mod{SourceData: map[string]any{
		"download_url": "   ",
		"url":          42,
		"file_id":      "fallback",
	}}
	{
		got := DownloadLocation(mod)
		assert.Equal(
			t,
			"fallback",
			got,
			"blank then non-string entries should be skipped, got %q",
			got,
		)
	}

	{
		got := DownloadLocation(&domain.Mod{})
		assert.Empty(t, got, "no source data: %q, want empty", got)
	}

	{
		got := DownloadLocation(nil)
		assert.Empty(t, got, "nil mod: %q, want empty", got)
	}
}

// A source that publishes a URL is cached by that URL; the bundled local source,
// which has none, is cached by where its payload lives — or the mod service cannot
// derive a cache key for its mods and cannot install them.
func TestDownloadLocationPrefersAURLOverTheLocalPath(t *testing.T) {
	mod := &domain.Mod{SourceData: map[string]any{
		"download_url": "https://example.invalid/better_ui.zip",
		"local_path":   "/home/modder/mods/better_ui",
	}}
	{
		got := DownloadLocation(mod)
		assert.Equal(
			t,
			"https://example.invalid/better_ui.zip",
			got,
			"location = %q, want the URL to win over the local path",
			got,
		)
	}
}

func TestPrecheckAcceptsNilContextAndRefusesACanceledOne(t *testing.T) {
	svc := &ModService{}
	//nolint:staticcheck // SA1012: tolerating a nil context is the contract.
	{
		err := svc.precheck(nil)
		assert.NoError(t, err, "a nil context should be tolerated: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		err := svc.precheck(ctx)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"cancelled: %v, want OPERATION_CANCELED",
			err,
		)
	}
}

func TestModLabelNeverDereferencesNil(t *testing.T) {
	{
		got := modLabel(nil)
		assert.Contains(t, got, "unknown", "nil mod: %q", got)
	}

	{
		got := modLabel(&domain.Mod{})
		assert.Contains(t, got, "unknown", "blank id: %q", got)
	}

	{
		got := modLabel(&domain.Mod{ID: "better_ui"})
		assert.Contains(t, got, "better_ui", "named mod: %q", got)
	}
}

func TestStagingDirectoriesAreCleanedUp(t *testing.T) {
	h := updateScenario(t)
	// Point the storage at a folder we can inspect for leftovers.
	entries, err := os.ReadDir(h.storage)
	require.NoError(t, err, "read storage")

	for _, e := range entries {
		assert.False(
			t,
			strings.HasPrefix(e.Name(), ".staging-"),
			"a staging directory survived: %q",
			e.Name(),
		)
	}

	if _, err := filepath.Glob(filepath.Join(h.storage, "*", ".staging-*")); err == nil {
		{
			matches, _ := filepath.Glob(
				filepath.Join(h.storage, "*", ".staging-*"),
			)
			assert.Empty(t, matches, "nested staging directories survived: %v", matches)
		}
	}
}
