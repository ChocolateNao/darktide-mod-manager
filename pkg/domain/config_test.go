package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformPredicates(t *testing.T) {
	cases := []struct {
		p            Platform
		windows      bool
		darwin       bool
		unix         bool
		caseInsensit bool
	}{
		{PlatformWindows, true, false, false, true},
		{PlatformLinux, false, false, true, false},
		{PlatformDarwin, false, true, true, true},
		{PlatformFreeBSD, false, false, true, false},
		{"plan9", false, false, false, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.windows, tc.p.IsWindows(), "%q.IsWindows", tc.p)

		assert.Equal(t, tc.darwin, tc.p.IsDarwin(), "%q.IsDarwin", tc.p)

		assert.Equal(t, tc.unix, tc.p.IsUnix(), "%q.IsUnix", tc.p)

		assert.Equal(
			t,
			tc.caseInsensit,
			tc.p.CaseInsensitivePaths(),
			"%q.CaseInsensitivePaths",
			tc.p,
		)
	}
}

func TestPlatformFromGOOS(t *testing.T) {
	cases := map[string]Platform{
		"windows": PlatformWindows,
		"linux":   PlatformLinux,
		"darwin":  PlatformDarwin,
		"freebsd": PlatformFreeBSD,
		"openbsd": PlatformFreeBSD,
		"solaris": Platform("solaris"),
	}
	for goos, want := range cases {
		assert.Equal(t, want, PlatformFromGOOS(goos), "PlatformFromGOOS(%q)", goos)
	}
}

func TestCurrentPlatformIsKnown(t *testing.T) {
	assert.NotEqual(t, "", CurrentPlatform(), "CurrentPlatform must not be empty")
}

// --- Config defaults ---

func TestApplyDefaultsFillsZeroValues(t *testing.T) {
	var c Config
	c.ApplyDefaults()

	d := DefaultConfig()
	assert.Equal(t, d.Cache.MaxSizeGB, c.Cache.MaxSizeGB, "cache defaults not applied")
	assert.Equal(t, d.Cache.MaxAgeDays, c.Cache.MaxAgeDays, "cache defaults not applied")

	assert.Equal(t, d.Hooks.DefaultTimeout, c.Hooks.DefaultTimeout, "hook defaults not applied")
	assert.Equal(t, d.Hooks.MaxTimeout, c.Hooks.MaxTimeout, "hook defaults not applied")

	assert.Equal(
		t,
		d.Storage.BackupRetentionDays,
		c.Storage.BackupRetentionDays,
		"storage defaults not applied",
	)

	assert.NotNil(t, c.Sources, "Sources map should be allocated")

	assert.Equal(t, "system", c.UI.Theme, "ui defaults not applied")
	assert.Equal(t, "en", c.UI.Language, "ui defaults not applied")

	assert.Equal(t, "info", c.Advanced.LogLevel, "advanced defaults not applied")

	assert.Equal(t, 24, c.AutoUpdate.CheckIntervalHours, "auto update defaults not applied")
}

func TestApplyDefaultsRepairsNegativeValues(t *testing.T) {
	c := Config{
		Cache:   CacheConfig{MaxSizeGB: -1, MaxAgeDays: -5},
		Hooks:   HooksConfig{DefaultTimeout: -3, MaxTimeout: -1},
		Storage: StorageConfig{BackupRetentionDays: 0},
	}
	c.ApplyDefaults()

	assert.True(
		t,
		c.Cache.MaxSizeGB > 0 && c.Cache.MaxAgeDays > 0,
		"negative cache values not repaired",
	)

	assert.True(t, c.Hooks.DefaultTimeout > 0, "negative hook timeout not repaired")

	assert.True(t, c.Storage.BackupRetentionDays > 0, "zero retention not repaired")
}

func TestApplyDefaultsKeepsUserValues(t *testing.T) {
	c := Config{
		ActiveGame: "darktide",
		Games: map[string]GameSettings{
			"darktide": {
				Path:               "/games/darktide",
				Source:             "steam",
				ModsFolderOverride: "/mnt/mods",
				AutoDetect:         false,
			},
		},
		Storage: StorageConfig{Path: "/data", BackupRetentionDays: 1},
		Cache:   CacheConfig{Path: "/cache", MaxSizeGB: 100, MaxAgeDays: 1},
		Hooks: HooksConfig{
			DefaultTimeout: Duration(5 * time.Second),
			MaxTimeout:     Duration(60 * time.Second),
		},
		AutoUpdate: AutoUpdateConfig{Enabled: true, CheckIntervalHours: 1, NotifyOnUpdate: false},
		UI:         UIConfig{Theme: "light", Language: "ru", CompactMode: true},
		Advanced:   AdvancedConfig{EnableExperimental: true, LogLevel: "debug"},
	}
	before := c.Clone()
	c.ApplyDefaults()

	assert.Equal(t, before.ActiveSettings(), c.ActiveSettings(), "user values changed")
	assert.Equal(t, "/data", c.Storage.Path, "user values changed")
	assert.Equal(t, 1, c.Storage.BackupRetentionDays, "user values changed")

	assert.Equal(t, 100, c.Cache.MaxSizeGB, "cache user values changed")
	assert.Equal(t, "/cache", c.Cache.Path, "cache user values changed")

	assert.Equal(t, Duration(5*time.Second), c.Hooks.DefaultTimeout, "hook user values changed")
	assert.Equal(t, Duration(60*time.Second), c.Hooks.MaxTimeout, "hook user values changed")
	assert.True(t, c.Hooks.Permissions.Default.FSRead, "hook permission defaults not applied")
	assert.False(t, c.Hooks.Permissions.Default.FSWrite, "hook permission defaults not applied")

	assert.True(t, c.AutoUpdate.Enabled, "auto update user values changed")
	assert.False(t, c.AutoUpdate.NotifyOnUpdate, "auto update user values changed")

	assert.Equal(t, "light", c.UI.Theme, "ui user values changed")
	assert.True(t, c.UI.CompactMode, "ui user values changed")

	assert.True(t, c.Advanced.EnableExperimental, "advanced user values changed")
	assert.Equal(t, "debug", c.Advanced.LogLevel, "advanced user values changed")
}

func TestApplyDefaultsForcesVerifyHashes(t *testing.T) {
	// VerifyHashes is a safety net, not a preference: the zero value means "not
	// configured", so it must not silently disable integrity checking.
	c := Config{Cache: CacheConfig{VerifyHashes: false}}
	c.ApplyDefaults()

	assert.True(t, c.Cache.VerifyHashes, "VerifyHashes should default to true")
}

func TestApplyDefaultsRaisesMaxTimeoutAboveDefault(t *testing.T) {
	c := Config{Hooks: HooksConfig{DefaultTimeout: Duration(900 * time.Second)}}
	c.ApplyDefaults()

	assert.True(t, c.Hooks.MaxTimeout >= c.Hooks.DefaultTimeout,
		"MaxTimeout %d below DefaultTimeout %d",
		c.Hooks.MaxTimeout,
		c.Hooks.DefaultTimeout,
	)
}

func TestApplyDefaultsNilReceiver(t *testing.T) {
	var c *Config
	c.ApplyDefaults() // must not panic
}

func TestApplyDefaultsAllocatesSourcesOnExistingNilMap(t *testing.T) {
	c := Config{Sources: nil}
	c.ApplyDefaults()
	c.SetSource("vortex", SourceConfig{Enabled: true})

	sc, ok := c.SourceConfigFor("vortex")
	assert.True(t, ok && sc.Enabled, "SetSource after defaults failed: %+v %v", sc, ok)
}

func TestSourceConfigForMissingAndNil(t *testing.T) {
	var nilCfg *Config

	_, ok := nilCfg.SourceConfigFor("vortex")
	assert.False(t, ok, "nil config should report absent")

	c := Config{Sources: map[string]SourceConfig{"local": {Enabled: true}}}
	_, ok = c.SourceConfigFor("vortex")
	assert.False(t, ok, "unknown source should report absent")

	sc, ok := c.SourceConfigFor("local")
	assert.True(t, ok && sc.Enabled, "known source = %+v %v", sc, ok)
}

func TestSetSourceNilReceiver(t *testing.T) {
	var c *Config
	c.SetSource("x", SourceConfig{}) // must not panic
}

// TestSourceConfigForMatchesKeysLoosely covers the hand-edited-config case: the
// keys come from a file the user owns, and a config naming "GitHub" describes the
// same source as "github". Reporting it as absent would leave the user with a
// token they can see in their file and that the manager never reads.
func TestSourceConfigForMatchesKeysLoosely(t *testing.T) {
	c := Config{Sources: map[string]SourceConfig{"GitHub": {APIKey: "tok-1"}}}

	for _, id := range []string{"GitHub", "github", "GITHUB", "  github  ", "\tGitHub\n"} {
		sc, ok := c.SourceConfigFor(id)
		if !ok {
			assert.Fail(t,
				"SourceConfigFor(%q) reported absent, want the block stored under %q",
				id,
				"GitHub",
			)

			continue
		}

		assert.Equal(t, "tok-1", sc.APIKey, "SourceConfigFor(%q)", id)
	}
}

// TestSourceConfigForPrefersTheExactKey keeps the loose match a fallback rather
// than a replacement: with two spellings present, the one the caller asked for
// is the one that answers.
func TestSourceConfigForPrefersTheExactKey(t *testing.T) {
	c := Config{Sources: map[string]SourceConfig{
		"github": {APIKey: "exact"},
		"GitHub": {APIKey: "other"},
	}}

	sc, ok := c.SourceConfigFor("github")
	assert.True(
		t,
		ok && sc.APIKey == "exact",
		"SourceConfigFor(github) = %+v %v, want the exact key",
		sc,
		ok,
	)

	sc, ok = c.SourceConfigFor("GitHub")
	assert.True(
		t,
		ok && sc.APIKey == "other",
		"SourceConfigFor(GitHub) = %+v %v, want the exact key",
		sc,
		ok,
	)
}

func TestSourceConfigForRejectsABlankSourceID(t *testing.T) {
	// A blank id has no meaning, so it must report absent rather than falling
	// through to the loose comparison and matching some key that happens to
	// normalise to the empty string.
	c := Config{Sources: map[string]SourceConfig{"   ": {Enabled: true}}}
	for _, id := range []string{"", "   ", "\t"} {
		_, ok := c.SourceConfigFor(id)
		assert.False(t, ok, "SourceConfigFor(%q) reported present for a blank id", id)
	}

	var nilCfg *Config

	_, ok := nilCfg.SourceConfigFor("  github  ")
	assert.False(t, ok, "nil config should report absent for a padded id too")
}

func TestConfigJSONRoundTrip(t *testing.T) {
	in := DefaultConfig()
	in.UpdateActiveSettings(func(gs *GameSettings) { gs.Path = "/games/darktide" })
	in.Sources = map[string]SourceConfig{
		"vortex": {Enabled: true, APIKey: "k"},
		"local":  {Enabled: true, Paths: []string{"/mods"}},
	}

	data, err := json.Marshal(in)
	require.NoError(t, err, "marshal")

	var out Config

	err = json.Unmarshal(data, &out)
	require.NoError(t, err, "unmarshal")

	out.ApplyDefaults()

	assert.Equal(t, in.ActiveSettings().Path, out.ActiveSettings().Path, "game path lost")

	assert.Equal(t, "k", out.Sources["vortex"].APIKey, "api key lost")

	assert.Len(t, out.Sources["local"].Paths, 1, "source paths lost")

	assert.Equal(t, in.Cache.MaxSizeGB, out.Cache.MaxSizeGB, "cache lost")
}

func TestGameInstallationPathEqual(t *testing.T) {
	g := GameInstallation{Path: `/Games/Darktide/`}
	assert.True(
		t,
		g.PathEqual("/games/darktide"),
		"should match case-insensitively with trailing slash",
	)

	assert.False(t, g.PathEqual("/games/other"), "different path must not match")

	assert.True(t, (GameInstallation{}).PathEqual(""), "two empty paths should match")
}

func TestHookContextJSONTags(t *testing.T) {
	ctx := HookContext{
		Mod:    ModHookContext{ID: "a", Path: "/mods/a"},
		Game:   GameHookContext{Path: "/game", ModsPath: "/game/mods"},
		Config: ConfigHookContext{CachePath: "/cache"},
		Phase:  PhasePreEnable,
		Index:  2,
	}

	data, err := json.Marshal(ctx)
	require.NoError(t, err, "marshal")

	var back map[string]any

	err = json.Unmarshal(data, &back)
	require.NoError(t, err, "unmarshal")

	for _, key := range []string{"mod", "game", "config", "phase", "index"} {
		_, ok := back[key]
		assert.True(t, ok, "hook context missing key %q in %s", key, data)
	}
}

func TestSourceKeyFoldsIDsAndSetSourceWritesWhereTheBlockAlreadyIs(t *testing.T) {
	// Two blocks for one source is not a harmless duplicate: they disagree about
	// Enabled, and the disabled one wins, so a settings screen that wrote
	// "Nexus" beside an existing "nexus" would switch the source off while
	// appearing to configure it.
	cfg := DefaultConfig()

	cfg.Sources = map[string]SourceConfig{
		"Nexus": {Enabled: true, APIKey: "typed-by-hand"},
	}
	key, ok := cfg.SourceKey("nexus")
	assert.True(
		t,
		ok && key == "Nexus",
		"SourceKey(nexus) is %q, %v; want the existing \"Nexus\"",
		key,
		ok,
	)

	key, ok = cfg.SourceKey(" NEXUS ")
	assert.True(
		t,
		ok && key == "Nexus",
		"SourceKey with padding and capitals is %q, %v; want \"Nexus\"",
		key,
		ok,
	)

	_, ok = cfg.SourceKey("github")
	assert.False(t, ok, "SourceKey found a block for a source that has none")

	_, ok = cfg.SourceKey("  ")
	assert.False(t, ok, "SourceKey matched a blank id")

	key, ok = (*Config)(nil).SourceKey("nexus")
	assert.False(t, ok || key != "", "SourceKey on a nil configuration reported a block")

	cfg.SetSource("nexus", SourceConfig{Enabled: true, APIKey: "replaced"})

	assert.Len(t, cfg.Sources, 1, "SetSource created blocks, want the existing one updated")

	block, ok := cfg.SourceConfigFor("nexus")
	assert.True(
		t,
		ok && block.APIKey == "replaced",
		"the folded block reads back as %+v, ok=%v",
		block,
		ok,
	)

	cfg.SetSource("GitHub", SourceConfig{Enabled: true})

	_, ok = cfg.Sources["github"]
	assert.True(t, ok, "a new block was written as %+v, want it folded to \"github\"", cfg.Sources)

	cfg.SetSource("   ", SourceConfig{Enabled: true})

	assert.Len(t, cfg.Sources, 2, "a blank id wrote a block")
}

func TestPermissionsForMergesModOverGameOverDefault(t *testing.T) {
	perms := HooksPermissionsConfig{
		Default: DefaultScriptPermissions(),
		Games: map[string]ScriptPermissions{
			"darktide": {EnvRead: true, TimeoutSeconds: 20},
		},
		Mods: map[string]ScriptPermissions{
			"cool-mod": {FSWrite: true, TimeoutSeconds: 5},
		},
	}

	got := perms.For("darktide", "cool-mod")
	assert.True(t, got.FSRead, "default capability survives")
	assert.True(t, got.EnvRead, "game scope contributes")
	assert.True(t, got.FSWrite, "mod scope contributes")
	assert.False(t, got.ConfigWrite, "nothing granted config write")
	assert.Equal(t, 5, got.TimeoutSeconds, "mod timeout wins over game timeout")

	got = perms.For("darktide", "unknown-mod")
	assert.Equal(t, 20, got.TimeoutSeconds, "game timeout applies without a mod entry")
	assert.False(t, got.FSWrite, "no mod entry, no write")

	got = perms.For("unknown-game", "cool-mod")
	assert.False(t, got.EnvRead, "game contribution is scoped to its id")
	assert.True(t, got.FSWrite, "mod contribution survives a missing game entry")
	assert.Equal(t, 5, got.TimeoutSeconds, "mod timeout applies without a game entry")

	got = perms.For("unknown-game", "unknown-mod")
	assert.Equal(t, DefaultScriptPermissions(), got, "bare defaults")
	assert.Equal(t, 0, got.TimeoutSeconds, "default scope has no timeout")

	got = HooksPermissionsConfig{
		Games: perms.Games,
		Mods:  perms.Mods,
	}.For("darktide", "cool-mod")
	assert.Equal(
		t,
		ScriptPermissions{EnvRead: true, FSWrite: true, TimeoutSeconds: 5},
		got,
		"zero default must not veto the scopes above it",
	)
}

func TestApplyDefaultsFillsPermissionScopes(t *testing.T) {
	cfg := Config{
		Hooks: HooksConfig{
			Permissions: HooksPermissionsConfig{
				Games: map[string]ScriptPermissions{"sietch": {}},
				Mods:  map[string]ScriptPermissions{"some-mod": {EnvRead: true}},
			},
		},
	}
	cfg.ApplyDefaults()

	assert.Equal(
		t,
		DefaultScriptPermissions(),
		cfg.Hooks.Permissions.Default,
		"absent default block gets the built-in permissions",
	)
	assert.Equal(
		t,
		DefaultScriptPermissions(),
		cfg.Hooks.Permissions.Games["sietch"],
		"a zero game entry is indistinguishable from an unset one",
	)
	assert.Equal(
		t,
		ScriptPermissions{EnvRead: true},
		cfg.Hooks.Permissions.Mods["some-mod"],
		"a non-zero mod entry is kept exactly as written",
	)
}

func TestConfigJSONRoundTripKeepsGameShape(t *testing.T) {
	in := DefaultConfig()
	in.ActiveGame = "sietch"
	in.Games = map[string]GameSettings{
		"darktide": {Path: "/g/darktide", Source: "steam", AutoDetect: false},
		"sietch":   {Path: "/g/sietch", ModsFolderOverride: "extra-mods"},
	}
	in.Hooks.Permissions.Games = map[string]ScriptPermissions{
		"sietch": {EnvRead: true},
	}
	in.Hooks.Permissions.Mods = map[string]ScriptPermissions{
		"m1": {FSWrite: true, TimeoutSeconds: 7},
	}

	data, err := json.Marshal(in)
	require.NoError(t, err, "marshal")

	var out Config

	err = json.Unmarshal(data, &out)
	require.NoError(t, err, "unmarshal")

	out.ApplyDefaults()

	assert.Equal(t, "sietch", out.ActiveGame, "active game lost")
	assert.Equal(t, in.Games, out.Games, "games map lost")
	assert.Equal(
		t,
		in.Hooks.Permissions.Games,
		out.Hooks.Permissions.Games,
		"per-game permissions lost",
	)
	assert.Equal(
		t,
		in.Hooks.Permissions.Mods,
		out.Hooks.Permissions.Mods,
		"per-mod permissions lost",
	)
}

func TestCloneDetachesGameAndPermissionMaps(t *testing.T) {
	in := DefaultConfig()
	in.Games = map[string]GameSettings{"a": {Path: "/a"}}
	in.Hooks.Permissions.Games = map[string]ScriptPermissions{"a": {EnvRead: true}}
	in.Hooks.Permissions.Mods = map[string]ScriptPermissions{"m": {FSWrite: true}}

	out := in.Clone()
	require.NotNil(t, out, "clone")

	out.Games["a"] = GameSettings{Path: "/changed"}
	out.Games["b"] = GameSettings{}
	out.Hooks.Permissions.Games["a"] = ScriptPermissions{}
	out.Hooks.Permissions.Mods["m"] = ScriptPermissions{}
	delete(out.Hooks.Permissions.Mods, "m")

	assert.Equal(t, GameSettings{Path: "/a"}, in.Games["a"], "clone shared the games map")
	assert.Len(t, in.Games, 1, "clone added a game to the original")
	assert.Equal(
		t,
		ScriptPermissions{EnvRead: true},
		in.Hooks.Permissions.Games["a"],
		"clone shared the game permissions map",
	)
	assert.Equal(
		t,
		ScriptPermissions{FSWrite: true},
		in.Hooks.Permissions.Mods["m"],
		"clone shared the mod permissions map",
	)
}
