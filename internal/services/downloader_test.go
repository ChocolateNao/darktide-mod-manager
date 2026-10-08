package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fetchSource is a source that can also download, so it satisfies Downloader the
// way every bundled source does. The two calls are recorded rather than performed:
// what these tests are about is the dispatch, not the transfer.
type fetchSource struct {
	dlErr     error
	verifyErr error
	downloads []string
	verifies  []string
	scriptSource
}

func (s *fetchSource) Download(
	_ context.Context,
	mod *domain.Mod,
	destPath string,
) (string, error) {
	s.downloads = append(s.downloads, destPath)
	if s.dlErr != nil {
		return "", s.dlErr
	}

	return destPath, nil
}

func (s *fetchSource) Verify(_ context.Context, mod *domain.Mod, path string) error {
	s.verifies = append(s.verifies, path)
	return s.verifyErr
}

// plainSource cannot download at all, which is what a metadata-only provider
// would look like to the cache.
type plainSource struct {
	scriptSource
}

func downloaderRegistry(t *testing.T, sources ...domain.ModSource) *SourceRegistry {
	t.Helper()

	registry := NewSourceRegistry()

	for _, source := range sources {
		{
			err := registry.Register(source)
			require.NoError(t, err, "register %s: %v", source.ID(), err)
		}
	}

	return registry
}

func TestRegistryDownloaderDelegatesToTheModsSource(t *testing.T) {
	github := &fetchSource{scriptSource: scriptSource{id: "github"}}
	local := &fetchSource{scriptSource: scriptSource{id: "local"}}
	downloader := NewRegistryDownloader(downloaderRegistry(t, github, local))

	// One cache serves every provider, so the download must reach the source that
	// owns the mod rather than a single pinned one.
	{
		_, err := downloader.Download(context.Background(),
			&domain.Mod{ID: "m", SourceID: "local"}, "/tmp/local.zip")
		require.NoError(t, err, "download from local: %v", err)
	}

	assert.False(
		t,
		len(local.downloads) != 1 || local.downloads[0] != "/tmp/local.zip",
		"local downloads = %v, want one call for the mod's own source",
		local.downloads,
	)

	assert.Empty(
		t,
		github.downloads,
		"the github source was asked for a local mod: %v",
		github.downloads,
	)

	{
		err := downloader.Verify(context.Background(),
			&domain.Mod{ID: "m", SourceID: "github"}, "/tmp/github.zip")
		require.NoError(t, err, "verify against github: %v", err)
	}

	assert.False(
		t,
		len(github.verifies) != 1 || github.verifies[0] != "/tmp/github.zip",
		"github verifies = %v, want the verified path recorded",
		github.verifies,
	)

	assert.Empty(t, local.verifies, "the local source verified a github mod: %v", local.verifies)
}

func TestRegistryDownloaderPropagatesTheSourcesFailure(t *testing.T) {
	downloader := NewRegistryDownloader(downloaderRegistry(t,
		&fetchSource{scriptSource: scriptSource{id: "nexus"}, dlErr: errBoom, verifyErr: errBoom}))

	_, err := downloader.Download(
		context.Background(),
		&domain.Mod{ID: "m", SourceID: "nexus"},
		"/tmp/x.zip",
	)
	assert.True(t, errors.Is(err, errBoom), "error = %v, want the source's own failure", err)

	{
		err := downloader.Verify(
			context.Background(),
			&domain.Mod{ID: "m", SourceID: "nexus"},
			"/tmp/x.zip",
		)
		assert.True(t, errors.Is(
			err,
			errBoom,
		), "error = %v, want the source's own verification failure", err)
	}
}

func TestRegistryDownloaderReportsAnUnknownSource(t *testing.T) {
	downloader := NewRegistryDownloader(downloaderRegistry(t,
		&fetchSource{scriptSource: scriptSource{id: "local"}}))

	_, err := downloader.Download(
		context.Background(),
		&domain.Mod{ID: "m", SourceID: "steam"},
		"/tmp/x.zip",
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceNotFound),
		"error = %v, want SOURCE_NOT_FOUND",
		err,
	)

	{
		err := downloader.Verify(
			context.Background(),
			&domain.Mod{ID: "m", SourceID: "steam"},
			"/tmp/x.zip",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "error = %v, want SOURCE_NOT_FOUND", err)
	}
}

func TestRegistryDownloaderNamesASourceThatCannotDownload(t *testing.T) {
	downloader := NewRegistryDownloader(
		downloaderRegistry(t, &plainSource{scriptSource{id: "catalogue"}}),
	)

	_, err := downloader.Download(
		context.Background(),
		&domain.Mod{ID: "m", SourceID: "catalogue"},
		"/tmp/x.zip",
	)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"error = %v, want SOURCE_UNAVAILABLE",
		err,
	)
	// The message has to name the source: "cannot download" without saying which
	// one leaves the user looking through every provider they have configured.
	assert.Contains(
		t,
		appErrors.Message(err),
		"catalogue",
		"error = %v, want it to name the source",
		err,
	)

	// A source that claims no digest has nothing to verify against, so reporting
	// a failure would reject every mod it lists.
	{
		err := downloader.Verify(
			context.Background(),
			&domain.Mod{ID: "m", SourceID: "catalogue"},
			"/tmp/x.zip",
		)
		assert.NoError(
			t,
			err,
			"verify against a source that cannot download returned %v, want nil",
			err,
		)
	}
}

func TestRegistryDownloaderRefusesANilMod(t *testing.T) {
	downloader := NewRegistryDownloader(downloaderRegistry(t,
		&fetchSource{scriptSource: scriptSource{id: "local"}}))

	{
		_, err := downloader.Download(
			context.Background(),
			nil,
			"/tmp/x.zip",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "error = %v, want INVALID_ARGUMENT for a nil mod", err)
	}

	{
		err := downloader.Verify(context.Background(), nil, "/tmp/x.zip")
		assert.NoError(t, err, "verify with a nil mod returned %v, want nil", err)
	}
}

func TestRegistryDownloaderSurvivesNoRegistry(t *testing.T) {
	var downloader *RegistryDownloader

	mod := &domain.Mod{ID: "m", SourceID: "local"}

	// A nil receiver must answer rather than panic: the cache manager is built
	// before the registry in one wiring path, and a manager that cannot start is
	// worse than one that reports a missing plugin.
	{
		_, err := downloader.Download(
			context.Background(),
			mod,
			"/tmp/x.zip",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceUnavailable,
		), "error = %v, want SOURCE_UNAVAILABLE", err)
	}

	{
		err := downloader.Verify(
			context.Background(),
			mod,
			"/tmp/x.zip",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceUnavailable,
		), "error = %v, want SOURCE_UNAVAILABLE", err)
	}

	empty := NewRegistryDownloader(nil)
	{
		_, err := empty.Download(
			context.Background(),
			mod,
			"/tmp/x.zip",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceUnavailable,
		), "error = %v, want SOURCE_UNAVAILABLE with no registry", err)
	}
}

func TestRegistryDownloaderHonoursACancelledContext(t *testing.T) {
	source := &fetchSource{scriptSource: scriptSource{id: "local"}}
	downloader := NewRegistryDownloader(downloaderRegistry(t, source))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		_, err := downloader.Download(
			ctx,
			&domain.Mod{ID: "m", SourceID: "local"},
			"/tmp/x.zip",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "error = %v, want OPERATION_CANCELED", err)
	}
	// The registry builds sources without a context because that work is local,
	// so the cancellation has to be checked here or it would be dropped entirely.
	assert.Empty(
		t,
		source.downloads,
		"a cancelled download still reached the source: %v",
		source.downloads,
	)

	{
		err := downloader.Verify(
			ctx,
			&domain.Mod{ID: "m", SourceID: "local"},
			"/tmp/x.zip",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "error = %v, want OPERATION_CANCELED", err)
	}
}

func TestRegistryDownloaderToleratesANilContext(t *testing.T) {
	source := &fetchSource{scriptSource: scriptSource{id: "local"}}
	downloader := NewRegistryDownloader(downloaderRegistry(t, source))

	//nolint:staticcheck // nil context passing is being tested here
	if _, err := downloader.Download(
		nil,
		&domain.Mod{ID: "m", SourceID: "local"},
		"/tmp/x.zip",
	); err != nil { //nolint:staticcheck // exercised deliberately
		assert.NoError(t, err, "download with a nil context returned %v", err)
	}
}

// TestRegistryDownloaderFetchesThroughTheRealCache is the reason this type
// exists: the cache manager holds one downloader, and without this adapter every
// install stops before the first byte with "no downloader is configured".
func TestRegistryDownloaderFetchesThroughTheRealCache(t *testing.T) {
	source := &fetchSource{scriptSource: scriptSource{id: "local"}}

	cacheStore, err := storage.NewCache(t.TempDir())
	require.NoError(t, err, "storage.NewCache")

	t.Cleanup(func() { _ = cacheStore.Close() })

	manager, err := NewCacheManager(CacheManagerDeps{
		Cache:    cacheStore,
		Config:   domain.DefaultConfig().Cache,
		Download: NewRegistryDownloader(downloaderRegistry(t, source)),
		Clock:    func() time.Time { return cacheNow },
	})
	require.NoError(t, err, "new cache manager")

	// The scripted source refuses to write a file, so the transfer must fail at
	// the validation step rather than at the missing-downloader check. That is
	// the distinction this test exists to pin.
	mod := &domain.Mod{
		ID: "better_ui", Version: "1.2.0", SourceID: "local",
		SourceData: map[string]any{"local_path": filepath.Join(t.TempDir(), "better_ui")},
	}

	_, err = manager.GetOrDownload(context.Background(), mod)
	require.Error(t, err, "a source that writes no file was reported as a successful download")

	require.False(t, appErrors.CodeOf(err) == appErrors.ErrCodeSourceUnavailable &&
		strings.Contains(
			appErrors.Message(err),
			"no downloader",
		), "error = %v, want the download to have been attempted", err)

	assert.Len(
		t,
		source.downloads,
		1,
		"the source was asked to download %d times, want exactly 1",
		len(source.downloads),
	)

	{
		_, statErr := os.Stat(source.downloads[0])
		assert.True(
			t,
			os.IsNotExist(statErr),
			"a staging file survived the failed download at %q",
			source.downloads[0],
		)
	}
}
