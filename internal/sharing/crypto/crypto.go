// Package crypto is the link codec for peer-to-peer sharing.
//
// A share link travels through places the manager does not control: a chat
// window, a mail client, a terminal paste. The passphrase, SDP and mod inventory
// are all inside the sealed blob, so a messenger that unfurls the link shows an
// opaque token.
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
// because the message has nothing to vary.
var ErrEmptyPassphrase = errors.New("empty passphrase")

// ErrMalformed reports text that is not a sealed blob, distinct from the GCM
// failure a different passphrase produces: one means "that is not a link from
// this manager", the other "that link needs its own passphrase".
var ErrMalformed = errors.New("not a sealed blob")

// Key labels. Binding every derived key to one of these keeps a key used to
// seal a link from authenticating a session, or the other way round.
const (
	// LabelEnvelope keys the link blob itself. It is exported so both
	// derivations share the label.
	LabelEnvelope = "dmm/p2p/envelope/v1"
	// LabelSession keys the handshake MAC, proving a peer holds the link secret.
	LabelSession = "dmm/p2p/session/v1"
)

// PassphraseBytes is how much randomness a generated passphrase holds. It is the
// only secret in the link, so it is sized like a key.
const PassphraseBytes = 32

// base64URL is padding-free, so a token survives a copy-paste through anything
// that treats a link as opaque text.
var base64URL = base64.URLEncoding.WithPadding(base64.NoPadding)

// GeneratePassphrase returns a fresh random passphrase of PassphraseBytes.
func GeneratePassphrase() (string, error) {
	return GenerateSecret(PassphraseBytes)
}

// GenerateSecret returns size bytes of randomness as a base64url token; one
// function keeps the link secret and the session key in the same encoding.
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
// HMAC-SHA256 keyed by the passphrase suffices because the passphrase is
// generated, never chosen; the label is the message, keeping uses distinct.
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
// field, so two groupings of the same bytes cannot produce one input.
func MAC(key []byte, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, key)
	for _, part := range parts {
		mac.Write([]byte{0})
		mac.Write(part)
	}

	return mac.Sum(nil)
}

// Seal encrypts plaintext for a passphrase and returns nonce‖ciphertext. The
// nonce is generated here so two calls produce different tokens: a link
// recognisable by its ciphertext reveals it is the same offer.
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
// goes into a share link. Compression happens before encryption.
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

// Compress returns the zstd form of data. One shared encoder serves every call:
// building a zstd encoder is the expensive part of compressing.
func Compress(data []byte) ([]byte, error) {
	encoder, err := zstdEncoder()
	if err != nil {
		return nil, err
	}

	return encoder.EncodeAll(data, nil), nil
}

// Decompress reverses Compress, bounding the result. The bound matters because
// the input came from a stranger: a few kilobytes of zstd can expand to
// gigabytes, and an out-of-memory decompress takes the whole process with it.
func Decompress(data []byte) ([]byte, error) {
	decoder, err := zstdDecoder()
	if err != nil {
		return nil, err
	}

	return decoder.DecodeAll(data, nil)
}

// MaxInflatedBytes bounds what Decompress will expand to: generous next to a
// real share link, which stays well under 100 KiB, while still capping a
// compression bomb.
const MaxInflatedBytes = 8 << 20

var (
	// zstdOnce guards the lazily-built codecs below; one pair serves every link.
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

// initZstd builds both codecs once. SpeedBestCompression, because a link is
// compressed once and read once, so a smaller token is worth the extra time.
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
// differ through timing; a plain bytes.Equal would turn a failed forgery into an
// oracle.
func EqualConstantTime(a, b []byte) bool {
	return hmac.Equal(a, b)
}
