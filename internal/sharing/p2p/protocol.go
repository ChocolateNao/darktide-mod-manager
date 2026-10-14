// Package p2p implements the wire protocol behind a dmm:// share link.
//
// A share has no server behind it, so the two managers have to introduce
// themselves without a third party: the sender publishes a WebRTC offer inside
// an encrypted link, the recipient answers with an encrypted link of their own,
// and once ICE has picked a path between them the data channel carries the
// bytes. Everything else in the manager treats that as a file transfer that
// happens to be peer-to-peer.
//
// # The three phases
//
//  1. Link exchange. The offer link carries the offerer's SDP, the inventory of
//     mods and the passphrase. The answer link carries the answerer's SDP and
//     the same passphrase. Neither passes through a server, which is what makes
//     the transfer work on networks where a relay would be needed and none is
//     available.
//  2. Handshake. Before a single byte of mod data moves, each side proves it
//     holds the passphrase by sending a tagged MAC over a value the other side
//     chose. DTLS already authenticates the peer connection, but it
//     authenticates it to whoever answered the SDP — and the answer came
//     through a chat window, not a trusted channel. The handshake is what binds
//     the data channel to the link the user chose to open.
//  3. Transfer. The sender announces each archive with its size and SHA-256,
//     streams it in chunks, and repeats the digest at the end. The receiver
//     hashes what it wrote and compares before the archive is allowed anywhere
//     near the mods folder.
//
// # Framing
//
// Every data channel message is one frame: a type byte followed by big-endian
// fields. There is no length prefix on the frame itself, because the channel
// already preserves message boundaries; what a reader has to bounds-check is
// each variable-length field inside a frame. See SHARE_PROTOCOL.md for the
// byte-level specification.
package p2p

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/crypto"
)

// ProtocolVersion is the version both peers must agree on.
//
// It travels in the handshake rather than being implied by the link format, so
// that a manager which predates a framing change can refuse the session with a
// clear message instead of misreading the first frame it is sent.
const ProtocolVersion = 1

// Fixed field widths, in bytes.
const (
	nonceSize  = 16
	macSize    = 32
	sha256Size = 32
)

// maxFieldBytes bounds the manifest payload, the only field whose length prefix
// is 4 bytes wide.
//
// The largest legitimate value is a manifest for a large share, still tens of
// kilobytes. A megabyte is comfortably above that and far below the point
// where a single frame could exhaust memory, so a peer that announces a larger
// field is treated as hostile rather than accommodated. Fields with a 2-byte
// prefix — file ids, reasons, offer ids — are bounded by that prefix instead.
const maxFieldBytes = 1 << 20

// maxChunkBytes bounds one CHUNK payload.
//
// A share moves in 256 KiB slices, so this is roughly eight times what the
// sender produces. The headroom absorbs a sender running with a different
// chunk size without letting a peer use a "chunk" to allocate whatever it
// likes.
const maxChunkBytes = 4 << 20

// MAC label prefixes. Each authenticated value gets its own label so that a tag
// produced for one message can never be replayed as the tag for another.
const (
	labelHello    = "dmm/p2p/hello/v1"
	labelHelloAck = "dmm/p2p/hello-ack/v1"
)

// Sentinel errors. They are distinct values rather than formatted messages so
// that a caller can tell a rejected handshake from a truncated frame without
// matching text, and so that the error surfaced to a user names the problem
// rather than a byte offset.
var (
	// ErrFrameTruncated means a frame ended before its declared fields did.
	ErrFrameTruncated = errors.New("p2p: frame is shorter than its header claims")
	// ErrUnknownFrame means a frame type this version does not know arrived.
	// It is fatal because the stream is not seekable: there is no way to find
	// where the next frame starts.
	ErrUnknownFrame = errors.New("p2p: unknown frame type")
	// ErrBadMAC means a handshake tag did not verify, which is the only
	// evidence available that the other end does not hold the passphrase.
	ErrBadMAC = errors.New("p2p: handshake authentication failed")
	// ErrBadRole means the peer claimed a role this side cannot be.
	ErrBadRole = errors.New("p2p: peer claimed an unusable role")
)

// Role names which side of the transfer a peer is. It is carried in the
// handshake so that a peer which somehow reached the data channel without
// answering the right SDP is refused before it can claim to be the sender.
type Role byte

const (
	// RoleSender streams the archives.
	RoleSender Role = 0
	// RoleReceiver accepts them.
	RoleReceiver Role = 1
)

// String implements fmt.Stringer.
func (r Role) String() string {
	switch r {
	case RoleSender:
		return "sender"
	case RoleReceiver:
		return "receiver"
	default:
		return fmt.Sprintf("role(%d)", byte(r))
	}
}

// FrameType identifies the first byte of a frame.
type FrameType byte

// The frame types. They are listed in the order a session produces them, and
// the numeric values are fixed: they appear on the wire between two builds of
// the manager, so reordering them would silently reinterpret a session.
const (
	// FrameHello opens a session. Sender only.
	FrameHello FrameType = 0x01
	// FrameHelloAck answers it and proves the answerer holds the passphrase.
	FrameHelloAck FrameType = 0x02
	// FrameManifest restates what is about to be transferred, so both ends
	// agree on the inventory rather than each trusting its own copy.
	FrameManifest FrameType = 0x03
	// FrameFileStart announces one archive. Sender only.
	FrameFileStart FrameType = 0x04
	// FrameChunk carries a slice of an archive. Sender only.
	FrameChunk FrameType = 0x05
	// FrameFileDone closes one archive and repeats its digest. Sender only.
	FrameFileDone FrameType = 0x06
	// FrameComplete ends a successful session. Sender only.
	FrameComplete FrameType = 0x07
	// FrameAbort ends the session from either side.
	FrameAbort FrameType = 0x08
	// FrameError reports a problem without ending the session, for a file the
	// sender could not open and wants to skip.
	FrameError FrameType = 0x09
	// FrameTransferComplete is the receiver's answer to FrameComplete: it says
	// every archive arrived and every digest matched. Receiver only.
	//
	// It exists because the sender cannot simply hang up afterwards. A sender
	// writes the last chunk and then closes its half of the connection, and a
	// close discards whatever the other side has not read out of its socket
	// buffer — so a transfer that genuinely succeeded would be reported as a
	// truncated one on exactly the shares that move the most bytes. Waiting for
	// this frame makes the sender's teardown mean "the receiver is done", which
	// is the only condition under which a teardown is safe.
	FrameTransferComplete FrameType = 0x0A
)

// String implements fmt.Stringer. It exists mostly for log lines and test
// failures, where a bare 0x05 tells nobody anything.
func (t FrameType) String() string {
	switch t {
	case FrameHello:
		return "HELLO"
	case FrameHelloAck:
		return "HELLO_ACK"
	case FrameManifest:
		return "MANIFEST"
	case FrameFileStart:
		return "FILE_START"
	case FrameChunk:
		return "CHUNK"
	case FrameFileDone:
		return "FILE_DONE"
	case FrameComplete:
		return "COMPLETE"
	case FrameAbort:
		return "ABORT"
	case FrameError:
		return "ERROR"
	case FrameTransferComplete:
		return "TRANSFER_COMPLETE"
	default:
		return fmt.Sprintf("FRAME(0x%02x)", byte(t))
	}
}

// HelloFrame proves the sender holds the session key.
//
// It carries the offer id rather than relying on the channel being the one that
// was negotiated: the offer id is what the user pasted, so binding the
// handshake to it means a data channel reached by any other route cannot be
// mistaken for this share.
type HelloFrame struct {
	OfferID string
	Nonce   []byte
	MAC     []byte
	Role    Role
	Version uint8
}

// Encode renders the frame.
func (h *HelloFrame) Encode() ([]byte, error) {
	if len(h.Nonce) != nonceSize {
		return nil, fmt.Errorf("p2p: hello nonce is %d bytes, want %d", len(h.Nonce), nonceSize)
	}

	if len(h.MAC) != macSize {
		return nil, fmt.Errorf("p2p: hello mac is %d bytes, want %d", len(h.MAC), macSize)
	}

	if len(h.OfferID) > 0xff {
		return nil, fmt.Errorf("p2p: offer id is %d bytes, too long for a frame", len(h.OfferID))
	}

	w := &writer{}
	w.byte(byte(FrameHello))
	w.byte(h.Version)
	w.byte(byte(h.Role))
	w.short([]byte(h.OfferID))
	w.raw(h.Nonce)
	w.raw(h.MAC)

	return w.bytes(), nil
}

// DecodeHello parses a HELLO frame and checks its shape.
func DecodeHello(payload []byte) (*HelloFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != FrameHello {
		return nil, ErrUnknownFrame
	}

	version, err := r.byte()
	if err != nil {
		return nil, err
	}

	role, err := r.byte()
	if err != nil {
		return nil, err
	}

	offerID, err := r.short()
	if err != nil {
		return nil, err
	}

	nonce, err := r.fixed(nonceSize)
	if err != nil {
		return nil, err
	}

	mac, err := r.fixed(macSize)
	if err != nil {
		return nil, err
	}

	if err := r.done(); err != nil {
		return nil, err
	}

	frame := &HelloFrame{
		Version: version,
		Role:    Role(role),
		OfferID: string(offerID),
		Nonce:   nonce,
		MAC:     mac,
	}

	// The role is checked here rather than being left to the switch below: a
	// peer that claims a role this build does not define is not a peer worth
	// interpreting further.
	if frame.Role != RoleSender && frame.Role != RoleReceiver {
		return nil, ErrBadRole
	}

	return frame, nil
}

// tag recomputes the value the MAC must cover.
func (h *HelloFrame) tag(key []byte) []byte {
	return crypto.MAC(key,
		[]byte(labelHello),
		[]byte{h.Version},
		[]byte{byte(h.Role)},
		[]byte(h.OfferID),
		h.Nonce,
	)
}

// Verify reports whether the frame was produced by a holder of the session key.
func (h *HelloFrame) Verify(key []byte) error {
	if subtle.ConstantTimeCompare(h.MAC, h.tag(key)) != 1 {
		return ErrBadMAC
	}

	return nil
}

// BuildHello produces a HELLO frame with a fresh nonce.
//
// The nonce is what makes the tag worth having. Without it a tag captured from
// one session could be replayed into another, because every other input is the
// same for both; with it, a replayed frame fails against a key derivation that
// is not the one it was made for, and fails the version check when the peer
// sends a different one.
func BuildHello(key []byte, role Role, offerID string) (*HelloFrame, error) {
	nonce := make([]byte, nonceSize)

	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	frame := &HelloFrame{
		Version: ProtocolVersion,
		Role:    role,
		OfferID: offerID,
		Nonce:   nonce,
	}

	frame.MAC = frame.tag(key)

	return frame, nil
}

// HelloAckFrame proves the receiver holds the session key.
//
// It echoes the sender's nonce and adds one of its own. Echoing alone would
// authenticate the sender but leave the receiver anonymous — anyone who could
// observe the sender's frame could answer it — so the second nonce makes the
// acknowledgement a response rather than a replay.
type HelloAckFrame struct {
	OfferID     string
	SenderNonce []byte
	PeerNonce   []byte
	MAC         []byte
}

// Encode renders the frame.
func (a *HelloAckFrame) Encode() ([]byte, error) {
	if len(a.SenderNonce) != nonceSize {
		return nil, fmt.Errorf("p2p: hello ack sender nonce is %d bytes, want %d",
			len(a.SenderNonce), nonceSize)
	}

	if len(a.PeerNonce) != nonceSize {
		return nil, fmt.Errorf("p2p: hello ack peer nonce is %d bytes, want %d",
			len(a.PeerNonce), nonceSize)
	}

	if len(a.MAC) != macSize {
		return nil, fmt.Errorf("p2p: hello ack mac is %d bytes, want %d", len(a.MAC), macSize)
	}

	if len(a.OfferID) > 0xff {
		return nil, fmt.Errorf("p2p: offer id is %d bytes, too long for a frame", len(a.OfferID))
	}

	w := &writer{}
	w.byte(byte(FrameHelloAck))
	w.short([]byte(a.OfferID))
	w.raw(a.SenderNonce)
	w.raw(a.PeerNonce)
	w.raw(a.MAC)

	return w.bytes(), nil
}

// DecodeHelloAck parses a HELLO_ACK frame.
func DecodeHelloAck(payload []byte) (*HelloAckFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != FrameHelloAck {
		return nil, ErrUnknownFrame
	}

	offerID, err := r.short()
	if err != nil {
		return nil, err
	}

	senderNonce, err := r.fixed(nonceSize)
	if err != nil {
		return nil, err
	}

	peerNonce, err := r.fixed(nonceSize)
	if err != nil {
		return nil, err
	}

	mac, err := r.fixed(macSize)
	if err != nil {
		return nil, err
	}

	if err := r.done(); err != nil {
		return nil, err
	}

	return &HelloAckFrame{
		OfferID:     string(offerID),
		SenderNonce: senderNonce,
		PeerNonce:   peerNonce,
		MAC:         mac,
	}, nil
}

// tag recomputes the value the MAC must cover.
func (a *HelloAckFrame) tag(key []byte) []byte {
	return crypto.MAC(key,
		[]byte(labelHelloAck),
		[]byte(a.OfferID),
		a.SenderNonce,
		a.PeerNonce,
	)
}

// Verify reports whether the frame was produced by a holder of the session key.
//
// senderNonce is the nonce from the HELLO this answers. Passing it in rather
// than reading it from the frame is what makes the check a response: a
// captured HELLO_ACK replayed into a later session carries the old nonce and
// fails here.
func (a *HelloAckFrame) Verify(key []byte, offerID string, senderNonce []byte) error {
	if a.OfferID != offerID {
		return fmt.Errorf(
			"%w: ack is for offer %q, this session is %q",
			ErrBadMAC,
			a.OfferID,
			offerID,
		)
	}

	if subtle.ConstantTimeCompare(a.SenderNonce, senderNonce) != 1 {
		return fmt.Errorf("%w: ack does not echo this session's challenge", ErrBadMAC)
	}

	if subtle.ConstantTimeCompare(a.MAC, a.tag(key)) != 1 {
		return ErrBadMAC
	}

	return nil
}

// BuildHelloAck answers a HELLO with a MAC over both nonces.
func BuildHelloAck(key []byte, offerID string, senderNonce []byte) (*HelloAckFrame, error) {
	peerNonce := make([]byte, nonceSize)

	if _, err := rand.Read(peerNonce); err != nil {
		return nil, err
	}

	frame := &HelloAckFrame{
		OfferID:     offerID,
		SenderNonce: senderNonce,
		PeerNonce:   peerNonce,
	}

	frame.MAC = frame.tag(key)

	return frame, nil
}

// ManifestFrame restates the inventory for the session.
//
// Both ends already hold it: the sender built it, the receiver read it out of
// the link. Sending it again is what turns "two managers happen to be talking"
// into "these two managers are talking about the same share" — a data channel
// reached some other way would have to reproduce the offer id and every
// archive's digest to get past this.
type ManifestFrame struct {
	OfferID string
	Payload []byte
}

// Encode renders the frame. The payload is length-prefixed because it is the
// only field of variable, unbounded size in the protocol.
func (m *ManifestFrame) Encode() ([]byte, error) {
	if len(m.OfferID) > 0xff {
		return nil, fmt.Errorf("p2p: offer id is %d bytes, too long for a frame", len(m.OfferID))
	}

	w := &writer{}
	w.byte(byte(FrameManifest))
	w.short([]byte(m.OfferID))
	w.field(m.Payload)

	return w.bytes(), nil
}

// DecodeManifest parses a MANIFEST frame.
func DecodeManifest(payload []byte) (*ManifestFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != FrameManifest {
		return nil, ErrUnknownFrame
	}

	offerID, err := r.short()
	if err != nil {
		return nil, err
	}

	body, err := r.field()
	if err != nil {
		return nil, err
	}

	if err := r.done(); err != nil {
		return nil, err
	}

	return &ManifestFrame{OfferID: string(offerID), Payload: body}, nil
}

// putSize and takeSize move a byte count across the wire.
//
// The wire carries sizes as unsigned 64-bit values because that is what the
// framing needs; the filesystem and Go's own APIs want a signed count. A peer
// that claims a negative size, or one above what this platform can address, is
// either broken or hostile — and neither answer belongs anywhere near a file —
// so both directions are checked rather than assumed.
func putSize(size int64) (uint64, error) {
	if size < 0 {
		return 0, fmt.Errorf("p2p: size %d is negative", size)
	}

	return uint64(size), nil
}

// putCount converts a file count for the wire.
//
// A count always comes from a manifest, and a manifest is itself length-bounded
// by the frame that carried it, so the conversion cannot overflow in practice.
// It is checked anyway so that a future limit change fails loudly here rather
// than as a silently wrapped count on the far side.
func putCount(count int) (uint32, error) {
	if count < 0 || count > math.MaxUint32 {
		return 0, fmt.Errorf("p2p: %d is not a usable file count", count)
	}

	return uint32(count), nil
}

func takeSize(size uint64) (int64, error) {
	if size > math.MaxInt64 {
		return 0, fmt.Errorf("p2p: size %d is larger than this platform can address", size)
	}

	return int64(size), nil
}

// FileStartFrame announces one archive and the digest it must arrive with.
type FileStartFrame struct {
	FileID string
	SHA256 []byte
	Size   int64
}

// Encode renders the frame.
func (f *FileStartFrame) Encode() ([]byte, error) {
	if len(f.SHA256) != sha256Size {
		return nil, fmt.Errorf("p2p: file %q digest is %d bytes, want %d",
			f.FileID, len(f.SHA256), sha256Size)
	}

	if len(f.FileID) > 0xffff {
		return nil, fmt.Errorf("p2p: file id is %d bytes, too long for a frame", len(f.FileID))
	}

	size, err := putSize(f.Size)
	if err != nil {
		return nil, err
	}

	w := &writer{}
	w.byte(byte(FrameFileStart))
	w.short([]byte(f.FileID))
	w.uint64(size)
	w.raw(f.SHA256)

	return w.bytes(), nil
}

// DecodeFileStart parses a FILE_START frame.
func DecodeFileStart(payload []byte) (*FileStartFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != FrameFileStart {
		return nil, ErrUnknownFrame
	}

	fileID, err := r.short()
	if err != nil {
		return nil, err
	}

	size, err := r.uint64()
	if err != nil {
		return nil, err
	}

	sum, err := r.fixed(sha256Size)
	if err != nil {
		return nil, err
	}

	if err := r.done(); err != nil {
		return nil, err
	}

	decoded, err := takeSize(size)
	if err != nil {
		return nil, err
	}

	return &FileStartFrame{FileID: string(fileID), Size: decoded, SHA256: sum}, nil
}

// ChunkFrame carries part of an archive.
//
// The offset is explicit rather than implied by the running total so that a
// receiver can tell a reordered or duplicated chunk from a correct one, which
// is the difference between detecting corruption and silently writing a
// mangled archive.
type ChunkFrame struct {
	Payload []byte
	Offset  uint64
}

// Encode renders the frame.
func (c *ChunkFrame) Encode() []byte {
	w := &writer{}
	w.byte(byte(FrameChunk))
	w.uint64(c.Offset)
	w.raw(c.Payload)

	return w.bytes()
}

// DecodeChunk parses a CHUNK frame. The payload is whatever is left in the
// message, so it is bounded by maxChunkBytes rather than maxFieldBytes.
func DecodeChunk(payload []byte) (*ChunkFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != FrameChunk {
		return nil, ErrUnknownFrame
	}

	offset, err := r.uint64()
	if err != nil {
		return nil, err
	}

	// The payload is taken before done is consulted. done reports bytes left
	// over, and on a CHUNK frame those bytes are the payload — so checking it
	// first would reject every frame that carried any data at all.
	if r.err != nil {
		return nil, r.err
	}

	if r.remaining() > maxChunkBytes {
		return nil, fmt.Errorf("%w: chunk of %d bytes exceeds the %d byte limit",
			ErrFrameTruncated, r.remaining(), maxChunkBytes)
	}

	return &ChunkFrame{Offset: offset, Payload: r.rest()}, nil
}

// FileDoneFrame closes one archive by repeating what it should hash to.
//
// Repeating the digest is what catches a sender whose bufferedAmount bookkeeping
// dropped a chunk on the way out: the byte count says the file is complete, but
// only the digest says it is the right complete.
type FileDoneFrame struct {
	FileID string
	SHA256 []byte
	Size   int64
}

// Encode renders the frame.
func (f *FileDoneFrame) Encode() ([]byte, error) {
	if len(f.SHA256) != sha256Size {
		return nil, fmt.Errorf("p2p: file %q digest is %d bytes, want %d",
			f.FileID, len(f.SHA256), sha256Size)
	}

	size, err := putSize(f.Size)
	if err != nil {
		return nil, err
	}

	w := &writer{}
	w.byte(byte(FrameFileDone))
	w.short([]byte(f.FileID))
	w.uint64(size)
	w.raw(f.SHA256)

	return w.bytes(), nil
}

// DecodeFileDone parses a FILE_DONE frame.
func DecodeFileDone(payload []byte) (*FileDoneFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != FrameFileDone {
		return nil, ErrUnknownFrame
	}

	fileID, err := r.short()
	if err != nil {
		return nil, err
	}

	size, err := r.uint64()
	if err != nil {
		return nil, err
	}

	sum, err := r.fixed(sha256Size)
	if err != nil {
		return nil, err
	}

	if err := r.done(); err != nil {
		return nil, err
	}

	decoded, err := takeSize(size)
	if err != nil {
		return nil, err
	}

	return &FileDoneFrame{FileID: string(fileID), Size: decoded, SHA256: sum}, nil
}

// CompleteFrame ends a successful session and states what it carried.
type CompleteFrame struct {
	FileCount  uint32
	TotalBytes int64
}

// Encode renders the frame.
func (c *CompleteFrame) Encode() ([]byte, error) {
	size, err := putSize(c.TotalBytes)
	if err != nil {
		return nil, err
	}

	w := &writer{}
	w.byte(byte(FrameComplete))
	w.uint32(c.FileCount)
	w.uint64(size)

	return w.bytes(), nil
}

// DecodeComplete parses a COMPLETE frame.
func DecodeComplete(payload []byte) (*CompleteFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != FrameComplete {
		return nil, ErrUnknownFrame
	}

	count, err := r.uint32()
	if err != nil {
		return nil, err
	}

	total, err := r.uint64()
	if err != nil {
		return nil, err
	}

	if err := r.done(); err != nil {
		return nil, err
	}

	bytesTotal, err := takeSize(total)
	if err != nil {
		return nil, err
	}

	return &CompleteFrame{FileCount: count, TotalBytes: bytesTotal}, nil
}

// TransferCompleteFrame is the receiver's confirmation that a transfer landed.
//
// It deliberately repeats the same two numbers COMPLETE carried. The receiver is
// not merely saying "I got something"; it is saying "I got exactly the files and
// the bytes that were promised", which lets the sender treat a mismatch as a
// failure rather than as a transfer that quietly differed from the offer.
type TransferCompleteFrame struct {
	FileCount  uint32
	TotalBytes int64
}

// Encode renders the frame.
func (c *TransferCompleteFrame) Encode() ([]byte, error) {
	size, err := putSize(c.TotalBytes)
	if err != nil {
		return nil, err
	}

	w := &writer{}
	w.byte(byte(FrameTransferComplete))
	w.uint32(c.FileCount)
	w.uint64(size)

	return w.bytes(), nil
}

// DecodeTransferComplete parses a TRANSFER_COMPLETE frame.
func DecodeTransferComplete(payload []byte) (*TransferCompleteFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != FrameTransferComplete {
		return nil, ErrUnknownFrame
	}

	count, err := r.uint32()
	if err != nil {
		return nil, err
	}

	total, err := r.uint64()
	if err != nil {
		return nil, err
	}

	if err := r.done(); err != nil {
		return nil, err
	}

	bytesTotal, err := takeSize(total)
	if err != nil {
		return nil, err
	}

	return &TransferCompleteFrame{FileCount: count, TotalBytes: bytesTotal}, nil
}

// StatusFrame carries an abort or an error. The two share a wire shape because
// they mean the same thing to a state machine — the sender cannot continue —
// and differ only in whether the session is over.
type StatusFrame struct {
	Reason string
	Code   uint32
}

// Encode renders the frame for the given type.
func (s *StatusFrame) Encode(typ FrameType) ([]byte, error) {
	switch typ {
	case FrameAbort, FrameError:
	default:
		return nil, fmt.Errorf("p2p: %s is not a status frame", typ)
	}

	if len(s.Reason) > 0xffff {
		return nil, fmt.Errorf("p2p: reason is %d bytes, too long for a frame", len(s.Reason))
	}

	w := &writer{}
	w.byte(byte(typ))
	w.uint32(s.Code)
	w.short([]byte(s.Reason))

	return w.bytes(), nil
}

// DecodeStatus parses an ABORT or ERROR frame. The expected type is passed in
// rather than returned so that a caller which is waiting for one cannot
// accidentally accept the other.
func DecodeStatus(payload []byte, want FrameType) (*StatusFrame, error) {
	r := &reader{buf: payload}

	typ, err := r.byte()
	if err != nil {
		return nil, err
	}

	if FrameType(typ) != want {
		return nil, ErrUnknownFrame
	}

	code, err := r.uint32()
	if err != nil {
		return nil, err
	}

	reason, err := r.short()
	if err != nil {
		return nil, err
	}

	if err := r.done(); err != nil {
		return nil, err
	}

	return &StatusFrame{Code: code, Reason: string(reason)}, nil
}

// FrameTypeOf returns the type byte of a message, for a state machine that has
// to dispatch before it knows the rest of the frame is well-formed.
func FrameTypeOf(payload []byte) (FrameType, error) {
	if len(payload) < 1 {
		return 0, ErrFrameTruncated
	}

	return FrameType(payload[0]), nil
}

// writer assembles a frame. A byte slice rather than bytes.Buffer: a frame is
// built once, measured once and sent once, and the append chain reads more like
// the specification it implements than a sequence of Write calls would.
type writer struct {
	buf []byte
}

func (w *writer) byte(v byte) {
	w.buf = append(w.buf, v)
}

// uint32 writes a big-endian 32-bit field.
func (w *writer) uint32(v uint32) {
	w.buf = binary.BigEndian.AppendUint32(w.buf, v)
}

// uint64 writes a big-endian 64-bit field.
func (w *writer) uint64(v uint64) {
	w.buf = binary.BigEndian.AppendUint64(w.buf, v)
}

// uint16 writes a big-endian 16-bit field.
func (w *writer) uint16(v uint16) {
	w.buf = binary.BigEndian.AppendUint16(w.buf, v)
}

// short writes a field prefixed by its 2-byte length.
func (w *writer) short(v []byte) {
	w.uint16(uint16(len(v))) //nolint:gosec // every caller bounds the length before writing.
	w.raw(v)
}

// field writes a field prefixed by its 4-byte length.
func (w *writer) field(v []byte) {
	w.uint32(uint32(len(v))) //nolint:gosec // every caller bounds the length before writing.
	w.raw(v)
}

// raw appends bytes with no header.
func (w *writer) raw(v []byte) {
	w.buf = append(w.buf, v...)
}

func (w *writer) bytes() []byte {
	return w.buf
}

// reader walks a frame. Every accessor is total: a reader that runs past the
// end records the failure and returns zero values, so the decoders above can
// check once at the end instead of after every field.
type reader struct {
	err error
	buf []byte
	off int
}

// byte reads a one-byte field.
func (r *reader) byte() (byte, error) {
	if r.off+1 > len(r.buf) {
		r.err = ErrFrameTruncated

		return 0, r.err
	}

	v := r.buf[r.off]
	r.off++

	return v, nil
}

// uint16 reads a big-endian 16-bit field.
func (r *reader) uint16() (uint16, error) {
	if r.off+2 > len(r.buf) {
		r.err = ErrFrameTruncated

		return 0, r.err
	}

	v := binary.BigEndian.Uint16(r.buf[r.off:])
	r.off += 2

	return v, nil
}

// uint32 reads a big-endian 32-bit field.
func (r *reader) uint32() (uint32, error) {
	if r.off+4 > len(r.buf) {
		r.err = ErrFrameTruncated

		return 0, r.err
	}

	v := binary.BigEndian.Uint32(r.buf[r.off:])
	r.off += 4

	return v, nil
}

// uint64 reads a big-endian 64-bit field.
func (r *reader) uint64() (uint64, error) {
	if r.off+8 > len(r.buf) {
		r.err = ErrFrameTruncated

		return 0, r.err
	}

	v := binary.BigEndian.Uint64(r.buf[r.off:])
	r.off += 8

	return v, nil
}

// short reads a 2-byte-length-prefixed field. The prefix is itself the bound:
// a field written this way cannot claim more than 64 KiB.
func (r *reader) short() ([]byte, error) {
	length, err := r.uint16()
	if err != nil {
		return nil, err
	}

	return r.fixed(int(length))
}

// field reads a 4-byte-length-prefixed field.
func (r *reader) field() ([]byte, error) {
	length, err := r.uint32()
	if err != nil {
		return nil, err
	}

	if length > maxFieldBytes {
		r.err = fmt.Errorf("%w: field claims %d bytes", ErrFrameTruncated, length)

		return nil, r.err
	}

	return r.fixed(int(length))
}

// fixed reads exactly n bytes.
func (r *reader) fixed(n int) ([]byte, error) {
	if r.off+n > len(r.buf) {
		r.err = ErrFrameTruncated

		return nil, r.err
	}

	out := make([]byte, n)
	copy(out, r.buf[r.off:r.off+n])
	r.off += n

	return out, nil
}

// rest returns everything not yet consumed, without copying.
func (r *reader) rest() []byte {
	return r.buf[r.off:]
}

// remaining counts the bytes not yet consumed.
func (r *reader) remaining() int {
	return len(r.buf) - r.off
}

// done reports the first failure the reader hit, plus whether the frame ended
// exactly where it should. Trailing bytes are as suspicious as a short frame:
// they mean the sender's writer and the reader's decoder disagree about the
// layout, and continuing would interpret one field as two.
func (r *reader) done() error {
	if r.err != nil {
		return r.err
	}

	if r.remaining() != 0 {
		return fmt.Errorf("%w: %d bytes left over", ErrFrameTruncated, r.remaining())
	}

	return nil
}
