package api

import (
	"context"
	"strconv"
	"strings"

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

// GetConfig returns a copy of the manager's configuration.
//
// The copy matters: the store's Load returns a fresh value each call, and handing
// that out directly would let a caller mutate what the next caller sees. A
// missing file yields defaults rather than an error, so first run needs no
// special case.
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

// UpdateConfig replaces the manager's configuration.
//
// Defaults are applied first, so a caller can hand over a partial configuration
// without having to fill in every block, and a zero that would disable integrity
// checking cannot reach disk. The write is atomic and only happens when the value
// actually differs, so calling this on a timer does not rewrite the file — which
// matters because a rewritten config file has a new modification time, and tools
// that back up on change would back up on nothing.
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
// yields defaults and true, matching how the registry treats an absent block: a
// fresh install has no blocks and should still show its bundled sources.
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

// GetConfigValue reads one setting by dotted key, for example
// "cache.max_size_gb" or "sources.vortex.api_key". A key naming an absent value
// reports ErrCodeNotFound rather than an empty string, so a caller can tell a
// setting that is unset from one that is set to nothing.
//
// Source blocks are addressable as "sources.<id>.enabled", "sources.<id>.api_key"
// and "sources.<id>.paths". An api_key is deliberately not readable through this
// function — see SetConfigValue for why the write side is different.
func (c *Client) GetConfigValue(ctx context.Context, key string) (string, error) {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return "", err
	}

	return ConfigValue(cfg, key)
}

// SetConfigValue changes one setting by dotted key and saves the result.
//
// An api_key may be written but not read back: a credential a caller cannot read
// is a credential a caller cannot accidentally log, and the configuration file is
// already the plaintext home of the key. Reading it back through GetConfigValue
// reports ErrCodeNotFound so a UI cannot render it into a page it will later
// serialise into a bug report.
//
// The value is parsed as the destination type, so a caller cannot store the
// string "banana" in a boolean and discover it at start-up.
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

// configField is one addressable setting. A table rather than reflection: the set
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
		key:  "game.path",
		help: "Darktide installation directory",
		get:  func(c *Config) string { return c.Game.Path },
		set:  func(c *Config, v string) error { c.Game.Path = v; return nil },
	},
	{
		key:  "game.source",
		help: "launcher to prefer when auto-detecting",
		get:  func(c *Config) string { return c.Game.Source },
		set:  func(c *Config, v string) error { c.Game.Source = v; return nil },
	},
	{
		key:  "game.mods_folder_override",
		help: "deploy into this folder instead of the detected one",
		get:  func(c *Config) string { return c.Game.ModsFolderOverride },
		set:  func(c *Config, v string) error { c.Game.ModsFolderOverride = v; return nil },
	},
	{
		key:  "game.auto_detect",
		help: "probe known launcher locations on start-up",
		get:  func(c *Config) string { return strconv.FormatBool(c.Game.AutoDetect) },
		set:  boolSetter(func(c *Config) *bool { return &c.Game.AutoDetect }),
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

	// The hook timeouts are plain seconds in the domain type, not durations, so
	// they read and write as integers like every other numeric setting.
	{
		key:  "hooks.default_timeout",
		help: "seconds a hook may run when it declares no timeout",
		get:  func(c *Config) string { return strconv.Itoa(c.Hooks.DefaultTimeout) },
		set:  intSetter(func(c *Config) *int { return &c.Hooks.DefaultTimeout }, 1, 3600),
	},
	{
		key:  "hooks.max_timeout",
		help: "longest timeout a hook may declare, in seconds",
		get:  func(c *Config) string { return strconv.Itoa(c.Hooks.MaxTimeout) },
		set:  intSetter(func(c *Config) *int { return &c.Hooks.MaxTimeout }, 1, 86400),
	},
	{
		key:  "hooks.allow_network",
		help: "allow hooks to reach the network",
		get:  func(c *Config) string { return strconv.FormatBool(c.Hooks.AllowNetwork) },
		set:  boolSetter(func(c *Config) *bool { return &c.Hooks.AllowNetwork }),
	},
	{
		key:  "hooks.allow_file_write",
		help: "allow hooks to write files",
		get:  func(c *Config) string { return strconv.FormatBool(c.Hooks.AllowFileWrite) },
		set:  boolSetter(func(c *Config) *bool { return &c.Hooks.AllowFileWrite }),
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

	// The logging block is listed before advanced.log_level, which stays
	// settable so an existing script does not break — it is read only as a
	// fallback for a config that predates this block (see migrateLevel), and
	// writing both would leave two settings that disagree.
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
		key:  "advanced.log_level",
		help: "deprecated: use logging.level",
		get:  func(c *Config) string { return c.Advanced.LogLevel },
		set: func(c *Config, v string) error {
			level, err := parseLogLevel(v)
			if err != nil {
				return err
			}

			c.Advanced.LogLevel = level

			return nil
		},
	},
}

// ConfigKeys returns every key GetConfigValue and SetConfigValue understand, with
// a one-line description each. Source blocks are not listed because their keys
// depend on the source ids; the pattern is "sources.<id>.enabled".
func ConfigKeys() []string {
	out := make([]string, 0, len(configFields)+3)
	for _, f := range configFields {
		out = append(out, f.key)
	}

	out = append(out, "sources.<id>.enabled", "sources.<id>.api_key", "sources.<id>.paths")

	return out
}

// boolSetter builds a setter that parses a boolean, refusing anything else.
// strconv.ParseBool is deliberately not used: it accepts "1", "t" and "TRUE",
// which is fine for a flag and wrong for a value a user typed into a settings
// field.
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
// matter: a cache limit of -1 would be read as "no limit" by the cache manager,
// which is not what someone typing -1 into a size field meant.
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
// Client.GetConfigValue, for a caller that already holds a configuration.
//
// See Client.GetConfigValue for the key syntax and the refusal to read back a
// credential.
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

	return appErrors.New(appErrors.ErrCodeNotFound,
		"no such configuration key: %q", strings.TrimSpace(key))
}

// setField applies one field's setter and, on failure, says which field failed.
//
// The key is added here rather than in every setter: a settings form has to tell
// the user which box they got wrong, and twenty-odd setters each repeating the
// key would be twenty-odd chances to forget one.
func setField(field configField, cfg *Config, value string) error {
	if err := field.set(cfg, value); err != nil {
		return nameKeyError(err, field.key)
	}

	return nil
}

// nameKeyError restates a parse failure with the key it belongs to.
//
// The cause is deliberately dropped rather than wrapped: these errors are created
// on the spot with no sentinel to match against, and wrapping would render as
// "INVALID_ARGUMENT: cannot set cache.max_size_gb: INVALID_ARGUMENT: ..." — the
// code twice, with the useful half buried.
func nameKeyError(err error, key string) error {
	return appErrors.New(appErrors.CodeOf(err), "cannot set %s: %s", key, appErrors.Message(err))
}

// normalizeConfigKey folds a key so "Game.Path", "game.path" and " game.path "
// are the same key. A settings screen should not have to care how a caller
// spelled it.
func normalizeConfigKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// secretReadError explains the refusal in terms the caller can act on.
func secretReadError(key string) error {
	return appErrors.New(appErrors.ErrCodeNotFound,
		"%s holds a credential and is not readable through the configuration API", key)
}

// splitSourceKey breaks "sources.<id>.<field>" apart. It reports false for
// anything that is not three or four segments starting with "sources", which
// leaves the caller to report an unknown key.
//
// The id is passed on as the user spelled it; folding its case is the writer's
// job, not the parser's. A read has to find a block however it is capitalised, and
// a write has to land in the block that already exists — see setSourceValue.
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
// error rather than a default: the caller asked about a source that does not
// exist, and answering "enabled: true" for a typo would be worse than failing.
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
// does not exist yet — writing a setting for a source with no block is how a
// block appears in the first place.
//
// The block is addressed by the key it already has, so a settings screen that
// sends "sources.Nexus.api_key" updates the "nexus" block instead of creating a
// second one beside it. Two blocks for one source is not a harmless duplicate:
// they disagree about Enabled, and a settings screen saving the one it read while
// the manager used the other would look like the switch was ignored.
//
// A block that has to be created is created enabled; see below.
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
		// A block that is being created is enabled, because a source with no block
		// at all is enabled: sources.RegisterAll treats "no block" as "use the
		// source" and only honours an explicit false. Writing a setting for the
		// first time — the usual way an API key arrives — would otherwise store
		// the block that says the opposite and silently switch the source off.
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
