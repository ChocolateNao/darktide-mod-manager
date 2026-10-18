package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/crypto"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// Transfer errors. Integrity failures get their own sentinel because the
// recipient's only correct response to one is to discard the bytes and use the
// source links, and that is different enough from "the connection dropped" to
// deserve its own path through the UI.
var (
	// ErrIntegrity reports a file whose received bytes do not hash to the
	// value published in the offer, or a frame whose contents disagree with
	// the offer.
	ErrIntegrity = errors.New("received file does not match its published hash")
	// ErrUnexpectedFrame reports a frame that does not belong where it
	// arrived: a chunk for a file that was never announced, or a manifest in
	// the middle of a transfer. It is a protocol violation rather than a
	// transport failure, and it means the peer is not speaking this protocol
	// even if it holds the right key.
	ErrUnexpectedFrame = errors.New("unexpected frame in share protocol")
	// ErrSizeMismatch reports a file that arrived the wrong length, or a
	// transfer whose totals do not add up.
	ErrSizeMismatch = errors.New("received file has the wrong length")
	// ErrPeerRejected reports that the other end sent ABORT.
	ErrPeerRejected = errors.New("the other end declined the transfer")
)

// Abort reasons. They are sent verbatim in the ABORT frame so the sender can
// tell the recipient apart from a connection that merely failed: "I do not trust
// you" and "the network is down" need different reactions, and a recipient that
// has declined in favour of the source links should say so rather than let the
// sender retry.
const (
	// ReasonRejected is the default decline message.
	ReasonRejected = "recipient declined the transfer"
	// ReasonUntrusted is sent when the recipient was offered the upstream
	// source links and preferred them.
	ReasonUntrusted = "recipient does not trust this peer; use the source links instead"
)

// Session is one end of a share: the connection, the secret both halves derive
// the handshake key from, and the offer they agreed on.
//
// It is a value rather than three arguments because the three are genuinely one
// thing. Passing the secret separately from the offer is the mistake that lets a
// caller derive a handshake key from the offer id — which travels in the clear
// inside the link — instead of from the capability secret, and a transfer that
// authenticates nothing is worse than one that fails loudly.
type Session struct {
	// Peer is the open data channel.
	Peer *Peer
	// Secret is the capability secret from the link.
	Secret string
	// Offer is the offer both halves share.
	Offer domain.P2PShareOffer
}

// handshakeKey derives the key that authenticates the handshake. It is derived on
// each call rather than cached in the struct: a session performs exactly two
// handshakes, so the saving would be two HMACs, and a cached key would be a
// secret living in a struct that gets copied around.
func (s Session) handshakeKey() ([]byte, error) {
	return SessionKeyFor(s.Secret)
}

// ProgressFunc is called as bytes move. It is allowed to be nil.
//
// Every callback runs on the transfer goroutine, so an implementation that
// touches the UI must hand off rather than block; the service layer does that
// by publishing the session record and letting the frontend poll or subscribe.
type ProgressFunc func(domain.P2PTransferProgress)

// kindOf is FrameTypeOf without its error. The call sites are dispatching, not
// validating: a payload too short to have a type byte has no type, and the
// frame's own decoder reports that precisely. Discarding the error keeps every
// dispatch on one line.
func kindOf(payload []byte) FrameType {
	kind, _ := FrameTypeOf(payload)

	return kind
}

// Send runs the offering half of the protocol and closes the peer.
//
// stageDir is where the archives named by the offer's files were staged. They are
// read in offer order, because the offer is the contract: the recipient sizes its
// progress bar and reserves its disk from that order, so a sender that reordered
// them would make every FILE_START look like an out-of-order arrival.
func Send(ctx context.Context, session Session, stageDir string, onProgress ProgressFunc) error {
	sender := &sender{session: session, stageDir: stageDir, onProgress: onProgress}

	defer shutdown(session.Peer, ctx)

	return sender.run(ctx)
}

// Receive runs the answering half of the protocol and closes the peer.
//
// destDir must exist. Archives are written under the names the offer gives them,
// which the sender derives from mod ids, so the result maps one-to-one onto the
// offer's inventory and the caller can install without trusting anything the peer
// said about what the files contain.
func Receive(ctx context.Context, session Session, destDir string, onProgress ProgressFunc) error {
	receiver := &receiver{session: session, destDir: destDir, onProgress: onProgress}

	defer shutdown(session.Peer, ctx)

	return receiver.run(ctx)
}

// flushGrace bounds how long a half-finished exchange is given to reach the wire
// before the connection is torn down anyway.
const flushGrace = 5 * time.Second

// Reject answers an offer and declines it.
//
// The decline travels over the same data channel a transfer would, rather than
// being signalled by never connecting, because a sender waiting on a silent peer
// cannot tell "they declined" from "they went to lunch". ABORT as the very first
// frame makes the decision legible: the sender is still in its handshake and
// treats ABORT there as a refusal rather than a protocol violation.
//
// The secret is still required. A recipient who cannot open the link has proved
// nothing about themselves, and refusing to talk to an unauthenticated peer is the
// whole point of the capability.
func Reject(ctx context.Context, session Session, reason string) error {
	defer shutdown(session.Peer, ctx)

	if err := session.Peer.WaitOpen(ctx); err != nil {
		return fmt.Errorf("waiting for the sender to connect: %w", err)
	}

	if reason == "" {
		reason = ReasonRejected
	}

	payload, err := (&StatusFrame{Code: 0, Reason: reason}).Encode(FrameAbort)
	if err != nil {
		return err
	}

	return session.Peer.Send(ctx, payload, defaultFlowControlThreshold)
}

// shutdown drains whatever is still queued and then closes the peer. The drain is
// what makes the last frame of a transfer arrive: a hard close discards bytes the
// other end has not read out of its socket buffer, and the symptom on that end is
// a truncated transfer rather than a closed connection.
//
// The context passed to a transfer is routinely already cancelled by the time the
// defer runs, which is the normal case for a user who closed a window, so the
// drain gets a fresh bounded one. Best effort throughout: nothing here can turn a
// failed transfer into a successful one, and the errors are dropped rather than
// replacing the one the caller is about to see.
func shutdown(peer *Peer, ctx context.Context) {
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushGrace)
	defer cancel()

	_ = peer.Flush(grace)
	_ = peer.Close()
}

// encodableFrame is the shape every frame the protocol sends shares. Encoding is
// fallible — a size the wire cannot carry is refused rather than truncated — so
// the call site has to handle it.
type encodableFrame interface {
	Encode() ([]byte, error)
}

// sendFrame encodes a frame and puts it on the wire under the flow-control
// watermark. Folding the two steps together keeps "this frame did not fit"
// attached to the frame that caused it.
func sendFrame(ctx context.Context, peer *Peer, frame encodableFrame) error {
	payload, err := frame.Encode()
	if err != nil {
		return err
	}

	return peer.Send(ctx, payload, defaultFlowControlThreshold)
}

// receiver implements the answering side of the wire protocol.
type receiver struct {
	onProgress ProgressFunc
	open       *openFile
	session    Session
	destDir    string
	files      []domain.P2PSharedFile
	received   int64
}

// peer is a shorthand for the session's connection, which every method below
// needs and which reads better than session.Peer at each use.
func (r *receiver) peer() *Peer {
	return r.session.Peer
}

// openFile is the state of one archive being written.
type openFile struct {
	digest  hash.Hash
	handle  *os.File
	path    string
	file    domain.P2PSharedFile
	written int64
}

// run performs the handshake and then the transfer. The order is fixed, and is
// the reason this is a protocol rather than "open a data channel and send bytes":
// the peer proves it holds the link secret before a single file is created, so
// somebody who intercepted the link but cannot complete the handshake cannot use
// the share to write arbitrary bytes onto this machine.
func (r *receiver) run(ctx context.Context) error {
	if err := r.peer().WaitOpen(ctx); err != nil {
		return fmt.Errorf("waiting for the sender to connect: %w", err)
	}

	if err := r.handshake(ctx); err != nil {
		return err
	}

	if err := r.expectManifest(ctx); err != nil {
		return err
	}

	return r.readFiles(ctx)
}

// handshake exchanges HELLO and HELLO_ACK. The answer carries a MAC over the
// sender's nonce plus one of the receiver's choosing. Echoing the sender's nonce
// alone would prove nothing against an attacker who could replay a recorded
// HELLO; binding both means a recorded exchange cannot be replayed into a fresh
// data channel, because the receiver's half would have to be regenerated against
// the new sender nonce.
func (r *receiver) handshake(ctx context.Context) error {
	key, err := r.session.handshakeKey()
	if err != nil {
		return err
	}

	raw, err := r.next(ctx)
	if err != nil {
		return fmt.Errorf("waiting for hello: %w", err)
	}

	kind := kindOf(raw)
	if kind != FrameHello {
		return fmt.Errorf("%w: expected HELLO, got %s", ErrUnexpectedFrame, kind)
	}

	hello, err := DecodeHello(raw)
	if err != nil {
		return err
	}

	if hello.Role != RoleSender {
		return fmt.Errorf("%w: HELLO claims role %s", ErrBadRole, hello.Role)
	}

	if hello.Version != ProtocolVersion {
		return fmt.Errorf("%w: peer speaks version %d, this build speaks %d",
			ErrUnknownProtocolVersion, hello.Version, ProtocolVersion)
	}

	if hello.OfferID != r.session.Offer.OfferID {
		return fmt.Errorf("%w: HELLO names offer %q, this share is %q",
			ErrIntegrity, hello.OfferID, r.session.Offer.OfferID)
	}

	if err := hello.Verify(key); err != nil {
		return err
	}

	ack, err := BuildHelloAck(key, r.session.Offer.OfferID, hello.Nonce)
	if err != nil {
		return err
	}

	payload, err := ack.Encode()
	if err != nil {
		return err
	}

	return r.peer().Send(ctx, payload, defaultFlowControlThreshold)
}

// expectManifest checks the sender's manifest against the offer this receiver
// accepted. The manifest is redundant with the offer on purpose: the offer travelled
// through a chat window and could have been pasted here from a different share,
// while the manifest travels over the authenticated channel from the peer that has
// just proved it holds the key. Comparing them means the recipient installs exactly
// the inventory its peer promised, and a mismatch is caught before a byte is written.
func (r *receiver) expectManifest(ctx context.Context) error {
	raw, err := r.next(ctx)
	if err != nil {
		return fmt.Errorf("waiting for manifest: %w", err)
	}

	kind := kindOf(raw)
	if kind != FrameManifest {
		return fmt.Errorf("%w: expected MANIFEST, got %s", ErrUnexpectedFrame, kind)
	}

	frame, err := DecodeManifest(raw)
	if err != nil {
		return err
	}

	if frame.OfferID != r.session.Offer.OfferID {
		return fmt.Errorf("%w: MANIFEST names offer %q, this share is %q",
			ErrIntegrity, frame.OfferID, r.session.Offer.OfferID)
	}

	var sent domain.P2PShareOffer

	if err := json.Unmarshal(frame.Payload, &sent); err != nil {
		return fmt.Errorf("%w: manifest is not a share offer: %v", ErrIntegrity, err)
	}

	if err := compareInventory(r.session.Offer.Files, sent.Files); err != nil {
		return err
	}

	r.files = sent.Files

	return nil
}

// compareInventory checks two file lists describe the same transfer.
func compareInventory(want, got []domain.P2PSharedFile) error {
	if len(got) != len(want) {
		return fmt.Errorf("%w: manifest lists %d files, the offer listed %d",
			ErrIntegrity, len(got), len(want))
	}

	for i := range want {
		if got[i].FileID != want[i].FileID ||
			got[i].Size != want[i].Size ||
			got[i].HashSHA256 != want[i].HashSHA256 {
			return fmt.Errorf(
				"%w: file %d is %s (%d bytes, %s) in the manifest but %s (%d bytes, %s) in the offer",
				ErrIntegrity,
				i,
				got[i].FileID,
				got[i].Size,
				shortHex(got[i].HashSHA256),
				want[i].FileID,
				want[i].Size,
				shortHex(want[i].HashSHA256),
			)
		}
	}

	return nil
}

// readFiles consumes frames until COMPLETE. The loop's exit condition is the
// complete frame rather than a frame count: a sender that dies half way through
// leaves the receiver blocked in next() until the connection fails, which is the
// right outcome, whereas counting frames would let a truncated transfer look complete.
func (r *receiver) readFiles(ctx context.Context) error {
	for {
		raw, err := r.next(ctx)
		if err != nil {
			r.discardPartial()

			return err
		}

		if err := r.handleFrame(ctx, raw); err != nil {
			r.discardPartial()

			return err
		}

		if kindOf(raw) == FrameComplete {
			return nil
		}
	}
}

// handleFrame advances the state machine by one frame. A dispatch rather than a
// chain of conditionals inside the read loop, so that each frame type's rules sit
// together and a new frame type becomes one entry rather than an edit to a loop
// with three nesting levels.
func (r *receiver) handleFrame(ctx context.Context, payload []byte) error {
	switch kind := kindOf(payload); kind {
	case FrameFileStart:
		return r.onFileStart(payload)
	case FrameChunk:
		return r.onChunk(payload)
	case FrameFileDone:
		return r.onFileDone(payload)
	case FrameComplete:
		return r.onComplete(ctx, payload)
	case FrameAbort:
		return r.onAbort(payload)
	case FrameError:
		return r.onError(payload)
	default:
		return fmt.Errorf("%w: %s", ErrUnexpectedFrame, kind)
	}
}

// onFileStart opens the archive the sender is about to stream. The expected size
// and hash come from the offer, not from this frame: a frame announcing a different
// size would be the peer asking the receiver to reserve space it had not agreed to,
// so the frame's numbers are checked rather than adopted, and the file is never
// created until they match.
func (r *receiver) onFileStart(payload []byte) error {
	frame, err := DecodeFileStart(payload)
	if err != nil {
		return err
	}

	if r.open != nil {
		return fmt.Errorf("%w: FILE_START for %q while %q is still open",
			ErrUnexpectedFrame, frame.FileID, r.open.file.FileID)
	}

	index := r.indexOf(frame.FileID)
	if index < 0 {
		return fmt.Errorf(
			"%w: FILE_START for %q, which the offer does not list",
			ErrIntegrity,
			frame.FileID,
		)
	}

	expected := r.files[index]

	if frame.Size != expected.Size {
		return fmt.Errorf("%w: %q is %d bytes in FILE_START, %d in the offer",
			ErrIntegrity, frame.FileID, frame.Size, expected.Size)
	}

	if want := decodeHash(expected.HashSHA256); len(frame.SHA256) == sha256.Size &&
		!equalHash(want, frame.SHA256) {
		return fmt.Errorf("%w: FILE_START announces hash %s for %q, the offer published %s",
			ErrIntegrity, shortSum(frame.SHA256), frame.FileID, shortHex(expected.HashSHA256))
	}

	// 0600 rather than the usual 0644: a staging file holds somebody else's bytes
	// until it has been verified, and a world-readable archive in a temporary
	// directory is a window where a local process can read a mod that has not
	// passed its hash check yet.
	path := filepath.Join(r.destDir, expected.Name)

	handle, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	r.open = &openFile{
		file:   expected,
		path:   path,
		handle: handle,
		digest: sha256.New(),
	}

	return nil
}

// onChunk appends one slice of the open archive. Offsets are checked rather than
// used: the receiver could seek to whatever offset it liked, which would make a
// duplicated or reordered chunk look fine until the hash failed at the end, whereas
// demanding that chunks arrive in order turns any such mistake into an error at the
// moment it happens, naming the frame that caused it.
func (r *receiver) onChunk(payload []byte) error {
	frame, err := DecodeChunk(payload)
	if err != nil {
		return err
	}

	if r.open == nil {
		return fmt.Errorf("%w: CHUNK outside a FILE_START", ErrUnexpectedFrame)
	}

	// written only grows by a payload checked against the announced size, so a negative
	// total is impossible — but it is checked, because a wrong offset is one corruption
	// a digest alone would catch late.
	expected, err := putSize(r.open.written)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIntegrity, err)
	}

	if frame.Offset != expected {
		return fmt.Errorf("%w: CHUNK for %q at offset %d, expected %d",
			ErrIntegrity, r.open.file.FileID, frame.Offset, r.open.written)
	}

	written, err := r.open.handle.Write(frame.Payload)
	if err != nil {
		return fmt.Errorf("write %s: %w", r.open.path, err)
	}

	// A short write would leave a hole in the archive, and the hash check at the
	// end should not be the one to notice.
	if written != len(frame.Payload) {
		return fmt.Errorf("%w: wrote %d of %d bytes of chunk for %q",
			ErrSizeMismatch, written, len(frame.Payload), r.open.file.FileID)
	}

	r.open.digest.Write(frame.Payload)
	r.open.written += int64(written)
	r.received += int64(written)

	if r.onProgress != nil {
		r.onProgress(r.progress())
	}

	return nil
}

// onFileDone closes the archive and checks its hash.
func (r *receiver) onFileDone(payload []byte) error {
	frame, err := DecodeFileDone(payload)
	if err != nil {
		return err
	}

	if r.open == nil {
		return fmt.Errorf("%w: FILE_DONE with no file open", ErrUnexpectedFrame)
	}

	if frame.FileID != r.open.file.FileID {
		return fmt.Errorf("%w: FILE_DONE for %q while %q is open",
			ErrUnexpectedFrame, frame.FileID, r.open.file.FileID)
	}

	expected := r.files[r.indexOf(frame.FileID)]
	if err := r.closeOpen(); err != nil {
		return err
	}

	if frame.Size != expected.Size {
		return fmt.Errorf("%w: %q is %d bytes, the offer said %d",
			ErrSizeMismatch, frame.FileID, frame.Size, expected.Size)
	}

	// FILE_DONE's hash is the sender's claim about what it just sent; the offer's
	// is what the recipient agreed to receive. Both are compared, because a
	// disagreement means the peer and the link came apart somewhere, which is
	// worth knowing even when the bytes were fine.
	if len(frame.SHA256) == sha256.Size &&
		!equalHash(decodeHash(expected.HashSHA256), frame.SHA256) {
		return fmt.Errorf("%w: %q hashed %s in FILE_DONE, the offer published %s",
			ErrIntegrity, frame.FileID, shortSum(frame.SHA256), shortHex(expected.HashSHA256))
	}

	if r.onProgress != nil {
		r.onProgress(r.progress())
	}

	return nil
}

// onComplete checks the totals, confirms them to the sender and ends the
// transfer. The confirmation is not politeness: it is what lets the sender close
// its half of the connection without discarding bytes the receiver has not read
// yet, and it is the last moment at which the receiver can say "these are not the
// files I agreed to" while the sender is still listening.
func (r *receiver) onComplete(ctx context.Context, payload []byte) error {
	frame, err := DecodeComplete(payload)
	if err != nil {
		return err
	}

	if r.open != nil {
		return fmt.Errorf(
			"%w: COMPLETE while %q is still open",
			ErrUnexpectedFrame,
			r.open.file.FileID,
		)
	}

	var expected int64

	for _, file := range r.files {
		expected += file.Size
	}

	if int(frame.FileCount) != len(r.files) {
		return fmt.Errorf("%w: COMPLETE claims %d files, the offer has %d",
			ErrIntegrity, frame.FileCount, len(r.files))
	}

	if frame.TotalBytes != expected {
		return fmt.Errorf("%w: COMPLETE claims %d bytes, the offer totals %d",
			ErrIntegrity, frame.TotalBytes, expected)
	}

	if r.received != expected {
		return fmt.Errorf("%w: received %d bytes, the offer totals %d",
			ErrSizeMismatch, r.received, expected)
	}

	count, err := putCount(len(r.files))
	if err != nil {
		return err
	}

	confirmed := &TransferCompleteFrame{FileCount: count, TotalBytes: r.received}

	return sendFrame(ctx, r.peer(), confirmed)
}

// onAbort reports that the sender declined the transfer.
func (r *receiver) onAbort(payload []byte) error {
	frame, err := DecodeStatus(payload, FrameAbort)
	if err != nil {
		return err
	}

	reason := frame.Reason
	if reason == "" {
		reason = "no reason given"
	}

	return fmt.Errorf("%w: %s", ErrPeerRejected, reason)
}

// onError reports that the sender failed.
func (r *receiver) onError(payload []byte) error {
	frame, err := DecodeStatus(payload, FrameError)
	if err != nil {
		return err
	}

	reason := frame.Reason
	if reason == "" {
		reason = "no reason given"
	}

	return fmt.Errorf("sender reported an error: %s", reason)
}

// closeOpen finalises the open archive and verifies its hash. The file is removed
// before an integrity error is returned: leaving a file that failed its hash in
// the staging directory would mean the next install attempt picks it up as though
// it were good, which is exactly the failure the hash exists to prevent.
func (r *receiver) closeOpen() error {
	if r.open == nil {
		return nil
	}

	open := r.open
	r.open = nil

	if err := open.handle.Close(); err != nil {
		return fmt.Errorf("close %s: %w", open.path, err)
	}

	sum := hex.EncodeToString(open.digest.Sum(nil))
	if sum == open.file.HashSHA256 {
		return nil
	}

	removeErr := os.Remove(open.path)

	return fmt.Errorf("%w: %q hashed %s, the offer published %s (removed: %v)",
		ErrIntegrity, open.file.FileID, shortHex(sum), shortHex(open.file.HashSHA256), removeErr)
}

// discardPartial removes a half-written archive when a transfer ends badly. It is
// best effort by design: the error that brought us here is already going to be
// returned, and losing the removal error would lose nothing while gaining a second
// error to report.
func (r *receiver) discardPartial() {
	if r.open == nil {
		return
	}

	open := r.open
	r.open = nil

	_ = open.handle.Close()
	_ = os.Remove(open.path)
}

// next returns the next frame, failing if the peer dies first.
//
// Queued frames are taken before a failure is reported. A sender that finishes
// closes its half of the connection straight after COMPLETE, and that close
// arrives on the failure channel while the frames before it are still sitting in
// the queue — pion hands a message to OnMessage before it reports the channel
// closed, so everything sent is already buffered. Draining first is what makes a
// transfer that succeeded not fail on its last frame.
func (r *receiver) next(ctx context.Context) ([]byte, error) {
	select {
	case frame := <-r.peer().Received():
		return frame, nil
	default:
	}

	select {
	case frame := <-r.peer().Received():
		return frame, nil
	case err := <-r.peer().Failures():
		return nil, fmt.Errorf("peer connection failed: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// indexOf finds a file's position in the inventory.
func (r *receiver) indexOf(fileID string) int {
	for i, file := range r.files {
		if file.FileID == fileID {
			return i
		}
	}

	return -1
}

// progress renders the receiver's current position for the UI.
func (r *receiver) progress() domain.P2PTransferProgress {
	var p domain.P2PTransferProgress

	p.FileCount = len(r.files)
	p.BytesReceived = r.received

	for _, file := range r.files {
		p.TotalBytes += file.Size
	}

	if r.open == nil {
		p.FileIndex = p.FileCount

		return p
	}

	p.FileID = r.open.file.FileID
	p.ModID = r.open.file.ModID
	p.FileIndex = r.indexOf(r.open.file.FileID)

	return p
}

// sender implements the offering side of the wire protocol.
type sender struct {
	onProgress ProgressFunc
	stageDir   string
	session    Session
	chunkSize  int
	sent       int64
}

// peer is a shorthand for the session's connection.
func (s *sender) peer() *Peer {
	return s.session.Peer
}

// run performs the handshake and then streams every archive.
func (s *sender) run(ctx context.Context) error {
	s.chunkSize = s.peer().ChunkSize()

	// The wait is explicit: otherwise a share whose answer never arrives fails with
	// "send frame", which reads like a protocol fault rather than a dead connection.
	if err := s.peer().WaitOpen(ctx); err != nil {
		return fmt.Errorf("waiting for the recipient to connect: %w", err)
	}

	if err := s.handshake(ctx); err != nil {
		return err
	}

	if err := s.sendManifest(ctx); err != nil {
		return err
	}

	return s.sendFiles(ctx)
}

// handshake sends HELLO and waits for the matching HELLO_ACK.
func (s *sender) handshake(ctx context.Context) error {
	key, err := s.session.handshakeKey()
	if err != nil {
		return err
	}

	hello, err := BuildHello(key, RoleSender, s.session.Offer.OfferID)
	if err != nil {
		return err
	}

	payload, err := hello.Encode()
	if err != nil {
		return err
	}

	if err := s.peer().Send(ctx, payload, defaultFlowControlThreshold); err != nil {
		return err
	}

	raw, err := s.next(ctx)
	if err != nil {
		return fmt.Errorf("waiting for hello ack: %w", err)
	}

	kind := kindOf(raw)

	// ABORT is legal here and nowhere else. It is how a recipient says "no" using
	// the only channel it has, and the sender is at this exact point in its
	// handshake, having just authenticated itself. Reading it as an unexpected frame
	// would turn a deliberate refusal into a protocol bug report.
	if kind == FrameAbort {
		return decodeAbort(raw, "waiting for hello ack")
	}

	if kind != FrameHelloAck {
		return fmt.Errorf("%w: expected HELLO_ACK, got %s", ErrUnexpectedFrame, kind)
	}

	ack, err := DecodeHelloAck(raw)
	if err != nil {
		return err
	}

	if ack.OfferID != s.session.Offer.OfferID {
		return fmt.Errorf("%w: HELLO_ACK names offer %q, this share is %q",
			ErrIntegrity, ack.OfferID, s.session.Offer.OfferID)
	}

	return ack.Verify(key, s.session.Offer.OfferID, hello.Nonce)
}

// sendManifest publishes the offer over the authenticated channel.
func (s *sender) sendManifest(ctx context.Context) error {
	payload, err := json.Marshal(s.session.Offer)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}

	frame := &ManifestFrame{OfferID: s.session.Offer.OfferID, Payload: payload}

	encoded, err := frame.Encode()
	if err != nil {
		return err
	}

	return s.peer().Send(ctx, encoded, defaultFlowControlThreshold)
}

// sendFiles streams each archive in offer order and then says so.
func (s *sender) sendFiles(ctx context.Context) error {
	for index, file := range s.session.Offer.Files {
		if err := s.sendFile(ctx, index, file); err != nil {
			s.reportFailure(ctx, err)

			return err
		}
	}

	count, err := putCount(len(s.session.Offer.Files))
	if err != nil {
		return err
	}

	complete := &CompleteFrame{FileCount: count, TotalBytes: s.sent}

	if err := sendFrame(ctx, s.peer(), complete); err != nil {
		return err
	}

	if err := s.awaitConfirmation(ctx, complete); err != nil {
		return err
	}

	if s.onProgress != nil {
		s.onProgress(s.progress(len(s.session.Offer.Files), domain.P2PSharedFile{}))
	}

	return nil
}

// awaitConfirmation waits for the receiver's TRANSFER_COMPLETE. Until it arrives
// the sender has written bytes and heard nothing about whether they were the right
// ones, and it must not close the connection: tearing down a data channel discards
// whatever the far end has not yet read, which is indistinguishable from a
// truncated transfer on the other side.
func (s *sender) awaitConfirmation(ctx context.Context, sent *CompleteFrame) error {
	raw, err := s.next(ctx)
	if err != nil {
		return fmt.Errorf("waiting for transfer confirmation: %w", err)
	}

	kind := kindOf(raw)
	if kind == FrameAbort {
		return decodeAbort(raw, "waiting for transfer confirmation")
	}

	if kind == FrameError {
		frame, decodeErr := DecodeStatus(raw, FrameError)
		if decodeErr == nil {
			return fmt.Errorf("receiver reported an error: %s", frame.Reason)
		}
	}

	if kind != FrameTransferComplete {
		return fmt.Errorf("%w: expected TRANSFER_COMPLETE, got %s", ErrUnexpectedFrame, kind)
	}

	confirmed, err := DecodeTransferComplete(raw)
	if err != nil {
		return err
	}

	if confirmed.FileCount != sent.FileCount || confirmed.TotalBytes != sent.TotalBytes {
		return fmt.Errorf(
			"%w: receiver confirmed %d files and %d bytes, the sender sent %d and %d",
			ErrIntegrity,
			confirmed.FileCount,
			confirmed.TotalBytes,
			sent.FileCount,
			sent.TotalBytes,
		)
	}

	return nil
}

// reportFailure tells the receiver why the transfer stopped. Best effort and
// deliberately so: the sender is already returning the real error, and a
// receiver left guessing is far better off than one told the transfer succeeded.
func (s *sender) reportFailure(ctx context.Context, cause error) {
	frame := &StatusFrame{Code: 1, Reason: cause.Error()}

	payload, err := frame.Encode(FrameError)
	if err != nil {
		return
	}

	_ = s.peer().Send(ctx, payload, defaultFlowControlThreshold)
}

// sendFile streams one archive and reports the hash the receiver will check.
func (s *sender) sendFile(ctx context.Context, index int, file domain.P2PSharedFile) error {
	path := filepath.Join(s.stageDir, file.Name)

	source, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	defer func() {
		_ = source.Close()
	}()

	start := &FileStartFrame{
		FileID: file.FileID,
		Size:   file.Size,
		SHA256: decodeHash(file.HashSHA256),
	}

	encoded, err := start.Encode()
	if err != nil {
		return err
	}

	if err := s.peer().Send(ctx, encoded, defaultFlowControlThreshold); err != nil {
		return err
	}

	if s.onProgress != nil {
		s.onProgress(s.progress(index, file))
	}

	// The digest is computed while streaming rather than in a separate pass, so
	// a few hundred megabytes of mods is read once instead of twice. The buffer
	// is reused for the same reason: a fresh allocation per chunk hands the
	// collector the allocation rate of a very large download.
	digest := sha256.New()
	buf := make([]byte, s.chunkSize)

	var offset uint64

	for {
		read, readErr := source.Read(buf)
		if read > 0 {
			chunk := &ChunkFrame{Offset: offset, Payload: buf[:read]}

			if err := s.peer().Send(ctx, chunk.Encode(), defaultFlowControlThreshold); err != nil {
				return err
			}

			digest.Write(buf[:read])
			offset += uint64(read)
			s.sent += int64(read)

			if s.onProgress != nil {
				s.onProgress(s.progress(index, file))
			}
		}

		if errors.Is(readErr, io.EOF) {
			break
		}

		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
	}

	if int64(offset) != file.Size {
		return fmt.Errorf("%w: %s is %d bytes on disk, the offer says %d",
			ErrSizeMismatch, file.Name, offset, file.Size)
	}

	done := &FileDoneFrame{
		FileID: file.FileID,
		Size:   int64(offset),
		SHA256: digest.Sum(nil),
	}

	encoded, err = done.Encode()
	if err != nil {
		return err
	}

	return s.peer().Send(ctx, encoded, defaultFlowControlThreshold)
}

// next returns the next frame from the receiver.
//
// Queued frames are taken before a failure, for the same reason the receiver
// does it: the receiver answers HELLO_ACK and then waits, and a failure
// notification that arrives alongside an already-queued ack must not lose the
// handshake.
func (s *sender) next(ctx context.Context) ([]byte, error) {
	select {
	case frame := <-s.peer().Received():
		return frame, nil
	default:
	}

	select {
	case frame := <-s.peer().Received():
		return frame, nil
	case err := <-s.peer().Failures():
		return nil, fmt.Errorf("peer connection failed: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// progress renders the sender's current position for the UI.
func (s *sender) progress(index int, file domain.P2PSharedFile) domain.P2PTransferProgress {
	var p domain.P2PTransferProgress

	p.FileID = file.FileID
	p.ModID = file.ModID
	p.FileIndex = index
	p.FileCount = len(s.session.Offer.Files)
	p.BytesSent = s.sent

	for _, offered := range s.session.Offer.Files {
		p.TotalBytes += offered.Size
	}

	return p
}

// decodeAbort turns an ABORT frame into the refusal error. A malformed abort is
// reported as an unexpected frame rather than swallowed: a peer that sent
// something undecodable at this point is either broken or not speaking this
// protocol, and neither should be reported as a polite decline.
func decodeAbort(raw []byte, at string) error {
	frame, err := DecodeStatus(raw, FrameAbort)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnexpectedFrame, at, err)
	}

	reason := frame.Reason
	if reason == "" {
		reason = ReasonRejected
	}

	return fmt.Errorf("%w: %s", ErrPeerRejected, reason)
}

// decodeHash turns a hex digest back into bytes. It returns nil for anything that
// is not a full SHA-256, which the frame decoders treat as "this frame did not
// claim a hash" rather than as an error: a hash is checked when present and simply
// absent otherwise, so a protocol version which omitted it still transfers.
func decodeHash(hexDigest string) []byte {
	raw, err := hex.DecodeString(hexDigest)
	if err != nil {
		return nil
	}

	return raw
}

// equalHash compares two digests without leaking where they differ. Constant-time
// because the digests come from a peer that has not been trusted yet: a comparison
// that returned early on the first differing byte would report, through its timing,
// how much of a forged digest was correct.
func equalHash(a, b []byte) bool {
	return crypto.EqualConstantTime(normaliseDigest(a), normaliseDigest(b))
}

// normaliseDigest pads or truncates a digest to the SHA-256 length so that two
// values of different lengths compare as unequal rather than panicking.
func normaliseDigest(sum []byte) []byte {
	out := make([]byte, sha256.Size)
	copy(out, sum)

	return out
}

// shortHex abbreviates a hex digest for an error message. A full 64-character
// hash makes a terminal wrap the message onto three lines and buries the reason.
func shortHex(digest string) string {
	if len(digest) <= 12 {
		return digest
	}

	return digest[:12] + "…"
}

// shortSum abbreviates a raw digest for an error message.
func shortSum(digest []byte) string {
	return shortHex(hex.EncodeToString(digest))
}
