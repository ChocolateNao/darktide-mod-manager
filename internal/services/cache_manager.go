package services

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// CacheStore is the slice of storage.Cache the manager depends on. Keeping it an
// interface here means CacheManager can be tested against a fake that counts
// calls and simulates I/O failures, while production wiring passes the real
// *storage.Cache.
type CacheStore interface {
	RootPath() string
	Index() storage.CacheIndex
	BlobPath(hash string) (string, error)
	Entry(ctx context.Context, hash string) (*storage.CacheEntry, error)
	Write(ctx context.Context, hash string, src io.Reader) (*storage.CacheEntry, error)
	Open(ctx context.Context, hash string) (*os.File, error)
	Remove(ctx context.Context, hash string) error
	RemoveAll(ctx context.Context) error
	Exists(ctx context.Context, hash string) (bool, error)
	Close() error
}

// Clock is the subset of time the cache manager needs. Tests pin it so an
// "entry is older than N days" case does not depend on wall time.
type Clock func() time.Time

// Cache limits used when a CacheConfig leaves them unset. They match
// domain.DefaultConfig's cache block so a manager built from a bare struct
// behaves like one built from a full configuration.
const (
	defaultMaxSizeGB  = 5
	defaultMaxAgeDays = 30
)

// CacheManager owns the download cache policy: how a mod's archive is keyed,
// when a cached copy is reused, when it is dropped, and how much disk the cache
// may use.
//
// The heavy lifting (atomic blob writes, refcounted rows) lives in
// storage.Cache; this type owns the decisions.
type CacheManager struct {
	cache    CacheStore
	download Downloader
	events   EventSink
	inflight map[string]*download
	log      *zerolog.Logger
	now      Clock
	config   domain.CacheConfig
	mu       sync.Mutex
}

// Downloader fetches a mod's archive into a file the caller owns.
//
// It is a narrow interface rather than domain.ModSource so the cache manager can
// be tested without a source, and so a future non-HTTP source (a local folder,
// an S3 bucket) plugs in here without touching the cache policy.
type Downloader interface {
	// Download writes the mod's archive to destPath and returns the path it
	// actually wrote. destPath's parent directory already exists and the file
	// does not.
	Download(ctx context.Context, mod *domain.Mod, destPath string) (string, error)
	// Verify reports whether the file at path matches the mod's expected
	// checksum. Implementations that have no expected checksum return nil.
	Verify(ctx context.Context, mod *domain.Mod, path string) error
}

// CacheManagerDeps are the collaborators a CacheManager is built from. Grouping
// them keeps the constructor from growing a positional-argument list nobody
// can remember.
type CacheManagerDeps struct {
	Cache     CacheStore
	Download  Downloader
	EventSink EventSink
	Clock     Clock
	Logger    *zerolog.Logger
	Config    domain.CacheConfig
}

// NewCacheManager builds a cache manager. A nil clock defaults to time.Now; a
// nil EventSink simply means no progress events.
func NewCacheManager(deps CacheManagerDeps) (*CacheManager, error) {
	if deps.Cache == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"cache manager requires a cache store",
		)
	}

	cfg := deps.Config
	// Normalised here rather than through (*domain.Config).ApplyDefaults because
	// a CacheManager can be built from a bare CacheConfig in a test or a
	// library embedder that has no whole-config object to hand over. Hash
	// verification defaults on: silently skipping integrity checks because a
	// struct field was left zero is the wrong failure direction.
	if cfg.MaxSizeGB <= 0 {
		cfg.MaxSizeGB = defaultMaxSizeGB
	}

	if cfg.MaxAgeDays <= 0 {
		cfg.MaxAgeDays = defaultMaxAgeDays
	}

	cfg.VerifyHashes = true

	now := deps.Clock
	if now == nil {
		now = time.Now
	}

	return &CacheManager{
		cache:    deps.Cache,
		config:   cfg,
		download: deps.Download,
		inflight: map[string]*download{},
		now:      now,
		events:   deps.EventSink,
		log:      loggerOf(deps.Logger),
	}, nil
}

// download is one in-flight fetch, shared by every caller waiting for it.
type download struct {
	err     error
	done    chan struct{}
	path    string
	waiters int
}

// Config returns a copy of the cache configuration in force.
func (c *CacheManager) Config() domain.CacheConfig { return c.config }

// RootPath is where the cache lives on disk.
func (c *CacheManager) RootPath() string { return c.cache.RootPath() }

// CacheKey derives the cache key for a mod.
//
// The key is the SHA256 of the download location plus the version, so the same
// bytes fetched from a different URL still collide (deduplication) while a new
// version of the same mod gets its own entry. The version is always part of the
// input even when empty: hashing "url|" and "url" together would let a mod
// without a version reuse an entry written for one that has.
func CacheKey(mod *domain.Mod) (string, error) {
	if mod == nil {
		return "", appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"cannot derive a cache key for a nil mod",
		)
	}

	source := DownloadLocation(mod)
	if source == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"mod %q has no download location to cache", mod.ID)
	}

	h := platform.StringSHA256([]byte(source + "\x00" + mod.Version))
	if _, err := storage.NormalizeCacheKey(h); err != nil {
		// Unreachable with SHA256 output, but the key is used as a file name
		// later; failing here beats trusting that assumption forever.
		return "", appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"mod %q produced an unusable cache key", mod.ID)
	}

	return h, nil
}

// DownloadLocation extracts the source-relative locator a mod is fetched from.
//
// Sources store this in SourceData under "download_url". A source that resolves a
// URL later (a Nexus file id, a Vortex download id) stores an opaque locator
// instead, and this must accept that without guessing at a scheme.
//
// The URL-shaped keys come first because a source that publishes a URL always
// wants to be cached by URL. "local_path" comes last and names the payload of a
// source that has no URL at all — the bundled local source, whose mods live in a
// folder the user chose. Without it that source could be listed, searched and
// described but never installed, because the cache key is derived from here.
func DownloadLocation(mod *domain.Mod) string {
	if mod == nil {
		return ""
	}

	for _, key := range []string{"download_url", "downloadUrl", "file_url", "url", "download_id", "file_id", "local_path"} {
		if v, ok := mod.SourceData[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}

	return ""
}

// GetOrDownload returns the path to a local copy of the mod's archive,
// downloading it only when the cache cannot satisfy the request.
//
// A cached blob whose checksum no longer matches is discarded and re-fetched
// rather than returned: handing back a corrupt archive would make the install
// fail with a much less obvious error later, or worse, install broken files.
func (c *CacheManager) GetOrDownload(ctx context.Context, mod *domain.Mod) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return "", canceledError(err)
	}

	key, err := CacheKey(mod)
	if err != nil {
		return "", err
	}

	// Fast path: a usable blob is already there.
	if path, ok, err := c.cached(ctx, key, mod); err != nil {
		return "", err
	} else if ok {
		return path, nil
	}

	// Slow path: join (or start) the in-flight download for this key.
	return c.downloadOrJoin(ctx, key, mod)
}

// cached reports the path of a usable cached blob for key.
func (c *CacheManager) cached(
	ctx context.Context,
	key string,
	mod *domain.Mod,
) (string, bool, error) {
	entry, err := c.cache.Entry(ctx, key)
	if err != nil {
		if appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss) {
			// Nothing recorded for this key. Notably this path must not write:
			// a caller that merely missed the cache would otherwise delete the row
			// another goroutine had just written, and two installs of the same mod
			// would each force the other to re-download.
			return "", false, nil
		}

		return "", false, err
	}

	ok, err := c.cache.Exists(ctx, key)
	if err != nil {
		return "", false, err
	}

	if !ok {
		// A row whose file has been deleted behind our back. Dropping the row is
		// safe here because the caller is the one who then downloads, and the
		// download re-inserts it.
		if err := c.cache.Remove(ctx, key); err != nil {
			return "", false, err
		}

		return "", false, nil
	}

	path := entry.Path
	if strings.TrimSpace(path) == "" {
		// An older build could have written a row without a path; recompute it
		// rather than serving an empty string as a file location.
		path, err = c.cache.BlobPath(key)
		if err != nil {
			return "", false, err
		}
	}
	// A blob whose size no longer matches the index row is not reusable: that
	// is the truncated-download and partial-write case. The key itself is
	// derived from the download location, so it cannot be used to check the
	// bytes; the source's own checksum (below) is what proves the content.
	if corrupt, err := sizeMismatch(ctx, c.cache, key, path); err != nil {
		return "", false, err
	} else if corrupt {
		if err := c.cache.Remove(ctx, key); err != nil {
			return "", false, err
		}

		return "", false, nil
	}

	if c.download != nil && c.config.VerifyHashes {
		if err := c.download.Verify(ctx, mod, path); err != nil {
			// The source's own expectation disagrees with what we hold. It may
			// just be a re-release under the same URL, so the blob is dropped
			// and re-fetched rather than trusted.
			if !appErrors.IsAppError(err, appErrors.ErrCodeIntegrity) {
				return "", false, appErrors.Wrap(err, appErrors.ErrCodeIntegrity,
					"cached copy of mod %q failed verification", mod.ID)
			}

			if err := c.cache.Remove(ctx, key); err != nil {
				return "", false, err
			}

			return "", false, nil
		}
	}
	// Refuse the cache so a later prune cannot delete a file this caller is
	// about to extract.
	if _, err := c.cache.Index().Acquire(ctx, key, c.now()); err != nil {
		return "", false, err
	}

	return path, true, nil
}

// fetch downloads key, collapsing concurrent requests for the same key onto one
// transfer.
func (c *CacheManager) fetch(ctx context.Context, key string, mod *domain.Mod) (string, error) {
	c.mu.Lock()
	if inflight, ok := c.inflight[key]; ok {
		inflight.waiters++
		c.mu.Unlock()
		// Waiting on the other caller rather than starting a second download.
		// The wait is not context-aware on purpose: abandoning the transfer
		// would leave a half-written blob the other caller is still writing.
		<-inflight.done

		return inflight.path, inflight.err
	}

	job := &download{done: make(chan struct{})}
	c.inflight[key] = job
	c.mu.Unlock()

	job.path, job.err = c.downloadOnce(ctx, key, mod)

	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	close(job.done)

	return job.path, job.err
}

// downloadOrJoin runs the transfer unless the cache was filled between the caller's
// miss and this goroutine reaching the lock.
//
// Without this re-check, callers queued behind a slow transfer all miss, all
// arrive at the lock after the winner has already finished, and each starts its
// own download — the exact thundering herd the in-flight map exists to prevent.
func (c *CacheManager) downloadOrJoin(
	ctx context.Context,
	key string,
	mod *domain.Mod,
) (string, error) {
	c.mu.Lock()
	_, busy := c.inflight[key]
	c.mu.Unlock()

	if !busy {
		if path, ok, err := c.cached(ctx, key, mod); err != nil {
			return "", err
		} else if ok {
			return path, nil
		}
	}

	return c.fetch(ctx, key, mod)
}

// downloadOnce performs the actual transfer and records it in the index.
func (c *CacheManager) downloadOnce(
	ctx context.Context,
	key string,
	mod *domain.Mod,
) (string, error) {
	if c.download == nil {
		return "", appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"no downloader is configured, cannot fetch mod %q", mod.ID)
	}

	if err := ctx.Err(); err != nil {
		return "", canceledError(err)
	}

	c.log.Debug().
		Str("op", "cache-download").
		Str("mod", mod.ID).
		Str("version", mod.Version).
		Str("key", key).
		Msg("downloading archive")

	dest, err := c.blobPathFor(key)
	if err != nil {
		return "", err
	}

	staging := dest + ".partial"
	if err := platform.MkdirAll(filepath.Dir(staging)); err != nil {
		return "", err
	}
	// A staging file left by a previous crashed run must not be appended to.
	if err := os.Remove(staging); err != nil && !os.IsNotExist(err) {
		return "", appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"clear stale download %q",
			staging,
		)
	}

	// The downloader may write somewhere else entirely (a source that names its
	// own temp file), so the returned path is what gets validated and moved,
	// and both it and the staging name are cleaned up on failure.
	written, err := c.download.Download(ctx, mod, staging)
	if err != nil {
		// The partial file is removed rather than left behind: the next attempt
		// would find it and either resume into a corrupt file or fail
		// confusingly.
		discardStaging(staging, written)
		return "", wrapDownloadError(mod, err)
	}

	if written == "" {
		written = staging
	}

	size, err := c.validateStaged(ctx, mod, written)
	if err != nil {
		discardStaging(staging, written)
		return "", err
	}

	if err := installBlob(written, dest); err != nil {
		discardStaging(staging, written)
		return "", err
	}
	// A blob with no index row is invisible to every later lookup and to
	// pruning, so the row is written before the call reports success. The
	// acquire that follows takes the reference the caller is about to hold.
	if err := c.cache.Index().Put(
		ctx,
		&storage.CacheEntry{Hash: key, Path: dest, SizeBytes: size},
	); err != nil {
		_ = os.Remove(dest)
		return "", err
	}

	if _, err := c.cache.Index().Acquire(ctx, key, c.now()); err != nil {
		return "", err
	}

	c.publish(Event{Type: EventCacheDownloaded, ModID: mod.ID, SourceID: mod.SourceID, Data: dest})

	return dest, nil
}

// discardStaging removes whichever of the two paths the downloader actually
// wrote. A no-op on the empty string, and it never touches a path that is not
// inside the cache: the downloader is external code and its choice of temp file
// is not ours to police.
func discardStaging(paths ...string) {
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}

		_ = os.Remove(p)
	}
}

// installBlob moves src onto dst, replacing whatever is there.
//
// The destination is removed first because Windows cannot rename onto an
// existing file. That is safe here: dest is keyed by the cache key, so it only
// ever holds bytes for this same download.
func installBlob(src, dst string) error {
	if src == dst {
		return nil
	}

	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	if rmErr := os.Remove(dst); rmErr != nil && !os.IsNotExist(rmErr) {
		return appErrors.Wrap(rmErr, appErrors.ErrCodeConfigIO, "replace cached archive %q", dst)
	}

	if err := os.Rename(src, dst); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "install cached archive %q", dst)
	}

	return nil
}

// validateStaged checks a freshly downloaded file before it becomes the cached
// blob, so a corrupt archive is never reachable through the cache index.
//
// An empty file is always rejected: a "successful" download of zero bytes means
// the server answered with an error page the source did not check for, or the
// connection dropped in a way the source treated as EOF.
func (c *CacheManager) validateStaged(
	ctx context.Context,
	mod *domain.Mod,
	path string,
) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, appErrors.Wrap(err, appErrors.ErrCodeDownloadFailed,
				"downloader reported success but wrote no file for mod %q", modLabel(mod))
		}

		return 0, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"stat downloaded archive for mod %q",
			modLabel(mod),
		)
	}

	if info.IsDir() {
		return 0, appErrors.New(appErrors.ErrCodeDownloadFailed,
			"downloader reported a directory for mod %q", modLabel(mod))
	}

	if info.Size() == 0 {
		return 0, appErrors.New(appErrors.ErrCodeDownloadFailed,
			"downloaded archive for mod %q is empty", modLabel(mod))
	}

	if c.download != nil && c.config.VerifyHashes {
		if err := c.download.Verify(ctx, mod, path); err != nil {
			return 0, appErrors.Wrap(err, appErrors.ErrCodeIntegrity,
				"downloaded copy of mod %q failed verification", modLabel(mod))
		}
	}

	return info.Size(), nil
}

// blobPathFor is the cache's own path calculation, reused for the staging file
// so the download lands in the same directory as the final blob (a rename
// across filesystems is not atomic and would defeat the point of staging).
func (c *CacheManager) blobPathFor(key string) (string, error) {
	return c.cache.BlobPath(key)
}

// Store writes bytes into the cache under hash and returns the blob path. It is
// the entry point for a mod the user already has on disk, and for tests.
func (c *CacheManager) Store(ctx context.Context, hash string, src io.Reader) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return "", canceledError(err)
	}

	if src == nil {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "cannot store a nil reader")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.cache.Write(ctx, hash, src)
	if err != nil {
		return "", err
	}

	return entry.Path, nil
}

// Verify checks that a file against the checksum the source published for it.
//
// Unlike the internal size check this is a real content check, so it is what
// backs the "verify hashes" configuration option: a mod that declares a SHA256
// has its archive compared against it, and a mismatch is ErrCodeIntegrity. A
// mod with no declared checksum passes, because there is nothing to compare
// against and inventing one would be worse than skipping the check.
func (c *CacheManager) Verify(ctx context.Context, hash, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	key, err := storage.NormalizeCacheKey(hash)
	if err != nil {
		return err
	}

	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "verify needs a file path")
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	info, err := os.Stat(trimmed)
	if err != nil {
		if os.IsNotExist(err) {
			return appErrors.Wrap(
				err,
				appErrors.ErrCodeCacheMiss,
				"cannot verify %q: no such file",
				trimmed,
			)
		}

		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "stat %q for verification", trimmed)
	}

	if info.Size() == 0 {
		return appErrors.New(
			appErrors.ErrCodeCacheCorrupt,
			"cannot verify %q: file is empty",
			trimmed,
		)
	}

	actual, err := platform.FileSHA256(trimmed)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "hash %q for verification", trimmed)
	}

	if actual != key {
		return appErrors.New(appErrors.ErrCodeIntegrity,
			"%q hashes to %s, expected %s", trimmed, actual, key)
	}

	return nil
}

// sizeMismatch reports whether the blob at path no longer matches the size its
// index row recorded.
//
// This is a cheap structural check, not a content check: the cache key is
// derived from the download location rather than from the bytes, so comparing
// hashes would be meaningless. A changed size still catches the realistic
// failures — a truncated download, a partial write left by a crash, or a file
// replaced by something else entirely — without reading the whole archive on
// every cache hit.
func sizeMismatch(ctx context.Context, cache CacheStore, key, path string) (bool, error) {
	entry, err := cache.Entry(ctx, key)
	if err != nil {
		if appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss) {
			// No row means no recorded size to disagree with; the caller has
			// already confirmed the file exists.
			return false, nil
		}

		return false, err
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}

		return false, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "stat cached file %q", path)
	}

	return info.Size() != entry.SizeBytes, nil
}

// Release drops one reference to a cached blob, as the caller finishes with it.
// It is separate from GetOrDownload because the reference outlives the call
// that took it: the extracted mod on disk keeps referencing the archive until
// the mod is uninstalled.
func (c *CacheManager) Release(ctx context.Context, hash string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	_, err := c.cache.Index().Release(ctx, hash)

	return err
}

// Remove deletes one blob and its index row regardless of its refcount. It is
// what "purge this mod's archive" does, and it is not what pruning does.
func (c *CacheManager) Remove(ctx context.Context, hash string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	return c.cache.Remove(ctx, hash)
}

// Prune enforces the size and age limits and reports what it freed.
//
// Entries are considered least-recently-accessed first, which is the whole
// point of tracking last_access: the archive someone just installed is the last
// thing that should go. Age is checked before size so a single ancient archive
// is removed even when the cache is well under its size limit — an archive from
// two years ago is stale regardless of how small it is.
func (c *CacheManager) Prune(ctx context.Context, maxSizeGB, maxAgeDays int) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}
	// Zero or negative means "no limit on this axis", which is how an operator
	// disables half of the policy without disabling all of it.
	entries, err := c.cache.Index().List(ctx)
	if err != nil {
		return err
	}
	// List already orders by last access ascending, oldest first, but sorting
	// here too keeps the guarantee local to the code that depends on it.
	sort.SliceStable(
		entries,
		func(i, j int) bool { return entries[i].LastAccess.Before(entries[j].LastAccess) },
	)

	now := c.now()
	maxAge := time.Duration(maxAgeDays) * 24 * time.Hour
	maxBytes := int64(maxSizeGB) * 1024 * 1024 * 1024

	var total int64
	for _, e := range entries {
		total += e.SizeBytes
	}

	var freed int64

	removed := 0

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return canceledError(err)
		}

		expired := maxAgeDays > 0 && !e.LastAccess.IsZero() && now.Sub(e.LastAccess) > maxAge

		overBudget := maxBytes > 0 && total > maxBytes
		if !expired && !overBudget {
			continue
		}

		if err := c.cache.Remove(ctx, e.Hash); err != nil {
			// One undeletable entry must not stop the sweep; the next prune
			// will try it again.
			continue
		}

		total -= e.SizeBytes
		freed += e.SizeBytes
		removed++
	}

	if removed > 0 {
		c.log.Info().
			Str("op", "cache-prune").
			Int("entries", removed).
			Int64("freed_bytes", freed).
			Msg("pruned cache")
		c.publish(Event{Type: EventCachePruned, Data: freed})
	}

	return nil
}

// PruneConfigured applies the limits from the manager's configuration.
func (c *CacheManager) PruneConfigured(ctx context.Context) error {
	return c.Prune(ctx, c.config.MaxSizeGB, c.config.MaxAgeDays)
}

// GetCacheInfo summarises the cache for the UI and the CLI.
func (c *CacheManager) GetCacheInfo(ctx context.Context) (domain.CacheInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return domain.CacheInfo{}, canceledError(err)
	}

	entries, err := c.cache.Index().List(ctx)
	if err != nil {
		return domain.CacheInfo{}, err
	}

	info := domain.CacheInfo{Path: c.cache.RootPath(), FileCount: len(entries)}
	if len(entries) == 0 {
		return info, nil
	}

	oldest, newest := entries[0].LastAccess, entries[0].LastAccess
	for _, e := range entries {
		info.TotalSize += e.SizeBytes
		if e.LastAccess.IsZero() {
			// An entry with no timestamp cannot order against the others, so it
			// is skipped rather than making the bounds meaningless.
			continue
		}

		if e.LastAccess.Before(oldest) {
			oldest = e.LastAccess
		}

		if e.LastAccess.After(newest) {
			newest = e.LastAccess
		}
	}

	if oldest.IsZero() {
		oldest = newest
	}

	info.Oldest, info.Newest = oldest, newest

	return info, nil
}

// ClearCache empties the cache.
//
// Referenced blobs are kept: an installed mod may still be extracting from one,
// and deleting it mid-install produces a half-written mod folder. Stale rows are
// dropped so the index does not accumulate entries that no longer exist.
func (c *CacheManager) ClearCache(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	if err := c.cache.RemoveAll(ctx); err != nil {
		return err
	}

	c.log.Info().Str("op", "cache-clear").Msg("cleared cache")
	c.publish(Event{Type: EventCacheCleared})

	return nil
}

// publish sends an event when a sink is configured. Services never fail because
// nobody was listening.
func (c *CacheManager) publish(e Event) {
	if c.events == nil {
		return
	}

	c.events.Publish(e)
}
