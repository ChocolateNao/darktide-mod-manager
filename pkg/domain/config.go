package domain

import "strings"

// Config is the complete user configuration. Every field has a usable default so
// that a zero Config is still valid; ApplyDefaults fills in anything unset.
type Config struct {
	Sources    map[string]SourceConfig `json:"sources"`
	Game       GameConfig              `json:"game"`
	UI         UIConfig                `json:"ui"`
	Advanced   AdvancedConfig          `json:"advanced"`
	Storage    StorageConfig           `json:"storage"`
	AutoUpdate AutoUpdateConfig        `json:"auto_update"`
	Cache      CacheConfig             `json:"cache"`
	Logging    LoggingConfig           `json:"logging"`
	Hooks      HooksConfig             `json:"hooks"`
}

// GameConfig points at the game installation the manager should manage.
type GameConfig struct {
	// Path is the user-configured installation root. Empty means auto-detect.
	Path string `json:"path"`
	// Source restricts auto-detection to a single store ("steam").
	Source string `json:"source"`
	// ModsFolderOverride replaces the installation's mods folder when set.
	ModsFolderOverride string `json:"mods_folder_override"`
	// AutoDetect enables scanning for installations when Path is empty.
	AutoDetect bool `json:"auto_detect"`
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

// HooksConfig controls hook execution policy.
type HooksConfig struct {
	DefaultTimeout int  `json:"default_timeout"`
	AllowNetwork   bool `json:"allow_network"`
	AllowFileWrite bool `json:"allow_file_write"`
	// MaxTimeout caps any per-script timeout, so a hostile manifest cannot
	// request an unbounded run.
	MaxTimeout int `json:"max_timeout"`
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
// NotifyOnUpdate defaults to true in DefaultConfig, and ApplyDefaults leaves it
// alone: JSON cannot tell an absent boolean from an explicit false, so forcing
// it would make the preference impossible to switch off. A hand-written config
// that omits the key therefore reads as false, while the file the manager writes
// itself carries the default. See ApplyDefaults.
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

// LoggingConfig is the persisted half of the logging settings. It deliberately
// does not carry the process-only switches (console mirroring, redaction list,
// the --log-dir override): those are command-line concerns, so they cannot
// silently become permanent preferences just because a flag was used once.
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
		Game: GameConfig{
			Source:     "auto",
			AutoDetect: true,
		},
		Storage: StorageConfig{
			BackupRetentionDays: 7,
		},
		Cache: CacheConfig{
			MaxSizeGB:    5,
			MaxAgeDays:   30,
			VerifyHashes: true,
		},
		Hooks: HooksConfig{
			DefaultTimeout: 30,
			AllowFileWrite: true,
			MaxTimeout:     600,
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

// ApplyDefaults fills unset fields from DefaultConfig and repairs nonsensical
// values, so the rest of the codebase can trust the config it is handed.
// Explicit user values always win; only zero values are replaced, except for
// fields where zero is never meaningful (see the max calls below).
//
// Booleans whose default is true are the awkward case: JSON cannot distinguish
// an absent key from an explicit false, so they are read exactly as written.
// AutoUpdate.NotifyOnUpdate is the only such field — forcing it would make the
// preference impossible to turn off, and leaving it alone means a hand-written
// file that omits it behaves as false rather than as the DefaultConfig value.
func (c *Config) ApplyDefaults() {
	if c == nil {
		return
	}

	d := DefaultConfig()

	if c.Game.Source == "" {
		c.Game.Source = d.Game.Source
	}

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

	if c.Hooks.DefaultTimeout <= 0 {
		c.Hooks.DefaultTimeout = d.Hooks.DefaultTimeout
	}

	if !c.Hooks.AllowFileWrite {
		c.Hooks.AllowFileWrite = true
	}

	if c.Hooks.MaxTimeout <= 0 {
		c.Hooks.MaxTimeout = d.Hooks.MaxTimeout
	}

	if c.Hooks.MaxTimeout < c.Hooks.DefaultTimeout {
		c.Hooks.MaxTimeout = c.Hooks.DefaultTimeout
	}

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
// A blank id names no source and always reports absent. The check comes first
// because a map written by hand can hold a blank key, and answering "here is the
// configuration for ”" would let a nonsense key masquerade as a real source.
//
// The exact key wins next. Failing that the keys are compared case-insensitively,
// because these keys come from a file the user is free to edit: a config naming
// "GitHub" describes the same source as "github", and reporting it as absent
// would leave the user with a token they can see in their config that the
// manager never reads. The comparison trims surrounding whitespace for the same
// reason. A nil receiver or a nil map reports absent.
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

// SourceKey returns the key under which sourceID's block is stored, and reports
// whether there is one.
//
// SourceConfigFor has to fold ids because it reads a file the user may have
// edited by hand; a writer needs the same answer for the opposite reason. A
// settings screen that sends "Nexus" must land in the block already called
// "nexus" rather than creating a second one beside it — and a second block is
// not a harmless duplicate, because the two disagree about Enabled and the
// disabled one wins.
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

// SetSource stores the configuration for sourceID.
//
// An existing block is updated under the key it already has, for the reason
// SourceKey gives. A new block is written under the folded id, because ids are
// compared case-insensitively everywhere else and "Nexus" would otherwise be a
// key nothing looks up.
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

// Clone returns a deep copy, so a caller that wants to adjust a candidate
// configuration (a "try these settings" flow, or ApplyDefaults before saving)
// cannot accidentally mutate the live one through the Sources map.
//
// A nil receiver clones to nil: it mirrors the nil-safe methods above.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	out := *c

	out.Sources = make(map[string]SourceConfig, len(c.Sources))
	for id, sc := range c.Sources {
		out.Sources[id] = sc.clone()
	}
	// Redact is a slice, so a shallow copy would let a caller editing the clone
	// edit the live configuration's list too. Every other block is value-only,
	// which is why this one line is the whole of the deep copy here.
	if c.Logging.Redact != nil {
		out.Logging.Redact = append([]string(nil), c.Logging.Redact...)
	}

	return &out
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
