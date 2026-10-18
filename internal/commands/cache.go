package commands

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

// CacheCommands is the download cache as a frontend sees it: how much space it
// uses, and the two ways to reclaim some.
type CacheCommands struct {
	// Embedded so the lifecycle methods reach a set directly: a caller holding
	// one set must be able to shut it down without knowing the aggregate.
	*base
}

// NewCacheCommands builds the cache command set.
func NewCacheCommands(deps Deps) *CacheCommands {
	return &CacheCommands{base: newBase(deps)}
}

func newCacheCommands(b *base) *CacheCommands {
	return &CacheCommands{base: b}
}

// GetCacheInfo reports the cache's size and how many blobs it holds.
func (c *CacheCommands) GetCacheInfo() (api.CacheInfo, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return api.CacheInfo{}, err
	}
	defer cancel()

	info, err := client.GetCacheInfo(ctx)
	if err != nil {
		return api.CacheInfo{}, c.fail(err, "read cache info")
	}

	return info, nil
}

// ClearCache empties the cache, whether or not anything is in use.
func (c *CacheCommands) ClearCache() error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.ClearCache(ctx); err != nil {
		return c.fail(err, "clear the cache")
	}

	return nil
}

// PruneCache drops the least recently used blobs until the cache fits, and
// removes anything older than maxAgeDays.
//
// The numbers are limits, not targets. Both zero means "apply the configured
// limits": the settings screen has a prune button but no fields, and guessing a
// budget there would delete more than the user asked for. A negative means no
// limit on that axis, which is also what one zero means once the other axis
// carries a real limit. The service owns that convention and it is passed through
// rather than translated, so exactly one place decides what a number means.
func (c *CacheCommands) PruneCache(maxSizeGB, maxAgeDays int) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	var pruneErr error
	if maxSizeGB == 0 && maxAgeDays == 0 {
		pruneErr = client.PruneCacheWithDefaults(ctx)
	} else {
		pruneErr = client.PruneCache(ctx, maxSizeGB, maxAgeDays)
	}

	if pruneErr != nil {
		return c.fail(
			pruneErr,
			"prune the cache (size %d GB, max age %d days)",
			maxSizeGB,
			maxAgeDays,
		)
	}

	return nil
}

// PruneWithDefaults is PruneCache's configured-limits form spelled out, so a
// frontend whose button has no fields next to it does not have to send two
// zeroes to mean "the defaults".
func (c *CacheCommands) PruneWithDefaults() error {
	return c.PruneCache(0, 0)
}

// CacheRoot is the folder holding the cache. The frontend shows it so a user can
// find it with a file manager when they want to delete something themselves.
func (c *CacheCommands) CacheRoot() (string, error) {
	client, _, _, err := c.ready()
	if err != nil {
		return "", err
	}

	info, err := client.GetCacheInfo(c.Context())
	if err != nil {
		return "", c.fail(err, "read cache info")
	}

	return strings.TrimSpace(info.Path), nil
}
