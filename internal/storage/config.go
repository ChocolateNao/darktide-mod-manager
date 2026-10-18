package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ConfigStore reads and writes the configuration file.
//
// The file is JSON because a user edits it by hand when the GUI cannot start, so the on-disk
// shape is exactly the struct in pkg/domain. Writes go through the platform's atomic replace,
// so an interrupted save cannot leave a half-written file behind.
type ConfigStore struct {
	path string
	mu   sync.RWMutex
}

// NewConfigStore returns a store bound to path. The file need not exist; the
// parent directory is created on the first save.
func NewConfigStore(path string) *ConfigStore {
	return &ConfigStore{path: strings.TrimSpace(path)}
}

// Path returns the file the store reads and writes. It is empty when the store
// was built without a path, in which case Load still returns defaults and Save
// fails.
func (s *ConfigStore) Path() string {
	if s == nil {
		return ""
	}

	return s.path
}

// Load reads the configuration, with defaults already applied.
//
// A missing file is not an error: it yields defaults, which is what a first run looks like.
// A file that exists but cannot be parsed is — silently replacing it would throw away the
// user's game path and API keys without telling anyone.
func (s *ConfigStore) Load() (*domain.Config, error) {
	path := s.Path()
	if path == "" {
		cfg := domain.DefaultConfig()
		return &cfg, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		cfg := domain.DefaultConfig()
		return &cfg, nil
	default:
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read config %q", path)
	}

	cfg, err := decodeConfig(data)
	if err != nil {
		return nil, err
	}

	cfg.ApplyDefaults()

	return cfg, nil
}

// decodeConfig parses configuration bytes, mapping every failure onto the typed
// invalid-config code. Whitespace-only content is an empty document rather than an
// error: a file truncated to nothing is still recoverable by defaults, whereas a
// half-written object is not, and the caller deserves to hear about it.
func decodeConfig(data []byte) (*domain.Config, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		cfg := domain.DefaultConfig()
		return &cfg, nil
	}

	cfg := &domain.Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigInvalid, "parse config: %v", err)
	}

	return cfg, nil
}

// Save writes cfg, applying defaults first so the file on disk is always
// complete: a field left blank by an older build gets its default instead of
// staying blank forever.
func (s *ConfigStore) Save(cfg *domain.Config) error {
	if s == nil || s.path == "" {
		return appErrors.New(appErrors.ErrCodeConfigInvalid, "no config path configured")
	}

	if cfg == nil {
		return appErrors.New(appErrors.ErrCodeConfigInvalid, "cannot save a nil config")
	}

	toStore := cfg.Clone()
	toStore.ApplyDefaults()

	data, err := json.MarshalIndent(toStore, "", "  ")
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigInvalid, "encode config: %v", err)
	}

	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create config directory %q", dir)
		}
	}

	if err := platform.AtomicWriteFile(s.path, data, 0o600); err != nil {
		// AtomicWriteFile reports raw os errors; re-code them here so callers only ever
		// have to look at one code for "the config would not persist".
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "write config %q: %v", s.path, err)
	}

	return nil
}

// SaveIfChanged writes cfg only when it differs from what is already on disk,
// and reports whether a write happened. The GUI calls this on every settings
// change; without the comparison each keystroke in a text field would rewrite the
// file and trip filesystem watchers.
func (s *ConfigStore) SaveIfChanged(cfg *domain.Config) (bool, error) {
	current, err := s.Load()
	if err != nil {
		return false, err
	}

	if equalConfig(current, cfg) {
		return false, nil
	}

	if err := s.Save(cfg); err != nil {
		return false, err
	}

	return true, nil
}

// LoadConfig is the one-shot form of (*ConfigStore).Load.
func LoadConfig(path string) (*domain.Config, error) {
	return NewConfigStore(path).Load()
}

// SaveConfig is the one-shot form of (*ConfigStore).Save.
func SaveConfig(path string, cfg *domain.Config) error {
	return NewConfigStore(path).Save(cfg)
}

// equalConfig compares two configurations by their canonical JSON form. Using the
// encoding rather than field-by-field comparison means a new field is covered the
// moment it is added, and it cannot be forgotten here.
func equalConfig(a, b *domain.Config) bool {
	if a == nil || b == nil {
		return a == b
	}

	norm := func(c *domain.Config) string {
		clone := c.Clone()
		clone.ApplyDefaults()

		data, err := json.Marshal(clone)
		if err != nil {
			return ""
		}

		return string(data)
	}

	return norm(a) == norm(b)
}
