package p2p

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/sharing/crypto"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// Link prefixes.
//
// A share is two links. The sender publishes an offer link and the recipient
// answers with an answer link; both are sealed under the same secret, and each
// carries exactly one session description. The two prefixes differ so that
// pasting the wrong half into the manager produces "that is an answer, not an
// offer" rather than a decode failure halfway through a transfer.
//
// They are deliberately not the dmm://share/<provider>/ shape the cloud links
// use. Those name a server; these name a peer, and a recipient who reads one
// should not have to consult a provider list to find out what they are holding.
const (
	// OfferLinkPrefix is the scheme of a sender's published link.
	OfferLinkPrefix = "dmm://p2p/v1/"
	// AnswerLinkPrefix is the scheme of a recipient's reply.
	AnswerLinkPrefix = "dmm://p2pa/v1/"
)

// linkSecretBytes sizes the capability secret that travels in the link.
//
// 16 bytes is chosen for the length of the link rather than for the strength of
// the key. The secret has to be pasted, so every character is a cost paid by the
// recipient; 128 bits is far past brute force while costing 22 base64url
// characters, which is roughly one line of most terminals.
const linkSecretBytes = 16

// maxLinkBytes bounds a link accepted from the outside. A real link carrying an
// SDP, an inventory and a mod list for a large selection is comfortably under
// 64 KiB; the ceiling is here so a paste of a minified web page fails fast
// instead of allocating whatever it happens to be.
const maxLinkBytes = 1 << 20

// Errors a link can fail with. Each one is a sentinel rather than a formatted
// string because the caller has to map them onto distinct user-facing outcomes —
// expired, wrong kind, not from this manager — and a formatted message cannot be
// matched on.
var (
	// ErrNotALink reports text that is not a dmm peer-to-peer link at all.
	ErrNotALink = errors.New("not a mod manager share link")
	// ErrWrongLinkKind reports a valid link of the other kind: an answer pasted
	// where an offer was expected, or the reverse.
	ErrWrongLinkKind = errors.New("link is the wrong half of a share")
	// ErrUnknownProtocolVersion reports a link this build cannot speak. It is
	// separate from a decode failure so the message can say "update the
	// manager" instead of "corrupt link".
	ErrUnknownProtocolVersion = errors.New("link uses an unsupported protocol version")
	// ErrWrongSecret reports a sealed blob that did not open under the secret
	// carried in the same link, which means the link was edited or truncated.
	ErrWrongSecret = errors.New("link secret does not open link payload")
)

// LinkOffer seals an offer and its session description into a shareable link,
// generating the capability secret that both halves of the share will use.
//
// The returned link is the entire capability: whoever holds it can read the
// offer and open the data channel. That is deliberate, and is the same bargain
// every link-based transfer makes — the link is the password. What the seal buys
// is that the link does not have to be legible to whatever carries it. A chat
// client that unfurls it sees an opaque token, not the recipient's local
// address, the list of mods, or the URLs the mods came from.
func LinkOffer(offer domain.P2PShareOffer, sdp string) (*domain.P2PShareLink, error) {
	secret, err := crypto.GenerateSecret(linkSecretBytes)
	if err != nil {
		return nil, fmt.Errorf("generate link secret: %w", err)
	}

	created := offer.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
		offer.CreatedAt = created
	}

	payload := domain.P2PSharePayload{
		Kind:      domain.P2PLinkKindOffer,
		Version:   domain.P2PProtocolVersion,
		CreatedAt: created,
		SDP:       sdp,
		Offer:     &offer,
	}

	link, err := sealLink(OfferLinkPrefix, payload, secret)
	if err != nil {
		return nil, err
	}

	return &domain.P2PShareLink{
		Link:      link,
		Secret:    secret,
		Offer:     offer,
		SDP:       sdp,
		PeerName:  offer.SenderName,
		CreatedAt: created,
	}, nil
}

// LinkAnswer seals a recipient's session description into a reply link.
//
// The offer's secret is echoed rather than regenerated: both ends have to derive
// the same session key, and deriving it from anything the sender did not choose
// would make the handshake fail in a way that looks like an attack.
func LinkAnswer(offerID, sdp, secret, receiverName string) (*domain.P2PAnswerLink, error) {
	if offerID == "" {
		return nil, errors.New("answer needs an offer id")
	}

	if secret == "" {
		return nil, crypto.ErrEmptyPassphrase
	}

	created := time.Now().UTC()
	payload := domain.P2PSharePayload{
		Kind:      domain.P2PLinkKindAnswer,
		Version:   domain.P2PProtocolVersion,
		CreatedAt: created,
		SDP:       sdp,
		OfferID:   offerID,
		PeerName:  receiverName,
	}

	link, err := sealLink(AnswerLinkPrefix, payload, secret)
	if err != nil {
		return nil, err
	}

	return &domain.P2PAnswerLink{
		Link:         link,
		Secret:       secret,
		OfferID:      offerID,
		SDP:          sdp,
		ReceiverName: receiverName,
		CreatedAt:    created,
	}, nil
}

// IsOfferLink and IsAnswerLink report whether text looks like the given half of
// a share, without decrypting it.
//
// The UI uses these to route a deep link to the right screen, and they answer
// from the prefix alone: deciding which screen to open must not require the
// secret to be correct, because the recipient has not typed one yet.
func IsOfferLink(text string) bool {
	return hasPrefix(text, OfferLinkPrefix)
}

// IsAnswerLink reports whether text looks like a recipient's reply.
func IsAnswerLink(text string) bool {
	return hasPrefix(text, AnswerLinkPrefix)
}

// OpenOffer parses an offer link, opening the sealed payload with the secret
// carried in the link itself.
//
// The version is checked before the offer is handed back so a recipient sees
// "update the manager" rather than a preview built from fields a future version
// may have moved.
func OpenOffer(text string) (*domain.P2PShareLink, error) {
	payload, secret, err := openLink(text, OfferLinkPrefix, domain.P2PLinkKindOffer)
	if err != nil {
		return nil, err
	}

	offer := payload.Offer
	if offer == nil {
		return nil, fmt.Errorf("%w: offer link carries no offer", ErrWrongLinkKind)
	}

	return &domain.P2PShareLink{
		Link:      normaliseLink(text),
		Secret:    secret,
		Offer:     *offer,
		SDP:       payload.SDP,
		PeerName:  payload.PeerName,
		CreatedAt: payload.CreatedAt,
	}, nil
}

// OpenAnswer parses a reply link.
func OpenAnswer(text string) (*domain.P2PAnswerLink, error) {
	payload, secret, err := openLink(text, AnswerLinkPrefix, domain.P2PLinkKindAnswer)
	if err != nil {
		return nil, err
	}

	if payload.OfferID == "" {
		return nil, fmt.Errorf("%w: answer link names no offer", ErrWrongLinkKind)
	}

	return &domain.P2PAnswerLink{
		Link:         normaliseLink(text),
		Secret:       secret,
		OfferID:      payload.OfferID,
		SDP:          payload.SDP,
		ReceiverName: payload.PeerName,
		CreatedAt:    payload.CreatedAt,
	}, nil
}

// SessionKeyFor derives the handshake key from a link's secret.
//
// Both halves of a share call this with the same secret and get the same key;
// the label is what stops that key from also being able to open the link, so a
// peer that has completed a handshake still cannot mint a new offer.
func SessionKeyFor(secret string) ([]byte, error) {
	return crypto.DeriveKey(secret, crypto.LabelSession)
}

// sealLink encodes a payload into a prefixed link.
//
// The order is marshal, compress, seal, encode. Compression has to happen
// before encryption because that is the only point at which the plaintext is
// visible to the compressor, and encrypting before compressing would give up the
// bulk of the size win on an SDP full of repeated lines.
func sealLink(prefix string, payload domain.P2PSharePayload, secret string) (string, error) {
	if secret == "" {
		return "", crypto.ErrEmptyPassphrase
	}

	token, err := crypto.EncodeJSON(payload, secret)
	if err != nil {
		return "", fmt.Errorf("seal share link: %w", err)
	}

	return prefix + secret + "/" + token, nil
}

// openLink is the inverse of sealLink: split the prefix and the secret, open the
// payload, and confirm it is the expected kind.
func openLink(
	text, prefix string,
	want domain.P2PLinkKind,
) (domain.P2PSharePayload, string, error) {
	var zero domain.P2PSharePayload

	trimmed := normaliseLink(text)
	if len(trimmed) > maxLinkBytes {
		return zero, "", fmt.Errorf(
			"%w: link is %d bytes, the ceiling is %d",
			ErrNotALink,
			len(trimmed),
			maxLinkBytes,
		)
	}

	if !hasPrefix(trimmed, prefix) {
		return zero, "", fmt.Errorf("%w: expected %s", ErrNotALink, prefix)
	}

	rest := trimmed[len(prefix):]

	secret, token, found := strings.Cut(rest, "/")
	if !found || secret == "" || token == "" {
		return zero, "", fmt.Errorf("%w: link has no secret and payload", crypto.ErrMalformed)
	}

	var payload domain.P2PSharePayload

	if err := crypto.DecodeJSON(token, secret, &payload); err != nil {
		if errors.Is(err, crypto.ErrMalformed) {
			return zero, "", fmt.Errorf("%w: payload is not base64url", ErrNotALink)
		}

		return zero, "", fmt.Errorf("%w: %v", ErrWrongSecret, err)
	}

	if payload.Version != domain.P2PProtocolVersion {
		return zero, "", fmt.Errorf(
			"%w: link is version %d, this build speaks %d",
			ErrUnknownProtocolVersion, payload.Version, domain.P2PProtocolVersion,
		)
	}

	if payload.Kind != want {
		return zero, "", fmt.Errorf(
			"%w: link is a %s, expected a %s",
			ErrWrongLinkKind,
			payload.Kind,
			want,
		)
	}

	return payload, secret, nil
}

// normaliseLink trims the whitespace a paste brings with it.
//
// Links arrive from chat windows, terminals and QR readers, and every one of
// them can add a trailing newline or a leading space. Failing on that would make
// the feature look broken for a reason the user cannot see, so it is stripped
// once, here, rather than at each of the six call sites that accept a link.
func normaliseLink(text string) string {
	return strings.TrimSpace(text)
}

// hasPrefix compares case-insensitively.
//
// The scheme is lowercase everywhere the manager emits it, but a link may have
// travelled through something that uppercased the scheme, and "that is not a
// link" would be a confusing answer to "I pasted exactly what you sent".
func hasPrefix(text, prefix string) bool {
	if len(text) < len(prefix) {
		return false
	}

	return strings.EqualFold(text[:len(prefix)], prefix)
}
