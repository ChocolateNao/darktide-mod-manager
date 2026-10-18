package p2p

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// DataChannelLabel is the only channel a share uses. One rather than one per
// file: SCTP streams already give ordering and reliability per stream, and a share
// is a sequential transfer, so a second channel is bookkeeping without throughput.
const DataChannelLabel = "dmm-share"

// bufferSize is how many incoming messages a peer queues before the reader drains
// them. Large enough that a fast sender is not throttled into a timeout while a
// receiver opens its staging file, small enough that a peer which sends without
// reading cannot make the process grow without bound.
const bufferSize = 64

// drainPollInterval is how often the flow-control loop re-checks a send buffer it
// is waiting on. OnBufferedAmountLow is the mechanism; this is the safety net for
// a peer that has stopped reading and will never produce the event. A quarter of
// a second neither busy-waits nor visibly stalls a resumed transfer.
const drainPollInterval = 250 * time.Millisecond

// ErrPeerClosed reports that the peer was closed while an operation was in
// flight. It is separate from context.Canceled because the two mean different
// things to a user: a cancelled transfer is one they stopped, a closed peer is
// one whose offer expired or whose answer never arrived.
var ErrPeerClosed = errors.New("p2p: peer connection is closed")

// Peer is one end of a WebRTC data channel.
//
// It owns the parts of pion the protocol needs and hides the parts it does not:
// SDP exchange, ICE gathering, flow control and connection state. Everything
// above it — handshake, transfer, verification — is written against a handful
// of methods and a pair of channels, which is what lets the protocol be tested
// without a network.
//
// Signalling (creating an offer, answering one) is done by the caller, one
// goroutine at a time. Once the channel is open, Send may be called from any
// goroutine; pion's DataChannel.Send is safe for that and the flow-control loop
// in sender.go depends on it.
type Peer struct {
	closeErr       error
	failures       chan error
	incoming       chan []byte
	conn           *webrtc.PeerConnection
	closed         chan struct{}
	ready          chan struct{}
	channel        *webrtc.DataChannel
	gatherSignal   <-chan struct{}
	connectTimeout time.Duration
	chunkSize      int
	gatherTimeout  time.Duration
	closeOnce      sync.Once
	gatherOnce     sync.Once
	readyOnce      sync.Once
	sendMu         sync.Mutex
}

// NewSenderPeer builds the offerer's peer and opens the data channel. The
// channel is created by the offerer because SCTP lets exactly one side announce
// it; the answerer learns about it from OnDataChannel. It is created before
// gathering so the SDP that goes into the link carries the application-mtu line
// the SCTP association needs.
func NewSenderPeer(cfg Config) (*Peer, error) {
	return newPeer(cfg, false)
}

// NewReceiverPeer builds the answerer's peer. It does not create a channel: it
// waits for the offerer's.
func NewReceiverPeer(cfg Config) (*Peer, error) {
	return newPeer(cfg, true)
}

// newPeer builds a peer and its channels.
func newPeer(cfg Config, answerer bool) (*Peer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	resolved := cfg.withDefaults()

	settings := webrtc.SettingEngine{}

	// Loopback candidates let two managers on one machine — the first thing anybody
	// tests a share with — connect without a STUN round trip.
	settings.SetIncludeLoopbackCandidate(resolved.IncludeLoopback)

	// mDNS advertises a candidate without telling the network the machine's address,
	// so a peer on the same LAN can resolve the name instead of needing an address.
	// It is switchable for networks that block multicast.
	if resolved.DisableMulticastDNS {
		settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	} else {
		settings.SetICEMulticastDNSMode(ice.MulticastDNSModeQueryAndGather)
	}

	// A bounded port range rather than the ephemeral one: ICE would otherwise take
	// whatever the OS hands it, and a user opening a firewall hole has no way to
	// know which port to open.
	settings.SetEphemeralUDPPortRange(resolved.PortMin, resolved.PortMax)

	settings.LoggerFactory = loggerFactoryFor(resolved.PionLogger)

	api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))

	conn, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers(resolved)})
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}

	peer := &Peer{
		conn:           conn,
		incoming:       make(chan []byte, bufferSize),
		failures:       make(chan error, 4),
		closed:         make(chan struct{}),
		ready:          make(chan struct{}),
		gatherTimeout:  resolved.GatherTimeout,
		connectTimeout: resolved.ConnectTimeout,
		chunkSize:      resolved.ChunkSize,
	}

	conn.OnConnectionStateChange(peer.onConnectionStateChange)

	if answerer {
		conn.OnDataChannel(func(channel *webrtc.DataChannel) {
			// Only the one channel a share uses is attached: pion announces any channel
			// the remote opens, and accepting them all would let a peer that got the
			// passphrase multiplex onto a session nobody asked for.
			if channel.Label() != DataChannelLabel {
				_ = channel.Close()

				return
			}

			peer.attach(channel)
		})
	} else {
		channel, err := conn.CreateDataChannel(DataChannelLabel, nil)
		if err != nil {
			_ = conn.Close()

			return nil, fmt.Errorf("create data channel: %w", err)
		}

		peer.attach(channel)
	}

	return peer, nil
}

// attach wires a data channel to the peer's message and state channels.
func (p *Peer) attach(channel *webrtc.DataChannel) {
	p.channel = channel

	channel.OnOpen(func() { p.markReady() })
	channel.OnClose(func() {
		p.fail(fmt.Errorf("%w: data channel closed", ErrPeerClosed))
	})
	channel.OnError(func(err error) { p.fail(fmt.Errorf("data channel: %w", err)) })
	channel.OnMessage(func(msg webrtc.DataChannelMessage) { p.deliver(msg.Data) })
}

// markReady latches the "channel is open" signal. Both peers wait on this rather
// than on their own callback: the answerer's channel does not exist until the
// offerer's SDP has been applied, so a peer that started waiting before its
// channel arrived would wait on nothing.
func (p *Peer) markReady() {
	p.readyOnce.Do(func() { close(p.ready) })
}

// onConnectionStateChange turns the terminal connection states into failures.
// A connection that reaches failed will not recover on its own, and waiting for
// the context deadline instead would leave a share looking as though it were
// still negotiating. Disconnected is deliberately not terminal: ICE keeps probing
// and the channel can come back, which matters for a share on a laptop that has
// just changed network.
func (p *Peer) onConnectionStateChange(state webrtc.PeerConnectionState) {
	switch state {
	case webrtc.PeerConnectionStateFailed:
		p.fail(fmt.Errorf("%w: peer connection failed", ErrPeerClosed))
	case webrtc.PeerConnectionStateClosed:
		p.fail(fmt.Errorf("%w: peer connection closed", ErrPeerClosed))
	case webrtc.PeerConnectionStateDisconnected:
	default:
	}
}

// deliver queues an incoming message, failing the session if the queue is full.
// Failing rather than dropping is deliberate: the protocol has no resynchronisation
// point inside a file, so a message that cannot be queued is one the receiver can
// never account for, and continuing would produce an archive that fails its digest
// check minutes later with no clue why.
func (p *Peer) deliver(data []byte) {
	select {
	case <-p.closed:
		return
	default:
	}

	select {
	case p.incoming <- data:
	case <-p.closed:
	default:
		p.fail(fmt.Errorf("%w: peer sent faster than the session could read", ErrPeerClosed))
	}
}

// fail reports a fatal condition without blocking.
func (p *Peer) fail(err error) {
	select {
	case p.failures <- err:
	case <-p.closed:
	default:
	}
}

// CreateOffer gathers candidates and returns the offer SDP. Gathering has to
// finish first: an offer sent with no candidates reaches the peer as a connection
// that cannot be made, and the only symptom the user sees is a share that times
// out. The wait is bounded, and the context lets a caller abandon a share it no
// longer wants.
func (p *Peer) CreateOffer(ctx context.Context) (string, error) {
	offer, err := p.conn.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("create offer: %w", err)
	}

	if err := p.conn.SetLocalDescription(offer); err != nil {
		return "", fmt.Errorf("set local offer: %w", err)
	}

	if err := p.gather(ctx); err != nil {
		return "", err
	}

	return p.localSDP()
}

// AcceptAnswer applies the answerer's SDP.
func (p *Peer) AcceptAnswer(sdpAnswer string) error {
	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdpAnswer}

	if err := p.conn.SetRemoteDescription(desc); err != nil {
		return fmt.Errorf("set remote answer: %w", err)
	}

	return nil
}

// CreateAnswer applies the offerer's SDP, gathers candidates and returns the
// answer SDP.
func (p *Peer) CreateAnswer(ctx context.Context, sdpOffer string) (string, error) {
	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdpOffer}

	if err := p.conn.SetRemoteDescription(desc); err != nil {
		return "", fmt.Errorf("set remote offer: %w", err)
	}

	answer, err := p.conn.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
	}

	if err := p.conn.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local answer: %w", err)
	}

	if err := p.gather(ctx); err != nil {
		return "", err
	}

	return p.localSDP()
}

// localSDP returns the description pion settled on after gathering.
func (p *Peer) localSDP() (string, error) {
	local := p.conn.LocalDescription()
	if local == nil {
		return "", fmt.Errorf(
			"%w: pion produced no local description after gathering",
			ErrPeerClosed,
		)
	}

	return local.SDP, nil
}

// gather waits for ICE to finish collecting candidates, once. The promise is
// created on first use and reused, so a peer whose caller gathers twice waits on
// the same closed channel the second time rather than hanging on a promise that
// will never fire again.
func (p *Peer) gather(ctx context.Context) error {
	p.gatherOnce.Do(func() {
		p.gatherSignal = webrtc.GatheringCompletePromise(p.conn)
	})

	timer := time.NewTimer(p.gatherTimeout)
	defer timer.Stop()

	select {
	case <-p.gatherSignal:
		return nil
	case <-timer.C:
		return fmt.Errorf("%w: ICE candidate gathering did not finish within %s",
			ErrPeerClosed, p.gatherTimeout)
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closed:
		return ErrPeerClosed
	}
}

// WaitOpen blocks until the data channel is usable. "Usable" means open, not
// merely negotiated: pion opens the channel after the DTLS handshake completes,
// and a message sent before that is dropped rather than queued, so a sender that
// skipped this wait would lose the first frame of every transfer.
func (p *Peer) WaitOpen(ctx context.Context) error {
	timer := time.NewTimer(p.connectTimeout)
	defer timer.Stop()

	select {
	case <-p.ready:
		return nil
	case err := <-p.failures:
		return err
	case <-timer.C:
		return fmt.Errorf("%w: no connection within %s", ErrPeerClosed, p.connectTimeout)
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closed:
		return ErrPeerClosed
	}
}

// Send writes one message, applying flow control first. The wait is why Send is a
// method rather than a call straight to the channel: SCTP's own window is finite,
// and a fast local disk produces chunks faster than a slow uplink drains them.
// Without this the reads pile up in memory until the process dies mid-transfer.
func (p *Peer) Send(ctx context.Context, data []byte, threshold int) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	if p.channel == nil {
		return ErrPeerClosed
	}

	if err := p.waitForDrain(ctx, threshold); err != nil {
		return err
	}

	select {
	case <-p.closed:
		return ErrPeerClosed
	default:
	}

	if err := p.channel.Send(data); err != nil {
		return fmt.Errorf("send frame: %w", err)
	}

	return nil
}

// waitForDrain blocks while the channel holds more than threshold unsent bytes.
func (p *Peer) waitForDrain(ctx context.Context, threshold int) error {
	if threshold <= 0 {
		threshold = defaultFlowControlThreshold
	}

	limit := uint64(
		threshold,
	) //nolint:gosec // bounded by Config.ChunkSize, which is validated non-negative.

	if p.channel.BufferedAmount() <= limit {
		return nil
	}

	drained := make(chan struct{}, 1)

	handler := func() {
		select {
		case drained <- struct{}{}:
		default:
		}
	}

	p.channel.SetBufferedAmountLowThreshold(limit)

	p.channel.OnBufferedAmountLow(handler)
	defer p.channel.OnBufferedAmountLow(func() {})

	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-drained:
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closed:
			return ErrPeerClosed
		}

		if p.channel.BufferedAmount() <= limit {
			return nil
		}
	}
}

// Flush waits until everything written has left the channel's send buffer, which
// is what makes a close safe. pion's DataChannel.Send hands bytes to the SCTP
// association and returns without waiting for them to reach the wire, so a caller
// that writes its last frame and immediately closes tears the association down
// while the peer is still holding those bytes, and they are discarded — which
// arrives at the other end as a truncated transfer rather than a closed connection.
//
// The wait is bounded by ctx and connectTimeout, so a peer that has stopped reading
// cannot hold a close open; the cost of hitting that bound is one truncated frame,
// which is what closing without flushing costs anyway.
func (p *Peer) Flush(ctx context.Context) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	if p.channel == nil {
		return ErrPeerClosed
	}

	if p.channel.BufferedAmount() == 0 {
		return nil
	}

	drained := make(chan struct{}, 1)

	handler := func() {
		select {
		case drained <- struct{}{}:
		default:
		}
	}

	p.channel.SetBufferedAmountLowThreshold(0)

	p.channel.OnBufferedAmountLow(handler)
	defer p.channel.OnBufferedAmountLow(func() {})

	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	timeout := time.NewTimer(p.connectTimeout)
	defer timeout.Stop()

	for {
		select {
		case <-drained:
		case <-ticker.C:
		case <-timeout.C:
			return fmt.Errorf("%w: %d bytes still unsent after %s",
				ErrPeerClosed, p.channel.BufferedAmount(), p.connectTimeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closed:
			return ErrPeerClosed
		}

		if p.channel.BufferedAmount() == 0 {
			return nil
		}
	}
}

// Received returns the channel incoming messages arrive on.
func (p *Peer) Received() <-chan []byte {
	return p.incoming
}

// Failures returns the channel a fatal error is reported on.
func (p *Peer) Failures() <-chan error {
	return p.failures
}

// State reports the connection state, for progress messages and tests.
func (p *Peer) State() webrtc.PeerConnectionState {
	return p.conn.ConnectionState()
}

// ChunkSize is the payload size the sender should use for this session. It is
// read back off the peer rather than passed alongside it because the peer is what
// applied the configuration: a caller that kept its own copy could disagree with
// the peer about a value both ends have to agree on.
func (p *Peer) ChunkSize() int {
	if p.chunkSize > 0 {
		return p.chunkSize
	}

	return DefaultChunkSize
}

// Close releases the connection. It is safe to call twice and from any
// goroutine, which matters because the transfer loop closes the peer on its
// error path and the caller closes it again on its own.
func (p *Peer) Close() error {
	if p == nil {
		return nil
	}

	p.closeOnce.Do(func() {
		close(p.closed)

		if p.channel != nil {
			_ = p.channel.Close()
		}

		if p.conn != nil {
			p.closeErr = p.conn.Close()
		}
	})

	return p.closeErr
}

// iceServers converts a configuration into pion's ICE server list. A TURN URL
// that also carries credentials is kept whole: pion parses the query parameters
// itself, and splitting the URL here to pass username and credential separately
// would mean re-implementing that parse badly.
func iceServers(cfg Config) []webrtc.ICEServer {
	servers := make([]webrtc.ICEServer, 0, len(cfg.STUNServers)+len(cfg.TURNServers))

	for _, url := range cfg.STUNServers {
		servers = append(servers, webrtc.ICEServer{URLs: []string{url}})
	}

	for _, url := range cfg.TURNServers {
		servers = append(servers, webrtc.ICEServer{URLs: []string{url}})
	}

	return servers
}
