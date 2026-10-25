package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/p2p"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// p2pTestConfig keeps a whole transfer on one machine: the default would reach a
// STUN server, making the suite depend on the network and a third party's uptime.
func p2pTestConfig() *P2PShareConfig {
	return &P2PShareConfig{
		Peer: p2p.Config{
			IncludeLoopback:     true,
			DisableDefaultSTUN:  true,
			DisableMulticastDNS: true,
			GatherTimeout:       30 * time.Second,
			ConnectTimeout:      30 * time.Second,
			ChunkSize:           16 << 10,
		},
	}
}

// p2pServiceFor builds a service whose installer records, not installs.
func p2pServiceFor(t *testing.T) (*P2PShareService, *stubInstaller) {
	t.Helper()

	err := games.EnsureBuiltins()
	require.NoError(t, err, "register built-in games: %v", err)

	installer := &stubInstaller{}

	service := NewP2PShareService(P2PShareServiceDeps{
		GameLocator:  &stubLocator{id: games.DefaultID(), found: true},
		ModInstaller: installer,
		Logger:       zerolog.Nop(),
		Config:       p2pTestConfig(),
	})

	t.Cleanup(service.Close)

	return service, installer
}

// installedMod writes a folder of files and describes it with the mod that owns it.
func installedMod(t *testing.T, id string, files map[string]string) domain.Mod {
	t.Helper()

	dir := filepath.Join(t.TempDir(), id)

	for name, body := range files {
		path := filepath.Join(dir, name)

		err := os.MkdirAll(filepath.Dir(path), 0o750)
		require.NoError(t, err, "mkdir: %v", err)

		err = os.WriteFile(path, []byte(body), 0o600)
		require.NoError(t, err, "write %s: %v", name, err)
	}

	return domain.Mod{
		ID:          id,
		Name:        id,
		Version:     "1.0.0",
		GameID:      games.DefaultID(),
		SourceID:    "nexus",
		LocalPath:   dir,
		Enabled:     true,
		SourceURL:   "https://www.nexusmods.com/darktide/mods/" + id,
		UpdateURL:   "https://www.nexusmods.com/darktide/mods/" + id + "/updates",
		SourceData:  map[string]any{"source_id_on_mod": "42"},
		InstallMode: domain.InstallModeModsFolder,
	}
}

// TestP2PShareRoundTrip drives the whole conversation at once because the parts
// are meaningless apart: an unanswered offer proves nothing.
func TestP2PShareRoundTrip(t *testing.T) {
	sender, _ := p2pServiceFor(t)
	recipient, installer := p2pServiceFor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mod := installedMod(t, "cool-mod", map[string]string{
		"mod.toml":      "id = \"cool-mod\"\n",
		"scripts/a.lua": "print('hi')\n",
	})

	link, err := sender.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	require.NoError(t, err, "create: %v", err)

	require.NotEmpty(t, link.Secret, "link is incomplete: %+v", link)
	require.NotEmpty(t, link.Offer.OfferID, "link is incomplete: %+v", link)

	// The preview must carry enough to decide with: the size, and where to get
	// the mod otherwise.
	preview, err := recipient.PreviewOffer(ctx, link.Link)
	require.NoError(t, err, "preview: %v", err)

	require.Len(t, preview.Files, 1, "preview = %+v", preview)
	require.Len(t, preview.Mods, 1, "preview = %+v", preview)

	require.Greater(t, preview.TotalSize, int64(0), "preview reported %d bytes", preview.TotalSize)

	require.True(t, len(preview.SourceLinks) > 0 && preview.SourceLinks[0].HasURL(), "preview carried no source link: %+v", preview.SourceLinks)

	answer, err := recipient.AcceptOffer(ctx, link.Link, "friend")
	require.NoError(t, err, "accept: %v", err)

	require.Equal(t, link.Offer.OfferID, answer.OfferID, "answer is for %q, offer was %q", answer.OfferID, link.Offer.OfferID)

	// Both halves run at once: neither can finish before the other starts.
	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)

	go func() { sendErr <- sender.SendShare(ctx, link.Offer.OfferID, answer.Link) }()
	go func() { recvErr <- recipient.AwaitShare(ctx, answer.OfferID) }()

	err = <-sendErr
	require.NoError(t, err, "send: %v", err)

	err = <-recvErr
	require.NoError(t, err, "receive: %v", err)

	for _, who := range []*P2PShareService{sender, recipient} {
		session, err := who.GetShareSession(ctx, link.Offer.OfferID)
		require.NoError(t, err, "session: %v", err)

		require.Equal(t, domain.P2PShareStateCompleted, session.State, "session state = %q (%s)", session.State, session.FailReason)

		require.Equal(t, session.TotalSize, session.TransferredBytes, "transferred %d of %d bytes", session.TransferredBytes, session.TotalSize)
	}

	installed, err := recipient.InstallShare(ctx, answer.OfferID, domain.InstallOptions{})
	require.NoError(t, err, "install: %v", err)

	require.Len(t, installed, 1, "installed %d mods", len(installed))

	require.NotEmpty(t, installer.archives[0], "the installer was handed an empty archive path")

	require.Equal(t, mod.GameID, installed[0].GameID, "installed mod is for %q, not %q", installed[0].GameID, mod.GameID)

	// The installed mod keeps the upstream link, which is the whole fallback story.
	require.NotEmpty(t, installed[0].SourceURL, "installed mod lost its source: %+v", installed[0])
	require.NotEmpty(t, installed[0].SourceID, "installed mod lost its source: %+v", installed[0])

	_, err = recipient.GetShareSession(ctx, answer.OfferID)
	require.Error(t, err, "the session survived installation")
}

// TestP2PShareRejectReachesTheSender covers the deadlock case: the answer link has
// to come back before anything can be delivered, and nothing may block meanwhile.
func TestP2PShareRejectReachesTheSender(t *testing.T) {
	sender, _ := p2pServiceFor(t)
	recipient, installer := p2pServiceFor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mod := installedMod(t, "declined-mod", map[string]string{"mod.toml": "id = \"declined-mod\"\n"})

	link, err := sender.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	require.NoError(t, err, "create: %v", err)

	answer, err := recipient.RejectOffer(ctx, link.Link, p2p.ReasonUntrusted)
	require.NoError(t, err, "reject: %v", err)

	require.NotEmpty(t, answer.Link, "refusal produced no usable answer: %+v", answer)
	require.Equal(t, link.Offer.OfferID, answer.OfferID, "refusal produced no usable answer: %+v", answer)

	// The refusal goes out in the background; the sender has to answer first.
	sendErr := make(chan error, 1)

	go func() { sendErr <- sender.SendShare(ctx, link.Offer.OfferID, answer.Link) }()

	err = recipient.AwaitRejection(ctx, answer.OfferID)
	require.NoError(t, err, "the refusal did not reach the sender: %v", err)

	err = <-sendErr
	require.Error(t, err, "the sender reported a successful transfer after a refusal")

	require.ErrorIs(t, err, p2p.ErrPeerRejected, "sender error = %v, want the peer's refusal", err)

	session, err := sender.GetShareSession(ctx, link.Offer.OfferID)
	require.NoError(t, err, "session: %v", err)

	require.Equal(t, domain.P2PShareStateRejected, session.State, "state = %q, want rejected", session.State)

	// The sender has to be told what the recipient said, not merely that the
	// connection failed.
	require.Contains(t, session.FailReason, p2p.ReasonUntrusted, "the sender was told %q", session.FailReason)

	require.Empty(t, installer.archives, "a refused share installed something")
}

// TestP2PShareRejectWithoutASenderFails: an unanswered refusal cannot be delivered.
func TestP2PShareRejectWithoutASenderFails(t *testing.T) {
	recipient, _ := p2pServiceFor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mod := installedMod(t, "ignored-mod", map[string]string{"mod.toml": "id = \"x\"\n"})

	sender, _ := p2pServiceFor(t)

	offer, err := sender.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	require.NoError(t, err, "create: %v", err)

	// The sender never answers; closing it leaves its candidates leading nowhere.
	sender.Close()

	answer, err := recipient.RejectOffer(ctx, offer.Link, "")
	require.NoError(t, err, "reject: %v", err)

	short, cancelShort := context.WithTimeout(ctx, 20*time.Second)
	defer cancelShort()

	err = recipient.AwaitRejection(short, answer.OfferID)
	require.Error(t, err, "expected the undeliverable refusal to be reported")
}

func TestP2PShareRefusesAnExpiredLink(t *testing.T) {
	recipient, _ := p2pServiceFor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mod := installedMod(t, "stale-mod", map[string]string{"mod.toml": "id = \"x\"\n"})

	sender, _ := p2pServiceFor(t)

	deadline := time.Now().Add(-time.Minute)

	link, err := sender.CreateShare(
		ctx,
		[]domain.Mod{mod},
		domain.P2PCreateOptions{ExpiresAt: &deadline},
	)
	require.NoError(t, err, "create: %v", err)

	_, err = recipient.PreviewOffer(ctx, link.Link)
	require.True(t, appErrors.IsAppError(err, appErrors.ErrCodeShareExpired), "preview error = %v, want SHARE_EXPIRED", err)

	_, err = recipient.AcceptOffer(ctx, link.Link, "")
	require.True(t, appErrors.IsAppError(err, appErrors.ErrCodeShareExpired), "accept error = %v, want SHARE_EXPIRED", err)

	_, err = recipient.RejectOffer(ctx, link.Link, "")
	require.True(t, appErrors.IsAppError(err, appErrors.ErrCodeShareExpired), "reject error = %v, want SHARE_EXPIRED", err)
}

// TestP2PShareRefusesAGameItDoesNotKnow: the far side could not accept it either.
func TestP2PShareRefusesAGameItDoesNotKnow(t *testing.T) {
	ctx := context.Background()

	sender, _ := p2pServiceFor(t)

	mod := installedMod(t, "m", map[string]string{"mod.toml": "id = \"m\"\n"})
	mod.GameID = "not-a-game"

	_, err := sender.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	require.True(t, appErrors.IsAppError(err, appErrors.ErrCodeGameNotRegistered), "error = %v, want GAME_NOT_REGISTERED", err)
}

func TestP2PShareRefusesMixedGames(t *testing.T) {
	ctx := context.Background()

	sender, _ := p2pServiceFor(t)

	first := installedMod(t, "one", map[string]string{"mod.toml": "id = \"one\"\n"})
	second := first
	second.ID = "two"
	second.GameID = second.GameID + "-other"

	_, err := sender.CreateShare(
		ctx,
		[]domain.Mod{first, second},
		domain.P2PCreateOptions{},
	)
	require.Error(t, err, "a share spanning two games was accepted")
}

func TestP2PShareRefusesAModWithNothingInstalled(t *testing.T) {
	ctx := context.Background()

	sender, _ := p2pServiceFor(t)

	_, err := sender.CreateShare(ctx, nil, domain.P2PCreateOptions{})
	require.Error(t, err, "an empty selection was accepted")

	_, err = sender.CreateShare(ctx, []domain.Mod{{ID: "ghost", GameID: games.DefaultID()}},
		domain.P2PCreateOptions{})
	require.Error(t, err, "a mod with no local path was accepted")
}

func TestP2PShareInstallRequiresACompletedTransfer(t *testing.T) {
	recipient, _ := p2pServiceFor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mod := installedMod(t, "pending-mod", map[string]string{"mod.toml": "id = \"x\"\n"})

	sender, _ := p2pServiceFor(t)

	link, err := sender.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	require.NoError(t, err, "create: %v", err)

	_, err = recipient.AcceptOffer(ctx, link.Link, "")
	require.NoError(t, err, "accept: %v", err)

	_, err = recipient.InstallShare(
		ctx,
		link.Offer.OfferID,
		domain.InstallOptions{},
	)
	require.Error(t, err, "a share was installed before it was received")
}

func TestP2PShareSessionLookup(t *testing.T) {
	recipient, _ := p2pServiceFor(t)

	ctx := context.Background()

	_, err := recipient.GetShareSession(ctx, "nope")
	require.True(t, appErrors.IsAppError(err, appErrors.ErrCodeShareNotFound), "error = %v, want SHARE_NOT_FOUND", err)

	err = recipient.AwaitRejection(ctx, "nope")
	require.True(t, appErrors.IsAppError(err, appErrors.ErrCodeShareNotFound), "error = %v, want SHARE_NOT_FOUND", err)

	got := recipient.ListShareSessions(ctx)
	require.Empty(t, got, "a fresh service listed %d sessions", len(got))
}

// TestP2PShareCloseClearsStaging: a quit must not leave sockets bound or someone's
// mod sitting in a temporary directory.
func TestP2PShareCloseClearsStaging(t *testing.T) {
	err := games.EnsureBuiltins()
	require.NoError(t, err, "register built-in games: %v", err)

	service := NewP2PShareService(P2PShareServiceDeps{
		GameLocator:  &stubLocator{id: games.DefaultID(), found: true},
		ModInstaller: &stubInstaller{},
		Logger:       zerolog.Nop(),
		Config:       p2pTestConfig(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mod := installedMod(t, "abandoned-mod", map[string]string{"mod.toml": "id = \"x\"\n"})

	link, err := service.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	require.NoError(t, err, "create: %v", err)

	session, err := service.GetShareSession(ctx, link.Offer.OfferID)
	require.NoError(t, err, "session: %v", err)

	staging := stagingOf(t, service, link.Offer.OfferID)
	_, err = os.Stat(staging)
	require.NoError(t, err, "staging folder is not there: %v", err)

	_ = session

	service.Close()

	_, err = os.Stat(staging)
	require.True(t, os.IsNotExist(err), "staging folder survived the close: %v", err)

	got := service.ListShareSessions(ctx)
	require.Empty(t, got, "sessions survived the close: %d", len(got))
}

// stagingOf reaches into the store for the staging path a closed service deleted.
func stagingOf(t *testing.T, service *P2PShareService, offerID string) string {
	t.Helper()

	record, err := service.store.load(offerID)
	require.NoError(t, err, "load %s: %v", offerID, err)

	return record.stageDir
}
