package services

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// ResolveActiveGame points a configuration at a game the registry knows and
// carries the settings parked under the provisional empty key across to it. It
// reports whether it changed anything.
//
// An empty active_game is legal — "whichever game the registry lists first" —
// but "" is useless as a map key, so settings written while it is unresolved land
// under a key no later lookup asks for. Resolving it once, where a configuration
// enters the program, leaves every later reader with a single concrete id. The
// carried-over entry never clobbers one the user has since written: an explicit
// setting always wins over a provisional one.
func ResolveActiveGame(cfg *domain.Config) bool {
	if cfg == nil {
		return false
	}

	def, ok := games.Get(strings.TrimSpace(cfg.ActiveGame))
	if !ok {
		def, ok = games.Get(games.DefaultID())
		if !ok {
			// Nothing is registered, so there is no id to resolve to; leaving the
			// configuration untouched keeps the caller's own error intact.
			return false
		}
	}

	changed := false

	if cfg.ActiveGame != def.ID() {
		cfg.ActiveGame = def.ID()
		changed = true
	}

	provisional, hasProvisional := cfg.GameSettingsFor("")
	if !hasProvisional {
		return changed
	}

	if _, taken := cfg.GameSettingsFor(def.ID()); !taken {
		cfg.SetGameSettings(def.ID(), provisional)
	}

	delete(cfg.Games, "")

	return true
}

// gameStore wraps a ConfigStore so that every configuration the program reads
// or writes already names a registered game.
type gameStore struct {
	inner ConfigStore
}

// NewGameStore returns a ConfigStore that resolves the active game on every
// load and save. It returns nil for a nil store so a caller wiring an optional
// store keeps the same "unset" signal it had.
func NewGameStore(inner ConfigStore) ConfigStore {
	if inner == nil {
		return nil
	}

	return gameStore{inner: inner}
}

// Path implements ConfigStore.
func (s gameStore) Path() string { return s.inner.Path() }

// Load implements ConfigStore, resolving the active game before the
// configuration reaches a caller.
func (s gameStore) Load() (*domain.Config, error) {
	cfg, err := s.inner.Load()
	if err != nil {
		return nil, err
	}

	ResolveActiveGame(cfg)

	return cfg, nil
}

// Save implements ConfigStore, so a caller that built a configuration by hand
// cannot write an unresolved active game back to disk.
func (s gameStore) Save(cfg *domain.Config) error {
	ResolveActiveGame(cfg)

	return s.inner.Save(cfg)
}

// SaveIfChanged implements ConfigStore. Resolving before the comparison is what
// makes the repair reach the file: the healed encoding differs from the stored
// one precisely when the file was the thing that was wrong.
func (s gameStore) SaveIfChanged(cfg *domain.Config) (bool, error) {
	ResolveActiveGame(cfg)

	return s.inner.SaveIfChanged(cfg)
}
