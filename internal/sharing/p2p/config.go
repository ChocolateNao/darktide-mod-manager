package p2p

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pion/logging"
)

// DefaultSTUNServers is what a share falls back to when the caller names no
// STUN server.
//
// Google's public STUN is the default because it needs no account, no key and
// no configuration, which is what a first share has to be. It is safe to use
// for that: STUN carries nothing but the reflexive address a NAT hands back,
// and none of the mod data ever touches it — the data channel is DTLS
// protected from end to end. What it does leak is the fact that a peer was
// looking for reachable addresses at that moment, which is the trade every
// WebRTC application makes and the reason TURN and a self-hosted STUN both
// remain configurable.
func DefaultSTUNServers() []string {
	return []string{"stun:stun.l.google.com:19302"}
}

// DefaultPortRange is the UDP range ICE is allowed to allocate host candidates
// from. A fixed range rather than an ephemeral one because a share host usually
// also has a game or a launcher bound, and a predictable range is something a
// user can open in their firewall; sharing that control would otherwise be
// something they could only get by reading a log.
const (
	DefaultPortMin = 40000
	DefaultPortMax = 40999
)

// DefaultChunkSize is how much of an archive goes into one CHUNK frame.
//
// 256 KiB is what the reference implementation this protocol follows uses. It
// is large enough that per-frame overhead disappears next to the payload and
// small enough that the receiver's write buffer and the sender's send buffer
// both stay small — SCTP fragments anything larger, and a frame that spans
// many fragments makes a single loss cost more than it should.
const DefaultChunkSize = 256 << 10

// defaultFlowControlThreshold is the point at which the sender stops writing
// and waits for the channel to drain.
//
// It is two chunks: enough that the channel is never idle between frames, and
// small enough that the kernel socket buffer is not asked to hold a whole mod.
const defaultFlowControlThreshold = 2 * DefaultChunkSize

// Config describes how a peer connection is built.
//
// It is a value rather than a set of constructor options because the same
// configuration is needed twice per share — once for the offer, once for the
// answer — and threading eight arguments through both call sites is how they
// drift apart.
type Config struct {
	PionLogger          logging.LeveledLogger
	STUNServers         []string
	TURNServers         []string
	ChunkSize           int
	GatherTimeout       time.Duration
	ConnectTimeout      time.Duration
	PortMin             uint16
	PortMax             uint16
	IncludeLoopback     bool
	DisableDefaultSTUN  bool
	DisableMulticastDNS bool
}

// DefaultGatherTimeout bounds candidate gathering when Config leaves it unset.
const DefaultGatherTimeout = 2 * time.Minute

// DefaultConnectionTimeout bounds how long a session waits for the data channel
// to open after both descriptions are in place. ICE keeps trying in the
// background — a user who has a link pasted to them while on a VPN may need a
// while — so this is generous rather than tight, and it only fails the session
// rather than the process.
const DefaultConnectionTimeout = 3 * time.Minute

// withDefaults returns a copy of c with every unset field filled in.
//
// A copy rather than mutation: the same Config is often held by a share service
// for the lifetime of the process and handed to several peers, so filling in
// defaults in place would let one peer's connection attempt reshape the next
// one's settings.
func (c Config) withDefaults() Config {
	out := c

	if len(out.STUNServers) == 0 && !out.DisableDefaultSTUN {
		out.STUNServers = DefaultSTUNServers()
	}

	if out.PortMin == 0 {
		out.PortMin = DefaultPortMin
	}

	if out.PortMax == 0 {
		out.PortMax = DefaultPortMax
	}

	if out.PortMax < out.PortMin {
		out.PortMin, out.PortMax = out.PortMax, out.PortMin
	}

	if out.ChunkSize <= 0 {
		out.ChunkSize = DefaultChunkSize
	}

	if out.GatherTimeout <= 0 {
		out.GatherTimeout = DefaultGatherTimeout
	}

	if out.ConnectTimeout <= 0 {
		out.ConnectTimeout = DefaultConnectionTimeout
	}

	return out
}

// Validate reports the first problem with the configuration, if there is one.
//
// It is called once per share rather than per peer, so that a bad STUN URL is
// reported as "that STUN server is not a URL" before any link exists rather
// than as an opaque ICE failure ten seconds after the user clicked share.
func (c Config) Validate() error {
	for _, raw := range c.STUNServers {
		if err := validateICEURL(raw, "stun", "stuns"); err != nil {
			return err
		}
	}

	for _, raw := range c.TURNServers {
		if err := validateICEURL(raw, "turn", "turns"); err != nil {
			return err
		}
	}

	// An inverted port range is not rejected: withDefaults swaps the bounds
	// before the peer is built, so the outcome is well defined either way and a
	// user who typed the two the wrong way round should get a working share
	// rather than an error message about a setting they cannot see.

	if c.ChunkSize < 0 {
		return fmt.Errorf("%w: chunk size %d is negative", ErrInvalidConfig, c.ChunkSize)
	}

	if c.ChunkSize > maxChunkBytes {
		return fmt.Errorf(
			"%w: chunk size %d exceeds the %d-byte frame ceiling; SCTP fragments anything larger, so a bigger frame costs throughput rather than saving overhead",
			ErrInvalidConfig,
			c.ChunkSize,
			maxChunkBytes,
		)
	}

	return nil
}

// ErrInvalidConfig reports a configuration that cannot produce a working
// connection. It is a sentinel because every caller maps it onto the same
// error code: a share the user misconfigured is an argument problem, not a
// network problem, and the two should not read the same in a log.
var ErrInvalidConfig = errors.New("p2p: invalid configuration")

// validateICEURL checks one STUN or TURN URL.
//
// Two things make this more than a url.Parse. First, the scheme is checked by
// hand, because url.Parse is perfectly happy with "http://example.com" and ICE
// is not — pion would then fail much later with a message about a malformed
// server URL that names neither the setting the user changed nor the value they
// typed. Second, the host is read from Opaque rather than Host: ICE URLs are
// scheme-colon-authority with no "//", so "stun:stun.example.test:3478" parses
// with everything after the scheme in Opaque and an empty Host. Checking Host
// would reject every URL that actually works.
func validateICEURL(raw string, schemes ...string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%w: an ICE server URL is empty", ErrInvalidConfig)
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%w: %q is not a URL: %w", ErrInvalidConfig, raw, err)
	}

	if !slices.Contains(schemes, strings.ToLower(parsed.Scheme)) {
		return fmt.Errorf(
			"%w: %q must start with %s://",
			ErrInvalidConfig,
			raw,
			strings.Join(schemes, ":// or "),
		)
	}

	if iceHost(parsed) == "" {
		return fmt.Errorf("%w: %q has no host", ErrInvalidConfig, raw)
	}

	return nil
}

// iceHost extracts the authority from an ICE URL, whichever field url.Parse put
// it in.
//
// Both forms are accepted because a user who writes "stuns://stun.example.test"
// out of habit from https produces a perfectly valid ICE URL that lands in Host
// instead of Opaque, and rejecting it would be pedantry rather than safety.
func iceHost(parsed *url.URL) string {
	if parsed.Host != "" {
		return parsed.Host
	}

	// Opaque carries "host:port?transport=udp"; the query is the transport
	// selector and the credential, neither of which is part of the host.
	authority, _, _ := strings.Cut(parsed.Opaque, "?")

	return authority
}

// contextOrBackground guards against a nil context, which a Wails command can
// produce if a bound method is invoked with no context.
func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}
