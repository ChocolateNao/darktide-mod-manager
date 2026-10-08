package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// key builds a syntactically valid cache key from a short seed. Tests care about
// behaviour, not about how a key is derived, so the seed is hashed and the
// digest used verbatim — which also keeps every key the right length and every
// character lowercase hex, exactly as NormalizeCacheKey demands.
func key(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func newCache(t *testing.T) *Cache {
	t.Helper()

	c, err := NewCache(filepath.Join(t.TempDir(), "cache"))
	require.NoError(t, err, "NewCache")

	t.Cleanup(func() { _ = c.Close() })

	return c
}

func newIndex(t *testing.T) CacheIndex {
	t.Helper()

	db, err := Open(context.Background(), MemoryPath)
	require.NoError(t, err, "Open")

	idx := NewSQLiteCacheIndex(db)

	t.Cleanup(func() { _ = idx.Close() })

	return idx
}

func TestNormalizeCacheKeyAcceptsLowercaseHexDigest(t *testing.T) {
	got, err := NormalizeCacheKey(key("a"))
	require.NoError(t, err, "NormalizeCacheKey")

	assert.Len(t, got, 64, "canonical key has %d characters, want 64", len(got))
}

func TestNormalizeCacheKeyTrimsSurroundingSpace(t *testing.T) {
	want := key("b")

	got, err := NormalizeCacheKey("  " + want + "\n")
	require.NoError(t, err, "NormalizeCacheKey")

	assert.Equal(t, want, got, "key = %q, want %q", got, want)
}

func TestNormalizeCacheKeyRejectsEverythingThatIsNotADigest(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		// A key becomes a filename. Accepting "../.." would let a caller write
		// outside the cache directory, so it is rejected rather than sanitised.
		{"path traversal", "../../etc/passwd"},
		{"separator", "aa/bb"},
		{"backslash", `aa\bb`},
		{"too short", "abc123"},
		{"too long", key("c") + "00"},
		{"uppercase hex", strings.ToUpper(key("d"))},
		{"non hex letters", strings.Repeat("z", 64)},
		{"embedded space", strings.Repeat("a", 63) + " "},
	}
	for _, tc := range cases {
		if _, err := NormalizeCacheKey(tc.in); err == nil {
			assert.Fail(t, "%s: NormalizeCacheKey(%q) succeeded, want an error", tc.name, tc.in)
		} else if code := appErrors.CodeOf(err); code != appErrors.ErrCodeInvalidArgument {
			assert.Fail(
				t,
				"%s: code = %s, want %s",
				tc.name,
				code,
				appErrors.ErrCodeInvalidArgument,
			)
		}
	}
}

func TestNewCacheRejectsEmptyRoot(t *testing.T) {
	if _, err := NewCache("   "); err == nil {
		require.FailNow(t, "NewCache with a blank root should fail")
	} else if code := appErrors.CodeOf(err); code != appErrors.ErrCodeNoStoragePath {
		assert.Fail(t, "code = %s, want %s", code, appErrors.ErrCodeNoStoragePath)
	}
}

func TestNewCacheResolvesRootToAbsolute(t *testing.T) {
	c, err := NewCacheWithFS(filepath.Join(t.TempDir(), "cache"), nil)
	require.NoError(t, err, "NewCacheWithFS")

	assert.True(
		t,
		filepath.IsAbs(c.RootPath()),
		"RootPath = %q, want an absolute path",
		c.RootPath(),
	)
}

func TestCacheBlobPathFansOutByFirstByte(t *testing.T) {
	c := newCache(t)
	k := key("ab")

	got, err := c.BlobPath(k)
	require.NoError(t, err, "BlobPath")

	want := filepath.Join(c.RootPath(), "blobs", k[:2], k)
	assert.Equal(t, want, got, "BlobPath = %q, want %q", got, want)
}

func TestCacheBlobPathRejectsBadKeyBeforeCheckingRoot(t *testing.T) {
	c, err := NewMemoryCache()
	require.NoError(t, err, "NewMemoryCache")

	t.Cleanup(func() { _ = c.Close() })

	_, err = c.BlobPath("nope")
	require.Error(t, err, "BlobPath should reject a non-digest key")
}

func TestCacheWriteStoresBlobAndMetadata(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	k := key("w")

	entry, err := c.Write(ctx, k, strings.NewReader("payload"))
	require.NoError(t, err, "Write")

	assert.Equal(t, k, entry.Hash, "Hash = %q, want %q", entry.Hash, k)

	assert.Equal(
		t,
		int64(len("payload")),
		entry.SizeBytes,
		"SizeBytes = %d, want %d",
		entry.SizeBytes,
		len("payload"),
	)

	assert.False(t, entry.LastAccess.IsZero(), "a fresh entry should carry a last-access stamp")

	body, err := os.ReadFile(entry.Path)
	require.NoError(t, err, "read blob")

	assert.Equal(t, "payload", string(body), "blob = %q, want %q", body, "payload")
}

func TestCacheWriteIsIdempotentForAnExistingBlob(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	k := key("id")

	_, err := c.Write(ctx, k, strings.NewReader("first"))
	require.NoError(t, err, "first Write: %v", err)

	entry, err := c.Write(ctx, k, strings.NewReader("second"))
	require.NoError(t, err, "second Write")

	body, err := os.ReadFile(entry.Path)
	require.NoError(t, err, "read blob")

	assert.Equal(
		t,
		"first",
		string(body),
		"blob = %q, want the cached %q to survive a repeat Write",
		body,
		"first",
	)
}

func TestCacheWriteReplacesARowWhoseBlobVanished(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	k := key("vanish")

	first, err := c.Write(ctx, k, strings.NewReader("first"))
	require.NoError(t, err, "Write")

	err = os.Remove(first.Path)
	require.NoError(t, err, "remove blob: %v", err)

	entry, err := c.Write(ctx, k, strings.NewReader("second"))
	require.NoError(t, err, "Write after the blob disappeared")

	body, err := os.ReadFile(entry.Path)
	require.NoError(t, err, "read blob")

	assert.Equal(t, "second", string(body), "blob = %q, want %q", body, "second")
}

func TestCacheWriteLeavesNoPartialFileWhenTheSourceFails(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	k := key("fail")

	failure := errors.New("network died")
	if _, err := c.Write(ctx, k, iotest_errReader{err: failure}); err == nil {
		require.FailNow(t, "Write should fail when the source fails")
	} else if !errors.Is(err, failure) {
		assert.Fail(t, "error = %v, want it to wrap %v", err, failure)
	}

	// A .partial-* file left behind would be indistinguishable from a cache
	// entry to any later directory scan and would never be cleaned up.
	dir := filepath.Join(c.RootPath(), "blobs", k[:2])

	names, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return // nothing was created at all, which is also fine
		}

		require.FailNow(t, "read blob directory: %v", err)
	}

	for _, name := range names {
		assert.Fail(t, "partial file %q was left behind", name.Name())
	}

	_, err = c.index.Get(ctx, k)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"a failed write left metadata behind: %v",
		err,
	)
}

func TestCacheWriteRejectsNilReader(t *testing.T) {
	c := newCache(t)
	_, err := c.Write(context.Background(), key("nil"), nil)
	require.Error(t, err, "Write with no reader should fail")
}

func TestCacheExistsChecksTheFileNotJustTheRow(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	k := key("exists")

	ok, err := c.Exists(ctx, k)
	require.False(t, err != nil || ok, "Exists before Write = %v, %v; want false, nil", ok, err)

	entry, err := c.Write(ctx, k, strings.NewReader("x"))
	require.NoError(t, err, "Write")

	ok, err = c.Exists(ctx, k)
	require.False(t, err != nil || !ok, "Exists after Write = %v, %v; want true, nil", ok, err)

	err = os.Remove(entry.Path)
	require.NoError(t, err, "remove blob: %v", err)
	// A row without bytes is the state a disk-cleanup tool leaves behind.
	// Reporting a hit here would fail every install at the extraction step.
	ok, err = c.Exists(ctx, k)
	require.False(
		t,
		err != nil || ok,
		"Exists after the blob was deleted = %v, %v; want false, nil",
		ok,
		err,
	)
}

func TestCacheExistsReportsInvalidKeysAsAnError(t *testing.T) {
	c := newCache(t)
	_, err := c.Exists(context.Background(), "short")
	require.Error(t, err, "Exists should reject a non-digest key")
}

func TestCacheOpenReportsMissingBlobAsCacheMiss(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	k := key("open")

	entry, err := c.Write(ctx, k, strings.NewReader("body"))
	require.NoError(t, err, "Write")

	f, err := c.Open(ctx, k)
	require.NoError(t, err, "Open")

	body, err := io.ReadAll(f)
	_ = f.Close()

	require.NoError(t, err, "read")

	assert.Equal(t, "body", string(body), "body = %q, want %q", body, "body")

	err = os.Remove(entry.Path)
	require.NoError(t, err, "remove blob: %v", err)
	// A missing row and a row without a file mean the same thing to a caller:
	// download it again.
	_, err = c.Open(ctx, k)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"Open after the blob was deleted = %v, want ErrCodeCacheMiss",
		err,
	)
}

func TestCacheRemoveDeletesBlobAndRow(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	k := key("rm")

	entry, err := c.Write(ctx, k, strings.NewReader("x"))
	require.NoError(t, err, "Write")

	err = c.Remove(ctx, k)
	require.NoError(t, err, "Remove: %v", err)

	_, err = os.Stat(entry.Path)
	assert.True(t, os.IsNotExist(err), "blob still present after Remove: %v", err)

	_, err = c.index.Get(ctx, k)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"row still present after Remove: %v",
		err,
	)
}

func TestCacheRemoveIsIdempotentAndValidatesTheKey(t *testing.T) {
	c := newCache(t)

	ctx := context.Background()
	err := c.Remove(ctx, key("absent"))
	assert.NoError(t, err, "removing an unknown key = %v, want nil", err)

	err = c.Remove(ctx, "../escape")
	assert.Error(t, err, "Remove should reject a non-digest key")
}

func TestCacheRemoveAllIgnoresReferences(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()

	k := key("pinned")
	_, err := c.Write(ctx, k, strings.NewReader("x"))
	require.NoError(t, err, "Write: %v", err)

	_, err = c.index.Acquire(ctx, k, fixedTime)
	require.NoError(t, err, "Acquire: %v", err)

	err = c.RemoveAll(ctx)
	require.NoError(t, err, "RemoveAll: %v", err)

	entries, err := c.index.List(ctx)
	require.NoError(t, err, "List")

	assert.Empty(t, entries, "List after RemoveAll = %d entries, want 0", len(entries))

	_, err = os.Stat(filepath.Join(c.RootPath(), "blobs", k[:2], k))
	assert.True(t, os.IsNotExist(err), "blob survived RemoveAll: %v", err)
}

func TestCacheRemoveAllPrunesEmptyFanoutDirectories(t *testing.T) {
	c := newCache(t)

	ctx := context.Background()
	for _, seed := range []string{"p", "q"} {
		_, err := c.Write(ctx, key(seed), strings.NewReader("x"))
		require.NoError(t, err, "Write: %v", err)
	}

	err := c.RemoveAll(ctx)
	require.NoError(t, err, "RemoveAll: %v", err)
	// A cleared cache that leaves hundreds of empty two-character folders behind
	// still costs the same directory walk on the next run.
	_, err = os.Stat(filepath.Join(c.RootPath(), "blobs"))
	assert.True(t, os.IsNotExist(err), "blobs directory survived RemoveAll: %v", err)
}

func TestCacheIndexAcquireAndReleaseTrackReferences(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	k := key("ref")

	if err := idx.Put(ctx, &CacheEntry{Hash: k, Path: "/tmp/x", SizeBytes: 3}); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	first, err := idx.Acquire(ctx, k, fixedTime)
	require.NoError(t, err, "Acquire")

	require.Equal(t, 1, first.Refs, "Refs after one acquire = %d, want 1", first.Refs)

	assert.True(
		t,
		first.LastAccess.Equal(fixedTime),
		"LastAccess = %v, want %v",
		first.LastAccess,
		fixedTime,
	)

	second, err := idx.Acquire(ctx, k, fixedTime.Add(time.Hour))
	require.NoError(t, err, "second Acquire")

	assert.Equal(t, 2, second.Refs, "Refs after two acquires = %d, want 2", second.Refs)

	released, err := idx.Release(ctx, k)
	require.NoError(t, err, "Release")

	assert.Equal(t, 1, released.Refs, "Refs after one release = %d, want 1", released.Refs)
}

func TestCacheIndexReleaseNeverGoesNegative(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	k := key("neg")
	if err := idx.Put(ctx, &CacheEntry{Hash: k, Path: "/tmp/x"}); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	for i := 0; i < 3; i++ {
		entry, err := idx.Release(ctx, k)
		require.NoError(t, err, "Release %d: %v", i)

		require.Equal(t, 0, entry.Refs, "Refs after release %d = %d, want 0", i, entry.Refs)
	}
}

func TestCacheIndexReleaseDoesNotRefreshLastAccess(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	k := key("touch")

	stamp := fixedTime.Add(-72 * time.Hour)
	if err := idx.Put(ctx, &CacheEntry{Hash: k, Path: "/tmp/x", LastAccess: stamp}); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	released, err := idx.Release(ctx, k)
	require.NoError(t, err, "Release")
	// Refreshing on release would make a long-unused archive look fresh and let it
	// outlive every age-based prune.
	assert.True(
		t,
		released.LastAccess.Equal(stamp.UTC()),
		"LastAccess = %v, want the stored %v",
		released.LastAccess,
		stamp.UTC(),
	)
}

func TestCacheIndexAcquireAndReleaseRequireAnEntry(t *testing.T) {
	idx := newIndex(t)

	ctx := context.Background()
	_, err := idx.Acquire(ctx, key("none"), fixedTime)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"Acquire on an unknown key = %v, want ErrCodeCacheMiss",
		err,
	)

	_, err = idx.Release(ctx, key("none"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"Release on an unknown key = %v, want ErrCodeCacheMiss",
		err,
	)
}

func TestCacheIndexPutClampsNonsenseValues(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	k := key("clamp")
	if err := idx.Put(
		ctx,
		&CacheEntry{Hash: k, Path: "/tmp/x", SizeBytes: -1, Refs: -5},
	); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	entry, err := idx.Get(ctx, k)
	require.NoError(t, err, "Get")

	assert.Equal(t, int64(0), entry.SizeBytes, "SizeBytes = %d, want 0", entry.SizeBytes)
	// A negative count makes every "is it referenced?" test wrong in a way that
	// silently deletes a live archive.
	assert.Equal(t, 0, entry.Refs, "Refs = %d, want 0", entry.Refs)

	assert.False(
		t,
		entry.LastAccess.IsZero(),
		"Put should stamp last_access when the caller left it zero",
	)
}

func TestCacheIndexPutReplacesAnExistingRow(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	k := key("put")
	if err := idx.Put(
		ctx,
		&CacheEntry{Hash: k, Path: "/tmp/a", SizeBytes: 1, Refs: 3},
	); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	if err := idx.Put(
		ctx,
		&CacheEntry{Hash: k, Path: "/tmp/b", SizeBytes: 2, Refs: 1},
	); err != nil {
		require.FailNow(t, "second Put: %v", err)
	}

	entry, err := idx.Get(ctx, k)
	require.NoError(t, err, "Get")

	assert.False(
		t,
		entry.Path != "/tmp/b" || entry.SizeBytes != 2 || entry.Refs != 1,
		"entry = %+v, want the second Put to win",
		entry,
	)
}

func TestCacheIndexPutRejectsNilAndBadKeys(t *testing.T) {
	idx := newIndex(t)

	ctx := context.Background()
	err := idx.Put(ctx, nil)
	assert.Error(t, err, "Put(nil) should fail")

	if err := idx.Put(ctx, &CacheEntry{Hash: "nope"}); err == nil {
		assert.Fail(t, "Put should reject a non-digest key")
	}
}

func TestCacheIndexListIsOrderedByLastAccessThenHash(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	older, sameTime, newer := key("o"), key("s"), key("n")
	if err := idx.Put(
		ctx,
		&CacheEntry{Hash: newer, Path: "/n", LastAccess: fixedTime.Add(2 * time.Hour)},
	); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	if err := idx.Put(
		ctx,
		&CacheEntry{Hash: older, Path: "/o", LastAccess: fixedTime},
	); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	if err := idx.Put(
		ctx,
		&CacheEntry{Hash: sameTime, Path: "/s", LastAccess: fixedTime},
	); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	entries, err := idx.List(ctx)
	require.NoError(t, err, "List")
	// Two entries share a timestamp, so the hash tiebreak is what makes the order
	// stable — and the order pruning evicts in reproducible across runs. The
	// expectation is built by sorting the two tied keys, never by seed order.
	tied := []string{older, sameTime}
	sort.Strings(tied)

	want := []string{tied[0], tied[1], newer}
	require.Len(t, entries, len(want), "List returned %d entries, want %d", len(entries), len(want))

	for i, w := range want {
		assert.Equal(t, w, entries[i].Hash, "entry %d = %s, want %s", i, entries[i].Hash, w)
	}
}

func TestCacheIndexListOnAnEmptyIndexReturnsAnEmptySlice(t *testing.T) {
	idx := newIndex(t)

	entries, err := idx.List(context.Background())
	require.NoError(t, err, "List")
	// A nil slice would serialise as JSON null, which the GUI renders as a
	// broken table instead of an empty one.
	require.NotNil(t, entries, "List returned nil, want an empty slice")

	assert.Empty(t, entries, "List = %d entries, want 0", len(entries))
}

func TestCacheIndexDeleteAndClearAreIdempotent(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	k := key("del")
	err := idx.Delete(ctx, k)
	assert.NoError(t, err, "Delete on an unknown key = %v, want nil", err)

	if err := idx.Put(ctx, &CacheEntry{Hash: k, Path: "/x"}); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	err = idx.Delete(ctx, k)
	require.NoError(t, err, "Delete: %v", err)

	err = idx.Delete(ctx, k)
	assert.NoError(t, err, "second Delete = %v, want nil", err)

	err = idx.Clear(ctx)
	require.NoError(t, err, "Clear: %v", err)

	err = idx.Clear(ctx)
	assert.NoError(t, err, "second Clear = %v, want nil", err)
}

func TestCacheIndexDeleteRejectsBadKeys(t *testing.T) {
	idx := newIndex(t)
	err := idx.Delete(context.Background(), "../nope")
	assert.Error(t, err, "Delete should reject a non-digest key")
}

func TestCacheIndexTouchUpdatesStampAndRequiresARow(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	k := key("t")
	if err := idx.Put(ctx, &CacheEntry{Hash: k, Path: "/x", Refs: 2}); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	err := idx.Touch(ctx, k, fixedTime)
	require.NoError(t, err, "Touch: %v", err)

	entry, err := idx.Get(ctx, k)
	require.NoError(t, err, "Get")

	assert.True(
		t,
		entry.LastAccess.Equal(fixedTime),
		"LastAccess = %v, want %v",
		entry.LastAccess,
		fixedTime,
	)

	assert.Equal(t, 2, entry.Refs, "Refs = %d, want Touch to leave 2 alone", entry.Refs)

	err = idx.Touch(ctx, key("absent"), fixedTime)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"Touch on an unknown key = %v, want ErrCodeCacheMiss",
		err,
	)
}

func TestCacheIndexTouchWithZeroStampUsesNow(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	k := key("z")
	if err := idx.Put(ctx, &CacheEntry{Hash: k, Path: "/x"}); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	before := time.Now().UTC().Add(-time.Second)

	if err := idx.Touch(ctx, k, time.Time{}); err != nil {
		require.FailNow(t, "Touch: %v", err)
	}

	entry, err := idx.Get(ctx, k)
	require.NoError(t, err, "Get")

	assert.False(
		t,
		entry.LastAccess.Before(before),
		"LastAccess = %v, want a stamp at or after %v",
		entry.LastAccess,
		before,
	)
}

func TestCacheIndexGetReportsMissForUnknownKey(t *testing.T) {
	idx := newIndex(t)
	_, err := idx.Get(context.Background(), key("u"))
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"Get on an unknown key = %v, want ErrCodeCacheMiss",
		err,
	)
}

func TestCacheIndexConcurrentAcquireDoesNotLoseUpdates(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	k := key("race")
	if err := idx.Put(ctx, &CacheEntry{Hash: k, Path: "/x"}); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	const n = 16

	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()

			_, err := idx.Acquire(ctx, k, fixedTime)
			assert.NoError(t, err, "Acquire: %v", err)
		}()
	}

	wg.Wait()

	entry, err := idx.Get(ctx, k)
	require.NoError(t, err, "Get")
	// A read-modify-write outside a transaction loses increments here, and a
	// lost increment is an archive that pruning will delete while a mod needs it.
	assert.Equal(t, n, entry.Refs, "Refs = %d, want %d", entry.Refs, n)
}

func TestCacheIndexMethodsOnAClosedIndexReportAnError(t *testing.T) {
	db, err := Open(context.Background(), MemoryPath)
	require.NoError(t, err, "Open")

	idx := NewSQLiteCacheIndex(db)
	err = idx.Close()
	require.NoError(t, err, "Close: %v", err)

	ctx := context.Background()
	_, err = idx.Get(ctx, key("c"))
	assert.Error(t, err, "Get on a closed index should fail")

	err = idx.Delete(ctx, key("c"))
	assert.Error(t, err, "Delete on a closed index should fail")

	_, err = idx.List(ctx)
	assert.Error(t, err, "List on a closed index should fail")
	// Close twice is what happens when both a caller and a deferred cleanup call
	// it; it must be silent rather than a spurious failure.
	err = idx.Close()
	assert.NoError(t, err, "second Close = %v, want nil", err)
}

func TestCacheIndexRoundTripsTimestampPrecision(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	k := key("ts")

	stamp := time.Date(2024, 5, 17, 8, 30, 15, 123000000, time.UTC)
	if err := idx.Put(ctx, &CacheEntry{Hash: k, Path: "/x", LastAccess: stamp}); err != nil {
		require.FailNow(t, "Put: %v", err)
	}

	entry, err := idx.Get(ctx, k)
	require.NoError(t, err, "Get")
	// The storage layout keeps milliseconds; sub-millisecond precision is dropped
	// on purpose so a round-trip is exact for the value the rest of the code
	// writes.
	assert.True(
		t,
		entry.LastAccess.Equal(stamp),
		"LastAccess = %v, want %v",
		entry.LastAccess,
		stamp,
	)
}

func TestMemoryCacheHasNoOnDiskState(t *testing.T) {
	c, err := NewMemoryCache()
	require.NoError(t, err, "NewMemoryCache")

	t.Cleanup(func() { _ = c.Close() })

	assert.Equal(t, "", c.RootPath(), "RootPath = %q, want empty for a memory cache", c.RootPath())

	_, err = c.BlobPath(key("m"))
	assert.Error(t, err, "BlobPath on a memory cache should fail: there is nowhere to put it")
}

func TestCacheCloseLeavesABorrowedIndexUsable(t *testing.T) {
	db, err := Open(context.Background(), MemoryPath)
	require.NoError(t, err, "Open")

	idx := NewSQLiteCacheIndex(db)

	c, err := NewCacheWithFS(filepath.Join(t.TempDir(), "cache"), idx)
	require.NoError(t, err, "NewCacheWithFS")

	err = c.Close()
	require.NoError(t, err, "Close: %v", err)
	// The caller handed the index in, so the caller still owns it — a shared
	// cache.db would break otherwise.
	_, err = idx.Get(context.Background(), key("b"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"borrowed index unusable after Cache.Close: %v",
		err,
	)

	t.Cleanup(func() { _ = idx.Close() })
}

func TestCacheWritePersistsAcrossReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	ctx := context.Background()

	first, err := NewCache(root)
	require.NoError(t, err, "NewCache")

	k := key("persist")
	_, err = first.Write(ctx, k, strings.NewReader("durable"))
	require.NoError(t, err, "Write: %v", err)

	err = first.Close()
	require.NoError(t, err, "Close: %v", err)

	second, err := NewCache(root)
	require.NoError(t, err, "reopen NewCache")

	t.Cleanup(func() { _ = second.Close() })
	// The whole point of keeping the index next to the blobs: a restart must not
	// re-download everything already on disk.
	entry, err := second.Write(ctx, k, strings.NewReader("other"))
	require.NoError(t, err, "Write after reopen")

	body, err := os.ReadFile(entry.Path)
	require.NoError(t, err, "read blob")

	assert.Equal(t, "durable", string(body), "blob = %q, want %q", body, "durable")
}

// iotest_errReader fails on the first Read, which is how the write-failure path
// is exercised without depending on a full disk.
type iotest_errReader struct{ err error }

func (r iotest_errReader) Read([]byte) (int, error) { return 0, r.err }
