package services

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeProvider is a CloudProvider that records calls and fails on demand.
// It is the seam the whole orchestrator is designed around: every behaviour
// below is exercised without a network in sight.
// content maps a file id to the bytes the provider "holds" for it. Download
// writes those bytes out, so the integrity check has something real to hash —
// a provider that writes nothing would make every hash mismatch, and the test
// could not tell a working check from a broken one.
type fakeProvider struct {
	link        domain.ShareLink
	resolveErr  error
	deleteErr   error
	authErr     error
	validateErr error
	uploadErr   error
	content     map[string]string
	id          string
	created     []string
	deleted     []string
	downloaded  []string
	uploads     [][]domain.ShareFile
	refs        []domain.FileReference
	mu          sync.Mutex
	needsAuth   bool
}

func newFakeProvider(id string) *fakeProvider {
	return &fakeProvider{id: id, content: map[string]string{}}
}

func (p *fakeProvider) ID() string          { return p.id }
func (p *fakeProvider) Name() string        { return "Fake " + p.id }
func (p *fakeProvider) Description() string { return "a provider for tests" }
func (p *fakeProvider) SupportsAuth() bool  { return p.needsAuth }
func (p *fakeProvider) MaxFileSize() int64  { return 0 }
func (p *fakeProvider) MaxTotalSize() int64 { return 0 }

func (p *fakeProvider) Authenticate(domain.CloudCredentials) error { return nil }
func (p *fakeProvider) ValidateCredentials(context.Context) error  { return p.validateErr }
func (p *fakeProvider) ForgetCredentials()                         {}

func (p *fakeProvider) Upload(
	_ context.Context,
	files []domain.ShareFile,
	_ domain.UploadOptions,
) (*domain.UploadResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.uploadErr != nil {
		return nil, p.uploadErr
	}

	p.uploads = append(p.uploads, files)

	// A real provider keeps the bytes it was handed, so the download on the far
	// side gets the archive the sharer packed rather than a re-fabricated one.
	// Reading them here is what makes the happy path's hash check honest.
	for _, f := range files {
		if f.Source == "" {
			continue
		}

		raw, err := os.ReadFile(f.Source)
		if err != nil {
			return nil, err
		}

		if p.content == nil {
			p.content = map[string]string{}
		}

		p.content[f.Path] = string(raw)
	}

	refs := make([]domain.FileReference, 0, len(files))
	for _, f := range files {
		refs = append(refs, domain.FileReference{ID: f.Path, Size: f.Size, Hash: f.Hash})
	}

	return &domain.UploadResult{References: refs}, nil
}

func (p *fakeProvider) Download(
	_ context.Context,
	ref domain.FileReference,
	destPath string,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.downloaded = append(p.downloaded, ref.ID+"->"+destPath)

	bytes, ok := p.content[ref.ID]
	if !ok {
		return fmt.Errorf("no object %q on this provider", ref.ID)
	}

	return os.WriteFile(destPath, []byte(bytes), 0o600)
}

func (p *fakeProvider) DeleteFile(_ context.Context, ref domain.FileReference) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.deleted = append(p.deleted, ref.ID)

	return p.deleteErr
}

func (p *fakeProvider) CreateShareLink(
	_ context.Context,
	_ []domain.FileReference,
	opts domain.ShareOptions,
) (*domain.ShareLink, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.created = append(p.created, p.id)

	link := p.link
	if link.ProviderID == "" {
		link.ProviderID = p.id
	}

	if !opts.Expiry.IsZero() {
		expiry := opts.Expiry
		link.ExpiresAt = &expiry
	}

	return &link, nil
}

func (p *fakeProvider) ResolveShareLink(
	_ context.Context,
	_ domain.ShareLink,
) ([]domain.FileReference, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.resolveErr != nil {
		return nil, p.resolveErr
	}

	return p.refs, nil
}

func (p *fakeProvider) uploadCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.uploads)
}

// refNames returns the ids Download was asked for, in order. A share that
// packs transfers one archive per mod, so this is also the assertion that a mod
// went up and came back as a single object.
func (p *fakeProvider) refNames() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	names := make([]string, 0, len(p.downloaded))
	for _, entry := range p.downloaded {
		id, _, _ := strings.Cut(entry, "->")
		names = append(names, id)
	}

	return names
}

// tamper swaps what the provider will hand back for a file, standing in for a
// truncated or substituted upload on the way down.
func tamper(t *testing.T, provider *fakeProvider, id string) {
	t.Helper()

	provider.mu.Lock()
	defer provider.mu.Unlock()

	if provider.content == nil {
		provider.content = map[string]string{}
	}

	provider.content[id] += " - tampered"
}

// recordingInstaller captures the options a share install passes down, which is
// how the AutoInstall translation is observed without a real ModService.
type recordingInstaller struct {
	record func(*domain.Mod, InstallOptions)
}

func (i *recordingInstaller) Install(
	_ context.Context,
	modID, _ string,
	_ InstallOptions,
) (*domain.Mod, error) {
	return &domain.Mod{ID: modID}, nil
}

func (i *recordingInstaller) InstallArchiveFromShare(
	_ context.Context,
	mod *domain.Mod,
	archivePath string,
	opts InstallOptions,
) (*domain.Mod, error) {
	i.record(mod, opts)

	out := mod.Clone()
	out.LocalPath = archivePath

	return out, nil
}

// archiveSpy inspects each archive while the install still holds it, and hands
// back a mod stamped with where it came from.
type archiveSpy struct {
	onArchive func(string)
}

func (a *archiveSpy) Install(
	_ context.Context,
	modID, _ string,
	_ InstallOptions,
) (*domain.Mod, error) {
	return &domain.Mod{ID: modID}, nil
}

func (a *archiveSpy) InstallArchiveFromShare(
	_ context.Context,
	mod *domain.Mod,
	archivePath string,
	_ InstallOptions,
) (*domain.Mod, error) {
	a.onArchive(archivePath)

	return mod.Clone(), nil
}

// memShareStore is an in-memory ShareStore.
type memShareStore struct {
	getErr error
	shares map[string]*domain.ShareRecord
	mu     sync.Mutex
}

func newMemShareStore() *memShareStore {
	return &memShareStore{shares: map[string]*domain.ShareRecord{}}
}

func (s *memShareStore) Save(_ context.Context, share *domain.ShareRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	saved := *share
	s.shares[share.ID] = &saved

	return nil
}

func (s *memShareStore) Get(_ context.Context, id string) (*domain.ShareRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	share, ok := s.shares[id]
	if s.getErr != nil {
		return nil, s.getErr
	}

	if !ok {
		return nil, domain.ErrShareNotFound
	}

	out := *share

	return &out, nil
}

func (s *memShareStore) List(_ context.Context) ([]domain.ShareRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]domain.ShareRecord, 0, len(s.shares))
	for _, share := range s.shares {
		out = append(out, *share)
	}

	return out, nil
}

func (s *memShareStore) ListByGame(_ context.Context, gameID string) ([]domain.ShareRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []domain.ShareRecord

	for _, share := range s.shares {
		if share.GameID == gameID {
			out = append(out, *share)
		}
	}

	return out, nil
}

func (s *memShareStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.shares[id]; !ok {
		return domain.ErrShareNotFound
	}

	delete(s.shares, id)

	return nil
}

func (s *memShareStore) GetByLink(
	_ context.Context,
	link domain.ShareLink,
) (*domain.ShareRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, share := range s.shares {
		for _, l := range share.Links {
			if l.URL == link.URL {
				out := *share

				return &out, nil
			}
		}
	}

	return nil, domain.ErrShareNotFound
}

// stubLocator resolves the active game without touching the filesystem.
type stubLocator struct {
	id    string
	found bool
}

func (l *stubLocator) Active() (domain.GameDefinition, domain.GameSettings, error) {
	if !l.found {
		return nil, domain.GameSettings{}, appErrors.ErrGameNotFound
	}

	return stubID(l.id), domain.GameSettings{}, nil
}

// stubID returns a GameDefinition whose ID is id.
func stubID(id string) domain.GameDefinition {
	return stubDef{id: id}
}

type stubDef struct{ id string }

func (d stubDef) ID() string                                   { return d.id }
func (d stubDef) DisplayName() string                          { return d.id }
func (d stubDef) Description() string                          { return "" }
func (d stubDef) SteamAppID() string                           { return "" }
func (d stubDef) InstallDirNames() []string                    { return nil }
func (d stubDef) BinaryNames() []string                        { return nil }
func (d stubDef) VersionMarkers() []string                     { return nil }
func (d stubDef) ModsPathCandidates() [][]string               { return nil }
func (d stubDef) ModsDirName() string                          { return "" }
func (d stubDef) ModInstallMethod() string                     { return "" }
func (d stubDef) ManifestNames() []string                      { return nil }
func (d stubDef) LoadOrderFile() string                        { return "" }
func (d stubDef) InstallModes() []domain.ModInstallMode        { return nil }
func (d stubDef) SourceDefaults() map[string]map[string]string { return nil }

func (d stubDef) GameActions() []domain.GameAction { return nil }

// stubInstaller records installs without running any of the real machinery.
type stubInstaller struct {
	installErr error
	failFor    map[string]error
	installed  []domain.Mod
	archives   []string
}

func (i *stubInstaller) Install(
	_ context.Context,
	modID, sourceID string,
	opts InstallOptions,
) (*domain.Mod, error) {
	if i.installErr != nil {
		return nil, i.installErr
	}

	if err, ok := i.failFor[modID]; ok {
		return nil, err
	}

	for _, m := range i.installed {
		if m.ID == modID {
			out := m
			out.ID = modID
			out.SourceID = sourceID

			return &out, nil
		}
	}

	return &domain.Mod{ID: modID, SourceID: sourceID}, nil
}

// InstallArchiveFromShare records the archive it was handed instead of placing
// anything. The orchestrator's job is to get a verified archive to this seam;
// what the installer does with it is covered by the mod service's own tests.
func (i *stubInstaller) InstallArchiveFromShare(
	_ context.Context,
	mod *domain.Mod,
	archivePath string,
	_ InstallOptions,
) (*domain.Mod, error) {
	i.archives = append(i.archives, archivePath)

	if i.installErr != nil {
		return nil, i.installErr
	}

	if err, ok := i.failFor[mod.ID]; ok {
		return nil, err
	}

	out := mod.Clone()

	return out, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// shareClock is the instant every shareServiceFor-built service reports. It is
// pinned so expiry assertions can be relative to it rather than to the wall
// clock, which would drift as the test run crosses a day boundary.
var shareClock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func shareServiceFor(t *testing.T, provider domain.CloudProvider, store ShareStore) *ShareService {
	t.Helper()

	return shareServiceWith(t, []domain.CloudProvider{provider}, store)
}

func shareServiceWith(
	t *testing.T,
	providers []domain.CloudProvider,
	store ShareStore,
) *ShareService {
	t.Helper()

	registry := NewCloudRegistry()
	for _, p := range providers {
		require.NoError(t, registry.Register(p))
	}

	return NewShareService(ShareServiceDeps{
		Registry:     registry,
		Store:        store,
		GameLocator:  &stubLocator{id: "darktide", found: true},
		ModInstaller: &stubInstaller{},
		Logger:       zerolog.Nop(),
		Config:       DefaultShareConfig(),
		Clock:        func() time.Time { return shareClock },
	})
}

// testMods returns one mod with real files on disk. A share packs what it finds,
// so a fixture without a LocalPath is refused rather than silently producing an
// empty archive.
func testMods(t *testing.T, gameID string) []domain.Mod {
	return []domain.Mod{{
		ID:        "mod-a",
		Name:      "Mod A",
		Version:   "1.0.0",
		GameID:    gameID,
		LocalPath: modDirOnDisk(t, "mod-a"),
	}}
}

// ---------------------------------------------------------------------------
// CreateShare
// ---------------------------------------------------------------------------

func TestCreateShareUploadsAndBuildsLink(t *testing.T) {
	provider := &fakeProvider{id: "s3"}
	store := newMemShareStore()

	svc := shareServiceFor(t, provider, store)

	result, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})
	require.NoError(t, err)

	assert.Contains(t, result.Primary.URL, domain.ShareLinkFormat("s3"))
	assert.Equal(t, "s3", result.Primary.ProviderID)
	assert.Empty(t, result.Fallbacks)
	assert.NotEmpty(t, result.ShareID)

	assert.Equal(t, 1, provider.uploadCount())

	saved, err := store.Get(context.Background(), result.ShareID)
	require.NoError(t, err, "share record should be persisted")

	assert.Equal(t, "darktide", saved.GameID, "share must carry the game id")
	assert.Len(t, saved.Mods, 1)
}

func TestCreateShareStampsGameIDFromActiveGame(t *testing.T) {
	// Mods with an empty GameID must not produce a blank share: the game comes
	// from the active game, and a share without one cannot be resolved.
	provider := &fakeProvider{id: "s3"}
	store := newMemShareStore()

	svc := shareServiceFor(t, provider, store)

	mods := []domain.Mod{{ID: "mod-a", Name: "Mod A", Version: "1.0.0"}}
	mods[0].LocalPath = modDirOnDisk(t, "mod-a")

	result, err := svc.CreateShare(context.Background(), mods, []string{"s3"})
	require.NoError(t, err)

	saved, err := store.Get(context.Background(), result.ShareID)
	require.NoError(t, err)

	assert.Equal(t, "darktide", saved.GameID)
}

func TestCreateShareRefusesModsFromAnotherGame(t *testing.T) {
	provider := &fakeProvider{id: "s3"}
	svc := shareServiceFor(t, provider, newMemShareStore())

	_, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})
	require.NoError(t, err)

	// A mix of games is the real hazard: the game id stamped on the record is
	// the first mod's, so the other mod's files land in the wrong place.
	mixed := []domain.Mod{
		{ID: "a", Name: "A", Version: "1", GameID: "darktide"},
		{ID: "b", Name: "B", Version: "1", GameID: "other"},
	}

	_, err = svc.CreateShare(context.Background(), mixed, []string{"s3"})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "game")
}

func TestCreateShareWithNoModsIsRejected(t *testing.T) {
	svc := shareServiceFor(t, &fakeProvider{id: "s3"}, newMemShareStore())

	_, err := svc.CreateShare(context.Background(), nil, []string{"s3"})
	assert.Error(t, err)
}

func TestCreateShareUnknownProviderIsRejected(t *testing.T) {
	svc := shareServiceFor(t, &fakeProvider{id: "s3"}, newMemShareStore())

	_, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"gdrive"})
	require.Error(t, err)

	// The unknown provider is one of possibly several, so it is reported
	// inside the aggregate rather than as the sole failure.
	assert.Contains(t, err.Error(), "gdrive")
}

func TestCreateShareAuthFailureIsReported(t *testing.T) {
	provider := &fakeProvider{
		id:          "s3",
		needsAuth:   true,
		validateErr: errors.New("token expired"),
	}
	svc := shareServiceFor(t, provider, newMemShareStore())

	_, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})
	require.Error(t, err)

	assert.ErrorIs(t, err, domain.ErrAllProvidersFailed)
	assert.Equal(t, 0, provider.uploadCount(), "nothing should be uploaded when auth fails")
}

func TestCreateShareMultiProviderFallback(t *testing.T) {
	broken := &fakeProvider{id: "s3", uploadErr: errors.New("bucket full")}
	good := &fakeProvider{id: "dropbox"}

	svc := shareServiceWith(t, []domain.CloudProvider{broken, good}, newMemShareStore())

	result, err := svc.CreateShare(
		context.Background(),
		testMods(t, "darktide"),
		[]string{"s3", "dropbox"},
	)
	require.NoError(t, err)

	// The link is the carrier form the orchestrator encodes, and it names the
	// provider that actually took the upload.
	assert.Contains(t, result.Primary.URL, domain.ShareLinkFormat("dropbox"))
	assert.Equal(t, "dropbox", result.Primary.ProviderID)
	assert.Equal(t, 0, broken.uploadCount(), "the failed provider must not be in the result")
}

func TestCreateShareAllProvidersFail(t *testing.T) {
	provider := &fakeProvider{id: "s3", uploadErr: errors.New("nope")}
	svc := shareServiceFor(t, provider, newMemShareStore())

	_, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})

	assert.ErrorIs(t, err, domain.ErrAllProvidersFailed)
}

func TestCreateShareDefaultsProviderWhenNoneGiven(t *testing.T) {
	provider := &fakeProvider{id: "s3"}
	svc := shareServiceFor(t, provider, nil)

	// DefaultShareConfig names s3, so an empty list is not an error.
	_, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), nil)
	require.NoError(t, err)

	assert.Equal(t, 1, provider.uploadCount())
}

func TestCreateShareAppliesExpiryFromConfig(t *testing.T) {
	provider := &fakeProvider{id: "s3"}
	svc := shareServiceFor(t, provider, newMemShareStore())

	result, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})
	require.NoError(t, err)

	require.NotNil(t, result.Primary.ExpiresAt, "expiry should be stamped on the link")
	assert.True(t, result.Primary.ExpiresAt.After(shareClock))
}

// ---------------------------------------------------------------------------
// ResolveShare
// ---------------------------------------------------------------------------

// shareLinkFor builds a link whose URL carries a payload for mods, the way the
// real CreateShare does.
func shareLinkFor(t *testing.T, gameID string) domain.ShareLink {
	t.Helper()

	payload := domain.SharePayload{
		Version:  1,
		Provider: "s3",
		GameID:   gameID,
		Files:    []domain.SharedFile{{FileID: "a.zip", Path: "a.zip", Size: 10, Hash: "abc"}},
		Mods: []domain.SharedMod{{
			Name: "Mod A", Version: "1.0.0", FileIDs: []string{"a.zip"},
		}},
	}

	link := domain.ShareLink{
		URL:        domain.DeepLinkFormat("s3") + encodePayload(t, payload),
		ProviderID: "s3",
	}

	return link
}

func TestResolveSharePreviewsMods(t *testing.T) {
	provider := &fakeProvider{
		id:   "s3",
		refs: []domain.FileReference{{ID: "a.zip", Size: 10}},
	}
	svc := shareServiceFor(t, provider, newMemShareStore())

	link := shareLinkFor(t, "darktide")

	preview, err := svc.ResolveShare(context.Background(), link)
	require.NoError(t, err)

	assert.Equal(
		t,
		"darktide",
		preview.GameID,
		"preview must say which game the share installs into",
	)
	require.Len(t, preview.Mods, 1)
	assert.Equal(t, "Mod A", preview.Mods[0].Name)
	require.Len(t, preview.Files, 1)
	assert.Equal(t, int64(10), preview.TotalSize)
}

func TestResolveShareRejectsUnregisteredGame(t *testing.T) {
	// The guard that matters most: a Darktide share must never resolve into
	// another game's storage.
	provider := &fakeProvider{
		id:   "s3",
		refs: []domain.FileReference{{ID: "a.zip", Size: 10}},
	}
	svc := shareServiceFor(t, provider, newMemShareStore())

	link := shareLinkFor(t, "sietch")

	_, err := svc.ResolveShare(context.Background(), link)
	require.Error(t, err)

	assert.ErrorIs(t, err, domain.ErrGameNotRegistered)
}

func TestResolveShareRejectsExpiredLink(t *testing.T) {
	provider := &fakeProvider{id: "s3"}
	svc := shareServiceFor(t, provider, newMemShareStore())

	link := shareLinkFor(t, "darktide")
	expired := shareClock.Add(-time.Hour)
	link.ExpiresAt = &expired

	_, err := svc.ResolveShare(context.Background(), link)

	assert.ErrorIs(t, err, domain.ErrShareExpired)
}

func TestResolveShareRequiresAuth(t *testing.T) {
	provider := &fakeProvider{id: "s3", needsAuth: true, validateErr: errors.New("no token")}
	svc := shareServiceFor(t, provider, newMemShareStore())

	_, err := svc.ResolveShare(context.Background(), shareLinkFor(t, "darktide"))

	assert.ErrorIs(t, err, domain.ErrCloudAuthRequired)
}

func TestResolveShareRejectsMalformedLink(t *testing.T) {
	provider := &fakeProvider{id: "s3"}
	svc := shareServiceFor(t, provider, newMemShareStore())

	_, err := svc.ResolveShare(
		context.Background(),
		domain.ShareLink{URL: "garbage", ProviderID: "s3"},
	)

	assert.ErrorIs(t, err, domain.ErrShareInvalid)
}

// ---------------------------------------------------------------------------
// InstallShare
// ---------------------------------------------------------------------------

// modOnDisk writes a mod with one file, shares it, and returns the link a
// recipient would be handed for it.
//
// The mod is packed by the real packer, so the hash in the payload is the
// archive's and the fake provider stores the archive bytes Upload read off
// disk. The round trip is therefore genuine: what is verified on install is the
// same set of bytes that was packed.
func modOnDisk(t *testing.T, provider *fakeProvider) domain.ShareLink {
	t.Helper()

	modDir := modDirOnDisk(t, "mod-a")

	svc := shareServiceFor(t, provider, newMemShareStore())

	result, err := svc.CreateShare(context.Background(), []domain.Mod{{
		ID:        "mod-a",
		Name:      "Mod A",
		Version:   "1.0.0",
		GameID:    "darktide",
		LocalPath: modDir,
	}}, []string{"s3"})
	require.NoError(t, err)

	return result.Primary
}

// modDirOnDisk writes a mod folder holding one file and returns its path.
func modDirOnDisk(t *testing.T, modID string) string {
	t.Helper()

	dir := t.TempDir()
	modDir := filepath.Join(dir, modID)
	require.NoError(t, os.MkdirAll(modDir, 0o700))

	content := "contents"
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "shared.zip"), []byte(content), 0o600))

	return modDir
}

func TestInstallShareStampsGameID(t *testing.T) {
	provider := newFakeProvider("s3")
	link := modOnDisk(t, provider)

	svc := shareServiceFor(t, provider, newMemShareStore())

	installed, err := svc.InstallShare(context.Background(), link, domain.InstallOptions{})
	require.NoError(t, err)

	require.Len(t, installed, 1)
	assert.Equal(t, "darktide", installed[0].GameID, "mods must install into the share's game")
}

func TestInstallShareHandsOverAPackedArchive(t *testing.T) {
	// The whole point of the archive: the installer is given one verified file,
	// not a list of loose files it has to reassemble.
	provider := newFakeProvider("s3")
	link := modOnDisk(t, provider)

	// The staging folder is cleaned up once the install returns, so the archive
	// is inspected at the seam — the only moment it is guaranteed to be there.
	var (
		paths  []string
		opened []string
	)

	installer := &archiveSpy{onArchive: func(path string) {
		paths = append(paths, path)

		if _, err := zip.OpenReader(path); err == nil {
			opened = append(opened, "readable")
		}
	}}

	svc := shareServiceFor(t, provider, newMemShareStore())
	svc.modInstaller = installer

	_, err := svc.InstallShare(context.Background(), link, domain.InstallOptions{})
	require.NoError(t, err)

	require.Len(t, paths, 1, "each mod must arrive as one archive")
	assert.True(t, strings.HasSuffix(paths[0], ".zip"), "the staged file must be an archive")
	assert.Equal(t, []string{"readable"}, opened, "the staged file must be a real zip")
}

func TestInstallShareHonoursPromisedHash(t *testing.T) {
	// The happy path must stay green if the check is going to be worth
	// anything: the bytes the sharer uploaded are the bytes the recipient
	// verifies.
	provider := newFakeProvider("s3")
	link := modOnDisk(t, provider)

	svc := shareServiceFor(t, provider, newMemShareStore())

	_, err := svc.InstallShare(context.Background(), link, domain.InstallOptions{})
	require.NoError(t, err)

	require.NotEmpty(t, provider.downloaded, "the archive should have been fetched")
	assert.Equal(t, []string{"mod-a.zip"}, provider.refNames())
}

func TestInstallShareRejectsHashMismatch(t *testing.T) {
	// The sharer promised one hash and the provider returned different bytes.
	// Silent acceptance would install whatever the provider sent, which is
	// exactly what a truncated or swapped upload looks like.
	provider := newFakeProvider("s3")
	link := modOnDisk(t, provider)

	// The provider now hands back different bytes than the ones it was given.
	// The payload promised the sharer's hash, so this must fail the check.
	tamper(t, provider, "mod-a.zip")

	svc := shareServiceFor(t, provider, newMemShareStore())

	_, err := svc.InstallShare(context.Background(), link, domain.InstallOptions{})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "integrity")
}

func TestInstallShareRefusesAModThatWasNeverPacked(t *testing.T) {
	// A payload built by hand can describe a mod without naming the archive it
	// arrived in. Guessing at its layout would install whatever the guess put
	// on disk, so it is refused instead.
	provider := newFakeProvider("s3")
	provider.refs = []domain.FileReference{{ID: "loose.txt", Size: 3}}
	provider.content["loose.txt"] = "abc"

	svc := shareServiceFor(t, provider, newMemShareStore())

	payload := domain.SharePayload{
		Version:  1,
		Provider: "s3",
		GameID:   "darktide",
		Files:    []domain.SharedFile{{FileID: "loose.txt", Path: "loose.txt", Size: 3}},
		Mods: []domain.SharedMod{{
			Name: "Loose", Version: "1.0.0", FileIDs: []string{"loose.txt"},
		}},
	}

	_, err := svc.InstallShare(context.Background(), domain.ShareLink{
		URL:        domain.DeepLinkFormat("s3") + encodePayload(t, payload),
		ProviderID: "s3",
	}, domain.InstallOptions{})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "packed")
}

func TestInstallShareStopsAtTheFirstFailure(t *testing.T) {
	// A failure part-way through must not leave the earlier mods installed and
	// the later ones missing: the caller sees an error, not a half share.
	provider := newFakeProvider("s3")
	link := modOnDisk(t, provider)
	tamper(t, provider, "mod-a.zip")

	installer := &stubInstaller{}
	svc := shareServiceFor(t, provider, newMemShareStore())
	svc.modInstaller = installer

	_, err := svc.InstallShare(context.Background(), link, domain.InstallOptions{})
	require.Error(t, err)

	assert.Empty(t, installer.archives, "nothing should reach the installer")
}

func TestInstallShareMapsAutoInstall(t *testing.T) {
	// AutoInstall is the sharer's request, so it has to become an explicit
	// enable rather than being left to the recipient's folder detection.
	provider := newFakeProvider("s3")
	link := modOnDisk(t, provider)

	var got InstallOptions

	installer := &recordingInstaller{record: func(_ *domain.Mod, opts InstallOptions) {
		got = opts
	}}

	svc := shareServiceFor(t, provider, newMemShareStore())
	svc.modInstaller = installer

	_, err := svc.InstallShare(
		context.Background(),
		link,
		domain.InstallOptions{AutoInstall: true},
	)
	require.NoError(t, err)

	require.NotNil(t, got.Enable, "auto install must ask the installer to enable")
	assert.True(t, *got.Enable)
}

func TestInstallShareRejectsUnregisteredGame(t *testing.T) {
	provider := &fakeProvider{id: "s3"}
	svc := shareServiceFor(t, provider, newMemShareStore())

	_, err := svc.InstallShare(
		context.Background(),
		shareLinkFor(t, "sietch"),
		domain.InstallOptions{},
	)

	assert.ErrorIs(t, err, domain.ErrGameNotRegistered)
}

func TestShareLinkForNamesThePackedArchive(t *testing.T) {
	// The payload a real share builds names the archive per mod, so a link that
	// predates packing is visibly different from one this manager makes.
	provider := newFakeProvider("s3")
	link := modOnDisk(t, provider)

	_, payload, err := shareServiceFor(t, provider, nil).ShareLinkFromURL(link.URL)
	require.NoError(t, err)

	require.Len(t, payload.Mods, 1)
	require.NotNil(t, payload.Mods[0].PackedArchive)
	assert.Equal(t, "mod-a.zip", payload.Mods[0].PackedArchive.FileID)
	assert.Equal(t, domain.ArchiveFormatZip, payload.Mods[0].PackedArchive.Format)
	assert.NotEmpty(t, payload.Mods[0].PackedArchive.Hash)
	assert.Equal(t, []string{"mod-a.zip"}, payload.Mods[0].FileIDs)
}

// ---------------------------------------------------------------------------
// Share management
// ---------------------------------------------------------------------------

func TestListSharesReturnsStored(t *testing.T) {
	store := newMemShareStore()

	svc := shareServiceFor(t, &fakeProvider{id: "s3"}, store)

	result, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})
	require.NoError(t, err)

	shares, err := svc.ListShares(context.Background())
	require.NoError(t, err)

	require.Len(t, shares, 1)
	assert.Equal(t, result.ShareID, shares[0].ID)
}

func TestListSharesByGameFilters(t *testing.T) {
	store := newMemShareStore()

	require.NoError(t, store.Save(context.Background(), &domain.ShareRecord{
		ID:        uuid.New().String(),
		GameID:    "darktide",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}))

	require.NoError(t, store.Save(context.Background(), &domain.ShareRecord{
		ID:        uuid.New().String(),
		GameID:    "other",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}))

	svc := shareServiceFor(t, &fakeProvider{id: "s3"}, store)

	shares, err := svc.ListSharesByGame(context.Background(), "darktide")
	require.NoError(t, err)

	require.Len(t, shares, 1)
	assert.Equal(t, "darktide", shares[0].GameID)
}

func TestRevokeShareDeletesRemoteFiles(t *testing.T) {
	provider := &fakeProvider{
		id:   "s3",
		refs: []domain.FileReference{{ID: "a.zip", Size: 10}},
	}
	store := newMemShareStore()

	svc := shareServiceFor(t, provider, store)

	result, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})
	require.NoError(t, err)

	require.NoError(t, svc.RevokeShare(context.Background(), result.ShareID))

	assert.Equal(t, []string{"a.zip"}, provider.deleted, "remote files must be deleted")

	_, err = store.Get(context.Background(), result.ShareID)
	assert.ErrorIs(t, err, domain.ErrShareNotFound)
}

func TestRevokeUnknownShareIsNotFound(t *testing.T) {
	svc := shareServiceFor(t, &fakeProvider{id: "s3"}, newMemShareStore())

	err := svc.RevokeShare(context.Background(), "nope")

	assert.ErrorIs(t, err, domain.ErrShareNotFound)
}

func TestUpdateShareAddsAndRemovesMods(t *testing.T) {
	store := newMemShareStore()
	svc := shareServiceFor(t, &fakeProvider{id: "s3"}, store)

	created, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})
	require.NoError(t, err)

	updated, err := svc.UpdateShare(
		context.Background(),
		created.ShareID,
		[]domain.Mod{{ID: "b", Name: "B", Version: "1"}},
		[]domain.Mod{{ID: "mod-a", Name: "Mod A", Version: "1.0.0"}},
	)
	require.NoError(t, err)

	names := make([]string, 0, len(updated.Mods))
	for _, m := range updated.Mods {
		names = append(names, m.Name)
	}

	assert.Equal(t, []string{"B"}, names)
}

// ---------------------------------------------------------------------------
// Link parsing
// ---------------------------------------------------------------------------

func TestShareLinkFromURLAcceptsBothForms(t *testing.T) {
	svc := shareServiceFor(t, &fakeProvider{id: "s3"}, newMemShareStore())

	payload := domain.SharePayload{
		Version:  1,
		Provider: "s3",
		GameID:   "darktide",
	}

	encoded := encodePayload(t, payload)

	for _, url := range []string{
		domain.ShareLinkFormat("s3") + encoded,
		domain.DeepLinkFormat("s3") + encoded,
	} {
		t.Run(url, func(t *testing.T) {
			link, got, err := svc.ShareLinkFromURL(url)
			require.NoError(t, err)

			assert.Equal(t, "s3", link.ProviderID)
			assert.Equal(t, "darktide", got.GameID)
			assert.True(t, strings.HasPrefix(link.URL, domain.ShareLinkFormat("s3")) ||
				strings.HasPrefix(link.URL, domain.DeepLinkFormat("s3")))
		})
	}
}

func TestShareLinkFromURLRejectsGarbage(t *testing.T) {
	svc := shareServiceFor(t, &fakeProvider{id: "s3"}, newMemShareStore())

	_, _, err := svc.ShareLinkFromURL("not a link at all")

	assert.ErrorIs(t, err, domain.ErrShareInvalid)
}

// ---------------------------------------------------------------------------
// Nil safety
// ---------------------------------------------------------------------------

func TestShareServiceNilReceiverIsSafe(t *testing.T) {
	var svc *ShareService

	_, err := svc.CreateShare(context.Background(), testMods(t, "darktide"), []string{"s3"})
	assert.Error(t, err)

	_, err = svc.ResolveShare(context.Background(), shareLinkFor(t, "darktide"))
	assert.Error(t, err)

	_, err = svc.InstallShare(
		context.Background(),
		shareLinkFor(t, "darktide"),
		domain.InstallOptions{},
	)
	assert.Error(t, err)

	_, err = svc.ListShares(context.Background())
	assert.Error(t, err)

	assert.Error(t, svc.RevokeShare(context.Background(), "x"))
}

func TestShareServiceClockDefaultsToNow(t *testing.T) {
	// A nil clock must not panic: the constructor substitutes time.Now.
	svc := NewShareService(ShareServiceDeps{Logger: zerolog.Nop()})

	before := time.Now()
	at := svc.clock()

	assert.False(t, at.Before(before.Add(-time.Second)))
}

func encodePayload(t *testing.T, p domain.SharePayload) string {
	t.Helper()

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	return base64.URLEncoding.EncodeToString(raw)
}

func TestListProvidersDescribesRegistry(t *testing.T) {
	provider := &fakeProvider{id: "s3"}
	svc := shareServiceFor(t, provider, newMemShareStore())

	infos := svc.ListProviders()
	require.Len(t, infos, 1)

	assert.Equal(t, "s3", infos[0].ID)
	assert.Equal(t, "Fake s3", infos[0].Name)
}
