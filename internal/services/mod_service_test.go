package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewModServiceRequiresIndex(t *testing.T) {
	_, err := NewModService(ModServiceDeps{})
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"a service with no index must be refused, got %v",
		err,
	)
	// A nil index is a wiring bug, not a runtime condition, so it is caught at
	// construction, not on the first ListMods.
	assert.False(
		t,
		err == nil || !strings.Contains(err.Error(), "index"),
		"the error should name the missing dependency, got %q",
		err,
	)
}

func TestNewModServiceDefaultsPlatformAndClock(t *testing.T) {
	svc, err := NewModService(ModServiceDeps{Index: newFakeIndex()})
	require.NoError(t, err, "a service with only an index must build")

	assert.Equal(
		t,
		domain.CurrentPlatform(),
		svc.deps.Platform,
		"platform defaulted to %q, want %q",
		svc.deps.Platform,
		domain.CurrentPlatform(),
	)

	assert.NotNil(t, svc.now, "clock was left nil; every timestamp write would panic")
}

func TestListModsReturnsEveryStoredMod(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "a", Name: "A", Version: "1.0.0"})
	h.index.put(&domain.Mod{ID: "b", Name: "B", Version: "2.0.0", Enabled: true})

	mods, err := h.svc.ListMods(context.Background())
	require.NoError(t, err, "list")

	require.Len(t, mods, 2, "got %d mods, want 2", len(mods))

	assert.False(
		t,
		mods[0].ID != "a" || mods[1].ID != "b",
		"unexpected order: %s, %s",
		mods[0].ID,
		mods[1].ID,
	)
}

func TestListModsEmptyIsNotNil(t *testing.T) {
	h := newHarness(t)

	mods, err := h.svc.ListMods(context.Background())
	require.NoError(t, err, "list")

	assert.NotNil(
		t,
		mods,
		"an empty library must be an empty slice, not nil; the UI would render \"null\"",
	)
}

func TestListModsPropagatesIndexFailure(t *testing.T) {
	h := newHarness(t)

	h.index.listErr = errBoom
	{
		_, err := h.svc.ListMods(context.Background())
		require.True(t, errors.Is(err, errBoom), "index failure must surface, got %v", err)
	}
}

func TestGetModUnknownIsModNotFound(t *testing.T) {
	h := newHarness(t)

	_, err := h.svc.GetMod(context.Background(), "nope")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
		"unknown mod must be MOD_NOT_FOUND, got %v",
		err,
	)
}

func TestGetModRejectsBlankIDWithoutTouchingTheIndex(t *testing.T) {
	h := newHarness(t)

	h.index.getErr = errBoom
	{
		_, err := h.svc.GetMod(context.Background(), "   ")
		require.Error(t, err, "a blank id must be refused before the index is consulted")
	}

	if !hasEvent(h.events.types(), EventLog) {
		t.Log("a blank id is a caller mistake and needs no event")
	}
}

func TestInstallModEndToEnd(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod, "better_ui.dll", "textures/hud.dds", "mod.toml")

	installed, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
	require.NoError(t, err, "install")

	assert.False(
		t,
		installed.ID != "better_ui" || installed.Version != "1.2.0",
		"returned the wrong mod: %+v",
		installed,
	)

	assert.True(t, installed.Enabled, "a mod installed with a game present should be enabled")

	// The payload lives under manager storage, keyed by id and version.
	want := h.payloadPath("better_ui", "1.2.0")
	assert.Equal(t, want, installed.LocalPath, "LocalPath = %q, want %q", installed.LocalPath, want)
	// The manifest is kept on disk (it is manager metadata) but must not be
	// recorded, because the deployer never exposed it.
	got := h.payloadFiles("better_ui", "1.2.0")

	want2 := []string{"better_ui.dll", "mod.toml", "textures/hud.dds"}
	assert.True(t, equalStrings(got, want2), "payload holds %v, want %v", got, want2)

	files, err := h.index.GetFilesByMod(context.Background(), "better_ui")
	require.NoError(t, err, "read inventory")

	require.Len(t, files, 2, "recorded %d files, want 2: %+v", len(files), files)

	for _, f := range files {
		assert.Equal(t, "better_ui", f.ModID, "file %q has ModID %q", f.RelPath, f.ModID)

		assert.NotEmpty(t, f.StoredPath, "file %q has no StoredPath", f.RelPath)

		assert.NotEmpty(t, f.Checksum, "file %q has no checksum", f.RelPath)
	}

	// Auto-enable inserts its own pair between the install pair.
	phases := h.hooks.executed()

	wantPhases := []domain.HookPhase{
		domain.PhasePreInstall,
		domain.PhasePreEnable,
		domain.PhasePostEnable,
		domain.PhasePostInstall,
	}
	require.Len(t, phases, len(wantPhases), "hook order = %v, want %v", phases, wantPhases)

	for i := range wantPhases {
		require.Equal(t, wantPhases[i], phases[i], "hook order = %v, want %v", phases, wantPhases)
	}

	types := h.events.types()
	assert.Equal(
		t,
		EventModInstallStarted,
		types[0],
		"first event = %q, want install started",
		types[0],
	)

	assert.Equal(
		t,
		EventModInstallCompleted,
		types[len(types)-1],
		"last event = %q, want install completed",
		types[len(types)-1],
	)

	assert.Equal(
		t,
		1,
		h.events.countOf(EventModEnabled),
		"expected one enabled event, got %v",
		types,
	)

	// Installing also deploys, because a game mods folder resolved.
	{
		got := h.deployer.deployedSources()
		assert.Len(
			t,
			got,
			2,
			"expected the payload's two top-level entries in the game folder, got %v",
			got,
		)
	}

	{
		got := h.deployer.deployTargets()
		assert.Len(t, got, 1, "expected the payload to be deployed once, got %v", got)
	}
}

func TestInstallWithoutGameLeavesModDisabled(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) { o.locator = withoutGame() })
	mod := modFixture("lonely", "1.0.0")
	h.withArchive(t, mod)

	installed, err := h.svc.InstallMod(context.Background(), "vortex", "lonely")
	require.NoError(t, err, "install")

	assert.False(
		t,
		installed.Enabled,
		"with no game there is nothing to enable for; the mod must stay disabled",
	)

	assert.Empty(
		t,
		h.deployer.deployTargets(),
		"nothing should be deployed without a game, got %v",
		h.deployer.deployTargets(),
	)

	assert.Equal(
		t,
		0,
		h.events.countOf(EventModEnabled),
		"no enabled event should be published when nothing was enabled",
	)
}

func TestInstallRejectsBlankArguments(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct{ source, id string }{
		{"", "better_ui"},
		{"   ", "better_ui"},
		{"vortex", ""},
		{"vortex", "  "},
	} {
		{
			_, err := h.svc.Install(
				context.Background(),
				tc.source,
				tc.id,
				InstallOptions{},
			)
			assert.Error(t, err, "source=%q id=%q must be refused", tc.source, tc.id)
		}
	}

	{
		got := h.sources.requested()
		assert.Empty(t, got, "a blank argument must not reach the source registry, got %v", got)
	}
}

func TestInstallUnknownSourceIsSourceNotFound(t *testing.T) {
	h := newHarness(t)

	_, err := h.svc.InstallMod(context.Background(), "nope", "better_ui")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceNotFound),
		"want SOURCE_NOT_FOUND, got %v",
		err,
	)
}

func TestInstallModWithoutRemoteIDIsInvalidMod(t *testing.T) {
	h := newHarness(t)
	// Reachable as "placeholder" but with an empty id: a source that loses the
	// id would otherwise create a mod row keyed by "".
	h.withArchive(
		t,
		&domain.Mod{ID: "", Version: "1.0.0", SourceData: map[string]any{"download_url": "x"}},
	)
	h.source.mods["placeholder"] = &domain.Mod{
		ID:         "",
		Version:    "1.0.0",
		SourceData: map[string]any{"download_url": "x"},
	}

	_, err := h.svc.Install(context.Background(), "vortex", "placeholder", InstallOptions{})
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod),
		"want INVALID_MOD, got %v",
		err,
	)
}

func TestInstallRejectsDuplicateWithoutForce(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "first install: %v", err)
	}

	_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModAlreadyExists),
		"want MOD_ALREADY_EXISTS, got %v",
		err,
	)
	// The source is queried before the duplicate check: the installed id comes
	// from the source, so there is nothing to check until it has answered.
	{
		got := h.sources.requested()
		assert.Len(t, got, 2, "expected the source to be consulted once per attempt, got %v", got)
	}
}

func TestInstallForceReplacesAndKeepsInstalledAt(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "first install: %v", err)
	}

	first := h.installed(t, "better_ui")

	// A reinstall must not reset the age of the mod.
	h.source.mods["better_ui"] = modFixture("better_ui", "1.3.0")
	h.cache.archives["better_ui"] = writeZipArchive(
		t,
		filepath.Join(h.archives, "better_ui-130.zip"),
		"better_ui.dll",
	)

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"better_ui",
			InstallOptions{Force: true},
		)
		require.NoError(t, err, "forced reinstall: %v", err)
	}

	second := h.installed(t, "better_ui")
	assert.Equal(t, "1.3.0", second.Version, "version = %q, want 1.3.0", second.Version)

	assert.True(
		t,
		second.InstalledAt.Equal(first.InstalledAt),
		"InstalledAt moved from %v to %v",
		first.InstalledAt,
		second.InstalledAt,
	)

	assert.NotEqual(
		t,
		first.LocalPath,
		second.LocalPath,
		"a new version must get its own payload folder",
	)

	{
		_, err := os.Stat(first.LocalPath)
		assert.NoError(t, err, "the old payload folder should still be there for rollback: %v", err)
	}
}

func TestInstallHonoursExplicitEnableChoice(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("quiet", "1.0.0")
	h.withArchive(t, mod)

	no := false
	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"quiet",
			InstallOptions{Enable: &no},
		)
		require.NoError(t, err, "install: %v", err)
	}

	{
		got := h.installed(t, "quiet")
		assert.False(t, got.Enabled, "Enable: &false was ignored")
	}

	assert.Empty(
		t,
		h.deployer.deployTargets(),
		"Enable: &false must not deploy, got %v",
		h.deployer.deployTargets(),
	)
}

func TestInstallPreHookFailureAbortsBeforeDownload(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)
	h.hooks.setFail(
		domain.PhasePreInstall,
		appErrors.New(appErrors.ErrCodeHookFailed, "pre install said no"),
	)

	_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeHookFailed),
		"a failing pre-install hook must abort the install, got %v",
		err,
	)

	assert.Contains(t, err.Error(), "pre_install", "the error should name the phase, got %q", err)

	{
		keys := h.cache.keysRequested()
		assert.Empty(t, keys, "nothing should be downloaded after the hook refused, got %v", keys)
	}

	{
		_, err := h.index.GetByID(
			context.Background(),
			"better_ui",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "a refused install must leave no mod record behind")
	}

	{
		_, err := h.index.GetFilesByMod(context.Background(), "better_ui")
		assert.NoError(t, err, "inventory read failed: %v", err)
	}
}

func TestInstallPostHookFailureIsAdvisory(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)
	h.hooks.setFail(
		domain.PhasePostInstall,
		appErrors.New(appErrors.ErrCodeHookFailed, "post install said no"),
	)

	installed, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
	require.NoError(
		t,
		err,
		"the mod is already installed; a late hook failure must not report failure: %v",
		err,
	)

	assert.Equal(t, "better_ui", installed.ID, "unexpected mod %+v", installed)

	{
		_, err := h.index.GetByID(context.Background(), "better_ui")
		assert.NoError(t, err, "the record must survive an advisory hook failure: %v", err)
	}

	assert.True(
		t,
		hasEvent(h.events.types(), EventLog),
		"the failure must still be reported, got events %v",
		h.events.types(),
	)
}

func TestInstallCacheFailureLeavesNoRecord(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.2.0"))
	h.cache.getErr = appErrors.New(appErrors.ErrCodeDownloadFailed, "server said no")

	_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeDownloadFailed),
		"want DOWNLOAD_FAILED, got %v",
		err,
	)

	{
		_, err := h.index.GetByID(
			context.Background(),
			"better_ui",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "a failed download must leave no mod record")
	}

	entries, _ := os.ReadDir(filepath.Dir(h.payloadPath("better_ui", "1.2.0")))
	assert.Len(t, entries, 0, "the staging area was not cleaned up: %v", entries)
}

func TestInstallEmptyArchivePathIsDownloadFailed(t *testing.T) {
	h := newHarness(t)
	// The cache can report success with an empty path; the service must not
	// hand "" to the extractor.
	h.source.mods["better_ui"] = modFixture("better_ui", "1.0.0")
	h.cache.archives["better_ui"] = ""

	{
		_, err := h.svc.InstallMod(
			context.Background(),
			"vortex",
			"better_ui",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeDownloadFailed,
		), "want DOWNLOAD_FAILED, got %v", err)
	}
}

func TestInstallWithoutDownloadLocationIsInvalidArgument(t *testing.T) {
	h := newHarness(t)
	h.source.mods["broken"] = &domain.Mod{ID: "broken", Version: "1.0.0"}

	_, err := h.svc.InstallMod(context.Background(), "vortex", "broken")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"a mod with no download location must be refused up front, got %v",
		err,
	)
}

func TestInstallPurgeCacheDropsTheArchiveOnceInstalled(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"))

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"better_ui",
			InstallOptions{PurgeCache: true},
		)
		require.NoError(t, err, "install: %v", err)
	}

	{
		got := h.cache.removedKeys()
		assert.Len(t, got, 1, "the archive should be dropped once the payload exists, got %v", got)
	}
}

func TestInstallKeepsTheArchiveByDefault(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"))

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	{
		got := h.cache.removedKeys()
		assert.Empty(t, got, "the cache is there to save a re-download, got %v", got)
	}
}

func TestInstallRejectsCanceledContextBeforeAnyWork(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		_, err := h.svc.InstallMod(
			ctx,
			"vortex",
			"better_ui",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "want OPERATION_CANCELED, got %v", err)
	}

	{
		got := h.sources.requested()
		assert.Empty(t, got, "a canceled install must not query the source, got %v", got)
	}
}

func TestInstallToleratesNilContext(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"))
	//nolint:staticcheck // SA1012 a nil context is a caller bug, but it must not panic a service
	{
		_, err := h.svc.InstallMod(nil, "vortex", "better_ui")
		assert.NoError(t, err, "a nil context should be treated as Background, got %v", err)
	}
}

func TestReinstallModForces(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"))

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	{
		_, err := h.svc.ReinstallMod(
			context.Background(),
			"vortex",
			"better_ui",
			InstallOptions{},
		)
		require.NoError(t, err, "reinstall: %v", err)
	}
}

func TestInstallConcurrentSameModRunsOnce(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"))
	h.hooks.block = make(chan struct{})

	const n = 8

	type result struct {
		mod *domain.Mod
		err error
	}

	results := make(chan result, n)
	for range n {
		go func() {
			m, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
			results <- result{m, err}
		}()
	}
	// Give the goroutines time to queue on the per-mod lock, then let them go.
	time.Sleep(50 * time.Millisecond)
	close(h.hooks.block)

	ok, refused := 0, 0

	for range n {
		r := <-results
		if r.err == nil {
			ok++
			continue
		}

		if appErrors.IsAppError(r.err, appErrors.ErrCodeModAlreadyExists) {
			refused++
			continue
		}

		assert.Fail(t, "unexpected error: %v", r.err)
	}

	assert.Equal(
		t,
		1,
		ok,
		"%d installs succeeded, want exactly 1 (the lock has to serialise them)",
		ok,
	)

	assert.Equal(t, n-1, refused, "%d installs refused, want %d", refused, n-1)
}

func TestEnableModDeploysAndRecords(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod, "better_ui.dll", "better_ui.json")

	installed, err := h.svc.Install(
		context.Background(),
		"vortex",
		"better_ui",
		InstallOptions{Enable: boolPtr(false)},
	)
	require.NoError(t, err, "install")

	{
		err := h.svc.EnableMod(context.Background(), "better_ui")
		require.NoError(t, err, "enable: %v", err)
	}

	got := h.installed(t, "better_ui")
	assert.True(t, got.Enabled, "the record still says disabled")

	assert.True(
		t,
		got.UpdatedAt.Equal(modNow),
		"UpdatedAt = %v, want the injected clock %v",
		got.UpdatedAt,
		modNow,
	)

	assert.Equal(
		t,
		1,
		h.events.countOf(EventModEnabled),
		"expected one enabled event, got %v",
		h.events.types(),
	)

	assert.Len(
		t,
		h.deployer.deployTargets(),
		1,
		"enable should deploy once, got %v",
		h.deployer.deployTargets(),
	)

	assert.Equal(t, got.LocalPath, installed.LocalPath, "enabling must not move the payload")
}

func TestEnableModUnknownIsModNotFound(t *testing.T) {
	h := newHarness(t)
	{
		err := h.svc.EnableMod(
			context.Background(),
			"ghost",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "want MOD_NOT_FOUND, got %v", err)
	}
}

func TestEnableModWithMissingPayloadIsInvalidMod(t *testing.T) {
	h := newHarness(t)
	// A record whose folder was deleted behind the manager's back must not be
	// "enabled" into a state where the game folder points nowhere.
	h.index.put(
		&domain.Mod{
			ID:        "ghost",
			Version:   "1.0.0",
			LocalPath: domain.ModStoragePath(h.storage, "", "ghost", "1.0.0"),
			Enabled:   false,
		},
	)

	err := h.svc.EnableMod(context.Background(), "ghost")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod),
		"want INVALID_MOD, got %v",
		err,
	)

	assert.Empty(
		t,
		h.deployer.deployTargets(),
		"nothing should be deployed for a missing payload, got %v",
		h.deployer.deployTargets(),
	)
}

func TestEnableModWithoutGameIsGameNotFound(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) { o.locator = withoutGame() })
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	{
		err := h.svc.EnableMod(
			context.Background(),
			"better_ui",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameNotFound,
		), "want GAME_NOT_FOUND, got %v", err)
	}

	{
		got := h.installed(t, "better_ui")
		assert.False(t, got.Enabled, "a failed enable must not leave the record claiming enabled")
	}
}

func TestEnableModPreHookFailureAborts(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"better_ui",
			InstallOptions{Enable: boolPtr(false)},
		)
		require.NoError(t, err, "install: %v", err)
	}

	h.hooks.setFail(domain.PhasePreEnable, appErrors.New(appErrors.ErrCodeHookFailed, "no"))

	{
		err := h.svc.EnableMod(
			context.Background(),
			"better_ui",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeHookFailed,
		), "want HOOK_FAILED, got %v", err)
	}

	assert.Empty(
		t,
		h.deployer.deployTargets(),
		"a refused pre-enable must not deploy, got %v",
		h.deployer.deployTargets(),
	)

	{
		got := h.installed(t, "better_ui")
		assert.False(t, got.Enabled, "the record must stay disabled after a refused enable")
	}
}

func TestEnableModDeployFailureLeavesRecordDisabled(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"better_ui",
			InstallOptions{Enable: boolPtr(false)},
		)
		require.NoError(t, err, "install: %v", err)
	}

	h.deployer.deployErr = appErrors.New(appErrors.ErrCodeConfigIO, "mods folder is read-only")

	{
		err := h.svc.EnableMod(
			context.Background(),
			"better_ui",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInstallFailed,
		), "want INSTALL_FAILED, got %v", err)
	}

	{
		got := h.installed(t, "better_ui")
		assert.False(t, got.Enabled, "a failed deploy must not be recorded as enabled")
	}
}

func TestDisableModRemovesLinksAndKeepsPayload(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod, "better_ui.dll", "better_ui.json")

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	payload := h.payloadPath("better_ui", "1.2.0")

	{
		err := h.svc.DisableMod(context.Background(), "better_ui")
		require.NoError(t, err, "disable: %v", err)
	}

	{
		got := h.installed(t, "better_ui")
		assert.False(t, got.Enabled, "the record still says enabled")
	}

	assert.Equal(
		t,
		1,
		h.events.countOf(EventModDisabled),
		"expected one disabled event, got %v",
		h.events.types(),
	)

	calls := h.deployer.undeployCalls()
	require.Len(t, calls, 1, "expected one undeploy, got %v", calls)

	assert.True(
		t,
		equalStrings(calls[0], []string{"better_ui.dll", "better_ui.json"}),
		"undeployed %v, want the top-level entry names",
		calls[0],
	)

	{
		_, err := os.Stat(payload)
		assert.NoError(t, err, "disabling must keep the payload: %v", err)
	}
}

func TestDisableModIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.2.0"))

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"better_ui",
			InstallOptions{Enable: boolPtr(false)},
		)
		require.NoError(t, err, "install: %v", err)
	}

	{
		err := h.svc.DisableMod(context.Background(), "better_ui")
		require.NoError(t, err, "first disable: %v", err)
	}

	{
		err := h.svc.DisableMod(context.Background(), "better_ui")
		require.NoError(
			t,
			err,
			"a second disable of an already-disabled mod is a no-op, got %v",
			err,
		)
	}

	assert.Len(
		t,
		h.deployer.undeployCalls(),
		2,
		"expected two undeploy attempts, got %v",
		h.deployer.undeployCalls(),
	)
}

func TestDisableModWithoutGameStillUpdatesTheRecord(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	h.locator.modsPath = ""
	h.locator.install = nil

	{
		err := h.svc.DisableMod(context.Background(), "better_ui")
		require.NoError(t, err, "disabling without a game is not an error, got %v", err)
	}

	{
		got := h.installed(t, "better_ui")
		assert.False(
			t,
			got.Enabled,
			"the record must be disabled even when there is no game folder to clean",
		)
	}
}

func TestDisableModUndeployFailureLeavesRecordEnabled(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.2.0"))

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	h.deployer.undeployErr = appErrors.New(appErrors.ErrCodeConfigIO, "in use")

	err := h.svc.DisableMod(context.Background(), "better_ui")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeUninstallFailed),
		"want UNINSTALL_FAILED, got %v",
		err,
	)

	{
		got := h.installed(t, "better_ui")
		assert.True(t, got.Enabled, "the links are still there; the record must stay enabled")
	}
}

func TestUninstallRemovesEverything(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod, "better_ui.dll")

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	payload := h.payloadPath("better_ui", "1.2.0")

	{
		err := h.svc.UninstallMod(context.Background(), "better_ui")
		require.NoError(t, err, "uninstall: %v", err)
	}

	{
		_, err := h.index.GetByID(
			context.Background(),
			"better_ui",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "the record must be gone")
	}

	{
		_, err := os.Stat(payload)
		assert.True(t, os.IsNotExist(err), "the payload folder must be removed, stat gave %v", err)
	}

	assert.Len(
		t,
		h.deployer.undeployCalls(),
		1,
		"uninstall should undeploy once, got %v",
		h.deployer.undeployCalls(),
	)

	types := h.events.types()
	assert.False(
		t,
		!hasEvent(types, EventModUninstallStarted) || !hasEvent(types, EventModUninstallCompleted),
		"expected uninstall events, got %v",
		types,
	)

	assert.Empty(
		t,
		h.cache.removedKeys(),
		"uninstall must not purge the cache unless asked, got %v",
		h.cache.removedKeys(),
	)
}

func TestUninstallPurgeCacheDropsTheArchive(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.2.0"))

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	{
		err := h.svc.Uninstall(
			context.Background(),
			"better_ui",
			UninstallOptions{PurgeCache: true},
		)
		require.NoError(t, err, "uninstall: %v", err)
	}

	assert.Len(
		t,
		h.cache.removedKeys(),
		1,
		"expected the archive to be dropped, got %v",
		h.cache.removedKeys(),
	)
}

func TestUninstallUnknownIsModNotFound(t *testing.T) {
	h := newHarness(t)
	{
		err := h.svc.UninstallMod(
			context.Background(),
			"ghost",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "want MOD_NOT_FOUND, got %v", err)
	}
}

func TestUninstallPreHookFailureAborts(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"))

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	h.hooks.setFail(domain.PhasePreUninstall, appErrors.New(appErrors.ErrCodeHookFailed, "no"))

	{
		err := h.svc.UninstallMod(
			context.Background(),
			"better_ui",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeHookFailed,
		), "want HOOK_FAILED, got %v", err)
	}

	{
		_, err := h.index.GetByID(context.Background(), "better_ui")
		assert.NoError(t, err, "a refused uninstall must keep the record: %v", err)
	}
}

func TestUninstallRefusesToDeleteOutsideStorage(t *testing.T) {
	h := newHarness(t)
	outside := t.TempDir()

	keep := filepath.Join(outside, "precious")
	{
		err := os.MkdirAll(keep, 0o755)
		require.NoError(t, err, "create payload: %v", err)
	}
	// LocalPath comes from the database and could have been tampered with.
	h.index.put(&domain.Mod{ID: "evil", Version: "1.0.0", LocalPath: keep, Enabled: true})

	{
		err := h.svc.UninstallMod(context.Background(), "evil")
		require.NoError(
			t,
			err,
			"uninstall should still succeed, it just must not delete outside storage: %v",
			err,
		)
	}

	{
		_, err := os.Stat(keep)
		assert.NoError(t, err, "a payload outside the storage root must survive: %v", err)
	}

	assert.True(
		t,
		hasEvent(h.events.types(), EventLog),
		"refusing to delete must be reported, got %v",
		h.events.types(),
	)
}

func TestUninstallBlankIDIsRefused(t *testing.T) {
	h := newHarness(t)
	{
		err := h.svc.UninstallMod(context.Background(), "  ")
		require.Error(t, err, "a blank id must be refused")
	}
}

func TestGetModHooksReturnsACopy(t *testing.T) {
	h := newHarness(t)
	hooks := domain.ModHooks{
		PreEnable: []domain.HookScript{{Language: domain.LangLua, Content: "echo hi"}},
	}
	h.index.put(&domain.Mod{ID: "better_ui", Version: "1.0.0", Hooks: hooks})

	got, err := h.svc.GetModHooks(context.Background(), "better_ui")
	require.NoError(t, err, "get hooks")

	got.PreEnable[0].Content = "mutated"

	again, err := h.svc.GetModHooks(context.Background(), "better_ui")
	require.NoError(t, err, "get hooks")

	assert.Equal(
		t,
		"echo hi",
		again.PreEnable[0].Content,
		"GetModHooks handed out a view into the stored record",
	)
}

func TestGetModHooksUnknownIsModNotFound(t *testing.T) {
	h := newHarness(t)
	{
		_, err := h.svc.GetModHooks(
			context.Background(),
			"ghost",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "want MOD_NOT_FOUND, got %v", err)
	}
}

func TestUpdateModHooksStoresACopy(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "better_ui", Version: "1.0.0"})

	hooks := domain.ModHooks{
		PreEnable: []domain.HookScript{{Language: domain.LangLua, Content: "echo hi"}},
	}
	{
		err := h.svc.UpdateModHooks(context.Background(), "better_ui", hooks)
		require.NoError(t, err, "update hooks: %v", err)
	}

	hooks.PreEnable[0].Content = "mutated"

	stored := h.installed(t, "better_ui")
	assert.Equal(
		t,
		"echo hi",
		stored.Hooks.PreEnable[0].Content,
		"UpdateModHooks stored the caller's slice instead of a copy",
	)
}

func TestUpdateModHooksValidatesBeforeWriting(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "better_ui", Version: "1.0.0"})

	cases := []struct {
		name  string
		hooks domain.ModHooks
	}{
		{
			"unknown type",
			domain.ModHooks{PreEnable: []domain.HookScript{{Language: "powershell", Content: "x"}}},
		},
		{
			"blank content",
			domain.ModHooks{
				PostEnable: []domain.HookScript{{Language: domain.LangJS, Content: "   "}},
			},
		},
		{
			"negative timeout",
			domain.ModHooks{
				PreInstall: []domain.HookScript{
					{Language: domain.LangJS, Content: "x", Timeout: -1},
				},
			},
		},
		{
			"env without =",
			domain.ModHooks{
				PreDisable: []domain.HookScript{
					{Language: domain.LangLua, Content: "x", Env: []string{"NOEQUALS"}},
				},
			},
		},
		{
			"env with empty key",
			domain.ModHooks{
				PreDisable: []domain.HookScript{
					{Language: domain.LangLua, Content: "x", Env: []string{"=v"}},
				},
			},
		},
	}
	for _, tc := range cases {
		err := h.svc.UpdateModHooks(context.Background(), "better_ui", tc.hooks)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeHookInvalid),
			"%s: want HOOK_INVALID, got %v",
			tc.name,
			err,
		)
	}

	{
		stored := h.installed(t, "better_ui")
		assert.True(
			t,
			stored.Hooks.Empty(),
			"a rejected hook set must not be written: %+v",
			stored.Hooks,
		)
	}
}

func TestUpdateModHooksEnvValuesMayContainEquals(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "better_ui", Version: "1.0.0"})

	hooks := domain.ModHooks{
		PreEnable: []domain.HookScript{
			{Language: domain.LangLua, Content: "x", Env: []string{"TOKEN=a=b=c"}},
		},
	}
	{
		err := h.svc.UpdateModHooks(context.Background(), "better_ui", hooks)
		require.NoError(t, err, "a value containing = is legal, got %v", err)
	}
}

func TestUpdateModHooksRejectsEveryPhase(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "better_ui", Version: "1.0.0"})
	// UpdateModHooks replaces the whole set, so the phases accumulate here.
	var acc domain.ModHooks
	for _, phase := range domain.HookPhases() {
		acc.Set(
			phase,
			[]domain.HookScript{{Language: domain.LangJS, Content: "// " + string(phase)}},
		)

		{
			err := h.svc.UpdateModHooks(context.Background(), "better_ui", acc.Clone())
			require.NoError(t, err, "phase %q must be updatable, got %v", phase, err)
		}
		// The call replaces the whole set, so what was sent must be what is
		// stored: a partial update would silently drop the other phases.
		{
			got := h.installed(t, "better_ui").Hooks
			require.Equal(
				t,
				acc.Count(),
				got.Count(),
				"after updating %q the stored set has %d hooks, want %d",
				phase,
				got.Count(),
				acc.Count(),
			)
		}
	}

	stored := h.installed(t, "better_ui")
	assert.Len(
		t,
		domain.HookPhases(),
		stored.Hooks.Count(),
		"stored %d hooks across %d phases",
		stored.Hooks.Count(),
		len(domain.HookPhases()),
	)
}

func TestUpdateModHooksClearingAPhase(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "better_ui", Version: "1.0.0", Hooks: domain.ModHooks{
		PreEnable:  []domain.HookScript{{Language: domain.LangJS, Content: "x"}},
		PostEnable: []domain.HookScript{{Language: domain.LangJS, Content: "y"}},
	}})

	{
		err := h.svc.UpdateModHooks(
			context.Background(),
			"better_ui",
			domain.ModHooks{},
		)
		require.NoError(t, err, "clearing: %v", err)
	}

	assert.True(t, h.installed(t, "better_ui").Hooks.Empty(), "an empty set must clear every phase")
}

func TestUpdateModHooksUnknownIsModNotFound(t *testing.T) {
	h := newHarness(t)
	{
		err := h.svc.UpdateModHooks(
			context.Background(),
			"ghost",
			domain.ModHooks{},
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "want MOD_NOT_FOUND, got %v", err)
	}
}

func TestReorderModsStoresTheOrder(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "a", Version: "1.0.0"})
	h.index.put(&domain.Mod{ID: "b", Version: "1.0.0"})

	{
		err := h.svc.ReorderMods(context.Background(), []string{"b", "a"})
		require.NoError(t, err, "reorder: %v", err)
	}

	got, err := h.svc.LoadOrder(context.Background())
	require.NoError(t, err, "load order")

	assert.True(t, equalStrings(got, []string{"b", "a"}), "load order = %v, want [b a]", got)
}

func TestListModsScopesToTheActiveGame(t *testing.T) {
	h := newHarness(t)
	h.locator.withGameID("darktide")

	h.index.put(&domain.Mod{ID: "mine", Version: "1.0.0", GameID: "darktide"})
	h.index.put(&domain.Mod{ID: "theirs", Version: "1.0.0", GameID: "sietch"})
	h.index.put(&domain.Mod{ID: "legacy", Version: "1.0.0"})

	mods, err := h.svc.ListMods(context.Background())
	require.NoError(t, err, "list: %v", err)

	ids := make([]string, 0, len(mods))
	for _, m := range mods {
		ids = append(ids, m.ID)
	}

	assert.True(
		t,
		equalStrings(ids, []string{"mine"}),
		"list = %v, want only the active game's mods", ids,
	)
}

func TestInstallStampsTheActiveGameID(t *testing.T) {
	h := newHarness(t).withArchive(t, modFixture("better_ui", "1.2.0"))
	h.locator.withGameID("darktide")

	installed, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
	require.NoError(t, err, "install: %v", err)

	assert.Equal(t, "darktide", installed.GameID, "installed.GameID")
	assert.Equal(
		t,
		domain.ModStoragePath(h.storage, "darktide", "better_ui", "1.2.0"),
		installed.LocalPath,
		"payload lands under the game id",
	)
}

func TestEnablingAModDeploysWithTheConfiguredInstallMethod(t *testing.T) {
	h := newHarness(t).withArchive(t, modFixture("better_ui", "1.2.0"))
	h.locator.withGameID("darktide")

	installed, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
	require.NoError(t, err, "install: %v", err)

	err = h.svc.EnableMod(context.Background(), installed.ID)
	require.NoError(t, err, "enable: %v", err)

	assert.NotEmpty(t, h.deployer.deployModes, "a deployment happened")

	h.locator.withInstallMethod(domain.InstallMethodDirect)

	err = h.svc.DisableMod(context.Background(), installed.ID)
	require.NoError(t, err, "disable: %v", err)

	err = h.svc.EnableMod(context.Background(), installed.ID)
	require.NoError(t, err, "re-enable: %v", err)

	last := len(h.deployer.deployModes) - 1
	assert.Equal(t, platform.DeployCopy, h.deployer.deployModes[last],
		"the configured override reaches the deployer")
}

func TestEnablingAModDeploysWithTheGamesOwnInstallMethodByDefault(t *testing.T) {
	h := newHarness(t).withArchive(t, modFixture("better_ui", "1.2.0"))
	h.locator.withGameID("darktide").withGameInstallMethod(domain.InstallMethodSymlink)

	installed, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
	require.NoError(t, err, "install: %v", err)

	err = h.svc.EnableMod(context.Background(), installed.ID)
	require.NoError(t, err, "enable: %v", err)

	last := len(h.deployer.deployModes) - 1
	assert.Equal(t, platform.DeploySymlink, h.deployer.deployModes[last],
		"with no override the game definition decides")
}

func TestReorderModsIsScopedToTheActiveGame(t *testing.T) {
	h := newHarness(t)
	h.locator.withGameID("darktide")

	h.index.put(&domain.Mod{ID: "a", Version: "1.0.0", GameID: "darktide"})
	h.index.put(&domain.Mod{ID: "b", Version: "1.0.0", GameID: "sietch"})

	err := h.svc.ReorderMods(context.Background(), []string{"b"})
	require.Error(t, err, "a mod of another game must be rejected")
	assert.Equal(
		t,
		appErrors.ErrCodeModNotFound,
		appErrors.CodeOf(err),
		"reorder error code",
	)

	require.NoError(t, h.svc.ReorderMods(context.Background(), []string{"a"}), "reorder")

	got, err := h.svc.LoadOrder(context.Background())
	require.NoError(t, err, "load order")

	assert.True(t, equalStrings(got, []string{"a"}), "load order = %v, want [a]", got)

	other, err := h.index.LoadOrderByGame(context.Background(), "sietch")
	require.NoError(t, err, "other game's order")

	assert.Empty(t, other, "the other game's order must stay empty, got %v", other)
}

func TestReorderModsTrimsAndSkipsBlanks(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "a", Version: "1.0.0"})
	h.index.put(&domain.Mod{ID: "b", Version: "1.0.0"})
	{
		err := h.svc.ReorderMods(context.Background(), []string{" a ", "", "   ", "b"})
		require.NoError(t, err, "reorder: %v", err)
	}

	got, _ := h.svc.LoadOrder(context.Background())
	assert.True(
		t,
		equalStrings(got, []string{"a", "b"}),
		"load order = %v, want [a b] with blanks dropped",
		got,
	)
}

func TestReorderModsRejectsDuplicates(t *testing.T) {
	h := newHarness(t)

	err := h.svc.ReorderMods(context.Background(), []string{"a", "a"})
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"a duplicate id would silently drop one entry, want INVALID_ARGUMENT, got %v",
		err,
	)
}

func TestReorderModsEmptyClearsTheOrder(t *testing.T) {
	h := newHarness(t)
	h.index.setOrder([]string{"a"})

	{
		err := h.svc.ReorderMods(context.Background(), nil)
		require.NoError(t, err, "clearing: %v", err)
	}

	got, _ := h.svc.LoadOrder(context.Background())
	assert.Len(t, got, 0, "load order = %v, want empty", got)
}

func TestReorderModsPropagatesIndexFailure(t *testing.T) {
	h := newHarness(t)

	h.index.setOrderErr = errBoom
	{
		err := h.svc.ReorderMods(context.Background(), []string{"a"})
		require.True(t, errors.Is(err, errBoom), "want the index failure, got %v", err)
	}
}

func TestStorageRootRejectsRelativeConfigPath(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.Storage.Path = "relative/mods"
	h := newHarness(t, func(o *harnessOpts) {
		o.config = newStaticConfig(&cfg)
		o.locator = withoutGame()
	})
	h.svc = newModServiceOrFail(
		t,
		ModServiceDeps{Index: h.index, Config: h.config, Clock: modClock()},
	)

	{
		_, err := h.svc.storageRoot()
		require.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeNoStoragePath),
			"a relative storage path must be refused, got %v",
			err,
		)
	}
}

func TestStorageRootFallsBackToConfigThenPlatform(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.Storage.Path = ""
	store := newStaticConfig(&cfg)
	svc := newModServiceOrFail(
		t,
		ModServiceDeps{Index: newFakeIndex(), Config: store, Clock: modClock()},
	)

	root, err := svc.storageRoot()
	require.NoError(t, err, "storage root")

	dirs, err := platform.CurrentDirs()
	if err != nil {
		t.Skipf("cannot resolve the platform data dir: %v", err)
	}

	assert.Equal(
		t,
		dirs.StorageRoot(),
		root,
		"storage root = %q, want the platform default %q",
		root,
		dirs.StorageRoot(),
	)
}

func TestStorageRootDepsOverrideConfig(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.Storage.Path = "/from/config"
	svc := newModServiceOrFail(t, ModServiceDeps{
		Index:       newFakeIndex(),
		Config:      newStaticConfig(&cfg),
		StoragePath: "/from/deps",
		Clock:       modClock(),
	})

	root, err := svc.storageRoot()
	require.NoError(t, err, "storage root")

	assert.Equal(t, "/from/deps", root, "storage root = %q, want the explicit override", root)
}

func TestModPathReportsThePayload(t *testing.T) {
	h := newHarness(t)
	mod := modFixture("better_ui", "1.2.0")
	h.withArchive(t, mod)

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	got, err := h.svc.ModPath(context.Background(), "better_ui")
	require.NoError(t, err, "mod path")

	assert.Equal(
		t,
		h.payloadPath("better_ui", "1.2.0"),
		got,
		"mod path = %q, want %q",
		got,
		h.payloadPath("better_ui", "1.2.0"),
	)
}

func TestSymlinkStatusReportsOnlyOwnedEntries(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"), "better_ui.dll", "textures/hud.dds")

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}
	// Another tool put an unrelated file in the game mods folder.
	h.deployer.deployed["someone_elses.dll"] = "/elsewhere"

	statuses, err := h.svc.SymlinkStatus(context.Background())
	require.NoError(t, err, "symlink status")

	require.Len(t, statuses, 1, "got %d statuses, want 1: %+v", len(statuses), statuses)

	st := statuses[0]
	assert.Equal(t, "better_ui", st.ModID, "status for %q", st.ModID)

	{
		_, ok := st.Entries["someone_elses.dll"]
		assert.False(t, ok, "a foreign entry must not be reported as ours")
	}

	{
		_, ok := st.Entries["better_ui.dll"]
		assert.True(t, ok, "expected better_ui.dll in %v", st.Entries)
	}

	assert.Equal(t, 0, st.Damaged, "Damaged = %d, want 0", st.Damaged)
}

func TestSymlinkStatusSurvivesAFailedScan(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"), "better_ui.dll")

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	h.deployer.verifyErr = appErrors.New(appErrors.ErrCodeConfigIO, "unreadable")

	// A status page that fails wholesale is less useful than one that reports
	// "no entries": the mods themselves are still fine.
	statuses, err := h.svc.SymlinkStatus(context.Background())
	require.NoError(t, err, "a failed link scan must not fail the whole status call")

	assert.False(
		t,
		len(statuses) != 1 || len(statuses[0].Entries) != 0,
		"expected one entryless status, got %+v",
		statuses,
	)
}

func TestSymlinkStatusWithoutGameStillListsMods(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) { o.locator = withoutGame() })
	h.withArchive(t, bareMod("better_ui", "1.0.0"), "better_ui.dll")

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	statuses, err := h.svc.SymlinkStatus(context.Background())
	require.NoError(t, err, "no game must not be an error for a status query")

	require.Len(t, statuses, 1, "got %d statuses, want 1", len(statuses))

	assert.Equal(t, "better_ui", statuses[0].ModID, "status for %q", statuses[0].ModID)

	assert.Empty(
		t,
		statuses[0].Entries,
		"there is no game folder, so there are no entries: %+v",
		statuses[0].Entries,
	)

	assert.Equal(t, 0, statuses[0].Damaged, "Damaged = %d with no entries", statuses[0].Damaged)
}

func TestRelinkModRepairs(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"), "better_ui.dll")

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	repaired, err := h.svc.RelinkMod(context.Background(), "better_ui")
	require.NoError(t, err, "relink")

	assert.Nil(t, repaired, "nothing needed repairing, got %v", repaired)

	{
		calls := h.deployer.repairCalls()
		assert.Len(t, calls, 1, "expected one repair call, got %v", calls)
	}
}

func TestRelinkModUnknownIsModNotFound(t *testing.T) {
	h := newHarness(t)
	{
		_, err := h.svc.RelinkMod(
			context.Background(),
			"ghost",
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "want MOD_NOT_FOUND, got %v", err)
	}
}

func TestRelinkModPropagatesRepairFailure(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"))

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	h.deployer.repairErr = appErrors.New(appErrors.ErrCodeConfigIO, "read-only")

	{
		_, err := h.svc.RelinkMod(context.Background(), "better_ui")
		require.Error(t, err, "a failed repair must surface")
	}
}

func TestRefreshModsDisablesModsWhosePayloadVanished(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("good", "1.0.0"), "good.dll")
	h.withArchive(t, modFixture("gone", "1.0.0"), "gone.dll")

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "good")
		require.NoError(t, err, "install: %v", err)
	}

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "gone")
		require.NoError(t, err, "install: %v", err)
	}
	// Somebody deleted the payload folder behind the manager's back.
	{
		err := os.RemoveAll(h.payloadPath("gone", "1.0.0"))
		require.NoError(t, err, "remove payload: %v", err)
	}

	{
		err := h.svc.RefreshMods(context.Background())
		require.NoError(t, err, "refresh: %v", err)
	}

	{
		got := h.installed(t, "good")
		assert.True(t, got.Enabled, "an intact mod must stay enabled")
	}

	{
		got := h.installed(t, "gone")
		assert.False(
			t,
			got.Enabled,
			"a mod with no payload cannot be enabled; the record must be corrected",
		)
	}

	assert.True(
		t,
		hasEvent(h.events.types(), EventLog),
		"the correction must be reported, got %v",
		h.events.types(),
	)
}

func TestRefreshModsRepairsLinksOfEnabledMods(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.0.0"), "better_ui.dll")

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	{
		err := h.svc.RefreshMods(context.Background())
		require.NoError(t, err, "refresh: %v", err)
	}

	{
		calls := h.deployer.repairCalls()
		assert.Len(
			t,
			calls,
			1,
			"expected the enabled mod's links to be repaired once, got %v",
			calls,
		)
	}
}

func TestRefreshModsWithoutGameStillRuns(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) { o.locator = withoutGame() })
	h.withArchive(t, modFixture("better_ui", "1.0.0"))

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "better_ui")
		require.NoError(t, err, "install: %v", err)
	}

	{
		err := h.svc.RefreshMods(context.Background())
		require.NoError(t, err, "refresh without a game is not an error: %v", err)
	}
}

func TestRefreshModsStopsOnCancel(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("a", "1.0.0"))
	h.withArchive(t, modFixture("b", "1.0.0"))

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "a")
		require.NoError(t, err, "install: %v", err)
	}

	{
		_, err := h.svc.InstallMod(context.Background(), "vortex", "b")
		require.NoError(t, err, "install: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		err := h.svc.RefreshMods(
			ctx,
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "want OPERATION_CANCELED, got %v", err)
	}
}

func boolPtr(v bool) *bool { return &v }

// foreignKeyIndex enforces the one constraint the real repository has and a plain
// fake does not: mod_files.mod_id references mods.id, and the database turns a
// dangling reference into an error.
//
// Without it the install path looked correct against the fake and failed against
// the real database, because the inventory was written before the mod row existed.
type foreignKeyIndex struct {
	*fakeIndex
}

func (f *foreignKeyIndex) SaveFiles(
	ctx context.Context,
	modID string,
	files []domain.ModFile,
) error {
	f.mu.Lock()
	_, known := f.mods[modID]
	f.mu.Unlock()

	if !known {
		return appErrors.New(appErrors.ErrCodeModNotFound,
			"mod %q is not installed", modID)
	}

	return f.fakeIndex.SaveFiles(ctx, modID, files)
}

func TestInstallRecordsTheModBeforeItsFileInventory(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.2.0"), "textures/hud.dds")
	index := &foreignKeyIndex{fakeIndex: h.index}
	svc := newModServiceOrFail(t, ModServiceDeps{
		Sources:     h.sources,
		Cache:       h.cache,
		Index:       index,
		Hooks:       h.hooks,
		Locator:     h.locator,
		Deployer:    h.deployer,
		Extractor:   storage.NewArchiveExtractor(),
		Config:      h.config,
		Events:      h.events,
		Clock:       modClock(),
		StoragePath: h.storage,
	})

	{
		_, err := svc.Install(
			context.Background(),
			"vortex",
			"better_ui",
			InstallOptions{},
		)
		require.NoError(t, err, "install: %v", err)
	}
	// Both halves of the record exist: a payload left over from a failed install is
	// recoverable, a file list pointing at an unwritten mod is not representable.
	{
		_, err := os.Stat(h.payloadPath("better_ui", "1.2.0"))
		assert.NoError(t, err, "payload missing after install: %v", err)
	}

	{
		files := h.index.files["better_ui"]
		assert.NotEmpty(t, files, "the file inventory was not recorded")
	}
}

func TestInstallMergesCallerSourceDataWithoutOverwritingTheSource(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.2.0"), "textures/hud.dds")
	// The source's own record is the authority on the mod: a caller repeating one
	// of its keys is echoing it, and contradicting it is not allowed silently.
	h.source.mods["better_ui"].SourceData["version_id"] = "8001"

	{
		_, err := h.svc.Install(context.Background(), "vortex", "better_ui", InstallOptions{
			SourceData: map[string]any{
				"nxm_key":     "one-time-key",
				"nxm_expires": "1791538669",
				"nxm_user_id": "189193053",

				"version_id": "9999",

				"   ": "blank",
			},
		})
		require.NoError(t, err, "install: %v", err)
	}

	got := h.installed(t, "better_ui").SourceData
	for key, want := range map[string]any{
		"nxm_key":     "one-time-key",
		"nxm_expires": "1791538669",
		"nxm_user_id": "189193053",
		"version_id":  "8001",
	} {
		assert.Equal(t, want, got[key], "SourceData[%q] = %v, want %v", key, got[key], want)
	}

	{
		_, exists := got["   "]
		assert.False(t, exists, "a blank key was merged into the record")
	}
}

func TestInstallLeavesSourceDataAloneWhenNoneIsSupplied(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("better_ui", "1.2.0"), "textures/hud.dds")

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"better_ui",
			InstallOptions{},
		)
		require.NoError(t, err, "install: %v", err)
	}

	{
		got := h.installed(t, "better_ui").SourceData
		assert.Len(
			t,
			got,
			1,
			"SourceData = %v, want only the download_url the source published",
			got,
		)
	}
}

func TestMergeSourceDataToleratesWhatItIsHanded(t *testing.T) {
	// Both guards sit on a path that has already checked the mod, and a panic
	// there would take a worker down with it.
	mergeSourceData(nil, map[string]any{"nxm_key": "k"})
	mergeSourceData(&domain.Mod{ID: "better_ui"}, nil)
	mergeSourceData(&domain.Mod{ID: "better_ui"}, map[string]any{})
}

// TestInstallWithGameRootMode verifies that a game-root mod deploys beside the
// mods folder and stays out of the load order file.
func TestInstallWithGameRootMode(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("game_root_mod", "1.0.0"), "mods/mymod/file.txt")

	_, err := h.svc.Install(context.Background(), "vortex", "game_root_mod", InstallOptions{
		SourceData: map[string]any{"install_mode": "game_root"},
	})
	require.NoError(t, err, "install")

	mod := h.installed(t, "game_root_mod")
	assert.Equal(
		t,
		domain.InstallModeGameRoot,
		mod.InstallMode,
		"InstallMode = %q, want %q",
		mod.InstallMode,
		domain.InstallModeGameRoot,
	)

	assert.Empty(
		t,
		mod.ModFolderName,
		"ModFolderName = %q, want empty for game_root mod",
		mod.ModFolderName,
	)

	{
		err := h.svc.EnableMod(context.Background(), "game_root_mod")
		require.NoError(t, err, "enable: %v", err)
	}

	loadOrderPath := filepath.Join(h.locator.modsPath, "mod_load_order.txt")

	content, err := os.ReadFile(loadOrderPath)
	if err != nil {
		// The file is only written once a mod is in the order.
		require.True(t, os.IsNotExist(err), "read load order file: %v", err)
	} else {
		assert.NotContains(
			t,
			string(content),
			"game_root_mod",
			"game_root mod should not appear in load order file",
		)
	}
}

// TestInstallWithModsFolderMode verifies that a mods-folder mod deploys into the
// mods folder and appears in the load order file.
func TestInstallWithModsFolderMode(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("mods_folder_mod", "1.0.0"), "mymod/file.txt")

	_, err := h.svc.Install(context.Background(), "vortex", "mods_folder_mod", InstallOptions{
		SourceData: map[string]any{"install_mode": "mods_folder"},
	})
	require.NoError(t, err, "install")

	mod := h.installed(t, "mods_folder_mod")
	assert.Equal(
		t,
		domain.InstallModeModsFolder,
		mod.InstallMode,
		"InstallMode = %q, want %q",
		mod.InstallMode,
		domain.InstallModeModsFolder,
	)

	assert.NotEmpty(t, mod.ModFolderName, "ModFolderName should be set for mods_folder mod")

	{
		err := h.svc.EnableMod(context.Background(), "mods_folder_mod")
		require.NoError(t, err, "enable: %v", err)
	}

	loadOrderPath := filepath.Join(h.locator.modsPath, "mod_load_order.txt")

	content, err := os.ReadFile(loadOrderPath)
	require.NoError(t, err, "read load order file")

	assert.Contains(
		t,
		string(content),
		mod.ModFolderName,
		"load order file should contain %q, got %q",
		mod.ModFolderName,
		string(content),
	)
}

// TestPrimaryModFolderName exercises several archive shapes. The ModFile entries
// mimic real extraction: a directory entry has SizeBytes=0 and an empty checksum.
func TestPrimaryModFolderName(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		files    []domain.ModFile
	}{
		{
			name: "single top-level folder",
			files: []domain.ModFile{
				{RelPath: "mymod/", SizeBytes: 0}, // directory
				{RelPath: "mymod/file1.txt", SizeBytes: 100, Checksum: "abc"},
				{RelPath: "mymod/file2.txt", SizeBytes: 200, Checksum: "def"},
			},
			expected: "mymod",
		},
		{
			name: "multiple top-level folders - first wins",
			files: []domain.ModFile{
				{RelPath: "folder_a/", SizeBytes: 0},
				{RelPath: "folder_a/file.txt", SizeBytes: 50, Checksum: "abc"},
				{RelPath: "folder_b/", SizeBytes: 0},
				{RelPath: "folder_b/file.txt", SizeBytes: 60, Checksum: "def"},
			},
			expected: "folder_a",
		},
		{
			name: "flat archive - no folder name",
			files: []domain.ModFile{
				{RelPath: "file1.txt", SizeBytes: 100, Checksum: "abc"},
				{RelPath: "file2.txt", SizeBytes: 200, Checksum: "def"},
			},
			expected: "",
		},
		{
			name:     "empty inventory",
			files:    []domain.ModFile{},
			expected: "",
		},
		{
			name: "nested paths",
			files: []domain.ModFile{
				{RelPath: "mymod/", SizeBytes: 0},
				{RelPath: "mymod/textures/texture.dds", SizeBytes: 500, Checksum: "abc"},
				{RelPath: "mymod/scripts/script.lua", SizeBytes: 300, Checksum: "def"},
			},
			expected: "mymod",
		},
		{
			name: "windows backslashes",
			files: []domain.ModFile{
				{RelPath: "mymod/", SizeBytes: 0},
				{RelPath: "mymod\\file.txt", SizeBytes: 100, Checksum: "abc"},
			},
			expected: "mymod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := primaryModFolderName(tt.files)
			assert.Equal(
				t,
				tt.expected,
				got,
				"primaryModFolderName = %q, want %q",
				got,
				tt.expected,
			)
		})
	}
}

// TestSyncLoadOrderFile verifies that the load order file is written correctly.
func TestSyncLoadOrderFile(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("mod_a", "1.0.0"), "mod_a/file.txt")
	h.withArchive(t, modFixture("mod_b", "1.0.0"), "mod_b/file.txt")
	h.withArchive(t, modFixture("mod_c", "1.0.0"), "mod_c/file.txt")

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"mod_a",
			InstallOptions{},
		)
		require.NoError(t, err, "install mod_a: %v", err)
	}

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"mod_b",
			InstallOptions{},
		)
		require.NoError(t, err, "install mod_b: %v", err)
	}

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"mod_c",
			InstallOptions{},
		)
		require.NoError(t, err, "install mod_c: %v", err)
	}

	{
		err := h.svc.EnableMod(context.Background(), "mod_b")
		require.NoError(t, err, "enable mod_b: %v", err)
	}

	{
		err := h.svc.EnableMod(context.Background(), "mod_a")
		require.NoError(t, err, "enable mod_a: %v", err)
	}

	{
		err := h.svc.EnableMod(context.Background(), "mod_c")
		require.NoError(t, err, "enable mod_c: %v", err)
	}

	// Set explicit load order
	{
		err := h.svc.ReorderMods(context.Background(), []string{"mod_c", "mod_a"})
		require.NoError(t, err, "reorder: %v", err)
	}

	loadOrderPath := filepath.Join(h.locator.modsPath, "mod_load_order.txt")

	content, err := os.ReadFile(loadOrderPath)
	require.NoError(t, err, "read load order file")

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	assert.False(t, len(lines) < 2, "load order file has too few lines: %v", lines)

	assert.Equal(t, "mod_c", lines[0], "first line = %q, want mod_c", lines[0])

	assert.Equal(t, "mod_a", lines[1], "second line = %q, want mod_a", lines[1])
	// mod_b should be third (not in explicit order, falls back to priority)
	assert.False(t, len(lines) >= 3 && lines[2] != "mod_b", "third line = %q, want mod_b", lines[2])
}

// TestDisableModRemovesFromLoadOrder verifies that disabling a mod removes
// it from the load order file.
func TestDisableModRemovesFromLoadOrder(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("disable_test", "1.0.0"), "disable_test/file.txt")

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"disable_test",
			InstallOptions{},
		)
		require.NoError(t, err, "install: %v", err)
	}

	{
		err := h.svc.EnableMod(context.Background(), "disable_test")
		require.NoError(t, err, "enable: %v", err)
	}

	loadOrderPath := filepath.Join(h.locator.modsPath, "mod_load_order.txt")

	content, _ := os.ReadFile(loadOrderPath)
	assert.Contains(
		t,
		string(content),
		"disable_test",
		"mod should be in load order file after enable",
	)

	{
		err := h.svc.DisableMod(context.Background(), "disable_test")
		require.NoError(t, err, "disable: %v", err)
	}

	content, _ = os.ReadFile(loadOrderPath)
	assert.NotContains(
		t,
		string(content),
		"disable_test",
		"mod should be removed from load order file after disable",
	)
}

// TestUninstallModRemovesFromLoadOrder verifies that uninstalling a mod
// removes it from the load order file.
func TestUninstallModRemovesFromLoadOrder(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("uninstall_test", "1.0.0"), "uninstall_test/file.txt")

	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"uninstall_test",
			InstallOptions{},
		)
		require.NoError(t, err, "install: %v", err)
	}

	{
		err := h.svc.EnableMod(context.Background(), "uninstall_test")
		require.NoError(t, err, "enable: %v", err)
	}

	loadOrderPath := filepath.Join(h.locator.modsPath, "mod_load_order.txt")

	content, _ := os.ReadFile(loadOrderPath)
	assert.Contains(
		t,
		string(content),
		"uninstall_test",
		"mod should be in load order file after enable",
	)

	{
		err := h.svc.UninstallMod(context.Background(), "uninstall_test")
		require.NoError(t, err, "uninstall: %v", err)
	}

	content, _ = os.ReadFile(loadOrderPath)
	assert.NotContains(
		t,
		string(content),
		"uninstall_test",
		"mod should be removed from load order file after uninstall",
	)
}

// TestEnableDisableModUpdatesLoadOrderFile verifies that enabling and disabling
// a mod correctly updates the load order file, even without an explicit
// reorder. The mod should appear when enabled and disappear when disabled.
func TestEnableDisableModUpdatesLoadOrderFile(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("enable_test", "1.0.0"), "enable_test/file.txt")

	disabled := false
	{
		_, err := h.svc.Install(
			context.Background(),
			"vortex",
			"enable_test",
			InstallOptions{Enable: &disabled},
		)
		require.NoError(t, err, "install: %v", err)
	}

	loadOrderPath := filepath.Join(h.locator.modsPath, "mod_load_order.txt")

	content, _ := os.ReadFile(loadOrderPath)
	assert.NotContains(
		t,
		string(content),
		"enable_test",
		"mod should not be in load order file before enabling",
	)

	{
		err := h.svc.EnableMod(context.Background(), "enable_test")
		require.NoError(t, err, "enable: %v", err)
	}

	content, _ = os.ReadFile(loadOrderPath)
	assert.Contains(
		t,
		string(content),
		"enable_test",
		"mod should be in load order file after enabling, got: %q",
		string(content),
	)

	{
		err := h.svc.DisableMod(context.Background(), "enable_test")
		require.NoError(t, err, "disable: %v", err)
	}

	content, _ = os.ReadFile(loadOrderPath)
	assert.NotContains(
		t,
		string(content),
		"enable_test",
		"mod should be removed from load order file after disabling, got: %q",
		string(content),
	)

	{
		err := h.svc.EnableMod(context.Background(), "enable_test")
		require.NoError(t, err, "re-enable: %v", err)
	}

	content, _ = os.ReadFile(loadOrderPath)
	assert.Contains(
		t,
		string(content),
		"enable_test",
		"mod should be in load order file after re-enabling, got: %q",
		string(content),
	)
}

// TestMultipleModsEnableDisableLoadOrder verifies that multiple mods can be
// enabled/disabled and the load order file reflects the correct state.
func TestMultipleModsEnableDisableLoadOrder(t *testing.T) {
	h := newHarness(t)
	h.withArchive(t, modFixture("mod_a", "1.0.0"), "mod_a/file.txt")
	h.withArchive(t, modFixture("mod_b", "1.0.0"), "mod_b/file.txt")
	h.withArchive(t, modFixture("mod_c", "1.0.0"), "mod_c/file.txt")

	for _, id := range []string{"mod_a", "mod_b", "mod_c"} {
		{
			_, err := h.svc.Install(
				context.Background(),
				"vortex",
				id,
				InstallOptions{},
			)
			require.NoError(t, err, "install %s: %v", id, err)
		}
	}

	loadOrderPath := filepath.Join(h.locator.modsPath, "mod_load_order.txt")

	{
		err := h.svc.EnableMod(context.Background(), "mod_a")
		require.NoError(t, err, "enable mod_a: %v", err)
	}

	content, _ := os.ReadFile(loadOrderPath)
	assert.Contains(
		t,
		string(content),
		"mod_a",
		"mod_a should be in load order file, got: %q",
		string(content),
	)

	{
		err := h.svc.EnableMod(context.Background(), "mod_c")
		require.NoError(t, err, "enable mod_c: %v", err)
	}

	content, _ = os.ReadFile(loadOrderPath)
	assert.Contains(
		t,
		string(content),
		"mod_c",
		"mod_c should be in load order file, got: %q",
		string(content),
	)

	{
		err := h.svc.DisableMod(context.Background(), "mod_a")
		require.NoError(t, err, "disable mod_a: %v", err)
	}

	content, _ = os.ReadFile(loadOrderPath)
	assert.NotContains(
		t,
		string(content),
		"mod_a",
		"mod_a should not be in load order file after disable, got: %q",
		string(content),
	)

	assert.Contains(
		t,
		string(content),
		"mod_c",
		"mod_c should still be in load order file, got: %q",
		string(content),
	)

	{
		err := h.svc.EnableMod(context.Background(), "mod_b")
		require.NoError(t, err, "enable mod_b: %v", err)
	}

	content, _ = os.ReadFile(loadOrderPath)
	assert.Contains(
		t,
		string(content),
		"mod_b",
		"mod_b should be in load order file, got: %q",
		string(content),
	)

	assert.Contains(
		t,
		string(content),
		"mod_c",
		"mod_c should still be in load order file, got: %q",
		string(content),
	)

	assert.NotContains(
		t,
		string(content),
		"mod_a",
		"mod_a should not be in load order file, got: %q",
		string(content),
	)
}
