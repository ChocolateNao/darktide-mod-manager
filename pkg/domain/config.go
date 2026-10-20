package domain

import (
	"strings"
	"time"
)

// Config is the complete user configuration. Every field has a usable default so
// that a zero Config is still valid; ApplyDefaults fills in anything unset.
type Config struct {
	Hooks        HooksConfig             `json:"hooks"`
	Sources      map[string]SourceConfig `json:"sources"`
	Games        map[string]GameSettings `json:"games,omitempty"`
	ActiveGame   string                  `json:"active_game"`
	UI           UIConfig                `json:"ui"`
	Advanced     AdvancedConfig          `json:"advanced"`
	Storage      StorageConfig           `json:"storage"`
	AutoUpdate   AutoUpdateConfig        `json:"auto_update"`
	Cache        CacheConfig             `json:"cache"`
	CloudSharing CloudSharingConfig      `json:"cloud_sharing"`
	Sharing      SharingConfig           `json:"sharing"`
	Logging      LoggingConfig           `json:"logging"`
}

// ScriptPermissions is the capability set one hook script runs with. A capability
// not granted here is absent from the host API entirely; there is no deny-at-runtime
// path, the binding simply does not exist.
type ScriptPermissions struct {
	FSRead      bool `json:"fs_read"`
	FSWrite     bool `json:"fs_write"`
	ConfigRead  bool `json:"config_read"`
	ConfigWrite bool `json:"config_write"`
	StorageRead bool `json:"storage_read"`
	EnvRead     bool `json:"env_read"`
	// TimeoutSeconds caps the execution time of a single hook script in this
	// scope. 0 = no cap at this scope, fall through to the next broader one.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// DefaultScriptPermissions is the built-in capability set: scripts may read the
// confined file system, config, and the storage inventory, but not write anything.
func DefaultScriptPermissions() ScriptPermissions {
	return ScriptPermissions{
		FSRead:      true,
		ConfigRead:  true,
		StorageRead: true,
	}
}

// Or merges other underneath p, per capability: the first true value wins. Resolving
// mod > game > default is a chain of Or calls, most specific scope on the left.
func (p ScriptPermissions) Or(other ScriptPermissions) ScriptPermissions {
	out := p
	if !out.FSRead {
		out.FSRead = other.FSRead
	}

	if !out.FSWrite {
		out.FSWrite = other.FSWrite
	}

	if !out.ConfigRead {
		out.ConfigRead = other.ConfigRead
	}

	if !out.ConfigWrite {
		out.ConfigWrite = other.ConfigWrite
	}

	if !out.StorageRead {
		out.StorageRead = other.StorageRead
	}

	if !out.EnvRead {
		out.EnvRead = other.EnvRead
	}

	if out.TimeoutSeconds <= 0 {
		out.TimeoutSeconds = other.TimeoutSeconds
	}

	return out
}

// HooksPermissionsConfig scopes ScriptPermissions by game id and mod id.
type HooksPermissionsConfig struct {
	Games   map[string]ScriptPermissions `json:"games,omitempty"`
	Mods    map[string]ScriptPermissions `json:"mods,omitempty"`
	Default ScriptPermissions            `json:"default"`
}

// For returns the permissions for the given game and mod ids, merged mod > game >
// default. A scope with no entry contributes a zero block, which Or ignores.
func (c HooksPermissionsConfig) For(gameID, modID string) ScriptPermissions {
	out := c.Default
	if perms, ok := c.Games[gameID]; ok {
		out = perms.Or(out)
	}

	if perms, ok := c.Mods[modID]; ok {
		out = perms.Or(out)
	}

	return out
}

// HooksConfig controls hook execution policy.
type HooksConfig struct {
	Permissions    HooksPermissionsConfig `json:"permissions"`
	DefaultTimeout Duration               `json:"default_timeout"`
	MaxTimeout     Duration               `json:"max_timeout"`
}

// StorageConfig controls where mod files live.
type StorageConfig struct {
	// Path is the manager's data root (mods, symlink targets). Empty means the
	// platform default data directory.
	Path string `json:"path"`
	// BackupRetentionDays is how long old mod versions are kept after an update.
	BackupRetentionDays int `json:"backup_retention_days"`
}

// CacheConfig controls the download cache.
type CacheConfig struct {
	Path         string `json:"path"`
	MaxSizeGB    int    `json:"max_size_gb"`
	MaxAgeDays   int    `json:"max_age_days"`
	VerifyHashes bool   `json:"verify_hashes"`
}

// CloudSharingConfig controls the sharing subsystem.
type CloudSharingConfig struct {
	Providers         map[string]map[string]string `json:"providers,omitempty"`
	DefaultProvider   string                       `json:"default_provider"`
	FallbackProviders []string                     `json:"fallback_providers,omitempty"`
	ShareExpiryDays   int                          `json:"share_expiry_days"`
	AutoInstall       bool                         `json:"auto_install"`
}

// SharingConfig groups the two ways mods are handed to somebody else. The cloud block
// is kept for configurations that still name it, but a share no longer needs a
// storage provider.
type SharingConfig struct {
	// P2P holds the transport settings for direct sharing.
	P2P P2PSharingConfig `json:"p2p"`
}

// P2PSharingConfig holds the transport settings for direct sharing. Nothing here can
// turn the data channel off: bytes always travel end-to-end over DTLS, and a STUN
// server only discovers which address the peer is reachable at.
type P2PSharingConfig struct {
	// STUNServers are consulted before the built-in default. A STUN server
	// learns the client's public address and nothing else about the transfer.
	STUNServers []string `json:"stun_servers,omitempty"`
	// TURNRelays fall back when no direct path exists. A relay forwards every
	// byte, so this is opt-in and never filled in by default.
	TURNRelays []string `json:"turn_relays,omitempty"`
	// ExpiryDays makes every new share expire after this many days. Zero means
	// a share is permanent unless the sender says otherwise on the command.
	ExpiryDays int `json:"expiry_days"`
	// ChunkSizeKB overrides the data channel chunk size, in kilobytes. Zero
	// keeps the protocol default.
	ChunkSizeKB int `json:"chunk_size_kb,omitempty"`
}

// SourceConfig is the per-source configuration block.
type SourceConfig struct {
	Options map[string]string `json:"options,omitempty"`
	APIKey  string            `json:"api_key,omitempty"`
	Paths   []string          `json:"paths,omitempty"`
	Enabled bool              `json:"enabled"`
}

// AutoUpdateConfig controls background update checks.
//
// NotifyOnUpdate defaults to true in DefaultConfig and ApplyDefaults leaves it alone:
// JSON cannot tell an absent boolean from an explicit false. See ApplyDefaults.
type AutoUpdateConfig struct {
	LastCheck             string `json:"last_check,omitempty"`
	CheckIntervalHours    int    `json:"check_interval_hours"`
	Enabled               bool   `json:"enabled"`
	DownloadAutomatically bool   `json:"download_automatically"`
	NotifyOnUpdate        bool   `json:"notify_on_update"`
}

// UIConfig holds presentation preferences. It never affects mod behaviour.
type UIConfig struct {
	Theme       string `json:"theme"`
	Language    string `json:"language"`
	CompactMode bool   `json:"compact_mode"`
}

// AdvancedConfig holds escape hatches.
type AdvancedConfig struct {
	LogLevel           string `json:"log_level"`
	EnableExperimental bool   `json:"enable_experimental"`
}

// LoggingConfig is the persisted half of the logging settings. It carries none of the
// process-only switches, so a flag used once cannot become a permanent preference.
type LoggingConfig struct {
	Level      string   `json:"level"`
	Directory  string   `json:"directory,omitempty"`
	Redact     []string `json:"redact,omitempty"`
	MaxSizeMB  int      `json:"max_size_mb"`
	MaxBackups int      `json:"max_backups"`
	MaxAgeDays int      `json:"max_age_days"`
	Compress   bool     `json:"compress"`
}

// DefaultConfig returns the configuration the manager uses when nothing is set.
func DefaultConfig() Config {
	return Config{
		Storage: StorageConfig{
			BackupRetentionDays: 7,
		},
		Cache: CacheConfig{
			MaxSizeGB:    5,
			MaxAgeDays:   30,
			VerifyHashes: true,
		},
		CloudSharing: CloudSharingConfig{
			ShareExpiryDays: 30,
		},
		Sharing: SharingConfig{
			// Expiry stays opt-in: a share with no expiry is permanent, which is
			// what somebody handing a link to a friend expects it to mean.
			P2P: P2PSharingConfig{},
		},
		Hooks: HooksConfig{
			DefaultTimeout: DurationOf(10 * time.Second),
			MaxTimeout:     DurationOf(30 * time.Second),
			Permissions: HooksPermissionsConfig{
				Default: DefaultScriptPermissions(),
			},
		},
		Sources: map[string]SourceConfig{},
		AutoUpdate: AutoUpdateConfig{
			Enabled:            false,
			CheckIntervalHours: 24,
			NotifyOnUpdate:     true,
		},
		UI: UIConfig{
			Theme:    "system",
			Language: "en",
		},
		Logging: LoggingConfig{
			Level:      "info",
			MaxSizeMB:  10,
			MaxBackups: 5,
			MaxAgeDays: 30,
			Compress:   true,
		},
		Advanced: AdvancedConfig{
			LogLevel: "info",
		},
	}
}

// ApplyDefaults fills unset fields from DefaultConfig and repairs nonsensical values,
// so the rest of the codebase can trust the config it is handed. Explicit user values
// always win.
//
// AutoUpdate.NotifyOnUpdate is the exception: JSON cannot distinguish an absent key
// from an explicit false, so it is read exactly as written and a hand-written file
// that omits it behaves as false.
func (c *Config) ApplyDefaults() {
	if c == nil {
		return
	}

	d := DefaultConfig()

	if c.Games == nil {
		c.Games = map[string]GameSettings{}
	}

	// JSON cannot tell an absent Games entry from one spelled {}, so a
	// present-but-zero block is deliberate, not unset.

	if c.Storage.BackupRetentionDays <= 0 {
		c.Storage.BackupRetentionDays = d.Storage.BackupRetentionDays
	}

	if c.Cache.MaxSizeGB <= 0 {
		c.Cache.MaxSizeGB = d.Cache.MaxSizeGB
	}

	if c.Cache.MaxAgeDays <= 0 {
		c.Cache.MaxAgeDays = d.Cache.MaxAgeDays
	}

	if !c.Cache.VerifyHashes {
		c.Cache.VerifyHashes = true
	}

	// A negative expiry would make every link dead on arrival. Zero means
	// "never expires", so it is left alone.
	if c.CloudSharing.ShareExpiryDays < 0 {
		c.CloudSharing.ShareExpiryDays = d.CloudSharing.ShareExpiryDays
	}

	if c.Hooks.DefaultTimeout <= 0 {
		c.Hooks.DefaultTimeout = d.Hooks.DefaultTimeout
	}

	if c.Hooks.MaxTimeout <= 0 {
		c.Hooks.MaxTimeout = d.Hooks.MaxTimeout
	}

	if c.Hooks.MaxTimeout < c.Hooks.DefaultTimeout {
		c.Hooks.MaxTimeout = c.Hooks.DefaultTimeout
	}

	c.Hooks.Permissions = defaultedPermissions(c.Hooks.Permissions)

	if c.Sources == nil {
		c.Sources = map[string]SourceConfig{}
	}

	if c.AutoUpdate.CheckIntervalHours <= 0 {
		c.AutoUpdate.CheckIntervalHours = d.AutoUpdate.CheckIntervalHours
	}

	if c.UI.Theme == "" {
		c.UI.Theme = d.UI.Theme
	}

	if c.UI.Language == "" {
		c.UI.Language = d.UI.Language
	}

	if c.Logging.Level == "" {
		c.Logging.Level = d.Logging.Level
	}

	if c.Logging.MaxSizeMB <= 0 {
		c.Logging.MaxSizeMB = d.Logging.MaxSizeMB
	}

	if c.Logging.MaxBackups <= 0 {
		c.Logging.MaxBackups = d.Logging.MaxBackups
	}

	if c.Logging.MaxAgeDays <= 0 {
		c.Logging.MaxAgeDays = d.Logging.MaxAgeDays
	}

	if !c.Logging.Compress {
		c.Logging.Compress = true
	}

	if c.Advanced.LogLevel == "" {
		c.Advanced.LogLevel = d.Advanced.LogLevel
	}
}

// SourceConfigFor returns the configuration for sourceID and whether it exists.
//
// A blank id names no source and always reports absent, or a hand-written blank key
// would masquerade as a real source. Otherwise the exact key wins, then a trimmed
// case-insensitive match: these keys come from a file the user edits, and reporting
// a "GitHub" block as absent would strand a token they can see. A nil receiver or a
// nil map reports absent.
func (c *Config) SourceConfigFor(sourceID string) (SourceConfig, bool) {
	if c == nil || c.Sources == nil {
		return SourceConfig{}, false
	}

	key, ok := c.SourceKey(sourceID)
	if !ok {
		return SourceConfig{}, false
	}

	return c.Sources[key], true
}

// SourceKey returns the key sourceID's block is stored under, and reports whether there
// is one.
//
// A writer folds ids for the same reason SourceConfigFor does: a settings screen
// sending "Nexus" must land in the block called "nexus"; do not create a second
// one, and two blocks that disagree about Enabled do not resolve harmlessly.
func (c *Config) SourceKey(sourceID string) (string, bool) {
	if c == nil || c.Sources == nil {
		return "", false
	}

	wanted := strings.ToLower(strings.TrimSpace(sourceID))
	if wanted == "" {
		return "", false
	}

	if _, ok := c.Sources[sourceID]; ok {
		return sourceID, true
	}

	for key := range c.Sources {
		if strings.ToLower(strings.TrimSpace(key)) == wanted {
			return key, true
		}
	}

	return "", false
}

// SetSource stores the configuration for sourceID under the key it already has, per
// SourceKey. A new block is written under the folded id, or nothing would look it up.
func (c *Config) SetSource(sourceID string, sc SourceConfig) {
	if c == nil {
		return
	}

	if c.Sources == nil {
		c.Sources = map[string]SourceConfig{}
	}

	key, ok := c.SourceKey(sourceID)
	if !ok {
		key = strings.ToLower(strings.TrimSpace(sourceID))
	}

	if key == "" {
		return
	}

	c.Sources[key] = sc
}

// Clone returns a deep copy so a candidate configuration cannot mutate the live one
// through the Sources map. A nil receiver clones to nil, mirroring the methods above.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	out := *c

	out.Sources = make(map[string]SourceConfig, len(c.Sources))
	for id, sc := range c.Sources {
		out.Sources[id] = sc.clone()
	}

	out.Games = make(map[string]GameSettings, len(c.Games))
	for id, settings := range c.Games {
		out.Games[id] = settings
	}

	if c.Hooks.Permissions.Games != nil {
		out.Hooks.Permissions.Games = make(
			map[string]ScriptPermissions,
			len(c.Hooks.Permissions.Games),
		)
		for id, perms := range c.Hooks.Permissions.Games {
			out.Hooks.Permissions.Games[id] = perms
		}
	}

	if c.Hooks.Permissions.Mods != nil {
		out.Hooks.Permissions.Mods = make(
			map[string]ScriptPermissions,
			len(c.Hooks.Permissions.Mods),
		)
		for id, perms := range c.Hooks.Permissions.Mods {
			out.Hooks.Permissions.Mods[id] = perms
		}
	}

	// Redact is a slice: a shallow copy would let a caller edit the live config.
	if c.Logging.Redact != nil {
		out.Logging.Redact = append([]string(nil), c.Logging.Redact...)
	}

	if c.CloudSharing.FallbackProviders != nil {
		out.CloudSharing.FallbackProviders = append(
			[]string(nil),
			c.CloudSharing.FallbackProviders...)
	}

	if c.CloudSharing.Providers != nil {
		out.CloudSharing.Providers = make(
			map[string]map[string]string,
			len(c.CloudSharing.Providers),
		)
		for id, opts := range c.CloudSharing.Providers {
			if opts == nil {
				continue
			}

			copied := make(map[string]string, len(opts))
			for k, v := range opts {
				copied[k] = v
			}

			out.CloudSharing.Providers[id] = copied
		}
	}

	return &out
}

// GameSettingsFor returns the stored settings for a game id and whether an entry exists.
// An untouched game reports the defaults with ok=false, so "not configured" and
// "configured empty" stay distinguishable.
func (c *Config) GameSettingsFor(gameID string) (GameSettings, bool) {
	if c == nil || c.Games == nil {
		return GameSettings{}, false
	}

	settings, ok := c.Games[gameID]

	return settings, ok
}

// SetGameSettings stores the settings for a game id, creating the map when
// the configuration has none yet.
func (c *Config) SetGameSettings(gameID string, settings GameSettings) {
	if c == nil || gameID == "" {
		return
	}

	if c.Games == nil {
		c.Games = map[string]GameSettings{}
	}

	c.Games[gameID] = settings
}

// ActiveSettings returns the active game's settings, or the defaults when no entry exists
// yet. An unresolved ActiveGame names no game, so the defaults apply.
func (c *Config) ActiveSettings() GameSettings {
	settings, ok := c.GameSettingsFor(c.activeGameID())
	if !ok {
		return DefaultGameSettings()
	}

	return settings
}

// UpdateActiveSettings mutates the active game's settings block in place, creating it
// from the defaults when absent. A map element cannot be addressed, so mutation goes
// through this callback and lands back in the map in one step.
func (c *Config) UpdateActiveSettings(fn func(*GameSettings)) {
	if c == nil || fn == nil {
		return
	}

	id := c.activeGameID()
	if c.Games == nil {
		c.Games = map[string]GameSettings{}
	}

	settings, ok := c.Games[id]
	if !ok {
		settings = DefaultGameSettings()
	}

	fn(&settings)
	c.Games[id] = settings
}

// activeGameID is the key the active game's settings live under. The empty id is kept
// as a real key so an unresolved configuration still round-trips its values.
func (c *Config) activeGameID() string {
	if c == nil {
		return ""
	}

	return c.ActiveGame
}

// defaultedPermissions replaces an entirely zero block with the built-in defaults: in
// JSON it cannot be told from an absent one. Any block with a field set is kept.
func defaultedPermissions(p HooksPermissionsConfig) HooksPermissionsConfig {
	if p.Default == (ScriptPermissions{}) {
		p.Default = DefaultScriptPermissions()
	}

	if p.Games == nil && p.Mods == nil {
		return p
	}

	for id, perms := range p.Games {
		if perms == (ScriptPermissions{}) {
			p.Games[id] = DefaultScriptPermissions()
		}
	}

	for id, perms := range p.Mods {
		if perms == (ScriptPermissions{}) {
			p.Mods[id] = DefaultScriptPermissions()
		}
	}

	return p
}

// clone deep-copies a source entry, including its string maps and slices.
func (s SourceConfig) clone() SourceConfig {
	out := s
	if s.Paths != nil {
		out.Paths = append([]string(nil), s.Paths...)
	}

	if s.Options != nil {
		out.Options = make(map[string]string, len(s.Options))
		for k, v := range s.Options {
			out.Options[k] = v
		}
	}

	return out
}
