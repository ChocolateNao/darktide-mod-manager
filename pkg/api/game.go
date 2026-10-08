package api

import (
	"context"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// locator returns the game locator, or the error to report.
func (c *Client) locator() (*services.GameLocator, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.Game == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a game locator")
	}

	return c.deps.Game, nil
}

// DetectGame probes the machine for Darktide installations and returns them
// sorted by path.
//
// A partially successful sweep keeps what it found: one candidate directory that
// cannot be read is not a reason to report that the user has no game. The error
// is only returned when the sweep itself was cancelled.
func (c *Client) DetectGame(ctx context.Context) ([]GameInstallation, error) {
	loc, err := c.locator()
	if err != nil {
		return nil, err
	}

	return loc.FindInstallations(ctx)
}

// CurrentGame returns the installation the manager is configured to use: the
// configured path when one is set, otherwise the first detected one.
//
// A configured path that no longer resolves is reported as ErrCodeGameInvalid
// rather than silently replaced with another install. Silently repointing a
// mod manager at a different copy of the game would move every deployed link,
// which is not something to do without asking.
func (c *Client) CurrentGame(ctx context.Context) (*GameInstallation, error) {
	loc, err := c.locator()
	if err != nil {
		return nil, err
	}

	return loc.Current(ctx)
}

// ValidateGamePath checks one path without storing it. It is the call behind a
// "browse" dialog's OK button: the path is probed, and only a path that looks
// like a real install is written.
func (c *Client) ValidateGamePath(ctx context.Context, path string) (*GameInstallation, error) {
	loc, err := c.locator()
	if err != nil {
		return nil, err
	}

	return loc.ValidateInstallation(strings.TrimSpace(path))
}

// SetGamePath validates a path and, if it is a usable installation, stores it so
// later calls and later starts use it. An invalid path is reported and nothing is
// written.
//
// Only the path is stored. The recorded source and the auto-detect switch are
// left alone: the source is a filter for auto-detection, not a record of where
// the user pointed, and turning auto-detection off because the user named a path
// would stop the manager noticing the game moved.
func (c *Client) SetGamePath(ctx context.Context, path string) error {
	loc, err := c.locator()
	if err != nil {
		return err
	}

	return loc.SetGamePath(ctx, strings.TrimSpace(path))
}

// GetModsPath returns the folder the manager deploys mods into for an
// installation, honouring a configured override.
//
// A nil install asks about the configured one. The distinction is worth keeping:
// "where do my mods go?" and "where would mods go for this install?" are
// different questions and the answers differ once an override is set.
func (c *Client) GetModsPath(ctx context.Context, install *GameInstallation) (string, error) {
	loc, err := c.locator()
	if err != nil {
		return "", err
	}

	if install == nil {
		current, err := loc.Current(ctx)
		if err != nil {
			return "", err
		}

		install = current
	}

	return loc.GetModsPath(install)
}
