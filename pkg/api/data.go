package api

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// DeleteDataOptions selects what DeleteData removes. Selecting nothing is
// rejected so a caller cannot report success after doing nothing.
type DeleteDataOptions struct {
	// ModData removes every managed mod with its hooks, plus the download cache
	// and share records that point at them.
	ModData bool
	// Config rewrites the configuration file with the defaults.
	Config bool
}

// DeleteData removes the selected application data. Callers own the confirmation
// step; this runs as soon as it is asked.
func (c *Client) DeleteData(ctx context.Context, opts DeleteDataOptions) error {
	if err := c.ready(); err != nil {
		return err
	}

	if !opts.ModData && !opts.Config {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"no application data selected for deletion")
	}

	if opts.ModData && c.deps.Mods == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a mod service")
	}

	var firstErr error

	if opts.ModData {
		if err := c.deps.Mods.DeleteAllMods(ctx); err != nil {
			firstErr = err
		}

		if c.deps.Cache != nil {
			if err := c.deps.Cache.ClearCache(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		// Share records point at mods that no longer exist, so they go too.
		if c.deps.Shares != nil {
			if err := c.deps.Shares.DeleteAllShares(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	if opts.Config {
		cfg := domain.DefaultConfig()
		services.ResolveActiveGame(&cfg)

		if c.deps.Config != nil {
			if err := c.deps.Config.Save(&cfg); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		if c.deps.ActiveConfig != nil {
			*c.deps.ActiveConfig = cfg
		}

		if c.deps.Sources != nil {
			if def, ok := games.Get(cfg.ActiveGame); ok {
				c.deps.Sources.RebuildForGame(def)
			}
		}
	}

	return firstErr
}

// DeleteAllData is DeleteData with every part selected: the factory reset.
func (c *Client) DeleteAllData(ctx context.Context) error {
	return c.DeleteData(ctx, DeleteDataOptions{ModData: true, Config: true})
}
