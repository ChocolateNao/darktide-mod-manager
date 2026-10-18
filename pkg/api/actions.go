package api

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/internal/actions"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// GameAction, GameActionResult and ActionID are re-exported so a frontend
// binding or a CLI only ever has to import this package to describe an action.
type (
	// GameAction is one code-defined action a game offers.
	GameAction = domain.GameAction
	// GameActionResult is what an action produced.
	GameActionResult = domain.GameActionResult
	// ActionID identifies an action; its prefix is the game it belongs to.
	ActionID = domain.ActionID
	// GameActionOption describes one argument an action accepts.
	GameActionOption = domain.ActionOption
)

// actionsRunner returns the runner that executes actions. Separate from the
// registry on purpose: listing needs only a table lookup, running needs a game
// locator, an opener and a command runner.
func (c *Client) actionsRunner() (*actions.Runner, error) {
	if c == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "the client is not initialised")
	}

	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.ActionRunner == nil {
		return nil, appErrors.New(appErrors.ErrCodeActionUnavailable,
			"game actions are not available in this session")
	}

	return c.deps.ActionRunner, nil
}

// actionRegistry returns the registry of code-defined actions, or an empty one so
// listing actions is never an error: a build shipping no actions answers none.
func (c *Client) actionRegistry() (*actions.Registry, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.Actions == nil {
		return actions.NewRegistry(), nil
	}

	return c.deps.Actions, nil
}

// ListGameActions returns every action the given game defines, sorted by id. The
// empty string lists every registered game's, as `actions list` does unprompted.
func (c *Client) ListGameActions(gameID string) ([]domain.GameAction, error) {
	registry, err := c.actionRegistry()
	if err != nil {
		return nil, err
	}

	if gameID == "" {
		return registry.ListAll(), nil
	}

	return registry.ListByGame(gameID), nil
}

// ListAvailableGameActions returns the actions of a game that can run on the given
// platform — the set a menu should offer, since an action the machine cannot perform
// is worse than no entry at all. An empty platform means the current one.
func (c *Client) ListAvailableGameActions(
	gameID string,
	platform domain.Platform,
) ([]domain.GameAction, error) {
	registry, err := c.actionRegistry()
	if err != nil {
		return nil, err
	}

	if platform == "" {
		platform = domain.CurrentPlatform()
	}

	if gameID == "" {
		out := make([]domain.GameAction, 0, registry.Len())

		for _, action := range registry.ListAll() {
			if action.AvailableOn(platform) {
				out = append(out, action)
			}
		}

		return out, nil
	}

	return registry.ListAvailable(gameID, platform), nil
}

// GetGameAction describes one action, including the options it accepts. A
// frontend renders its form from this rather than hard-coding a field list.
func (c *Client) GetGameAction(actionID domain.ActionID) (*domain.GameAction, error) {
	registry, err := c.actionRegistry()
	if err != nil {
		return nil, err
	}

	action, ok := registry.Get(actionID)
	if !ok {
		return nil, appErrors.New(
			appErrors.ErrCodeActionNotFound,
			"no action %q is registered",
			actionID,
		)
	}

	return &action, nil
}

// ExecuteGameAction runs an action and returns what it produced. Args are keyed by
// option name (see GameActionOption) and coerced to the declared type, so a frontend
// may send a form's strings without converting them itself.
func (c *Client) ExecuteGameAction(
	ctx context.Context,
	actionID domain.ActionID,
	args map[string]any,
) (*domain.GameActionResult, error) {
	runner, err := c.actionsRunner()
	if err != nil {
		return nil, err
	}

	return runner.Run(ctx, actionID.String(), args)
}
