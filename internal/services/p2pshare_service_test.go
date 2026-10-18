package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/p2p"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
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

// p2pServiceFor builds a service whose installer records rather than installs.
func p2pServiceFor(t *testing.T) (*P2PShareService, *stubInstaller) {
	t.Helper()

	if err := games.EnsureBuiltins(); err != nil {
		t.Fatalf("register built-in games: %v", err)
	}

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

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
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
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if link.Secret == "" || link.Offer.OfferID == "" {
		t.Fatalf("link is incomplete: %+v", link)
	}

	// The preview must carry enough to decide with: the size, and where to get
	// the mod otherwise.
	preview, err := recipient.PreviewOffer(ctx, link.Link)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	if len(preview.Files) != 1 || len(preview.Mods) != 1 {
		t.Fatalf("preview = %+v", preview)
	}

	if preview.TotalSize <= 0 {
		t.Fatalf("preview reported %d bytes", preview.TotalSize)
	}

	if len(preview.SourceLinks) == 0 || !preview.SourceLinks[0].HasURL() {
		t.Fatalf("preview carried no source link: %+v", preview.SourceLinks)
	}

	answer, err := recipient.AcceptOffer(ctx, link.Link, "friend")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	if answer.OfferID != link.Offer.OfferID {
		t.Fatalf("answer is for %q, offer was %q", answer.OfferID, link.Offer.OfferID)
	}

	// Both halves run at once: neither can finish before the other starts.
	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)

	go func() { sendErr <- sender.SendShare(ctx, link.Offer.OfferID, answer.Link) }()
	go func() { recvErr <- recipient.AwaitShare(ctx, answer.OfferID) }()

	if err := <-sendErr; err != nil {
		t.Fatalf("send: %v", err)
	}

	if err := <-recvErr; err != nil {
		t.Fatalf("receive: %v", err)
	}

	for _, who := range []*P2PShareService{sender, recipient} {
		session, err := who.GetShareSession(ctx, link.Offer.OfferID)
		if err != nil {
			t.Fatalf("session: %v", err)
		}

		if session.State != domain.P2PShareStateCompleted {
			t.Fatalf("session state = %q (%s)", session.State, session.FailReason)
		}

		if session.TransferredBytes != session.TotalSize {
			t.Fatalf("transferred %d of %d bytes", session.TransferredBytes, session.TotalSize)
		}
	}

	installed, err := recipient.InstallShare(ctx, answer.OfferID, domain.InstallOptions{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	if len(installed) != 1 {
		t.Fatalf("installed %d mods", len(installed))
	}

	if installer.archives[0] == "" {
		t.Fatal("the installer was handed an empty archive path")
	}

	if installed[0].GameID != mod.GameID {
		t.Fatalf("installed mod is for %q, not %q", installed[0].GameID, mod.GameID)
	}

	// The installed mod keeps the upstream link, which is the whole fallback story.
	if installed[0].SourceURL == "" || installed[0].SourceID == "" {
		t.Fatalf("installed mod lost its source: %+v", installed[0])
	}

	if _, err := recipient.GetShareSession(ctx, answer.OfferID); err == nil {
		t.Fatal("the session survived installation")
	}
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
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	answer, err := recipient.RejectOffer(ctx, link.Link, p2p.ReasonUntrusted)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}

	if answer.Link == "" || answer.OfferID != link.Offer.OfferID {
		t.Fatalf("refusal produced no usable answer: %+v", answer)
	}

	// The refusal goes out in the background; the sender has to answer first.
	sendErr := make(chan error, 1)

	go func() { sendErr <- sender.SendShare(ctx, link.Offer.OfferID, answer.Link) }()

	if err := recipient.AwaitRejection(ctx, answer.OfferID); err != nil {
		t.Fatalf("the refusal did not reach the sender: %v", err)
	}

	err = <-sendErr
	if err == nil {
		t.Fatal("the sender reported a successful transfer after a refusal")
	}

	if !errors.Is(err, p2p.ErrPeerRejected) {
		t.Fatalf("sender error = %v, want the peer's refusal", err)
	}

	session, err := sender.GetShareSession(ctx, link.Offer.OfferID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	if session.State != domain.P2PShareStateRejected {
		t.Fatalf("state = %q, want rejected", session.State)
	}

	// The sender has to be told what the recipient said, not merely that the
	// connection failed.
	if !strings.Contains(session.FailReason, p2p.ReasonUntrusted) {
		t.Fatalf("the sender was told %q", session.FailReason)
	}

	if len(installer.archives) != 0 {
		t.Fatal("a refused share installed something")
	}
}

// TestP2PShareRejectWithoutASenderFails: an unanswered refusal cannot be delivered.
func TestP2PShareRejectWithoutASenderFails(t *testing.T) {
	recipient, _ := p2pServiceFor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mod := installedMod(t, "ignored-mod", map[string]string{"mod.toml": "id = \"x\"\n"})

	sender, _ := p2pServiceFor(t)

	offer, err := sender.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The sender never answers; closing it leaves its candidates leading nowhere.
	sender.Close()

	answer, err := recipient.RejectOffer(ctx, offer.Link, "")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}

	short, cancelShort := context.WithTimeout(ctx, 20*time.Second)
	defer cancelShort()

	if err := recipient.AwaitRejection(short, answer.OfferID); err == nil {
		t.Fatal("expected the undeliverable refusal to be reported")
	}
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
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := recipient.PreviewOffer(ctx, link.Link); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeShareExpired,
	) {
		t.Fatalf("preview error = %v, want SHARE_EXPIRED", err)
	}

	if _, err := recipient.AcceptOffer(ctx, link.Link, ""); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeShareExpired,
	) {
		t.Fatalf("accept error = %v, want SHARE_EXPIRED", err)
	}

	if _, err := recipient.RejectOffer(ctx, link.Link, ""); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeShareExpired,
	) {
		t.Fatalf("reject error = %v, want SHARE_EXPIRED", err)
	}
}

// TestP2PShareRefusesAGameItDoesNotKnow: the far side could not accept it either.
func TestP2PShareRefusesAGameItDoesNotKnow(t *testing.T) {
	ctx := context.Background()

	sender, _ := p2pServiceFor(t)

	mod := installedMod(t, "m", map[string]string{"mod.toml": "id = \"m\"\n"})
	mod.GameID = "not-a-game"

	_, err := sender.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	if !appErrors.IsAppError(err, appErrors.ErrCodeGameNotRegistered) {
		t.Fatalf("error = %v, want GAME_NOT_REGISTERED", err)
	}
}

func TestP2PShareRefusesMixedGames(t *testing.T) {
	ctx := context.Background()

	sender, _ := p2pServiceFor(t)

	first := installedMod(t, "one", map[string]string{"mod.toml": "id = \"one\"\n"})
	second := first
	second.ID = "two"
	second.GameID = second.GameID + "-other"

	if _, err := sender.CreateShare(
		ctx,
		[]domain.Mod{first, second},
		domain.P2PCreateOptions{},
	); err == nil {
		t.Fatal("a share spanning two games was accepted")
	}
}

func TestP2PShareRefusesAModWithNothingInstalled(t *testing.T) {
	ctx := context.Background()

	sender, _ := p2pServiceFor(t)

	if _, err := sender.CreateShare(ctx, nil, domain.P2PCreateOptions{}); err == nil {
		t.Fatal("an empty selection was accepted")
	}

	if _, err := sender.CreateShare(ctx, []domain.Mod{{ID: "ghost", GameID: games.DefaultID()}},
		domain.P2PCreateOptions{}); err == nil {
		t.Fatal("a mod with no local path was accepted")
	}
}

func TestP2PShareInstallRequiresACompletedTransfer(t *testing.T) {
	recipient, _ := p2pServiceFor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mod := installedMod(t, "pending-mod", map[string]string{"mod.toml": "id = \"x\"\n"})

	sender, _ := p2pServiceFor(t)

	link, err := sender.CreateShare(ctx, []domain.Mod{mod}, domain.P2PCreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := recipient.AcceptOffer(ctx, link.Link, ""); err != nil {
		t.Fatalf("accept: %v", err)
	}

	if _, err := recipient.InstallShare(
		ctx,
		link.Offer.OfferID,
		domain.InstallOptions{},
	); err == nil {
		t.Fatal("a share was installed before it was received")
	}
}

func TestP2PShareSessionLookup(t *testing.T) {
	recipient, _ := p2pServiceFor(t)

	ctx := context.Background()

	if _, err := recipient.GetShareSession(ctx, "nope"); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeShareNotFound,
	) {
		t.Fatalf("error = %v, want SHARE_NOT_FOUND", err)
	}

	if err := recipient.AwaitRejection(ctx, "nope"); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeShareNotFound,
	) {
		t.Fatalf("error = %v, want SHARE_NOT_FOUND", err)
	}

	if got := recipient.ListShareSessions(ctx); len(got) != 0 {
		t.Fatalf("a fresh service listed %d sessions", len(got))
	}
}

// TestP2PShareCloseClearsStaging: a quit must not leave sockets bound or someone's
// mod sitting in a temporary directory.
func TestP2PShareCloseClearsStaging(t *testing.T) {
	if err := games.EnsureBuiltins(); err != nil {
		t.Fatalf("register built-in games: %v", err)
	}

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
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	session, err := service.GetShareSession(ctx, link.Offer.OfferID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	staging := stagingOf(t, service, link.Offer.OfferID)
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging folder is not there: %v", err)
	}

	_ = session

	service.Close()

	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging folder survived the close: %v", err)
	}

	if got := service.ListShareSessions(ctx); len(got) != 0 {
		t.Fatalf("sessions survived the close: %d", len(got))
	}
}

// stagingOf reaches into the store for the staging path a closed service deleted.
func stagingOf(t *testing.T, service *P2PShareService, offerID string) string {
	t.Helper()

	record, err := service.store.load(offerID)
	if err != nil {
		t.Fatalf("load %s: %v", offerID, err)
	}

	return record.stageDir
}
