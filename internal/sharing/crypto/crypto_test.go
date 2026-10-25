package crypto_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSealOpenRoundTrip covers the ordinary path: what goes in comes out.
func TestSealOpenRoundTrip(t *testing.T) {
	t.Parallel()

	plaintext := []byte("a mod archive, more or less")

	secret, err := crypto.GeneratePassphrase()
	require.NoError(t, err, "GeneratePassphrase: %v", err)

	sealed, err := crypto.Seal(plaintext, secret)
	require.NoError(t, err, "Seal: %v", err)

	assert.False(t, bytes.Contains(sealed, plaintext), "sealed blob contains the plaintext")

	opened, err := crypto.Open(sealed, secret)
	require.NoError(t, err, "Open: %v", err)

	assert.Equal(t, plaintext, opened, "Open returned %q, want %q", opened, plaintext)
}

// TestSealIsNotDeterministic covers the same plaintext sealed twice giving two tokens.
func TestSealIsNotDeterministic(t *testing.T) {
	t.Parallel()

	const secret = "a fixed secret"

	first, err := crypto.Seal([]byte("same"), secret)
	require.NoError(t, err, "Seal: %v", err)

	second, err := crypto.Seal([]byte("same"), secret)
	require.NoError(t, err, "Seal: %v", err)

	assert.NotEqual(t, first, second, "two seals of the same plaintext produced the same token")
}

// TestOpenWithWrongSecret covers the failure a tampered link produces.
func TestOpenWithWrongSecret(t *testing.T) {
	t.Parallel()

	sealed, err := crypto.Seal([]byte("payload"), "the real secret")
	require.NoError(t, err, "Seal: %v", err)

	_, err = crypto.Open(sealed, "a different secret")
	require.Error(t, err, "Open succeeded with the wrong secret")
}

// TestOpenRejectsTamperedBlob covers GCM authentication: one flipped bit fails the open.
func TestOpenRejectsTamperedBlob(t *testing.T) {
	t.Parallel()

	const secret = "the real secret"

	sealed, err := crypto.Seal([]byte("payload that is long enough to matter"), secret)
	require.NoError(t, err, "Seal: %v", err)

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01

	_, err = crypto.Open(tampered, secret)
	require.Error(t, err, "Open accepted a blob with a flipped bit")
}

// TestOpenShortBlob covers a truncated link, reported as ErrMalformed: not a bad secret.
func TestOpenShortBlob(t *testing.T) {
	t.Parallel()

	const secret = "the real secret"

	for _, size := range []int{0, 1, 11} {
		_, err := crypto.Open(make([]byte, size), secret)
		assert.ErrorIs(t, err, crypto.ErrMalformed, "Open of a %d-byte blob returned %v, want ErrMalformed", size, err)
	}
}

// TestEmptyPassphrase covers an empty secret failing at derivation, not keying on nothing.
func TestEmptyPassphrase(t *testing.T) {
	t.Parallel()

	_, err := crypto.Seal([]byte("x"), "")
	assert.ErrorIs(t, err, crypto.ErrEmptyPassphrase, "Seal with an empty secret returned %v, want ErrEmptyPassphrase", err)

	_, err = crypto.Open([]byte("0123456789abcdef0123456789abcdef"), "")
	assert.ErrorIs(t, err, crypto.ErrEmptyPassphrase, "Open with an empty secret returned %v, want ErrEmptyPassphrase", err)

	_, err = crypto.DeriveKey("", crypto.LabelSession)
	assert.ErrorIs(t, err, crypto.ErrEmptyPassphrase, "DeriveKey with an empty secret returned %v, want ErrEmptyPassphrase", err)
}

// TestGenerateSecretRejectsNonPositiveSize covers the guard on a token that would decode empty.
func TestGenerateSecretRejectsNonPositiveSize(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, -1} {
		_, err := crypto.GenerateSecret(size)
		assert.Error(t, err, "GenerateSecret(%d) succeeded, want an error", size)
	}
}

// TestDeriveKeySeparatesLabels covers the sealing key not being the session key.
func TestDeriveKeySeparatesLabels(t *testing.T) {
	t.Parallel()

	const secret = "one passphrase"

	envelope, err := crypto.DeriveKey(secret, crypto.LabelEnvelope)
	require.NoError(t, err, "DeriveKey: %v", err)

	session, err := crypto.DeriveKey(secret, crypto.LabelSession)
	require.NoError(t, err, "DeriveKey: %v", err)

	assert.NotEqual(t, envelope, session, "envelope and session keys are identical")

	repeat, err := crypto.DeriveKey(secret, crypto.LabelSession)
	require.NoError(t, err, "DeriveKey: %v", err)

	assert.Equal(t, session, repeat, "DeriveKey is not deterministic for one secret and label")
}

// TestMACSeparatesParts covers two groupings of the same bytes not producing one tag.
func TestMACSeparatesParts(t *testing.T) {
	t.Parallel()

	key := []byte("key")

	joined := crypto.MAC(key, []byte("ab"), []byte("c"))
	split := crypto.MAC(key, []byte("a"), []byte("bc"))

	assert.NotEqual(t, joined, split, "MAC is ambiguous about where one part ends and the next begins")
}

// TestEncodeJSONRoundTrip covers the full link token path.
func TestEncodeJSONRoundTrip(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	secret, err := crypto.GenerateSecret(16)
	require.NoError(t, err, "GenerateSecret: %v", err)

	want := payload{Name: "offer", Count: 3}

	token, err := crypto.EncodeJSON(want, secret)
	require.NoError(t, err, "EncodeJSON: %v", err)

	assert.False(t, strings.ContainsAny(token, "+/="), "token %q is not URL-safe and padding-free", token)

	var got payload

	err = crypto.DecodeJSON(token, secret, &got)
	require.NoError(t, err, "DecodeJSON: %v", err)

	assert.Equal(t, want, got, "DecodeJSON returned %+v, want %+v", got, want)
}

// TestDecodeJSONRejectsBadTokens covers a token that is not base64url, and the wrong secret.
func TestDecodeJSONRejectsBadTokens(t *testing.T) {
	t.Parallel()

	const secret = "the real secret"

	var out map[string]any

	err := crypto.DecodeJSON("!!! not base64 !!!", secret, &out)
	assert.ErrorIs(t, err, crypto.ErrMalformed, "DecodeJSON of non-base64 returned %v, want ErrMalformed", err)

	token, err := crypto.EncodeJSON(map[string]string{"k": "v"}, "another secret")
	require.NoError(t, err, "EncodeJSON: %v", err)

	err = crypto.DecodeJSON(token, secret, &out)
	assert.Error(t, err, "DecodeJSON succeeded with the wrong secret")

	notJSON, err := crypto.EncodeJSON("just a string", secret)
	require.NoError(t, err, "EncodeJSON: %v", err)

	var target struct {
		Field string `json:"field"`
	}

	err = crypto.DecodeJSON(notJSON, secret, &target)
	assert.Error(t, err, "DecodeJSON accepted a payload that does not fit the target")
}

// TestEncodeJSONOmitsUnsetFields covers the size path: a share link is built from compact JSON.
func TestEncodeJSONOmitsUnsetFields(t *testing.T) {
	t.Parallel()

	type sparse struct {
		Set   string `json:"set"`
		Unset string `json:"unset,omitempty"`
	}

	token, err := crypto.EncodeJSON(sparse{Set: "yes"}, "secret")
	require.NoError(t, err, "EncodeJSON: %v", err)

	sealed, err := base64URLDecode(token)
	require.NoError(t, err, "decode token: %v", err)

	packed, err := crypto.Open(sealed, "secret")
	require.NoError(t, err, "Open: %v", err)

	plaintext, err := crypto.Decompress(packed)
	require.NoError(t, err, "Decompress: %v", err)

	var round map[string]json.RawMessage

	require.NoError(t, json.Unmarshal(plaintext, &round), "Unmarshal: %v", err)

	assert.NotContains(t, round, "unset", "an empty omitempty field survived encoding")

	assert.Contains(t, round, "set", "a populated field was dropped")
}

// TestDecompressBoundsOutput covers the compression-bomb guard.
func TestDecompressBoundsOutput(t *testing.T) {
	t.Parallel()

	bomb, err := crypto.Compress(make([]byte, crypto.MaxInflatedBytes*2))
	if err != nil {
		t.Skipf("this build's zstd cannot make the bomb: %v", err)
	}

	_, err = crypto.Decompress(bomb)
	require.Error(t, err, "Decompress expanded past its ceiling without complaining")

}

// TestCompressRoundTrip covers the usual input: verbose, repetitive ASCII.
func TestCompressRoundTrip(t *testing.T) {
	t.Parallel()

	var builder strings.Builder

	for i := 0; i < 200; i++ {
		builder.WriteString("a=candidate:1 1 udp 2113937151 192.168.1.2 54321 typ host\r\n")
	}

	original := []byte(builder.String())

	packed, err := crypto.Compress(original)
	require.NoError(t, err, "Compress: %v", err)

	assert.Less(t, len(packed), len(original)/4, "repetitive input compressed to %d bytes from %d", len(packed), len(original))

	unpacked, err := crypto.Decompress(packed)
	require.NoError(t, err, "Decompress: %v", err)

	assert.Equal(t, original, unpacked, "Decompress did not return the original bytes")
}

// TestEqualConstantTime covers the handshake's tag comparison, including differing lengths.
func TestEqualConstantTime(t *testing.T) {
	t.Parallel()

	a := []byte("0123456789abcdef0123456789abcdef")

	assert.True(t, crypto.EqualConstantTime(a, bytes.Clone(a)), "identical digests compared unequal")

	assert.False(t, crypto.EqualConstantTime(a, []byte("short")), "digests of different lengths compared equal")

	flipped := bytes.Clone(a)
	flipped[0] ^= 0xff

	assert.False(t, crypto.EqualConstantTime(a, flipped), "differing digests compared equal")
}

// TestConcurrentUse covers the shared zstd codecs, which the manager hits whenever shares overlap.
func TestConcurrentUse(t *testing.T) {
	t.Parallel()

	const workers = 16

	var wg sync.WaitGroup

	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			token, err := crypto.EncodeJSON(map[string]int{"n": n}, "secret")
			if err != nil {
				errs <- err

				return
			}

			var out map[string]int

			if err := crypto.DecodeJSON(token, "secret", &out); err != nil {
				errs <- err

				return
			}

			if out["n"] != n {
				errs <- errors.New("decoded the wrong value")
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		assert.Error(t, err, "concurrent use: %v", err)
	}
}

// base64URLDecode mirrors EncodeJSON's encoding so a test can look inside a token.
func base64URLDecode(token string) ([]byte, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	lookup := make(map[byte]byte, len(alphabet))

	for i := 0; i < len(alphabet); i++ {
		lookup[alphabet[i]] = byte(i)
	}

	var out []byte

	var buffer, bits int

	for i := 0; i < len(token); i++ {
		value, ok := lookup[token[i]]
		if !ok {
			return nil, errors.New("not base64url")
		}

		buffer = buffer<<6 | int(value)
		bits += 6

		if bits >= 8 {
			bits -= 8
			out = append(out, byte(buffer>>bits))
		}
	}

	return out, nil
}
