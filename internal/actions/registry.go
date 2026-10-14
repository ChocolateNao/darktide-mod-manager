package actions

import (
	"sort"
	"strings"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Registry is the set of actions this manager can run.
//
// It is a map guarded by a mutex rather than a slice so that registering a game
// at startup and listing actions while the UI is open can happen concurrently,
// and so that a lookup by id is a single map read instead of a scan.
//
// Registration validates: a definition that could never run correctly — an id
// belonging to another game, an option nobody could fill in, an action with
// neither a handler nor a script — is refused where it is introduced rather than
// discovered when a user tries to use it.
type Registry struct {
	actions map[domain.ActionID]domain.GameAction
	mu      sync.RWMutex
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{actions: make(map[domain.ActionID]domain.GameAction)}
}

// Register adds one action, replacing any action already registered under the
// same id.
//
// Replacement rather than refusal is deliberate: game definitions are registered
// by init-time functions, and a test suite that re-registers a fixture game
// should not have to care whether a previous run left the id behind.
func (r *Registry) Register(action domain.GameAction) error {
	if r == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action registry is not initialised")
	}

	if err := validate(action); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.actions[action.ID] = action

	return nil
}

// RegisterGame registers every action a game definition publishes.
//
// Games declare their own actions so that adding support for a game never
// requires editing the registry, and a game that declares none costs nothing
// here beyond a call to GameActions.
func (r *Registry) RegisterGame(game domain.GameDefinition) error {
	if r == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action registry is not initialised")
	}

	if game == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"cannot register the actions of a nil game")
	}

	for _, action := range game.GameActions() {
		if err := r.Register(action); err != nil {
			return err
		}
	}

	return nil
}

// validate refuses an action that could not run.
//
// The checks are ordered cheapest-first and, more importantly, they are made once
// at registration: a malformed action is a bug in this program, so it should
// surface during development rather than as a confusing failure on a user's
// machine.
func validate(action domain.GameAction) error {
	gameID := strings.TrimSpace(action.GameID)

	if err := action.ID.Validate(gameID); err != nil {
		return err
	}

	if action.ID.Game() != gameID {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action %q is registered for game %q", action.ID, gameID)
	}

	switch {
	case action.Handler != nil && action.Script != nil:
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action %q has both a handler and a script; it needs exactly one", action.ID)
	case action.Handler == nil && action.Script == nil:
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action %q has neither a handler nor a script", action.ID)
	case action.Script != nil:
		if err := action.Script.Validate(); err != nil {
			return err
		}
	}

	if !action.Platform.IsValid() && action.Platform != "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action %q targets unknown platform %q", action.ID, action.Platform)
	}

	seen := make(map[string]struct{}, len(action.Options))

	for _, option := range action.Options {
		if err := option.Validate(); err != nil {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"action %q: %v", action.ID, err)
		}

		if _, duplicate := seen[option.Name]; duplicate {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"action %q declares option %q twice", action.ID, option.Name)
		}

		seen[option.Name] = struct{}{}
	}

	return nil
}

// Get returns one action by id.
func (r *Registry) Get(id domain.ActionID) (domain.GameAction, bool) {
	if r == nil {
		return domain.GameAction{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	action, ok := r.actions[id]

	return action, ok
}

// Has reports whether an id is registered.
func (r *Registry) Has(id domain.ActionID) bool {
	_, ok := r.Get(id)

	return ok
}

// ListByGame returns the actions one game published, in a stable order.
//
// The order is by id rather than by map iteration so that the CLI and the
// frontend render the same list in the same sequence on every run.
func (r *Registry) ListByGame(gameID string) []domain.GameAction {
	return r.list(func(action domain.GameAction) bool {
		return action.GameID == strings.TrimSpace(gameID)
	})
}

// ListAll returns every registered action in a stable order.
func (r *Registry) ListAll() []domain.GameAction {
	return r.list(func(domain.GameAction) bool { return true })
}

// ListAvailable returns the actions of one game that run on target.
//
// This is the list a surface should offer rather than ListByGame: an
// action that is not offered on the current platform should be invisible rather
// than present and then refused.
func (r *Registry) ListAvailable(gameID string, target domain.Platform) []domain.GameAction {
	return r.list(func(action domain.GameAction) bool {
		return action.GameID == strings.TrimSpace(gameID) && action.AvailableOn(target)
	})
}

func (r *Registry) list(keep func(domain.GameAction) bool) []domain.GameAction {
	if r == nil {
		return nil
	}

	r.mu.RLock()

	matching := make([]domain.GameAction, 0, len(r.actions))

	for _, action := range r.actions {
		if keep(action) {
			matching = append(matching, action)
		}
	}

	r.mu.RUnlock()

	sort.Slice(matching, func(i, j int) bool {
		return matching[i].ID < matching[j].ID
	})

	return matching
}

// Len reports how many actions are registered.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.actions)
}
