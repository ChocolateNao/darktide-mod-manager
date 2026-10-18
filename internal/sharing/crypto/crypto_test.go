package crypto_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/crypto"
)

// TestSealOpenRoundTrip covers the ordinary path: what goes in comes out.
func TestSealOpenRoundTrip(t *testing.T) {
	t.Parallel()

	secret, err := crypto.GeneratePassphrase()
	if err != nil {
		t.Fatalf("GeneratePassphrase: %v", err)
	}

	plaintext := []byte("a mod archive, more or less")

	sealed, err := crypto.Seal(plaintext, secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if bytes.Contains(sealed, plaintext) {
		t.Fatal("sealed blob contains the plaintext")
	}

	opened, err := crypto.Open(sealed, secret)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open returned %q, want %q", opened, plaintext)
	}
}

// TestSealIsNotDeterministic covers the same plaintext sealed twice giving two tokens.
func TestSealIsNotDeterministic(t *testing.T) {
	t.Parallel()

	const secret = "a fixed secret"

	first, err := crypto.Seal([]byte("same"), secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	second, err := crypto.Seal([]byte("same"), secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same plaintext produced the same token")
	}
}

// TestOpenWithWrongSecret covers the failure a tampered link produces.
func TestOpenWithWrongSecret(t *testing.T) {
	t.Parallel()

	sealed, err := crypto.Seal([]byte("payload"), "the real secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := crypto.Open(sealed, "a different secret"); err == nil {
		t.Fatal("Open succeeded with the wrong secret")
	}
}

// TestOpenRejectsTamperedBlob covers GCM authentication: one flipped bit fails the open.
func TestOpenRejectsTamperedBlob(t *testing.T) {
	t.Parallel()

	const secret = "the real secret"

	sealed, err := crypto.Seal([]byte("payload that is long enough to matter"), secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01

	if _, err := crypto.Open(tampered, secret); err == nil {
		t.Fatal("Open accepted a blob with a flipped bit")
	}
}

// TestOpenShortBlob covers a truncated link, reported as ErrMalformed rather than a bad secret.
func TestOpenShortBlob(t *testing.T) {
	t.Parallel()

	const secret = "the real secret"

	for _, size := range []int{0, 1, 11} {
		_, err := crypto.Open(make([]byte, size), secret)
		if !errors.Is(err, crypto.ErrMalformed) {
			t.Errorf("Open of a %d-byte blob returned %v, want ErrMalformed", size, err)
		}
	}
}

// TestEmptyPassphrase covers an empty secret failing at derivation, not keying on nothing.
func TestEmptyPassphrase(t *testing.T) {
	t.Parallel()

	if _, err := crypto.Seal([]byte("x"), ""); !errors.Is(err, crypto.ErrEmptyPassphrase) {
		t.Errorf("Seal with an empty secret returned %v, want ErrEmptyPassphrase", err)
	}

	if _, err := crypto.Open([]byte("0123456789abcdef0123456789abcdef"), ""); !errors.Is(
		err,
		crypto.ErrEmptyPassphrase,
	) {
		t.Errorf("Open with an empty secret returned %v, want ErrEmptyPassphrase", err)
	}

	if _, err := crypto.DeriveKey("", crypto.LabelSession); !errors.Is(
		err,
		crypto.ErrEmptyPassphrase,
	) {
		t.Errorf("DeriveKey with an empty secret returned %v, want ErrEmptyPassphrase", err)
	}
}

// TestGenerateSecretRejectsNonPositiveSize covers the guard on a token that would decode empty.
func TestGenerateSecretRejectsNonPositiveSize(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, -1} {
		if _, err := crypto.GenerateSecret(size); err == nil {
			t.Errorf("GenerateSecret(%d) succeeded, want an error", size)
		}
	}
}

// TestDeriveKeySeparatesLabels covers the sealing key not being the session key.
func TestDeriveKeySeparatesLabels(t *testing.T) {
	t.Parallel()

	const secret = "one passphrase"

	envelope, err := crypto.DeriveKey(secret, crypto.LabelEnvelope)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}

	session, err := crypto.DeriveKey(secret, crypto.LabelSession)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}

	if bytes.Equal(envelope, session) {
		t.Fatal("envelope and session keys are identical")
	}

	repeat, err := crypto.DeriveKey(secret, crypto.LabelSession)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}

	if !bytes.Equal(session, repeat) {
		t.Fatal("DeriveKey is not deterministic for one secret and label")
	}
}

// TestMACSeparatesParts covers two groupings of the same bytes not producing one tag.
func TestMACSeparatesParts(t *testing.T) {
	t.Parallel()

	key := []byte("key")

	joined := crypto.MAC(key, []byte("ab"), []byte("c"))
	split := crypto.MAC(key, []byte("a"), []byte("bc"))

	if bytes.Equal(joined, split) {
		t.Fatal("MAC is ambiguous about where one part ends and the next begins")
	}
}

// TestEncodeJSONRoundTrip covers the full link token path.
func TestEncodeJSONRoundTrip(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	secret, err := crypto.GenerateSecret(16)
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}

	want := payload{Name: "offer", Count: 3}

	token, err := crypto.EncodeJSON(want, secret)
	if err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}

	if strings.ContainsAny(token, "+/=") {
		t.Fatalf("token %q is not URL-safe and padding-free", token)
	}

	var got payload

	if err := crypto.DecodeJSON(token, secret, &got); err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}

	if got != want {
		t.Fatalf("DecodeJSON returned %+v, want %+v", got, want)
	}
}

// TestDecodeJSONRejectsBadTokens covers a token that is not base64url, and the wrong secret.
func TestDecodeJSONRejectsBadTokens(t *testing.T) {
	t.Parallel()

	const secret = "the real secret"

	var out map[string]any

	if err := crypto.DecodeJSON("!!! not base64 !!!", secret, &out); !errors.Is(
		err,
		crypto.ErrMalformed,
	) {
		t.Errorf("DecodeJSON of non-base64 returned %v, want ErrMalformed", err)
	}

	token, err := crypto.EncodeJSON(map[string]string{"k": "v"}, "another secret")
	if err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}

	if err := crypto.DecodeJSON(token, secret, &out); err == nil {
		t.Error("DecodeJSON succeeded with the wrong secret")
	}

	notJSON, err := crypto.EncodeJSON("just a string", secret)
	if err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}

	var target struct {
		Field string `json:"field"`
	}

	if err := crypto.DecodeJSON(notJSON, secret, &target); err == nil {
		t.Error("DecodeJSON accepted a payload that does not fit the target")
	}
}

// TestEncodeJSONOmitsUnsetFields covers the size path: a share link is built from compact JSON.
func TestEncodeJSONOmitsUnsetFields(t *testing.T) {
	t.Parallel()

	type sparse struct {
		Set   string `json:"set"`
		Unset string `json:"unset,omitempty"`
	}

	token, err := crypto.EncodeJSON(sparse{Set: "yes"}, "secret")
	if err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}

	sealed, err := base64URLDecode(token)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}

	packed, err := crypto.Open(sealed, "secret")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	plaintext, err := crypto.Decompress(packed)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}

	var round map[string]json.RawMessage

	if err := json.Unmarshal(plaintext, &round); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if _, present := round["unset"]; present {
		t.Error("an empty omitempty field survived encoding")
	}

	if _, present := round["set"]; !present {
		t.Error("a populated field was dropped")
	}
}

// TestDecompressBoundsOutput covers the compression-bomb guard.
func TestDecompressBoundsOutput(t *testing.T) {
	t.Parallel()

	bomb, err := crypto.Compress(make([]byte, crypto.MaxInflatedBytes*2))
	if err != nil {
		t.Skipf("this build's zstd cannot make the bomb: %v", err)
	}

	if _, err := crypto.Decompress(bomb); err == nil {
		t.Fatal("Decompress expanded past its ceiling without complaining")
	}
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
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	if len(packed) >= len(original)/4 {
		t.Errorf("repetitive input compressed to %d bytes from %d", len(packed), len(original))
	}

	unpacked, err := crypto.Decompress(packed)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}

	if !bytes.Equal(unpacked, original) {
		t.Fatal("Decompress did not return the original bytes")
	}
}

// TestEqualConstantTime covers the handshake's tag comparison, including differing lengths.
func TestEqualConstantTime(t *testing.T) {
	t.Parallel()

	a := []byte("0123456789abcdef0123456789abcdef")

	if !crypto.EqualConstantTime(a, bytes.Clone(a)) {
		t.Error("identical digests compared unequal")
	}

	if crypto.EqualConstantTime(a, []byte("short")) {
		t.Error("digests of different lengths compared equal")
	}

	flipped := bytes.Clone(a)
	flipped[0] ^= 0xff

	if crypto.EqualConstantTime(a, flipped) {
		t.Error("differing digests compared equal")
	}
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
		t.Errorf("concurrent use: %v", err)
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
