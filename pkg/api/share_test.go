package api

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shareFakeProvider is a CloudProvider that records what the orchestrator asked
// of it. It exists to prove the api layer drives the orchestrator correctly, not
// to exercise S3: nothing here touches the network.
type shareFakeProvider struct {
	id          string
	uploaded    []domain.ShareFile
	deleted     []domain.FileReference
	authChecks  int
	requireAuth bool
	failUpload  bool
}

func (p *shareFakeProvider) ID() string          { return p.id }
func (p *shareFakeProvider) Name() string        { return "Fake" }
func (p *shareFakeProvider) Description() string { return "a fake provider" }

func (p *shareFakeProvider) SupportsAuth() bool  { return p.requireAuth }
func (p *shareFakeProvider) MaxFileSize() int64  { return 0 }
func (p *shareFakeProvider) MaxTotalSize() int64 { return 0 }

func (p *shareFakeProvider) Authenticate(domain.CloudCredentials) error { return nil }
func (p *shareFakeProvider) ValidateCredentials(context.Context) error {
	p.authChecks++
	return nil
}
func (p *shareFakeProvider) ForgetCredentials() {}

func (p *shareFakeProvider) Upload(
	_ context.Context,
	files []domain.ShareFile,
	_ domain.UploadOptions,
) (*domain.UploadResult, error) {
	if p.failUpload {
		return nil, appErrors.New(appErrors.ErrCodeCloudUploadFailed, "upload refused")
	}

	p.uploaded = append(p.uploaded, files...)

	refs := make([]domain.FileReference, 0, len(files))
	for _, f := range files {
		refs = append(refs, domain.FileReference{ID: f.Path, Size: f.Size, Hash: f.Hash})
	}

	return &domain.UploadResult{References: refs}, nil
}

func (p *shareFakeProvider) Download(
	_ context.Context,
	ref domain.FileReference,
	destPath string,
) error {
	return nil
}

func (p *shareFakeProvider) DeleteFile(_ context.Context, ref domain.FileReference) error {
	p.deleted = append(p.deleted, ref)
	return nil
}

func (p *shareFakeProvider) CreateShareLink(
	_ context.Context,
	files []domain.FileReference,
	opts domain.ShareOptions,
) (*domain.ShareLink, error) {
	var expiry *time.Time
	if !opts.Expiry.IsZero() {
		expiry = &opts.Expiry
	}

	return &domain.ShareLink{
		URL:        "https://dmm.app/s/" + p.id + "/abc",
		ProviderID: p.id,
		ExpiresAt:  expiry,
	}, nil
}

func (p *shareFakeProvider) ResolveShareLink(_ context.Context,
	link domain.ShareLink,
) ([]domain.FileReference, error) {
	return []domain.FileReference{{ID: "mod.txt", Size: 3, Hash: "abc"}}, nil
}

// shareMemStore is an in-memory ShareStore.
type shareMemStore struct {
	shares []domain.ShareRecord
	mu     sync.RWMutex
}

func (s *shareMemStore) Save(_ context.Context, share *domain.ShareRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.shares {
		if existing.ID == share.ID {
			s.shares[i] = *share
			return nil
		}
	}

	s.shares = append(s.shares, *share)

	return nil
}

func (s *shareMemStore) Get(_ context.Context, id string) (*domain.ShareRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.shares {
		if s.shares[i].ID == id {
			copied := s.shares[i]
			return &copied, nil
		}
	}

	return nil, appErrors.ErrShareNotFound
}

func (s *shareMemStore) List(_ context.Context) ([]domain.ShareRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]domain.ShareRecord, len(s.shares))
	copy(out, s.shares)

	return out, nil
}

func (s *shareMemStore) ListByGame(_ context.Context, gameID string) ([]domain.ShareRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.ShareRecord

	for i := range s.shares {
		if s.shares[i].GameID == gameID {
			out = append(out, s.shares[i])
		}
	}

	return out, nil
}

func (s *shareMemStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.shares {
		if s.shares[i].ID == id {
			s.shares = append(s.shares[:i], s.shares[i+1:]...)
			return nil
		}
	}

	return appErrors.ErrShareNotFound
}

func (s *shareMemStore) GetByLink(
	_ context.Context,
	link domain.ShareLink,
) (*domain.ShareRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.shares {
		for _, l := range s.shares[i].Links {
			if l.URL == link.URL {
				copied := s.shares[i]
				return &copied, nil
			}
		}
	}

	return nil, appErrors.ErrShareNotFound
}

// shareStubLocator answers Active() with a registered game definition.
type shareStubLocator struct {
	def domain.GameDefinition
}

func (l shareStubLocator) Active() (domain.GameDefinition, domain.GameSettings, error) {
	return l.def, domain.GameSettings{}, nil
}

// shareStubInstaller records installation attempts.
type shareStubInstaller struct {
	installed []string
	mu        sync.Mutex
}

func (i *shareStubInstaller) Install(
	_ context.Context,
	modID, sourceID string,
	_ services.InstallOptions,
) (*domain.Mod, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.installed = append(i.installed, modID)

	return &domain.Mod{ID: modID, SourceID: sourceID}, nil
}

func (i *shareStubInstaller) InstallArchiveFromShare(
	_ context.Context,
	mod *domain.Mod,
	_ string,
	_ services.InstallOptions,
) (*domain.Mod, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.installed = append(i.installed, mod.ID)

	return mod.Clone(), nil
}

// shareClient builds a Client over a real ShareService with fake deps.
func shareClient(
	t *testing.T,
	provider *shareFakeProvider,
) (*Client, *shareMemStore, *shareStubInstaller) {
	t.Helper()

	require.NoError(t, games.EnsureBuiltins(), "EnsureBuiltins")

	store := &shareMemStore{}
	installer := &shareStubInstaller{}

	registry := services.NewCloudRegistry()
	require.NoError(t, registry.Register(provider), "register provider")

	svc := services.NewShareService(services.ShareServiceDeps{
		Registry:     registry,
		Store:        store,
		GameLocator:  shareStubLocator{def: games.MustGet("darktide")},
		ModInstaller: installer,
		Logger:       zerolog.Nop(),
		Config:       services.DefaultShareConfig(),
		Clock: func() time.Time {
			return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		},
	})

	client, err := NewClientFromServices(ClientDeps{Shares: svc})
	require.NoError(t, err, "NewClientFromServices")

	t.Cleanup(func() { _ = client.Close() })

	return client, store, installer
}

// shareModOnDisk returns a mod with a real file on disk.
func shareModOnDisk(t *testing.T) domain.Mod {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "payload.lua"), []byte("-- mod"), 0o600))

	return domain.Mod{
		ID:        "mod-a",
		Name:      "Mod A",
		Version:   "1.0",
		GameID:    "darktide",
		LocalPath: dir,
	}
}

func TestAPICreateShareReturnsLinkAndPersistsRecord(t *testing.T) {
	provider := &shareFakeProvider{id: "fake"}
	client, _, _ := shareClient(t, provider)

	mod := shareModOnDisk(t)

	link, err := client.CreateShare(context.Background(), []Mod{mod}, []string{"fake"})
	require.NoError(t, err, "CreateShare")

	require.NotEmpty(t, link.Primary.URL, "primary link URL")
	assert.Equal(t, "fake", link.Primary.ProviderID, "provider id")
	assert.NotEmpty(t, link.ShareID, "share id")

	shares, err := client.ListShares(context.Background())
	require.NoError(t, err, "ListShares")

	require.Len(t, shares, 1, "persisted records")
	assert.Equal(t, "darktide", shares[0].GameID, "stamped game id")
	require.Len(t, shares[0].Mods, 1, "mods in record")
	assert.Equal(t, "mod-a", shares[0].Mods[0].ID, "mod id in record")
	require.Len(t, shares[0].Links, 1, "links in record")
	assert.NotNil(t, shares[0].Links[0].ExpiresAt, "expiry stamped on the stored link")
}

func TestAPICreateShareWithoutModsFails(t *testing.T) {
	client, _, _ := shareClient(t, &shareFakeProvider{id: "fake"})

	_, err := client.CreateShare(context.Background(), nil, nil)
	require.Error(t, err, "CreateShare with no mods")
	assert.Equal(t, appErrors.ErrCodeInvalidArgument, appErrors.CodeOf(err), "error code")
}

func TestAPICreateShareUnknownProviderFails(t *testing.T) {
	client, _, _ := shareClient(t, &shareFakeProvider{id: "fake"})

	mod := shareModOnDisk(t)

	_, err := client.CreateShare(context.Background(), []Mod{mod}, []string{"nope"})
	require.Error(t, err, "CreateShare with unknown provider")
	assert.Equal(t, appErrors.ErrCodeAllProvidersFailed, appErrors.CodeOf(err), "error code")
}

func TestAPIListSharesByGameFilters(t *testing.T) {
	client, store, _ := shareClient(t, &shareFakeProvider{id: "fake"})

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Save(context.Background(), &domain.ShareRecord{
		ID: "s1", GameID: "darktide", Mods: []domain.Mod{{ID: "a"}}, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.Save(context.Background(), &domain.ShareRecord{
		ID: "s2", GameID: "other", Mods: []domain.Mod{{ID: "b"}}, CreatedAt: now, UpdatedAt: now,
	}))

	got, err := client.ListSharesByGame(context.Background(), "darktide")
	require.NoError(t, err, "ListSharesByGame")

	require.Len(t, got, 1, "records for the game")
	assert.Equal(t, "s1", got[0].ID, "record id")
}

func TestAPIListSharesWithoutShareServiceFails(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	defer func() { _ = client.Close() }()

	_, err = client.ListShares(context.Background())
	require.Error(t, err, "ListShares without a share service")
}

func TestAPIListShareProvidersDescribesProvider(t *testing.T) {
	client, _, _ := shareClient(t, &shareFakeProvider{id: "fake"})

	providers, err := client.ListShareProviders()
	require.NoError(t, err, "ListShareProviders")

	require.Len(t, providers, 1, "descriptions")
	assert.Equal(t, "fake", providers[0].ID, "provider id")
	assert.Equal(t, "Fake", providers[0].Name, "provider name")
}

func TestAPIDefaultShareProviderFallsBackWhenUnavailable(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	defer func() { _ = client.Close() }()

	assert.Equal(t, "s3", client.DefaultShareProvider(), "fallback provider")
}

func TestAPIResolveShareWithoutShareServiceFails(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	defer func() { _ = client.Close() }()

	_, err = client.ResolveShare(context.Background(), ShareLink{URL: "https://dmm.app/s/fake/abc"})
	require.Error(t, err, "ResolveShare without a share service")
}

func TestAPIRevokeShareUnknownIDFails(t *testing.T) {
	client, _, _ := shareClient(t, &shareFakeProvider{id: "fake"})

	err := client.RevokeShare(context.Background(), "nope")
	require.Error(t, err, "RevokeShare unknown id")
	assert.Equal(t, appErrors.ErrCodeShareNotFound, appErrors.CodeOf(err), "error code")
}
