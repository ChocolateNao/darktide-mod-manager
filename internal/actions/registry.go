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
// A map guarded by a mutex lets startup registration and UI listing happen
// concurrently. Registration validates, so a definition that could never run is
// refused where it is introduced.
type Registry struct {
	actions map[domain.ActionID]domain.GameAction
	mu      sync.RWMutex
}

func NewRegistry() *Registry {
	return &Registry{actions: make(map[domain.ActionID]domain.GameAction)}
}

// Register adds one action, replacing any action already registered under the
// same id. Replacement lets a test suite re-register a fixture without caring
// what a previous run left behind.
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

// RegisterGame registers every action a game definition publishes, so adding a
// game never requires editing the registry.
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

// validate refuses an action that could not run. The checks run once, at
// registration, so a malformed action surfaces during development.
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

func (r *Registry) Get(id domain.ActionID) (domain.GameAction, bool) {
	if r == nil {
		return domain.GameAction{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	action, ok := r.actions[id]

	return action, ok
}

func (r *Registry) Has(id domain.ActionID) bool {
	_, ok := r.Get(id)

	return ok
}

// ListByGame returns the actions one game published, ordered by id so the CLI and
// the frontend render the same sequence on every run.
func (r *Registry) ListByGame(gameID string) []domain.GameAction {
	return r.list(func(action domain.GameAction) bool {
		return action.GameID == strings.TrimSpace(gameID)
	})
}

func (r *Registry) ListAll() []domain.GameAction {
	return r.list(func(domain.GameAction) bool { return true })
}

// ListAvailable returns the actions offered for one game on target. Actions not
// offered on the platform stay invisible. `RequiresGame: false` actions are
// included for every game, so "darktide:open_workshop" survives a game selection.
func (r *Registry) ListAvailable(gameID string, target domain.Platform) []domain.GameAction {
	wanted := strings.TrimSpace(gameID)

	return r.list(func(action domain.GameAction) bool {
		if !action.AvailableOn(target) {
			return false
		}

		owner := strings.TrimSpace(action.GameID)

		return wanted == "" || owner == wanted || owner == ""
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

func (r *Registry) Len() int {
	if r == nil {
		return 0
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.actions)
}
