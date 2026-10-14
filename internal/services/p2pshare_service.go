// Package services: peer-to-peer mod sharing.
//
// The orchestrator here sits between two very different things: the sealed,
// human-readable links that travel through a chat window, and the live WebRTC
// connection that carries the bytes. It owns the parts neither of them can: which
// game a share belongs to, what the recipient is shown before deciding, where
// archives are staged on both sides, and what the session looks like to a
// frontend polling for progress.
//
// Nothing in this file is the protocol. Framing, the handshake and hash
// verification live in internal/sharing/p2p, because they are the parts that have
// to agree byte for byte across two machines and have no business depending on a
// storage path or a game registry.
package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/p2p"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// p2pRoleSender and p2pRoleRecipient are the two ends of a share. They are
// strings rather than the protocol's Role byte because they end up in a session
// record that a frontend renders verbatim.
const (
	p2pRoleSender    = "sender"
	p2pRoleRecipient = "recipient"
)

// staging patterns for the two sides of a transfer.
//
// They are distinct so that an operator reading a filled temp directory can tell
// at a glance whether a folder holds bytes waiting to be sent or bytes waiting to
// be verified and installed.
const (
	senderStagingPattern    = "dmm-p2p-send-*"
	recipientStagingPattern = "dmm-p2p-recv-*"
)

// P2PShareConfig is the transport configuration handed to every peer this service
// creates.
//
// It is a thin wrapper around p2p.Config rather than a copy of its fields: the
// settings are meaningful only to the WebRTC layer, and duplicating them here
// would be a second place to forget a knob. Per-share overrides are applied on a
// copy so creating one unusual share does not reconfigure the next one.
type P2PShareConfig struct {
	// Peer is the template every peer starts from.
	Peer p2p.Config
	// DefaultExpiry applies to a share whose creator did not choose an expiry.
	// Zero means "permanent", which is the default on purpose: a link that
	// silently stops working is worse than one the user has to revoke.
	DefaultExpiry time.Duration
}

// DefaultP2PShareConfig returns the configuration used unless the caller supplies
// its own.
//
// The default ICE servers are Google's public STUN, which is enough for the
// symmetric-NAT case that motivated peer-to-peer sharing in the first place. No
// TURN is configured: a relay would be the third party this feature exists to
// avoid, and it would see the mod bytes, so adding one is a deliberate decision
// rather than a default.
func DefaultP2PShareConfig() P2PShareConfig {
	return P2PShareConfig{Peer: p2p.Config{STUNServers: p2p.DefaultSTUNServers()}}
}

// P2PShareService orchestrates peer-to-peer shares.
//
// One instance lives for the lifetime of the process. That matters more than it
// sounds: a WebRTC peer connection cannot be reconstructed from a link, so every
// share in flight — including the peer's socket buffers and the archives staged
// for it — has to live in memory until it finishes or is abandoned.
type P2PShareService struct {
	logger       zerolog.Logger
	gameLocator  ShareGameLocator
	modInstaller ModInstaller
	store        *p2pSessionStore
	now          func() time.Time
	config       P2PShareConfig
}

// P2PShareServiceDeps are the collaborators the service needs.
type P2PShareServiceDeps struct {
	// GameLocator answers which game is active, for shares whose mods carry no
	// game id of their own.
	GameLocator ShareGameLocator
	// ModInstaller installs a verified archive. It is the same installer the
	// cloud share uses, so a mod that arrives over a data channel is unpacked,
	// registered and enabled by exactly the code path a mod from a provider goes
	// through.
	ModInstaller ModInstaller
	// Logger receives share lifecycle events.
	Logger zerolog.Logger
	// Config is the transport configuration. Nil means the default.
	Config *P2PShareConfig
	// Clock is the time source. Nil means time.Now. It is injectable so expiry
	// can be tested without sleeping.
	Clock func() time.Time
}

// NewP2PShareService builds the service with its session store.
func NewP2PShareService(deps P2PShareServiceDeps) *P2PShareService {
	config := DefaultP2PShareConfig()
	if deps.Config != nil {
		config = *deps.Config
	}

	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}

	return &P2PShareService{
		store:        newP2PSessionStore(),
		gameLocator:  deps.GameLocator,
		modInstaller: deps.ModInstaller,
		logger:       deps.Logger,
		config:       config,
		now:          clock,
	}
}

// Close abandons every share in flight and removes its staging.
//
// It is registered as a bundle cleanup rather than left to the process exiting:
// a desktop app that restarts without exiting (the Wails reload path) would
// otherwise leave every socket from the previous run open, and a peer that never
// gets a close keeps gathering ICE in the background.
func (s *P2PShareService) Close() {
	if s == nil {
		return
	}

	s.store.drain(func(rec *p2pShareRecord) {
		_ = rec.peer.Close()
		_ = os.RemoveAll(rec.stageDir)
	})
}

// CreateShare packs the mods, opens a peer connection and returns the offer link.
//
// The returned link is the whole share as far as the recipient is concerned: it
// carries the SDP, the inventory, the hashes and the secret that authorises the
// transfer, sealed so nothing in it renders as readable text in a chat preview.
//
// Everything expensive happens before the link exists — packing, hashing,
// ICE gathering — so that a link handed to somebody else is known to be backed
// by bytes that are on disk right now and reachable right now. A link that
// materialised later, once the sender's disk filled or the network moved, would
// be a promise the manager cannot keep.
func (s *P2PShareService) CreateShare(
	ctx context.Context,
	mods []domain.Mod,
	opts domain.P2PCreateOptions,
) (*domain.P2PShareLink, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	if len(mods) == 0 {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "no mods to share")
	}

	gameID, err := s.resolveGameID(mods)
	if err != nil {
		return nil, err
	}

	// The game is checked here rather than only when the recipient opens the
	// link. A share of mods for a game this build does not know cannot succeed
	// on the far side either, so catching it at the prompt that created it saves
	// the user from pasting a link that was never going to work.
	if _, known := games.Get(gameID); !known {
		return nil, appErrors.New(appErrors.ErrCodeGameNotRegistered,
			"game %q is not registered in this build, so its mods cannot be shared", gameID)
	}

	staging, err := os.MkdirTemp("", senderStagingPattern)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"create a staging folder to pack the mods in")
	}

	// Everything below this point either succeeds and hands the staging folder to
	// a session, or fails and has to take the folder with it. A single deferred
	// cleanup flag keeps that honest without a dozen error paths each remembering
	// to remove a directory.
	keepStaging := false

	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	packed, err := packP2PShare(ctx, staging, mods)
	if err != nil {
		return nil, err
	}

	cfg := s.config.Peer
	if len(opts.STUNServers) > 0 {
		cfg.STUNServers = opts.STUNServers
	}

	if opts.ChunkSize > 0 {
		cfg.ChunkSize = opts.ChunkSize
	}

	if err := cfg.Validate(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigInvalid,
			"p2p transport configuration",
		)
	}

	peer, err := p2p.NewSenderPeer(cfg)
	if err != nil {
		return nil, wrapTransport(err, "open the sending connection")
	}

	offerSDP, err := peer.CreateOffer(ctx)
	if err != nil {
		_ = peer.Close()

		return nil, wrapTransport(err, "gather connection candidates for the offer")
	}

	offer := buildP2POffer(gameID, packed, opts, cfg.STUNServers)

	// A configured default expiry applies only when the creator chose none, so
	// setting a policy in config never overrides an explicit choice on the
	// command line or in the UI.
	if offer.ExpiresAt == nil && s.config.DefaultExpiry > 0 {
		deadline := time.Now().Add(s.config.DefaultExpiry)
		offer.ExpiresAt = &deadline
	}

	link, err := p2p.LinkOffer(offer, offerSDP)
	if err != nil {
		_ = peer.Close()

		return nil, appErrors.Wrap(err, appErrors.ErrCodeShareInvalid, "seal the offer link")
	}

	var (
		total    int64
		modIDs   []string
		expiry   *time.Time
		created  = s.now()
		linkText = link.Link
	)

	for i := range offer.Files {
		total += offer.Files[i].Size
	}

	for i := range packed {
		modIDs = append(modIDs, packed[i].mod.ID)
	}

	if offer.ExpiresAt != nil {
		deadline := *offer.ExpiresAt
		expiry = &deadline
	}

	s.store.add(&p2pShareRecord{
		offerID:   offer.OfferID,
		offer:     offer,
		secret:    link.Secret,
		link:      linkText,
		role:      p2pRoleSender,
		state:     domain.P2PShareStateCreated,
		stageDir:  staging,
		peer:      peer,
		modIDs:    modIDs,
		totalSize: total,
		createdAt: created,
		updatedAt: created,
		done:      make(chan struct{}),
	})

	keepStaging = true

	s.logger.Info().
		Str("offer_id", offer.OfferID).
		Str("game_id", gameID).
		Int("mod_count", len(mods)).
		Int("file_count", len(offer.Files)).
		Int64("total_bytes", total).
		Str("expires_at", expiryLabel(expiry)).
		Msg("p2p share created")

	return link, nil
}

// PreviewOffer opens a link and reports what it offers, without connecting.
//
// This is the recipient's only chance to judge the sender before any byte moves,
// so it answers the questions the UI actually has to render: which game, which
// mods, how large, whether the link has expired, and — crucially — which upstream
// each mod can be fetched from instead, for the recipient who would rather not
// take the bytes at all.
func (s *P2PShareService) PreviewOffer(
	ctx context.Context,
	linkText string,
) (*domain.P2PSharePreview, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	link, err := s.resolveOffer(ctx, linkText)
	if err != nil {
		return nil, err
	}

	preview := buildP2PPreview(link)

	s.logger.Debug().
		Str("offer_id", preview.OfferID).
		Str("game_id", preview.GameID).
		Int("mod_count", len(preview.Mods)).
		Int("file_count", len(preview.Files)).
		Msg("p2p offer previewed")

	return preview, nil
}

// AcceptOffer answers an offer and returns the answer link.
//
// The connection is established here — the answer SDP is gathered and returned —
// but nothing is read until AwaitShare is called. Splitting the two is what lets
// a frontend show the answer link, hand it back to the sender out of band, and
// only then start blocking on progress.
//
// The session is recorded before the answer is returned so that a transfer which
// starts before the caller gets its answer back still has somewhere to publish
// progress to.
func (s *P2PShareService) AcceptOffer(
	ctx context.Context,
	linkText string,
	receiverName string,
) (*domain.P2PAnswerLink, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	link, err := s.resolveOffer(ctx, linkText)
	if err != nil {
		return nil, err
	}

	destDir, err := os.MkdirTemp("", recipientStagingPattern)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"create a staging folder for the received archives")
	}

	cfg := s.config.Peer
	if stun := link.Offer.STUNServers; len(stun) > 0 {
		cfg.STUNServers = stun
	}

	if err := cfg.Validate(); err != nil {
		_ = os.RemoveAll(destDir)

		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigInvalid,
			"p2p transport configuration",
		)
	}

	peer, err := p2p.NewReceiverPeer(cfg)
	if err != nil {
		_ = os.RemoveAll(destDir)

		return nil, wrapTransport(err, "open the receiving connection")
	}

	answerSDP, err := peer.CreateAnswer(ctx, link.SDP)
	if err != nil {
		_ = peer.Close()
		_ = os.RemoveAll(destDir)

		return nil, wrapTransport(err, "gather connection candidates for the answer")
	}

	answer, err := p2p.LinkAnswer(link.Offer.OfferID, answerSDP, link.Secret, receiverName)
	if err != nil {
		_ = peer.Close()
		_ = os.RemoveAll(destDir)

		return nil, appErrors.Wrap(err, appErrors.ErrCodeShareInvalid, "seal the answer link")
	}

	s.store.add(s.newRecipientRecord(link, peer, destDir))

	s.logger.Info().
		Str("offer_id", link.Offer.OfferID).
		Str("game_id", link.Offer.GameID).
		Str("receiver", receiverName).
		Msg("p2p offer accepted")

	return answer, nil
}

// RejectOffer declines a share, hands back the answer link so the sender can open
// the connection, and delivers the refusal over it in the background.
//
// The answer link is returned rather than discarded, and this is the whole reason
// a refusal can work at all: the sender holds the connection offer, so without an
// answer it never opens a data channel, and a refusal sent into a channel that
// has not opened is indistinguishable from a peer that went away. The sender
// would learn nothing, which is exactly the outcome a recipient who declined
// wants to avoid — they want the sender to take the mod from its own page.
//
// It returns as soon as the answer is sealed rather than blocking on delivery,
// because blocking would deadlock the one flow that matters: the caller has to be
// able to show the answer link, and the sender cannot open the connection until
// they have it. AwaitRejection is what waits for the outcome.
func (s *P2PShareService) RejectOffer(
	ctx context.Context,
	linkText, reason string,
) (*domain.P2PAnswerLink, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	link, err := s.resolveOffer(ctx, linkText)
	if err != nil {
		return nil, err
	}

	destDir, err := os.MkdirTemp("", recipientStagingPattern)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"create a staging folder to answer in")
	}

	cfg := s.config.Peer
	if stun := link.Offer.STUNServers; len(stun) > 0 {
		cfg.STUNServers = stun
	}

	peer, err := p2p.NewReceiverPeer(cfg)
	if err != nil {
		_ = os.RemoveAll(destDir)

		return nil, wrapTransport(err, "open the receiving connection")
	}

	answerSDP, err := peer.CreateAnswer(ctx, link.SDP)
	if err != nil {
		_ = peer.Close()
		_ = os.RemoveAll(destDir)

		return nil, wrapTransport(err, "gather connection candidates for the answer")
	}

	answer, err := p2p.LinkAnswer(link.Offer.OfferID, answerSDP, link.Secret, "")
	if err != nil {
		_ = peer.Close()
		_ = os.RemoveAll(destDir)

		return nil, wrapTransport(err, "seal the answer link")
	}

	session := s.newRecipientRecord(link, peer, destDir)
	session.state = domain.P2PShareStateRejected
	session.failReason = reason

	s.store.add(session)

	// The delivery context is detached from ctx on purpose. ctx belongs to the
	// call that produced the answer link, and that call returns as soon as the
	// link is ready; cancelling with it would abort the refusal the moment the
	// user closed the dialog. Close still tears the peer down.
	deliverCtx := context.WithoutCancel(ctx)

	go s.deliverRejection(deliverCtx, session, reason)

	return answer, nil
}

// deliverRejection pushes the refusal across the channel and records the outcome.
func (s *P2PShareService) deliverRejection(
	ctx context.Context,
	session *p2pShareRecord,
	reason string,
) {
	err := p2p.Reject(ctx, p2p.Session{
		Peer:   session.peer,
		Secret: session.secret,
		Offer:  session.offer,
	}, reason)

	defer s.store.remove(session.offerID, true)
	defer session.signal()
	// The session is dropped whether or not the abort reached the sender: there
	// are no archives to install either way, and keeping a rejected share
	// occupying memory would let a recipient accumulate one per link they were
	// ever sent.
	//
	// The outcome is recorded on the session, not in the store: a waiter woken
	// by signal() could lose the race against remove() and then read a missing
	// record as success.
	if err != nil {
		session.rejectErr = appErrors.New(appErrors.ErrCodeShareRevoked,
			"the refusal did not reach the sender: %s", err)

		s.store.mark(session.offerID, domain.P2PShareStateFailed, session.rejectErr.Error())

		return
	}

	s.store.mark(session.offerID, domain.P2PShareStateRejected, reason)

	s.logger.Info().
		Str("offer_id", session.offerID).
		Str("reason", reason).
		Msg("p2p offer rejected")
}

// AwaitRejection blocks until a refusal this process sent has been delivered, or
// the context ends.
//
// It exists because the sender is the half that has to be running: the refusal
// travels on a channel the sender opens, so the recipient's own refusal is not
// finished until the sender has picked it up. A caller that never waits is not
// wrong, it is simply not told.
func (s *P2PShareService) AwaitRejection(ctx context.Context, offerID string) error {
	if s == nil {
		return appErrors.ErrInvalidArgument
	}

	session, err := s.store.load(offerID)
	if err != nil {
		return err
	}

	select {
	case <-session.done:
	case <-ctx.Done():
		return appErrors.Wrap(ctx.Err(), appErrors.ErrCodeOperationCanceled,
			"wait for the refusal to be delivered")
	}

	return session.rejectErr
}

// SendShare completes a share the sender created: it applies the recipient's
// answer and streams every archive.
//
// It blocks for the length of the transfer. Progress is published to the session
// record as it goes, so a frontend shows a moving bar by polling GetShareSession
// rather than by receiving per-byte events, and a CLI simply waits.
func (s *P2PShareService) SendShare(ctx context.Context, offerID, answerLink string) error {
	if s == nil {
		return appErrors.ErrInvalidArgument
	}

	rec, err := s.store.load(offerID)
	if err != nil {
		return err
	}

	if rec.role != p2pRoleSender {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"share %s is a recipient session, not a sender session", offerID)
	}

	if err := s.checkLive(rec); err != nil {
		return err
	}

	answer, err := p2p.OpenAnswer(answerLink)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeShareInvalid, "open the answer link")
	}

	// The answer names the offer it belongs to. Checking it here means a
	// recipient who pastes the wrong answer into the wrong share is told so
	// instead of watching a connection that never opens.
	if answer.OfferID != rec.offerID {
		return appErrors.New(appErrors.ErrCodeShareInvalid,
			"answer link is for offer %s, this share is %s", answer.OfferID, rec.offerID)
	}

	if err := rec.peer.AcceptAnswer(answer.SDP); err != nil {
		return wrapTransport(err, "apply the recipient's answer")
	}

	s.store.mark(offerID, domain.P2PShareStateConnecting, "")

	session := p2p.Session{Peer: rec.peer, Secret: rec.secret, Offer: rec.offer}
	transferErr := p2p.Send(ctx, session, rec.stageDir, s.progressReporter(offerID))

	s.finish(offerID, transferErr)

	if transferErr != nil {
		return translateTransferError(transferErr)
	}

	s.logger.Info().
		Str("offer_id", offerID).
		Int("file_count", len(rec.offer.Files)).
		Int64("total_bytes", rec.totalSize).
		Msg("p2p share sent")

	return nil
}

// AwaitShare receives a share the recipient accepted and blocks until the
// transfer ends.
//
// Every archive is verified by the protocol against the hashes in the offer
// before this returns, so a nil error means the bytes in the staging folder are
// the bytes the sender promised — which is what makes InstallShare safe to trust.
func (s *P2PShareService) AwaitShare(ctx context.Context, offerID string) error {
	if s == nil {
		return appErrors.ErrInvalidArgument
	}

	rec, err := s.store.load(offerID)
	if err != nil {
		return err
	}

	if rec.role != p2pRoleRecipient {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"share %s is a sender session, not a recipient session", offerID)
	}

	session := p2p.Session{Peer: rec.peer, Secret: rec.secret, Offer: rec.offer}
	transferErr := p2p.Receive(ctx, session, rec.stageDir, s.progressReporter(offerID))

	s.finish(offerID, transferErr)

	if transferErr != nil {
		return translateTransferError(transferErr)
	}

	s.logger.Info().
		Str("offer_id", offerID).
		Int("file_count", len(rec.offer.Files)).
		Int64("total_bytes", rec.totalSize).
		Msg("p2p share received")

	return nil
}

// InstallShare installs a received share's archives.
//
// The hashes are recomputed here rather than trusted from the transfer: the
// archives have been sitting in a world-readable-by-the-user temporary directory
// since the transfer finished, and this is the last point at which a file that is
// not what the sender promised can still be refused. It costs one extra pass over
// the bytes and removes the only window in which a local process could swap them.
//
// The upstream links travel with the offer, so a mod installed this way keeps the
// source it came from. That is what lets it be updated later by following the link
// instead of waiting for another share from the same person.
func (s *P2PShareService) InstallShare(
	ctx context.Context,
	offerID string,
	opts domain.InstallOptions,
) ([]domain.Mod, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	rec, err := s.store.load(offerID)
	if err != nil {
		return nil, err
	}

	if rec.role != p2pRoleRecipient {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"share %s is a sender session, not a recipient session", offerID)
	}

	if rec.state != domain.P2PShareStateCompleted {
		return nil, appErrors.New(appErrors.ErrCodeShareInvalid,
			"share %s is %s, not completed", offerID, rec.state)
	}

	if s.modInstaller == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigInvalid,
			"no mod installer is configured for p2p shares")
	}

	sharedByArchive := sharedModsByArchive(rec.offer)

	installed := make([]domain.Mod, 0, len(rec.offer.Files))

	for i := range rec.offer.Files {
		file := rec.offer.Files[i]

		shared, ok := sharedByArchive[file.Name]
		if !ok {
			return nil, appErrors.New(appErrors.ErrCodeShareInvalid,
				"offer lists archive %q with no mod behind it", file.Name)
		}

		archivePath := filepath.Join(rec.stageDir, file.Name)
		if err := verifyStagedArchive(archivePath, file); err != nil {
			return nil, err
		}

		mod := buildInstalledMod(rec.offer, shared, file)

		result, err := s.modInstaller.InstallArchiveFromShare(
			ctx,
			&mod,
			archivePath,
			shareInstallOptions(opts),
		)
		if err != nil {
			s.logger.Warn().Str("mod", mod.Name).Err(err).Msg("install from p2p share failed")
			return nil, err
		}

		result.GameID = rec.offer.GameID
		installed = append(installed, *result)
	}

	// The archives are removed once they have been extracted. They are a full
	// copy of every shared mod, and leaving them in the temp directory would let
	// every share ever received accumulate.
	s.store.remove(offerID, true)

	s.logger.Info().
		Str("offer_id", offerID).
		Str("game_id", rec.offer.GameID).
		Int("installed_count", len(installed)).
		Msg("p2p share installed")

	return installed, nil
}

// DiscardShare drops a received share without installing it.
//
// This is the path a recipient takes after choosing the upstream source links over
// the peer's bytes, and it is also how a failed transfer is cleaned up: either
// way the staging folder holds nothing worth keeping.
func (s *P2PShareService) DiscardShare(_ context.Context, offerID string) error {
	if s == nil {
		return appErrors.ErrInvalidArgument
	}

	if _, ok := s.store.peek(offerID); !ok {
		return appErrors.New(
			appErrors.ErrCodeShareNotFound,
			"share %s is not in this session",
			offerID,
		)
	}

	s.store.remove(offerID, true)

	s.logger.Info().Str("offer_id", offerID).Msg("p2p share discarded")

	return nil
}

// CancelShare abandons a share this manager created as the sender.
//
// The peer is closed and the staged archives removed. The recipient, if they ever
// connect, sees the connection drop rather than a clean refusal — which is the
// honest outcome, because there is nobody left to tell.
func (s *P2PShareService) CancelShare(_ context.Context, offerID string) error {
	if s == nil {
		return appErrors.ErrInvalidArgument
	}

	rec, ok := s.store.peek(offerID)
	if !ok {
		return appErrors.New(
			appErrors.ErrCodeShareNotFound,
			"share %s is not in this session",
			offerID,
		)
	}

	_ = rec.peer.Close()

	s.store.remove(offerID, true)

	s.logger.Info().Str("offer_id", offerID).Msg("p2p share cancelled")

	return nil
}

// GetShareSession returns the manager's view of one share.
func (s *P2PShareService) GetShareSession(
	_ context.Context,
	offerID string,
) (*domain.P2PShareSession, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	rec, ok := s.store.peek(offerID)
	if !ok {
		return nil, appErrors.New(
			appErrors.ErrCodeShareNotFound,
			"share %s is not in this session",
			offerID,
		)
	}

	session := rec.snapshot(s.now())

	return &session, nil
}

// ListShareSessions returns every share this manager is part of, newest first.
//
// The frontend uses it to rebuild its list after a reload: sessions are in
// memory, so a page refresh has to ask again rather than trust anything it had.
func (s *P2PShareService) ListShareSessions(_ context.Context) []domain.P2PShareSession {
	if s == nil {
		return nil
	}

	records := s.store.list()

	sessions := make([]domain.P2PShareSession, 0, len(records))
	for _, rec := range records {
		sessions = append(sessions, rec.snapshot(s.now()))
	}

	return sessions
}

// DefaultSTUNServers reports the ICE servers used when a share does not name its
// own, so the CLI can tell the user what their traffic touches.
//
// Worth surfacing because the question is the one everybody asks about
// peer-to-peer: a STUN server learns the client's public address and nothing
// else, and the mod bytes go directly between the two machines over DTLS.
func (s *P2PShareService) DefaultSTUNServers() []string {
	if s == nil {
		return nil
	}

	stun := s.config.Peer.STUNServers
	if len(stun) == 0 {
		return p2p.DefaultSTUNServers()
	}

	out := make([]string, len(stun))
	copy(out, stun)

	return out
}

// resolveOffer opens a link and applies every check that must happen before a
// recipient is shown anything or commits to connecting.
func (s *P2PShareService) resolveOffer(
	_ context.Context,
	linkText string,
) (*domain.P2PShareLink, error) {
	link, err := p2p.OpenOffer(linkText)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeShareInvalid, "open the offer link")
	}

	now := s.now()

	if link.IsExpired(now) {
		return nil, appErrors.Wrap(domain.ErrShareExpired, appErrors.ErrCodeShareExpired,
			"this share link expired at %s", link.Offer.ExpiresAt.Format(time.RFC3339))
	}

	// A share for a game this manager does not have is refused before a
	// connection is made, not after the archives have been received: installing a
	// Darktide mod into a Helldivers directory produces a broken install that is
	// very hard to spot afterwards.
	if _, ok := games.Get(link.Offer.GameID); !ok {
		return nil, appErrors.Wrap(
			domain.ErrGameNotRegistered,
			appErrors.ErrCodeGameNotRegistered,
			"share targets game %q, which this manager does not have", link.Offer.GameID,
		)
	}

	return link, nil
}

// checkLive refuses to act on a share whose window has closed.
func (s *P2PShareService) checkLive(rec *p2pShareRecord) error {
	if rec.offer.ExpiresAt == nil {
		return nil
	}

	if s.now().After(*rec.offer.ExpiresAt) {
		s.store.mark(rec.offerID, domain.P2PShareStateExpired, "the share link expired")

		return appErrors.Wrap(domain.ErrShareExpired, appErrors.ErrCodeShareExpired,
			"this share link expired at %s", rec.offer.ExpiresAt.Format(time.RFC3339))
	}

	return nil
}

// newRecipientRecord builds the session record for an accepted offer.
func (s *P2PShareService) newRecipientRecord(
	link *domain.P2PShareLink,
	peer *p2p.Peer,
	destDir string,
) *p2pShareRecord {
	now := s.now()

	rec := &p2pShareRecord{
		offerID:   link.Offer.OfferID,
		offer:     link.Offer,
		secret:    link.Secret,
		link:      link.Link,
		role:      p2pRoleRecipient,
		state:     domain.P2PShareStateConnecting,
		stageDir:  destDir,
		peer:      peer,
		modIDs:    make([]string, 0, len(link.Offer.Files)),
		createdAt: now,
		updatedAt: now,
		done:      make(chan struct{}),
	}

	for i := range link.Offer.Files {
		rec.totalSize += link.Offer.Files[i].Size
		rec.modIDs = append(rec.modIDs, link.Offer.Files[i].ModID)
	}

	if link.Offer.ExpiresAt != nil {
		deadline := *link.Offer.ExpiresAt
		rec.expiresAt = &deadline
	}

	rec.preview = buildP2PPreview(link)

	return rec
}

// finish records the outcome of a transfer.
func (s *P2PShareService) finish(offerID string, err error) {
	if err == nil {
		s.store.mark(offerID, domain.P2PShareStateCompleted, "")

		return
	}

	// A refusal is a decision, not a fault, and gets its own state so the UI can
	// point at the source links instead of showing a retry button.
	if errors.Is(err, p2p.ErrPeerRejected) {
		s.store.mark(offerID, domain.P2PShareStateRejected, err.Error())

		return
	}

	s.store.mark(offerID, domain.P2PShareStateFailed, err.Error())
}

// progressReporter returns the callback the protocol hands progress to.
func (s *P2PShareService) progressReporter(offerID string) p2p.ProgressFunc {
	return func(p domain.P2PTransferProgress) {
		transferred := p.BytesSent + p.BytesReceived

		s.store.advance(offerID, transferred, domain.P2PShareStateTransferring)
	}
}

// resolveGameID picks the game a share belongs to and refuses a mixed selection.
//
// A share is for exactly one game. A mixed selection would stamp one game on the
// offer and leave the other mod's files resolving against a manifest the
// recipient does not have, so it is refused here rather than silently resolved by
// taking the first mod's id.
func (s *P2PShareService) resolveGameID(mods []domain.Mod) (string, error) {
	gameID := ""

	for i := range mods {
		if mods[i].GameID == "" {
			continue
		}

		if gameID == "" {
			gameID = mods[i].GameID

			continue
		}

		if mods[i].GameID != gameID {
			return "", appErrors.New(
				appErrors.ErrCodeGameNotRegistered,
				"mod %q belongs to game %q, but this share is for game %q",
				mods[i].Name, mods[i].GameID, gameID,
			)
		}
	}

	if gameID != "" {
		return gameID, nil
	}

	if s.gameLocator != nil {
		def, _, err := s.gameLocator.Active()
		if err != nil {
			return "", err
		}

		return def.ID(), nil
	}

	return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "cannot resolve game id for share")
}

// p2pPacked is one mod, packed and described.
type p2pPacked struct {
	link   domain.ModSourceLink
	shared domain.SharedMod
	file   domain.P2PSharedFile
	mod    domain.Mod
}

// packP2PShare packs every mod and describes it for the offer, in one pass.
//
// The two are produced together for the same reason the cloud share does it that
// way: a mod that cannot be packed cannot be shared, and an offer that named it
// anyway would install nothing for the recipient.
func packP2PShare(ctx context.Context, staging string, mods []domain.Mod) ([]p2pPacked, error) {
	packed := make([]p2pPacked, 0, len(mods))

	for i := range mods {
		mod := mods[i]

		entry := domain.SharedMod{
			Name:         mod.Name,
			Version:      mod.Version,
			Author:       mod.Author,
			Description:  mod.Description,
			FileIDs:      []string{},
			Dependencies: append([]string(nil), mod.Dependencies...),
		}

		file, err := packP2PMod(ctx, staging, mod, collectModFiles(zerolog.Nop(), mod))
		if err != nil {
			return nil, err
		}

		entry.FileIDs = append(entry.FileIDs, file.Name)

		packed = append(packed, p2pPacked{
			mod:    mod,
			file:   file,
			shared: entry,
			link:   domain.P2PSourceLinkForMod(mod),
		})
	}

	return packed, nil
}

// packP2PMod writes one mod's files into a single archive and describes it.
//
// One archive per mod is the unit that makes a share verifiable: the recipient
// checks one hash per mod rather than one per file, and a single large write over
// a data channel is a better use of the connection than hundreds of small ones.
func packP2PMod(
	ctx context.Context,
	staging string,
	mod domain.Mod,
	collected []domain.ShareFile,
) (domain.P2PSharedFile, error) {
	if mod.LocalPath == "" {
		return domain.P2PSharedFile{}, appErrors.New(
			appErrors.ErrCodeShareInvalid,
			"mod %q has no files on disk to share", mod.Name,
		)
	}

	if len(collected) == 0 {
		return domain.P2PSharedFile{}, appErrors.New(
			appErrors.ErrCodeShareInvalid,
			"mod %q has no shareable files", mod.Name,
		)
	}

	entries := make([]storage.PackEntry, 0, len(collected))
	for _, file := range collected {
		entries = append(entries, storage.PackEntry{
			Source: filepath.Join(mod.LocalPath, filepath.FromSlash(file.Path)),
			Name:   file.Path,
		})
	}

	// Named after the mod id, which is unique within a game, so two mods that
	// both ship a shared.zip cannot overwrite each other in the staging folder or
	// on the far side.
	archiveName := domain.SanitizeSegment(mod.ID) + ".zip"
	archivePath := filepath.Join(staging, archiveName)

	if _, err := storage.PackArchive(ctx, archivePath, entries); err != nil {
		return domain.P2PSharedFile{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeShareInvalid,
			"pack mod %q", mod.Name,
		)
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		return domain.P2PSharedFile{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeShareInvalid,
			"measure the packed archive of mod %q", mod.Name,
		)
	}

	// The hash covers the archive as it will travel, not the files inside it. A
	// sum of the parts would accept a truncated archive as long as everything it
	// did contain was whole.
	hash, err := fileHash(archivePath)
	if err != nil {
		return domain.P2PSharedFile{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeShareInvalid,
			"hash the packed archive of mod %q", mod.Name,
		)
	}

	return domain.P2PSharedFile{
		FileID:     mod.ID,
		Name:       archiveName,
		ModID:      mod.ID,
		Size:       info.Size(),
		HashSHA256: hash,
	}, nil
}

// buildP2POffer assembles the offer from the packed mods.
func buildP2POffer(
	gameID string,
	packed []p2pPacked,
	opts domain.P2PCreateOptions,
	stunServers []string,
) domain.P2PShareOffer {
	offer := domain.P2PShareOffer{
		OfferID:     uuid.New().String(),
		GameID:      gameID,
		CreatedAt:   time.Now(),
		SenderName:  opts.SenderName,
		STUNServers: stunServers,
	}

	for i := range packed {
		offer.Mods = append(offer.Mods, packed[i].shared)
		offer.Files = append(offer.Files, packed[i].file)
		offer.SourceLinks = append(offer.SourceLinks, packed[i].link)
	}

	if opts.ExpiresAt != nil {
		deadline := *opts.ExpiresAt
		offer.ExpiresAt = &deadline
	}

	return offer
}

// buildP2PPreview renders an offer for the recipient.
func buildP2PPreview(link *domain.P2PShareLink) *domain.P2PSharePreview {
	preview := &domain.P2PSharePreview{
		OfferID:     link.Offer.OfferID,
		GameID:      link.Offer.GameID,
		SenderName:  link.Offer.SenderName,
		Mods:        link.Offer.Mods,
		Files:       link.Offer.Files,
		SourceLinks: link.Offer.SourceLinks,
		CreatedAt:   link.Offer.CreatedAt,
		Expired:     link.IsExpired(time.Now()),
	}

	for i := range preview.Files {
		preview.TotalSize += preview.Files[i].Size
	}

	if link.Offer.ExpiresAt != nil {
		deadline := *link.Offer.ExpiresAt
		preview.ExpiresAt = &deadline
	}

	return preview
}

// sharedModsByArchive indexes the offer's inventory by archive name.
//
// The archive name is the join key because it is the one value that appears on
// both sides and is unique within the offer by construction. Matching on mod name
// would break the moment a share carried two versions of the same mod.
func sharedModsByArchive(offer domain.P2PShareOffer) map[string]domain.SharedMod {
	byArchive := make(map[string]domain.SharedMod, len(offer.Mods))

	for i := range offer.Mods {
		for _, archive := range offer.Mods[i].FileIDs {
			byArchive[archive] = offer.Mods[i]
		}
	}

	return byArchive
}

// buildInstalledMod turns an inventory entry into the mod the installer writes.
//
// The upstream link is carried across rather than dropped: a mod that came from
// somebody's share is still a mod from Nexus or GitHub, and keeping the source
// and its update URL is what lets it be brought up to date later without another
// share. A mod with no upstream at all is recorded as coming from "p2p-share",
// which no source plugin claims, so nothing will try to update it automatically.
func buildInstalledMod(
	offer domain.P2PShareOffer,
	shared domain.SharedMod,
	file domain.P2PSharedFile,
) domain.Mod {
	link := sourceLinkFor(offer, file.ModID)

	sourceID := link.SourceID
	if sourceID == "" {
		sourceID = "p2p-share"
	}

	// The mod keeps the id the sender had.
	//
	// It is the same mod from the same upstream, and an id invented from the
	// display name would be both worse — display names carry the spacing and
	// capitalisation their author chose, while an id is used as a path segment
	// and as an argument on a command line — and useless: the user would not be
	// able to name the mod to `mod show`, `mod source show`, or a later share.
	// The fallback exists only for an offer that carried no id, which this build
	// never produces.
	modID := file.ModID
	if modID == "" {
		modID = domain.SanitizeSegment(shared.Name + "-" + shared.Version)
	}

	mod := domain.Mod{
		ID:          modID,
		Name:        shared.Name,
		Version:     shared.Version,
		Author:      shared.Author,
		Description: shared.Description,
		SourceID:    sourceID,
		SourceURL:   link.SourceURL,
		DownloadURL: link.DownloadURL,
		UpdateURL:   link.UpdateURL,
		GameID:      offer.GameID,
		Enabled:     true,
	}

	mod.Dependencies = append(mod.Dependencies, shared.Dependencies...)

	return mod
}

// sourceLinkFor finds a mod's upstream link in the offer.
func sourceLinkFor(offer domain.P2PShareOffer, modID string) domain.ModSourceLink {
	for i := range offer.SourceLinks {
		if offer.SourceLinks[i].ModID == modID {
			return offer.SourceLinks[i]
		}
	}

	return domain.ModSourceLink{ModID: modID}
}

// verifyStagedArchive re-checks a received archive against the offer.
func verifyStagedArchive(path string, file domain.P2PSharedFile) error {
	actual, err := fileHash(path)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeShareInvalid,
			"hash the received archive of %q", file.ModID)
	}

	if actual != file.HashSHA256 {
		return appErrors.New(
			appErrors.ErrCodeIntegrityMismatch,
			"archive %q hashed %s, the offer published %s", file.Name, actual, file.HashSHA256,
		)
	}

	return nil
}

// p2pShareRecord is the manager's live half of one share.
//
// It holds the peer connection and the staging directory, neither of which can
// be reconstructed from the link, which is why it exists at all: the link is the
// portable artefact, this is the volatile state behind it.
type p2pShareRecord struct {
	createdAt        time.Time
	updatedAt        time.Time
	rejectErr        error
	preview          *domain.P2PSharePreview
	peer             *p2p.Peer
	expiresAt        *time.Time
	done             chan struct{}
	role             string
	offerID          string
	state            domain.P2PShareState
	failReason       string
	secret           string
	link             string
	stageDir         string
	offer            domain.P2PShareOffer
	modIDs           []string
	totalSize        int64
	transferredBytes int64
	doneOnce         sync.Once
}

// signal releases every waiter on this record. Close calls it too, so a share
// torn down by a shutdown unblocks its waiters instead of stranding them.
func (r *p2pShareRecord) signal() {
	r.doneOnce.Do(func() {
		close(r.done)
	})
}

// snapshot renders the record for a UI or a CLI.
func (r *p2pShareRecord) snapshot(now time.Time) domain.P2PShareSession {
	modIDs := make([]string, len(r.modIDs))
	copy(modIDs, r.modIDs)

	session := domain.P2PShareSession{
		OfferID:          r.offerID,
		GameID:           r.offer.GameID,
		Role:             r.role,
		State:            r.state,
		ModIDs:           modIDs,
		TotalSize:        r.totalSize,
		TransferredBytes: r.transferredBytes,
		FailReason:       r.failReason,
		Link:             r.link,
		CreatedAt:        r.createdAt,
		UpdatedAt:        r.updatedAt,
	}

	// The preview is copied rather than shared so a caller cannot mutate the
	// stored one, and so the list of sessions is not holding pointers into records
	// that a running transfer is writing to.
	if r.preview != nil {
		preview := *r.preview
		session.Preview = &preview
	}

	if r.expiresAt != nil {
		deadline := *r.expiresAt
		session.ExpiresAt = &deadline

		if !now.Before(deadline) && !r.state.Terminal() {
			session.State = domain.P2PShareStateExpired
		}
	}

	return session
}

// p2pSessionStore is the set of shares this process is part of.
//
// A map behind one mutex rather than a database: a peer connection cannot survive
// a restart, so a persisted record would be a lie the moment the process came
// back. What is worth keeping is only what is true right now.
type p2pSessionStore struct {
	records map[string]*p2pShareRecord
	mu      sync.Mutex
}

func newP2PSessionStore() *p2pSessionStore {
	return &p2pSessionStore{records: make(map[string]*p2pShareRecord)}
}

// add stores a record, replacing any earlier one with the same offer id.
func (s *p2pSessionStore) add(rec *p2pShareRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records[rec.offerID] = rec
}

// load returns a record for reading. The record itself is shared, so callers must
// not mutate it; every field that changes during a transfer goes through the
// mutating helpers below.
func (s *p2pSessionStore) load(offerID string) (*p2pShareRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[offerID]
	if !ok {
		return nil, appErrors.New(
			appErrors.ErrCodeShareNotFound,
			"share %s is not in this session",
			offerID,
		)
	}

	return rec, nil
}

// peek returns a record if it exists.
func (s *p2pSessionStore) peek(offerID string) (*p2pShareRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[offerID]

	return rec, ok
}

// mark sets a terminal-or-not state and its explanation.
func (s *p2pSessionStore) mark(offerID string, state domain.P2PShareState, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[offerID]
	if !ok {
		return
	}

	rec.state = state
	rec.failReason = reason
	rec.updatedAt = time.Now()
}

// advance publishes transfer progress.
func (s *p2pSessionStore) advance(offerID string, transferred int64, state domain.P2PShareState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[offerID]
	if !ok {
		return
	}

	// Progress only ever moves forward. A late callback from a transfer that was
	// already marked failed would otherwise walk the bar backwards.
	if transferred > rec.transferredBytes {
		rec.transferredBytes = transferred
	}

	if !rec.state.Terminal() {
		rec.state = state
	}

	rec.updatedAt = time.Now()
}

// remove drops a record, optionally deleting its staging directory.
func (s *p2pSessionStore) remove(offerID string, removeFiles bool) {
	s.mu.Lock()

	rec, ok := s.records[offerID]
	if ok {
		delete(s.records, offerID)
	}

	s.mu.Unlock()

	if !ok || !removeFiles || rec.stageDir == "" {
		return
	}

	_ = os.RemoveAll(rec.stageDir)
}

// list returns every record.
func (s *p2pSessionStore) list() []*p2pShareRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*p2pShareRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec)
	}

	return out
}

// drain empties the store, handing each record to a caller that tears it down.
//
// The teardown runs outside the lock: closing a peer connection and removing a
// directory can both block, and holding the store lock through them would stall
// every progress callback in the process.
func (s *p2pSessionStore) drain(teardown func(rec *p2pShareRecord)) {
	s.mu.Lock()

	records := make([]*p2pShareRecord, 0, len(s.records))
	for _, rec := range s.records {
		records = append(records, rec)
	}

	s.records = make(map[string]*p2pShareRecord)

	s.mu.Unlock()

	for _, rec := range records {
		rec.signal()

		teardown(rec)
	}
}

// translateTransferError maps the protocol's errors onto the manager's codes.
//
// The sentinels survive the wrap so that a caller — and a test — can still ask
// whether a transfer failed an integrity check or was refused, which are two
// completely different things to tell a user.
func translateTransferError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, p2p.ErrIntegrity):
		return appErrors.Wrap(err, appErrors.ErrCodeIntegrityMismatch, "%s", err.Error())
	case errors.Is(err, p2p.ErrPeerRejected):
		return appErrors.Wrap(err, appErrors.ErrCodeShareRevoked, "%s", err.Error())
	case errors.Is(err, p2p.ErrBadMAC), errors.Is(err, p2p.ErrBadRole):
		return appErrors.Wrap(err, appErrors.ErrCodeAuthFailed, "%s", err.Error())
	case errors.Is(err, p2p.ErrPeerClosed), errors.Is(err, p2p.ErrUnexpectedFrame):
		return appErrors.Wrap(err, appErrors.ErrCodeDownloadFailed, "%s", err.Error())
	default:
		return appErrors.Wrap(err, appErrors.ErrCodeDownloadFailed, "%s", err.Error())
	}
}

// wrapTransport maps a connection failure onto the manager's codes.
func wrapTransport(err error, what string) error {
	if err == nil {
		return nil
	}

	return appErrors.Wrap(err, appErrors.ErrCodeDownloadFailed, "%s: %s", what, err.Error())
}

// expiryLabel renders an expiry for a log line, or a marker when there is none.
func expiryLabel(expiry *time.Time) string {
	if expiry == nil {
		return "never"
	}

	return expiry.Format(time.RFC3339)
}
