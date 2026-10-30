package api

import (
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stringConfigFields are the settings a caller sets by typing text.
var stringConfigFields = map[string]string{
	"active_game":                        "darktide",
	"game.path":                          "/games/darktide",
	"game.source":                        "steam",
	"game.mods_folder_override":          "/games/darktide/mods",
	"storage.path":                       "/var/lib/dmm",
	"cache.path":                         "/var/cache/dmm",
	"auto_update.last_check":             "2026-03-01T12:00:00Z",
	"ui.theme":                           "dark",
	"ui.language":                        "en",
	"logging.level":                      "warn",
	"logging.directory":                  "/var/log/dmm",
	"storage.backup_retention_days":      "",
	"cache.max_size_gb":                  "",
	"cache.max_age_days":                 "",
	"cache.verify_hashes":                "",
	"game.auto_detect":                   "",
	"hooks.default_timeout":              "",
	"hooks.max_timeout":                  "",
	"auto_update.enabled":                "",
	"auto_update.check_interval_hours":   "",
	"auto_update.download_automatically": "",
	"auto_update.notify_on_update":       "",
	"ui.compact_mode":                    "",
	"advanced.enable_experimental":       "",
}

// boolConfigFields maps each boolean setting to the value it should read back as.
var boolConfigFields = map[string]bool{
	"game.auto_detect":                   true,
	"cache.verify_hashes":                false,
	"auto_update.enabled":                false,
	"auto_update.download_automatically": true,
	"auto_update.notify_on_update":       true,
	"ui.compact_mode":                    false,
	"advanced.enable_experimental":       true,
	"logging.compress":                   true,
	"security.allow_action_commands":     false,
}

// intConfigFields maps each numeric setting to the value it should read back as.
var intConfigFields = map[string]string{
	"storage.backup_retention_days":    "7",
	"cache.max_size_gb":                "25",
	"cache.max_age_days":               "90",
	"hooks.default_timeout":            "45s",
	"hooks.max_timeout":                "5m0s",
	"auto_update.check_interval_hours": "12",
	"logging.max_size_mb":              "25",
	"logging.max_backups":              "3",
	"logging.max_age_days":             "90",
}

// configPtr returns a pointer to a copy of cfg so a test can hand a store its
// own configuration without aliasing the value the test still holds.
func configPtr(cfg domain.Config) *domain.Config {
	return &cfg
}

// configStoreStub is an in-memory services.ConfigStore: Load answers from the struct,
// Save counts writes, and last reports the stored configuration. The mutex matters
// because a client may finish work on a goroutine while the test reads the store back.
type configStoreStub struct {
	loadErr error
	cfg     *domain.Config
	saves   int
	mu      sync.Mutex
}

func (s *configStoreStub) Path() string { return "" }

func (s *configStoreStub) Load() (*domain.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loadErr != nil {
		return nil, s.loadErr
	}

	return s.cfg, nil
}

func (s *configStoreStub) Save(cfg *domain.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.saves++
	s.cfg = cfg

	return nil
}

// SaveIfChanged counts as a save whenever asked to write: the client calls it on
// every persist path.
func (s *configStoreStub) SaveIfChanged(cfg *domain.Config) (bool, error) {
	return true, s.Save(cfg)
}

// last reports the stored configuration: the file's contents before the first save,
// the saved value after.
func (s *configStoreStub) last() *domain.Config {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cfg
}

// The three tables and configFields must cover each other exactly: otherwise adding a
// setting and forgetting to test it — or testing a key that no longer exists — passes.
func TestEveryConfigFieldIsCoveredByATestTable(t *testing.T) {
	covered := make(map[string]string)
	for key := range stringConfigFields {
		covered[key] = "string"
	}

	for key := range boolConfigFields {
		covered[key] = "bool"
	}

	for key := range intConfigFields {
		covered[key] = "int"
	}

	for _, field := range configFields {
		kind, ok := covered[field.key]
		if !ok {
			assert.Fail(t, "configuration key %q is not in any test table", field.key)
			continue
		}

		switch kind {
		case "string":
			if stringConfigFields[field.key] == "" {
				assert.Fail(t,
					"configuration key %q is tested as a string but has no sample value",
					field.key,
				)
			}
		case "int":
			if intConfigFields[field.key] == "" {
				assert.Fail(t,
					"configuration key %q is tested as a number but has no sample value",
					field.key,
				)
			}
		case "bool":
			if _, ok := boolConfigFields[field.key]; !ok {
				assert.Fail(
					t,
					"configuration key %q is tested as a boolean but has no sample",
					field.key,
				)
			}
		}
	}

	for key := range covered {
		var found bool

		for _, field := range configFields {
			if field.key == key {
				found = true
				break
			}
		}

		assert.True(
			t,
			found,
			"a test table names configuration key %q, which no longer exists",
			key,
		)
	}
	// A duplicated key would make the tables disagree with themselves.
	seen := make(map[string]bool, len(configFields))
	for _, field := range configFields {
		assert.False(
			t,
			seen[field.key],
			"configuration key %q appears twice in the table",
			field.key,
		)

		seen[field.key] = true
	}
}

func TestConfigValueReadsEverySetting(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())

	for key, want := range stringConfigFields {
		if want == "" {
			continue
		}

		require.NoError(t, SetConfigValue(cfg, key, want), "SetConfigValue(%q)", key)

		got, err := ConfigValue(cfg, key)
		require.NoError(t, err, "ConfigValue(%q)", key)

		assert.Equal(t, want, got, "ConfigValue(%q)", key)
	}
	// Each boolean is written twice, once as a person types it and once as a
	// machine writes it, and must read back as the table says.
	for key, want := range boolConfigFields {
		for _, value := range []bool{want, !want} {
			for _, spelling := range boolSpellings(value) {
				require.NoError(
					t,
					SetConfigValue(cfg, key, spelling),
					"SetConfigValue(%q, %q)",
					key,
					spelling,
				)

				got, err := ConfigValue(cfg, key)
				require.NoError(t, err, "ConfigValue(%q)", key)

				assert.Equal(t, boolText(value), got,
					"ConfigValue(%q) after setting %q",
					key,
					spelling,
				)
			}
		}
	}

	for key, want := range intConfigFields {
		require.NoError(t, SetConfigValue(cfg, key, want), "SetConfigValue(%q)", key)

		got, err := ConfigValue(cfg, key)
		require.NoError(t, err, "ConfigValue(%q)", key)

		assert.Equal(t, want, got, "ConfigValue(%q)", key)
	}
}

// boolSpellings returns the ways a caller might write one boolean value.
func boolSpellings(value bool) []string {
	if value {
		return []string{"true", "yes", "on"}
	}

	return []string{"false", "no", "off"}
}

func boolText(v bool) string {
	if v {
		return "true"
	}

	return "false"
}

func TestConfigValueAcceptsEveryBooleanSpelling(t *testing.T) {
	// Someone typing into a settings field writes "yes" and "no" as often as "true";
	// refusing them would be pedantry that generates bug reports.
	cfg := configPtr(domain.DefaultConfig())
	for _, value := range []string{"true", "TRUE", " yes ", "on", "1"} {
		require.NoError(
			t,
			SetConfigValue(cfg, "game.auto_detect", value),
			"SetConfigValue with %q",
			value,
		)

		got, _ := ConfigValue(cfg, "game.auto_detect")
		assert.Equal(t, "true", got, "game.auto_detect after setting %q", value)
	}

	for _, value := range []string{"false", "No", "off", "0"} {
		require.NoError(
			t,
			SetConfigValue(cfg, "game.auto_detect", value),
			"SetConfigValue with %q",
			value,
		)

		got, _ := ConfigValue(cfg, "game.auto_detect")
		assert.Equal(t, "false", got, "game.auto_detect after setting %q", value)
	}

	for _, value := range []string{"maybe", "", "2", "t", "y"} {
		err := SetConfigValue(cfg, "game.auto_detect", value)
		require.Error(t, err, "SetConfigValue accepted %q as a boolean", value)

		assert.True(
			t,
			IsAppError(err, ErrCodeInvalidArgument),
			"SetConfigValue with %q returned %v, want invalid-argument",
			value,
			err,
		)
	}
}

func TestSetConfigValueRefusesNumbersOutOfRange(t *testing.T) {
	// A cache limit of -1 would read as "no limit" to the cache manager, which
	// is not what someone typing -1 into a size field meant.
	cfg := configPtr(domain.DefaultConfig())

	cases := map[string][]string{
		"cache.max_size_gb":                {"-1", "1000001", "1.5", "big", ""},
		"cache.max_age_days":               {"-1"},
		"storage.backup_retention_days":    {"-1", "36501"},
		"hooks.default_timeout":            {"0", "3601"},
		"hooks.max_timeout":                {"0", "86401"},
		"auto_update.check_interval_hours": {"0", "8761"},
	}
	for key, values := range cases {
		for _, value := range values {
			err := SetConfigValue(cfg, key, value)
			require.Error(t, err, "SetConfigValue(%q, %q) was accepted", key, value)

			assert.True(t, IsAppError(err, ErrCodeInvalidArgument),
				"SetConfigValue(%q, %q) returned %v, want invalid-argument",
				key,
				value,
				err,
			)

			assert.True(
				t,
				strings.Contains(err.Error(), key) || strings.Contains(err.Error(), value),
				"SetConfigValue(%q, %q) said %q, which names neither the key nor the value",
				key,
				value,
				err,
			)
		}
	}
	// The edges are the boundaries themselves: zero means "no limit" for a cache
	// axis, which is a legitimate setting.
	for key, value := range map[string]string{
		"cache.max_size_gb":                "0",
		"cache.max_age_days":               "0",
		"storage.backup_retention_days":    "0",
		"hooks.default_timeout":            "1",
		"hooks.max_timeout":                "1",
		"auto_update.check_interval_hours": "1",
	} {
		require.NoError(
			t,
			SetConfigValue(cfg, key, value),
			"SetConfigValue(%q, %q) was refused",
			key,
			value,
		)
	}
}

func TestConfigValueNormalisesTheKey(t *testing.T) {
	// A settings screen should not have to know how a caller spelled a key.
	cfg := configPtr(domain.DefaultConfig())
	require.NoError(
		t,
		SetConfigValue(cfg, "  Game.Path  ", "/games/darktide"),
		"SetConfigValue with a padded, mixed-case key",
	)

	got, err := ConfigValue(cfg, "GAME.PATH")
	require.NoError(t, err, "ConfigValue with a different spelling")

	assert.Equal(t, "/games/darktide", got, "game.path reads, want the value that was set")
}

func TestConfigValueReportsUnknownKeys(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())
	for _, key := range []string{"nope", "game.nope", "game.path.deeper", "sources", "sources."} {
		_, err := ConfigValue(cfg, key)
		assert.True(
			t,
			IsAppError(err, ErrCodeNotFound),
			"ConfigValue(%q) returned %v, want not-found",
			key,
			err,
		)

		err = SetConfigValue(cfg, key, "x")
		assert.True(
			t,
			IsAppError(err, ErrCodeNotFound),
			"SetConfigValue(%q) returned %v, want not-found",
			key,
			err,
		)
	}
	// A blank key is a bad argument, not a missing setting: the caller made
	// a mistake, not a request the manager cannot answer.
	for _, key := range []string{"", "   ", "\t"} {
		_, err := ConfigValue(cfg, key)
		assert.True(
			t,
			IsAppError(err, ErrCodeInvalidArgument),
			"ConfigValue(%q) returned %v, want invalid-argument",
			key,
			err,
		)

		err = SetConfigValue(cfg, key, "x")
		assert.True(
			t,
			IsAppError(err, ErrCodeInvalidArgument),
			"SetConfigValue(%q) returned %v, want invalid-argument",
			key,
			err,
		)
	}
}

func TestConfigValueRejectsANilConfig(t *testing.T) {
	_, err := ConfigValue(nil, "game.path")
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"ConfigValue on a nil config returned %v, want invalid-argument",
		err,
	)

	err = SetConfigValue(nil, "game.path", "/x")
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"SetConfigValue on a nil config returned %v, want invalid-argument",
		err,
	)
}

func TestSourceConfigKeysReadAndWrite(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())
	require.NoError(t, SetConfigValue(cfg, "sources.vortex.enabled", "false"), "SetConfigValue")

	got, _ := ConfigValue(cfg, "sources.vortex.enabled")
	assert.Equal(t, "false", got, "sources.vortex.enabled reads")
	// The source block is created by the write, which is how a block appears in a
	// hand-written configuration at all.
	block, ok := cfg.SourceConfigFor("vortex")
	require.True(t, ok, "no configuration block for vortex after writing to it")

	assert.False(t, block.Enabled, "the block was created enabled even though the write said false")

	require.NoError(t, SetConfigValue(cfg, "sources.vortex.paths", "/a, /b\n/c,"), "SetConfigValue")

	got, _ = ConfigValue(cfg, "sources.vortex.paths")
	assert.Equal(t, "/a,/b,/c", got, "sources.vortex.paths reads, want the three paths joined")
	// The block read before the write is a copy, so the paths are re-read here.
	// Reading the stale copy is exactly the mistake this catches.
	require.NoError(t, SetConfigValue(cfg, "sources.vortex.paths", "/a, /b\n/c,"), "SetConfigValue")

	fresh, _ := cfg.SourceConfigFor("vortex")
	want := []string{
		"/a",
		"/b",
		"/c",
	}
	assert.Equal(t, strings.Join(want, ","), strings.Join(fresh.Paths, ","), "the stored paths")

	require.NoError(t, SetConfigValue(
		cfg,
		"sources.vortex.options.api_base",
		"https://api.invalid",
	), "SetConfigValue")

	got, _ = ConfigValue(cfg, "sources.vortex.options.api_base")
	assert.Equal(t, "https://api.invalid", got, "the provider option reads")

	_, err := ConfigValue(
		cfg,
		"sources.vortex.options.missing",
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeNotFound),
		"reading an unset provider option returned %v, want not-found",
		err,
	)
}

func TestSourceConfigKeyKeepsTheSourceIDVerbatim(t *testing.T) {
	// The block's key in the configuration file is the source id; folding its case
	// here would write one block while the registry looked up another.
	cfg := configPtr(domain.DefaultConfig())
	require.NoError(t, SetConfigValue(cfg, "sources.  Vortex  .enabled", "true"), "SetConfigValue")

	_, ok := cfg.SourceConfigFor("Vortex")
	assert.True(
		t,
		ok,
		"configuration blocks are %v, want one keyed by the id as written",
		cfg.Sources,
	)
}

func TestSourceConfigKeyIsCaseInsensitiveWhenReading(t *testing.T) {
	// Reading, unlike writing, goes through SourceConfigFor, which folds case, so
	// a caller asking about "vortex" finds a block written as "Vortex".
	cfg := configPtr(domain.DefaultConfig())
	cfg.Sources["Vortex"] = domain.SourceConfig{Enabled: false, APIKey: "k"}

	got, err := ConfigValue(cfg, "sources.vortex.enabled")
	require.NoError(t, err, "ConfigValue")

	assert.Equal(t, "false", got, "sources.vortex.enabled reads, want the block's own false")
}

func TestSourceConfigKeyReportsAnUnknownSource(t *testing.T) {
	// Answering "enabled: true" for a typo'd source id would be worse than failing:
	// the caller would believe it had configured something.
	cfg := configPtr(domain.DefaultConfig())

	_, err := ConfigValue(cfg, "sources.nosuch.enabled")
	assert.True(
		t,
		IsAppError(err, ErrCodeNotFound),
		"ConfigValue for an unknown source returned %v, want not-found",
		err,
	)

	assert.Contains(t, err.Error(), "nosuch", "the error does not name the source")

	err = SetConfigValue(
		cfg,
		"sources.nosuch.enabled",
		"maybe",
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"SetConfigValue with an unusable value returned %v, want invalid-argument",
		err,
	)
}

func TestSourceConfigKeyRejectsAnUnknownField(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())
	require.NoError(t, SetConfigValue(cfg, "sources.vortex.enabled", "true"), "SetConfigValue")

	_, err := ConfigValue(cfg, "sources.vortex.nonsense")
	assert.True(
		t,
		IsAppError(err, ErrCodeNotFound),
		"ConfigValue for an unknown source field returned %v, want not-found",
		err,
	)

	err = SetConfigValue(cfg, "sources.vortex.nonsense", "x")
	assert.True(
		t,
		IsAppError(err, ErrCodeNotFound),
		"SetConfigValue for an unknown source field returned %v, want not-found",
		err,
	)
}

func TestSourceConfigKeyRejectsABlankSourceID(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())

	cfg.Sources["   "] = domain.SourceConfig{Enabled: true}
	for _, key := range []string{"sources..enabled", "sources.   .enabled"} {
		_, err := ConfigValue(cfg, key)
		assert.True(t, IsAppError(err, ErrCodeNotFound),
			"ConfigValue(%q) returned %v, want not-found — a blank id is not a source",
			key,
			err,
		)
	}
}

// A credential is writable and not readable — the asymmetry that stops a UI
// rendering one into a diagnostics dump.
func TestSourceConfigKeyRefusesToReadAnAPIKey(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())
	require.NoError(t, SetConfigValue(cfg, "sources.vortex.api_key", "secret"), "SetConfigValue")

	block, _ := cfg.SourceConfigFor("vortex")
	assert.Equal(t, "secret", block.APIKey, "the api key was not stored; block is")

	for _, key := range []string{"sources.vortex.api_key", "sources.vortex.API_KEY"} {
		got, err := ConfigValue(cfg, key)
		require.Error(
			t,
			err,
			"ConfigValue(%q) returned %q; a credential must not be readable",
			key,
			got,
		)

		assert.True(
			t,
			IsAppError(err, ErrCodeNotFound),
			"ConfigValue(%q) returned %v, want not-found",
			key,
			err,
		)

		assert.Contains(t, err.Error(), "credential", "the refusal does not explain itself")
	}
}

func TestConfigKeysListsEverythingAddressable(t *testing.T) {
	keys := ConfigKeys()

	have := make(map[string]bool, len(keys))
	for _, key := range keys {
		have[key] = true
	}

	for _, field := range configFields {
		assert.True(t, have[field.key], "ConfigKeys omits %q", field.key)
	}

	for _, documented := range []string{"sources.<id>.enabled", "sources.<id>.api_key", "sources.<id>.paths"} {
		assert.True(
			t,
			have[documented],
			"ConfigKeys omits the documented source pattern %q",
			documented,
		)
	}
	// The list is a fresh slice, so a caller sorting it cannot corrupt it.
	keys[0] = "tampered"

	assert.NotEqual(t, "tampered", ConfigKeys()[0], "ConfigKeys handed out its own slice")
}

func TestMergeSourceBlockLeavesTheFileAlone(t *testing.T) {
	// A caller supplying one source's token must not silently unconfigure the
	// others, nor reset the file's own values for that source.
	existing := domain.SourceConfig{
		Enabled: false,
		APIKey:  "from-file",
		Paths:   []string{"/from/file"},
		Options: map[string]string{"api_base": "https://from.file", "topic": "mods"},
	}
	override := domain.SourceConfig{
		Enabled: true,
		APIKey:  "from-option",
		Paths:   []string{"/from/option"},
		Options: map[string]string{"topic": "darktide"},
	}

	merged := mergeSourceBlock(existing, override, true)
	assert.True(t, merged.Enabled, "an explicit enabled:true did not win")

	assert.Equal(t, "from-option", merged.APIKey, "api key, want the option's")

	assert.Len(t, merged.Paths, 2, "paths, want the file's and the option's appended")

	assert.Equal(
		t,
		"https://from.file",
		merged.Options["api_base"],
		"the file's provider option was dropped",
	)

	assert.Equal(
		t,
		"darktide",
		merged.Options["topic"],
		"the option's provider override did not win",
	)

	// With nothing stated, every field keeps the file's value.
	untouched := mergeSourceBlock(existing, domain.SourceConfig{APIKey: "x"}, false)
	assert.False(
		t,
		untouched.Enabled,
		"an override that never mentioned enabled turned the source on",
	)

	assert.Equal(t, "x", untouched.APIKey, "the override's api key was not applied")

	assert.Equal(
		t,
		"mods",
		untouched.Options["topic"],
		"an override with no options disturbed the file's",
	)

	assert.NotNil(t, untouched.Options, "merge built a new options map when the override had none")
	// The file's own map must not be written through: merge rebuilds it, so the
	// block that came straight out of the loaded file stays exactly as read.
	assert.Equal(
		t,
		"mods",
		existing.Options["topic"],
		"the merge wrote into the file's own options map",
	)

	assert.Equal(
		t,
		"https://from.file",
		existing.Options["api_base"],
		"the merge wrote into the file's own options map",
	)

	assert.Len(t, existing.Options, 2, "a merge gained a key in the file's own options")
}

func TestGameKeysReadAndWritePerGameSettings(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())

	// A game with no entry answers with the defaults, not a failure:
	// auto_detect is what a fresh installation would do.
	got, err := ConfigValue(cfg, "games.sietch.auto_detect")
	require.NoError(t, err, "ConfigValue for an unconfigured game")
	assert.Equal(t, "true", got, "the default answer for a game with no entry")

	require.NoError(t, SetConfigValue(cfg, "games.sietch.path", "/games/sietch"),
		"SetConfigValue games.sietch.path")
	require.NoError(t, SetConfigValue(cfg, "games.sietch.auto_detect", "no"),
		"SetConfigValue games.sietch.auto_detect")
	require.NoError(t, SetConfigValue(cfg, "games.darktide.path", "/games/darktide"),
		"SetConfigValue for a second game")

	got, err = ConfigValue(cfg, "games.sietch.path")
	require.NoError(t, err, "read back sietch path")
	assert.Equal(t, "/games/sietch", got, "sietch's own path")

	got, err = ConfigValue(cfg, "games.sietch.auto_detect")
	require.NoError(t, err, "read back sietch auto_detect")
	assert.Equal(t, "false", got, "sietch's own auto_detect")

	got, err = ConfigValue(cfg, "games.darktide.path")
	require.NoError(t, err, "read back darktide path")
	assert.Equal(t, "/games/darktide", got, "the games do not share a block")

	// One game's switch must not move another's.
	got, err = ConfigValue(cfg, "games.darktide.auto_detect")
	require.NoError(t, err, "read back darktide auto_detect")
	assert.Equal(t, "true", got, "darktide kept the default")

	err = SetConfigValue(cfg, "games.sietch.nope", "x")
	assert.True(t, IsAppError(err, ErrCodeNotFound),
		"an unknown game setting returned %v, want not-found", err)
}

func TestInstallMethodKeyReadsTheGameDefaultUntilOverridden(t *testing.T) {
	builtinsRegistered(t)

	cfg := configPtr(domain.DefaultConfig())

	// Darktide's own definition prefers symlinks, so an untouched setting reads
	// back as the behaviour a user will actually get.
	got, err := ConfigValue(cfg, "games.darktide.install_method")
	require.NoError(t, err, "read the default answer")
	assert.Equal(t, domain.InstallMethodSymlink, got, "the game's own preference")

	// A game this build has no definition for has nothing to fall back to.
	got, err = ConfigValue(cfg, "games.sietch.install_method")
	require.NoError(t, err, "read a game with no definition")
	assert.Empty(t, got, "no definition, no preference")

	require.NoError(t, SetConfigValue(cfg, "games.darktide.install_method", "direct"),
		"override the method")

	got, err = ConfigValue(cfg, "games.darktide.install_method")
	require.NoError(t, err, "read the override back")
	assert.Equal(t, domain.InstallMethodDirect, got, "the override wins")

	settings, ok := cfg.GameSettingsFor("darktide")
	require.True(t, ok, "the game's block exists")
	assert.Equal(t, domain.InstallMethodDirect, settings.InstallMethod,
		"the override is stored on the game's block")

	got, err = ConfigValue(cfg, "games.sietch.install_method")
	require.NoError(t, err, "the other game is untouched")
	assert.Empty(t, got, "one game's override must not leak into another's")

	err = SetConfigValue(cfg, "games.darktide.install_method", "hardlink")
	assert.True(t, IsAppError(err, ErrCodeInvalidArgument),
		"an unknown method returned %v, want invalid-argument", err)

	// Blank is how a user takes the override back.
	require.NoError(t, SetConfigValue(cfg, "games.darktide.install_method", ""),
		"clear the override")

	got, err = ConfigValue(cfg, "games.darktide.install_method")
	require.NoError(t, err, "read after clearing")
	assert.Equal(t, domain.InstallMethodSymlink, got, "clearing hands the decision back")
}

func TestPermissionKeysReadAndWriteEveryScope(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())

	require.NoError(t, SetConfigValue(cfg, "hooks.permissions.default.timeout_seconds", "7"),
		"set the default timeout cap")
	require.NoError(t, SetConfigValue(cfg, "hooks.permissions.games.darktide.fs_write", "yes"),
		"grant the game fs_write")
	require.NoError(t, SetConfigValue(cfg, "hooks.permissions.mods.betterui.env_read", "true"),
		"grant one mod env_read")

	got, err := ConfigValue(cfg, "hooks.permissions.default.timeout_seconds")
	require.NoError(t, err, "read the default timeout cap")
	assert.Equal(t, "7", got, "timeout_seconds round-trip")

	got, err = ConfigValue(cfg, "hooks.permissions.games.darktide.fs_write")
	require.NoError(t, err, "read the game scope")
	assert.Equal(t, "true", got, "fs_write round-trip")

	got, err = ConfigValue(cfg, "hooks.permissions.mods.betterui.env_read")
	require.NoError(t, err, "read the mod scope")
	assert.Equal(t, "true", got, "env_read round-trip")

	// A capability nobody granted reads false — the zero is the truthful
	// answer, not a missing setting.
	got, err = ConfigValue(cfg, "hooks.permissions.games.darktide.fs_read")
	require.NoError(t, err, "read an ungranted capability")
	assert.Equal(t, "false", got, "an ungranted capability is off")

	// The merge the executor performs is keyed by these very blocks.
	perms := cfg.Hooks.Permissions.For("darktide", "betterui")
	assert.True(t, perms.FSWrite, "the game scope reaches the merge")
	assert.True(t, perms.EnvRead, "the mod scope reaches the merge")
	assert.Equal(t, 7, perms.TimeoutSeconds, "the default cap reaches the merge")

	err = SetConfigValue(cfg, "hooks.permissions.default.nope", "true")
	assert.True(t, IsAppError(err, ErrCodeNotFound),
		"an unknown capability returned %v, want not-found", err)

	err = SetConfigValue(cfg, "hooks.permissions.default.timeout_seconds", "-1")
	assert.True(t, IsAppError(err, ErrCodeInvalidArgument),
		"a negative timeout cap returned %v, want invalid-argument", err)

	err = SetConfigValue(cfg, "hooks.permissions.default.timeout_seconds", "90000")
	assert.True(t, IsAppError(err, ErrCodeInvalidArgument),
		"an oversized timeout cap returned %v, want invalid-argument", err)
}

func TestPermissionKeyShapeRejectsMalformedForms(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())

	for _, key := range []string{
		"hooks.permissions",
		"hooks.permissions.",
		"hooks.permissions.default",
		"hooks.permissions.default.",
		"hooks.permissions.games..fs_read",
		"hooks.permissions.nope.darktide.fs_read",
		"hooks.permissions.mods.betterui",
	} {
		_, err := ConfigValue(cfg, key)
		assert.True(t, IsAppError(err, ErrCodeNotFound),
			"ConfigValue(%q) returned %v, want not-found", key, err)

		err = SetConfigValue(cfg, key, "x")
		assert.True(t, IsAppError(err, ErrCodeNotFound),
			"SetConfigValue(%q) returned %v, want not-found", key, err)
	}
}

func TestConfigKeysListsTheDynamicPatterns(t *testing.T) {
	keys := ConfigKeys()

	for _, want := range []string{
		"games.<id>.path",
		"games.<id>.auto_detect",
		"hooks.permissions.default.<capability>",
		"hooks.permissions.games.<id>.<capability>",
		"hooks.permissions.mods.<id>.<capability>",
	} {
		assert.Contains(t, keys, want, "ConfigKeys is the contract for the settings screen")
	}
}
