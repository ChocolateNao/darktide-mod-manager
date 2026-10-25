package api

import (
	"context"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources"
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

// DetectGame probes the machine for selected game installations, sorted by path. A
// partially successful sweep keeps what it found: one unreadable candidate is no
// reason to report that the user has no game.
func (c *Client) DetectGame(ctx context.Context) ([]GameInstallation, error) {
	loc, err := c.locator()
	if err != nil {
		return nil, err
	}

	return loc.FindInstallations(ctx)
}

// CurrentGame returns the installation the manager is configured to use: the
// configured path when set, otherwise the first detected one. A configured path
// that no longer resolves is ErrCodeGameInvalid, not silently replaced — repointing
// would move every deployed link.
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

// SetGamePath validates a path and, if usable, stores it for later calls and
// starts; an invalid path is reported and nothing is written. Only the path is
// stored: the source is a filter for auto-detection, and switching auto-detect off
// would stop the manager noticing the game moved.
func (c *Client) SetGamePath(ctx context.Context, path string) error {
	loc, err := c.locator()
	if err != nil {
		return err
	}

	return loc.SetGamePath(ctx, strings.TrimSpace(path))
}

// GetModsPath returns the folder the manager deploys mods into, honouring a
// configured override. A nil install asks about the configured one: "where do my
// mods go?" and "where would they go for this install?" differ once an override is
// set.
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

// GameInfo describes one registered game as the switcher needs it: identity,
// presentation, which entry is the default and which is live, and what it can do
// with a mod once the mod is fetched.
type GameInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	// DefaultInstallMethod is the method used when nothing else is chosen.
	DefaultInstallMethod string `json:"default_install_method"`
	// InstallModes are the deployment modes this game accepts. Without them a UI either
	// offers a mode the game cannot deploy or hides one it needs, and cannot tell which
	// from the id alone.
	InstallModes []string `json:"install_modes"`
	IsDefault    bool     `json:"is_default"`
	IsActive     bool     `json:"is_active"`
}

// SupportedGames lists every game the registry knows, sorted by id, with the default
// and active game marked. The list comes from the registry, not the configuration:
// a game the config mentions but the registry does not offer is not switchable.
func (c *Client) SupportedGames() ([]GameInfo, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	defs := games.All()
	infos := make([]GameInfo, 0, len(defs))

	active := ""

	switch {
	case c.deps.ActiveConfig != nil:
		active = strings.TrimSpace(c.deps.ActiveConfig.ActiveGame)
	case c.deps.Config != nil:
		if cfg, err := c.deps.Config.Load(); err == nil && cfg != nil {
			active = strings.TrimSpace(cfg.ActiveGame)
		}
	}

	defaultID := games.DefaultID()
	if active == "" {
		active = defaultID
	}

	for _, def := range defs {
		modes := def.InstallModes()

		installModes := make([]string, 0, len(modes))
		for _, mode := range modes {
			installModes = append(installModes, string(mode))
		}

		infos = append(infos, GameInfo{
			ID:                   def.ID(),
			DisplayName:          def.DisplayName(),
			Description:          def.Description(),
			IsDefault:            def.ID() == defaultID,
			IsActive:             def.ID() == active,
			InstallModes:         installModes,
			DefaultInstallMethod: def.ModInstallMethod(),
		})
	}

	return infos, nil
}

// UseGame makes id the active game for this process and the next start: the id is
// persisted through the locator and the source registry is rebuilt in place.
// Everything else keeps its identity — the mod service, cache and hook executor
// re-resolve the active game per call — so a switch is a configuration change, not
// a restart.
func (c *Client) UseGame(ctx context.Context, id string) error {
	loc, err := c.locator()
	if err != nil {
		return err
	}

	if ctx != nil && ctx.Err() != nil {
		return appErrors.New(appErrors.ErrCodeOperationCanceled,
			"operation stopped before switching to %q", strings.TrimSpace(id))
	}

	trimmed := strings.TrimSpace(id)

	def, ok := games.Get(trimmed)
	if !ok {
		return appErrors.New(appErrors.ErrCodeGameNotFound,
			"game %q is not registered", trimmed)
	}

	if err := loc.SetActive(def.ID()); err != nil {
		return err
	}

	// The wiring closures (hook GameID, registry WithConfig) read the live
	// configuration object; updating it here makes a switch visible without a
	// graph rebuild.
	if c.deps.ActiveConfig != nil {
		c.deps.ActiveConfig.ActiveGame = def.ID()
	}

	if c.deps.Sources == nil {
		return nil
	}

	// The live configuration object. It carries the
	// per-source overrides a command line supplied, which are never written back —
	// reloading would drop them.
	cfg := c.deps.ActiveConfig
	if cfg == nil {
		if c.deps.Config == nil {
			return nil
		}

		loaded, err := c.deps.Config.Load()
		if err != nil || loaded == nil {
			return nil
		}

		cfg = loaded
	}

	c.deps.Sources.RebuildForGame(def)

	if err := sources.RegisterAll(c.deps.Sources, cfg.Sources, def); err != nil {
		// As in the initial wiring: a source that cannot be configured is a
		// problem the user can see and fix, not a reason to refuse the switch.
		c.logger.Printf("some sources could not be configured for %q: %v", def.ID(), err)
	}

	return nil
}

// ActiveGameID returns active game ID from config.
func (c *Client) ActiveGameID() string {
	if c.deps.ActiveConfig != nil {
		return strings.TrimSpace(c.deps.ActiveConfig.ActiveGame)
	}

	return ""
}
