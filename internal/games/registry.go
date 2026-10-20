// Package games holds the registry of supported games. Each game is a
// domain.GameDefinition registered once at start-up; the rest of the program
// looks games up by id and never imports a concrete game package.
package games

import (
	"sort"
	"strings"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games/darktide"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

var (
	registryMu  sync.RWMutex
	registry    = map[string]domain.GameDefinition{}
	order       []string
	builtinsMu  sync.Mutex
	builtinsRan bool
)

// EnsureBuiltins registers every game the binary ships with. It is idempotent,
// and a Reset in a test puts it back so the next call registers again. Failing
// here means the binary cannot name its own game, which is fatal at wiring time.
func EnsureBuiltins() error {
	builtinsMu.Lock()
	defer builtinsMu.Unlock()

	if builtinsRan {
		return nil
	}

	if err := Register(darktide.New()); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"register built-in game: %v", err)
	}

	builtinsRan = true

	return nil
}

// Register records a game definition. A duplicate id, a blank id or a nil
// definition is an error: two definitions answering to one id would make
// every lookup after the first a coin toss between them.
func Register(def domain.GameDefinition) error {
	if def == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "game definition must not be nil")
	}

	id := normalizeID(def.ID())
	if id == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "game id must not be empty")
	}

	if !domain.ValidInstallMethod(def.ModInstallMethod()) {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"game %q declares unknown install method %q", id, def.ModInstallMethod())
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	if _, exists := registry[id]; exists {
		return appErrors.New(appErrors.ErrCodeSourceExists, "game %q already registered", id)
	}

	registry[id] = def
	order = append(order, id)

	return nil
}

// Get returns the definition registered for an id. The id is matched
// case-insensitively with surrounding space ignored, because it arrives from
// hand-edited config files and CLI input.
func Get(id string) (domain.GameDefinition, bool) {
	key := normalizeID(id)
	if key == "" {
		return nil, false
	}

	registryMu.RLock()
	defer registryMu.RUnlock()

	def, ok := registry[key]

	return def, ok
}

// MustGet returns the definition for an id or panics. It is for built-ins wired
// at composition time, where a missing game means the binary was built without
// its own registry.
func MustGet(id string) domain.GameDefinition {
	def, ok := Get(id)
	if !ok {
		panic("games: no game registered as " + id)
	}

	return def
}

// All returns every registered game, sorted by id so UIs and tests see a
// stable order regardless of registration order.
func All() []domain.GameDefinition {
	registryMu.RLock()
	defer registryMu.RUnlock()

	out := make([]domain.GameDefinition, 0, len(registry))
	for _, def := range registry {
		out = append(out, def)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })

	return out
}

// DefaultID is the id of the first registered game, which is what a
// configuration with an empty active_game resolves to.
func DefaultID() string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if len(order) == 0 {
		return ""
	}

	return order[0]
}

// IDs returns every registered id, sorted.
func IDs() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}

	sort.Strings(out)

	return out
}

// BySourceDomain returns the game that claims slug as its sub-domain on sourceID.
//
// The lookup exists because the two sides of a launcher link use different strings
// for the same game: the link carries the site's sub-domain, the rest of the
// program names games by registry id, and neither can be derived from the other.
// The first match wins, so a game that declares the same sub-domain twice is a
// definition mistake: not a runtime choice.
func BySourceDomain(sourceID, slug string) (domain.GameDefinition, bool) {
	needle := strings.ToLower(strings.TrimSpace(slug))
	if needle == "" {
		return nil, false
	}

	for _, def := range All() {
		got := strings.ToLower(domain.SourceDefault(def, sourceID, domain.SourceDomainKey))
		if got != "" && got == needle {
			return def, true
		}
	}

	return nil, false
}

// SourceDomains lists the sub-domains the registered games claim on one source,
// sorted by game id so a refusal names them in a stable order. It answers "which
// links would work?" without the caller repeating the SourceDefaults layout.
func SourceDomains(sourceID string) []string {
	out := make([]string, 0, len(registry))

	for _, def := range All() {
		if slug := domain.SourceDefault(def, sourceID, domain.SourceDomainKey); slug != "" {
			out = append(out, slug)
		}
	}

	return out
}

// Lookup resolves one game by id for a consumer that cannot ask the locator.
//
// The action runner needs this: it describes actions belonging to any game,
// including one the user has not made active.
type Lookup struct{}

// Get returns the definition registered for an id.
func (Lookup) Get(id string) (domain.GameDefinition, bool) { return Get(id) }

// Reset empties the registry. Production code never calls it; tests share one
// process-wide registry and without a way to clear it every test package would
// inherit whatever the last one registered.
func Reset() {
	builtinsMu.Lock()
	defer builtinsMu.Unlock()

	registryMu.Lock()
	defer registryMu.Unlock()

	registry = map[string]domain.GameDefinition{}
	order = nil
	builtinsRan = false
}

// normalizeID folds a game id for storage and lookup.
func normalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
