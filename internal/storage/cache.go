package storage

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// CacheEntry is the bookkeeping row for one cached download.
//
// The archive is addressed by Hash, which is the SHA256 of the download URL
// plus the mod version. That makes the key stable across restarts (no need to
// remember which URL a blob came from) and unique per version (an update never
// overwrites the old archive in place).
type CacheEntry struct {
	LastAccess time.Time `json:"last_access"`
	Hash       string
	Path       string `json:"path"`
	SizeBytes  int64  `json:"size_bytes"`
	Refs       int    `json:"refs"`
}

// CacheIndex is the persistence contract for cache metadata.
//
// Every method takes a context: the index is a SQLite handle and inherits the
// same cancellation semantics as the rest of the storage layer. Implementations
// must be safe for concurrent use.
type CacheIndex interface {
	// Get returns the entry for hash, or ErrCacheMiss when there is none. An
	// entry whose blob has disappeared from disk is *not* a miss here — checking
	// that is the caller's job, because only the caller knows whether the file
	// it cares about is the one named by Path.
	Get(ctx context.Context, hash string) (*CacheEntry, error)
	// Put inserts or replaces an entry.
	Put(ctx context.Context, entry *CacheEntry) error
	// Acquire increments the reference count and stamps last_access, returning
	// the updated entry. It is what makes an entry prunable-ineligible while a
	// mod is installed.
	Acquire(ctx context.Context, hash string, at time.Time) (*CacheEntry, error)
	// Release decrements the reference count, never below zero, and returns the
	// updated entry. Releasing an unknown hash is a miss, not a silent success:
	// a caller balancing acquire/release should have a real row to release.
	Release(ctx context.Context, hash string) (*CacheEntry, error)
	// Touch stamps last_access without changing the reference count.
	Touch(ctx context.Context, hash string, at time.Time) error
	// List returns every entry ordered by last_access ascending, ties broken by
	// hash. The order is the one age-based pruning wants, and it is stable so
	// two runs prune the same entries in the same sequence.
	List(ctx context.Context) ([]CacheEntry, error)
	// Delete forgets an entry. Deleting an unknown hash is not an error, because
	// callers use it to make a store idempotent.
	Delete(ctx context.Context, hash string) error
	// Clear forgets every entry. The blobs on disk are *not* removed; that is
	// the caller's job (see Cache.RemoveAll).
	Clear(ctx context.Context) error
	// Close releases the underlying resources.
	Close() error
}

// Cache is the on-disk download cache: a blob store rooted at a directory plus
// the CacheIndex that describes it.
//
// Blobs live at <root>/blobs/<first two hex chars>/<hash>. The two-character
// fan-out exists because a single directory holding thousands of 200 MB archives
// is slow to list on Windows, where directory enumeration is O(n) per entry.
type Cache struct {
	index     CacheIndex
	rootPath  string
	blobsDir  string
	ownsIndex bool
}

// NewCache opens (creating if necessary) the cache rooted at rootPath and its
// SQLite index, which lives inside the cache root as "cache.db". Keeping the
// index next to the blobs means one path to back up and one path to delete.
func NewCache(rootPath string) (*Cache, error) {
	return NewCacheWithFS(rootPath, nil)
}

// NewCacheWithIndex builds a Cache around an index the caller already owns.
//
// Passing a non-nil index is how tests inject an in-memory database, and how a
// caller that wants the metadata elsewhere (a shared cache.db for several
// roots) avoids a second connection fighting over the same file. The caller
// keeps ownership: Close does not close a borrowed index.
func NewCacheWithFS(rootPath string, index CacheIndex) (*Cache, error) {
	trimmed := strings.TrimSpace(rootPath)
	if trimmed == "" {
		return nil, appErrors.New(appErrors.ErrCodeNoStoragePath, "cache root path is empty")
	}

	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"resolve cache root %q",
			rootPath,
		)
	}

	c := &Cache{rootPath: absolute, blobsDir: filepath.Join(absolute, "blobs"), index: index}
	if index != nil {
		return c, nil
	}

	db, err := Open(context.Background(), filepath.Join(absolute, "cache.db"))
	if err != nil {
		return nil, err
	}

	c.index = NewSQLiteCacheIndex(db)
	c.ownsIndex = true

	return c, nil
}

// NewMemoryCache builds a cache whose index lives in memory. Nothing is written
// to disk, which is what makes it usable for `--dry-run` flows and for tests
// that care about ref-counting rather than about layout.
func NewMemoryCache() (*Cache, error) {
	db, err := Open(context.Background(), MemoryPath)
	if err != nil {
		return nil, err
	}

	return &Cache{index: NewSQLiteCacheIndex(db), ownsIndex: true}, nil
}

// RootPath is the directory holding the blobs and the index. It is empty for a
// memory cache, which is also the signal that there is nothing to clean up.
func (c *Cache) RootPath() string { return c.rootPath }

// Index exposes the metadata store for callers that need more than the
// convenience methods here (the cache manager does).
func (c *Cache) Index() CacheIndex { return c.index }

// Close releases the index when this Cache opened it. A borrowed index is left
// alone, so a caller sharing one database between two caches keeps working.
func (c *Cache) Close() error {
	if c == nil || !c.ownsIndex || c.index == nil {
		return nil
	}

	err := c.index.Close()
	c.index = nil
	c.ownsIndex = false

	return err
}

// BlobPath is where the blob for hash lives. It is a pure function of the root
// and the key, so it can be called before anything exists on disk.
func (c *Cache) BlobPath(hash string) (string, error) {
	key, err := NormalizeCacheKey(hash)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(c.rootPath) == "" {
		return "", appErrors.New(appErrors.ErrCodeNoStoragePath, "cache has no root path")
	}

	return filepath.Join(c.blobsDir, key[:2], key), nil
}

// NormalizeCacheKey validates a cache key and returns it in canonical form.
//
// The key becomes a filename, so anything that could escape the blob directory
// is rejected outright rather than sanitised: a caller that mangles a key would
// otherwise silently write to an unrelated path. Only lowercase hex is accepted
// (not mixed case, not a short prefix) because the key is defined as a SHA256
// and comparing keys byte-for-byte is much easier when there is one spelling.
func NormalizeCacheKey(hash string) (string, error) {
	key := strings.TrimSpace(hash)
	if key == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "cache key is empty")
	}

	if len(key) != 64 {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"cache key %q is not a SHA256 digest (%d characters)", hash, len(key))
	}

	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}

		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"cache key %q contains a non-lowercase-hex character %q", hash, string(rune(c)))
	}

	return key, nil
}

// Entry returns the metadata for hash. A missing entry is ErrCacheMiss.
func (c *Cache) Entry(ctx context.Context, hash string) (*CacheEntry, error) {
	key, err := NormalizeCacheKey(hash)
	if err != nil {
		return nil, err
	}

	return c.index.Get(ctx, key)
}

// Write stores the bytes read from src under hash and records the metadata.
//
// The write is atomic: the bytes land in a temporary file inside the same
// directory and are renamed into place, so a crash or a concurrent reader never
// observes a half-written archive that later looks like a cache hit. When the
// key already has a usable blob, Write is a no-op that returns the existing
// entry — callers use it to make "fetch and cache" idempotent.
func (c *Cache) Write(ctx context.Context, hash string, src io.Reader) (*CacheEntry, error) {
	key, err := NormalizeCacheKey(hash)
	if err != nil {
		return nil, err
	}

	if src == nil {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"cache write for %q has no source",
			key,
		)
	}

	if existing, err := c.usableEntry(ctx, key); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	dest, err := c.BlobPath(key)
	if err != nil {
		return nil, err
	}

	if err := platform.MkdirAll(filepath.Dir(dest)); err != nil {
		return nil, wrapKeepingCode(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"create cache directory for %q",
			key,
		)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".partial-"+key+"-*")
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"create cache temp file for %q",
			key,
		)
	}

	tmpName := tmp.Name()
	// Any failure past this point must not leave the partial file behind: a
	// leftover .partial-* in the blob directory looks like a cache entry to every
	// later scan and is never cleaned up on its own.
	committed := false

	defer func() {
		_ = tmp.Close()

		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	written, err := io.Copy(tmp, src)
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeDownloadFailed,
			"write cache blob for %q",
			key,
		)
	}

	if err := tmp.Sync(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"flush cache blob for %q",
			key,
		)
	}

	if err := tmp.Close(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"close cache blob for %q",
			key,
		)
	}

	if err := os.Chmod(tmpName, 0o600); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"set cache blob mode for %q",
			key,
		)
	}

	if err := os.Rename(tmpName, dest); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"commit cache blob for %q",
			key,
		)
	}

	committed = true

	entry := &CacheEntry{Hash: key, Path: dest, SizeBytes: written}
	if err := c.index.Put(ctx, entry); err != nil {
		// The metadata is what makes the blob findable, so a blob with no row is
		// dead weight. Drop it rather than leaving an orphan nobody will prune.
		_ = os.Remove(dest)
		return nil, err
	}
	// Put keeps LastAccess at the zero time for a fresh row only when the index
	// wants to; normalise here so callers can rely on it being set.
	stored, err := c.index.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	return stored, nil
}

// Open returns a reader for the blob stored under hash.
//
// A missing metadata row and a present row whose file has vanished are reported
// the same way (ErrCacheMiss) on purpose: both mean "not in the cache", and the
// caller's correct response — download it again — is identical.
func (c *Cache) Open(ctx context.Context, hash string) (*os.File, error) {
	entry, err := c.Entry(ctx, hash)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(entry.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, appErrors.Wrap(err, appErrors.ErrCodeCacheMiss,
				"cache blob for %q is recorded but missing at %q", entry.Hash, entry.Path)
		}

		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"open cache blob for %q",
			entry.Hash,
		)
	}

	return f, nil
}

// Remove deletes both the blob and the metadata for hash. Removing an unknown
// key succeeds, so uninstall can call it unconditionally.
func (c *Cache) Remove(ctx context.Context, hash string) error {
	key, err := NormalizeCacheKey(hash)
	if err != nil {
		return err
	}

	if entry, err := c.index.Get(ctx, key); err == nil {
		if err := os.Remove(entry.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"remove cache blob %q",
				entry.Path,
			)
		}
	} else if appErrors.CodeOf(err) != appErrors.ErrCodeCacheMiss {
		return err
	}

	return c.index.Delete(ctx, key)
}

// RemoveAll deletes every blob and every metadata row.
//
// Refs are deliberately ignored: "clear the cache" is an explicit user action
// and the affected mods can be re-downloaded on demand. It is not the same
// operation as pruning, which must never remove a referenced archive.
func (c *Cache) RemoveAll(ctx context.Context) error {
	entries, err := c.index.List(ctx)
	if err != nil {
		return err
	}

	var firstErr error

	for i := range entries {
		if err := os.Remove(entries[i].Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			if firstErr == nil {
				firstErr = appErrors.Wrap(
					err,
					appErrors.ErrCodeConfigIO,
					"remove cache blob %q",
					entries[i].Path,
				)
			}
		}
	}

	if err := c.index.Clear(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	// Prune the now-empty fan-out directories so a cleared cache does not leave
	// thousands of empty folders behind on Windows.
	if c.rootPath != "" {
		if err := removeEmptySubdirs(c.blobsDir); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// Exists reports whether a blob for hash is present on disk, checking the file
// rather than only the metadata: an archive deleted by a disk-cleanup tool has
// a row but no bytes, and treating that as a hit would make every install fail
// at the extraction step with a confusing error.
func (c *Cache) Exists(ctx context.Context, hash string) (bool, error) {
	entry, err := c.Entry(ctx, hash)
	if err != nil {
		if appErrors.CodeOf(err) == appErrors.ErrCodeCacheMiss {
			return false, nil
		}

		return false, err
	}

	if _, err := os.Stat(entry.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"stat cache blob %q",
			entry.Path,
		)
	}

	return true, nil
}

// usableEntry returns the entry for key when it is safe to hand back as a cache
// hit, or nil when the caller must download. A recorded entry whose blob is gone
// is self-healing: the stale row is dropped so the fresh write can take its
// place instead of tripping over the primary key.
func (c *Cache) usableEntry(ctx context.Context, key string) (*CacheEntry, error) {
	entry, err := c.index.Get(ctx, key)
	if err != nil {
		if appErrors.CodeOf(err) == appErrors.ErrCodeCacheMiss {
			return nil, nil
		}

		return nil, err
	}

	if _, statErr := os.Stat(entry.Path); statErr == nil {
		return entry, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, appErrors.Wrap(
			statErr,
			appErrors.ErrCodeConfigIO,
			"stat cache blob %q",
			entry.Path,
		)
	}

	if err := c.index.Delete(ctx, key); err != nil {
		return nil, err
	}

	return nil, nil
}

// removeEmptySubdirs deletes every subdirectory of root that has no entries,
// then root itself. Missing directories are not an error: the goal state is
// "no empty directories", and it already holds.
func removeEmptySubdirs(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read cache directory %q", root)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if err := removeEmptySubdirs(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}

	if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The directory is not empty, which is the normal case when there are
		// still blobs: not an error.
		if errors.Is(err, os.ErrExist) {
			return nil
		}

		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return nil
		}

		return appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"remove empty cache directory %q",
			root,
		)
	}

	return nil
}

// sqliteCacheIndex is the CacheIndex backed by the manager database.
type sqliteCacheIndex struct{ db *sql.DB }

// NewSQLiteCacheIndex returns a CacheIndex over an already-open database. The
// database must already carry the schema; callers normally get there through
// Open or NewCache.
func NewSQLiteCacheIndex(db *sql.DB) CacheIndex { return &sqliteCacheIndex{db: db} }

// NewSQLiteCacheIndexAt opens the cache index at path, creating and initialising
// the database if necessary.
func NewSQLiteCacheIndexAt(ctx context.Context, path string) (CacheIndex, error) {
	db, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}

	return &sqliteCacheIndex{db: db}, nil
}

const cacheEntryColumns = "hash, path, size_bytes, refs, last_access"

func (s *sqliteCacheIndex) Get(ctx context.Context, hash string) (*CacheEntry, error) {
	if s == nil || s.db == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "cache index is not open")
	}

	key, err := NormalizeCacheKey(hash)
	if err != nil {
		return nil, err
	}

	row := s.db.QueryRowContext(
		ctx,
		"SELECT "+cacheEntryColumns+" FROM cache_entries WHERE hash = ?",
		key,
	)

	entry, err := scanCacheEntry(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeCacheMiss,
				"no cache entry for %q",
				key,
			)
		}

		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read cache entry %q", key)
	}

	return entry, nil
}

func (s *sqliteCacheIndex) Put(ctx context.Context, entry *CacheEntry) error {
	if s == nil || s.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "cache index is not open")
	}

	if entry == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "nil cache entry")
	}

	key, err := NormalizeCacheKey(entry.Hash)
	if err != nil {
		return err
	}

	access := entry.LastAccess
	if access.IsZero() {
		access = time.Now().UTC()
	}

	refs := entry.Refs
	if refs < 0 {
		// A negative count would make every "is it referenced?" test wrong in a
		// way that silently deletes a live archive, so it is clamped rather than
		// stored.
		refs = 0
	}

	size := entry.SizeBytes
	if size < 0 {
		size = 0
	}

	_, err = s.db.ExecContext(
		ctx,
		"INSERT INTO cache_entries(hash, path, size_bytes, refs, last_access) VALUES(?, ?, ?, ?, ?) "+
			"ON CONFLICT(hash) DO UPDATE SET path = excluded.path, size_bytes = excluded.size_bytes, "+
			"refs = excluded.refs, last_access = excluded.last_access",
		key,
		entry.Path,
		size,
		refs,
		formatTime(access),
	)

	return wrapKeepingCode(err, appErrors.ErrCodeConfigIO, "write cache entry %q", key)
}

func (s *sqliteCacheIndex) Acquire(
	ctx context.Context,
	hash string,
	at time.Time,
) (*CacheEntry, error) {
	return s.mutate(ctx, hash, at, 1)
}

func (s *sqliteCacheIndex) Release(ctx context.Context, hash string) (*CacheEntry, error) {
	return s.mutate(ctx, hash, time.Time{}, -1)
}

// mutate adjusts the reference count and, when at is non-zero, the last-access
// stamp, inside one transaction so two concurrent acquires cannot both read the
// same count and write back a single increment.
func (s *sqliteCacheIndex) mutate(
	ctx context.Context,
	hash string,
	at time.Time,
	delta int,
) (*CacheEntry, error) {
	if s == nil || s.db == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "cache index is not open")
	}

	key, err := NormalizeCacheKey(hash)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"begin cache transaction for %q",
			key,
		)
	}

	defer func() { _ = tx.Rollback() }()

	entry, err := scanCacheEntry(
		tx.QueryRowContext(
			ctx,
			"SELECT "+cacheEntryColumns+" FROM cache_entries WHERE hash = ?",
			key,
		),
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeCacheMiss,
				"no cache entry for %q",
				key,
			)
		}

		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read cache entry %q", key)
	}

	next := entry.Refs + delta
	if next < 0 {
		next = 0
	}

	lastAccess := entry.LastAccess
	if !at.IsZero() {
		lastAccess = at.UTC()
	}

	_, err = tx.ExecContext(
		ctx,
		"UPDATE cache_entries SET refs = ?, last_access = ? WHERE hash = ?",
		next,
		formatTime(lastAccess),
		key,
	)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "update cache entry %q", key)
	}

	if err := tx.Commit(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"commit cache transaction for %q",
			key,
		)
	}

	entry.Refs = next
	entry.LastAccess = lastAccess

	return entry, nil
}

func (s *sqliteCacheIndex) Touch(ctx context.Context, hash string, at time.Time) error {
	if s == nil || s.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "cache index is not open")
	}

	key, err := NormalizeCacheKey(hash)
	if err != nil {
		return err
	}

	if at.IsZero() {
		at = time.Now().UTC()
	}

	res, err := s.db.ExecContext(
		ctx,
		"UPDATE cache_entries SET last_access = ? WHERE hash = ?",
		formatTime(at),
		key,
	)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "touch cache entry %q", key)
	}

	if affected, _ := res.RowsAffected(); affected == 0 {
		return appErrors.New(appErrors.ErrCodeCacheMiss, "no cache entry for %q", key)
	}

	return nil
}

func (s *sqliteCacheIndex) List(ctx context.Context) ([]CacheEntry, error) {
	if s == nil || s.db == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "cache index is not open")
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT "+cacheEntryColumns+" FROM cache_entries ORDER BY last_access ASC, hash ASC")
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "list cache entries")
	}

	defer func() { _ = rows.Close() }()

	out := []CacheEntry{}

	for rows.Next() {
		entry, err := scanCacheEntry(rows)
		if err != nil {
			return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "list cache entries")
		}

		out = append(out, *entry)
	}

	if err := rows.Err(); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "list cache entries")
	}

	return out, nil
}

func (s *sqliteCacheIndex) Delete(ctx context.Context, hash string) error {
	if s == nil || s.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "cache index is not open")
	}

	key, err := NormalizeCacheKey(hash)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, "DELETE FROM cache_entries WHERE hash = ?", key)

	return wrapKeepingCode(err, appErrors.ErrCodeConfigIO, "delete cache entry %q", key)
}

func (s *sqliteCacheIndex) Clear(ctx context.Context) error {
	if s == nil || s.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "cache index is not open")
	}

	_, err := s.db.ExecContext(ctx, "DELETE FROM cache_entries")

	return wrapKeepingCode(err, appErrors.ErrCodeConfigIO, "clear cache entries")
}

func (s *sqliteCacheIndex) Close() error {
	if s == nil || s.db == nil {
		return nil
	}

	err := s.db.Close()
	s.db = nil

	return wrapKeepingCode(err, appErrors.ErrCodeConfigIO, "close cache index")
}

// scanCacheEntry reads one cache_entries row.
//
// A missing row surfaces as sql.ErrNoRows so callers can tell "no entry" apart
// from "could not read the table"; Get translates it into ErrCacheMiss.
func scanCacheEntry(row rowScanner) (*CacheEntry, error) {
	var (
		entry      CacheEntry
		size       int64
		refs       int
		lastAccess string
	)
	if err := row.Scan(&entry.Hash, &entry.Path, &size, &refs, &lastAccess); err != nil {
		return nil, err
	}

	entry.SizeBytes = size
	entry.Refs = refs
	entry.LastAccess = parseTime(lastAccess)

	return &entry, nil
}
