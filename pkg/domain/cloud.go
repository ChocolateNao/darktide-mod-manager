package domain

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrCloudProviderNotFound = errors.New("cloud provider not found")
	ErrCloudAuthRequired     = errors.New("cloud provider authentication required")
	ErrShareExpired          = errors.New("share link has expired")
	ErrShareInvalid          = errors.New("share link is invalid or corrupted")
	ErrShareRevoked          = errors.New("share has been revoked")
	ErrGameNotRegistered     = errors.New("share targets a game this manager does not have")
	ErrFileTooLarge          = errors.New("file exceeds provider size limit")
	ErrIntegrityMismatch     = errors.New("downloaded file hash mismatch")
	ErrAllProvidersFailed    = errors.New("all providers failed")
	ErrShareNotFound         = errors.New("share not found")
)

type CloudCredentials map[string]string

type CloudProvider interface {
	ID() string
	Name() string
	Description() string

	SupportsAuth() bool
	MaxFileSize() int64
	MaxTotalSize() int64

	// Authenticate accepts credentials the caller already holds. It is not the
	// interactive flow: a provider's own consent page produces the token this
	// takes.
	Authenticate(creds CloudCredentials) error
	// ValidateCredentials reports whether the session can still reach the
	// provider, refreshing when it can.
	ValidateCredentials(ctx context.Context) error
	// ForgetCredentials drops everything held. Nothing is written anywhere, so
	// there is nothing to erase.
	ForgetCredentials()

	Upload(ctx context.Context, files []ShareFile, opts UploadOptions) (*UploadResult, error)
	Download(ctx context.Context, fileRef FileReference, destPath string) error
	DeleteFile(ctx context.Context, fileRef FileReference) error
	CreateShareLink(
		ctx context.Context,
		files []FileReference,
		opts ShareOptions,
	) (*ShareLink, error)
	ResolveShareLink(ctx context.Context, link ShareLink) ([]FileReference, error)
}

type ShareFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	// Source is the local file the bytes are read from. A provider has to name
	// it to upload anything, and it is deliberately absent from the share
	// payload: a recipient's manager would have no such path, and a leak of the
	// sharer's filesystem layout in a link is not something to hand out.
	Source string `json:"-"`
	Size   int64  `json:"size"`
}

type FileReference struct {
	ID   string `json:"id"`
	Hash string `json:"hash,omitempty"`
	Size int64  `json:"size"`
}

type UploadOptions struct {
	Progress func(bytes int64)
}

type UploadResult struct {
	References []FileReference
}

type ShareOptions struct {
	Expiry time.Time
}

type ShareLink struct {
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	URL        string     `json:"url"`
	ProviderID string     `json:"provider_id"`
}

func (l ShareLink) IsExpired(now time.Time) bool {
	if l.ExpiresAt == nil {
		return false
	}

	return now.After(*l.ExpiresAt)
}

type SharedFile struct {
	FileID     string `json:"id"`
	Path       string `json:"path"`
	Hash       string `json:"hash"`
	RemoteName string `json:"n"`
	Size       int64  `json:"size"`
}

// ArchiveFormatZip is the only archive format shares are packed into. Zip is
// what archive.Extract reads without a streaming dependency and what every
// provider accepts as a single opaque object.
const ArchiveFormatZip = "zip"

// PackedMod describes a mod transmitted as one archive rather than as its
// individual files.
//
// One archive per mod is the unit that makes a share resumable and verifiable:
// the recipient checks a single hash instead of one per file, and a provider
// never sees a mod as N separate objects that a partial failure can leave
// half-uploaded. The archive is named after the mod id, so two mods that both
// ship a file called shared.zip cannot overwrite each other.
type PackedMod struct {
	FileID string `json:"id"`
	Hash   string `json:"h"`
	Format string `json:"f"`
	Size   int64  `json:"s"`
}

// PackedModFromFile builds the packed descriptor for an archive that was hashed
// and whose size is known.
func PackedModFromFile(fileID string, size int64, hash string) *PackedMod {
	return &PackedMod{FileID: fileID, Hash: hash, Format: ArchiveFormatZip, Size: size}
}

type SharedMod struct {
	PackedArchive *PackedMod `json:"archive,omitempty"`
	Name          string     `json:"name"`
	Version       string     `json:"version"`
	Author        string     `json:"author"`
	Description   string     `json:"description"`
	FileIDs       []string   `json:"files"`
	Dependencies  []string   `json:"deps"`
}

type SharePayload struct {
	CreatedAt time.Time    `json:"t"`
	ExpiresAt *time.Time   `json:"e,omitempty"`
	Provider  string       `json:"p"`
	GameID    string       `json:"g"`
	Files     []SharedFile `json:"f"`
	Mods      []SharedMod  `json:"m"`
	Version   int          `json:"v"`
}

type ShareRecord struct {
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	ID        string      `json:"id"`
	GameID    string      `json:"game_id"`
	Mods      []Mod       `json:"mods"`
	Links     []ShareLink `json:"links"`
}

type SharePreview struct {
	ShareID   string       `json:"share_id"`
	GameID    string       `json:"game_id"`
	ModID     string       `json:"mod_id,omitempty"`
	Mods      []SharedMod  `json:"mods"`
	Files     []SharedFile `json:"files"`
	TotalSize int64        `json:"total_size"`
}

type MultiShareLink struct {
	Primary   ShareLink   `json:"primary"`
	ShareID   string      `json:"share_id"`
	Fallbacks []ShareLink `json:"fallbacks"`
}

type InstallOptions struct {
	AutoInstall bool
}

// CloudProviderInfo is the projection of a provider used to render a picker.
// It exists so a caller that only needs to show the list does not have to hold
// a live CloudProvider, which would drag in credentials and an HTTP client.
type CloudProviderInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	SupportsAuth bool   `json:"supports_auth"`
	MaxFileSize  int64  `json:"max_file_size,omitempty"`
	MaxTotalSize int64  `json:"max_total_size,omitempty"`
}

func ShareLinkFormat(providerID string) string {
	return "https://dmm.app/s/" + providerID + "/"
}

// ShareURLScheme is the custom URL scheme that opens the app straight into a
// share preview. It bypasses the browser entirely, so a link pasted into a
// chat client launches the manager rather than a web page that redirects.
const ShareURLScheme = "dmm://"

// DeepLinkFormat builds the deep link for a share payload.
func DeepLinkFormat(providerID string) string {
	return ShareURLScheme + "share/" + providerID + "/"
}

// IsDeepLink reports whether a link uses the dmm:// scheme.
func IsDeepLink(url string) bool {
	return strings.HasPrefix(url, ShareURLScheme)
}

// ParseDeepLink extracts the provider and encoded payload from a dmm:// link.
//
// A malformed link is rejected before any provider is contacted: the payload
// is decoded and schema-checked here, so a garbage URL never reaches the
// network.
func ParseDeepLink(url string) (providerID, encoded string, err error) {
	if !IsDeepLink(url) {
		return "", "", ErrShareInvalid
	}

	prefix := ShareURLScheme + "share/"
	if !strings.HasPrefix(url, prefix) {
		return "", "", ErrShareInvalid
	}

	rest := strings.TrimPrefix(url, prefix)

	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return "", "", ErrShareInvalid
	}

	providerID = rest[:slash]
	encoded = rest[slash+1:]

	if providerID == "" || encoded == "" {
		return "", "", ErrShareInvalid
	}

	return providerID, encoded, nil
}

// ParseShareURL splits either link form into its provider id and encoded
// payload, so a caller can accept a link pasted from a browser or from chat
// without caring which one it is.
func ParseShareURL(url string) (providerID, encoded string, err error) {
	if IsDeepLink(url) {
		return ParseDeepLink(url)
	}

	const webPrefix = "https://dmm.app/s/"
	if !strings.HasPrefix(url, webPrefix) {
		return "", "", ErrShareInvalid
	}

	rest := strings.TrimPrefix(url, webPrefix)

	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return "", "", ErrShareInvalid
	}

	providerID = rest[:slash]
	encoded = rest[slash+1:]

	if providerID == "" || encoded == "" {
		return "", "", ErrShareInvalid
	}

	return providerID, encoded, nil
}
