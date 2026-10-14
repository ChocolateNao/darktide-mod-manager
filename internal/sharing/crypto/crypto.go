// Package crypto is the link codec for peer-to-peer sharing.
//
// A share link has to travel through places the manager does not control: a chat
// window, a mail client, a terminal paste. Everything in it is therefore opaque
// to whatever carries it, and everything that matters — the passphrase, the SDP,
// the mod inventory — is inside the sealed blob rather than beside it. A
// messenger that renders link previews shows the recipient "someone shared a mod
// with you" rather than the list of mods and their source URLs.
//
// Three primitives live here, and the rest of the sharing code composes them:
//
//   - DeriveKey turns a passphrase into a 32-byte key. The passphrase is a
//     random 256-bit value, not something a human types, so an iterated KDF
//     would cost start-up time to slow down a brute force that cannot
//     succeed. HMAC with a domain-separation label is used instead: one
//     passphrase yields several unrelated keys, so a key used to seal the link
//     is never the key used to authenticate the data channel.
//   - Seal/Open are AES-256-GCM with a random nonce prepended to the
//     ciphertext. They provide confidentiality and integrity together, so a
//     link that has been edited fails to open rather than opening into an offer
//     the sender did not make.
//   - Compress/Decompress are zstd. The dominant cost in a link is the SDP,
//     which is verbose ASCII with long repeated lines; compressing before
//     encrypting typically takes a 3 KiB offer down to well under a kilobyte,
//     which is the difference between a link that fits in a chat window and one
//     that does not.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// ErrEmptyPassphrase is what Seal and Open report for a passphrase that is not
// there. It is a sentinel rather than a formatted message because there is
// nothing to vary: the passphrase is either a value or it is not.
var ErrEmptyPassphrase = errors.New("empty passphrase")

// ErrMalformed reports a token that is not a sealed blob at all — it is not
// base64url, or it is shorter than a nonce. It is deliberately distinct from the
// failure GCM returns for a blob sealed with a different passphrase, because the
// two mean different things to a user: one is "that is not a link from this
// manager", the other is "that link needs its own passphrase".
var ErrMalformed = errors.New("not a sealed blob")

// Key labels. Every derived key is bound to one of these, so the same
// passphrase can seal a link and authenticate a session without either key
// being able to stand in for the other.
const (
	// LabelEnvelope keys the link blob itself. It is exported because the link
	// codec seals a payload the session key must never be able to open, and
	// that guarantee is only credible if both derivations name the same two
	// labels in the same place.
	LabelEnvelope = "dmm/p2p/envelope/v1"
	// LabelSession keys the handshake MAC over the data channel. It is what
	// proves a peer actually holds the link secret rather than having scraped
	// the offer out of a chat log.
	LabelSession = "dmm/p2p/session/v1"
)

// PassphraseBytes is how much randomness a generated passphrase holds. 32 bytes
// is 256 bits, which is more than an exhaustive search of any keyspace anybody
// will build; the passphrase is the only secret in the link, so it is sized like
// a key rather than like a password.
const PassphraseBytes = 32

// base64URL is the encoding every carrier in the manager uses. It is URL-safe
// and padding-free, so a token survives a copy-paste through anything that
// treats a link as opaque text.
var base64URL = base64.URLEncoding.WithPadding(base64.NoPadding)

// GeneratePassphrase returns a fresh random passphrase of PassphraseBytes.
func GeneratePassphrase() (string, error) {
	return GenerateSecret(PassphraseBytes)
}

// GenerateSecret returns size bytes of randomness as a base64url token.
//
// This is the size-parameterised form of GeneratePassphrase. The share-link
// codec wants a shorter secret than the session key does, because its secret is
// carried inside the link and every extra character is one the recipient has to
// paste; deriving both from one function keeps them in the same encoding and out
// of two hand-rolled randomizers.
func GenerateSecret(size int) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("crypto: secret size %d must be positive", size)
	}

	buf := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}

	return base64URL.EncodeToString(buf), nil
}

// DeriveKey returns the 32-byte key for one labelled use of a passphrase.
//
// HMAC-SHA256 keyed by the passphrase rather than a KDF such as PBKDF2: the
// passphrase is generated, never chosen, so there is no low-entropy guess to
// slow down, and every call is a single hash. The label is mixed in as the
// message, which is what keeps two uses of one passphrase from producing the
// same key.
func DeriveKey(passphrase, label string) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, ErrEmptyPassphrase
	}

	mac := hmac.New(sha256.New, []byte(passphrase))
	mac.Write([]byte(label))

	return mac.Sum(nil), nil
}

// MAC returns the authentication tag a peer sends to prove it holds the
// session key. The parts are concatenated in the order given and separated by a
// byte that cannot occur in a length-prefixed field, so two different groupings
// cannot produce the same input.
func MAC(key []byte, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, key)
	for _, part := range parts {
		mac.Write([]byte{0})
		mac.Write(part)
	}

	return mac.Sum(nil)
}

// Seal encrypts plaintext for a passphrase and returns nonce‖ciphertext.
//
// The nonce is generated here rather than supplied so that two calls with the
// same passphrase and the same plaintext produce different tokens: a link that
// could be recognised by its ciphertext would tell anyone who saw it twice that
// it was the same offer.
func Seal(plaintext []byte, passphrase string) ([]byte, error) {
	gcm, err := newGCM(passphrase, LabelEnvelope)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal. It reports ErrMalformed for anything too short to contain
// a nonce, and GCM's authentication failure for a blob this passphrase did not
// seal.
func Open(sealed []byte, passphrase string) ([]byte, error) {
	gcm, err := newGCM(passphrase, LabelEnvelope)
	if err != nil {
		return nil, err
	}

	if len(sealed) < gcm.NonceSize() {
		return nil, ErrMalformed
	}

	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]

	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// EncodeJSON marshals v, compresses it and seals it, returning the token that
// goes into a share link. Compression happens before encryption because zstd
// only sees the plaintext, and no key material is derived from the compressed
// form.
func EncodeJSON(v any, passphrase string) (string, error) {
	plaintext, err := json.Marshal(v)
	if err != nil {
		return "", err
	}

	packed, err := Compress(plaintext)
	if err != nil {
		return "", err
	}

	sealed, err := Seal(packed, passphrase)
	if err != nil {
		return "", err
	}

	return base64URL.EncodeToString(sealed), nil
}

// DecodeJSON reverses EncodeJSON into v.
func DecodeJSON(token, passphrase string, v any) error {
	sealed, err := base64URL.DecodeString(token)
	if err != nil {
		return ErrMalformed
	}

	packed, err := Open(sealed, passphrase)
	if err != nil {
		return err
	}

	plaintext, err := Decompress(packed)
	if err != nil {
		return err
	}

	return json.Unmarshal(plaintext, v)
}

// Compress returns the zstd form of data.
//
// A shared encoder is used rather than one per call: creating a zstd encoder is
// the expensive part of compressing, and a share link is compressed once per
// file at most. The concurrency-safe encoder is what makes that worth doing.
func Compress(data []byte) ([]byte, error) {
	encoder, err := zstdEncoder()
	if err != nil {
		return nil, err
	}

	return encoder.EncodeAll(data, nil), nil
}

// Decompress reverses Compress, bounding the result.
//
// The bound matters because the input came from a stranger: a few kilobytes of
// zstd can expand to gigabytes, and a decompress that runs out of memory takes
// the whole process with it rather than failing the one link that caused it.
func Decompress(data []byte) ([]byte, error) {
	decoder, err := zstdDecoder()
	if err != nil {
		return nil, err
	}

	return decoder.DecodeAll(data, nil)
}

// MaxInflatedBytes bounds what Decompress will expand to. It is generous next to
// a real share link — the largest thing that legitimately travels this way is an
// SDP offer and an inventory, together well under 100 KiB — while still being
// small enough that a compression bomb costs a bounded amount of memory.
const MaxInflatedBytes = 8 << 20

var (
	// zstdOnce guards the lazily-built codecs below. Building them through one
	// helper keeps the error handling identical to the rest of the package, and
	// a single pair of codecs is enough for every link this process will make.
	zstdOnce    sync.Once
	zstdEnc     *zstd.Encoder
	zstdDec     *zstd.Decoder
	zstdInitErr error
)

func zstdEncoder() (*zstd.Encoder, error) {
	if err := initZstd(); err != nil {
		return nil, err
	}

	return zstdEnc, nil
}

func zstdDecoder() (*zstd.Decoder, error) {
	if err := initZstd(); err != nil {
		return nil, err
	}

	return zstdDec, nil
}

// initZstd builds both codecs once.
//
// SpeedBestCompression rather than the default: a link is compressed once and
// read once, so the time saved by a smaller token is worth more than the time
// spent producing it.
func initZstd() error {
	zstdOnce.Do(func() {
		enc, err := zstd.NewWriter(
			nil,
			zstd.WithEncoderLevel(zstd.SpeedBestCompression),
			zstd.WithEncoderConcurrency(1),
		)
		if err != nil {
			zstdInitErr = err
			return
		}

		dec, err := zstd.NewReader(
			nil,
			zstd.WithDecoderMaxMemory(MaxInflatedBytes),
			zstd.WithDecoderConcurrency(1),
		)
		if err != nil {
			zstdInitErr = err
			return
		}

		zstdEnc = enc
		zstdDec = dec
	})

	return zstdInitErr
}

// newGCM builds the AEAD used for the link envelope under one passphrase.
func newGCM(passphrase, label string) (cipher.AEAD, error) {
	key, err := DeriveKey(passphrase, label)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return cipher.NewGCM(block)
}

// EqualConstantTime reports whether two tags match, without leaking where they
// differ through timing. The handshake compares tags a stranger can choose to
// replay, so a plain bytes.Equal would turn a failed forgery into an oracle.
func EqualConstantTime(a, b []byte) bool {
	return hmac.Equal(a, b)
}
