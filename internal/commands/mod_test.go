package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modWorkspace builds a workspace with a game, a configured game path and one
// local mod archive ready to install.
func modWorkspace(t *testing.T) (*workspace, *App) {
	t.Helper()
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	})
	w.writeModArchive(t, "better_ui", "1.2.0")

	return w, w.mustApp(t)
}

// findLocalMod returns the id of the one mod the local source offers.
func findLocalMod(t *testing.T, app *App) string {
	t.Helper()

	mods, err := app.Sources.SearchSource("local", "better")
	require.NoError(t, err, "search the local source")

	require.Len(t, mods, 1, "local source offered %d mods, want 1", len(mods))

	return mods[0].ID
}

// TestModLifecycleThroughTheCommandLayer walks one mod through every state a
// frontend drives it through, against a real game folder, a real archive, the
// real deployer and the real database. Everything the command layer adds is the
// context it derives and the trimming it does, so the only way to see whether it
// did that correctly is to let a real operation run under a real scope.
func TestModLifecycleThroughTheCommandLayer(t *testing.T) {
	w, app := modWorkspace(t)
	id := findLocalMod(t, app)

	mods, err := app.Mods.ListMods()
	require.NoError(t, err, "ListMods on an empty install")

	require.Empty(t, mods, "a fresh manager listed %d mods", len(mods))

	installed, err := app.Mods.InstallMod("local", id)
	require.NoError(t, err, "InstallMod")

	assert.True(
		t,
		installed.Enabled,
		"the mod was installed while a game folder was configured, so it should be enabled",
	)

	assert.Equal(
		t,
		"local",
		installed.SourceID,
		"installed mod SourceID = %q, want local",
		installed.SourceID,
	)

	wantPath := domain.ModStoragePath(w.storageRoot, installed.GameID, installed.ID,
		installed.Version)
	assert.Equal(
		t,
		wantPath,
		installed.LocalPath,
		"installed mod LocalPath = %q, want %q",
		installed.LocalPath,
		wantPath,
	)

	{
		_, err := os.Stat(filepath.Join(installed.LocalPath, "textures", "hud.dds"))
		assert.NoError(t, err, "the payload was not extracted: %v", err)
	}

	// The mods folder holds one entry per top-level payload entry, and the
	// manifest never reaches the game.
	{
		_, err := os.Lstat(filepath.Join(w.modsDir, "textures"))
		assert.NoError(t, err, "the mod was not deployed: %v", err)
	}

	{
		_, err := os.Lstat(filepath.Join(w.modsDir, "mod.toml"))
		assert.Error(t, err, "mod.toml was deployed into the game folder")
	}

	// A padded id is what a user pastes from a filename, and it must still work.
	padded, err := app.Mods.GetMod("  " + id + "  ")
	require.NoError(t, err, "GetMod with a padded id")

	assert.Equal(t, id, padded.ID, "GetMod returned %q, want %q", padded.ID, id)

	path, err := app.Mods.ModPath(id)
	require.NoError(t, err, "ModPath")

	assert.Equal(t, installed.LocalPath, path, "ModPath = %q, want %q", path, installed.LocalPath)

	status, err := app.Mods.SymlinkStatus()
	require.NoError(t, err, "SymlinkStatus")

	require.False(
		t,
		len(status) != 1 || status[0].ModID != id,
		"SymlinkStatus = %+v, want one entry for %q",
		status,
		id,
	)

	assert.Equal(
		t,
		0,
		status[0].Damaged,
		"a freshly enabled mod reports %d damaged links",
		status[0].Damaged,
	)

	{
		err := app.Mods.DisableMod(id)
		require.NoError(t, err, "DisableMod: %v", err)
	}

	{
		_, err := os.Lstat(filepath.Join(w.modsDir, "textures"))
		assert.Error(t, err, "DisableMod left the mod deployed")
	}

	{
		_, err := os.Stat(installed.LocalPath)
		assert.NoError(t, err, "DisableMod removed the payload: %v", err)
	}

	{
		err := app.Mods.EnableMod(id)
		require.NoError(t, err, "EnableMod after DisableMod: %v", err)
	}

	{
		_, err := os.Lstat(filepath.Join(w.modsDir, "textures"))
		assert.NoError(t, err, "EnableMod did not redeploy: %v", err)
	}

	repaired, err := app.Mods.RelinkMod(id)
	require.NoError(t, err, "RelinkMod")

	assert.Len(t, repaired, 0, "RelinkMod rewrote %v on a healthy install", repaired)

	{
		err := app.Mods.RefreshMods()
		require.NoError(t, err, "RefreshMods: %v", err)
	}

	order, err := app.Mods.LoadOrder()
	require.NoError(t, err, "LoadOrder")

	assert.False(t, len(order) > 1, "LoadOrder on a single mod = %v", order)

	{
		err := app.Mods.ReorderMods([]string{id})
		assert.NoError(t, err, "ReorderMods with one id: %v", err)
	}

	updates, err := app.Mods.CheckModUpdates()
	require.NoError(t, err, "CheckModUpdates with every remote source switched off")

	require.Len(
		t,
		updates,
		1,
		"CheckModUpdates = %d entries, want one for the installed mod",
		len(updates),
	)

	assert.False(t, updates[0].Available, "a local mod claims an update is available")

	conflicts, err := app.Mods.DetectConflicts()
	require.NoError(t, err, "DetectConflicts")

	assert.Len(t, conflicts, 0, "DetectConflicts on a single mod = %+v", conflicts)

	{
		err := app.Mods.UninstallMod(id)
		require.NoError(t, err, "UninstallMod: %v", err)
	}

	{
		_, err := app.Mods.GetMod(id)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
			"GetMod after uninstall error = %v, want MOD_NOT_FOUND",
			err,
		)
	}

	{
		_, err := os.Stat(installed.LocalPath)
		assert.True(t, os.IsNotExist(err), "UninstallMod left the payload behind: %v", err)
	}

	{
		_, err := os.Lstat(filepath.Join(w.modsDir, "textures"))
		assert.Error(t, err, "UninstallMod left the mod deployed")
	}
}

// TestInstallWithoutAGameFolderLeavesTheModDisabled is the first-run case: there
// is nowhere to deploy to, and a mod that reported itself enabled would be a lie
// the user discovers at launch.
func TestInstallWithoutAGameFolderLeavesTheModDisabled(t *testing.T) {
	w := newWorkspace(t)
	w.writeModArchive(t, "better_ui", "1.2.0")
	app := w.mustApp(t)
	id := findLocalMod(t, app)

	installed, err := app.Mods.InstallMod("local", id)
	require.NoError(t, err, "InstallMod with no game")

	assert.False(
		t,
		installed.Enabled,
		"the mod reported itself enabled although no game folder resolves",
	)

	{
		err := app.Mods.EnableMod(id)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound),
			"EnableMod with no game error = %v, want GAME_NOT_FOUND",
			err,
		)
	}
}

// TestUninstallWithPurgeDropsTheCachedArchive pins the difference between the two
// uninstall forms. Keeping the archive is usually what a user wants, so the
// simple command must keep it and only the explicit form may remove it.
func TestUninstallWithPurgeDropsTheCachedArchive(t *testing.T) {
	_, app := modWorkspace(t)

	id := findLocalMod(t, app)
	{
		_, err := app.Mods.InstallMod("local", id)
		require.NoError(t, err, "InstallMod: %v", err)
	}

	info, err := app.Cache.GetCacheInfo()
	require.NoError(t, err, "GetCacheInfo")

	require.NotEqual(t, 0, info.FileCount, "installing left the cache empty")

	{
		err := app.Mods.UninstallMod(id)
		require.NoError(t, err, "UninstallMod: %v", err)
	}

	kept, err := app.Cache.GetCacheInfo()
	require.NoError(t, err, "GetCacheInfo after uninstall")

	assert.Equal(
		t,
		info.FileCount,
		kept.FileCount,
		"the plain uninstall removed %d cached archives",
		info.FileCount-kept.FileCount,
	)

	{
		_, err := app.Mods.InstallMod("local", id)
		require.NoError(t, err, "reinstall: %v", err)
	}

	{
		err := app.Mods.UninstallModWithOptions(
			id,
			api.UninstallOptions{PurgeCache: true},
		)
		require.NoError(t, err, "UninstallModWithOptions: %v", err)
	}

	purged, err := app.Cache.GetCacheInfo()
	require.NoError(t, err, "GetCacheInfo after a purge")

	assert.Equal(
		t,
		0,
		purged.FileCount,
		"a purging uninstall left %d archives in the cache",
		purged.FileCount,
	)
}

// TestModCommandsReportAnUnknownMod: every id-taking command has to agree that the
// mod does not exist. A frontend showing "no such mod" for one button and a bare
// failure for the next one is a support ticket nobody can act on.
func TestModCommandsReportAnUnknownMod(t *testing.T) {
	_, app := modWorkspace(t)

	const missing = "no-such-mod"

	cases := []struct {
		call func() error
		name string
	}{
		{func() error { _, err := app.Mods.GetMod(missing); return err }, "GetMod"},
		{func() error { return app.Mods.EnableMod(missing) }, "EnableMod"},
		{func() error { return app.Mods.DisableMod(missing) }, "DisableMod"},
		{func() error { return app.Mods.UninstallMod(missing) }, "UninstallMod"},
		{func() error { return app.Mods.UpdateMod(missing) }, "UpdateMod"},
		{func() error { _, err := app.Mods.ModPath(missing); return err }, "ModPath"},
		{func() error { _, err := app.Mods.RelinkMod(missing); return err }, "RelinkMod"},
		{func() error { _, err := app.Mods.GetModHooks(missing); return err }, "GetModHooks"},
		{func() error {
			return app.Mods.UpdateModHooks(missing, api.ModHooks{})
		}, "UpdateModHooks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			require.Error(t, err, "%s reported success for a mod that is not installed", tc.name)

			assert.True(
				t,
				appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
				"%s error code = %v, want MOD_NOT_FOUND (err: %v)",
				tc.name,
				appErrors.CodeOf(err),
				err,
			)

			assert.Contains(
				t,
				err.Error(),
				missing,
				"%s error does not name the mod: %v",
				tc.name,
				err,
			)
		})
	}
}

// TestCancelUpdateIsSilentWhenNothingIsRunning: the caller wanted the update to
// stop and it is stopped, so there is nothing to report.
func TestCancelUpdateIsSilentWhenNothingIsRunning(t *testing.T) {
	_, app := modWorkspace(t)
	{
		err := app.Mods.CancelUpdate("no-such-mod")
		assert.NoError(t, err, "CancelUpdate with nothing running: %v", err)
	}
}

// TestUpdateModHooksRoundTripsAndRefusesABadSet.
func TestUpdateModHooksRoundTripsAndRefusesABadSet(t *testing.T) {
	_, app := modWorkspace(t)

	id := findLocalMod(t, app)
	{
		_, err := app.Mods.InstallMod("local", id)
		require.NoError(t, err, "InstallMod: %v", err)
	}

	hooks := api.ModHooks{
		PreEnable: []api.HookScript{
			{Language: api.LangJS, Content: "console.log('enabling')"},
		},
		PostInstall: []api.HookScript{
			{Language: api.LangJS, Content: "console.log('installed')", Timeout: 5},
		},
	}
	{
		err := app.Mods.UpdateModHooks(id, hooks)
		require.NoError(t, err, "UpdateModHooks: %v", err)
	}

	stored, err := app.Mods.GetModHooks(id)
	require.NoError(t, err, "GetModHooks")

	assert.False(
		t,
		len(stored.PreEnable) != 1 || stored.PreEnable[0].Content != hooks.PreEnable[0].Content,
		"stored pre_enable hooks = %+v",
		stored.PreEnable,
	)

	assert.False(
		t,
		len(stored.PostInstall) != 1 || stored.PostInstall[0].Timeout != 5,
		"stored post_install hooks = %+v",
		stored.PostInstall,
	)

	// A rejected set must leave the stored one alone, or one bad script in a
	// settings form would silently clear the rest.
	bad := api.ModHooks{
		PreEnable:  []api.HookScript{{Language: api.LangJS, Content: "kept"}},
		PostEnable: []api.HookScript{{Language: "powershell", Content: "boom"}},
	}
	{
		err := app.Mods.UpdateModHooks(
			id,
			bad,
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeHookInvalid,
		), "UpdateModHooks with an unknown hook type error = %v, want HOOK_INVALID", err)
	}

	again, err := app.Mods.GetModHooks(id)
	require.NoError(t, err, "GetModHooks after the refusal")

	assert.False(
		t,
		len(again.PostEnable) != 0 || len(again.PreEnable) != 1,
		"a refused hook set was partly stored: %+v",
		again,
	)
}

// TestResolveConflictRefusesAnUnknownResolution. The complaint has to list the
// spellings, because the caller supplied one and the useful next step is to see
// what the alternative is.
func TestResolveConflictRefusesAnUnknownResolution(t *testing.T) {
	_, app := modWorkspace(t)

	err := app.Mods.ResolveConflict("file_overlap:a:b", "disable-everything")
	require.Error(t, err, "an unrecognised resolution was accepted")

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error code = %v, want INVALID_ARGUMENT",
		appErrors.CodeOf(err),
	)

	for _, spelling := range []string{"keep-both", "disable-mod-a", "uninstall-mod-b"} {
		assert.Contains(t, err.Error(), spelling, "error does not list %q: %v", spelling, err)
	}
}

// TestResolveConflictReportsAStaleReportID: the resolution is checked against the
// conflicts found now, because the two mods may have changed since the report was
// listed.
func TestResolveConflictReportsAStaleReportID(t *testing.T) {
	_, app := modWorkspace(t)

	err := app.Mods.ResolveConflict("file_overlap:gone:a:b", "keep-both")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeNotFound),
		"error = %v, want NOT_FOUND for a report that is no longer reported",
		err,
	)
}

// TestReorderModsRefusesADuplicate: a duplicated entry would give the same mod two
// positions and the load order would depend on which one the file reader saw last.
func TestReorderModsRefusesADuplicate(t *testing.T) {
	_, app := modWorkspace(t)
	{
		err := app.Mods.ReorderMods(
			[]string{"a", "a"},
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "error = %v, want INVALID_ARGUMENT for a duplicated id", err)
	}
}
