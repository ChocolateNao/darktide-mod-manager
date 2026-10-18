package api

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ShareLink is a link that resolves a shared collection on one provider.
type ShareLink = domain.ShareLink

// MultiShareLink is a share uploaded to more than one provider.
type MultiShareLink = domain.MultiShareLink

// ShareRecord is the persisted metadata of a share.
type ShareRecord = domain.ShareRecord

// SharePreview is what a recipient sees before committing to an install.
type SharePreview = domain.SharePreview

// SharedMod is one mod inside a share payload.
type SharedMod = domain.SharedMod

// CloudProviderInfo describes a cloud provider a user can share to.
type CloudProviderInfo = domain.CloudProviderInfo

// shareService returns the share service, or the error to report.
//
// Deprecated: this resolves the cloud sharing service. Use p2pService for a new
// call site; the accessor stays because a share that already exists still has
// to be resolvable and revocable.
func (c *Client) shareService() (*services.ShareService, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.Shares == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a share service")
	}

	return c.deps.Shares, nil
}

// CreateShare uploads mods to one or more cloud providers and returns the shareable
// links. An empty providerIDs uses the configured default, so a caller that only knows
// "share this collection" does not have to learn provider ids.
func (c *Client) CreateShare(
	ctx context.Context,
	mods []Mod,
	providerIDs []string,
) (*MultiShareLink, error) {
	svc, err := c.shareService()
	if err != nil {
		return nil, err
	}

	domainMods := make([]domain.Mod, 0, len(mods))
	domainMods = append(domainMods, mods...)

	return svc.CreateShare(ctx, domainMods, providerIDs)
}

// ListShares returns every share this manager has created.
func (c *Client) ListShares(ctx context.Context) ([]ShareRecord, error) {
	svc, err := c.shareService()
	if err != nil {
		return nil, err
	}

	return svc.ListShares(ctx)
}

// ListSharesByGame returns the shares for one game, so the UI can scope the
// Shares tab to the game the user is currently on.
func (c *Client) ListSharesByGame(ctx context.Context, gameID string) ([]ShareRecord, error) {
	svc, err := c.shareService()
	if err != nil {
		return nil, err
	}

	return svc.ListSharesByGame(ctx, gameID)
}

// GetShare returns one share record by id.
func (c *Client) GetShare(ctx context.Context, id string) (*ShareRecord, error) {
	svc, err := c.shareService()
	if err != nil {
		return nil, err
	}

	return svc.GetShare(ctx, id)
}

// ResolveShare previews a share — the mod names, versions, authors and total size,
// without downloading anything. The preview carries the share's GameID so a recipient
// can see which game a share installs into before agreeing to the download.
func (c *Client) ResolveShare(ctx context.Context, link ShareLink) (*SharePreview, error) {
	svc, err := c.shareService()
	if err != nil {
		return nil, err
	}

	return svc.ResolveShare(ctx, link)
}

// InstallShare downloads and installs every mod in a share.
func (c *Client) InstallShare(
	ctx context.Context,
	link ShareLink,
	opts InstallShareOptions,
) ([]Mod, error) {
	svc, err := c.shareService()
	if err != nil {
		return nil, err
	}

	installed, err := svc.InstallShare(ctx, link, domain.InstallOptions{
		AutoInstall: opts.AutoInstall,
	})
	if err != nil {
		return nil, err
	}

	mods := make([]Mod, 0, len(installed))
	mods = append(mods, installed...)

	return mods, nil
}

// RevokeShare deletes the remote files for a share and forgets the record.
func (c *Client) RevokeShare(ctx context.Context, id string) error {
	svc, err := c.shareService()
	if err != nil {
		return err
	}

	return svc.RevokeShare(ctx, id)
}

// UpdateShare adds or removes mods from an existing share.
func (c *Client) UpdateShare(
	ctx context.Context,
	id string,
	addMods []Mod,
	removeMods []Mod,
) (*ShareRecord, error) {
	svc, err := c.shareService()
	if err != nil {
		return nil, err
	}

	toDomain := func(mods []Mod) []domain.Mod {
		out := make([]domain.Mod, 0, len(mods))
		out = append(out, mods...)

		return out
	}

	return svc.UpdateShare(ctx, id, toDomain(addMods), toDomain(removeMods))
}

// ShareLinkFromURL parses either carrier form of a share link into the link and
// its payload. A caller that receives a pasted URL needs both halves: the link
// to resolve, and the payload to show the user what it holds.
func (c *Client) ShareLinkFromURL(url string) (ShareLink, domain.SharePayload, error) {
	svc, err := c.shareService()
	if err != nil {
		return ShareLink{}, domain.SharePayload{}, err
	}

	return svc.ShareLinkFromURL(url)
}

// ListShareProviders lists the cloud providers this build knows how to share to.
func (c *Client) ListShareProviders() ([]CloudProviderInfo, error) {
	svc, err := c.shareService()
	if err != nil {
		return nil, err
	}

	return svc.ListProviders(), nil
}

// DefaultShareProvider reports the provider used when a caller does not name
// one.
func (c *Client) DefaultShareProvider() string {
	svc, err := c.shareService()
	if err != nil {
		return "s3"
	}

	return svc.DefaultProvider()
}
