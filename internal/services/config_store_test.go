package services

import (
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// provisionalConfig is a configuration before anything resolves the active game.
func provisionalConfig() *domain.Config {
	cfg := domain.DefaultConfig()

	cfg.ActiveGame = ""
	cfg.Games = map[string]domain.GameSettings{
		"": {Path: "/games/darktide", AutoDetect: true},
	}

	return &cfg
}

func TestResolveActiveGameCarriesTheProvisionalEntry(t *testing.T) {
	cfg := provisionalConfig()

	require.True(t, ResolveActiveGame(cfg), "an unresolved configuration changes")

	assert.Equal(t, "darktide", cfg.ActiveGame, "the registry default becomes the active game")

	settings, ok := cfg.GameSettingsFor("darktide")
	require.True(t, ok, "the provisional settings follow the game")
	assert.Equal(t, "/games/darktide", settings.Path, "the path is not lost")

	_, provisionalLeft := cfg.GameSettingsFor("")
	assert.False(t, provisionalLeft, "the provisional key is gone")
}

func TestResolveActiveGameNeverClobbersAnExplicitEntry(t *testing.T) {
	cfg := provisionalConfig()

	cfg.Games["darktide"] = domain.GameSettings{Path: "/games/real", AutoDetect: true}

	require.True(t, ResolveActiveGame(cfg))

	settings, ok := cfg.GameSettingsFor("darktide")
	require.True(t, ok)
	assert.Equal(t, "/games/real", settings.Path, "a real setting outranks the provisional one")

	_, provisionalLeft := cfg.GameSettingsFor("")
	assert.False(t, provisionalLeft, "the provisional key is still removed")
}

func TestResolveActiveGameFallsBackFromAnUnknownGame(t *testing.T) {
	cfg := domain.DefaultConfig()

	cfg.ActiveGame = "not-a-game"
	cfg.Games = map[string]domain.GameSettings{
		"sietch": {Path: "/games/sietch"},
	}

	require.True(t, ResolveActiveGame(&cfg))

	assert.Equal(t, "darktide", cfg.ActiveGame)

	settings, ok := cfg.GameSettingsFor("sietch")
	require.True(t, ok, "another game's settings are untouched")
	assert.Equal(t, "/games/sietch", settings.Path)
}

func TestResolveActiveGameCanonicalisesTheStoredID(t *testing.T) {
	cfg := domain.DefaultConfig()

	cfg.ActiveGame = "  Darktide  "

	require.True(t, ResolveActiveGame(&cfg))
	assert.Equal(t, "darktide", cfg.ActiveGame, "the id is stored the way the registry spells it")
}

func TestResolveActiveGameLeavesASettledConfigurationAlone(t *testing.T) {
	cfg := domain.DefaultConfig()

	cfg.ActiveGame = "darktide"
	cfg.Games = map[string]domain.GameSettings{"darktide": {Path: "/games/darktide"}}

	assert.False(t, ResolveActiveGame(&cfg), "nothing to repair")
	assert.Equal(t, "darktide", cfg.ActiveGame)

	cfg.ActiveGame = ""

	assert.False(t, ResolveActiveGame(nil), "a nil configuration is not an error")
}

func TestGameStoreHealsEveryLoad(t *testing.T) {
	store := NewGameStore(newFakeStore(provisionalConfig()))

	cfg, err := store.Load()
	require.NoError(t, err)

	assert.Equal(t, "darktide", cfg.ActiveGame)

	settings, ok := cfg.GameSettingsFor("darktide")
	require.True(t, ok, "the caller sees the path under the resolved id")
	assert.Equal(t, "/games/darktide", settings.Path)
}

func TestGameStoreHealsBeforeSaving(t *testing.T) {
	inner := newFakeStore(nil)
	store := NewGameStore(inner)

	cfg := provisionalConfig()
	require.NoError(t, store.Save(cfg))

	assert.Equal(t, "darktide", inner.saved.ActiveGame)
	assert.Equal(t, "/games/darktide", inner.saved.Games["darktide"].Path,
		"the path is written under the resolved id")

	_, provisionalLeft := inner.saved.GameSettingsFor("")
	assert.False(t, provisionalLeft, "no settings are written under the empty key")
}

func TestGameStoreSavesTheRepair(t *testing.T) {
	inner := newFakeStore(provisionalConfig())
	store := NewGameStore(inner)

	written, err := store.SaveIfChanged(provisionalConfig())
	require.NoError(t, err)
	assert.True(t, written, "the healed encoding differs from what is stored")

	assert.Equal(t, "darktide", inner.saved.ActiveGame)
}

func TestGameStoreSurfacesStoreFailures(t *testing.T) {
	inner := newFakeStore(nil)
	inner.loadErr = appErrors.New(appErrors.ErrCodeConfigIO, "boom")

	cfg, err := NewGameStore(inner).Load()
	require.Error(t, err)
	assert.Nil(t, cfg, "a failed load stays failed rather than handing back defaults")
}

func TestNewGameStorePassesThroughNil(t *testing.T) {
	assert.Nil(t, NewGameStore(nil), "an absent store stays absent")
	assert.Equal(t, "/tmp/config.json", NewGameStore(newFakeStore(nil)).Path())
}
