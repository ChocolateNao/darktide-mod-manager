package commands

import (
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetConfigHandsOutACopy(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t, func(cfg *domain.Config) {
		cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	})
	app := w.mustApp(t)

	first, err := app.Config.GetConfig()
	require.NoError(t, err, "GetConfig")

	require.Equal(
		t,
		w.gameDir,
		first.ActiveSettings().Path,
		"game path = %q, want %q",
		first.ActiveSettings().Path,
		w.gameDir,
	)

	first.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "/somewhere/else" })
	first.Cache.MaxSizeGB = 999

	second, err := app.Config.GetConfig()
	require.NoError(t, err, "GetConfig again")

	assert.Equal(
		t,
		w.gameDir,
		second.ActiveSettings().Path,
		"editing the returned config changed the stored path to %q",
		second.ActiveSettings().Path,
	)

	assert.NotEqual(
		t,
		999,
		second.Cache.MaxSizeGB,
		"editing the returned config changed the stored cache budget",
	)
}

func TestSetConfigValueRoundTrips(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	{
		err := app.Config.SetConfigValue("  cache.max_size_gb  ", "7")
		require.NoError(t, err, "SetConfigValue: %v", err)
	}

	value, err := app.Config.GetConfigValue("cache.max_size_gb")
	require.NoError(t, err, "GetConfigValue")

	assert.Equal(t, "7", value, "read back %q, want \"7\"", value)

	cfg, err := app.Config.GetConfig()
	require.NoError(t, err, "GetConfig")

	assert.Equal(
		t,
		7,
		cfg.Cache.MaxSizeGB,
		"the configuration in memory reports %d GB, want 7",
		cfg.Cache.MaxSizeGB,
	)
}

func TestSetConfigValueRefusesABadValue(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	before, err := app.Config.GetConfig()
	require.NoError(t, err, "GetConfig")

	refused := app.Config.SetConfigValue("advanced.enable_experimental", "maybe")
	require.True(
		t,
		appErrors.IsAppError(refused, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT",
		refused,
	)

	assert.Contains(
		t,
		refused.Error(),
		"advanced.enable_experimental",
		"the error does not name the setting: %v",
		refused,
	)

	after, err := app.Config.GetConfig()
	require.NoError(t, err, "GetConfig after the refusal")

	assert.Equal(
		t,
		before.Advanced.EnableExperimental,
		after.Advanced.EnableExperimental,
		"a refused value was written anyway",
	)
}

func TestGetConfigValueRefusesAnUnknownOrBlankKey(t *testing.T) {
	app := newWorkspace(t).mustApp(t)
	{
		_, err := app.Config.GetConfigValue(
			"no.such.setting",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeNotFound,
		), "error = %v, want NOT_FOUND", err)
	}

	{
		_, err := app.Config.GetConfigValue(
			"   ",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "error = %v, want INVALID_ARGUMENT for a blank key", err)
	}
}

func TestSourceSettingsAreReachableThroughDottedKeys(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	{
		err := app.Config.SetConfigValue("sources.vortex.api_key", "secret-token")
		require.NoError(t, err, "storing a credential: %v", err)
	}

	if _, err := app.Config.GetConfigValue("sources.vortex.api_key"); err == nil {
		require.Error(t, err, "a stored credential was read back")
	} else {
		assert.Contains(
			t,
			strings.ToLower(err.Error()),
			"credential",
			"the refusal does not say why: %v",
			err,
		)
	}

	{
		err := app.Config.SetConfigValue("sources.local.paths", "/one, /two")
		require.NoError(t, err, "storing source paths: %v", err)
	}

	paths, err := app.Config.GetConfigValue("sources.local.paths")
	require.NoError(t, err, "reading source paths")

	assert.Equal(
		t,
		"/one,/two",
		paths,
		"source paths = %q, want the two paths joined by commas",
		paths,
	)

	{
		err := app.Config.SetConfigValue("sources.vortex.options.max_releases", "5")
		require.NoError(t, err, "storing a source option: %v", err)
	}

	option, err := app.Config.GetConfigValue("sources.vortex.options.max_releases")
	require.NoError(t, err, "reading a source option")

	assert.Equal(t, "5", option, "source option = %q, want \"5\"", option)
}

func TestConfigKeysListsEverySettingAndTheSourcePatterns(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	keys, err := app.Config.ConfigKeys()
	require.NoError(t, err, "ConfigKeys")

	require.NotNil(t, keys, "ConfigKeys returned nil")

	joined := strings.Join(keys, " ")
	// Patterns are listed by name; the set of sources is the configuration's
	// business.
	for _, want := range []string{
		"cache.max_size_gb", "game.path",
		"sources.<id>.enabled", "sources.<id>.api_key", "sources.<id>.paths",
	} {
		assert.Contains(t, joined, want, "ConfigKeys omits %q; got %v", want, keys)
	}
}

func TestConfigPathMatchesTheFile(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)

	path, err := app.Config.ConfigPath()
	require.NoError(t, err, "ConfigPath")

	assert.Equal(t, w.configPath, path, "ConfigPath = %q, want %q", path, w.configPath)
}

func TestUpdateConfigAppliesDefaultsAndSaves(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)

	{
		err := app.Config.UpdateConfig(&api.Config{})
		require.NoError(t, err, "UpdateConfig: %v", err)
	}

	stored, err := app.Config.GetConfig()
	require.NoError(t, err, "GetConfig")

	assert.Equal(
		t,
		domain.DefaultConfig().Cache.MaxSizeGB,
		stored.Cache.MaxSizeGB,
		"cache budget = %d, want the default %d",
		stored.Cache.MaxSizeGB,
		domain.DefaultConfig().Cache.MaxSizeGB,
	)

	assert.Equal(
		t,
		domain.DefaultConfig().Hooks.DefaultTimeout,
		stored.Hooks.DefaultTimeout,
		"hook timeout = %d, want the default %d",
		stored.Hooks.DefaultTimeout,
		domain.DefaultConfig().Hooks.DefaultTimeout,
	)

	{
		err := app.Config.UpdateConfig(nil)
		assert.Error(t, err, "UpdateConfig accepted a nil configuration")
	}

	reloaded, err := newConfigStore(t, w.configPath).Load()
	require.NoError(t, err, "reload the configuration")

	assert.Equal(
		t,
		stored.Cache.MaxSizeGB,
		reloaded.Cache.MaxSizeGB,
		"the file reports %d GB, want %d",
		reloaded.Cache.MaxSizeGB,
		stored.Cache.MaxSizeGB,
	)
}
