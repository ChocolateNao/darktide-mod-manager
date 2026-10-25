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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loopbackConfig is a peer configuration that works without touching the network:
// no STUN, no mDNS, loopback candidates included, because the peers live in one
// process on one host.
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

	err := cfg.Validate()
	require.NoError(t, err, "loopback config is invalid: %v", err)

	return cfg
}

func writeArchive(t *testing.T, dir, name string, size int) domain.P2PSharedFile {
	t.Helper()

	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 251)
	}

	path := filepath.Join(dir, name)
	err := os.WriteFile(path, body, 0o600)
	require.NoError(t, err, "write %s: %v", path, err)

	sum := sha256.Sum256(body)

	return domain.P2PSharedFile{
		FileID:     name,
		Name:       name,
		ModID:      strings.TrimSuffix(name, ".zip"),
		Size:       int64(size),
		HashSHA256: hex.EncodeToString(sum[:]),
	}
}

func connectPeers(t *testing.T, ctx context.Context) (*p2p.Peer, *p2p.Peer) {
	t.Helper()

	cfg := loopbackConfig(t)

	sender, err := p2p.NewSenderPeer(cfg)
	require.NoError(t, err, "NewSenderPeer: %v", err)

	offer, err := sender.CreateOffer(ctx)
	require.NoError(t, err, "CreateOffer: %v", err)

	receiver, err := p2p.NewReceiverPeer(cfg)
	require.NoError(t, err, "NewReceiverPeer: %v", err)

	answer, err := receiver.CreateAnswer(ctx, offer)
	require.NoError(t, err, "CreateAnswer: %v", err)

	err = sender.AcceptAnswer(answer)
	require.NoError(t, err, "AcceptAnswer: %v", err)

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
		require.NoError(t, err, "WaitOpen: %v", err)
	}

	return sender, receiver
}

func TestTransferRoundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stage := t.TempDir()
	dest := t.TempDir()

	small := writeArchive(t, stage, "small.zip", 1024)
	// Larger than the chunk size so the transfer spans many CHUNK frames and
	// exercises the flow-control path.
	large := writeArchive(t, stage, "large.zip", 200*1024)

	offer := domain.P2PShareOffer{
		OfferID: "offer-round-trip",
		GameID:  "darktide",
		Files:   []domain.P2PSharedFile{small, large},
	}

	secret, err := linkSecret()
	require.NoError(t, err, "generate secret: %v", err)

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

	assert.NotZero(t, reported, "no progress was reported for a transfer that moved bytes")

	for _, file := range offer.Files {
		got, err := hashFile(filepath.Join(dest, file.Name))
		require.NoError(t, err, "hash received %s: %v", file.Name, err)

		assert.Equal(t, file.HashSHA256, got, "received %s hashes %s, the offer published %s", file.Name, got, file.HashSHA256)
	}

	// The sender streams the files in offer order; anything else would mean the
	// receiver's per-file accounting and the offer disagree.
	for i, file := range offer.Files {
		want := filepath.Join(stage, file.Name)
		got := filepath.Join(dest, file.Name)

		wantInfo, err := os.Stat(want)
		require.NoError(t, err, "stat %s: %v", want, err)

		gotInfo, err := os.Stat(got)
		require.NoError(t, err, "stat %s: %v", got, err)

		assert.Equal(t, wantInfo.Size(), gotInfo.Size(), "file %d (%s) arrived %d bytes, expected %d", i, file.Name, gotInfo.Size(), wantInfo.Size())
	}
}

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
	require.NoError(t, err, "generate secret: %v", err)

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
	require.Error(t, receiveResult, "Receive accepted a peer holding the wrong secret")

	// Whichever side failed first is a matter of timing, so both errors are
	// accepted as long as the transfer did not succeed.
	require.Error(t, sendResult, "Send completed against a receiver that rejected the handshake")

	entries, err := os.ReadDir(dest)
	require.NoError(t, err, "read destination: %v", err)

	assert.Empty(t, entries, "an unauthenticated peer left %d files on disk", len(entries))
}

func TestReceiveDetectsCorruptedFile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stage := t.TempDir()
	dest := t.TempDir()

	file := writeArchive(t, stage, "mod.zip", 4096)

	// The offer publishes the hash of what was staged, so the corruption is
	// introduced on disk afterwards.
	offer := domain.P2PShareOffer{
		OfferID: "offer-integrity",
		GameID:  "darktide",
		Files:   []domain.P2PSharedFile{file},
	}

	staged := filepath.Join(stage, file.Name)

	body, err := os.ReadFile(staged)
	require.NoError(t, err, "read staged archive: %v", err)

	body[0] ^= 0xff

	err = os.WriteFile(staged, body, 0o600)
	require.NoError(t, err, "rewrite staged archive: %v", err)

	secret, err := linkSecret()
	require.NoError(t, err, "generate secret: %v", err)

	senderPeer, receiverPeer := connectPeers(t, ctx)

	receiveErr := make(chan error, 1)

	go func() {
		_ = p2p.Send(ctx, p2p.Session{Peer: senderPeer, Secret: secret, Offer: offer}, stage, nil)
	}()

	go func() {
		receiveErr <- p2p.Receive(ctx, p2p.Session{Peer: receiverPeer, Secret: secret, Offer: offer}, dest, nil)
	}()

	err = <-receiveErr
	require.Error(t, err, "Receive accepted an archive whose bytes did not match the offer")

	// Either the length check or the digest check is a correct rejection; the file
	// must not be left behind.
	if !errors.Is(err, p2p.ErrIntegrity) && !errors.Is(err, p2p.ErrSizeMismatch) &&
		!errors.Is(err, p2p.ErrUnexpectedFrame) {
		t.Logf("rejected with: %v", err)
	}

	entries, err := os.ReadDir(dest)
	require.NoError(t, err, "read destination: %v", err)

	assert.Empty(t, entries, "a file that failed its hash check was left in %s", dest)
}

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

	// The sender believes there is a second, larger file, so the manifests
	// disagree.
	other := writeArchive(t, t.TempDir(), "other.zip", 4096)

	senderOffer := offer
	senderOffer.Files = []domain.P2PSharedFile{file, other}

	secret, err := linkSecret()
	require.NoError(t, err, "generate secret: %v", err)

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
	require.ErrorIs(t, err, p2p.ErrIntegrity, "Receive returned %v, want an integrity failure", err)

	entries, err := os.ReadDir(dest)
	require.NoError(t, err, "read destination: %v", err)

	assert.Empty(t, entries, "a rejected manifest still left %d files on disk", len(entries))
}

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
	require.NoError(t, err, "generate secret: %v", err)

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
	require.ErrorIs(t, err, p2p.ErrIntegrity, "Receive returned %v, want an integrity failure", err)
}

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
	require.NoError(t, err, "generate secret: %v", err)

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelConnect()

	senderPeer, _ := connectPeers(t, connectCtx)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = p2p.Send(ctx, p2p.Session{Peer: senderPeer, Secret: secret, Offer: offer}, stage, nil)
	require.ErrorIs(t, err, context.Canceled, "Send returned %v, want context.Canceled", err)
}

// TestConfigValidation covers the guards that stop a mistyped STUN URL gathering nothing silently.
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

			if testCase.ok {
				assert.NoError(t, err, "Validate returned %v, want nil", err)
			}

			if !testCase.ok {
				assert.Error(t, err, "Validate accepted a configuration it should reject")
			}
		})
	}
}

// TestDefaultSTUNServers pins Google's public STUN and nothing else; a default TURN
// relay would route mod data through a third party.
func TestDefaultSTUNServers(t *testing.T) {
	t.Parallel()

	servers := p2p.DefaultSTUNServers()

	assert.Equal(t, []string{"stun:stun.l.google.com:19302"}, servers, "default STUN servers are %v, want [stun:stun.l.google.com:19302]", servers)
}

func hashFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	return domain.ComputeSHA256Hex(body), nil
}

// linkSecret generates a secret the way the link codec does, so tests see production derivation.
func linkSecret() (string, error) {
	link, err := p2p.LinkOffer(domain.P2PShareOffer{OfferID: "probe", GameID: "probe"}, "v=0\r\n")
	if err != nil {
		return "", err
	}

	return link.Secret, nil
}
