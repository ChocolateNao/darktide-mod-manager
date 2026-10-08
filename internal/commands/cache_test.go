package commands

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetCacheInfoReportsTheCacheRoot.
func TestGetCacheInfoReportsTheCacheRoot(t *testing.T) {
	w := newWorkspace(t)
	app := w.mustApp(t)

	info, err := app.Cache.GetCacheInfo()
	require.NoError(t, err, "GetCacheInfo on an empty cache")

	assert.Equal(t, w.cacheRoot, info.Path, "cache path = %q, want %q", info.Path, w.cacheRoot)

	assert.False(
		t,
		info.FileCount != 0 || info.TotalSize != 0,
		"an empty cache reported %d files and %d bytes",
		info.FileCount,
		info.TotalSize,
	)

	root, err := app.Cache.CacheRoot()
	require.NoError(t, err, "CacheRoot")

	assert.Equal(t, w.cacheRoot, root, "CacheRoot = %q, want %q", root, w.cacheRoot)
}

// TestClearCacheEmptiesItAndSaysSo.
func TestClearCacheEmptiesItAndSaysSo(t *testing.T) {
	_, app, handler := appWithHandler(t)
	{
		err := app.Cache.ClearCache()
		require.NoError(t, err, "ClearCache on an empty cache: %v", err)
	}

	assert.True(
		t,
		handler.has(api.EventCacheCleared),
		"ClearCache did not publish %s",
		api.EventCacheCleared,
	)
}

// cacheWorkspace builds a workspace whose configuration expires archives after a
// day, one stored archive whose last access is far older than that, and the
// container behind the surface. The stale archive plus a small age limit are what
// let a test tell the configured limits apart from "no limits".
func cacheWorkspace(t *testing.T, maxAgeDays int) (*workspace, *App, *di.Container) {
	t.Helper()
	w := newWorkspace(t)
	w.writeConfig(t, func(cfg *domain.Config) {
		cfg.Cache.Path = w.cacheRoot
		cfg.Cache.MaxAgeDays = maxAgeDays
		cfg.Cache.MaxSizeGB = 5
	})
	c := w.container(t)
	app := New(Deps{Client: c.Client})
	storeArchive(t, w, c, "stale-archive", "payload bytes", 365*24)

	return w, app, c
}

// storeArchive puts one blob in the cache and ages its last-access stamp.
//
// The stamp is written straight into the index because the public surface has no
// way to backdate an entry, and a test that slept for a day would not be a test.
// The index is opened as a second connection, which is exactly what a second
// process looking at the same cache directory does.
func storeArchive(
	t *testing.T,
	w *workspace,
	c *di.Container,
	name, body string,
	ageHours int,
) string {
	t.Helper()

	hash := platform.StringSHA256([]byte(name))

	path, err := c.CacheManager.Store(t.Context(), hash, strings.NewReader(body))
	require.NoError(t, err, "store %s: %v", name)

	index, err := storage.NewSQLiteCacheIndexAt(t.Context(), filepath.Join(w.cacheRoot, "cache.db"))
	require.NoError(t, err, "open the cache index")

	t.Cleanup(func() { _ = index.Close() })

	entry, err := index.Get(t.Context(), hash)
	require.NoError(t, err, "read the stored entry")

	entry.LastAccess = entry.LastAccess.Add(-time.Duration(ageHours) * time.Hour)
	{
		err := index.Put(t.Context(), entry)
		require.NoError(t, err, "backdate the entry: %v", err)
	}

	return path
}

func cacheFileCount(t *testing.T, app *App) int {
	t.Helper()

	info, err := app.Cache.GetCacheInfo()
	require.NoError(t, err, "GetCacheInfo")

	return info.FileCount
}

// TestPruneCacheWithNoArgumentsUsesTheConfiguredLimits is the assertion that both
// zeros do not mean "no limits": with the configuration expiring archives after a
// day and one archive a year stale, the archive has to go.
func TestPruneCacheWithNoArgumentsUsesTheConfiguredLimits(t *testing.T) {
	w, app, c := cacheWorkspace(t, 1)
	{
		got := cacheFileCount(t, app)
		require.Equal(t, 1, got, "the workspace holds %d archives, want 1", got)
	}

	{
		err := app.Cache.PruneWithDefaults()
		require.NoError(t, err, "PruneWithDefaults: %v", err)
	}

	{
		got := cacheFileCount(t, app)
		assert.Equal(t, 0, got, "%d archives survived a prune that should have expired them", got)
	}

	// The two-argument form with both zeros is the same command, so it has to
	// behave identically — a frontend with no fields next to the button sends
	// exactly that.
	storeArchive(t, w, c, "second", "more bytes", 365*24)

	{
		err := app.Cache.PruneCache(0, 0)
		require.NoError(t, err, "PruneCache(0, 0): %v", err)
	}

	{
		got := cacheFileCount(t, app)
		assert.Equal(t, 0, got, "%d archives survived PruneCache(0, 0)", got)
	}
}

// TestPruneCacheWithNoLimitsKeepsEverything is the other half: a negative on both
// axes means "no limit on either", so the archive survives. Without it the two
// halves of the argument above could not tell each other apart.
func TestPruneCacheWithNoLimitsKeepsEverything(t *testing.T) {
	_, app, _ := cacheWorkspace(t, 1)
	{
		err := app.Cache.PruneCache(-1, -1)
		require.NoError(t, err, "PruneCache(-1, -1): %v", err)
	}

	{
		got := cacheFileCount(t, app)
		assert.Equal(
			t,
			1,
			got,
			"a prune with no limits on either axis left %d archives, want 1",
			got,
		)
	}
}

// TestPruneCacheAppliesEachAxisIndependently: a zero on one axis is no limit on
// that axis, so the archive the configured age would have expired survives a
// prune that sets only a size limit, and does not survive one that sets the age.
func TestPruneCacheAppliesEachAxisIndependently(t *testing.T) {
	_, app, _ := cacheWorkspace(t, 1)
	{
		err := app.Cache.PruneCache(5, 0)
		require.NoError(t, err, "PruneCache(5, 0): %v", err)
	}

	{
		got := cacheFileCount(t, app)
		assert.Equal(
			t,
			1,
			got,
			"PruneCache(5, 0) left %d archives; a zero age means no age limit",
			got,
		)
	}

	{
		err := app.Cache.PruneCache(0, 1)
		require.NoError(t, err, "PruneCache(0, 1): %v", err)
	}

	{
		got := cacheFileCount(t, app)
		assert.Equal(t, 0, got, "%d archives survived PruneCache(0, 1)", got)
	}
}

// TestASuccessfulPruneIsNotLogged: the log is for failures, and filling it with
// routine successes would bury the one line a bug report needs. The other
// direction — that a failure *is* logged — is covered by
// TestFailedOperationsAreLogged, which drives a real MOD_NOT_FOUND rather than
// inventing one here.
func TestASuccessfulPruneIsNotLogged(t *testing.T) {
	logger := &recordingLogger{}
	w := newWorkspace(t)
	c := w.container(t)
	app := New(Deps{Client: c.Client, Logger: logger})

	{
		err := app.Cache.PruneCache(1, 1)
		require.NoError(t, err, "PruneCache on a healthy cache: %v", err)
	}

	{
		err := app.Cache.ClearCache()
		require.NoError(t, err, "ClearCache: %v", err)
	}

	{
		lines := logger.snapshot()
		assert.Empty(t, lines, "successful operations were logged: %q", lines)
	}
}

// TestRefusedCallsAreNotLogged: a set that was never wired is the caller's own
// bug, and filling the log with it would bury the one line that mattered.
func TestRefusedCallsAreNotLogged(t *testing.T) {
	logger := &recordingLogger{}

	app := New(Deps{Logger: logger})
	{
		err := app.Cache.PruneCache(1, 1)
		require.Error(t, err, "PruneCache ran without a client")
	}

	{
		lines := logger.snapshot()
		assert.Empty(t, lines, "a refused call was logged: %q", lines)
	}
}

// appWithHandler builds the surface with an event handler wired through the
// container, which is the route a GUI uses.
func appWithHandler(t *testing.T) (*workspace, *App, *recordingHandler) {
	t.Helper()
	w := newWorkspace(t)
	handler := &recordingHandler{}
	c := w.container(t, func(o *di.Options) { o.EventHandler = handler.handle })

	return w, New(Deps{Client: c.Client}), handler
}
