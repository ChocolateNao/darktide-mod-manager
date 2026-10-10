package commands

import (
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetectGameFindsTheConfiguredInstallation.
func TestDetectGameFindsTheConfiguredInstallation(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	})
	app := w.mustApp(t)

	found, err := app.Game.DetectGame()
	require.NoError(t, err, "DetectGame")

	require.Len(t, found, 1, "DetectGame = %d installations, want 1", len(found))

	assert.Equal(
		t,
		w.gameDir,
		found[0].Path,
		"DetectGame path = %q, want %q",
		found[0].Path,
		w.gameDir,
	)

	assert.Equal(
		t,
		w.modsDir,
		found[0].ModsPath,
		"DetectGame mods path = %q, want %q",
		found[0].ModsPath,
		w.modsDir,
	)
}

// TestDetectGameWithNothingToFind: "I could not find the game" is the answer a
// first-run wizard draws, so it is an empty slice rather than an error.
func TestDetectGameWithNothingToFind(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	found, err := app.Game.DetectGame()
	require.NoError(t, err, "DetectGame with nothing installed")

	require.NotNil(
		t,
		found,
		"DetectGame returned nil; the frontend cannot tell that from a failure",
	)

	assert.Len(t, found, 0, "DetectGame = %+v, want nothing", found)
}

// TestCurrentGamePrefersTheConfiguredPath.
func TestCurrentGamePrefersTheConfiguredPath(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	})
	app := w.mustApp(t)

	current, err := app.Game.CurrentGame()
	require.NoError(t, err, "CurrentGame")

	assert.Equal(
		t,
		w.gameDir,
		current.Path,
		"CurrentGame path = %q, want %q",
		current.Path,
		w.gameDir,
	)
}

// TestCurrentGameReportsNoGameAtAll.
func TestCurrentGameReportsNoGameAtAll(t *testing.T) {
	app := newWorkspace(t).mustApp(t)
	{
		_, err := app.Game.CurrentGame()
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeGameNotFound),
			"error = %v, want GAME_NOT_FOUND",
			err,
		)
	}
}

// TestValidateGamePathAcceptsAndRefuses.
func TestValidateGamePathAcceptsAndRefuses(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	app := w.mustApp(t)

	// The path is trimmed, because it is usually pasted out of a file manager
	// with a trailing separator or a stray space.
	install, err := app.Game.ValidateGamePath("  " + w.gameDir + "  ")
	require.NoError(t, err, "ValidateGamePath")

	assert.Equal(t, w.gameDir, install.Path, "ValidateGamePath kept the padding: %q", install.Path)

	assert.True(t, install.Valid, "a real game folder was reported invalid")

	empty := t.TempDir()
	{
		_, err := app.Game.ValidateGamePath(
			empty,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameInvalid,
		), "validating an empty folder error = %v, want GAME_INVALID", err)
	}

	{
		_, err := app.Game.ValidateGamePath(
			"   ",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameInvalid,
		), "validating a blank path error = %v, want GAME_INVALID", err)
	}
}

// TestSetGamePathPersistsAndRefuses. A refused path has to leave the stored one
// alone, or the next launch discovers the mistake the settings screen refused to
// make.
func TestSetGamePathPersistsAndRefuses(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	app := w.mustApp(t)

	{
		err := app.Game.SetGamePath(w.gameDir)
		require.NoError(t, err, "SetGamePath: %v", err)
	}

	current, err := app.Game.CurrentGame()
	require.NoError(t, err, "CurrentGame after SetGamePath")

	assert.Equal(
		t,
		w.gameDir,
		current.Path,
		"the stored game path is %q, want %q",
		current.Path,
		w.gameDir,
	)
	// And it reached the file, not just the in-memory configuration.
	store := newConfigStore(t, w.configPath)

	stored, err := store.Load()
	require.NoError(t, err, "reload the configuration")

	assert.Equal(
		t,
		w.gameDir,
		stored.ActiveSettings().Path,
		"the game path on disk is %q, want %q",
		stored.ActiveSettings().Path,
		w.gameDir,
	)

	{
		err := app.Game.SetGamePath(
			t.TempDir(),
		)
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameInvalid,
		), "SetGamePath with an invalid path error = %v, want GAME_INVALID", err)
	}

	stored, err = store.Load()
	require.NoError(t, err, "reload after the refusal")

	assert.Equal(
		t,
		w.gameDir,
		stored.ActiveSettings().Path,
		"a refused path overwrote the stored one: %q",
		stored.ActiveSettings().Path,
	)
}

// TestGetModsPathResolvesForTheConfiguredGame.
func TestGetModsPathResolvesForTheConfiguredGame(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	})
	app := w.mustApp(t)

	path, err := app.Game.GetModsPath(nil)
	require.NoError(t, err, "GetModsPath(nil): %v", err)

	assert.Equal(t, w.modsDir, path, "GetModsPath(nil) = %q, want %q", path, w.modsDir)

	// An explicit installation is answered from that installation rather than
	// from the configuration, which is what a settings screen comparing two
	// candidates needs.
	other, err := app.Game.ValidateGamePath(w.gameDir)
	require.NoError(t, err, "ValidateGamePath")

	path, err = app.Game.GetModsPath(other)
	require.NoError(t, err, "GetModsPath(install): %v", err)

	assert.Equal(
		t,
		other.ModsPath,
		path,
		"GetModsPath(install) = %q, want %q",
		path,
		other.ModsPath,
	)
}

// TestSupportedGamesAnswersWithoutAnInstallation.
//
// The registry is a separate question from the filesystem: a settings screen
// offers the games it can manage whether or not any of them are installed, so
// listing must not fail just because nothing is installed yet.
func TestSupportedGamesAnswersWithoutAnInstallation(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	games, err := app.Game.SupportedGames()
	require.NoError(t, err, "SupportedGames: %v", err)
	require.NotEmpty(t, games, "SupportedGames returned nothing at all")

	var (
		active        int
		defaultMarked bool
	)

	for _, game := range games {
		assert.NotEmpty(t, game.ID, "a game was listed without an id")
		assert.NotEmpty(t, game.DisplayName, "game %q has no display name", game.ID)

		if game.IsActive {
			active++
		}

		if game.IsDefault {
			defaultMarked = true
		}
	}

	assert.Equal(t, 1, active, "SupportedGames marked %d games active, want 1", active)
	assert.True(t, defaultMarked, "SupportedGames marked no game as the default")
}

// TestUseGameSwitchesAndRefusesUnknownIDs.
func TestUseGameSwitchesAndRefusesUnknownIDs(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)
	store := newConfigStore(t, w.configPath)

	{
		err := app.Game.UseGame(" Darktide ")
		require.NoError(t, err, "UseGame: %v", err)
	}

	// The switch reached the file: a restart must come up on the same game,
	// not on whatever the configuration happened to say before.
	stored, err := store.Load()
	require.NoError(t, err, "reload the configuration")

	assert.Equal(
		t,
		"darktide",
		stored.ActiveGame,
		"the active game on disk is %q, want %q",
		stored.ActiveGame,
		"darktide",
	)

	games, err := app.Game.SupportedGames()
	require.NoError(t, err, "SupportedGames after UseGame")

	var active string

	for _, game := range games {
		if game.IsActive {
			active = game.ID
		}
	}

	assert.Equal(t, "darktide", active, "SupportedGames active = %q, want darktide", active)

	{
		err := app.Game.UseGame("nope")
		require.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameNotFound,
		), "UseGame with an unknown id error = %v, want GAME_NOT_FOUND", err)
	}

	stored, err = store.Load()
	require.NoError(t, err, "reload after the refusal")

	assert.Equal(
		t,
		"darktide",
		stored.ActiveGame,
		"a refused switch changed the stored game: %q",
		stored.ActiveGame,
	)
}

// TestGetModsPathWithNoGameReportsNoGame.
func TestGetModsPathWithNoGameReportsNoGame(t *testing.T) {
	app := newWorkspace(t).mustApp(t)
	{
		_, err := app.Game.GetModsPath(
			nil,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeGameNotFound,
		), "error = %v, want GAME_NOT_FOUND", err)
	}
}
