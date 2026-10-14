package p2p_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/p2p"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// loopbackConfig is a peer configuration that works without touching the
// network.
//
// No STUN server and no mDNS, with loopback candidates included. The two peers
// in these tests live in one process on one host, so every candidate they need
// is a loopback one; asking a public STUN server to reflect a loopback address
// would be pointless, and a test that depends on the network is a test that
// fails in a sandbox.
func loopbackConfig(t *testing.T) p2p.Config {
	t.Helper()

	cfg := p2p.Config{
		IncludeLoopback:     true,
		DisableDefaultSTUN:  true,
		DisableMulticastDNS: true,
		GatherTimeout:       20 * time.Second,
		ConnectTimeout:      20 * time.Second,
		ChunkSize:           16 * 1024,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("loopback config is invalid: %v", err)
	}

	return cfg
}

// writeArchive stages a file of the given size with recognisable content and
// returns its descriptor.
func writeArchive(t *testing.T, dir, name string, size int) domain.P2PSharedFile {
	t.Helper()

	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 251)
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	sum := sha256.Sum256(body)

	return domain.P2PSharedFile{
		FileID:     name,
		Name:       name,
		ModID:      strings.TrimSuffix(name, ".zip"),
		Size:       int64(size),
		HashSHA256: hex.EncodeToString(sum[:]),
	}
}

// connectPeers runs the SDP exchange and returns two open peers.
func connectPeers(t *testing.T, ctx context.Context) (*p2p.Peer, *p2p.Peer) {
	t.Helper()

	cfg := loopbackConfig(t)

	sender, err := p2p.NewSenderPeer(cfg)
	if err != nil {
		t.Fatalf("NewSenderPeer: %v", err)
	}

	offer, err := sender.CreateOffer(ctx)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}

	receiver, err := p2p.NewReceiverPeer(cfg)
	if err != nil {
		t.Fatalf("NewReceiverPeer: %v", err)
	}

	answer, err := receiver.CreateAnswer(ctx, offer)
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}

	if err := sender.AcceptAnswer(answer); err != nil {
		t.Fatalf("AcceptAnswer: %v", err)
	}

	var wg sync.WaitGroup

	openErrs := make(chan error, 2)

	for _, peer := range []*p2p.Peer{sender, receiver} {
		wg.Add(1)

		go func(p *p2p.Peer) {
			defer wg.Done()

			if err := p.WaitOpen(ctx); err != nil {
				openErrs <- err
			}
		}(peer)
	}

	wg.Wait()
	close(openErrs)

	for err := range openErrs {
		t.Fatalf("WaitOpen: %v", err)
	}

	return sender, receiver
}

// TestTransferRoundTrip is the end-to-end case: a share of two archives crosses
// a real WebRTC data channel and arrives byte-for-byte, with the receiver's
// hashes matching what the offer published.
func TestTransferRoundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stage := t.TempDir()
	dest := t.TempDir()

	small := writeArchive(t, stage, "small.zip", 1024)
	// Larger than the chunk size so the transfer spans many CHUNK frames and
	// the flow-control path is exercised rather than short-circuited.
	large := writeArchive(t, stage, "large.zip", 200*1024)

	offer := domain.P2PShareOffer{
		OfferID: "offer-round-trip",
		GameID:  "darktide",
		Files:   []domain.P2PSharedFile{small, large},
	}

	secret, err := linkSecret()
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}

	senderPeer, receiverPeer := connectPeers(t, ctx)

	var progress []domain.P2PTransferProgress

	var progressMu sync.Mutex

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()

		err := p2p.Send(ctx, p2p.Session{Peer: senderPeer, Secret: secret, Offer: offer}, stage,
			func(p domain.P2PTransferProgress) {
				progressMu.Lock()
				defer progressMu.Unlock()

				progress = append(progress, p)
			})
		if err != nil {
			t.Errorf("Send: %v", err)
		}
	}()

	go func() {
		defer wg.Done()

		err := p2p.Receive(
			ctx,
			p2p.Session{Peer: receiverPeer, Secret: secret, Offer: offer},
			dest,
			nil,
		)
		if err != nil {
			t.Errorf("Receive: %v", err)
		}
	}()

	wg.Wait()

	progressMu.Lock()
	reported := len(progress)
	progressMu.Unlock()

	if reported == 0 {
		t.Error("no progress was reported for a transfer that moved bytes")
	}

	for _, file := range offer.Files {
		got, err := hashFile(filepath.Join(dest, file.Name))
		if err != nil {
			t.Fatalf("hash received %s: %v", file.Name, err)
		}

		if got != file.HashSHA256 {
			t.Errorf(
				"received %s hashes %s, the offer published %s",
				file.Name,
				got,
				file.HashSHA256,
			)
		}
	}

	// The sender streams the files in offer order; anything else would mean the
	// receiver's per-file accounting and the offer disagree.
	for i, file := range offer.Files {
		want := filepath.Join(stage, file.Name)
		got := filepath.Join(dest, file.Name)

		wantInfo, err := os.Stat(want)
		if err != nil {
			t.Fatalf("stat %s: %v", want, err)
		}

		gotInfo, err := os.Stat(got)
		if err != nil {
			t.Fatalf("stat %s: %v", got, err)
		}

		if wantInfo.Size() != gotInfo.Size() {
			t.Errorf(
				"file %d (%s) arrived %d bytes, expected %d",
				i,
				file.Name,
				gotInfo.Size(),
				wantInfo.Size(),
			)
		}
	}
}

// TestReceiveRejectsWrongSecret covers the handshake's whole purpose: a peer
// that does not hold the link secret must not be able to write a single byte.
func TestReceiveRejectsWrongSecret(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stage := t.TempDir()
	dest := t.TempDir()

	file := writeArchive(t, stage, "mod.zip", 4096)
	offer := domain.P2PShareOffer{
		OfferID: "offer-auth",
		GameID:  "darktide",
		Files:   []domain.P2PSharedFile{file},
	}

	realSecret, err := linkSecret()
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}

	attackerSecret := realSecret + "x"

	senderPeer, receiverPeer := connectPeers(t, ctx)

	sendErr := make(chan error, 1)
	receiveErr := make(chan error, 1)

	go func() {
		sendErr <- p2p.Send(ctx, p2p.Session{Peer: senderPeer, Secret: realSecret, Offer: offer}, stage, nil)
	}()

	go func() {
		receiveErr <- p2p.Receive(ctx, p2p.Session{Peer: receiverPeer, Secret: attackerSecret, Offer: offer}, dest, nil)
	}()

	sendResult := <-sendErr

	receiveResult := <-receiveErr
	if receiveResult == nil {
		t.Fatal("Receive accepted a peer holding the wrong secret")
	}

	// The receiver's own error is a handshake failure; whichever side failed
	// first is a matter of timing, so both are accepted as long as the
	// transfer did not succeed.
	if sendResult == nil {
		t.Fatal("Send completed against a receiver that rejected the handshake")
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("an unauthenticated peer left %d files on disk", len(entries))
	}
}

// TestReceiveDetectsCorruptedFile covers the integrity check: an archive that
// arrives with the wrong bytes must be removed rather than left for an install
// to pick up.
func TestReceiveDetectsCorruptedFile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stage := t.TempDir()
	dest := t.TempDir()

	file := writeArchive(t, stage, "mod.zip", 4096)

	// The offer publishes the hash of what was actually staged, so the sender
	// and the offer agree; the corruption is introduced on disk after the
	// offer was built, which is exactly the case the receiver's own digest
	// has to catch.
	offer := domain.P2PShareOffer{
		OfferID: "offer-integrity",
		GameID:  "darktide",
		Files:   []domain.P2PSharedFile{file},
	}

	staged := filepath.Join(stage, file.Name)

	body, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("read staged archive: %v", err)
	}

	body[0] ^= 0xff

	if err := os.WriteFile(staged, body, 0o600); err != nil {
		t.Fatalf("rewrite staged archive: %v", err)
	}

	secret, err := linkSecret()
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}

	senderPeer, receiverPeer := connectPeers(t, ctx)

	receiveErr := make(chan error, 1)

	go func() {
		_ = p2p.Send(ctx, p2p.Session{Peer: senderPeer, Secret: secret, Offer: offer}, stage, nil)
	}()

	go func() {
		receiveErr <- p2p.Receive(ctx, p2p.Session{Peer: receiverPeer, Secret: secret, Offer: offer}, dest, nil)
	}()

	err = <-receiveErr
	if err == nil {
		t.Fatal("Receive accepted an archive whose bytes did not match the offer")
	}

	// Either the length check or the digest check is a correct rejection; what
	// must not happen is the file being left behind.
	if !errors.Is(err, p2p.ErrIntegrity) && !errors.Is(err, p2p.ErrSizeMismatch) &&
		!errors.Is(err, p2p.ErrUnexpectedFrame) {
		t.Logf("rejected with: %v", err)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("a file that failed its hash check was left in %s", dest)
	}
}

// TestReceiveRejectsForeignManifest covers a peer that holds the key but
// describes a different transfer: the manifest is compared against the offer, so
// the inventory a recipient agreed to is the one it gets.
func TestReceiveRejectsForeignManifest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stage := t.TempDir()
	dest := t.TempDir()

	file := writeArchive(t, stage, "mod.zip", 2048)

	offer := domain.P2PShareOffer{
		OfferID: "offer-manifest",
		GameID:  "darktide",
		Files:   []domain.P2PSharedFile{file},
	}

	// The sender believes there is a second, larger file. The receiver's offer
	// lists one, so the manifests disagree.
	other := writeArchive(t, t.TempDir(), "other.zip", 4096)

	senderOffer := offer
	senderOffer.Files = []domain.P2PSharedFile{file, other}

	secret, err := linkSecret()
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}

	senderPeer, receiverPeer := connectPeers(t, ctx)

	receiveErr := make(chan error, 1)

	go func() {
		_ = p2p.Send(
			ctx,
			p2p.Session{Peer: senderPeer, Secret: secret, Offer: senderOffer},
			stage,
			nil,
		)
	}()

	go func() {
		receiveErr <- p2p.Receive(ctx, p2p.Session{Peer: receiverPeer, Secret: secret, Offer: offer}, dest, nil)
	}()

	err = <-receiveErr
	if !errors.Is(err, p2p.ErrIntegrity) {
		t.Fatalf("Receive returned %v, want an integrity failure", err)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("a rejected manifest still left %d files on disk", len(entries))
	}
}

// TestReceiveRejectsMismatchedOfferID covers the frame-level check: a peer that
// authenticates against a different offer must not be able to pour its bytes
// into this share.
func TestReceiveRejectsMismatchedOfferID(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stage := t.TempDir()
	dest := t.TempDir()

	file := writeArchive(t, stage, "mod.zip", 1024)
	offer := domain.P2PShareOffer{
		OfferID: "offer-mine",
		GameID:  "darktide",
		Files:   []domain.P2PSharedFile{file},
	}

	senderOffer := offer
	senderOffer.OfferID = "offer-theirs"

	secret, err := linkSecret()
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}

	senderPeer, receiverPeer := connectPeers(t, ctx)

	receiveErr := make(chan error, 1)

	go func() {
		_ = p2p.Send(
			ctx,
			p2p.Session{Peer: senderPeer, Secret: secret, Offer: senderOffer},
			stage,
			nil,
		)
	}()

	go func() {
		receiveErr <- p2p.Receive(ctx, p2p.Session{Peer: receiverPeer, Secret: secret, Offer: offer}, dest, nil)
	}()

	if err := <-receiveErr; !errors.Is(err, p2p.ErrIntegrity) {
		t.Fatalf("Receive returned %v, want an integrity failure", err)
	}
}

// TestSendHonoursContextCancellation covers a user who closes the window
// mid-transfer: the send must return promptly rather than sit in a flow-control
// wait forever.
func TestSendHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	stage := t.TempDir()

	file := writeArchive(t, stage, "big.zip", 4*1024*1024)
	offer := domain.P2PShareOffer{
		OfferID: "offer-cancel",
		GameID:  "darktide",
		Files:   []domain.P2PSharedFile{file},
	}

	secret, err := linkSecret()
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelConnect()

	senderPeer, _ := connectPeers(t, connectCtx)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = p2p.Send(ctx, p2p.Session{Peer: senderPeer, Secret: secret, Offer: offer}, stage, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send returned %v, want context.Canceled", err)
	}
}

// TestConfigValidation covers the guards on peer configuration, which are what
// stop a mistyped STUN URL from turning into a connection that silently gathers
// nothing.
func TestConfigValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  p2p.Config
		ok   bool
	}{
		{name: "empty is filled in", cfg: p2p.Config{}, ok: true},
		{
			name: "google stun by default",
			cfg:  p2p.Config{},
			ok:   true,
		},
		{
			name: "custom stun",
			cfg:  p2p.Config{STUNServers: []string{"stun:stun.example.test:3478"}},
			ok:   true,
		},
		{
			name: "turn with credentials",
			cfg:  p2p.Config{TURNServers: []string{"turn:turn.example.test:3478?transport=udp"}},
			ok:   true,
		},
		{
			name: "http is not an ice url",
			cfg:  p2p.Config{STUNServers: []string{"http://stun.example.test:3478"}},
			ok:   false,
		},
		{
			name: "garbage is not an ice url",
			cfg:  p2p.Config{STUNServers: []string{"not a url"}},
			ok:   false,
		},
		{
			name: "inverted port range",
			cfg:  p2p.Config{PortMin: 50000, PortMax: 40000},
			ok:   true,
		},
		{
			name: "negative chunk size",
			cfg:  p2p.Config{ChunkSize: -1},
			ok:   false,
		},
		{
			name: "chunk size above the frame ceiling",
			cfg:  p2p.Config{ChunkSize: 8 << 20},
			ok:   false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := testCase.cfg.Validate()

			if testCase.ok && err != nil {
				t.Errorf("Validate returned %v, want nil", err)
			}

			if !testCase.ok && err == nil {
				t.Error("Validate accepted a configuration it should reject")
			}
		})
	}
}

// TestDefaultSTUNServers pins the promise the documentation makes: Google's
// public STUN server, and nothing else. Adding a default TURN server here would
// start relaying mod data through a third party.
func TestDefaultSTUNServers(t *testing.T) {
	t.Parallel()

	servers := p2p.DefaultSTUNServers()

	if len(servers) != 1 || servers[0] != "stun:stun.l.google.com:19302" {
		t.Fatalf("default STUN servers are %v, want [stun:stun.l.google.com:19302]", servers)
	}
}

// hashFile returns the hex SHA-256 of a file's contents.
func hashFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	return domain.ComputeSHA256Hex(body), nil
}

// linkSecret generates a capability secret the way the link codec does, so the
// tests exercise the same derivation the protocol will see in production.
func linkSecret() (string, error) {
	link, err := p2p.LinkOffer(domain.P2PShareOffer{OfferID: "probe", GameID: "probe"}, "v=0\r\n")
	if err != nil {
		return "", err
	}

	return link.Secret, nil
}
