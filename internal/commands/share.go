package commands

import (
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ShareCommands is the sharing surface the frontend drives: what the user has
// shared, who to, and the two ways out of a share once it exists.
//
// Deprecated: a cloud share uploads the mods to a storage provider, so a third
// party holds the files even when it never looks at them. The peer-to-peer
// surface in P2PShareCommands replaces it and should be offered first; this set
// stays so an existing share can still be listed and revoked, which is the one
// operation that cannot be dropped without stranding someone's data.
type ShareCommands struct {
	// Embedded so a caller holding one set can shut it down without the
	// aggregate.
	*base
}

func NewShareCommands(deps Deps) *ShareCommands {
	return &ShareCommands{base: newBase(deps)}
}

func newShareCommands(b *base) *ShareCommands {
	return &ShareCommands{base: b}
}

func (c *ShareCommands) ListShares() ([]api.ShareRecord, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	shares, err := client.ListShares(ctx)
	if err != nil {
		return nil, c.fail(err, "list shares")
	}

	return shares, nil
}

func (c *ShareCommands) ListSharesByGame(gameID string) ([]api.ShareRecord, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	shares, err := client.ListSharesByGame(ctx, gameID)
	if err != nil {
		return nil, c.fail(err, "list shares for game")
	}

	return shares, nil
}

func (c *ShareCommands) GetShare(id string) (*api.ShareRecord, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	share, err := client.GetShare(ctx, id)
	if err != nil {
		return nil, c.fail(err, "read share")
	}

	return share, nil
}

func (c *ShareCommands) CreateShare(
	modIDs []string,
	providerIDs []string,
) (*api.MultiShareLink, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	// The ids are resolved here so an uninstalled mod is reported before the
	// service is called.
	mods := make([]api.Mod, 0, len(modIDs))

	for _, id := range modIDs {
		mod, err := client.GetMod(ctx, id)
		if err != nil {
			return nil, c.fail(err, "read mod to share")
		}

		mods = append(mods, *mod)
	}

	if len(mods) == 0 {
		c.logf("create share: no installed mods matched the requested ids")

		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"no installed mods matched the requested ids")
	}

	result, err := client.CreateShare(ctx, mods, providerIDs)
	if err != nil {
		return nil, c.fail(err, "create share")
	}

	return result, nil
}

func (c *ShareCommands) ResolveShare(url string) (*api.SharePreview, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	link, _, err := client.ShareLinkFromURL(url)
	if err != nil {
		return nil, c.fail(err, "parse share link")
	}

	preview, err := client.ResolveShare(ctx, link)
	if err != nil {
		return nil, c.fail(err, "resolve share")
	}

	return preview, nil
}

func (c *ShareCommands) InstallShare(url string) ([]api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	link, _, err := client.ShareLinkFromURL(url)
	if err != nil {
		return nil, c.fail(err, "parse share link")
	}

	mods, err := client.InstallShare(ctx, link, api.InstallShareOptions{})
	if err != nil {
		return nil, c.fail(err, "install share")
	}

	return mods, nil
}

func (c *ShareCommands) RevokeShare(id string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.RevokeShare(ctx, id); err != nil {
		return c.fail(err, "revoke share")
	}

	return nil
}

func (c *ShareCommands) UpdateShare(
	id string,
	addIDs, removeIDs []string,
) (*api.ShareRecord, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	// Both halves resolve through the same helper, so a typo in either list
	// reports the same way.
	resolve := func(ids []string) ([]api.Mod, error) {
		mods := make([]api.Mod, 0, len(ids))

		for _, modID := range ids {
			mod, err := client.GetMod(ctx, modID)
			if err != nil {
				return nil, err
			}

			mods = append(mods, *mod)
		}

		return mods, nil
	}

	addMods, err := resolve(addIDs)
	if err != nil {
		return nil, c.fail(err, "read mods to add")
	}

	removeMods, err := resolve(removeIDs)
	if err != nil {
		return nil, c.fail(err, "read mods to remove")
	}

	record, err := client.UpdateShare(ctx, id, addMods, removeMods)
	if err != nil {
		return nil, c.fail(err, "update share")
	}

	return record, nil
}

func (c *ShareCommands) ListShareProviders() ([]api.CloudProviderInfo, error) {
	client, _, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	providers, err := client.ListShareProviders()
	if err != nil {
		return nil, c.fail(err, "list share providers")
	}

	return providers, nil
}

func (c *ShareCommands) DefaultShareProvider() string {
	client, _, cancel, err := c.ready()
	if err != nil {
		return ""
	}
	defer cancel()

	return client.DefaultShareProvider()
}
