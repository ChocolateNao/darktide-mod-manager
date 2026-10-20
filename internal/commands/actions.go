package commands

import (
	"runtime"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

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

func (c *ActionCommands) CurrentGameID() string {
	client, _, cancel, err := c.ready()
	if err != nil {
		return ""
	}
	defer cancel()

	return client.ActiveGameID()
}

func (c *ActionCommands) Platform() string {
	return runtime.GOOS
}
