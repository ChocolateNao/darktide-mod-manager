package p2p_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/crypto"
	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/p2p"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
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

// secretFor is a fresh link secret, taken from a real link so the tests cannot
// drift away from the shape LinkOffer actually produces.
func secretFor(t *testing.T) string {
	t.Helper()

	secret, err := linkSecret()
	if err != nil {
		t.Fatalf("link secret: %v", err)
	}

	return secret
}

// The link has to survive a chat client. That means: no whitespace, no shell
// metacharacters, and a size a person can still paste.
func TestLinkOfferRoundTrips(t *testing.T) {
	t.Parallel()

	offer := sampleOffer()
	sdp := "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n"

	link, err := p2p.LinkOffer(offer, sdp)
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	if !p2p.IsOfferLink(link.Link) {
		t.Fatalf("the link does not announce itself as an offer: %q", link.Link)
	}

	if p2p.IsAnswerLink(link.Link) {
		t.Fatal("an offer link was mistaken for an answer link")
	}

	if strings.ContainsAny(link.Link, " \t\n\r") {
		t.Fatal("the link contains whitespace")
	}

	if link.Offer.OfferID != offer.OfferID || link.Offer.GameID != offer.GameID {
		t.Fatalf("the link did not keep the offer it was given: %+v", link.Offer)
	}

	if link.SDP != sdp {
		t.Fatalf("sdp = %q", link.SDP)
	}

	opened, err := p2p.OpenOffer(link.Link)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if opened.Offer.OfferID != offer.OfferID || opened.Offer.SenderName != offer.SenderName {
		t.Fatalf("offer = %+v", opened.Offer)
	}

	if opened.Offer.ExpiresAt == nil || !opened.Offer.ExpiresAt.Equal(*offer.ExpiresAt) {
		t.Fatalf("expiry = %v", opened.Offer.ExpiresAt)
	}

	if len(opened.Offer.Files) != 1 ||
		opened.Offer.Files[0].HashSHA256 != offer.Files[0].HashSHA256 {
		t.Fatalf("files = %+v", opened.Offer.Files)
	}

	if len(opened.Offer.SourceLinks) != 1 ||
		opened.Offer.SourceLinks[0].SourceURL != offer.SourceLinks[0].SourceURL {
		t.Fatalf("source links = %+v", opened.Offer.SourceLinks)
	}

	if opened.Secret != link.Secret {
		t.Fatal("the round trip returned a different secret")
	}
}

// The whole point of sealing the offer is that a chat preview — or a curious
// server that relays the link — cannot read what is inside it.
func TestLinkOfferHidesTheOffer(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	for _, secret := range []string{"nexusmods.com", "sharpshooter", "darktide", "scout"} {
		if strings.Contains(link.Link, secret) {
			t.Fatalf("the link leaks %q in the clear", secret)
		}
	}
}

// A permanent share has no expiry at all, which has to survive as absence
// rather than as a zero timestamp that reads as 1 January year one.
func TestLinkOfferWithoutAnExpiryIsPermanent(t *testing.T) {
	t.Parallel()

	offer := sampleOffer()
	offer.ExpiresAt = nil

	link, err := p2p.LinkOffer(offer, "v=0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	if link.ExpiresAt() != nil {
		t.Fatalf("ExpiresAt = %v, want nil", link.ExpiresAt())
	}

	if link.IsExpired(time.Now()) {
		t.Fatal("a permanent link reported itself as expired")
	}

	opened, err := p2p.OpenOffer(link.Link)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if opened.Offer.ExpiresAt != nil {
		t.Fatalf("the round trip invented an expiry: %v", opened.Offer.ExpiresAt)
	}
}

func TestLinkIsExpired(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	offer := sampleOffer()
	offer.ExpiresAt = &deadline

	link, err := p2p.LinkOffer(offer, "v=0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	if link.IsExpired(deadline.Add(-time.Second)) {
		t.Fatal("the link expired a second early")
	}

	if !link.IsExpired(deadline) {
		t.Fatal("the link survived its own deadline")
	}

	if !link.IsExpired(deadline.Add(time.Hour)) {
		t.Fatal("the link survived an hour past its deadline")
	}
}

func TestOpenOfferRefusesAnAnswer(t *testing.T) {
	t.Parallel()

	answer, err := p2p.LinkAnswer("offer-1", "v=0", secretFor(t), "medic")
	if err != nil {
		t.Fatalf("answer: %v", err)
	}

	if _, err := p2p.OpenOffer(answer.Link); err == nil {
		t.Fatal("an answer link opened as an offer")
	}
}

func TestOpenAnswerRefusesAnOffer(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	if _, err := p2p.OpenAnswer(link.Link); err == nil {
		t.Fatal("an offer link opened as an answer")
	}
}

func TestLinkAnswerRoundTrips(t *testing.T) {
	t.Parallel()

	secret := secretFor(t)

	answer, err := p2p.LinkAnswer("offer-1", "v=0\r\n", secret, "medic")
	if err != nil {
		t.Fatalf("answer: %v", err)
	}

	if !p2p.IsAnswerLink(answer.Link) {
		t.Fatalf("the link does not announce itself as an answer: %q", answer.Link)
	}

	if answer.OfferID != "offer-1" || answer.Secret != secret {
		t.Fatalf("answer = %+v", answer)
	}

	opened, err := p2p.OpenAnswer(answer.Link)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if opened.OfferID != "offer-1" || opened.ReceiverName != "medic" {
		t.Fatalf("payload = %+v", opened)
	}

	if opened.SDP != "v=0\r\n" {
		t.Fatalf("sdp = %q", opened.SDP)
	}
}

func TestLinkAnswerNeedsAnOfferID(t *testing.T) {
	t.Parallel()

	if _, err := p2p.LinkAnswer("", "v=0", secretFor(t), "medic"); err == nil {
		t.Fatal("an answer with no offer id was accepted")
	}

	if _, err := p2p.LinkAnswer("offer-1", "v=0", "", "medic"); err == nil {
		t.Fatal("an answer with no secret was accepted")
	}
}

// Two links for the same offer must not share a token, or a paste of one would
// be interchangeable with a paste of the other.
func TestLinkOfferGeneratesAFreshSecret(t *testing.T) {
	t.Parallel()

	offer := sampleOffer()

	first, err := p2p.LinkOffer(offer, "v=0")
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	second, err := p2p.LinkOffer(offer, "v=0")
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if first.Secret == second.Secret {
		t.Fatal("two links for one offer share a secret")
	}

	if first.Link == second.Link {
		t.Fatal("two links for one offer are identical")
	}
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
			if _, err := p2p.OpenOffer(tc.text); err == nil {
				t.Fatalf("%q was accepted", tc.text)
			}
		})
	}
}

// A link whose payload was sealed under a different secret must fail, not
// silently decrypt to something meaningless.
func TestOpenOfferRefusesAWrongSecret(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	other, err := crypto.GenerateSecret(8)
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}

	// Swap in a different secret segment, keeping the sealed token intact.
	forged := p2p.OfferLinkPrefix + other + "/" + strings.SplitN(link.Link, "/", 6)[5]

	if _, err := p2p.OpenOffer(forged); err == nil {
		t.Fatal("a link sealed under another secret was accepted")
	}
}

// Flipping one bit of the ciphertext must be detected: that is the whole value
// of an authenticated mode.
func TestOpenOfferRefusesATamperedPayload(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	token := []byte(strings.SplitN(link.Link, "/", 6)[5])
	token[len(token)-1] ^= 0x01

	forged := p2p.OfferLinkPrefix + link.Secret + "/" + string(token)

	if _, err := p2p.OpenOffer(forged); err == nil {
		t.Fatal("a tampered link was accepted")
	}
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
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if _, err := p2p.OpenOffer(p2p.OfferLinkPrefix + secret + "/" + sealed); err == nil {
		t.Fatal("a link from a future protocol version was accepted")
	}
}

// A link that seals a valid offer of the wrong kind must not pass as an answer:
// the two links are pasted into different sides of the conversation.
func TestOpenAnswerRefusesAnOfferPayload(t *testing.T) {
	t.Parallel()

	secret := secretFor(t)

	sealed, err := crypto.EncodeJSON(map[string]any{
		"k": string(domain.P2PLinkKindOffer),
		"v": int(domain.P2PProtocolVersion),
		"s": "v=0",
		"o": map[string]any{"offer_id": "x", "game_id": "darktide"},
	}, secret)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if _, err := p2p.OpenAnswer(p2p.AnswerLinkPrefix + secret + "/" + sealed); err == nil {
		t.Fatal("an offer payload opened as an answer")
	}
}

// The session key and the envelope key must not be the same bytes: a party that
// could open the envelope could otherwise forge a handshake.
func TestSessionKeyIsNotTheEnvelopeKey(t *testing.T) {
	t.Parallel()

	secret := secretFor(t)

	sessionKey, err := p2p.SessionKeyFor(secret)
	if err != nil {
		t.Fatalf("session key: %v", err)
	}

	if len(sessionKey) != 32 {
		t.Fatalf("session key is %d bytes", len(sessionKey))
	}

	if string(sessionKey) == secret {
		t.Fatal("the session key is the secret")
	}

	other, err := p2p.SessionKeyFor(secretFor(t))
	if err != nil {
		t.Fatalf("other session key: %v", err)
	}

	if string(sessionKey) == string(other) {
		t.Fatal("two secrets produced the same session key")
	}
}

// The scheme is the only part of a link that may change case: the secret and
// the token are base64url, where an upper-case letter is a different value.
func TestIsOfferLinkIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	shouted := strings.ToUpper(
		p2p.OfferLinkPrefix,
	) + strings.TrimPrefix(
		link.Link,
		p2p.OfferLinkPrefix,
	)

	if !p2p.IsOfferLink(shouted) {
		t.Fatal("a link with an upper-cased scheme was not recognised")
	}

	if _, err := p2p.OpenOffer(shouted); err != nil {
		t.Fatalf("open shouted: %v", err)
	}
}

// Upper-casing the payload is not a supported edit: base64url is case-sensitive
// and the token must stay exactly as it was sealed.
func TestOpenOfferRefusesAnUpperCasedPayload(t *testing.T) {
	t.Parallel()

	link, err := p2p.LinkOffer(sampleOffer(), "v=0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	if _, err := p2p.OpenOffer(strings.ToUpper(link.Link)); err == nil {
		t.Fatal("an upper-cased payload was accepted")
	}
}

// A very large share must not be able to make the reader allocate whatever the
// sender claims.
func TestOpenOfferRefusesAnOversizedLink(t *testing.T) {
	t.Parallel()

	huge := p2p.OfferLinkPrefix + secretFor(t) + "/" + strings.Repeat("A", 2<<20)

	if _, err := p2p.OpenOffer(huge); err == nil {
		t.Fatal("an oversized link was accepted")
	}
}
