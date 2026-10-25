package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------- fixtures

var cacheNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// newRealCache builds a manager over a real on-disk cache backed by an in-memory
// index, so these tests stay honest about blob paths, atomic renames and the index
// schema; only the downloader and the clock are faked.
func newRealCache(t *testing.T) (*CacheManager, *storage.Cache) {
	t.Helper()

	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err, "storage.NewCache")

	t.Cleanup(func() { _ = cache.Close() })

	m, err := NewCacheManager(CacheManagerDeps{
		Cache: cache,
		Clock: func() time.Time { return cacheNow },
	})
	require.NoError(t, err, "NewCacheManager")

	return m, cache
}

// scriptDownloader serves a fixed body per mod id and counts calls.
type scriptDownloader struct {
	err         error
	verifyErr   error
	release     chan struct{}
	bodies      map[string][]byte
	perMod      map[string]int
	entered     chan struct{}
	relPath     string
	observed    []string
	verifyFails int
	calls       atomic.Int64
	enteredOnce sync.Once
	mu          sync.Mutex
	empty       bool
	noWrite     bool
}

func (d *scriptDownloader) Download(
	ctx context.Context,
	mod *domain.Mod,
	destPath string,
) (string, error) {
	d.calls.Add(1)
	d.mu.Lock()
	d.observed = append(d.observed, destPath)
	d.perMod[mod.ID]++
	body, haveBody := d.bodies[mod.ID]
	release, entered := d.release, d.entered
	d.mu.Unlock()

	if entered != nil {
		d.enteredOnce.Do(func() { close(entered) })
	}

	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	if d.err != nil {
		return "", d.err
	}

	if d.noWrite {
		return destPath, nil
	}

	target := destPath
	if d.relPath != "" {
		target = filepath.Join(filepath.Dir(destPath), d.relPath)
	}

	if d.empty {
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			return "", err
		}

		return target, nil
	}

	if !haveBody {
		body = []byte("payload:" + mod.ID)
	}

	if err := os.WriteFile(target, body, 0o644); err != nil {
		return "", err
	}

	return target, nil
}

func (d *scriptDownloader) Verify(ctx context.Context, mod *domain.Mod, path string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.verifyFails > 0 {
		d.verifyFails--
		if d.verifyErr != nil {
			return d.verifyErr
		}

		return errors.New("verification failed")
	}

	return nil
}

// failVerification makes the next n Verify calls fail. The count is what lets a
// test model "the cached copy is bad but a fresh download is good".
func (d *scriptDownloader) failVerification(n int, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.verifyFails = n
	d.verifyErr = err
}

func (d *scriptDownloader) callCount() int { return int(d.calls.Load()) }

func (d *scriptDownloader) modCalls(id string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.perMod[id]
}

func newDownloader() *scriptDownloader {
	return &scriptDownloader{bodies: map[string][]byte{}, perMod: map[string]int{}}
}

// withDownloader attaches a downloader and rebuilds the manager around the same
// real cache.
func withDownloader(t *testing.T, m *CacheManager, d Downloader) *CacheManager {
	t.Helper()

	next, err := NewCacheManager(CacheManagerDeps{
		Cache:    m.cache,
		Config:   m.config,
		Download: d,
		Clock:    m.now,
	})
	require.NoError(t, err, "NewCacheManager")

	return next
}

// downloadMod builds a mod that identifies its payload by URL.
func downloadMod(id, version, url string) *domain.Mod {
	return &domain.Mod{
		ID:         id,
		Version:    version,
		SourceID:   "local",
		SourceData: map[string]any{"download_url": url},
	}
}

// stubEvents collects published events.
type stubEvents struct {
	events []Event
	mu     sync.Mutex
}

func (s *stubEvents) Publish(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(s.events, e)
}

func (s *stubEvents) types() []EventType {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]EventType, 0, len(s.events))
	for _, e := range s.events {
		out = append(out, e.Type)
	}

	return out
}

func (s *stubEvents) hasType(want EventType) bool {
	for _, got := range s.types() {
		if got == want {
			return true
		}
	}

	return false
}

// errReader fails after n bytes, standing in for a connection that drops
// mid-transfer.
type errReader struct {
	remaining int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("connection reset")
	}

	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}

	for i := 0; i < n; i++ {
		p[i] = 'x'
	}

	r.remaining -= n

	return n, nil
}

// --------------------------------------------------------------- construction

func TestNewCacheManagerRequiresACacheStore(t *testing.T) {
	_, err := NewCacheManager(CacheManagerDeps{})
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"nil cache: got %v, want ErrCodeInvalidArgument",
		err,
	)
}

func TestNewCacheManagerFillsInMissingLimits(t *testing.T) {
	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err)

	defer func() { _ = cache.Close() }()

	m, err := NewCacheManager(CacheManagerDeps{Cache: cache})
	require.NoError(t, err, "NewCacheManager")

	cfg := m.Config()
	assert.False(
		t,
		cfg.MaxSizeGB != defaultMaxSizeGB || cfg.MaxAgeDays != defaultMaxAgeDays,
		"limits = %dGB/%dd, want the defaults %d/%d",
		cfg.MaxSizeGB,
		cfg.MaxAgeDays,
		defaultMaxSizeGB,
		defaultMaxAgeDays,
	)
	// A zero struct must not silently disable integrity checking.
	assert.True(t, cfg.VerifyHashes, "VerifyHashes defaulted to false")
}

func TestNewCacheManagerRejectsNegativeLimits(t *testing.T) {
	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err)

	defer func() { _ = cache.Close() }()

	m, err := NewCacheManager(CacheManagerDeps{
		Cache:  cache,
		Config: domain.CacheConfig{MaxSizeGB: -1, MaxAgeDays: -5},
	})
	require.NoError(t, err, "NewCacheManager")

	assert.False(
		t,
		m.Config().MaxSizeGB != defaultMaxSizeGB || m.Config().MaxAgeDays != defaultMaxAgeDays,
		"negative limits were kept: %+v",
		m.Config(),
	)
}

func TestNewCacheManagerHonoursExplicitLimits(t *testing.T) {
	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err)

	defer func() { _ = cache.Close() }()

	m, err := NewCacheManager(CacheManagerDeps{
		Cache:  cache,
		Config: domain.CacheConfig{MaxSizeGB: 12, MaxAgeDays: 3},
	})
	require.NoError(t, err)

	assert.False(
		t,
		m.Config().MaxSizeGB != 12 || m.Config().MaxAgeDays != 3,
		"explicit limits were overwritten: %+v",
		m.Config(),
	)
}

func TestRootPathAndConfigAreExposed(t *testing.T) {
	m, cache := newRealCache(t)
	assert.Equal(
		t,
		cache.RootPath(),
		m.RootPath(),
		"RootPath() = %q, want %q",
		m.RootPath(),
		cache.RootPath(),
	)
	// Config hands out a copy: mutating it must not retune the live manager.
	cfg := m.Config()

	//nolint:govet // This is required for the test
	cfg.MaxSizeGB = 999
	assert.NotEqual(t, 999, m.Config().MaxSizeGB, "Config() exposed the manager's own struct")
}

// ------------------------------------------------------------------- CacheKey

func TestCacheKeyRejectsModsWithNothingToCache(t *testing.T) {
	{
		_, err := CacheKey(nil)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
			"nil mod: got %v, want ErrCodeInvalidArgument",
			err,
		)
	}
	// A mod with no source data at all cannot be downloaded, so refusing to
	// mint a key is better than caching under a key nothing will ever fill.
	{
		_, err := CacheKey(
			&domain.Mod{ID: "x", Version: "1"},
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "mod with no download location: got %v, want ErrCodeInvalidArgument", err)
	}
}

func TestCacheKeyIsStableAndUsableAsAFileName(t *testing.T) {
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	first, err := CacheKey(mod)
	require.NoError(t, err, "CacheKey")

	second, err := CacheKey(mod)
	require.NoError(t, err, "CacheKey")

	assert.Equal(t, second, first, "key is not deterministic: %q vs %q", first, second)

	{
		_, err := storage.NormalizeCacheKey(first)
		assert.NoError(t, err, "key %q is not a usable file name: %v", first, err)
	}
}

func TestCacheKeySeparatesVersions(t *testing.T) {
	a, err := CacheKey(downloadMod("ui", "1.0.0", "https://example.test/ui.zip"))
	require.NoError(t, err)

	b, err := CacheKey(downloadMod("ui", "2.0.0", "https://example.test/ui.zip"))
	require.NoError(t, err)
	// The same URL serving two versions must not share one blob.
	assert.NotEqual(t, b, a, "two versions of the same mod share a cache key")
}

func TestCacheKeyDistinguishesAnEmptyVersionFromAMissingOne(t *testing.T) {
	// "url\x00" and "url\x00" are the same input, so this documents that the
	// separator stops ("url", "1.0") colliding with ("url\x001.0", "").
	a, err := CacheKey(downloadMod("a", "1.0", "https://example.test/x"))
	require.NoError(t, err)

	b, err := CacheKey(downloadMod("b", "", "https://example.test/x1.0"))
	require.NoError(t, err)

	assert.NotEqual(t, b, a, "the version separator does not prevent a cross-mod collision")
}

func TestCacheKeyIsSharedAcrossModsWithTheSamePayload(t *testing.T) {
	// Two ids pointing at the same archive are one download, which is the whole
	// point of keying on location plus version instead of on mod id.
	a, err := CacheKey(downloadMod("alias-a", "1.0", "https://example.test/same.zip"))
	require.NoError(t, err)

	b, err := CacheKey(downloadMod("alias-b", "1.0", "https://example.test/same.zip"))
	require.NoError(t, err)

	assert.Equal(t, b, a, "aliases of one payload got different keys")
}

func TestCacheKeyAcceptsAlternativeSourceDataSpellings(t *testing.T) {
	for _, field := range []string{"download_url", "downloadUrl", "file_url", "url", "download_id", "file_id"} {
		mod := &domain.Mod{ID: "m", Version: "1", SourceData: map[string]any{field: "locator-1"}}

		key, err := CacheKey(mod)
		if err != nil {
			assert.NoError(t, err, "field %q: %v", field, err)
			continue
		}

		want, err := CacheKey(
			&domain.Mod{
				ID:         "m",
				Version:    "1",
				SourceData: map[string]any{"download_url": "locator-1"},
			},
		)
		require.NoError(t, err)

		assert.Equal(t, want, key, "field %q produced a different key than download_url", field)
	}
}

func TestCacheKeyIgnoresBlankAndNonStringLocators(t *testing.T) {
	// A source that fills in an empty placeholder must not poison the key.
	for _, bad := range []any{"", "   ", 42, nil, []string{"x"}} {
		mod := &domain.Mod{ID: "m", Version: "1", SourceData: map[string]any{"download_url": bad}}
		{
			_, err := CacheKey(mod)
			assert.True(
				t,
				appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
				"locator %#v: got %v, want ErrCodeInvalidArgument",
				bad,
				err,
			)
		}
	}
}

func TestCacheKeySkipsABlankLocatorToFindAUsableOne(t *testing.T) {
	mod := &domain.Mod{ID: "m", Version: "1", SourceData: map[string]any{
		"download_url": "  ",
		"file_id":      "real-locator",
	}}
	{
		_, err := CacheKey(mod)
		assert.NoError(t, err, "a later populated field was not used: %v", err)
	}
}

func TestDownloadLocationHandlesNilAndEmpty(t *testing.T) {
	{
		got := DownloadLocation(nil)
		assert.Empty(t, got, "DownloadLocation(nil) = %q, want empty", got)
	}

	{
		got := DownloadLocation(&domain.Mod{ID: "m"})
		assert.Empty(t, got, "DownloadLocation with no source data = %q, want empty", got)
	}

	{
		got := DownloadLocation(&domain.Mod{SourceData: map[string]any{"url": " x "}})
		assert.Equal(t, "x", got, "DownloadLocation did not trim: %q", got)
	}
}

// -------------------------------------------------------------- GetOrDownload

func TestGetOrDownloadDownloadsOnceAndThenServesFromCache(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.bodies["ui"] = []byte("archive bytes")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	first, err := m.GetOrDownload(context.Background(), mod)
	require.NoError(t, err, "first GetOrDownload")

	second, err := m.GetOrDownload(context.Background(), mod)
	require.NoError(t, err, "second GetOrDownload")

	assert.Equal(t, second, first, "paths differ: %q vs %q", first, second)

	assert.Equal(t, 1, d.callCount(), "downloaded %d times, want 1", d.callCount())

	body, err := os.ReadFile(first)
	require.NoError(t, err)

	assert.Equal(
		t,
		"archive bytes",
		string(body),
		"cached body = %q, want %q",
		body,
		"archive bytes",
	)
}

func TestGetOrDownloadRecordsTheBlobInTheIndex(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.bodies["ui"] = []byte("12345")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	path, err := m.GetOrDownload(context.Background(), mod)
	require.NoError(t, err)

	key, err := CacheKey(mod)
	require.NoError(t, err)

	entry, err := m.cache.Index().Get(context.Background(), key)
	require.NoError(t, err, "index Get")

	assert.Equal(t, path, entry.Path, "index path = %q, want %q", entry.Path, path)

	assert.Equal(t, int64(5), entry.SizeBytes, "index size = %d, want 5", entry.SizeBytes)
	// The caller now holds a reference, so pruning must leave this alone.
	assert.Equal(t, 1, entry.Refs, "index refs = %d, want 1", entry.Refs)
}

func TestGetOrDownloadCountsEveryReference(t *testing.T) {
	m, _ := newRealCache(t)
	m = withDownloader(t, m, newDownloader())
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	key, err := CacheKey(mod)
	require.NoError(t, err)

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		{
			_, err := m.GetOrDownload(ctx, mod)
			require.NoError(t, err, "call %d: %v", i, err)
		}
	}

	entry, err := m.cache.Index().Get(ctx, key)
	require.NoError(t, err)

	assert.Equal(t, 3, entry.Refs, "refs = %d, want 3 — every hit holds a reference", entry.Refs)

	for i := 0; i < 2; i++ {
		{
			err := m.Release(ctx, key)
			require.NoError(t, err, "Release %d: %v", i, err)
		}
	}

	entry, err = m.cache.Index().Get(ctx, key)
	require.NoError(t, err)

	assert.Equal(t, 1, entry.Refs, "refs after two releases = %d, want 1", entry.Refs)
}

func TestGetOrDownloadRedownloadsWhenTheBlobVanishes(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")
	ctx := context.Background()

	path, err := m.GetOrDownload(ctx, mod)
	require.NoError(t, err)

	{
		err := os.Remove(path)
		require.NoError(t, err)
	}
	// The index row still points at a file that no longer exists; the manager
	// must notice.
	again, err := m.GetOrDownload(ctx, mod)
	require.NoError(t, err, "GetOrDownload after the blob was deleted")

	assert.Equal(t, 2, d.callCount(), "downloaded %d times, want 2", d.callCount())

	{
		_, err := os.Stat(again)
		assert.NoError(t, err, "re-download did not restore the file: %v", err)
	}
}

func TestGetOrDownloadRedownloadsWhenTheBlobIsTruncated(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.bodies["ui"] = []byte("a long archive body")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")
	ctx := context.Background()

	path, err := m.GetOrDownload(ctx, mod)
	require.NoError(t, err)

	{
		err := os.WriteFile(path, []byte("a"), 0o644)
		require.NoError(t, err)
	}

	{
		_, err := m.GetOrDownload(ctx, mod)
		require.NoError(t, err, "GetOrDownload on a truncated blob: %v", err)
	}

	assert.Equal(
		t,
		2,
		d.callCount(),
		"downloaded %d times, want 2 — a size mismatch must force a refetch",
		d.callCount(),
	)

	body, _ := os.ReadFile(path)
	assert.Equal(t, "a long archive body", string(body), "body after refetch = %q", body)
}

func TestGetOrDownloadRedownloadsWhenTheSourceRejectsTheCopy(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	ctx := context.Background()
	{
		_, err := m.GetOrDownload(ctx, mod)
		require.NoError(t, err)
	}

	// The source now says the cached bytes are not what it published.
	d.failVerification(1, appErrors.New(appErrors.ErrCodeIntegrity, "checksum mismatch"))

	{
		_, err := m.GetOrDownload(ctx, mod)
		require.NoError(t, err, "GetOrDownload after a failed verify: %v", err)
	}

	assert.Equal(t, 2, d.callCount(), "downloaded %d times, want 2", d.callCount())
}

func TestGetOrDownloadFailsOnANonIntegrityVerificationError(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	ctx := context.Background()
	{
		_, err := m.GetOrDownload(ctx, mod)
		require.NoError(t, err)
	}

	// A verify error that is not "these bytes are wrong" — a disk read failure,
	// say — must surface, not trigger an endless re-download loop.
	d.failVerification(1, appErrors.New(appErrors.ErrCodeConfigIO, "input/output error"))

	_, err := m.GetOrDownload(ctx, mod)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeIntegrity),
		"got %v, want ErrCodeIntegrity wrapping the cause",
		err,
	)

	assert.Contains(
		t,
		err.Error(),
		"input/output error",
		"error lost the underlying cause: %v",
		err,
	)

	assert.Equal(
		t,
		1,
		d.callCount(),
		"downloaded %d times, want 1 — a non-integrity error must not refetch",
		d.callCount(),
	)
}

func TestGetOrDownloadRejectsAFirstDownloadThatFailsVerification(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.failVerification(100, appErrors.New(appErrors.ErrCodeIntegrity, "wrong bytes"))
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	_, err := m.GetOrDownload(context.Background(), mod)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeIntegrity),
		"got %v, want ErrCodeIntegrity",
		err,
	)
	// Nothing may be left behind: a failed verification must not leave a blob
	// the next call could pick up.
	key, _ := CacheKey(mod)

	ok, err := m.cache.Exists(context.Background(), key)
	require.NoError(t, err)

	assert.False(t, ok, "a blob survived a failed verification")

	{
		_, err := os.Stat(key + ".partial")
		assert.Error(t, err, "a staging file survived a failed verification")
	}
}

func TestGetOrDownloadRejectsAnEmptyDownload(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.empty = true
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	_, err := m.GetOrDownload(context.Background(), mod)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeDownloadFailed),
		"got %v, want ErrCodeDownloadFailed",
		err,
	)

	assert.Contains(t, err.Error(), "empty", "error should explain the cause: %v", err)
}

func TestGetOrDownloadRejectsADownloadThatWroteNothing(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.noWrite = true
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	_, err := m.GetOrDownload(context.Background(), mod)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeDownloadFailed),
		"got %v, want ErrCodeDownloadFailed",
		err,
	)

	assert.Contains(t, err.Error(), "no file", "error should explain the cause: %v", err)
}

func TestGetOrDownloadMovesADownloaderTempFileIntoPlace(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.relPath = "downloader-temp.bin"
	d.bodies["ui"] = []byte("payload from elsewhere")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	path, err := m.GetOrDownload(context.Background(), mod)
	require.NoError(t, err, "GetOrDownload")

	body, err := os.ReadFile(path)
	require.NoError(t, err)

	assert.Equal(t, "payload from elsewhere", string(body), "body = %q", body)

	{
		_, err := os.Stat(filepath.Join(filepath.Dir(path), "downloader-temp.bin"))
		assert.Error(t, err, "the downloader's temp file was left behind")
	}
	// Nothing may be left in the blob directory that looks like a blob.
	dir := filepath.Dir(path)

	names, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, n := range names {
		assert.NotContains(t, n.Name(), ".partial", "staging file %q survived", n.Name())
	}
}

func TestGetOrDownloadCleansUpStagingAfterADownloadError(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.err = errors.New("connection refused")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	{
		_, err := m.GetOrDownload(context.Background(), mod)
		require.Error(t, err, "expected an error")
	}

	key, _ := CacheKey(mod)
	dir := filepath.Dir(filepath.Join(m.RootPath(), "blobs", key[:2], key))

	names, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return // nothing was created, which is also fine
		}

		require.NoError(t, err)
	}

	for _, n := range names {
		assert.Fail(t, "file %q survived a failed download", n.Name())
	}
}

func TestGetOrDownloadLabelsDownloadFailures(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.err = appErrors.New(appErrors.ErrCodeSourceUnavailable, "launcher is down")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	_, err := m.GetOrDownload(context.Background(), mod)
	// The source's own code survives; what matters is that the mod is named.
	assert.Contains(t, err.Error(), "ui", "error should name the mod: %v", err)
}

func TestGetOrDownloadWithoutADownloaderIsAnError(t *testing.T) {
	m, _ := newRealCache(t)

	_, err := m.GetOrDownload(
		context.Background(),
		downloadMod("ui", "1.0.0", "https://example.test/ui.zip"),
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"got %v, want ErrCodeSourceUnavailable",
		err,
	)
}

func TestGetOrDownloadHonoursAPreCanceledContext(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	m = withDownloader(t, m, d)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.GetOrDownload(ctx, downloadMod("ui", "1.0.0", "https://example.test/ui.zip"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"got %v, want ErrCodeOperationCanceled",
		err,
	)

	assert.Equal(t, 0, d.callCount(), "a canceled context still started a download")
}

func TestGetOrDownloadAcceptsANilContext(t *testing.T) {
	m, _ := newRealCache(t)
	m = withDownloader(t, m, newDownloader())
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")
	//nolint:staticcheck // A nil context is exactly what is under test.
	{
		_, err := m.GetOrDownload(nil, mod)
		assert.NoError(t, err, "GetOrDownload(nil ctx): %v", err)
	}
}

func TestGetOrDownloadPublishesAnEvent(t *testing.T) {
	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err)

	defer func() { _ = cache.Close() }()

	events := &stubEvents{}

	m, err := NewCacheManager(CacheManagerDeps{
		Cache:     cache,
		Download:  newDownloader(),
		Clock:     func() time.Time { return cacheNow },
		EventSink: events,
	})
	require.NoError(t, err)

	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")
	{
		_, err := m.GetOrDownload(context.Background(), mod)
		require.NoError(t, err)
	}
	// Only the first call downloads, so exactly one event even though a second,
	// cache-served call adds none.
	{
		_, err := m.GetOrDownload(context.Background(), mod)
		require.NoError(t, err)
	}

	assert.True(
		t,
		events.hasType(EventCacheDownloaded),
		"events = %v, want one cache.downloaded",
		events.types(),
	)
}

func TestGetOrDownloadWorksWithoutAnEventSink(t *testing.T) {
	m, _ := newRealCache(t)
	m = withDownloader(t, m, newDownloader())
	// A library embedder that does not care about events must not crash.
	{
		_, err := m.GetOrDownload(
			context.Background(),
			downloadMod("ui", "1", "https://example.test/u.zip"),
		)
		assert.NoError(t, err, "GetOrDownload: %v", err)
	}
}

// ------------------------------------------------------- concurrent downloads

// blockingDownloader returns a downloader that parks inside Download until
// release is closed, and reports when the transfer has been entered.
func blockingDownloader(body string) (*scriptDownloader, chan struct{}, chan struct{}) {
	d := newDownloader()
	d.bodies["ui"] = []byte(body)
	d.release = make(chan struct{})
	d.entered = make(chan struct{}, 8)

	return d, d.release, d.entered
}

// waitForJoiners blocks until the in-flight job for key has at least want
// waiters, or the deadline passes. Asserting "the second caller did not return"
// proves nothing, since a goroutine that has not been scheduled has not returned
// either; only a recorded join establishes that the second caller reached fetch.
func waitForJoiners(t *testing.T, m *CacheManager, key string, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		job, ok := m.inflight[key]

		got := -1
		if ok {
			got = job.waiters
		}
		m.mu.Unlock()

		if got >= want {
			return
		}

		time.Sleep(time.Millisecond)
	}

	require.FailNow(t, "no in-flight job for %q reached %d waiters within the deadline", key, want)
}

func TestFetchCollapsesConcurrentCallersOntoOneTransfer(t *testing.T) {
	m, _ := newRealCache(t)
	d, release, entered := blockingDownloader("shared payload")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")
	ctx := context.Background()

	key, err := CacheKey(mod)
	require.NoError(t, err)

	// The first caller owns the transfer and parks inside Download.
	firstPath := make(chan string, 1)
	firstErr := make(chan error, 1)

	go func() {
		p, err := m.fetch(ctx, key, mod)
		firstPath <- p

		firstErr <- err
	}()

	<-entered

	// This goroutine blocks until the transfer ends, so reaching the return below proves
	// the second caller joined, not starting its own.
	secondPath := make(chan string, 1)
	secondErr := make(chan error, 1)

	go func() {
		p, err := m.fetch(ctx, key, mod)
		secondPath <- p

		secondErr <- err
	}()

	waitForJoiners(t, m, key, 1)
	// Both callers must still be parked: neither can finish until release.
	select {
	case p := <-firstPath:
		require.FailNow(t, "the first caller returned %q before the transfer was released", p)
	case <-secondPath:
		require.FailNow(
			t,
			"the second caller returned before the transfer was released, so it did not join",
		)
	default:
	}

	close(release)

	{
		err := <-firstErr
		require.NoError(t, err, "first caller: %v", err)
	}

	{
		err := <-secondErr
		require.NoError(t, err, "second caller: %v", err)
	}

	assert.Equal(t, <-secondPath, <-firstPath, "the joined caller received a different path")

	assert.Equal(t, 1, d.callCount(), "downloaded %d times, want 1", d.callCount())
}

func TestFetchSharesAFailureWithJoinedCallers(t *testing.T) {
	m, _ := newRealCache(t)
	d, release, entered := blockingDownloader("")
	d.err = errors.New("boom")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")
	ctx := context.Background()

	key, err := CacheKey(mod)
	require.NoError(t, err)

	firstErr := make(chan error, 1)

	go func() {
		_, err := m.fetch(ctx, key, mod)
		firstErr <- err
	}()

	<-entered

	secondErr := make(chan error, 1)

	go func() {
		_, err := m.fetch(ctx, key, mod)
		secondErr <- err
	}()

	waitForJoiners(t, m, key, 1)

	select {
	case err := <-secondErr:
		require.FailNow(t, "the second caller returned %v before the transfer was released", err)
	default:
	}

	close(release)

	{
		err := <-firstErr
		assert.Error(t, err, "the first caller got no error")
	}

	{
		err := <-secondErr
		assert.Error(t, err, "the joined caller got no error")
	}

	assert.Equal(
		t,
		1,
		d.callCount(),
		"downloaded %d times, want 1 — a failed transfer must not be retried per caller",
		d.callCount(),
	)
	// The failed key must not be left registered as in-flight, or a later caller
	// would wait on a job that already finished.
	m.mu.Lock()
	remaining := len(m.inflight)
	m.mu.Unlock()

	assert.Equal(t, 0, remaining, "%d in-flight jobs left registered", remaining)
}

func TestGetOrDownloadIsSafeUnderConcurrency(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	d.bodies["ui"] = []byte("shared payload")
	m = withDownloader(t, m, d)
	mod := downloadMod("ui", "1.0.0", "https://example.test/ui.zip")

	const workers = 16

	paths := make([]string, workers)
	errs := make([]error, workers)
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			<-start

			paths[i], errs[i] = m.GetOrDownload(context.Background(), mod)
		}(i)
	}

	close(start)
	wg.Wait()

	// The transfer count is not asserted: a caller arriving after the winner finds
	// the cache, so an exact count would describe scheduling, not behaviour.
	for i := range errs {
		require.Nil(t, errs[i], "worker %d: %v", i, errs[i])

		assert.Equal(t, paths[0], paths[i], "worker %d got %q, want %q", i, paths[i], paths[0])
	}

	assert.NotEqual(t, 0, d.callCount(), "nothing was downloaded")

	assert.False(
		t,
		d.callCount() > workers,
		"downloaded %d times for %d callers",
		d.callCount(),
		workers,
	)
}

func TestDistinctModsDownloadInParallel(t *testing.T) {
	m, _ := newRealCache(t)
	d := newDownloader()
	m = withDownloader(t, m, d)
	mods := []*domain.Mod{
		downloadMod("a", "1", "https://example.test/a.zip"),
		downloadMod("b", "1", "https://example.test/b.zip"),
		downloadMod("c", "1", "https://example.test/c.zip"),
	}

	var wg sync.WaitGroup
	for _, mod := range mods {
		wg.Add(1)
		go func(m2 *domain.Mod) {
			defer wg.Done()

			{
				_, err := m.GetOrDownload(context.Background(), m2)
				assert.NoError(t, err, "mod %s: %v", m2.ID, err)
			}
		}(mod)
	}

	wg.Wait()

	assert.Equal(t, 3, d.callCount(), "downloaded %d times, want 3", d.callCount())
}

// ---------------------------------------------------------------------- Store

func TestStoreWritesAndReturnsABlobPath(t *testing.T) {
	m, _ := newRealCache(t)
	key := strings.Repeat("ab", 32)

	path, err := m.Store(context.Background(), key, strings.NewReader("hello"))
	require.NoError(t, err, "Store")

	body, err := os.ReadFile(path)
	require.NoError(t, err)

	assert.Equal(t, "hello", string(body), "body = %q", body)
}

func TestStoreRejectsBadKeysAndNilReaders(t *testing.T) {
	m, _ := newRealCache(t)

	ctx := context.Background()

	for _, bad := range []string{"", "  ", "nothex", strings.Repeat("z", 64), "../escape"} {
		{
			_, err := m.Store(
				ctx,
				bad,
				strings.NewReader("x"),
			)
			assert.True(t, appErrors.IsAppError(
				err,
				appErrors.ErrCodeInvalidArgument,
			), "key %q: got %v, want ErrCodeInvalidArgument", bad, err)
		}
	}

	key := strings.Repeat("cd", 32)
	{
		_, err := m.Store(
			ctx,
			key,
			nil,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "nil reader: got %v, want ErrCodeInvalidArgument", err)
	}
}

func TestStoreHonoursAPreCanceledContext(t *testing.T) {
	m, _ := newRealCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.Store(ctx, strings.Repeat("ef", 32), strings.NewReader("x"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"got %v, want ErrCodeOperationCanceled",
		err,
	)
}

func TestStorePropagatesAReaderFailure(t *testing.T) {
	m, _ := newRealCache(t)
	key := strings.Repeat("12", 32)

	_, err := m.Store(context.Background(), key, &errReader{remaining: 4})
	require.Error(t, err, "expected the reader failure to surface")
	// Nothing half-written may remain findable.
	ok, err := m.cache.Exists(context.Background(), key)
	require.NoError(t, err)

	assert.False(t, ok, "a partially written blob was recorded")
}

// --------------------------------------------------------------------- Verify

func TestVerifyComparesContentAgainstTheSuppliedDigest(t *testing.T) {
	m, _ := newRealCache(t)
	dir := t.TempDir()

	path := filepath.Join(dir, "archive.bin")
	{
		err := os.WriteFile(path, []byte("payload"), 0o644)
		require.NoError(t, err)
	}

	digest := platform.StringSHA256([]byte("payload"))

	{
		err := m.Verify(context.Background(), digest, path)
		assert.NoError(t, err, "Verify on matching content: %v", err)
	}

	wrong := strings.Repeat("0", 64)

	err := m.Verify(context.Background(), wrong, path)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeIntegrity),
		"Verify on mismatched content: got %v, want ErrCodeIntegrity",
		err,
	)
}

func TestVerifyRejectsBadArguments(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	key := strings.Repeat("34", 32)

	{
		err := m.Verify(
			ctx,
			"nope",
			"/tmp/x",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "bad digest: got %v, want ErrCodeInvalidArgument", err)
	}

	{
		err := m.Verify(
			ctx,
			key,
			"  ",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "blank path: got %v, want ErrCodeInvalidArgument", err)
	}

	{
		err := m.Verify(
			ctx,
			key,
			filepath.Join(t.TempDir(), "absent"),
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeCacheMiss,
		), "missing file: got %v, want ErrCodeCacheMiss", err)
	}

	empty := filepath.Join(t.TempDir(), "empty")
	{
		err := os.WriteFile(empty, nil, 0o644)
		require.NoError(t, err)
	}

	{
		err := m.Verify(ctx, key, empty)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeCacheCorrupt),
			"empty file: got %v, want ErrCodeCacheCorrupt",
			err,
		)
	}
}

func TestVerifyHonoursAPreCanceledContext(t *testing.T) {
	m, _ := newRealCache(t)

	path := filepath.Join(t.TempDir(), "a.bin")
	{
		err := os.WriteFile(path, []byte("x"), 0o644)
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		err := m.Verify(
			ctx,
			platform.StringSHA256([]byte("x")),
			path,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "got %v, want ErrCodeOperationCanceled", err)
	}
}

// -------------------------------------------------------------------- Release

func TestReleaseOnAnUnknownKeyIsAMiss(t *testing.T) {
	m, _ := newRealCache(t)

	err := m.Release(context.Background(), strings.Repeat("56", 32))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"got %v, want ErrCodeCacheMiss",
		err,
	)
}

func TestReleaseNeverGoesBelowZero(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	m = withDownloader(t, m, newDownloader())

	mod := downloadMod("ui", "1", "https://example.test/u.zip")
	{
		_, err := m.GetOrDownload(ctx, mod)
		require.NoError(t, err)
	}

	key, _ := CacheKey(mod)
	// More releases than acquires must not produce a negative count that would
	// later look like "someone else still holds this".
	for i := 0; i < 3; i++ {
		{
			err := m.Release(ctx, key)
			require.NoError(t, err, "Release %d: %v", i, err)
		}
	}

	entry, err := m.cache.Index().Get(ctx, key)
	require.NoError(t, err)

	assert.Equal(t, 0, entry.Refs, "refs = %d, want 0", entry.Refs)
}

func TestRemoveDeletesABlobRegardlessOfReferences(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	m = withDownloader(t, m, newDownloader())
	mod := downloadMod("ui", "1", "https://example.test/u.zip")

	path, err := m.GetOrDownload(ctx, mod)
	require.NoError(t, err)

	key, _ := CacheKey(mod)

	{
		err := m.Remove(ctx, key)
		require.NoError(t, err, "Remove: %v", err)
	}

	{
		_, err := os.Stat(path)
		assert.Error(t, err, "the blob file survived Remove")
	}

	{
		_, err := m.cache.Index().Get(
			ctx,
			key,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeCacheMiss,
		), "index row survived: %v", err)
	}
}

// ---------------------------------------------------------------------- Prune

// seedEntry writes a real entry of the given size with the given last-access
// time. The blob really exists, so pruning has a file to delete.
func seedEntry(
	t *testing.T,
	m *CacheManager,
	seed string,
	size int,
	accessed time.Time,
) storage.CacheEntry {
	t.Helper()

	key := platform.StringSHA256([]byte(seed))

	path := filepath.Join(m.RootPath(), "blobs", key[:2], key)
	{
		err := platform.MkdirAll(filepath.Dir(path))
		require.NoError(t, err)
	}

	{
		err := os.WriteFile(path, make([]byte, size), 0o644)
		require.NoError(t, err)
	}

	return seedIndexEntry(
		t,
		m,
		storage.CacheEntry{Hash: key, Path: path, SizeBytes: int64(size), LastAccess: accessed},
	)
}

// seedLargeEntry records an entry of a size no test would ever write to disk.
// Pruning works off the recorded SizeBytes, so the size budget can be exercised
// without allocating gigabytes; Remove tolerates the missing file, which is what a
// cache with a stale index row needs.
func seedLargeEntry(
	t *testing.T,
	m *CacheManager,
	seed string,
	sizeGB int,
	accessed time.Time,
) storage.CacheEntry {
	t.Helper()

	key := platform.StringSHA256([]byte(seed))
	path := filepath.Join(m.RootPath(), "blobs", key[:2], key)

	return seedIndexEntry(t, m, storage.CacheEntry{
		Hash:       key,
		Path:       path,
		SizeBytes:  int64(sizeGB) * (1 << 30),
		LastAccess: accessed,
	})
}

func seedIndexEntry(t *testing.T, m *CacheManager, entry storage.CacheEntry) storage.CacheEntry {
	t.Helper()

	{
		err := m.cache.Index().Put(context.Background(), &entry)
		require.NoError(t, err)
	}

	return entry
}

func TestPruneRemovesEntriesOlderThanMaxAge(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	old := seedEntry(t, m, "old", 10, cacheNow.Add(-40*24*time.Hour))
	fresh := seedEntry(t, m, "fresh", 10, cacheNow.Add(-1*24*time.Hour))

	{
		err := m.Prune(ctx, 0, 30)
		require.NoError(t, err, "Prune: %v", err)
	}

	{
		_, err := m.cache.Index().Get(
			ctx,
			old.Hash,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeCacheMiss,
		), "the stale entry survived: %v", err)
	}

	{
		_, err := m.cache.Index().Get(ctx, fresh.Hash)
		assert.NoError(t, err, "a fresh entry was pruned: %v", err)
	}

	{
		_, err := os.Stat(old.Path)
		assert.Error(t, err, "the stale blob file survived")
	}
}

func TestPruneRemovesLeastRecentlyUsedFirst(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	// 3 x 1 MB is over a 2 MB budget only if nothing is released, so every entry
	// becomes a candidate; the order decides which two go.
	a := seedLargeEntry(t, m, "lru-a", 1, cacheNow.Add(-10*24*time.Hour))
	b := seedLargeEntry(t, m, "lru-b", 1, cacheNow.Add(-9*24*time.Hour))
	c := seedLargeEntry(t, m, "lru-c", 1, cacheNow.Add(-8*24*time.Hour))

	{
		err := m.Prune(ctx, 2, 0)
		require.NoError(t, err, "Prune: %v", err)
	}
	// 3 MiB against a 2 MiB budget: evicting one entry is enough, and evicting more
	// would drop data the budget already accommodates.
	{
		_, err := m.cache.Index().Get(
			ctx,
			a.Hash,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeCacheMiss,
		), "the least recently used entry survived")
	}

	{
		_, err := m.cache.Index().Get(ctx, b.Hash)
		assert.NoError(t, err, "an entry that still fits was pruned: %v", err)
	}

	{
		_, err := m.cache.Index().Get(ctx, c.Hash)
		assert.NoError(t, err, "the newest entry was pruned: %v", err)
	}
}

func TestPruneEvictsUntilTheBudgetIsMet(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	entries := []storage.CacheEntry{
		seedLargeEntry(t, m, "budget-lru-1", 1, cacheNow.Add(-5*24*time.Hour)),
		seedLargeEntry(t, m, "budget-lru-2", 1, cacheNow.Add(-4*24*time.Hour)),
		seedLargeEntry(t, m, "budget-lru-3", 1, cacheNow.Add(-3*24*time.Hour)),
		seedLargeEntry(t, m, "budget-lru-4", 1, cacheNow.Add(-2*24*time.Hour)),
	}

	// 4 MiB over a 2 MiB budget needs two evictions, and they must be the two
	// oldest.
	{
		err := m.Prune(ctx, 2, 0)
		require.NoError(t, err, "Prune: %v", err)
	}

	for _, i := range []int{0, 1} {
		{
			_, err := m.cache.Index().Get(
				ctx,
				entries[i].Hash,
			)
			assert.True(t, appErrors.IsAppError(
				err,
				appErrors.ErrCodeCacheMiss,
			), "entry %d should have been evicted", i)
		}
	}

	for _, i := range []int{2, 3} {
		{
			_, err := m.cache.Index().Get(ctx, entries[i].Hash)
			assert.NoError(t, err, "entry %d should have survived: %v", i, err)
		}
	}
}

func TestPruneStopsOnceUnderTheSizeBudget(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	a := seedLargeEntry(t, m, "budget-a", 1, cacheNow.Add(-5*24*time.Hour))
	b := seedLargeEntry(t, m, "budget-b", 1, cacheNow.Add(-4*24*time.Hour))

	// Exactly 2 GB is not over a 2 GB budget, so nothing goes.
	{
		err := m.Prune(ctx, 2, 0)
		require.NoError(t, err)
	}

	for _, e := range []storage.CacheEntry{a, b} {
		{
			_, err := m.cache.Index().Get(ctx, e.Hash)
			assert.NoError(t, err, "an entry at exactly the budget was pruned: %v", err)
		}
	}
}

func TestPruneTreatsZeroAsNoLimit(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()

	old := seedLargeEntry(t, m, "ancient", 4, cacheNow.Add(-3650*24*time.Hour))
	{
		err := m.Prune(ctx, 0, 0)
		require.NoError(t, err, "Prune: %v", err)
	}
	// Zero means "no limit on this axis", so an operator can disable one half of
	// the policy without disabling all of it.
	{
		_, err := m.cache.Index().Get(ctx, old.Hash)
		assert.NoError(t, err, "an entry was pruned with both limits disabled: %v", err)
	}
}

func TestPruneTreatsNegativeAsNoLimit(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()

	old := seedLargeEntry(t, m, "ancient-neg", 4, cacheNow.Add(-3650*24*time.Hour))
	{
		err := m.Prune(ctx, -1, -1)
		require.NoError(t, err)
	}

	{
		_, err := m.cache.Index().Get(ctx, old.Hash)
		assert.NoError(t, err, "an entry was pruned with negative limits: %v", err)
	}
}

func TestPruneLeavesAnEntryWithNoTimestampAlone(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	key := platform.StringSHA256([]byte("no-timestamp"))

	path := filepath.Join(m.RootPath(), "blobs", key[:2], key)
	{
		err := platform.MkdirAll(filepath.Dir(path))
		require.NoError(t, err)
	}

	{
		err := os.WriteFile(path, []byte("data"), 0o644)
		require.NoError(t, err)
	}

	{
		err := m.cache.Index().Put(
			ctx,
			&storage.CacheEntry{Hash: key, Path: path, SizeBytes: 4},
		)
		require.NoError(t, err)
	}
	// Put stamps a timestamp for a zero value, so the row is fresh; a row whose
	// timestamp is somehow unset must not be treated as infinitely old.
	entry, err := m.cache.Index().Get(ctx, key)
	require.NoError(t, err)

	require.False(t, entry.LastAccess.IsZero(), "Put did not stamp a timestamp")
}

func TestPrunePublishesAnEvent(t *testing.T) {
	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err)

	defer func() { _ = cache.Close() }()

	events := &stubEvents{}

	m, err := NewCacheManager(CacheManagerDeps{
		Cache:     cache,
		Clock:     func() time.Time { return cacheNow },
		EventSink: events,
	})
	require.NoError(t, err)

	seedEntry(t, m, "eventful", 10, cacheNow.Add(-90*24*time.Hour))

	{
		err := m.Prune(context.Background(), 0, 30)
		require.NoError(t, err)
	}

	assert.True(
		t,
		events.hasType(EventCachePruned),
		"events = %v, want one cache.pruned",
		events.types(),
	)
}

func TestPruneStaysQuietWhenNothingIsRemoved(t *testing.T) {
	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err)

	defer func() { _ = cache.Close() }()

	events := &stubEvents{}

	m, err := NewCacheManager(CacheManagerDeps{
		Cache:     cache,
		Clock:     func() time.Time { return cacheNow },
		EventSink: events,
	})
	require.NoError(t, err)

	seedEntry(t, m, "kept", 10, cacheNow)

	{
		err := m.Prune(context.Background(), 0, 30)
		require.NoError(t, err)
	}

	assert.Empty(t, events.types(), "events = %v, want none", events.types())
}

func TestPruneHonoursAPreCanceledContext(t *testing.T) {
	m, _ := newRealCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		err := m.Prune(ctx, 0, 1)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"got %v, want ErrCodeOperationCanceled",
			err,
		)
	}
}

func TestPruneKeepsGoingAfterOneUndeletableEntry(t *testing.T) {
	m, _ := newRealCache(t)
	ctx := context.Background()
	// Two stale entries; the first one's blob is replaced by a directory so its
	// removal fails, and the second must still go.
	first := seedEntry(t, m, "stuck", 10, cacheNow.Add(-90*24*time.Hour))

	second := seedEntry(t, m, "not-stuck", 10, cacheNow.Add(-90*24*time.Hour))
	{
		err := os.Remove(first.Path)
		require.NoError(t, err)
	}

	{
		err := os.Mkdir(first.Path, 0o755)
		require.NoError(t, err)
	}

	{
		err := m.Prune(ctx, 0, 30)
		require.NoError(t, err, "one undeletable entry must not abort the sweep: %v", err)
	}

	{
		_, err := m.cache.Index().Get(
			ctx,
			second.Hash,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeCacheMiss,
		), "the second stale entry was not pruned")
	}
}

func TestPruneConfiguredUsesTheConfiguredLimits(t *testing.T) {
	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err)

	defer func() { _ = cache.Close() }()

	m, err := NewCacheManager(CacheManagerDeps{
		Cache:  cache,
		Config: domain.CacheConfig{MaxSizeGB: 100, MaxAgeDays: 7},
		Clock:  func() time.Time { return cacheNow },
	})
	require.NoError(t, err)

	ctx := context.Background()

	stale := seedEntry(t, m, "configured-stale", 10, cacheNow.Add(-8*24*time.Hour))
	{
		err := m.PruneConfigured(ctx)
		require.NoError(t, err)
	}

	{
		_, err := m.cache.Index().Get(
			ctx,
			stale.Hash,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeCacheMiss,
		), "the configured age limit was not applied")
	}
}

// ----------------------------------------------------------------- CacheInfo

func TestGetCacheInfoOnAnEmptyCache(t *testing.T) {
	m, _ := newRealCache(t)

	info, err := m.GetCacheInfo(context.Background())
	require.NoError(t, err, "GetCacheInfo")

	assert.Equal(t, m.RootPath(), info.Path, "path = %q, want %q", info.Path, m.RootPath())

	assert.False(t, info.FileCount != 0 || info.TotalSize != 0, "empty cache reported %+v", info)

	assert.False(
		t,
		!info.Oldest.IsZero() || !info.Newest.IsZero(),
		"empty cache reported bounds %v..%v",
		info.Oldest,
		info.Newest,
	)
}

func TestGetCacheInfoSummarisesEntries(t *testing.T) {
	m, _ := newRealCache(t)
	seedEntry(t, m, "info-a", 100, cacheNow.Add(-2*24*time.Hour))
	seedEntry(t, m, "info-b", 250, cacheNow)
	seedEntry(t, m, "info-c", 50, cacheNow.Add(-10*24*time.Hour))

	info, err := m.GetCacheInfo(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 3, info.FileCount, "FileCount = %d, want 3", info.FileCount)

	assert.Equal(t, int64(400), info.TotalSize, "TotalSize = %d, want 400", info.TotalSize)

	assert.True(t, info.Newest.Equal(cacheNow), "Newest = %v, want %v", info.Newest, cacheNow)

	assert.True(
		t,
		info.Oldest.Equal(cacheNow.Add(-10*24*time.Hour)),
		"Oldest = %v, want %v",
		info.Oldest,
		cacheNow.Add(-10*24*time.Hour),
	)
}

func TestGetCacheInfoHonoursAPreCanceledContext(t *testing.T) {
	m, _ := newRealCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.GetCacheInfo(ctx)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"got %v, want ErrCodeOperationCanceled",
		err,
	)
}

// ----------------------------------------------------------------- ClearCache

func TestClearCacheEmptiesBlobsAndRows(t *testing.T) {
	cache, err := storage.NewCache(t.TempDir())
	require.NoError(t, err)

	defer func() { _ = cache.Close() }()

	events := &stubEvents{}

	m, err := NewCacheManager(CacheManagerDeps{
		Cache:     cache,
		Download:  newDownloader(),
		Clock:     func() time.Time { return cacheNow },
		EventSink: events,
	})
	require.NoError(t, err)

	ctx := context.Background()

	for _, mod := range []*domain.Mod{
		downloadMod("a", "1", "https://example.test/a.zip"),
		downloadMod("b", "1", "https://example.test/b.zip"),
	} {
		{
			_, err := m.GetOrDownload(ctx, mod)
			require.NoError(t, err)
		}
	}

	{
		err := m.ClearCache(ctx)
		require.NoError(t, err, "ClearCache: %v", err)
	}

	info, err := m.GetCacheInfo(ctx)
	require.NoError(t, err)

	assert.False(t, info.FileCount != 0 || info.TotalSize != 0, "cache still reports %+v", info)

	assert.True(
		t,
		events.hasType(EventCacheCleared),
		"events = %v, want one cache.cleared",
		events.types(),
	)
}

func TestClearCacheOnAnEmptyCacheIsFine(t *testing.T) {
	m, _ := newRealCache(t)
	{
		err := m.ClearCache(context.Background())
		assert.NoError(t, err, "ClearCache on an empty cache: %v", err)
	}
}

func TestClearCacheHonoursAPreCanceledContext(t *testing.T) {
	m, _ := newRealCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		err := m.ClearCache(ctx)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"got %v, want ErrCodeOperationCanceled",
			err,
		)
	}
}

// ----------------------------------------------------- in-flight bookkeeping

func TestInflightBookkeepingIsReleasedOnEveryPath(t *testing.T) {
	m, cache := newRealCache(t)
	ctx := context.Background()
	successor := withDownloader(t, m, newDownloader())
	mod := downloadMod("ui", "1", "https://example.test/u.zip")

	// Success.
	{
		_, err := successor.GetOrDownload(ctx, mod)
		require.NoError(t, err)
	}
	// Failure: no downloader configured. A different mod, because the one above
	// is now cached and would never reach the transfer.
	{
		_, err := m.GetOrDownload(
			ctx,
			downloadMod("no-downloader", "1", "https://example.test/nd.zip"),
		)
		assert.Error(t, err, "expected an error with no downloader configured")
	}
	// Failure: a downloader that refuses.
	failing := &scriptDownloader{
		perMod: map[string]int{},
		bodies: map[string][]byte{},
		err:    errors.New("nope"),
	}

	failureManager := withDownloader(t, m, failing)
	{
		_, err := failureManager.GetOrDownload(
			ctx,
			downloadMod("bad", "1", "https://example.test/bad.zip"),
		)
		require.Error(t, err, "expected a download failure")
	}

	for _, check := range []*CacheManager{m, successor, failureManager} {
		check.mu.Lock()
		remaining := len(check.inflight)
		check.mu.Unlock()

		assert.Equal(t, 0, remaining, "%d in-flight entries were left behind", remaining)
	}
	// A leaked map entry would also mean the next request for the same key waits
	// on a job that already finished.
	{
		_, err := cache.Index().List(ctx)
		assert.NoError(t, err, "the index became unusable: %v", err)
	}
}
