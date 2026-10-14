package commands

import (
	"context"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// P2PShareCommands is peer-to-peer sharing as a frontend drives it.
//
// The flow is deliberately shaped as four separate calls rather than one
// blocking one, because a share is a conversation between two people who are not
// necessarily at their computers at the same moment:
//
//  1. the sender packs the mods and gets an offer link,
//  2. the recipient previews that link and sees what it claims to contain plus
//     where each mod can be downloaded instead,
//  3. the recipient accepts (or rejects) and gets an answer link,
//  4. the sender feeds the answer in, and the recipient waits and installs.
//
// Nothing is received and nothing is installed until the recipient has had a
// preview and a decision, which is what makes declining a real option rather
// than a courtesy.
type P2PShareCommands struct {
	*base
}

// NewP2PShareCommands builds the peer-to-peer sharing command set.
func NewP2PShareCommands(deps Deps) *P2PShareCommands {
	return &P2PShareCommands{base: newBase(deps)}
}

func newP2PShareCommands(b *base) *P2PShareCommands {
	return &P2PShareCommands{base: b}
}

// CreateShare packs the given mods and returns the offer link to hand out.
func (c *P2PShareCommands) CreateShare(
	modIDs []string,
	expiresAt string,
	senderName string,
	stunServers []string,
) (*api.P2PShareLink, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	mods, err := c.resolveMods(ctx, client, modIDs)
	if err != nil {
		return nil, err
	}

	opts, err := createOptions(expiresAt, senderName, stunServers)
	if err != nil {
		return nil, err
	}

	link, err := client.CreateP2PShare(ctx, mods, opts)
	if err != nil {
		return nil, c.fail(err, "create a peer-to-peer share")
	}

	return link, nil
}

// createOptions turns the two fields a frontend collects into service options.
//
// The expiry arrives as a string because a JavaScript date is a string; an empty
// string means no expiry, which is the deliberate default — a share the sender
// did not put a time limit on is permanent, and the recipient is told so inside
// the link rather than by guessing.
func createOptions(
	expiresAt string,
	senderName string,
	stunServers []string,
) (domain.P2PCreateOptions, error) {
	opts := domain.P2PCreateOptions{SenderName: strings.TrimSpace(senderName)}

	if trimmed := strings.TrimSpace(expiresAt); trimmed != "" {
		when, err := time.Parse(time.RFC3339, trimmed)
		if err != nil {
			return domain.P2PCreateOptions{}, appErrors.New(appErrors.ErrCodeInvalidArgument,
				"expiry %q is not an RFC 3339 timestamp", expiresAt)
		}

		opts.ExpiresAt = &when
	}

	for _, server := range stunServers {
		if trimmed := strings.TrimSpace(server); trimmed != "" {
			opts.STUNServers = append(opts.STUNServers, trimmed)
		}
	}

	return opts, nil
}

// resolveMods turns the ids a frontend collected into the records the service
// packs. A frontend knows ids; the share is defined in terms of the mods
// themselves, and re-reading them here means the archives are built from what
// is on disk now rather than from what a stale form claimed.
func (c *P2PShareCommands) resolveMods(
	ctx context.Context,
	client *api.Client,
	modIDs []string,
) ([]domain.Mod, error) {
	if len(modIDs) == 0 {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"select at least one mod to share",
		)
	}

	mods := make([]domain.Mod, 0, len(modIDs))

	for _, id := range modIDs {
		mod, err := client.GetMod(ctx, strings.TrimSpace(id))
		if err != nil {
			return nil, c.fail(err, "read mod %q", id)
		}

		mods = append(mods, *mod)
	}

	return mods, nil
}

// PreviewShare describes an offer without connecting to anybody. This is the
// call that makes a decline informed: it reports the mods, their sizes, the
// fallback download locations and whether the link has already expired.
func (c *P2PShareCommands) PreviewShare(link string) (*api.P2PSharePreview, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	preview, err := client.PreviewP2PShare(ctx, strings.TrimSpace(link))
	if err != nil {
		return nil, c.fail(err, "preview the share")
	}

	return preview, nil
}

// AcceptShare answers an offer and returns the link to send back to the sender.
func (c *P2PShareCommands) AcceptShare(
	link string,
	receiverName string,
) (*api.P2PAnswerLink, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	answer, err := client.AcceptP2PShare(
		ctx,
		strings.TrimSpace(link),
		strings.TrimSpace(receiverName),
	)
	if err != nil {
		return nil, c.fail(err, "accept the share")
	}

	return answer, nil
}

// RejectShare declines an offer. The recipient gets nothing installed, and the
// sender is told why rather than being left to time out.
//
// The returned answer link has to be sent back to the sender, because the sender
// holds the connection offer and a refusal cannot travel until a channel opens.
// The call returns as soon as that link exists so the UI can show it; use
// AwaitRejectedShare to find out whether the sender ever got it.
func (c *P2PShareCommands) RejectShare(link string, reason string) (*api.P2PAnswerLink, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	answer, err := client.RejectP2PShare(
		ctx,
		strings.TrimSpace(link),
		strings.TrimSpace(reason),
	)
	if err != nil {
		return nil, c.fail(err, "reject the share")
	}

	return answer, nil
}

// AwaitRejectedShare reports whether a refusal the user already handed back
// reached the sender.
func (c *P2PShareCommands) AwaitRejectedShare(offerID string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.AwaitRejectedP2PShare(ctx, strings.TrimSpace(offerID)); err != nil {
		return c.fail(err, "deliver the refusal")
	}

	return nil
}

// SendShare completes a share the recipient accepted and transfers the archives.
func (c *P2PShareCommands) SendShare(offerID string, answerLink string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.SendP2PShare(
		ctx,
		strings.TrimSpace(offerID),
		strings.TrimSpace(answerLink),
	); err != nil {
		return c.fail(err, "send the share")
	}

	return nil
}

// AwaitShare receives a share the user accepted, verifying every archive's
// hash before reporting success.
func (c *P2PShareCommands) AwaitShare(offerID string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.AwaitP2PShare(ctx, strings.TrimSpace(offerID)); err != nil {
		return c.fail(err, "receive the share")
	}

	return nil
}

// InstallShare installs the verified archives of a completed share.
//
// Enabling is a parameter rather than a policy: the share may be several mods
// the user picked to try, and some installs are meant to sit in the folder
// disabled until the next launch.
func (c *P2PShareCommands) InstallShare(offerID string, enable bool) ([]api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	opts := domain.InstallOptions{AutoInstall: enable}

	installed, err := client.InstallP2PShare(ctx, strings.TrimSpace(offerID), opts)
	if err != nil {
		return nil, c.fail(err, "install the share")
	}

	if installed == nil {
		return []api.Mod{}, nil
	}

	return installed, nil
}

// GetShareSession reports the state of one share, so a frontend can re-attach
// to a transfer after a reload without guessing.
func (c *P2PShareCommands) GetShareSession(offerID string) (*api.P2PShareSession, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	session, err := client.GetP2PShareSession(ctx, strings.TrimSpace(offerID))
	if err != nil {
		return nil, c.fail(err, "read the share session")
	}

	return session, nil
}

// ListShareSessions reports every share this manager knows about.
func (c *P2PShareCommands) ListShareSessions() ([]api.P2PShareSession, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	sessions, err := client.ListP2PShareSessions(ctx)
	if err != nil {
		return nil, c.fail(err, "list share sessions")
	}

	if sessions == nil {
		return []api.P2PShareSession{}, nil
	}

	return sessions, nil
}

// CancelShare abandons a share: the peer connection closes and the staged
// archives are deleted.
func (c *P2PShareCommands) CancelShare(offerID string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.CancelP2PShare(ctx, strings.TrimSpace(offerID)); err != nil {
		return c.fail(err, "cancel the share")
	}

	return nil
}

// DiscardShare drops a finished share's staged archives without installing them.
func (c *P2PShareCommands) DiscardShare(offerID string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.DiscardP2PShare(ctx, strings.TrimSpace(offerID)); err != nil {
		return c.fail(err, "discard the share")
	}

	return nil
}

// DefaultSTUNServers lists the STUN servers a share uses unless the user
// configured others.
//
// A frontend shows this next to the link it hands out. STUN carries no mod data
// — only a reflexive address, so two peers can find each other — but it is still
// a third party the user's traffic touches, and a share is precisely the sort of
// thing a user is asked to trust blindly.
func (c *P2PShareCommands) DefaultSTUNServers() []string {
	client, _, cancel, err := c.ready()
	if err != nil {
		return nil
	}
	defer cancel()

	return client.DefaultSTUNServers()
}
