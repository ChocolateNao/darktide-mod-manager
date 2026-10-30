package services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type ShareStore interface {
	Save(ctx context.Context, share *domain.ShareRecord) error
	Get(ctx context.Context, id string) (*domain.ShareRecord, error)
	List(ctx context.Context) ([]domain.ShareRecord, error)
	ListByGame(ctx context.Context, gameID string) ([]domain.ShareRecord, error)
	Delete(ctx context.Context, id string) error
	DeleteAll(ctx context.Context) error
	GetByLink(ctx context.Context, link domain.ShareLink) (*domain.ShareRecord, error)
}

type ModInstaller interface {
	Install(ctx context.Context, modID, sourceID string, opts InstallOptions) (*domain.Mod, error)
	InstallArchiveFromShare(
		ctx context.Context,
		mod *domain.Mod,
		archivePath string,
		opts InstallOptions,
	) (*domain.Mod, error)
}

// ShareService uploads packed mods to a storage provider and hands out a
// deep link to the uploaded copy.
//
// Deprecated: everything this type produces travels through a third party — the
// provider holds the archives and could serve them to anyone who guesses the
// identifier. P2PShareService moves the same bytes straight between two peers;
// this one stays because existing shares must stay resolvable and revocable.
type ShareService struct {
	registry     *CloudRegistry
	store        ShareStore
	gameLocator  ShareGameLocator
	modInstaller ModInstaller
	logger       zerolog.Logger
	config       *ShareConfig
	now          func() time.Time
}

type ShareConfig struct {
	DefaultProvider   string
	FallbackProviders []string
	ShareExpiryDays   int
	AutoInstall       bool
}

func DefaultShareConfig() *ShareConfig {
	return &ShareConfig{
		DefaultProvider:   "s3",
		FallbackProviders: []string{},
		ShareExpiryDays:   30,
		AutoInstall:       false,
	}
}

// ShareGameLocator is the slice of GameLocator the sharing subsystem needs:
// which game is active and what it is called. Narrowing it keeps the
// orchestrator testable without a filesystem-backed configuration.
type ShareGameLocator interface {
	Active() (domain.GameDefinition, domain.GameSettings, error)
}

type ShareServiceDeps struct {
	Registry     *CloudRegistry
	Store        ShareStore
	GameLocator  ShareGameLocator
	ModInstaller ModInstaller
	Logger       zerolog.Logger
	Config       *ShareConfig
	Clock        func() time.Time
}

func NewShareService(deps ShareServiceDeps) *ShareService {
	if deps.Registry == nil {
		deps.Registry = NewCloudRegistry()
	}

	if deps.Config == nil {
		deps.Config = DefaultShareConfig()
	}

	if deps.Clock == nil {
		deps.Clock = time.Now
	}

	return &ShareService{
		registry:     deps.Registry,
		store:        deps.Store,
		gameLocator:  deps.GameLocator,
		modInstaller: deps.ModInstaller,
		logger:       deps.Logger,
		config:       deps.Config,
		now:          deps.Clock,
	}
}

func (s *ShareService) clock() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}

	return s.now()
}

func (s *ShareService) CreateShare(
	ctx context.Context,
	mods []domain.Mod,
	providerIDs []string,
) (*domain.MultiShareLink, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	if len(mods) == 0 {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "no mods to share")
	}

	if len(providerIDs) == 0 {
		providerIDs = []string{s.config.DefaultProvider}
	}

	gameID, err := s.resolveGameID(mods)
	if err != nil {
		return nil, err
	}

	// A share is for exactly one game: a mixed selection would stamp one game on
	// the payload while the other mod's files point at storage resolved differently.
	for i := range mods {
		if id := mods[i].GameID; id != "" && id != gameID {
			return nil, appErrors.New(
				appErrors.ErrCodeGameNotRegistered,
				"mod %q belongs to game %q, but this share is for game %q",
				mods[i].Name, id, gameID,
			)
		}
	}

	// Packing writes a full copy of every mod to disk before anything is
	// uploaded, so it happens in the system temp directory: filling the volume the
	// manager was installed on would be a surprising side effect of clicking share.
	// The staging folder lives until the uploads are done, not until the mods are
	// packed, because a provider reads the bytes back off disk.
	staging, err := os.MkdirTemp("", "dmm-share-pack-*")
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeCloudUploadFailed,
			"create a staging folder to pack the mods in")
	}

	defer func() { _ = os.RemoveAll(staging) }()

	files, sharedMod, err := s.collectShare(ctx, staging, mods)
	if err != nil {
		return nil, err
	}

	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}

	var expiry *time.Time

	if s.config.ShareExpiryDays > 0 {
		t := s.clock().Add(time.Duration(s.config.ShareExpiryDays) * 24 * time.Hour)
		expiry = &t
	}

	// The id is minted before the uploads so a partially failed share can still
	// be traced against the same share the recipient's link points at.
	shareID := uuid.New().String()

	var (
		links []domain.ShareLink
		errs  []error
	)

	for _, pid := range providerIDs {
		provider, err := s.registry.Get(pid)
		if err != nil {
			errs = append(errs, fmt.Errorf("provider %q: %w", pid, err))
			continue
		}

		if provider.SupportsAuth() {
			if err := provider.ValidateCredentials(ctx); err != nil {
				s.logger.Warn().Str("op", "cloud-share").Str("provider", pid).Err(err).Msg("provider auth failed, skipping")
				errs = append(errs, fmt.Errorf("provider %q: %w", pid, err))

				continue
			}
		}

		uploadStart := s.clock()

		uploaded, err := provider.Upload(ctx, files, domain.UploadOptions{})
		if err != nil {
			s.logger.Warn().
				Str("op", "cloud-share").
				Str("provider", pid).
				Int("files", len(files)).
				Err(err).
				Msg("upload to provider failed")
			errs = append(errs, fmt.Errorf("provider %q: %w", pid, err))

			continue
		}

		if len(uploaded.References) != len(files) {
			errs = append(errs, fmt.Errorf(
				"provider %q: uploaded %d of %d files", pid, len(uploaded.References), len(files)))

			continue
		}

		// The provider's own link is optional; the carrier link below is the
		// contract the recipient actually resolves.
		if _, err := provider.CreateShareLink(ctx, uploaded.References, domain.ShareOptions{
			Expiry: deadlineOrZero(expiry),
		}); err != nil {
			s.logger.Warn().Str("op", "cloud-share").Str("provider", pid).Err(err).Msg("provider share link ignored")
		}

		link, err := s.encodeLink(pid, gameID, uploaded.References, sharedMod, expiry)
		if err != nil {
			errs = append(errs, fmt.Errorf("provider %q: %w", pid, err))
			continue
		}

		links = append(links, link)

		s.logger.Info().
			Str("op", "cloud-share").
			Str("share_id", shareID).
			Str("provider", pid).
			Int("mod_count", len(sharedMod)).
			Int("file_count", len(files)).
			Int64("total_bytes", totalBytes).
			Int64("duration_ms", s.clock().Sub(uploadStart).Milliseconds()).
			Msg("uploaded to provider")
	}

	if len(links) == 0 {
		return nil, appErrors.Wrap(
			domain.ErrAllProvidersFailed,
			appErrors.ErrCodeAllProvidersFailed,
			"all providers failed: %s",
			joinErrors(errs),
		)
	}

	record := &domain.ShareRecord{
		ID:        shareID,
		GameID:    gameID,
		Mods:      mods,
		Links:     links,
		CreatedAt: s.clock(),
		UpdatedAt: s.clock(),
	}
	if s.store != nil {
		if err := s.store.Save(ctx, record); err != nil {
			s.logger.Warn().Str("op", "cloud-share").Err(err).Msg("failed to persist share record")
		}
	}

	return &domain.MultiShareLink{
		Primary:   links[0],
		Fallbacks: links[1:],
		ShareID:   record.ID,
	}, nil
}

func (s *ShareService) ResolveShare(
	ctx context.Context,
	link domain.ShareLink,
) (*domain.SharePreview, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	// The payload is decoded and schema-checked before any provider call, so a
	// malformed link never reaches the network.
	payload, err := s.decodePayload(link)
	if err != nil {
		return nil, err
	}

	// A share for a game this manager does not have is refused; installing it
	// as the active game would be silently broken.
	if _, ok := games.Get(payload.GameID); !ok {
		return nil, appErrors.Wrap(
			domain.ErrGameNotRegistered,
			appErrors.ErrCodeGameNotRegistered,
			"share targets game %q, which this manager does not have", payload.GameID,
		)
	}

	if link.IsExpired(s.clock()) {
		return nil, appErrors.Wrap(domain.ErrShareExpired, appErrors.ErrCodeShareExpired,
			"share link expired at %s", link.ExpiresAt.Format(time.RFC3339))
	}

	provider, err := s.registry.Get(payload.Provider)
	if err != nil {
		return nil, err
	}

	if provider.SupportsAuth() {
		if err := provider.ValidateCredentials(ctx); err != nil {
			return nil, appErrors.Wrap(domain.ErrCloudAuthRequired,
				appErrors.ErrCodeCloudAuthRequired,
				"provider %q: %s", payload.Provider, err.Error())
		}
	}

	files, err := provider.ResolveShareLink(ctx, link)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed, "resolve share link")
	}

	s.logger.Debug().
		Str("op", "cloud-share").
		Str("provider", payload.Provider).
		Int("payload_version", payload.Version).
		Str("share", link.URL).
		Msg("deep link received")

	preview := s.buildPreview(link, payload, files)

	s.logger.Info().
		Str("op", "cloud-share").
		Str("share_id", preview.ShareID).
		Str("provider", payload.Provider).
		Int("mod_count", len(preview.Mods)).
		Msg("share resolved")

	return preview, nil
}

func (s *ShareService) buildPreview(
	link domain.ShareLink,
	payload domain.SharePayload,
	files []domain.FileReference,
) *domain.SharePreview {
	preview := &domain.SharePreview{
		ShareID: link.URL,
		GameID:  payload.GameID,
		Mods:    payload.Mods,
	}

	// The provider is asked what exists, the payload what it means: a file both
	// describe keeps the payload's path and hash, which the sharer wrote.
	described := make(map[string]domain.SharedFile, len(payload.Files))
	for _, f := range payload.Files {
		described[f.FileID] = f
	}

	for _, ref := range files {
		file := domain.SharedFile{
			FileID: ref.ID,
			Path:   ref.ID,
			Size:   ref.Size,
			Hash:   ref.Hash,
		}

		if known, ok := described[ref.ID]; ok {
			file.Path = known.Path
			file.Hash = known.Hash
			file.RemoteName = known.RemoteName
		}

		preview.Files = append(preview.Files, file)
		preview.TotalSize += file.Size
	}

	return preview
}

func (s *ShareService) InstallShare(
	ctx context.Context,
	link domain.ShareLink,
	opts domain.InstallOptions,
) ([]domain.Mod, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	started := s.clock()

	preview, err := s.ResolveShare(ctx, link)
	if err != nil {
		return nil, err
	}

	// The game is stamped from the share, not the active one; ResolveShare has
	// already rejected a share whose game this manager does not have.
	gameID := preview.GameID
	if gameID == "" {
		if s.gameLocator != nil {
			def, _, err := s.gameLocator.Active()
			if err != nil {
				return nil, appErrors.Wrap(
					err,
					appErrors.ErrCodeGameNotRegistered,
					"share targets unregistered game",
				)
			}

			gameID = def.ID()
		}
	}

	provider, err := s.registry.Get(link.ProviderID)
	if err != nil {
		return nil, err
	}

	// Every archive is staged under one folder that goes away when the install
	// returns, so temp is not left holding a copy of every shared mod.
	staging, err := os.MkdirTemp("", "dmm-share-install-*")
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"create a staging folder for the share")
	}

	defer func() { _ = os.RemoveAll(staging) }()

	var installed []domain.Mod

	for i, sm := range preview.Mods {
		mod := domain.Mod{
			ID:          sm.Name + "-" + sm.Version,
			Name:        sm.Name,
			Version:     sm.Version,
			Author:      sm.Author,
			Description: sm.Description,
			SourceID:    "cloud-share",
			GameID:      gameID,
			Enabled:     true,
		}

		mod.Dependencies = append(mod.Dependencies, sm.Dependencies...)

		if s.modInstaller == nil {
			continue
		}

		// The archive is fetched and verified before anything is installed, so
		// a corrupted download is refused before it can displace a working mod.
		archivePath, err := s.fetchModArchive(ctx, provider, preview, sm, staging, i)
		if err != nil {
			return nil, err
		}

		result, err := s.modInstaller.InstallArchiveFromShare(
			ctx,
			&mod,
			archivePath,
			shareInstallOptions(opts),
		)
		if err != nil {
			s.logger.Warn().Str("op", "cloud-share").Str("mod", mod.Name).Err(err).Msg("install from share failed")
			return nil, err
		}

		result.GameID = gameID
		installed = append(installed, *result)
	}

	s.logger.Info().
		Str("op", "cloud-share").
		Str("share", link.URL).
		Str("share_id", preview.ShareID).
		Str("provider", link.ProviderID).
		Int("installed_count", len(installed)).
		Int64("duration_ms", s.clock().Sub(started).Milliseconds()).
		Msg("share install completed")

	return installed, nil
}

// shareInstallOptions maps the share's install preferences onto the installer's.
// AutoInstall is a property of the share, so it becomes the installer's explicit
// enable flag, not the installer's own auto-detection.
func shareInstallOptions(opts domain.InstallOptions) InstallOptions {
	if !opts.AutoInstall {
		return InstallOptions{}
	}

	enabled := true

	return InstallOptions{Enable: &enabled}
}

// fetchModArchive downloads the archive one shared mod is packed into and
// verifies it against the hash the payload recorded. The check happens here
// because these are the only bytes in hand; after extraction a truncated
// download would only be discovered once its files were already in storage.
func (s *ShareService) fetchModArchive(
	ctx context.Context,
	provider domain.CloudProvider,
	preview *domain.SharePreview,
	sm domain.SharedMod,
	staging string,
	slot int,
) (string, error) {
	if sm.PackedArchive == nil {
		return "", appErrors.New(
			appErrors.ErrCodeShareInvalid,
			"mod %q in this share lists its files individually instead of one packed archive",
			sm.Name,
		)
	}

	packed := *sm.PackedArchive

	// The slot keeps two mods that sanitize to the same name — differing only
	// in a character SanitizeSegment drops — from overwriting each other.
	archivePath := filepath.Join(
		staging,
		fmt.Sprintf("%02d-%s.zip", slot, domain.SanitizeSegment(sm.Name)),
	)

	f, err := os.Create(archivePath)
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"create the download target for %q", sm.Name)
	}

	if err := f.Close(); err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"close the download target for %q", sm.Name)
	}

	ref := domain.FileReference{ID: packed.FileID, Size: packed.Size, Hash: packed.Hash}

	downloadStart := s.clock()

	if err := provider.Download(ctx, ref, archivePath); err != nil {
		return "", appErrors.Wrap(
			err,
			appErrors.ErrCodeCloudDownloadFailed,
			"download %q",
			packed.FileID,
		)
	}

	actual, err := fileHash(archivePath)
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"hash the download of %q", packed.FileID)
	}

	if actual != packed.Hash {
		s.logger.Warn().
			Str("op", "cloud-share").
			Str("file", packed.FileID).
			Str("expected_hash", packed.Hash).
			Str("actual_hash", actual).
			Msg("integrity check failed")

		return "", appErrors.New(
			appErrors.ErrCodeIntegrityMismatch,
			"file %q failed integrity check: expected %s, got %s",
			packed.FileID, packed.Hash, actual,
		)
	}

	s.logger.Info().
		Str("op", "cloud-share").
		Str("file", packed.FileID).
		Int64("bytes", packed.Size).
		Int64("duration_ms", s.clock().Sub(downloadStart).Milliseconds()).
		Msg("shared archive downloaded")

	return archivePath, nil
}

func (s *ShareService) ListShares(ctx context.Context) ([]domain.ShareRecord, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	if s.store == nil {
		return nil, nil
	}

	return s.store.List(ctx)
}

func (s *ShareService) ListSharesByGame(
	ctx context.Context,
	gameID string,
) ([]domain.ShareRecord, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	if s.store == nil {
		return nil, nil
	}

	return s.store.ListByGame(ctx, gameID)
}

// DeleteAllShares removes every share record, used by the factory reset.
func (s *ShareService) DeleteAllShares(ctx context.Context) error {
	if s == nil {
		return appErrors.ErrInvalidArgument
	}

	if s.store == nil {
		return nil
	}

	return s.store.DeleteAll(ctx)
}

// GetShare returns one share record by id.
func (s *ShareService) GetShare(ctx context.Context, id string) (*domain.ShareRecord, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	if s.store == nil {
		return nil, appErrors.ErrShareNotFound
	}

	record, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeShareNotFound, "get share %q", id)
	}

	return record, nil
}

// ListProviders describes every registered cloud provider so a caller can
// render a picker without importing the plugin packages.
func (s *ShareService) ListProviders() []domain.CloudProviderInfo {
	if s == nil || s.registry == nil {
		return nil
	}

	providers := s.registry.List()

	infos := make([]domain.CloudProviderInfo, 0, len(providers))
	for _, p := range providers {
		infos = append(infos, domain.CloudProviderInfo{
			ID:           p.ID(),
			Name:         p.Name(),
			Description:  p.Description(),
			SupportsAuth: p.SupportsAuth(),
			MaxFileSize:  p.MaxFileSize(),
			MaxTotalSize: p.MaxTotalSize(),
		})
	}

	return infos
}

// DefaultProvider reports the provider used when a caller does not name one.
func (s *ShareService) DefaultProvider() string {
	if s == nil || s.config == nil || s.config.DefaultProvider == "" {
		return "s3"
	}

	return s.config.DefaultProvider
}

func (s *ShareService) RevokeShare(ctx context.Context, shareID string) error {
	if s == nil {
		return appErrors.ErrInvalidArgument
	}

	if s.store == nil {
		return appErrors.ErrShareNotFound
	}

	record, err := s.store.Get(ctx, shareID)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeShareNotFound, "get share %q", shareID)
	}

	for _, link := range record.Links {
		provider, err := s.registry.Get(link.ProviderID)
		if err != nil {
			s.logger.Warn().Str("op", "cloud-share").Str("share_id", shareID).Str("provider", link.ProviderID).Err(err).Msg("provider for share link unavailable, skipping")
			continue
		}

		files, err := provider.ResolveShareLink(ctx, link)
		if err != nil {
			s.logger.Warn().Str("op", "cloud-share").Str("share_id", shareID).Str("provider", link.ProviderID).Err(err).Msg("share link could not be resolved, skipping")
			continue
		}

		for _, f := range files {
			_ = provider.DeleteFile(ctx, f)
		}

		s.logger.Info().
			Str("op", "cloud-share").
			Str("share_id", shareID).
			Str("provider", link.ProviderID).
			Int("files", len(files)).
			Msg("share revoked")
	}

	if err := s.store.Delete(ctx, shareID); err != nil {
		return err
	}

	return nil
}

func (s *ShareService) UpdateShare(
	ctx context.Context,
	shareID string,
	addMods []domain.Mod,
	removeMods []domain.Mod,
) (*domain.ShareRecord, error) {
	if s == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	if s.store == nil {
		return nil, appErrors.ErrShareNotFound
	}

	record, err := s.store.Get(ctx, shareID)
	if err != nil {
		return nil, err
	}

	removeIDs := make(map[string]bool)
	for _, m := range removeMods {
		removeIDs[m.ID] = true
	}

	var kept []domain.Mod

	for _, m := range record.Mods {
		if !removeIDs[m.ID] {
			kept = append(kept, m)
		}
	}

	kept = append(kept, addMods...)
	record.Mods = kept
	record.UpdatedAt = s.clock()

	if err := s.store.Save(ctx, record); err != nil {
		return nil, err
	}

	s.logger.Info().
		Str("op", "share-update").
		Str("share_id", shareID).
		Int("added", len(addMods)).
		Int("removed", len(removeMods)).
		Int("mod_count", len(kept)).
		Msg("share record updated")

	return record, nil
}

// encodeLink builds the carrier link: a self-describing payload naming the game,
// the mods and every file, so a recipient sees what they are getting before a
// single byte is downloaded.
//
// The provider's own link is deliberately not the carrier: a Dropbox URL says
// nothing about which game the mods are for, and a provider that shuts down
// leaves a recipient with a dead string.
func (s *ShareService) encodeLink(
	providerID string,
	gameID string,
	refs []domain.FileReference,
	mods []domain.SharedMod,
	expiry *time.Time,
) (domain.ShareLink, error) {
	if s == nil {
		return domain.ShareLink{}, appErrors.ErrInvalidArgument
	}

	payload := domain.SharePayload{
		Version:   1,
		Provider:  providerID,
		GameID:    gameID,
		Files:     sharedFilesFor(refs),
		Mods:      mods,
		CreatedAt: s.clock(),
		ExpiresAt: expiry,
	}

	encoded, err := s.encodePayload(payload)
	if err != nil {
		return domain.ShareLink{}, err
	}

	return domain.ShareLink{
		URL:        domain.ShareLinkFormat(providerID) + encoded,
		ProviderID: providerID,
		ExpiresAt:  expiry,
	}, nil
}

// encodePayload marshals a payload and base64url-encodes it for the carrier
// link. URL encoding, not standard: the payload travels in a path, and
// '+' and '/' would both change meaning there.
func (s *ShareService) encodePayload(payload domain.SharePayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeShareInvalid, "marshal share payload")
	}

	return base64.URLEncoding.EncodeToString(raw), nil
}

// sharedFilesFor projects provider file references into the payload's file
// list. The provider's ID becomes the file's identity: that is what the
// recipient hands back to resolve it.
func sharedFilesFor(refs []domain.FileReference) []domain.SharedFile {
	files := make([]domain.SharedFile, 0, len(refs))

	for _, ref := range refs {
		files = append(files, domain.SharedFile{
			FileID: ref.ID,
			Path:   ref.ID,
			Size:   ref.Size,
			Hash:   ref.Hash,
		})
	}

	return files
}

// deadlineOrZero dereferences an optional expiry for a call that takes a plain
// time. A nil means "no expiry", which the zero value already says.
func deadlineOrZero(expiry *time.Time) time.Time {
	if expiry == nil {
		return time.Time{}
	}

	return *expiry
}

// decodePayload parses the payload out of a carrier link. It is the single place
// that decides a link is ours: a URL in neither carrier form is rejected before
// any provider is contacted.
func (s *ShareService) decodePayload(link domain.ShareLink) (domain.SharePayload, error) {
	providerID, encoded, err := domain.ParseShareURL(link.URL)
	if err != nil {
		return domain.SharePayload{}, err
	}

	if link.ProviderID != "" && providerID != link.ProviderID {
		return domain.SharePayload{}, appErrors.New(
			appErrors.ErrCodeShareInvalid,
			"link is for provider %q but was resolved through %q",
			providerID, link.ProviderID,
		)
	}

	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return domain.SharePayload{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeShareInvalid,
			"decode share payload",
		)
	}

	var payload domain.SharePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return domain.SharePayload{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeShareInvalid,
			"unmarshal share payload",
		)
	}

	if payload.Version != 1 {
		return domain.SharePayload{}, appErrors.New(
			appErrors.ErrCodeShareInvalid, "unsupported payload version %d", payload.Version)
	}

	payload.Provider = providerID

	return payload, nil
}

// ShareLinkFromURL parses either carrier form into a ShareLink plus its
// payload, so a caller that receives a pasted URL gets both halves at once.
func (s *ShareService) ShareLinkFromURL(url string) (domain.ShareLink, domain.SharePayload, error) {
	if s == nil {
		return domain.ShareLink{}, domain.SharePayload{}, appErrors.ErrInvalidArgument
	}

	link := domain.ShareLink{URL: url}

	payload, err := s.decodePayload(link)
	if err != nil {
		return domain.ShareLink{}, domain.SharePayload{}, err
	}

	link.ProviderID = payload.Provider
	link.ExpiresAt = payload.ExpiresAt

	return link, payload, nil
}

func (s *ShareService) resolveGameID(mods []domain.Mod) (string, error) {
	if s == nil {
		return "", appErrors.ErrInvalidArgument
	}

	for i := range mods {
		if mods[i].GameID != "" {
			return mods[i].GameID, nil
		}
	}

	if s.gameLocator != nil {
		def, _, err := s.gameLocator.Active()
		if err != nil {
			return "", err
		}

		return def.ID(), nil
	}

	return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "cannot resolve game id for share")
}

// collectShare packs every mod and builds the upload set and the payload's mod
// list in one pass, because the two are tied together: a payload entry names the
// archive that was packed, and building them separately is how a share ends up
// promising files it never sent.
//
// The archive is named after the mod id, unique within a game, so two mods that
// both contain a shared.zip cannot overwrite each other on the provider.
func (s *ShareService) collectShare(
	ctx context.Context,
	staging string,
	mods []domain.Mod,
) ([]domain.ShareFile, []domain.SharedMod, error) {
	var (
		files     []domain.ShareFile
		sharedMod = make([]domain.SharedMod, 0, len(mods))
	)

	for i := range mods {
		mod := mods[i]

		entry := domain.SharedMod{
			Name:         mod.Name,
			Version:      mod.Version,
			Author:       mod.Author,
			Description:  mod.Description,
			FileIDs:      []string{},
			Dependencies: append([]string(nil), mod.Dependencies...),
		}

		// A mod whose files cannot be read produces no archive, so the whole
		// share is refused, not promising the recipient nothing.
		upload, packed, err := s.packMod(ctx, staging, mod, collectModFiles(s.logger, mod))
		if err != nil {
			return nil, nil, err
		}

		files = append(files, upload)
		entry.FileIDs = append(entry.FileIDs, upload.Path)
		entry.PackedArchive = &packed

		sharedMod = append(sharedMod, entry)
	}

	return files, sharedMod, nil
}

// packMod writes one mod's files into a single archive and reports it both as an
// upload candidate and as the payload's description of it. Packing makes a mod
// one object, not every file it contains: twenty small files are twenty
// chances for the connection to drop, and per-request pricing is per file.
func (s *ShareService) packMod(
	ctx context.Context,
	staging string,
	mod domain.Mod,
	collected []domain.ShareFile,
) (domain.ShareFile, domain.PackedMod, error) {
	if mod.LocalPath == "" {
		return domain.ShareFile{}, domain.PackedMod{}, appErrors.New(
			appErrors.ErrCodeShareInvalid,
			"mod %q has no files on disk to share",
			mod.Name,
		)
	}

	if len(collected) == 0 {
		return domain.ShareFile{}, domain.PackedMod{}, appErrors.New(
			appErrors.ErrCodeShareInvalid,
			"mod %q has no shareable files",
			mod.Name,
		)
	}

	// Entry names are the paths relative to the mod folder, which is what the
	// install on the far side extracts them to.
	entries := make([]storage.PackEntry, 0, len(collected))

	for _, file := range collected {
		entries = append(entries, storage.PackEntry{
			Source: filepath.Join(mod.LocalPath, filepath.FromSlash(file.Path)),
			Name:   file.Path,
		})
	}

	archiveName := domain.SanitizeSegment(mod.ID) + ".zip"

	archivePath := filepath.Join(staging, archiveName)

	result, err := storage.PackArchive(ctx, archivePath, entries)
	if err != nil {
		return domain.ShareFile{}, domain.PackedMod{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeCloudUploadFailed,
			"pack mod %q",
			mod.Name,
		)
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		return domain.ShareFile{}, domain.PackedMod{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeCloudUploadFailed,
			"measure the packed archive of mod %q",
			mod.Name,
		)
	}

	// The hash the recipient checks covers the archive's own bytes; hashing the
	// files inside it would let a truncated archive through.
	hash, err := fileHash(archivePath)
	if err != nil {
		return domain.ShareFile{}, domain.PackedMod{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeCloudUploadFailed,
			"hash the packed archive of mod %q",
			mod.Name,
		)
	}

	upload := domain.ShareFile{
		Path:   archiveName,
		Size:   info.Size(),
		Hash:   hash,
		Source: archivePath,
	}

	// The constructor already stamps the format, which is the only format the
	// packer writes.
	packed := *domain.PackedModFromFile(archiveName, info.Size(), hash)

	s.logger.Debug().
		Str("op", "cloud-share").
		Str("mod", mod.Name).
		Str("archive", archiveName).
		Int("entries", len(result.Files)).
		Int64("packed_bytes", info.Size()).
		Int64("payload_bytes", result.TotalSize).
		Msg("mod packed for sharing")

	return upload, packed, nil
}

// collectModFiles reads a mod's directory into hash-checked files.
//
// The walk is recursive: a Darktide mod is usually a folder of folders, and a
// top-level-only read would ship a mod that loads and does nothing.
//
// A file that cannot be hashed is logged and skipped, not failing the
// share, because one unreadable file should not cost the user the other nineteen.
func collectModFiles(logger zerolog.Logger, mod domain.Mod) []domain.ShareFile {
	if mod.LocalPath == "" {
		return nil
	}

	entries, err := storage.CollectTree(mod.LocalPath)
	if err != nil {
		logger.Warn().
			Str("op", "cloud-share").
			Str("mod", mod.Name).
			Str("path", mod.LocalPath).
			Err(err).
			Msg("failed to read mod directory")

		return nil
	}

	files := make([]domain.ShareFile, 0, len(entries))

	for _, entry := range entries {
		hash, err := fileHash(entry.Source)
		if err != nil {
			logger.Warn().
				Str("op", "cloud-share").
				Str("mod", mod.Name).
				Str("path", entry.Path).
				Err(err).
				Msg("failed to hash file")

			continue
		}

		files = append(files, domain.ShareFile{
			Path:   entry.Path,
			Source: entry.Source,
			Size:   entry.Size,
			Hash:   hash,
		})
	}

	return files
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func joinErrors(errs []error) string {
	if len(errs) == 0 {
		return "unknown"
	}

	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}

	return strings.Join(msgs, "; ")
}
