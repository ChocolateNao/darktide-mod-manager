// Package crypto is the link codec for peer-to-peer sharing.
//
// A share link travels through places the manager does not control: a chat
// window, a mail client, a terminal paste. Everything that matters — the
// passphrase, the SDP, the mod inventory — is therefore inside the sealed blob
// rather than beside it, so a messenger that unfurls the link shows an opaque
// token instead of a list of mods and their source URLs.
//
// Three primitives live here and the rest of the sharing code composes them:
// DeriveKey turns a passphrase into a labelled 32-byte key, Seal/Open are
// AES-256-GCM with a random nonce prepended to the ciphertext, and
// Compress/Decompress are zstd, which is what keeps a link full of verbose SDP
// inside a chat window.
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

// ErrEmptyPassphrase reports a passphrase that is not there. It is a sentinel
// rather than a formatted message because there is nothing to vary.
var ErrEmptyPassphrase = errors.New("empty passphrase")

// ErrMalformed reports text that is not a sealed blob at all. It is deliberately
// distinct from the failure GCM returns for a blob sealed with a different
// passphrase, because the two mean different things to a user: one is "that is
// not a link from this manager", the other is "that link needs its own passphrase".
var ErrMalformed = errors.New("not a sealed blob")

// Key labels. Binding every derived key to one of these is what keeps a key used
// to seal a link from being able to authenticate a session, or the other way round.
const (
	// LabelEnvelope keys the link blob itself. It is exported so that both
	// derivations name the same two labels in the same place rather than by
	// convention.
	LabelEnvelope = "dmm/p2p/envelope/v1"
	// LabelSession keys the handshake MAC, which is what proves a peer holds the
	// link secret rather than having scraped the offer out of a chat log.
	LabelSession = "dmm/p2p/session/v1"
)

// PassphraseBytes is how much randomness a generated passphrase holds. The
// passphrase is the only secret in the link, so it is sized like a key rather
// than like a password.
const PassphraseBytes = 32

// base64URL is padding-free, so a token survives a copy-paste through anything
// that treats a link as opaque text.
var base64URL = base64.URLEncoding.WithPadding(base64.NoPadding)

// GeneratePassphrase returns a fresh random passphrase of PassphraseBytes.
func GeneratePassphrase() (string, error) {
	return GenerateSecret(PassphraseBytes)
}

// GenerateSecret returns size bytes of randomness as a base64url token. The
// share-link codec wants a shorter secret than the session key does, because its
// secret is pasted; one function keeps both in the same encoding.
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
// HMAC-SHA256 keyed by the passphrase rather than a KDF, because the passphrase
// is generated and never chosen, so there is no low-entropy guess to slow down;
// the label is mixed in as the message, which is what keeps two uses of one
// passphrase from producing the same key.
func DeriveKey(passphrase, label string) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, ErrEmptyPassphrase
	}

	mac := hmac.New(sha256.New, []byte(passphrase))
	mac.Write([]byte(label))

	return mac.Sum(nil), nil
}

// MAC returns the authentication tag a peer sends to prove it holds the session
// key. The parts are separated by a byte that cannot occur in a length-prefixed
// field, so two different groupings of the same bytes cannot produce one input.
func MAC(key []byte, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, key)
	for _, part := range parts {
		mac.Write([]byte{0})
		mac.Write(part)
	}

	return mac.Sum(nil)
}

// Seal encrypts plaintext for a passphrase and returns nonce‖ciphertext.
// The nonce is generated here rather than supplied, so two calls with the same
// passphrase and plaintext produce different tokens: a link recognisable by its
// ciphertext would tell anyone who saw it twice that it was the same offer.
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
// only ever sees the plaintext.
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

// Compress returns the zstd form of data. One shared encoder rather than one per
// call: building a zstd encoder is the expensive part of compressing, and the
// concurrency-safe encoder is what makes sharing it worthwhile.
func Compress(data []byte) ([]byte, error) {
	encoder, err := zstdEncoder()
	if err != nil {
		return nil, err
	}

	return encoder.EncodeAll(data, nil), nil
}

// Decompress reverses Compress, bounding the result. The bound matters because
// the input came from a stranger: a few kilobytes of zstd can expand to
// gigabytes, and a decompress that runs out of memory takes the whole process
// with it rather than failing the one link that caused it.
func Decompress(data []byte) ([]byte, error) {
	decoder, err := zstdDecoder()
	if err != nil {
		return nil, err
	}

	return decoder.DecodeAll(data, nil)
}

// MaxInflatedBytes bounds what Decompress will expand to. It is generous next to
// a real share link — the largest thing that legitimately travels this way is an
// SDP offer and an inventory, well under 100 KiB — while still capping what a
// compression bomb can cost.
const MaxInflatedBytes = 8 << 20

var (
	// zstdOnce guards the lazily-built codecs below; one pair serves every link this
	// process will make.
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

// initZstd builds both codecs once. SpeedBestCompression rather than the
// default because a link is compressed once and read once, so a smaller token is
// worth more than the time spent producing it.
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
