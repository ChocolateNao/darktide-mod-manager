package api

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// cacheManager returns the cache manager, or the error to report.
func (c *Client) cacheManager() (*services.CacheManager, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.Cache == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a cache manager")
	}

	return c.deps.Cache, nil
}

// GetCacheInfo reports what the download cache holds. An error, not an empty
// CacheInfo, on failure — an empty answer would read as "0 files, 0 bytes" on a
// broken cache.
func (c *Client) GetCacheInfo(ctx context.Context) (CacheInfo, error) {
	mgr, err := c.cacheManager()
	if err != nil {
		return CacheInfo{}, err
	}

	return mgr.GetCacheInfo(ctx)
}

// ClearCache deletes every downloaded archive, ignoring reference counts: an
// install mid-extraction holds one, but the user asked for the cache to go. The
// staged file of an in-flight download is already outside the cache.
func (c *Client) ClearCache(ctx context.Context) error {
	mgr, err := c.cacheManager()
	if err != nil {
		return err
	}

	return mgr.ClearCache(ctx)
}

// PruneCache deletes the least recently used archives until the cache fits.
// maxSizeGB and maxAgeDays are limits, not targets: zero or less means "no limit on
// that axis". The defaults come from the configuration.
func (c *Client) PruneCache(ctx context.Context, maxSizeGB, maxAgeDays int) error {
	mgr, err := c.cacheManager()
	if err != nil {
		return err
	}

	return mgr.Prune(ctx, maxSizeGB, maxAgeDays)
}

// PruneCacheWithDefaults applies the configured size and age limits.
func (c *Client) PruneCacheWithDefaults(ctx context.Context) error {
	mgr, err := c.cacheManager()
	if err != nil {
		return err
	}

	return mgr.PruneConfigured(ctx)
}
