package commands

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

// GameCommands is game discovery as a frontend sees it: where the game is, and
// whether a path the user typed is usable.
type GameCommands struct {
	// Embedded so a caller holding one set can shut it down without the
	// aggregate.
	*base
}

func NewGameCommands(deps Deps) *GameCommands {
	return &GameCommands{base: newBase(deps)}
}

func newGameCommands(b *base) *GameCommands {
	return &GameCommands{base: b}
}

// DetectGame looks for Darktide installations. It returns an empty slice when
// nothing was found, because "I could not find the game" is an answer a first-run
// wizard draws.
func (c *GameCommands) DetectGame() ([]api.GameInstallation, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	found, err := client.DetectGame(ctx)
	if err != nil {
		return nil, c.fail(err, "search for the game")
	}

	if found == nil {
		return []api.GameInstallation{}, nil
	}

	return found, nil
}

// CurrentGame returns the installation in use: the configured path when one is
// set, otherwise the first one that was detected.
func (c *GameCommands) CurrentGame() (*api.GameInstallation, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	install, err := client.CurrentGame(ctx)
	if err != nil {
		return nil, c.fail(err, "read the current game installation")
	}

	return install, nil
}

// ValidateGamePath checks a path the user typed without storing it.
func (c *GameCommands) ValidateGamePath(path string) (*api.GameInstallation, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	install, err := client.ValidateGamePath(ctx, strings.TrimSpace(path))
	if err != nil {
		return nil, c.fail(err, "validate game path %q", path)
	}

	return install, nil
}

// SetGamePath stores the path to use. It validates first, so a bad path leaves
// the configuration as it was.
func (c *GameCommands) SetGamePath(path string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.SetGamePath(ctx, strings.TrimSpace(path)); err != nil {
		return c.fail(err, "set game path to %q", path)
	}

	return nil
}

// SupportedGames lists the games the manager can manage, with the default and
// the active one marked. It reads the registry, so a game is listed whether or
// not it is installed.
func (c *GameCommands) SupportedGames() ([]api.GameInfo, error) {
	// This reads the registry; ready() still gates it so the services are up and
	// cancel stays paired.
	client, _, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	games, err := client.SupportedGames()
	if err != nil {
		return nil, c.fail(err, "list supported games")
	}

	return games, nil
}

// UseGame switches the active game and remembers the choice. The id is what
// SupportedGames reports; an unknown one is refused, because a typo silently
// running the wrong game is worse than a failed switch.
func (c *GameCommands) UseGame(id string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.UseGame(ctx, strings.TrimSpace(id)); err != nil {
		return c.fail(err, "switch to game %q", id)
	}

	return nil
}

// GetModsPath returns the mods folder for an installation, or for the one in
// use when install is nil.
func (c *GameCommands) GetModsPath(install *api.GameInstallation) (string, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return "", err
	}
	defer cancel()

	path, err := client.GetModsPath(ctx, install)
	if err != nil {
		return "", c.fail(err, "resolve the mods folder")
	}

	return path, nil
}
