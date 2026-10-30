package api

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// configStore returns the configuration store, or the error to report.
func (c *Client) configStore() (services.ConfigStore, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.Config == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a configuration store")
	}

	return c.deps.Config, nil
}

// GetConfig returns a copy of the manager's configuration. The copy matters: Load
// returns a fresh value each call, and handing that out would let a caller mutate
// what the next caller sees. A missing file yields defaults, not an error.
func (c *Client) GetConfig(ctx context.Context) (*Config, error) {
	store, err := c.configStore()
	if err != nil {
		return nil, err
	}

	cfg, err := store.Load()
	if err != nil {
		return nil, err
	}

	if cfg == nil {
		// A store that answers nil without an error is not to be trusted, but
		// inventing an empty configuration would silently reset every value.
		return nil, appErrors.New(appErrors.ErrCodeConfigInvalid,
			"configuration store returned nothing")
	}

	return cfg.Clone(), nil
}

// UpdateConfig replaces the manager's configuration. Defaults are applied first so
// a partial configuration is completed and a zero that would disable integrity
// checking cannot reach disk. The write is skipped when unchanged, so a
// timer-driven call does not rewrite the file and trigger backup tools.
func (c *Client) UpdateConfig(ctx context.Context, cfg *Config) error {
	store, err := c.configStore()
	if err != nil {
		return err
	}

	if cfg == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"configuration must not be nil")
	}

	updated := cfg.Clone()
	updated.ApplyDefaults()

	if _, err := store.SaveIfChanged(updated); err != nil {
		return err
	}

	return nil
}

// ConfigPath returns the file the configuration is read from and written to.
func (c *Client) ConfigPath() (string, error) {
	store, err := c.configStore()
	if err != nil {
		return "", err
	}

	return store.Path(), nil
}

// SourceConfig returns one source's configuration block. A source with no block
// yields defaults and true, matching the registry: a fresh install has no blocks
// and should still show its bundled sources.
func (c *Client) SourceConfig(
	ctx context.Context,
	sourceID string,
) (domain.SourceConfig, bool, error) {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return domain.SourceConfig{}, false, err
	}

	block, ok := cfg.SourceConfigFor(sourceID)

	return block, ok, nil
}

// GetConfigValue reads one setting by dotted key. An absent key reports
// ErrCodeNotFound, not an empty string, so unset is distinguishable from
// set-to-nothing. An api_key is deliberately not readable — see SetConfigValue.
func (c *Client) GetConfigValue(ctx context.Context, key string) (string, error) {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return "", err
	}

	return ConfigValue(cfg, key)
}

// SetConfigValue changes one setting by dotted key and saves. An api_key may be
// written but not read back: a credential a caller cannot read cannot be
// accidentally logged. The value is parsed as the destination type, so "banana"
// cannot be stored in a boolean.
func (c *Client) SetConfigValue(ctx context.Context, key, value string) error {
	store, err := c.configStore()
	if err != nil {
		return err
	}

	cfg, err := store.Load()
	if err != nil {
		return err
	}

	if cfg == nil {
		return appErrors.New(appErrors.ErrCodeConfigInvalid,
			"configuration store returned nothing")
	}

	if err := SetConfigValue(cfg, key, value); err != nil {
		return err
	}

	if _, err := store.SaveIfChanged(cfg); err != nil {
		return err
	}

	return nil
}

// configField is one addressable setting. A table, not reflection: the set
// of settings is fixed and small, reflection would make "is this key real?" a
// runtime question, and a table makes the accepted keys greppable.
type configField struct {
	get  func(cfg *Config) string
	set  func(cfg *Config, value string) error
	key  string
	help string
}

// configFields lists every settable setting. The order is the order the keys are
// searched in, so a prefix rule for sources.* cannot shadow a real key.
var configFields = []configField{
	{
		key:  "active_game",
		help: "registry id of the game currently managed; empty means the default",
		get:  func(c *Config) string { return c.ActiveGame },
		set:  func(c *Config, v string) error { c.ActiveGame = strings.TrimSpace(v); return nil },
	},
	{
		key:  "game.path",
		help: "installation directory of the active game",
		get:  func(c *Config) string { return c.ActiveSettings().Path },
		set: func(c *Config, v string) error {
			c.UpdateActiveSettings(func(s *domain.GameSettings) { s.Path = v })

			return nil
		},
	},
	{
		key:  "game.source",
		help: "launcher to prefer when auto-detecting the active game",
		get:  func(c *Config) string { return string(c.ActiveSettings().Source) },
		set: func(c *Config, v string) error {
			c.UpdateActiveSettings(func(s *domain.GameSettings) {
				s.Source = domain.GameSource(strings.TrimSpace(v))
			})

			return nil
		},
	},
	{
		key:  "game.mods_folder_override",
		help: "deploy into this folder instead of the detected one",
		get:  func(c *Config) string { return c.ActiveSettings().ModsFolderOverride },
		set: func(c *Config, v string) error {
			c.UpdateActiveSettings(func(s *domain.GameSettings) { s.ModsFolderOverride = v })

			return nil
		},
	},
	{
		key:  "game.auto_detect",
		help: "probe known launcher locations on start-up",
		get:  func(c *Config) string { return strconv.FormatBool(c.ActiveSettings().AutoDetect) },
		set: func(c *Config, v string) error {
			parsed, err := parseConfigBool(v)
			if err != nil {
				return err
			}

			c.UpdateActiveSettings(func(s *domain.GameSettings) { s.AutoDetect = parsed })

			return nil
		},
	},

	{
		key:  "storage.path",
		help: "where mod payloads are stored",
		get:  func(c *Config) string { return c.Storage.Path },
		set:  func(c *Config, v string) error { c.Storage.Path = v; return nil },
	},
	{
		key:  "storage.backup_retention_days",
		help: "keep superseded mod versions for this many days; 0 keeps none",
		get:  func(c *Config) string { return strconv.Itoa(c.Storage.BackupRetentionDays) },
		set:  intSetter(func(c *Config) *int { return &c.Storage.BackupRetentionDays }, 0, 36500),
	},

	{
		key:  "cache.path",
		help: "where downloaded archives are stored",
		get:  func(c *Config) string { return c.Cache.Path },
		set:  func(c *Config, v string) error { c.Cache.Path = v; return nil },
	},
	{
		key:  "cache.max_size_gb",
		help: "prune the cache above this size in gigabytes",
		get:  func(c *Config) string { return strconv.Itoa(c.Cache.MaxSizeGB) },
		set:  intSetter(func(c *Config) *int { return &c.Cache.MaxSizeGB }, 0, 1000000),
	},
	{
		key:  "cache.max_age_days",
		help: "prune cached archives older than this",
		get:  func(c *Config) string { return strconv.Itoa(c.Cache.MaxAgeDays) },
		set:  intSetter(func(c *Config) *int { return &c.Cache.MaxAgeDays }, 0, 1000000),
	},
	{
		key:  "cache.verify_hashes",
		help: "verify every download against its published digest",
		get:  func(c *Config) string { return strconv.FormatBool(c.Cache.VerifyHashes) },
		set:  boolSetter(func(c *Config) *bool { return &c.Cache.VerifyHashes }),
	},

	{
		key:  "hooks.default_timeout",
		help: "duration a hook may run when it declares none, e.g. 10s",
		get: func(c *Config) string {
			return c.Hooks.DefaultTimeout.Duration().String()
		},
		set: durationSetter(
			func(c *Config) *domain.Duration { return &c.Hooks.DefaultTimeout },
			1,
			3600,
		),
	},
	{
		key:  "hooks.max_timeout",
		help: "absolute ceiling on one hook run, e.g. 30s",
		get: func(c *Config) string {
			return c.Hooks.MaxTimeout.Duration().String()
		},
		set: durationSetter(
			func(c *Config) *domain.Duration { return &c.Hooks.MaxTimeout },
			1,
			86400,
		),
	},

	{
		key:  "auto_update.enabled",
		help: "check installed mods for updates",
		get:  func(c *Config) string { return strconv.FormatBool(c.AutoUpdate.Enabled) },
		set:  boolSetter(func(c *Config) *bool { return &c.AutoUpdate.Enabled }),
	},
	{
		key:  "auto_update.check_interval_hours",
		help: "hours between update checks",
		get:  func(c *Config) string { return strconv.Itoa(c.AutoUpdate.CheckIntervalHours) },
		set:  intSetter(func(c *Config) *int { return &c.AutoUpdate.CheckIntervalHours }, 1, 8760),
	},
	{
		key:  "auto_update.download_automatically",
		help: "install updates without asking",
		get:  func(c *Config) string { return strconv.FormatBool(c.AutoUpdate.DownloadAutomatically) },
		set:  boolSetter(func(c *Config) *bool { return &c.AutoUpdate.DownloadAutomatically }),
	},
	{
		key:  "auto_update.notify_on_update",
		help: "show a notification when an update is found",
		get:  func(c *Config) string { return strconv.FormatBool(c.AutoUpdate.NotifyOnUpdate) },
		set:  boolSetter(func(c *Config) *bool { return &c.AutoUpdate.NotifyOnUpdate }),
	},
	{
		key:  "auto_update.last_check",
		help: "when the last update check ran",
		get:  func(c *Config) string { return c.AutoUpdate.LastCheck },
		set:  func(c *Config, v string) error { c.AutoUpdate.LastCheck = v; return nil },
	},

	{
		key:  "ui.theme",
		help: "colour theme: light, dark or system",
		get:  func(c *Config) string { return c.UI.Theme },
		set:  func(c *Config, v string) error { c.UI.Theme = v; return nil },
	},
	{
		key:  "ui.language",
		help: "interface language",
		get:  func(c *Config) string { return c.UI.Language },
		set:  func(c *Config, v string) error { c.UI.Language = v; return nil },
	},
	{
		key:  "ui.compact_mode",
		help: "fit more rows on screen",
		get:  func(c *Config) string { return strconv.FormatBool(c.UI.CompactMode) },
		set:  boolSetter(func(c *Config) *bool { return &c.UI.CompactMode }),
	},

	{
		key:  "logging.level",
		help: "verbosity: trace, debug, info, warn or error",
		get:  func(c *Config) string { return c.Logging.Level },
		set: func(c *Config, v string) error {
			level, err := parseLogLevel(v)
			if err != nil {
				return err
			}

			c.Logging.Level = level

			return nil
		},
	},
	{
		key:  "logging.directory",
		help: "where log files are written; empty uses the platform default",
		get:  func(c *Config) string { return c.Logging.Directory },
		set:  func(c *Config, v string) error { c.Logging.Directory = v; return nil },
	},
	{
		key:  "logging.max_size_mb",
		help: "rotate the log once it reaches this many megabytes",
		get:  func(c *Config) string { return strconv.Itoa(c.Logging.MaxSizeMB) },
		set:  intSetter(func(c *Config) *int { return &c.Logging.MaxSizeMB }, 1, 4096),
	},
	{
		key:  "logging.max_backups",
		help: "how many rotated log files to keep",
		get:  func(c *Config) string { return strconv.Itoa(c.Logging.MaxBackups) },
		set:  intSetter(func(c *Config) *int { return &c.Logging.MaxBackups }, 0, 1000),
	},
	{
		key:  "logging.max_age_days",
		help: "delete rotated log files older than this many days",
		get:  func(c *Config) string { return strconv.Itoa(c.Logging.MaxAgeDays) },
		set:  intSetter(func(c *Config) *int { return &c.Logging.MaxAgeDays }, 0, 36500),
	},
	{
		key:  "logging.compress",
		help: "gzip rotated log files",
		get:  func(c *Config) string { return strconv.FormatBool(c.Logging.Compress) },
		set:  boolSetter(func(c *Config) *bool { return &c.Logging.Compress }),
	},

	{
		key:  "advanced.enable_experimental",
		help: "expose features that are not finished",
		get:  func(c *Config) string { return strconv.FormatBool(c.Advanced.EnableExperimental) },
		set:  boolSetter(func(c *Config) *bool { return &c.Advanced.EnableExperimental }),
	},

	{
		key:  "security.allow_action_commands",
		help: "let game actions start external programs",
		get:  func(c *Config) string { return strconv.FormatBool(c.Security.AllowsActionCommands()) },
		set: func(c *Config, v string) error {
			parsed, err := parseConfigBool(v)
			if err != nil {
				return err
			}

			c.Security.AllowActionCommands = &parsed

			return nil
		},
	},
}

// ConfigKeys returns every key GetConfigValue and SetConfigValue understand, with
// a one-line description each. Source block keys are not listed because their ids
// vary; the pattern is "sources.<id>.enabled".
func ConfigKeys() []string {
	out := make([]string, 0, len(configFields)+3)
	for _, f := range configFields {
		out = append(out, f.key)
	}

	out = append(out, "sources.<id>.enabled", "sources.<id>.api_key", "sources.<id>.paths")

	// <capability> is one of fs_read, fs_write, config_read, config_write,
	// storage_read, env_read or timeout_seconds.
	out = append(out,
		"games.<id>.path",
		"games.<id>.source",
		"games.<id>.mods_folder_override",
		"games.<id>.auto_detect",
		"games.<id>.install_method",
		"games.<id>.icon",
		"hooks.permissions.default.<capability>",
		"hooks.permissions.games.<id>.<capability>",
		"hooks.permissions.mods.<id>.<capability>",
	)

	return out
}

// boolSetter builds a setter that parses a boolean, refusing anything else.
// strconv.ParseBool is deliberately not used: it accepts "1", "t" and "TRUE",
// which is fine for a flag and wrong for a value a user typed into a settings field.
func boolSetter(target func(*Config) *bool) func(*Config, string) error {
	return func(cfg *Config, value string) error {
		parsed, err := parseConfigBool(value)
		if err != nil {
			return err
		}

		*target(cfg) = parsed

		return nil
	}
}

// intSetter builds a setter that parses an integer within bounds. The bounds
// matter: a cache limit of -1 would read as "no limit" to the cache manager, which
// is not what someone typing -1 into a size field meant.
func intSetter(target func(*Config) *int, min, max int) func(*Config, string) error {
	return func(cfg *Config, value string) error {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"%q is not a whole number", value)
		}

		if parsed < min || parsed > max {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"%d is out of range: expected %d to %d", parsed, min, max)
		}

		*target(cfg) = parsed

		return nil
	}
}

// durationSetter builds a setter for hook timeouts, clamped to
// [minSeconds, maxSeconds]. The value is a Go duration string ("10s") or a bare
// number of seconds — the second spelling because "30" is what every previous
// release accepted.
func durationSetter(
	target func(*Config) *domain.Duration,
	minSeconds, maxSeconds int,
) func(*Config, string) error {
	return func(cfg *Config, value string) error {
		trimmed := strings.TrimSpace(value)

		var (
			parsed time.Duration
			err    error
		)

		if secs, numErr := strconv.Atoi(trimmed); numErr == nil {
			parsed = time.Duration(secs) * time.Second
		} else {
			parsed, err = time.ParseDuration(trimmed)
			if err != nil {
				return appErrors.New(appErrors.ErrCodeInvalidArgument,
					"%q is not a duration", value)
			}
		}

		seconds := int(parsed / time.Second)
		if seconds < minSeconds || seconds > maxSeconds {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"%q is out of range: expected %ds to %ds", value, minSeconds, maxSeconds)
		}

		*target(cfg) = domain.DurationOf(parsed)

		return nil
	}
}

// parseConfigBool accepts the spellings a person types into a settings field.
func parseConfigBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}

	return false, appErrors.New(appErrors.ErrCodeInvalidArgument,
		"%q is not a yes/no value", strings.TrimSpace(value))
}

// secretConfigKeys are the keys holding credentials. They are writable and not
// readable, so a UI cannot render one into a screenshot or a diagnostics dump.
var secretConfigKeys = map[string]bool{
	"sources.<id>.api_key": true,
}

// ConfigValue reads one setting by dotted key. It is the free-function form of
// Client.GetConfigValue, for a caller that already holds a configuration; see it
// for the key syntax and the refusal to read back a credential.
func ConfigValue(cfg *Config, key string) (string, error) {
	if cfg == nil {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"configuration must not be nil")
	}

	normalized := normalizeConfigKey(key)
	if normalized == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"configuration key must not be blank")
	}

	for _, field := range configFields {
		if field.key != normalized {
			continue
		}

		if secretConfigKeys[normalized] {
			return "", secretReadError(normalized)
		}

		return field.get(cfg), nil
	}

	if sourceID, sub, ok := splitSourceKey(normalized); ok {
		return sourceValue(cfg, sourceID, sub)
	}

	if gameID, sub, ok := splitGameKey(normalized); ok {
		return gameValue(cfg, gameID, sub)
	}

	if scope, ok := splitPermissionKey(normalized); ok {
		return scope.read(cfg)
	}

	return "", appErrors.New(appErrors.ErrCodeNotFound,
		"no such configuration key: %q", strings.TrimSpace(key))
}

// SetConfigValue writes one setting by dotted key into cfg. It does not save —
// that is Client.SetConfigValue's job — so a caller can apply several changes and
// write once.
func SetConfigValue(cfg *Config, key, value string) error {
	if cfg == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"configuration must not be nil")
	}

	normalized := normalizeConfigKey(key)
	if normalized == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"configuration key must not be blank")
	}

	for _, field := range configFields {
		if field.key == normalized {
			return setField(field, cfg, strings.TrimSpace(value))
		}
	}

	if sourceID, sub, ok := splitSourceKey(normalized); ok {
		if err := setSourceValue(cfg, sourceID, sub, strings.TrimSpace(value)); err != nil {
			return nameKeyError(err, "sources."+sourceID+"."+sub)
		}

		return nil
	}

	if gameID, sub, ok := splitGameKey(normalized); ok {
		if err := setGameValue(cfg, gameID, sub, strings.TrimSpace(value)); err != nil {
			return nameKeyError(err, "games."+gameID+"."+sub)
		}

		return nil
	}

	if scope, ok := splitPermissionKey(normalized); ok {
		if err := scope.write(cfg, strings.TrimSpace(value)); err != nil {
			return nameKeyError(err, scope.key())
		}

		return nil
	}

	return appErrors.New(appErrors.ErrCodeNotFound,
		"no such configuration key: %q", strings.TrimSpace(key))
}

// setField applies one field's setter and, on failure, says which field failed. The
// key is added here, not in every setter: twenty-odd setters each repeating
// it would be twenty-odd chances to forget one.
func setField(field configField, cfg *Config, value string) error {
	if err := field.set(cfg, value); err != nil {
		return nameKeyError(err, field.key)
	}

	return nil
}

// nameKeyError restates a parse failure with the key it belongs to. The cause is
// deliberately dropped, not wrapped: there is no sentinel to match against,
// and wrapping would render the code twice with the useful half buried.
func nameKeyError(err error, key string) error {
	return appErrors.New(appErrors.CodeOf(err), "cannot set %s: %s", key, appErrors.Message(err))
}

// normalizeConfigKey folds a key so "Game.Path", "game.path" and " game.path "
// are the same key: a settings screen should not have to care how it was spelled.
func normalizeConfigKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// secretReadError explains the refusal in terms the caller can act on.
func secretReadError(key string) error {
	return appErrors.New(appErrors.ErrCodeNotFound,
		"%s holds a credential and is not readable through the configuration API", key)
}

// splitSourceKey breaks "sources.<id>.<field>" apart. The id is passed on as the
// user spelled it; folding its case is the writer's job — a read must find a block
// however it is capitalised.
func splitSourceKey(key string) (sourceID, field string, ok bool) {
	parts := strings.Split(key, ".")
	switch len(parts) {
	case 3:
		if parts[0] != "sources" {
			return "", "", false
		}

		id := strings.TrimSpace(parts[1])
		if id == "" {
			return "", "", false
		}

		return id, parts[2], true
	case 4:
		// "sources.<id>.options.<name>": one extra segment for a provider's own
		// setting, which is where api_base, topic and repositories live.
		if parts[0] != "sources" || parts[2] != "options" {
			return "", "", false
		}

		id := strings.TrimSpace(parts[1])
		if id == "" || strings.TrimSpace(parts[3]) == "" {
			return "", "", false
		}

		return id, "options." + parts[3], true
	}

	return "", "", false
}

// sourceValue reads one field of one source block. An unknown source id is an
// error, not a default: answering "enabled: true" for a typo would be worse
// than failing.
func sourceValue(cfg *Config, sourceID, field string) (string, error) {
	block, ok := cfg.SourceConfigFor(sourceID)
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeNotFound,
			"no configuration for source %q", sourceID)
	}

	switch {
	case field == "enabled":
		return strconv.FormatBool(block.Enabled), nil
	case field == "api_key":
		return "", secretReadError("sources." + sourceID + ".api_key")
	case field == "paths":
		return strings.Join(block.Paths, ","), nil
	case strings.HasPrefix(field, "options."):
		name := strings.TrimPrefix(field, "options.")

		value, ok := block.Options[name]
		if !ok {
			return "", appErrors.New(appErrors.ErrCodeNotFound,
				"source %q has no option %q", sourceID, name)
		}

		return value, nil
	}

	return "", appErrors.New(appErrors.ErrCodeNotFound,
		"source %q has no setting %q", sourceID, field)
}

// setSourceValue writes one field of one source block, creating the block if it
// does not exist. The block is addressed by the key it already has, so
// "sources.Nexus.api_key" updates the "nexus" block instead of creating a second
// one: two blocks for one source disagree about Enabled.
func setSourceValue(cfg *Config, sourceID, field, value string) error {
	if cfg.Sources == nil {
		cfg.Sources = make(map[string]domain.SourceConfig)
	}

	key, ok := cfg.SourceKey(sourceID)
	if !ok {
		key = strings.ToLower(strings.TrimSpace(sourceID))
	}

	if key == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "source id must not be empty")
	}

	block, exists := cfg.Sources[key]
	if !exists {
		// Enabled, because sources.RegisterAll treats "no block" as "use the source"
		// and only honours an explicit false — and a first-time write is an API key.
		block = domain.SourceConfig{Enabled: true}
	}

	switch {
	case field == "enabled":
		parsed, err := parseConfigBool(value)
		if err != nil {
			return err
		}

		block.Enabled = parsed
	case field == "api_key":
		block.APIKey = value
	case field == "paths":
		list := make([]string, 0, 2)

		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r'
		}) {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				list = append(list, trimmed)
			}
		}

		block.Paths = list
	case strings.HasPrefix(field, "options."):
		name := strings.TrimPrefix(field, "options.")

		if block.Options == nil {
			block.Options = make(map[string]string)
		}

		block.Options[name] = value
	default:
		return appErrors.New(appErrors.ErrCodeNotFound,
			"source %q has no setting %q", sourceID, field)
	}

	cfg.Sources[key] = block

	return nil
}

// splitGameKey breaks "games.<id>.<field>" apart, reporting false for anything that
// is not three segments starting with "games". The id is already folded by
// normalizeConfigKey, matching the registry: game ids are lowercase everywhere.
func splitGameKey(key string) (gameID, field string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "games" {
		return "", "", false
	}

	id := strings.TrimSpace(parts[1])
	if id == "" {
		return "", "", false
	}

	return id, parts[2], true
}

// registeredGame looks up a game's definition by config id, answering nil when the
// id names a game this build does not have. Callers treat nil as "no preference to
// report", not an error, since a configuration may describe a removed game.
func registeredGame(gameID string) domain.GameDefinition {
	def, ok := games.Get(strings.TrimSpace(gameID))
	if !ok {
		return nil
	}

	return def
}

// gameValue reads one field of one game's settings. A game with no entry yet answers
// with the defaults, not an error: asking about a game never pointed at a path
// is a normal question, and the defaults are truthful.
func gameValue(cfg *Config, gameID, field string) (string, error) {
	settings, ok := cfg.GameSettingsFor(gameID)
	if !ok {
		settings = domain.DefaultGameSettings()
	}

	switch field {
	case "path":
		return settings.Path, nil
	case "source":
		return string(settings.Source), nil
	case "mods_folder_override":
		return settings.ModsFolderOverride, nil
	case "auto_detect":
		return strconv.FormatBool(settings.AutoDetect), nil
	case "install_method":
		// An unset override reads as the game's own preference, never an empty
		// string: blank would answer "what happens if I never touch this?" with
		// nothing.
		if method := settings.InstallMethodOrDefault(registeredGame(gameID)); method != "" {
			return method, nil
		}

		return "", nil
	case "icon":
		return settings.Icon, nil
	}

	return "", appErrors.New(appErrors.ErrCodeNotFound,
		"game %q has no setting %q", gameID, field)
}

// setGameValue writes one field of one game's settings, creating the entry if it
// does not exist. The id is not checked against the registry: a configuration may
// describe a game the process cannot switch to yet; a typo is caught by UseGame.
func setGameValue(cfg *Config, gameID, field, value string) error {
	settings, ok := cfg.GameSettingsFor(gameID)
	if !ok {
		settings = domain.DefaultGameSettings()
	}

	switch field {
	case "path":
		settings.Path = value
	case "source":
		settings.Source = domain.GameSource(strings.TrimSpace(value))
	case "mods_folder_override":
		settings.ModsFolderOverride = value
	case "auto_detect":
		parsed, err := parseConfigBool(value)
		if err != nil {
			return err
		}

		settings.AutoDetect = parsed
	case "install_method":
		// Blank clears the override and hands the decision back to the game
		// definition, so a user who tried an override has a way out.
		if value != "" && !domain.ValidInstallMethod(value) {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"install method must be %q or %q",
				domain.InstallMethodSymlink, domain.InstallMethodDirect)
		}

		settings.InstallMethod = value
	case "icon":
		// Not a checked-in enum: a built-in choice or a data URL image the frontend
		// validates, and an unknown one falls back to the generic pad (not an error).
		settings.Icon = strings.TrimSpace(value)
	default:
		return appErrors.New(appErrors.ErrCodeNotFound,
			"game %q has no setting %q", gameID, field)
	}

	cfg.SetGameSettings(gameID, settings)

	return nil
}

// permissionScope names one hooks.permissions block — the default, one game or
// one mod — plus the capability inside it.
type permissionScope struct {
	kind string // "default", "games" or "mods"
	id   string // blank for the default scope
	cap  string
}

// key rebuilds the dotted key, for naming a failure at its origin.
func (s permissionScope) key() string {
	if s.kind == "default" {
		return "hooks.permissions.default." + s.cap
	}

	return "hooks.permissions." + s.kind + "." + s.id + "." + s.cap
}

// splitPermissionKey breaks a hooks.permissions key apart. Four segments name
// a capability on the default scope; five name one on a game or a mod.
func splitPermissionKey(key string) (permissionScope, bool) {
	parts := strings.Split(key, ".")
	if len(parts) < 2 || parts[0] != "hooks" || parts[1] != "permissions" {
		return permissionScope{}, false
	}

	switch {
	case len(parts) == 4 && parts[2] == "default":
		if strings.TrimSpace(parts[3]) == "" {
			return permissionScope{}, false
		}

		return permissionScope{kind: "default", cap: parts[3]}, true
	case len(parts) == 5 && (parts[2] == "games" || parts[2] == "mods"):
		id := strings.TrimSpace(parts[3])
		if id == "" || strings.TrimSpace(parts[4]) == "" {
			return permissionScope{}, false
		}

		return permissionScope{kind: parts[2], id: id, cap: parts[4]}, true
	}

	return permissionScope{}, false
}

// permissionCaps maps each boolean capability name to its field. timeout_seconds
// is handled separately: it is an integer ceiling, not a switch.
var permissionCaps = map[string]func(*domain.ScriptPermissions) *bool{
	"config_read":  func(p *domain.ScriptPermissions) *bool { return &p.ConfigRead },
	"config_write": func(p *domain.ScriptPermissions) *bool { return &p.ConfigWrite },
	"env_read":     func(p *domain.ScriptPermissions) *bool { return &p.EnvRead },
	"fs_read":      func(p *domain.ScriptPermissions) *bool { return &p.FSRead },
	"fs_write":     func(p *domain.ScriptPermissions) *bool { return &p.FSWrite },
	"storage_read": func(p *domain.ScriptPermissions) *bool { return &p.StorageRead },
}

// resolve returns the scope's current block and the key it lives under in its map
// (empty for the default scope). A scope with no block of its own answers with the
// zero permissions — no grant — which is the truth about a mod never given one.
func (s permissionScope) resolve(cfg *Config) (domain.ScriptPermissions, string) {
	switch s.kind {
	case "games":
		return lookupPermission(cfg.Hooks.Permissions.Games, s.id)
	case "mods":
		return lookupPermission(cfg.Hooks.Permissions.Mods, s.id)
	}

	return cfg.Hooks.Permissions.Default, ""
}

// lookupPermission finds a scope's block however it was capitalised, returning the
// key it is stored under so a write lands in the existing entry, not a second one.
func lookupPermission(
	blocks map[string]domain.ScriptPermissions,
	id string,
) (domain.ScriptPermissions, string) {
	if block, ok := blocks[id]; ok {
		return block, id
	}

	for key, block := range blocks {
		if strings.EqualFold(key, id) {
			return block, key
		}
	}

	return domain.ScriptPermissions{}, id
}

// read returns one capability in its string form: "true"/"false" for the
// switches, the count of seconds for the timeout cap.
func (s permissionScope) read(cfg *Config) (string, error) {
	block, _ := s.resolve(cfg)

	if s.cap == "timeout_seconds" {
		return strconv.Itoa(block.TimeoutSeconds), nil
	}

	field, ok := permissionCaps[s.cap]
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeNotFound,
			"unknown hook permission %q", s.cap)
	}

	return strconv.FormatBool(*field(&block)), nil
}

// write applies one capability to its scope, creating the scope's block if it does
// not exist yet. A capability of 0 (timeout_seconds) is stored as written: zero
// means "no cap below the configured ceiling", which is a real setting.
func (s permissionScope) write(cfg *Config, value string) error {
	block, key := s.resolve(cfg)

	if s.cap != "timeout_seconds" {
		field, ok := permissionCaps[s.cap]
		if !ok {
			return appErrors.New(appErrors.ErrCodeNotFound,
				"unknown hook permission %q", s.cap)
		}

		parsed, err := parseConfigBool(value)
		if err != nil {
			return err
		}

		*field(&block) = parsed
	} else {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"%q is not a whole number", value)
		}

		if parsed < 0 || parsed > 86400 {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"%d is out of range: expected 0 to 86400", parsed)
		}

		block.TimeoutSeconds = parsed
	}

	s.store(cfg, key, block)

	return nil
}

// DefaultPaths are the platform folders the manager falls back to when the
// stored configuration leaves storage or cache empty. Read-only.
type DefaultPaths struct {
	Storage string `json:"storage"`
	Cache   string `json:"cache"`
}

// store writes the block back into its scope, creating the map when the
// configuration has none yet.
func (s permissionScope) store(cfg *Config, key string, block domain.ScriptPermissions) {
	switch s.kind {
	case "games":
		if cfg.Hooks.Permissions.Games == nil {
			cfg.Hooks.Permissions.Games = make(map[string]domain.ScriptPermissions)
		}

		cfg.Hooks.Permissions.Games[key] = block
	case "mods":
		if cfg.Hooks.Permissions.Mods == nil {
			cfg.Hooks.Permissions.Mods = make(map[string]domain.ScriptPermissions)
		}

		cfg.Hooks.Permissions.Mods[key] = block
	default:
		cfg.Hooks.Permissions.Default = block
	}
}
