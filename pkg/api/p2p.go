package api

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// The peer-to-peer sharing types are re-exported so a frontend binding or a CLI
// describes a share without importing the domain package as well.
type (
	// P2PShareLink is an offer: the link a sender hands to a recipient.
	P2PShareLink = domain.P2PShareLink
	// P2PAnswerLink is the reply a recipient hands back to the sender.
	P2PAnswerLink = domain.P2PAnswerLink
	// P2PSharePreview is what an offer claims to contain, readable without
	// connecting to anybody.
	P2PSharePreview = domain.P2PSharePreview
	// P2PShareSession is the manager's view of one in-flight or finished share.
	P2PShareSession = domain.P2PShareSession
	// P2PCreateOptions tunes a new share.
	P2PCreateOptions = domain.P2PCreateOptions
	// P2PModSourceLink is the fallback download location for one mod.
	P2PModSourceLink = domain.ModSourceLink
)

// p2pService returns the peer-to-peer sharing service.
func (c *Client) p2pService() (*services.P2PShareService, error) {
	if c == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "the client is not initialised")
	}

	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.P2PShares == nil {
		return nil, appErrors.New(appErrors.ErrCodeShareNotFound,
			"peer-to-peer sharing is not available in this session")
	}

	return c.deps.P2PShares, nil
}

// CreateP2PShare packs the given mods and produces a link to hand out. The work
// happens here rather than when the recipient accepts: the archives are packed and
// hashed and the peer's connection details gathered, so the link the sender receives
// is already a working offer. Expiry is optional — a share with no expiry is
// permanent, and the recipient sees the expiry inside the link when one was set.
func (c *Client) CreateP2PShare(
	ctx context.Context,
	mods []domain.Mod,
	opts domain.P2PCreateOptions,
) (*domain.P2PShareLink, error) {
	service, err := c.p2pService()
	if err != nil {
		return nil, err
	}

	return service.CreateShare(ctx, mods, opts)
}

// PreviewP2PShare describes an offer without connecting to the sender. This is what
// lets a recipient decide whether to trust the link: it opens the sealed link
// locally, reports the mods, the sizes and the fallback download locations, and says
// whether the link has already expired.
func (c *Client) PreviewP2PShare(
	ctx context.Context,
	link string,
) (*domain.P2PSharePreview, error) {
	service, err := c.p2pService()
	if err != nil {
		return nil, err
	}

	return service.PreviewOffer(ctx, link)
}

// AcceptP2PShare answers an offer and returns the link to send back.
//
// Accepting does not yet receive anything: it completes the connection
// description. The transfer only starts once the sender has this answer, so a
// recipient can stop here, decide to reject instead, or simply do nothing.
func (c *Client) AcceptP2PShare(
	ctx context.Context,
	link string,
	receiverName string,
) (*domain.P2PAnswerLink, error) {
	service, err := c.p2pService()
	if err != nil {
		return nil, err
	}

	return service.AcceptOffer(ctx, link, receiverName)
}

// RejectP2PShare declines an offer and falls back to the source links. It returns
// the answer link, which has to be handed back to the sender: the sender holds the
// connection offer, so a refusal cannot reach it until a channel opens, and a channel
// does not open without an answer. The call returns as soon as that link exists —
// blocking would leave the caller with nothing to show the sender — and
// AwaitRejectedP2PShare waits for the outcome.
//
// The fallback is the point: the preview already carried a Nexus or GitHub page for
// every mod that has one, and a sender told "I would rather take it from there"
// learns something a silent timeout never would.
func (c *Client) RejectP2PShare(
	ctx context.Context,
	link string,
	reason string,
) (*P2PAnswerLink, error) {
	service, err := c.p2pService()
	if err != nil {
		return nil, err
	}

	return service.RejectOffer(ctx, link, reason)
}

// AwaitRejectedP2PShare waits for a refusal to reach the sender, reporting whether
// handing over the answer link actually worked.
func (c *Client) AwaitRejectedP2PShare(ctx context.Context, offerID string) error {
	service, err := c.p2pService()
	if err != nil {
		return err
	}

	return service.AwaitRejection(ctx, offerID)
}

// SendP2PShare completes a share the recipient accepted: the sender feeds the
// answer link in and the archives go over the data channel.
func (c *Client) SendP2PShare(ctx context.Context, offerID string, answerLink string) error {
	service, err := c.p2pService()
	if err != nil {
		return err
	}

	return service.SendShare(ctx, offerID, answerLink)
}

// AwaitP2PShare receives a share the recipient accepted and blocks until it has
// finished verifying every archive, or the context ends.
func (c *Client) AwaitP2PShare(ctx context.Context, offerID string) error {
	service, err := c.p2pService()
	if err != nil {
		return err
	}

	return service.AwaitShare(ctx, offerID)
}

// InstallP2PShare installs the verified archives of a completed share. It is a
// separate step from receiving on purpose: the bytes are on disk and hashed, but
// installing them changes the user's game folder, and a recipient who only wanted the
// files should be able to stop short of that.
func (c *Client) InstallP2PShare(
	ctx context.Context,
	offerID string,
	opts domain.InstallOptions,
) ([]domain.Mod, error) {
	service, err := c.p2pService()
	if err != nil {
		return nil, err
	}

	return service.InstallShare(ctx, offerID, opts)
}

// GetP2PShareSession returns the manager's view of one share, or nil when no
// share with that id is in flight or was never seen.
func (c *Client) GetP2PShareSession(
	ctx context.Context,
	offerID string,
) (*domain.P2PShareSession, error) {
	service, err := c.p2pService()
	if err != nil {
		return nil, err
	}

	return service.GetShareSession(ctx, offerID)
}

// ListP2PShareSessions returns every share this manager knows about, newest
// first. A frontend uses it to re-attach to a transfer after a reload; the CLI
// uses it to inspect a session it created in an earlier invocation.
func (c *Client) ListP2PShareSessions(ctx context.Context) ([]domain.P2PShareSession, error) {
	service, err := c.p2pService()
	if err != nil {
		return nil, err
	}

	return service.ListShareSessions(ctx), nil
}

// CancelP2PShare abandons a share: the peer connection is closed and the staged
// archives are deleted, whether the share is half-finished or already finished
// but not yet installed.
func (c *Client) CancelP2PShare(ctx context.Context, offerID string) error {
	service, err := c.p2pService()
	if err != nil {
		return err
	}

	return service.CancelShare(ctx, offerID)
}

// DiscardP2PShare drops a finished share's staged archives without installing
// them.
func (c *Client) DiscardP2PShare(ctx context.Context, offerID string) error {
	service, err := c.p2pService()
	if err != nil {
		return err
	}

	return service.DiscardShare(ctx, offerID)
}

// DefaultSTUNServers lists the STUN servers a share would use by default. The CLI
// prints it next to every link it hands out: STUN carries no mod data — only a
// reflexive address, so the peers can find each other — but it is still a third party
// the user's traffic touches, and saying so is cheaper than explaining it afterwards.
func (c *Client) DefaultSTUNServers() []string {
	service, err := c.p2pService()
	if err != nil {
		return nil
	}

	return service.DefaultSTUNServers()
}
