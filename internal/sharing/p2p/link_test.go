package p2p_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/crypto"
	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/p2p"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleOffer() domain.P2PShareOffer {
	deadline := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)

	return domain.P2PShareOffer{
		OfferID:    "0f0f0f0f-0000-4000-8000-000000000000",
		GameID:     "darktide",
		CreatedAt:  time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC),
		ExpiresAt:  &deadline,
		SenderName: "scout",
		Mods:       []domain.SharedMod{{Name: "Sharpshooter", Version: "1.2.0"}},
		Files: []domain.P2PSharedFile{{
			FileID: "sharpshooter", Name: "sharpshooter.zip", ModID: "sharpshooter",
			Size: 4096, HashSHA256: strings.Repeat("ab", 32),
		}},
		SourceLinks: []domain.ModSourceLink{{
			ModID: "sharpshooter", SourceID: "nexus",
			SourceURL: "https://www.nexusmods.com/darktide/mods/1",
		}},
		STUNServers: []string{"stun:stun.l.google.com:19302"},
	}
}

// secretFor takes a link secret from a real link, so the tests cannot drift from LinkOffer's shape.
func secretFor(t *testing.T) string {
	t.Helper()

	secret, err := linkSecret()
	require.NoError(t, err, "link secret: %v", err)

	return secret
}

// TestLinkOfferRoundTrips covers a link surviving a chat client: no whitespace, pasteable size.
func TestLinkOfferRoundTrips(t *testing.T) {
	t.Parallel()

	offer := sampleOffer()
	sdp := "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n"

	link, err := p2p.LinkOffer(offer, sdp)
	require.NoError(t, err, "link: %v", err)

	assert.True(t, p2p.IsOfferLink(link.Link), "the link does not announce itself as an offer: %q", link.Link)

	assert.False(t, p2p.IsAnswerLink(link.Link), "an offer link was mistaken for an answer link")

	assert.False(t, strings.ContainsAny(link.Link, " \t\n\r"), "the link contains whitespace")

	assert.Equal(t, offer.OfferID, link.Offer.OfferID, "the link did not keep the offer it was given: %+v", link.Offer)
	assert.Equal(t, offer.GameID, link.Offer.GameID, "the link did not keep the offer it was given: %+v", link.Offer)

	assert.Equal(t, sdp, link.SDP, "sdp = %q", link.SDP)

	opened, err := p2p.OpenOffer(link.Link)
	require.NoError(t, err, "open: %v", err)

	assert.Equal(t, offer.OfferID, opened.Offer.OfferID, "offer = %+v", opened.Offer)
	assert.Equal(t, offer.SenderName, opened.Offer.SenderName, "offer = %+v", opened.Offer)

	assert.True(
		t,
		opened.Offer.ExpiresAt != nil && opened.Offer.ExpiresAt.Equal(*offer.ExpiresAt),
		"expiry = %v",
		opened.Offer.ExpiresAt,
	)

	assert.True(
		t,
		len(opened.Offer.Files) == 1 && opened.Offer.Files[0].HashSHA256 == offer.Files[0].HashSHA256,
		"files = %+v",
		opened.Offer.Files,
	)

	assert.True(
		t,
		len(opened.Offer.SourceLinks) == 1 && opened.Offer.SourceLinks[0].SourceURL == offer.SourceLinks[0].SourceURL,
		"source links = %+v",
		opened.Offer.SourceLinks,
	)

	assert.Equal(t, link.Secret, opened.Secret, "the round trip returned a different secret")
}

// TestLinkOfferHidesTheOffer covers a chat preview or a relaying server reading nothing inside.
func TestLinkOfferHidesTheOffer(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	require.NoError(t, err, "link: %v", err)

	for _, secret := range []string{"nexusmods.com", "sharpshooter", "darktide", "scout"} {
		assert.NotContains(t, link.Link, secret, "the link leaks %q in the clear", secret)
	}
}

// TestLinkOfferWithoutAnExpiryIsPermanent covers absence surviving the round trip, not a zero time.
func TestLinkOfferWithoutAnExpiryIsPermanent(t *testing.T) {
	t.Parallel()

	offer := sampleOffer()
	offer.ExpiresAt = nil

	link, err := p2p.LinkOffer(offer, "v=0")
	require.NoError(t, err, "link: %v", err)

	assert.Nil(t, link.ExpiresAt(), "ExpiresAt = %v, want nil", link.ExpiresAt())

	assert.False(t, link.IsExpired(time.Now()), "a permanent link reported itself as expired")

	opened, err := p2p.OpenOffer(link.Link)
	require.NoError(t, err, "open: %v", err)

	assert.Nil(t, opened.Offer.ExpiresAt, "the round trip invented an expiry: %v", opened.Offer.ExpiresAt)
}

func TestLinkIsExpired(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	offer := sampleOffer()
	offer.ExpiresAt = &deadline

	link, err := p2p.LinkOffer(offer, "v=0")
	require.NoError(t, err, "link: %v", err)

	assert.False(t, link.IsExpired(deadline.Add(-time.Second)), "the link expired a second early")

	assert.True(t, link.IsExpired(deadline), "the link survived its own deadline")

	assert.True(t, link.IsExpired(deadline.Add(time.Hour)), "the link survived an hour past its deadline")
}

func TestOpenOfferRefusesAnAnswer(t *testing.T) {
	t.Parallel()

	answer, err := p2p.LinkAnswer("offer-1", "v=0", secretFor(t), "medic")
	require.NoError(t, err, "answer: %v", err)

	_, err = p2p.OpenOffer(answer.Link)
	require.Error(t, err, "an answer link opened as an offer")
}

func TestOpenAnswerRefusesAnOffer(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	require.NoError(t, err, "link: %v", err)

	_, err = p2p.OpenAnswer(link.Link)
	require.Error(t, err, "an offer link opened as an answer")
}

func TestLinkAnswerRoundTrips(t *testing.T) {
	t.Parallel()

	secret := secretFor(t)

	answer, err := p2p.LinkAnswer("offer-1", "v=0\r\n", secret, "medic")
	require.NoError(t, err, "answer: %v", err)

	assert.True(t, p2p.IsAnswerLink(answer.Link), "the link does not announce itself as an answer: %q", answer.Link)

	assert.Equal(t, "offer-1", answer.OfferID, "answer = %+v", answer)
	assert.Equal(t, secret, answer.Secret, "answer = %+v", answer)

	opened, err := p2p.OpenAnswer(answer.Link)
	require.NoError(t, err, "open: %v", err)

	assert.Equal(t, "offer-1", opened.OfferID, "payload = %+v", opened)
	assert.Equal(t, "medic", opened.ReceiverName, "payload = %+v", opened)

	assert.Equal(t, "v=0\r\n", opened.SDP, "sdp = %q", opened.SDP)
}

func TestLinkAnswerNeedsAnOfferID(t *testing.T) {
	t.Parallel()

	_, err := p2p.LinkAnswer("", "v=0", secretFor(t), "medic")
	require.Error(t, err, "an answer with no offer id was accepted")

	_, err = p2p.LinkAnswer("offer-1", "v=0", "", "medic")
	require.Error(t, err, "an answer with no secret was accepted")
}

// TestLinkOfferGeneratesAFreshSecret covers two links for one offer not being interchangeable.
func TestLinkOfferGeneratesAFreshSecret(t *testing.T) {
	t.Parallel()

	offer := sampleOffer()

	first, err := p2p.LinkOffer(offer, "v=0")
	require.NoError(t, err, "first: %v", err)

	second, err := p2p.LinkOffer(offer, "v=0")
	require.NoError(t, err, "second: %v", err)

	assert.NotEqual(t, first.Secret, second.Secret, "two links for one offer share a secret")

	assert.NotEqual(t, first.Link, second.Link, "two links for one offer are identical")
}

func TestOpenOfferRefusesGarbage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "not a link at all", text: "https://example.test/mods"},
		{name: "prefix only", text: p2p.OfferLinkPrefix},
		{name: "prefix with no payload", text: p2p.OfferLinkPrefix + "abcd"},
		{name: "secret with no token", text: p2p.OfferLinkPrefix + "abcd"},
		{name: "token is not base64", text: p2p.OfferLinkPrefix + "abcd/!!!!"},
		{name: "token is too short for a nonce", text: p2p.OfferLinkPrefix + "abcd/AAAA"},
		{name: "truncated ciphertext", text: p2p.OfferLinkPrefix + "abcd/AAAAAAAA"},
		{name: "extra path segment", text: p2p.OfferLinkPrefix + "abcd/AAAAAAAA/BBBB"},
		{name: "wrong scheme", text: "dmm://p2px/v1/abcd/AAAAAAAA"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p2p.OpenOffer(tc.text)
			require.Error(t, err, "%q was accepted", tc.text)
		})
	}
}

// TestOpenOfferRefusesAWrongSecret covers another secret's payload failing, not decrypting.
func TestOpenOfferRefusesAWrongSecret(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	require.NoError(t, err, "link: %v", err)

	other, err := crypto.GenerateSecret(8)
	require.NoError(t, err, "generate secret: %v", err)

	// Swap in a different secret segment, keeping the sealed token intact.
	forged := p2p.OfferLinkPrefix + other + "/" + strings.SplitN(link.Link, "/", 6)[5]

	_, err = p2p.OpenOffer(forged)
	require.Error(t, err, "a link sealed under another secret was accepted")
}

// TestOpenOfferRefusesATamperedPayload covers one flipped ciphertext bit being detected.
func TestOpenOfferRefusesATamperedPayload(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	require.NoError(t, err, "link: %v", err)

	token := []byte(strings.SplitN(link.Link, "/", 6)[5])
	token[len(token)-1] ^= 0x01

	forged := p2p.OfferLinkPrefix + link.Secret + "/" + string(token)

	_, err = p2p.OpenOffer(forged)
	require.Error(t, err, "a tampered link was accepted")
}

func TestOpenOfferRefusesAnUnknownProtocolVersion(t *testing.T) {
	t.Parallel()

	secret := secretFor(t)

	// Seal a payload that claims a future version, using the same envelope key.
	sealed, err := crypto.EncodeJSON(map[string]any{
		"k": string(domain.P2PLinkKindOffer),
		"v": int(domain.P2PProtocolVersion) + 1,
		"s": "v=0",
	}, secret)
	require.NoError(t, err, "encode: %v", err)

	_, err = p2p.OpenOffer(p2p.OfferLinkPrefix + secret + "/" + sealed)
	require.Error(t, err, "a link from a future protocol version was accepted")
}

// TestOpenAnswerRefusesAnOfferPayload covers an offer payload sealed under the answer scheme.
func TestOpenAnswerRefusesAnOfferPayload(t *testing.T) {
	t.Parallel()

	secret := secretFor(t)

	sealed, err := crypto.EncodeJSON(map[string]any{
		"k": string(domain.P2PLinkKindOffer),
		"v": int(domain.P2PProtocolVersion),
		"s": "v=0",
		"o": map[string]any{"offer_id": "x", "game_id": "darktide"},
	}, secret)
	require.NoError(t, err, "encode: %v", err)

	_, err = p2p.OpenAnswer(p2p.AnswerLinkPrefix + secret + "/" + sealed)
	require.Error(t, err, "an offer payload opened as an answer")
}

// TestSessionKeyIsNotTheEnvelopeKey covers opening an envelope without forging a handshake.
func TestSessionKeyIsNotTheEnvelopeKey(t *testing.T) {
	t.Parallel()

	secret := secretFor(t)

	sessionKey, err := p2p.SessionKeyFor(secret)
	require.NoError(t, err, "session key: %v", err)

	assert.Len(t, sessionKey, 32, "session key is %d bytes", len(sessionKey))

	assert.NotEqual(t, secret, string(sessionKey), "the session key is the secret")

	other, err := p2p.SessionKeyFor(secretFor(t))
	require.NoError(t, err, "other session key: %v", err)

	assert.NotEqual(t, string(other), string(sessionKey), "two secrets produced the same session key")
}

// TestIsOfferLinkIsCaseInsensitive covers the scheme alone changing case.
func TestIsOfferLinkIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	require.NoError(t, err, "link: %v", err)

	shouted := strings.ToUpper(
		p2p.OfferLinkPrefix,
	) + strings.TrimPrefix(
		link.Link,
		p2p.OfferLinkPrefix,
	)

	assert.True(t, p2p.IsOfferLink(shouted), "a link with an upper-cased scheme was not recognised")

	_, err = p2p.OpenOffer(shouted)
	require.NoError(t, err, "open shouted: %v", err)
}

// TestOpenOfferRefusesAnUpperCasedPayload covers base64url staying case-sensitive.
func TestOpenOfferRefusesAnUpperCasedPayload(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	require.NoError(t, err, "link: %v", err)

	_, err = p2p.OpenOffer(strings.ToUpper(link.Link))
	require.Error(t, err, "an upper-cased payload was accepted")
}

// TestOpenOfferRefusesAnOversizedLink covers the ceiling on what a paste may allocate.
func TestOpenOfferRefusesAnOversizedLink(t *testing.T) {
	t.Parallel()

	huge := p2p.OfferLinkPrefix + secretFor(t) + "/" + strings.Repeat("A", 2<<20)

	_, err := p2p.OpenOffer(huge)
	require.Error(t, err, "an oversized link was accepted")
}
