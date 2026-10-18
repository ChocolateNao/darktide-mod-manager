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
	// Embedded so the lifecycle methods reach a set directly: a caller holding
	// one set must be able to shut it down without knowing the aggregate.
	*base
}

// NewShareCommands builds the share command set.
func NewShareCommands(deps Deps) *ShareCommands {
	return &ShareCommands{base: newBase(deps)}
}

func newShareCommands(b *base) *ShareCommands {
	return &ShareCommands{base: b}
}

// ListShares reports every share, newest first.
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

// ListSharesByGame reports the shares for one game, so the Shares tab can be
// scoped to the game the user is currently on.
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

// GetShare returns one share by id.
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

// CreateShare uploads mods to a cloud provider and returns the link.
//
// The provider list is the user's choice of where to upload. An empty list
// uses the configured default, which is what a "Share" button that does not
// ask should pass.
func (c *ShareCommands) CreateShare(
	modIDs []string,
	providerIDs []string,
) (*api.MultiShareLink, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	// The ids are resolved here rather than in the service: a mod that is not
	// installed has no files to upload, and the caller should hear that first.
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

// ResolveShare previews a link: what it holds, and which game it installs
// into, before a byte is downloaded.
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

// InstallShare downloads and installs every mod in a link.
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

// RevokeShare deletes the remote files and forgets the record.
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

// UpdateShare adds or removes mods from an existing share.
func (c *ShareCommands) UpdateShare(
	id string,
	addIDs, removeIDs []string,
) (*api.ShareRecord, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	// Both halves resolve through the same helper so a typo in either list
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

// ListShareProviders lists the providers the user can share to, so a picker
// can be rendered without knowing which plugins this build ships.
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

// DefaultShareProvider reports the provider a share uses when the user does
// not pick one.
func (c *ShareCommands) DefaultShareProvider() string {
	client, _, cancel, err := c.ready()
	if err != nil {
		return ""
	}
	defer cancel()

	return client.DefaultShareProvider()
}
