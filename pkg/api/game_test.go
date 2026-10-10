package api

import (
	"context"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/games/gamestest"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// builtinsRegistered puts the shipped games in the process-wide registry and
// leaves them there after the test. The first game registered owns the default,
// so this must never run against a registry that already holds a stub.
func builtinsRegistered(t *testing.T) {
	t.Helper()

	require.NoError(t, games.EnsureBuiltins(), "register builtins")
}

// registerSecondGame puts a second game in the process-wide registry for the
// duration of one test and restores the builtins afterwards, so the default id
// never moves between tests. The builtins are registered first: the first
// game registered owns the default.
func registerSecondGame(t *testing.T, id string) {
	t.Helper()

	builtinsRegistered(t)
	require.NoError(t, games.Register(gamestest.Def{IDValue: id}), "register %q", id)

	t.Cleanup(func() {
		games.Reset()

		_ = games.EnsureBuiltins()
	})
}

func TestSupportedGamesMarksTheDefaultAndActiveGame(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	client, err := NewClient(context.Background(), w.options()...)
	require.NoError(t, err, "new client")

	t.Cleanup(func() { _ = client.Close() })

	infos, err := client.SupportedGames()
	require.NoError(t, err, "supported games")
	require.NotEmpty(t, infos, "the registry always offers at least one game")

	var darktide *GameInfo

	for i := range infos {
		if infos[i].ID == "darktide" {
			darktide = &infos[i]
		}
	}

	require.NotNil(t, darktide, "darktide is a builtin; the list must contain it")
	assert.True(t, darktide.IsDefault, "the first registered game is the default")
	assert.True(t, darktide.IsActive, "a fresh configuration starts on the default")
	assert.NotEmpty(t, darktide.DisplayName, "presentation for the switcher")
}

func TestWithGameRejectsAnUnregisteredGame(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)

	opts := append(w.options(), WithGame("nope"))
	_, err := NewClient(context.Background(), opts...)
	require.Error(t, err, "an unknown game id must not silently fall back")

	assert.Equal(t, appErrors.ErrCodeInvalidArgument, appErrors.CodeOf(err),
		"code (err %v)", err)
}

func TestWithGameStartsOnTheNamedGameWithoutTouchingTheFile(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)
	registerSecondGame(t, "sietch")

	opts := append(w.options(), WithGame("SIETCH"))
	client, err := NewClient(context.Background(), opts...)
	require.NoError(t, err, "new client")

	t.Cleanup(func() { _ = client.Close() })

	infos, err := client.SupportedGames()
	require.NoError(t, err, "supported games")

	for _, info := range infos {
		switch info.ID {
		case "sietch":
			assert.True(t, info.IsActive, "WithGame picked this game")
			assert.False(t, info.IsDefault, "the default is still the builtin")
		case "darktide":
			assert.False(t, info.IsActive, "WithGame overrode the default")
		}
	}

	// The override is in-memory only: a start-up that fails afterwards must
	// not have rewritten the user's file.
	stored, err := storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, err, "reload the file")

	assert.NotEqual(t, "sietch", stored.ActiveGame,
		"start-up must not write the WithGame choice back")
}

func TestUseGamePersistsTheSwitchAndRefusesUnknownIds(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, nil)
	registerSecondGame(t, "sietch")

	client, err := NewClient(context.Background(), w.options()...)
	require.NoError(t, err, "new client")

	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()

	err = client.UseGame(ctx, " SIETCH ")
	require.NoError(t, err, "switch to a registered id")

	stored, err := storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, err, "reload the file")

	assert.Equal(t, "sietch", stored.ActiveGame, "the switch reached the disk")

	infos, err := client.SupportedGames()
	require.NoError(t, err, "supported games")

	for _, info := range infos {
		assert.Equal(t, info.ID == "sietch", info.IsActive,
			"active flag for %q", info.ID)
	}

	err = client.UseGame(ctx, "nope")
	require.Error(t, err, "switching to an unknown game must fail")

	assert.Equal(t, appErrors.ErrCodeGameNotFound, appErrors.CodeOf(err),
		"code (err %v)", err)

	stored, err = storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, err, "reload the file")

	assert.Equal(t, "sietch", stored.ActiveGame, "a failed switch changed the file")
}

// TestTheGamePathSurvivesSwitchingAwayAndBack is the first-run flow a user
// actually performs: set the install path before ever naming a game, switch to
// another game, switch back. The path used to vanish on the way back, because
// it was filed under the empty id the unresolved active_game stood for and
// nothing ever looked there again.
func TestTheGamePathSurvivesSwitchingAwayAndBack(t *testing.T) {
	w := newWireWorkspace(t)
	w.makeGame(t)
	// Deliberately no saveConfig: a brand new installation has no config file
	// and no active_game, which is the state the report describes.
	registerSecondGame(t, "sietch")

	client, err := NewClient(context.Background(), w.options()...)
	require.NoError(t, err, "new client")

	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()

	require.NoError(t, client.SetConfigValue(ctx, "game.path", w.gameDir),
		"pin the install path for the default game")

	stored, err := storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, err, "reload the file")

	assert.Equal(t, "darktide", stored.ActiveGame, "the default game is written down")
	assert.Equal(t, w.gameDir, stored.Games["darktide"].Path, "the path sits under it")

	require.NoError(t, client.UseGame(ctx, "sietch"), "switch away")
	require.NoError(t, client.UseGame(ctx, "darktide"), "switch back")

	got, err := client.GetConfigValue(ctx, "game.path")
	require.NoError(t, err, "read the path back")
	assert.Equal(t, w.gameDir, got, "the path survived the round trip")

	stored, err = storage.NewConfigStore(w.configPath).Load()
	require.NoError(t, err, "reload the file")

	_, provisional := stored.GameSettingsFor("")
	assert.False(t, provisional, "nothing is left under the empty key")
}

func TestHookPermissionsMergesModOverGameOverDefault(t *testing.T) {
	w := newWireWorkspace(t)
	w.saveConfig(t, func(cfg *domain.Config) {
		cfg.Hooks.Permissions.Default = domain.DefaultScriptPermissions()
		cfg.Hooks.Permissions.Games = map[string]domain.ScriptPermissions{
			"darktide": {FSWrite: true, TimeoutSeconds: 5},
		}
		cfg.Hooks.Permissions.Mods = map[string]domain.ScriptPermissions{
			"colourful": {ConfigWrite: true},
		}
	})

	client, err := NewClient(context.Background(), w.options()...)
	require.NoError(t, err, "new client")

	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()

	perms, err := client.HookPermissions(ctx, "darktide", "colourful")
	require.NoError(t, err, "permissions for a scoped mod")

	assert.True(t, perms.FSRead, "the default scope survives the merge")
	assert.True(t, perms.FSWrite, "the game scope wins over the default")
	assert.True(t, perms.ConfigWrite, "the mod scope wins over the game")
	assert.Equal(t, 5, perms.TimeoutSeconds, "the game's timeout cap")

	// A mod with no scope of its own still sees the game's.
	plain, err := client.HookPermissions(ctx, "darktide", "unscoped")
	require.NoError(t, err, "permissions for an unscoped mod")

	assert.True(t, plain.FSWrite, "the game scope applies without a mod scope")
	assert.False(t, plain.ConfigWrite, "no mod scope means no mod grant")

	// A game with no scope gets only the default, whatever the mod asks for.
	other, err := client.HookPermissions(ctx, "sietch", "colourful")
	require.NoError(t, err, "permissions under another game")

	assert.False(t, other.FSWrite, "the game scope does not leak across games")
	assert.True(t, other.ConfigWrite, "the mod scope follows the mod, not the game")
}
