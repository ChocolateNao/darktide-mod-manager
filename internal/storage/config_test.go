package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireCode asserts that err carries the expected typed error code.
func requireCode(t *testing.T, err error, want appErrors.Code, what string) {
	t.Helper()

	if err == nil {
		assert.Fail(t, "%s: expected error with code %s, got nil", what, want)
		return
	}

	got := appErrors.CodeOf(err)
	assert.Equal(t, want, got, "%s: expected code %s, got %s (err: %v)", what, want, got, err)
}

// configPath returns a path inside a fresh temp dir for a config file.
func configPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "darktide", "config.json")
}

func writeConfigFile(t *testing.T, path, contents string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o755)
	require.NoError(t, err, "create config directory: %v", err)

	err = os.WriteFile(path, []byte(contents), 0o600)
	require.NoError(t, err, "write config file: %v", err)
}

// assertNoTempFiles fails when the atomic writer left a temporary file behind.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "read directory %q: %v", dir)

	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "temporary file left behind: %q", e.Name())
		assert.False(
			t,
			strings.HasPrefix(e.Name(), "."),
			"temporary file left behind: %q",
			e.Name(),
		)
	}
}

func TestConfigStoreMissingFileYieldsDefaults(t *testing.T) {
	store := NewConfigStore(configPath(t))

	cfg, err := store.Load()
	require.NoError(t, err, "load missing config")

	want := domain.DefaultConfig()
	assert.Equal(
		t,
		want.Storage.BackupRetentionDays,
		cfg.Storage.BackupRetentionDays,
		"expected default backup retention %d, got %d",
		want.Storage.BackupRetentionDays,
		cfg.Storage.BackupRetentionDays,
	)

	assert.True(t, cfg.Cache.VerifyHashes, "expected defaults to keep hash verification on")
}

func TestConfigStoreWithoutPath(t *testing.T) {
	store := NewConfigStore("   ")

	got := store.Path()
	assert.Equal(t, "", got, "expected an empty path for a blank store path, got %q", got)

	cfg, err := store.Load()
	require.NoError(t, err, "load without a path should return defaults, got %v")

	require.NotNil(t, cfg, "load without a path returned a nil config")

	requireCode(t, store.Save(cfg), appErrors.ErrCodeConfigInvalid, "save without a path")
}

func TestConfigStoreNilReceiver(t *testing.T) {
	var store *ConfigStore

	got := store.Path()
	assert.Equal(t, "", got, "expected an empty path from a nil store, got %q", got)

	_, err := store.Load()
	assert.NoError(t, err, "load on a nil store should return defaults, got %v", err)

	requireCode(
		t,
		store.Save(&domain.Config{}),
		appErrors.ErrCodeConfigInvalid,
		"save on a nil store",
	)
}

func TestConfigStorePathIsTrimmed(t *testing.T) {
	store := NewConfigStore("  /tmp/config.json  ")
	got := store.Path()
	assert.Equal(t, "/tmp/config.json", got, "expected the path to be trimmed, got %q", got)
}

func TestConfigStoreLoadAppliesDefaults(t *testing.T) {
	path := configPath(t)
	writeConfigFile(
		t,
		path,
		`{"game":{"path":"/games/Darktide"},"storage":{"backup_retention_days":-3}}`,
	)

	cfg, err := NewConfigStore(path).Load()
	require.NoError(t, err, "load partial config")

	assert.Equal(
		t,
		"/games/Darktide",
		cfg.Game.Path,
		"expected the user game path to survive, got %q",
		cfg.Game.Path,
	)

	assert.False(
		t,
		cfg.Storage.BackupRetentionDays <= 0,
		"expected the negative retention to be repaired, got %d",
		cfg.Storage.BackupRetentionDays,
	)

	assert.False(
		t,
		cfg.Hooks.DefaultTimeout <= 0,
		"expected a default hook timeout to be filled in, got %d",
		cfg.Hooks.DefaultTimeout,
	)
}

func TestConfigStoreLoadEmptyDocuments(t *testing.T) {
	cases := map[string]string{
		"zero byte file":  "",
		"whitespace only": "  \n\t\r\n ",
		"json null":       "null",
		"empty object":    "{}",
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			path := configPath(t)
			writeConfigFile(t, path, contents)

			cfg, err := NewConfigStore(path).Load()
			require.NoError(t, err, "load %q: %v", contents)

			want := domain.DefaultConfig()
			assert.Equal(
				t,
				want.UI.Theme,
				cfg.UI.Theme,
				"expected default theme %q, got %q",
				want.UI.Theme,
				cfg.UI.Theme,
			)
		})
	}
}

func TestConfigStoreLoadRejectsUnparseableFile(t *testing.T) {
	cases := map[string]string{
		"truncated object": `{"game":{"path":"/games"}`,
		"not json":         `game = path`,
		"array":            `["game"]`,
		"wrong field type": `{"storage":{"backup_retention_days":"soon"}}`,
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			path := configPath(t)
			writeConfigFile(t, path, contents)

			cfg, err := NewConfigStore(path).Load()
			requireCode(t, err, appErrors.ErrCodeConfigInvalid, "load unparseable config")

			assert.Nil(t, cfg, "expected no config alongside the error, got %+v", cfg)
			// A file that cannot be parsed must survive untouched: replacing it
			// with defaults would throw away the user's settings silently.
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr, "re-read config: %v", readErr)

			assert.Equal(
				t,
				contents,
				string(data),
				"expected the file to be left alone, got %q",
				data,
			)
		})
	}
}

func TestConfigStoreLoadReportsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions are not enforced")
	}

	path := configPath(t)
	writeConfigFile(t, path, "{}")

	err := os.Chmod(path, 0o000)
	require.NoError(t, err, "chmod config: %v", err)

	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err = NewConfigStore(path).Load()
	requireCode(t, err, appErrors.ErrCodeConfigIO, "load unreadable config")
}

func TestConfigStoreLoadRejectsDirectory(t *testing.T) {
	dir := t.TempDir()

	_, err := NewConfigStore(dir).Load()
	requireCode(t, err, appErrors.ErrCodeConfigIO, "load a directory as config")
}

func TestConfigStoreSaveCreatesParentsAndIsPrivate(t *testing.T) {
	path := configPath(t)
	store := NewConfigStore(path)

	cfg := domain.DefaultConfig()
	cfg.Storage.Path = "/data/storage"

	cfg.Sources = map[string]domain.SourceConfig{
		"vortex": {Enabled: true, APIKey: "secret", Paths: []string{"/a", "/b"}},
	}
	err := store.Save(&cfg)
	require.NoError(t, err, "save config: %v", err)

	info, err := os.Stat(path)
	require.NoError(t, err, "stat saved config")

	perm := info.Mode().Perm()
	assert.Equal(
		t,
		os.FileMode(0o600),
		perm,
		"expected mode 0600 for a file holding an API key, got %o",
		perm,
	)

	assertNoTempFiles(t, filepath.Dir(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read saved config")

	assert.True(
		t,
		strings.HasSuffix(string(data), "\n"),
		"expected the saved file to end with a newline",
	)

	var round domain.Config

	err = json.Unmarshal(data, &round)
	require.NoError(t, err, "saved config is not valid JSON: %v", err)

	assert.Equal(
		t,
		"/data/storage",
		round.Storage.Path,
		"expected storage path to round-trip, got %q",
		round.Storage.Path,
	)

	got := round.Sources["vortex"].APIKey
	assert.Equal(t, "secret", got, "expected the source API key to round-trip, got %q", got)
}

func TestConfigStoreSaveDoesNotMutateCaller(t *testing.T) {
	path := configPath(t)
	store := NewConfigStore(path)

	cfg := &domain.Config{Storage: domain.StorageConfig{BackupRetentionDays: 11}}
	err := store.Save(cfg)
	require.NoError(t, err, "save config: %v", err)

	assert.False(
		t,
		cfg.Cache.VerifyHashes,
		"expected the caller's config to keep its false VerifyHashes",
	)

	assert.Equal(
		t,
		0,
		cfg.Hooks.DefaultTimeout,
		"expected the caller's config to keep a zero hook timeout, got %d",
		cfg.Hooks.DefaultTimeout,
	)
	// The clone written to disk must have the defaults.
	stored, err := store.Load()
	require.NoError(t, err, "reload config")

	assert.False(
		t,
		stored.Hooks.DefaultTimeout <= 0,
		"expected the stored config to be defaulted, got hook timeout %d",
		stored.Hooks.DefaultTimeout,
	)

	assert.Equal(
		t,
		11,
		stored.Storage.BackupRetentionDays,
		"expected the caller's value to be stored, got %d",
		stored.Storage.BackupRetentionDays,
	)
}

func TestConfigStoreSaveRejectsNilConfig(t *testing.T) {
	store := NewConfigStore(configPath(t))

	requireCode(t, store.Save(nil), appErrors.ErrCodeConfigInvalid, "save nil config")
}

func TestConfigStoreSaveOverwrites(t *testing.T) {
	path := configPath(t)
	store := NewConfigStore(path)

	first := domain.DefaultConfig()

	first.Game.Path = "/first"
	err := store.Save(&first)
	require.NoError(t, err, "save first config: %v", err)

	second := domain.DefaultConfig()

	second.Game.Path = "/second"
	err = store.Save(&second)
	require.NoError(t, err, "save second config: %v", err)

	cfg, err := store.Load()
	require.NoError(t, err, "load config")

	assert.Equal(
		t,
		"/second",
		cfg.Game.Path,
		"expected the newer config to win, got %q",
		cfg.Game.Path,
	)

	assertNoTempFiles(t, filepath.Dir(path))
}

func TestConfigStoreSaveIfChanged(t *testing.T) {
	path := configPath(t)
	store := NewConfigStore(path)
	cfg := domain.DefaultConfig()

	// Saving the default config over a missing file is a no-op: the defaults
	// are what a missing file reads back as, so there is nothing to persist.
	changed, err := store.SaveIfChanged(&cfg)
	require.NoError(t, err, "first save")

	assert.False(t, changed, "expected saving defaults over a missing file to be a no-op")

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "expected no config file to be created, stat error: %v", err)

	cfg.UI.Theme = "dark"

	changed, err = store.SaveIfChanged(&cfg)
	require.NoError(t, err, "second save")

	assert.True(t, changed, "expected a changed config to be written")

	changed, err = store.SaveIfChanged(&cfg)
	require.NoError(t, err, "third save")

	assert.False(t, changed, "expected an unchanged config not to be rewritten")

	cfg.Storage.BackupRetentionDays++

	changed, err = store.SaveIfChanged(&cfg)
	require.NoError(t, err, "fourth save")

	assert.True(t, changed, "expected a second change to be written")
}

func TestConfigStoreSaveIfChangedComparesEffectiveConfigs(t *testing.T) {
	path := configPath(t)
	store := NewConfigStore(path)
	cfg := domain.DefaultConfig()

	cfg.Game.Source = "steam"
	err := store.Save(&cfg)
	require.NoError(t, err, "save config: %v", err)

	same := domain.DefaultConfig()
	same.Game.Source = "steam"

	changed, err := store.SaveIfChanged(&same)
	require.NoError(t, err, "save-if-changed with the same config")

	assert.False(t, changed, "expected an identical effective config not to be rewritten")

	// A candidate that only spells out the values it cares about, leaving every
	// zero field zero, has the same effective configuration after defaults are
	// applied — except for booleans, which JSON cannot tell apart from an
	// absent key. Those are compared as written, which is why this candidate
	// does count as a change.
	candidate := domain.Config{Game: domain.GameConfig{Source: "steam"}}

	changed, err = store.SaveIfChanged(&candidate)
	require.NoError(t, err, "save-if-changed")

	assert.True(t, changed, "expected a differing auto-detect flag to count as a change")
}

func TestConfigStoreSaveIfChangedDetectsNestedSourceChanges(t *testing.T) {
	path := configPath(t)
	store := NewConfigStore(path)
	cfg := domain.DefaultConfig()
	cfg.SetSource(
		"vortex",
		domain.SourceConfig{Enabled: true, Options: map[string]string{"game": "darktide"}},
	)

	err := store.Save(&cfg)
	require.NoError(t, err, "save config: %v", err)

	reloaded, err := store.Load()
	require.NoError(t, err, "load config")

	changed, err := store.SaveIfChanged(reloaded)
	assert.False(
		t,
		err != nil || changed,
		"expected a reloaded config to be unchanged (changed=%v, err=%v)",
		changed,
		err,
	)

	reloaded.SetSource(
		"vortex",
		domain.SourceConfig{Enabled: true, Options: map[string]string{"game": "darktide2"}},
	)

	changed, err = store.SaveIfChanged(reloaded)
	assert.False(
		t,
		err != nil || !changed,
		"expected a nested option change to be written (changed=%v, err=%v)",
		changed,
		err,
	)

	reloaded.SetSource(
		"vortex",
		domain.SourceConfig{Enabled: false, Options: map[string]string{"game": "darktide"}},
	)

	changed, err = store.SaveIfChanged(reloaded)
	assert.False(
		t,
		err != nil || !changed,
		"expected an Enabled change to be written (changed=%v, err=%v)",
		changed,
		err,
	)
}

func TestConfigStoreSaveIfChangedRejectsNilConfig(t *testing.T) {
	store := NewConfigStore(configPath(t))

	changed, err := store.SaveIfChanged(nil)
	requireCode(t, err, appErrors.ErrCodeConfigInvalid, "save-if-changed with a nil config")

	assert.False(t, changed, "expected no write to be reported for a nil config")
}

func TestConfigStoreSaveIfChangedPropagatesReadFailure(t *testing.T) {
	path := configPath(t)
	writeConfigFile(t, path, "{oops")
	store := NewConfigStore(path)

	changed, err := store.SaveIfChanged(&domain.Config{})
	requireCode(t, err, appErrors.ErrCodeConfigInvalid, "save-if-changed on a corrupt file")

	assert.False(t, changed, "expected no write when the current config cannot be read")
}

func TestConfigStoreSaveIfChangedWithoutPath(t *testing.T) {
	store := NewConfigStore("")

	changed, err := store.SaveIfChanged(&domain.Config{})
	requireCode(t, err, appErrors.ErrCodeConfigInvalid, "save-if-changed without a path")

	assert.False(t, changed, "expected no write without a path")
}

func TestOneShotConfigHelpers(t *testing.T) {
	path := configPath(t)

	cfg, err := LoadConfig(path)
	require.NoError(t, err, "load config")

	cfg.Game.AutoDetect = false

	cfg.Advanced.LogLevel = "debug"
	err = SaveConfig(path, cfg)
	require.NoError(t, err, "save config: %v", err)

	reloaded, err := LoadConfig(path)
	require.NoError(t, err, "reload config")

	assert.False(t, reloaded.Game.AutoDetect, "expected auto detect to stay off")

	assert.Equal(
		t,
		"debug",
		reloaded.Advanced.LogLevel,
		"expected the log level to round-trip, got %q",
		reloaded.Advanced.LogLevel,
	)
}

func TestDecodeConfig(t *testing.T) {
	t.Run("whitespace is an empty document", func(t *testing.T) {
		cfg, err := decodeConfig([]byte("\n\t "))
		require.NoError(t, err, "decode whitespace")

		assert.Equal(
			t,
			domain.DefaultConfig().UI.Theme,
			cfg.UI.Theme,
			"expected defaults, got theme %q",
			cfg.UI.Theme,
		)
	})

	t.Run("garbage is invalid", func(t *testing.T) {
		if _, err := decodeConfig([]byte("}{")); err == nil {
			require.FailNow(t, "expected an error for unbalanced braces")
		} else {
			requireCode(t, err, appErrors.ErrCodeConfigInvalid, "decode garbage")
		}
	})

	t.Run("nil bytes are an empty document", func(t *testing.T) {
		cfg, err := decodeConfig(nil)
		require.NoError(t, err, "decode nil")

		require.NotNil(t, cfg, "expected a default config")
	})
}

func TestEqualConfig(t *testing.T) {
	base := domain.DefaultConfig()
	same := domain.DefaultConfig()

	cases := []struct {
		a    *domain.Config
		b    *domain.Config
		name string
		want bool
	}{
		{nil, nil, "both nil", true},
		{nil, &base, "one nil", false},
		{&base, nil, "other nil", false},
		{&base, &same, "identical", true},
		{&base, func() *domain.Config {
			c := domain.DefaultConfig()
			c.Storage.Path = "/elsewhere"

			return &c
		}(), "different value", false},
		{&base, func() *domain.Config {
			c := domain.DefaultConfig()
			c.Sources = map[string]domain.SourceConfig{
				"vortex": {Options: map[string]string{"a": "1"}},
			}

			return &c
		}(), "source option differs", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := equalConfig(tc.a, tc.b)
			assert.Equal(t, tc.want, got, "equalConfig() = %v, want %v", got, tc.want)
		})
	}
}

func TestConfigStoreIsSafeForConcurrentUse(t *testing.T) {
	path := configPath(t)
	store := NewConfigStore(path)

	cfg := domain.DefaultConfig()
	err := store.Save(&cfg)
	require.NoError(t, err, "seed config: %v", err)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()

			local := domain.DefaultConfig()

			local.Advanced.LogLevel = string(rune('a' + n))
			err := store.Save(&local)
			assert.NoError(t, err, "concurrent save: %v", err)
		}(i)
		go func() {
			defer wg.Done()

			_, err := store.Load()
			assert.NoError(t, err, "concurrent load: %v", err)
		}()
	}

	wg.Wait()

	_, err = store.Load()
	require.NoError(t, err, "final load: %v", err)

	assertNoTempFiles(t, filepath.Dir(path))
}
