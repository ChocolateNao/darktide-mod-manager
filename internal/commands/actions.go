package commands

import (
	"runtime"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// ActionCommands is the game-specific toolbox as a frontend sees it.
//
// The actions a game publishes are chosen by the game, not by this package:
// listing takes a game id rather than being hard-wired to Darktide, so a
// frontend rendering only what it is told needs no change when a second game
// arrives.
type ActionCommands struct {
	*base
}

// NewActionCommands builds the action command set.
func NewActionCommands(deps Deps) *ActionCommands {
	return &ActionCommands{base: newBase(deps)}
}

func newActionCommands(b *base) *ActionCommands {
	return &ActionCommands{base: b}
}

// ListActions returns the actions a game publishes, or every action when gameID
// is empty. An unknown game is not an error here: a frontend rendering a menu
// during startup, before the active game has loaded, needs a list it can show
// rather than a rejection it can only log.
func (c *ActionCommands) ListActions(gameID string) ([]api.GameAction, error) {
	client, _, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	list, err := client.ListGameActions(strings.TrimSpace(gameID))
	if err != nil {
		return nil, c.fail(err, "list actions")
	}

	if list == nil {
		return []api.GameAction{}, nil
	}

	return list, nil
}

// ListAvailableActions returns the actions that could actually run on a
// platform. A Linux build has no business offering a Windows-only action, and a
// menu full of entries that fail on click is worse than a shorter menu. platform
// defaults to the one this process runs on, and is a parameter only because a
// frontend previewing another machine's menu has no other way to ask.
func (c *ActionCommands) ListAvailableActions(gameID, platform string) ([]api.GameAction, error) {
	client, _, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	list, err := client.ListAvailableGameActions(
		strings.TrimSpace(gameID),
		domain.Platform(strings.TrimSpace(platform)),
	)
	if err != nil {
		return nil, c.fail(err, "list available actions")
	}

	if list == nil {
		return []api.GameAction{}, nil
	}

	return list, nil
}

// GetGameAction returns one action by id, so a frontend can render its form —
// labels, option types, defaults — without listing everything first.
func (c *ActionCommands) GetGameAction(actionID string) (*api.GameAction, error) {
	client, _, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	action, err := client.GetGameAction(api.ActionID(strings.TrimSpace(actionID)))
	if err != nil {
		return nil, c.fail(err, "read action %q", actionID)
	}

	return action, nil
}

// ExecuteGameAction runs one action with the arguments the user supplied. A nil
// map is not an error: an action that declares no options must be runnable with
// no arguments.
func (c *ActionCommands) ExecuteGameAction(
	actionID string,
	args map[string]any,
) (*api.GameActionResult, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	if args == nil {
		args = map[string]any{}
	}

	result, err := client.ExecuteGameAction(ctx, api.ActionID(strings.TrimSpace(actionID)), args)
	if err != nil {
		return nil, c.fail(err, "run action %q", actionID)
	}

	return result, nil
}

// CurrentGameID reports which game the actions in the menu belong to. An empty
// string rather than an error when no game is active: a frontend has to render
// an empty menu, and an error there looks like a failure to start.
func (c *ActionCommands) CurrentGameID() string {
	client, _, cancel, err := c.ready()
	if err != nil {
		return ""
	}
	defer cancel()

	return client.ActiveGameID()
}

// Platform reports the platform this build runs on, taken from runtime.GOOS
// rather than from the action layer's configured platform so it answers "where am
// I actually running", which is what a frontend needs to pick a menu or a
// warning.
func (c *ActionCommands) Platform() string {
	return runtime.GOOS
}
