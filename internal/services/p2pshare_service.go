// Package services: peer-to-peer mod sharing.
//
// The orchestrator here owns the parts neither the sealed links nor the live WebRTC
// connection can: which game a share belongs to, what the recipient is shown before
// deciding, and where archives are staged on both sides. The protocol itself —
// framing, handshake and hash verification — lives in internal/sharing/p2p.
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

// p2pRoleSender and p2pRoleRecipient are the two ends of a share.
const (
	p2pRoleSender    = "sender"
	p2pRoleRecipient = "recipient"
)

// staging patterns for the two sides of a transfer, distinct so an operator
// reading a filled temp directory can tell which end a folder belongs to.
const (
	senderStagingPattern    = "dmm-p2p-send-*"
	recipientStagingPattern = "dmm-p2p-recv-*"
)

// shareSourceID is the SourceID every share event carries. Shares are not sourced
// from a registry, so the field would otherwise be empty on every event and a
// subscriber filtering by source would see share activity as belonging to none.
const shareSourceID = "p2p"

// shareEventFor maps a session state onto the event that announces it. The mapping
// is total, so adding a state to domain.P2PShareState fails to compile here rather
// than producing an event nothing listens for. Non-terminal states with no
// dedicated type map onto share.progress: they are the phases a UI renders as
// "working, no bytes yet".
func shareEventFor(state domain.P2PShareState) EventType {
	switch state {
	case domain.P2PShareStateCreated:
		return EventShareCreated
	case domain.P2PShareStateOffered:
		return EventShareOffered
	case domain.P2PShareStateConnecting:
		return EventShareAccepted
	case domain.P2PShareStateTransferring, domain.P2PShareStateVerifying:
		return EventShareProgress
	case domain.P2PShareStateCompleted:
		return EventShareCompleted
	case domain.P2PShareStateRejected:
		return EventShareRejected
	case domain.P2PShareStateExpired:
		return EventShareExpired
	default:
		return EventShareFailed
	}
}

// P2PShareConfig is the transport configuration handed to every peer this service
// creates. It wraps p2p.Config, not copying its fields: the settings mean
// something only to the WebRTC layer, and duplicating them here would be a second
// place to forget a knob. Per-share overrides are applied to a copy.
type P2PShareConfig struct {
	// Peer is the template every peer starts from.
	Peer p2p.Config
	// DefaultExpiry applies to a share whose creator chose none. Zero means
	// "permanent": a link that silently stops working is worse than one to revoke.
	DefaultExpiry time.Duration
}

// DefaultP2PShareConfig returns the configuration used unless the caller supplies
// its own. The default ICE servers are Google's public STUN, enough for the
// symmetric-NAT case that motivated peer-to-peer sharing. No TURN: a relay would
// be the third party this feature exists to avoid, and it would see the bytes.
func DefaultP2PShareConfig() P2PShareConfig {
	return P2PShareConfig{Peer: p2p.Config{STUNServers: p2p.DefaultSTUNServers()}}
}

// P2PShareService orchestrates peer-to-peer shares. One instance lives for the
// lifetime of the process: a WebRTC peer connection cannot be reconstructed from a
// link, so every share in flight has to live in memory until it ends.
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
	// ModInstaller installs a verified archive. It is the same installer the cloud
	// share uses, so a mod that arrives over a data channel is unpacked, registered
	// and enabled by exactly the code path a mod from a provider goes through.
	ModInstaller ModInstaller
	// Logger receives share lifecycle events.
	Logger zerolog.Logger
	// Events publishes share state transitions. Nil disables publishing, which is
	// what the CLI and most tests want: only the share view needs to hear about a
	// transfer, and a caller with no UI has nothing to tell.
	Events EventSink
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
		store:        newP2PSessionStore(deps.Events),
		gameLocator:  deps.GameLocator,
		modInstaller: deps.ModInstaller,
		logger:       deps.Logger,
		config:       config,
		now:          clock,
	}
}

// Close abandons every share in flight and removes its staging. It is registered
// as a bundle cleanup, not left to the process exiting: a desktop app that
// restarts without exiting would leave every socket from the previous run open.
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
// The link is the whole share as far as the recipient is concerned: the SDP, the
// inventory, the hashes and the authorising secret, sealed so nothing in it renders
// as readable text in a chat preview.
//
// Everything expensive happens before the link exists, so a link handed to somebody
// is known to be backed by bytes that are on disk and reachable right now.
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

	// Checked here, not only when the recipient opens the link: a share of
	// mods for a game this build does not know cannot succeed there either.
	if _, known := games.Get(gameID); !known {
		return nil, appErrors.New(appErrors.ErrCodeGameNotRegistered,
			"game %q is not registered in this build, so its mods cannot be shared", gameID)
	}

	staging, err := os.MkdirTemp("", senderStagingPattern)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"create a staging folder to pack the mods in")
	}

	// Everything below either succeeds and hands the staging folder to a session or
	// fails and has to take the folder with it; one flag keeps that honest.
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

	// A configured default expiry applies only when the creator chose none, so a
	// policy in config never overrides an explicit choice.
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
		Str("op", "p2p-share").
		Str("offer_id", offer.OfferID).
		Str("game_id", gameID).
		Int("mod_count", len(mods)).
		Int("file_count", len(offer.Files)).
		Int64("total_bytes", total).
		Str("expires_at", expiryLabel(expiry)).
		Msg("p2p share created")

	return link, nil
}

// PreviewOffer opens a link and reports what it offers, without connecting. It is
// the recipient's only chance to judge the sender before any byte moves, so it
// answers the questions the UI actually renders: which game, which mods, how large,
// whether the link expired, and which upstream each mod can be fetched from instead.
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
		Str("op", "p2p-share").
		Str("offer_id", preview.OfferID).
		Str("game_id", preview.GameID).
		Int("mod_count", len(preview.Mods)).
		Int("file_count", len(preview.Files)).
		Msg("p2p offer previewed")

	return preview, nil
}

// AcceptOffer answers an offer and returns the answer link. The connection is
// established here but nothing is read until AwaitShare is called, which is what
// lets a frontend show the answer link, hand it back out of band, and only then
// block on progress. The session is recorded first, so a transfer that starts
// before the caller gets its answer back still has somewhere to publish to.
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
		Str("op", "p2p-share").
		Str("offer_id", link.Offer.OfferID).
		Str("game_id", link.Offer.GameID).
		Str("receiver", receiverName).
		Msg("p2p offer accepted")

	return answer, nil
}

// RejectOffer declines a share, hands back the answer link so the sender can open
// the connection, and delivers the refusal over it in the background.
//
// It returns once the answer is sealed, not blocking on delivery, because
// the caller has to be able to show the link. AwaitRejection waits for the outcome.
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

	// Detached from ctx on purpose: that call returns as soon as the link is ready,
	// and cancelling with it would abort the refusal the moment the dialog closed.
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
	// The session is dropped either way: there are no archives to install, and a
	// rejected share left in memory is one per link the user ever declined. The
	// outcome goes on the session, not the store, because signal() could
	// lose the race against remove() and read a missing record as success.
	if err != nil {
		session.rejectErr = appErrors.New(appErrors.ErrCodeShareRevoked,
			"the refusal did not reach the sender: %s", err)

		s.store.mark(session.offerID, domain.P2PShareStateFailed, session.rejectErr.Error())
		// Background goroutine: nothing receives this error unless the sender waits on it.
		s.logger.Error().
			Str("op", "p2p-share").
			Str("offer_id", session.offerID).
			Err(err).
			Msg("failed to deliver the p2p rejection")

		return
	}

	s.store.mark(session.offerID, domain.P2PShareStateRejected, reason)

	s.logger.Info().
		Str("op", "p2p-share").
		Str("offer_id", session.offerID).
		Str("reason", reason).
		Msg("p2p offer rejected")
}

// AwaitRejection blocks until a refusal this process sent has been delivered, or
// the context ends. It exists because the sender is the half that has to be
// running: the refusal travels on a channel the sender opens.
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
// answer and streams every archive. It blocks for the length of the transfer;
// progress is published to the session record, so a frontend shows a moving bar
// by polling GetShareSession, not by receiving per-byte events.
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

	// The answer names the offer it belongs to, so a recipient who pastes the wrong
	// answer into the wrong share is told instead of watching nothing open.
	if answer.OfferID != rec.offerID {
		return appErrors.New(appErrors.ErrCodeShareInvalid,
			"answer link is for offer %s, this share is %s", answer.OfferID, rec.offerID)
	}

	if err := rec.peer.AcceptAnswer(answer.SDP); err != nil {
		return wrapTransport(err, "apply the recipient's answer")
	}

	s.store.mark(offerID, domain.P2PShareStateConnecting, "")

	session := p2p.Session{Peer: rec.peer, Secret: rec.secret, Offer: rec.offer}
	start := s.now()
	transferErr := p2p.Send(ctx, session, rec.stageDir, s.progressReporter(offerID))

	s.finish(offerID, transferErr)

	if transferErr != nil {
		return translateTransferError(transferErr)
	}

	s.logger.Info().
		Str("op", "p2p-share").
		Str("offer_id", offerID).
		Int("file_count", len(rec.offer.Files)).
		Int64("total_bytes", rec.totalSize).
		Int64("duration_ms", s.now().Sub(start).Milliseconds()).
		Msg("p2p share sent")

	return nil
}

// AwaitShare receives a share the recipient accepted and blocks until the transfer
// ends. Every archive is verified against the offer's hashes before this returns,
// so a nil error means the staged bytes are the bytes the sender promised.
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
	start := s.now()
	transferErr := p2p.Receive(ctx, session, rec.stageDir, s.progressReporter(offerID))

	s.finish(offerID, transferErr)

	if transferErr != nil {
		return translateTransferError(transferErr)
	}

	s.logger.Info().
		Str("op", "p2p-share").
		Str("offer_id", offerID).
		Int("file_count", len(rec.offer.Files)).
		Int64("total_bytes", rec.totalSize).
		Int64("duration_ms", s.now().Sub(start).Milliseconds()).
		Msg("p2p share received")

	return nil
}

// InstallShare installs a received share's archives.
//
// The hashes are recomputed here, not trusted from the transfer: the
// archives have been sitting in a temporary directory since the transfer ended, and
// this is the last point at which a substituted file can still be refused.
//
// The upstream links travel with the offer, so a mod installed this way keeps the
// source it came from and can be updated later by following the link.
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
	start := s.now()

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
			s.logger.Warn().Str("op", "p2p-share").Str("mod", mod.Name).Err(err).Msg("install from p2p share failed")
			return nil, err
		}

		result.GameID = rec.offer.GameID
		installed = append(installed, *result)
	}

	// The archives are a full copy of every shared mod; leaving them would let every
	// share ever received accumulate.
	s.store.remove(offerID, true)

	s.logger.Info().
		Str("op", "p2p-share").
		Str("offer_id", offerID).
		Str("game_id", rec.offer.GameID).
		Int("installed_count", len(installed)).
		Int64("duration_ms", s.now().Sub(start).Milliseconds()).
		Msg("p2p share installed")

	return installed, nil
}

// DiscardShare drops a received share without installing it. It is also how a
// failed transfer is cleaned up: either way the staging folder holds nothing
// worth keeping.
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

	s.logger.Info().Str("op", "p2p-share").Str("offer_id", offerID).Msg("p2p share discarded")

	return nil
}

// CancelShare abandons a share this manager created as the sender. The recipient,
// if they connect, sees the connection drop, which is honest because there is
// nobody left to tell them.
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

	s.logger.Info().Str("op", "p2p-share").Str("offer_id", offerID).Msg("p2p share cancelled")

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
// Sessions live in memory, so a frontend has to ask again after a reload.
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
// own. Worth surfacing because a STUN server learns the client's public address
// and nothing else; the mod bytes go directly between the two machines over DTLS.
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

	// Refused before a connection is made, not after the archives arrive: installing
	// a mod into another game's directory is hard to spot afterwards.
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

// resolveGameID picks the game a share belongs to and refuses a mixed selection:
// the offer would name one game while the other mod's files resolve against a
// manifest the recipient does not have.
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

// packP2PShare packs every mod and describes it for the offer in one pass, for the
// same reason the cloud share does it that way: an offer naming a mod that could
// not be packed would install nothing for the recipient.
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

// packP2PMod writes one mod's files into a single archive and describes it. One
// archive per mod is the unit that makes a share verifiable: the recipient checks
// one hash per mod, and one large write beats hundreds of small ones.
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

	// Named after the mod id, unique within a game, so two mods that both ship a
	// shared.zip cannot overwrite each other in staging or on the far side.
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

	// The hash covers the archive as it will travel, not the files inside it: a sum
	// of the parts would accept a truncated archive whose contents were all whole.
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

// sharedModsByArchive indexes the offer's inventory by archive name, the join key
// because it is the one value on both sides and unique within the offer by
// construction. Matching on mod name would break on two versions of one mod.
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
// The upstream link is carried across, not dropped: a mod that came from
// somebody's share is still a mod from Nexus or GitHub, and keeping the source and
// its update URL is what lets it be updated later without another share. A mod
// with no upstream is recorded as "p2p-share", which no source plugin claims.
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

	// The mod keeps the sender's id: an id invented from the display name would be
	// unusable as a path segment and unnameable on the command line.
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

// p2pShareRecord is the manager's live half of one share. It holds the peer
// connection and the staging directory, neither of which can be reconstructed from
// the link: the link is the portable artefact, this the volatile state behind it.
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

	// The preview is copied so a caller cannot mutate the stored
	// one while a running transfer writes to it.
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
// A map behind one mutex, not a database: a peer connection cannot survive
// a restart, so a persisted record would be a lie the moment the process came back.
//
// It owns event publishing because it owns every state transition; routing that
// through the callers that mutate a record would mean eight call sites each
// remembering to announce themselves.
type p2pSessionStore struct {
	records map[string]*p2pShareRecord
	sink    EventSink
	mu      sync.Mutex
}

func newP2PSessionStore(sink EventSink) *p2pSessionStore {
	return &p2pSessionStore{records: make(map[string]*p2pShareRecord), sink: sink}
}

// publish announces a state transition. It runs without the store lock held: the
// bus delivers synchronously and a subscriber may call back in, so publishing
// under the lock would deadlock on the first UI that lists sessions.
func (s *p2pSessionStore) publish(rec *p2pShareRecord) {
	if s.sink == nil {
		return
	}

	s.sink.Publish(Event{
		Type:     shareEventFor(rec.state),
		Data:     rec.snapshot(time.Now()),
		ModID:    firstOrEmpty(rec.modIDs),
		SourceID: shareSourceID,
	})
}

// firstOrEmpty reports the first mod id, which is what the mod-scoped fields of
// Event carry. A share of several mods is not any one of them.
func firstOrEmpty(ids []string) string {
	if len(ids) == 0 {
		return ""
	}

	return ids[0]
}

// add stores a record, replacing any earlier one with the same offer id.
func (s *p2pSessionStore) add(rec *p2pShareRecord) {
	s.mu.Lock()
	s.records[rec.offerID] = rec
	s.mu.Unlock()

	s.publish(rec)
}

// load returns a record for reading. The record itself is shared, so callers must
// not mutate it; every field that changes goes through the mutating helpers below.
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

	rec, ok := s.records[offerID]
	if !ok {
		s.mu.Unlock()

		return
	}

	rec.state = state
	rec.failReason = reason
	rec.updatedAt = time.Now()
	s.mu.Unlock()

	s.publish(rec)
}

// advance publishes transfer progress.
func (s *p2pSessionStore) advance(offerID string, transferred int64, state domain.P2PShareState) {
	s.mu.Lock()

	rec, ok := s.records[offerID]
	if !ok {
		s.mu.Unlock()

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
	s.mu.Unlock()

	s.publish(rec)
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
// The teardown runs outside the lock: closing a peer connection and removing a
// directory can both block, and holding the lock would stall every progress callback.
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

// translateTransferError maps the protocol's errors onto the manager's codes. The
// sentinels survive the wrap so a caller can still ask whether a transfer failed an
// integrity check or was refused, which are very different things to tell a user.
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
