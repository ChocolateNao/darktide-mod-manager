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
// Google's public STUN needs no account, no key and no configuration, which is
// what a first share has to be, and it is safe here: STUN carries nothing but
// the reflexive address a NAT hands back, and the data channel is DTLS
// protected. What it does leak is that a peer was looking for reachable
// addresses, which is why TURN and a self-hosted STUN stay configurable.
func DefaultSTUNServers() []string {
	return []string{"stun:stun.l.google.com:19302"}
}

// DefaultPortRange is the UDP range ICE is allowed to allocate host candidates
// from.
const (
	DefaultPortMin = 40000
	DefaultPortMax = 40999
)

// DefaultChunkSize is how much of an archive goes into one CHUNK frame. It is
// large enough that per-frame overhead disappears next to the payload and small
// enough to stay inside SCTP's fragmenting, since a frame spanning many
// fragments makes a single loss cost more than it should.
const DefaultChunkSize = 256 << 10

// defaultFlowControlThreshold is the point at which the sender stops writing
// and waits for the channel to drain. Two chunks is enough that the channel is
// never idle between frames, and small enough that the kernel socket buffer is
// not asked to hold a whole mod.
const defaultFlowControlThreshold = 2 * DefaultChunkSize

// Config describes how a peer connection is built.
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
// to open after both descriptions are in place. Generous because ICE keeps
// trying in the background and a user who has a link pasted to them while on a
// VPN may need a while; failing here only fails the session, not the process.
const DefaultConnectionTimeout = 3 * time.Minute

// withDefaults returns a copy of c with every unset field filled in.
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

	// An inverted port range is not rejected: withDefaults swaps the bounds, so a
	// user who typed the two the wrong way round still gets a working share.

	if c.ChunkSize < 0 {
		return fmt.Errorf("%w: chunk size %d is negative", ErrInvalidConfig, c.ChunkSize)
	}

	if c.ChunkSize > maxChunkBytes {
		return fmt.Errorf(
			"%w: chunk size %d exceeds the %d-byte frame ceiling",
			ErrInvalidConfig,
			c.ChunkSize,
			maxChunkBytes,
		)
	}

	return nil
}

// ErrInvalidConfig reports a configuration that cannot produce a working
// connection. It is a sentinel because every caller maps it onto the same error
// code: a share the user misconfigured is an argument problem, not a network
// problem, and the two should not read the same in a log.
var ErrInvalidConfig = errors.New("p2p: invalid configuration")

// validateICEURL checks one STUN or TURN URL. Two things make this more than a
// url.Parse. The scheme is checked by hand, because url.Parse is happy with
// "http://example.com" and ICE is not, and pion would then fail much later with a
// message naming neither the setting the user changed nor the value they typed.
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
// it in. Both forms are accepted: a user who writes "stuns://stun.example.test"
// out of habit from https produces a perfectly valid ICE URL that lands in Host
// instead of Opaque.
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
