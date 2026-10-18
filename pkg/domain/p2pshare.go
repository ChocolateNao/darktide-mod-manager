package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// P2PProtocolVersion is the version stamped into every offer and every frame
// header. It is a number rather than a semver range because both ends run the
// same manager: there is nothing to negotiate with, only a decision to attempt.
const P2PProtocolVersion = 1

// P2PSourceFallback is how a recipient gets a mod without taking the file from the
// peer. It rides in the offer, the only thing a recipient can read before deciding
// whether to trust the sender.
const (
	// P2PSourceFallbackNone means the sender found no upstream URL for a mod.
	P2PSourceFallbackNone = ""
	// P2PSourceFallbackDownload means DownloadURL can be fetched directly.
	P2PSourceFallbackDownload = "download"
	// P2PSourceFallbackBrowse means SourceURL must be visited by hand: the file
	// needs an account or an API key the manager does not hold.
	P2PSourceFallbackBrowse = "browse"
	// P2PSourceFallbackUpdate points at the newest release of the same mod, which
	// is what makes "get the updated version yourself" a single click.
	P2PSourceFallbackUpdate = "update"
)

// P2PShareOffer is everything the sender will tell the recipient before a single
// byte of mod data moves. It travels inside the sealed link payload, so there is
// no server-side copy and no way to read an offer out of a chat log.
type P2PShareOffer struct {
	CreatedAt   time.Time       `json:"c"`
	ExpiresAt   *time.Time      `json:"e,omitempty"`
	OfferID     string          `json:"o"`
	GameID      string          `json:"g"`
	SenderName  string          `json:"n,omitempty"`
	Mods        []SharedMod     `json:"m"`
	Files       []P2PSharedFile `json:"f"`
	SourceLinks []ModSourceLink `json:"l,omitempty"`
	STUNServers []string        `json:"s,omitempty"`
}

// P2PSharedFile is one archive in an offer.
//
// Size and HashSHA256 are the numbers the transfer is checked against, published
// up front so a recipient can refuse an oversized share without connecting.
type P2PSharedFile struct {
	FileID     string `json:"i"`
	Name       string `json:"n"`
	ModID      string `json:"m"`
	HashSHA256 string `json:"h"`
	Size       int64  `json:"s"`
}

// ModSourceLink is the upstream a recipient can use instead of taking the file
// from the peer.
type ModSourceLink struct {
	// ModID is the mod this fallback belongs to.
	ModID string `json:"m"`
	// SourceID is the manager source plugin the link came from, so the
	// recipient can label it and, where credentials exist, import from it.
	SourceID string `json:"s,omitempty"`
	// SourceURL is the mod's page on the source.
	SourceURL string `json:"p,omitempty"`
	// DownloadURL is a direct file URL when the source has one. Authenticated
	// sources leave it empty: an unfetchable URL is worse than none.
	DownloadURL string `json:"d,omitempty"`
	// UpdateURL points at the newest version, which lets a recipient replace
	// an installed mod rather than reinstall the exact version being shared.
	UpdateURL string `json:"u,omitempty"`
	// Fallback is one of the P2PSourceFallback constants, and is what the UI
	// and CLI turn into a button.
	Fallback string `json:"f,omitempty"`
}

// HasURL reports whether any upstream link is worth offering; a mod with none has no
// fallback, so the UI says so instead of showing dead buttons.
func (l ModSourceLink) HasURL() bool {
	return l.DownloadURL != "" || l.SourceURL != "" || l.UpdateURL != ""
}

// P2PSourceLinkForMod builds the fallback descriptor for a mod, choosing the most
// useful link its stored URLs allow. A direct download beats a page to visit, and
// the update link is only preferred without one: "fetch this exact file" is more
// precise than "go and find the newest one yourself".
func P2PSourceLinkForMod(mod Mod) ModSourceLink {
	link := ModSourceLink{
		ModID:       mod.ID,
		SourceID:    mod.SourceID,
		SourceURL:   mod.SourceURL,
		DownloadURL: mod.DownloadURL,
		UpdateURL:   mod.UpdateURL,
	}

	switch {
	case link.DownloadURL != "":
		link.Fallback = P2PSourceFallbackDownload
	case link.SourceURL != "":
		link.Fallback = P2PSourceFallbackBrowse
	case link.UpdateURL != "":
		link.Fallback = P2PSourceFallbackUpdate
	default:
		link.Fallback = P2PSourceFallbackNone
	}

	return link
}

// P2PLinkKind distinguishes the two halves of the handshake: the sender publishes
// an offer, the recipient answers it, and each link is sealed under the same secret.
type P2PLinkKind string

const (
	// P2PLinkKindOffer is a sender's published offer.
	P2PLinkKindOffer P2PLinkKind = "offer"
	// P2PLinkKindAnswer is a recipient's answer carrying their SDP.
	P2PLinkKindAnswer P2PLinkKind = "answer"
)

// P2PSharePayload is the plaintext that a link seals.
//
// One struct serves both kinds so the codec has a single shape; answer payloads
// leave Offer empty, a couple of JSON bytes against zstd.
type P2PSharePayload struct {
	CreatedAt time.Time      `json:"c"`
	Offer     *P2PShareOffer `json:"o,omitempty"`
	Kind      P2PLinkKind    `json:"k"`
	SDP       string         `json:"s"`
	OfferID   string         `json:"i,omitempty"`
	PeerName  string         `json:"n,omitempty"`
	Version   int            `json:"v"`
}

// P2PShareLink is a decoded offer link, what the recipient holds once the seal is
// opened. Secret is included because the handshake needs it, and dropping it would
// mean threading a second value through every caller that only wants the offer.
type P2PShareLink struct {
	CreatedAt time.Time     `json:"created_at"`
	Link      string        `json:"link"`
	Secret    string        `json:"-"`
	PeerName  string        `json:"sender,omitempty"`
	SDP       string        `json:"-"`
	Offer     P2PShareOffer `json:"offer"`
}

// ExpiresAt is a shortcut to the offer's expiry, for callers that hold a link
// and do not otherwise care about the offer's contents.
func (l *P2PShareLink) ExpiresAt() *time.Time {
	if l == nil {
		return nil
	}

	return l.Offer.ExpiresAt
}

// IsExpired reports whether the offer may no longer be accepted.
//
// The check lives on the link so a recipient reading an expired link is told so
// rather than left to guess. The deadline itself is excluded: treating T as still
// valid would make a share's last moment depend on clock skew between two machines.
func (l *P2PShareLink) IsExpired(now time.Time) bool {
	if l == nil {
		return false
	}

	expires := l.Offer.ExpiresAt
	if expires == nil {
		return false
	}

	return !now.Before(*expires)
}

// P2PAnswerLink is a decoded answer link, produced by the recipient and pasted
// back to the sender.
type P2PAnswerLink struct {
	CreatedAt    time.Time `json:"created_at"`
	Link         string    `json:"link"`
	Secret       string    `json:"-"`
	OfferID      string    `json:"offer_id"`
	SDP          string    `json:"-"`
	ReceiverName string    `json:"receiver,omitempty"`
}

// P2PSharePreview is what a recipient is shown before deciding.
//
// The upstream fallbacks sit next to the file list on purpose: "18 MiB over the
// wire" and "or grab these three from Nexus instead" are one decision.
type P2PSharePreview struct {
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
	OfferID     string          `json:"offer_id"`
	GameID      string          `json:"game_id"`
	SenderName  string          `json:"sender,omitempty"`
	Mods        []SharedMod     `json:"mods"`
	Files       []P2PSharedFile `json:"files"`
	SourceLinks []ModSourceLink `json:"source_links,omitempty"`
	TotalSize   int64           `json:"total_size"`
	Expired     bool            `json:"expired"`
}

// P2PCreateOptions controls how a share is produced.
type P2PCreateOptions struct {
	ExpiresAt   *time.Time
	SenderName  string
	STUNServers []string
	ChunkSize   int
}

// P2PShareState is the lifecycle of one share, from either side. The frontend renders
// it verbatim and the CLI maps it to a word, so a new state is a visible change.
type P2PShareState string

const (
	// P2PShareStateCreated means the archives are packed and the link exists,
	// but nobody has accepted it.
	P2PShareStateCreated P2PShareState = "created"
	// P2PShareStateOffered means the recipient has read the offer and is deciding,
	// which is where declining in favour of the source links happens.
	P2PShareStateOffered P2PShareState = "offered"
	// P2PShareStateConnecting means an answer exists and ICE is running.
	P2PShareStateConnecting P2PShareState = "connecting"
	// P2PShareStateTransferring means the handshake passed and archives are
	// moving.
	P2PShareStateTransferring P2PShareState = "transferring"
	// P2PShareStateVerifying means every archive arrived and the recipient is
	// checking hashes.
	P2PShareStateVerifying P2PShareState = "verifying"
	// P2PShareStateCompleted means every hash matched.
	P2PShareStateCompleted P2PShareState = "completed"
	// P2PShareStateRejected means the recipient declined and will use the
	// source links instead. This is a first-class outcome, not a failure.
	P2PShareStateRejected P2PShareState = "rejected"
	// P2PShareStateExpired means the offer passed its expiry.
	P2PShareStateExpired P2PShareState = "expired"
	// P2PShareStateFailed means the share did not complete. FailReason says
	// why.
	P2PShareStateFailed P2PShareState = "failed"
)

// Terminal reports whether no further progress is possible from this state.
// Callers use it to decide whether a session can be dropped from the store.
func (s P2PShareState) Terminal() bool {
	switch s {
	case P2PShareStateCompleted, P2PShareStateRejected, P2PShareStateExpired, P2PShareStateFailed:
		return true
	default:
		return false
	}
}

// P2PShareSession is the manager's view of one share, at either end. The frontend and
// the CLI render progress from this record instead of subscribing to per-byte events.
type P2PShareSession struct {
	UpdatedAt        time.Time        `json:"updated_at"`
	CreatedAt        time.Time        `json:"created_at"`
	Preview          *P2PSharePreview `json:"preview,omitempty"`
	ExpiresAt        *time.Time       `json:"expires_at,omitempty"`
	State            P2PShareState    `json:"state"`
	FailReason       string           `json:"fail_reason,omitempty"`
	Link             string           `json:"link,omitempty"`
	OfferID          string           `json:"offer_id"`
	Role             string           `json:"role"`
	GameID           string           `json:"game_id"`
	ModIDs           []string         `json:"mod_ids"`
	TotalSize        int64            `json:"total_size"`
	TransferredBytes int64            `json:"transferred_bytes"`
}

// Progress returns completed bytes as a fraction of the total, bounded to [0,1]. A
// zero-byte share reports 1, or the progress bar would sit at the start forever.
func (s *P2PShareSession) Progress() float64 {
	if s == nil || s.TotalSize <= 0 {
		return 1
	}

	progress := float64(s.TransferredBytes) / float64(s.TotalSize)
	if progress < 0 {
		return 0
	}

	if progress > 1 {
		return 1
	}

	return progress
}

// P2PTransferProgress reports the position inside the current archive, for the
// rare caller that wants a finer signal than the session record.
type P2PTransferProgress struct {
	// FileID is the archive in flight.
	FileID string `json:"file_id"`
	// ModID is the mod that archive installs.
	ModID string `json:"mod_id"`
	// FileIndex is the zero-based position in the offer.
	FileIndex int `json:"file_index"`
	// FileCount is how many archives the offer holds.
	FileCount int `json:"file_count"`
	// BytesSent and BytesReceived are cumulative across the whole offer.
	BytesSent     int64 `json:"bytes_sent"`
	BytesReceived int64 `json:"bytes_received"`
	// TotalBytes is the offer total.
	TotalBytes int64 `json:"total_bytes"`
}

// ComputeSHA256Hex returns the lowercase hex SHA-256 of data.
//
// It lives here because the hash is part of the share contract — offer, frame header,
// and the mismatch message — and three layers encoding a byte slice three ways is
// three chances to disagree.
func ComputeSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}
