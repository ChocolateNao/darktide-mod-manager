package services

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// folderDownloader writes a folder instead of a file, as the local source does
// when a mod's path names a directory rather than an archive.
type folderDownloader struct {
	files map[string]string
	path  string
}

func (d *folderDownloader) Download(
	_ context.Context,
	mod *domain.Mod,
	dest string,
) (string, error) {
	if d.path != "" {
		mod.SourceData = map[string]any{"path": d.path}
	}

	for name, body := range d.files {
		full := filepath.Join(dest, filepath.FromSlash(name))

		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return dest, err
		}

		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			return dest, err
		}
	}

	return dest, nil
}

func (d *folderDownloader) Verify(context.Context, *domain.Mod, string) error { return nil }

// A local mod path may name a folder or an archive; both have to land in the
// cache as the one thing the rest of the pipeline can read, a zip.
func TestGetOrDownloadTurnsAFolderDownloadIntoAnArchive(t *testing.T) {
	m, cache := newRealCache(t)
	m = withDownloader(t, m, &folderDownloader{files: map[string]string{
		"mod.toml":             "id = \"tree\"\n",
		"scripts/init.lua":     "print('hi')\n",
		"assets/deep/logo.png": "png",
	}})
	mod := downloadMod("tree", "1.0.0", "https://example.test/tree")

	blob, err := m.GetOrDownload(context.Background(), mod)
	require.NoError(t, err, "GetOrDownload")

	reader, err := zip.OpenReader(blob)
	require.NoError(t, err, "the cached blob is not a zip: %s", blob)

	defer func() { _ = reader.Close() }()

	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}

	sort.Strings(names)

	// The subfolders are the whole point: a top-level-only walk would produce a
	// mod that loads and does nothing, while reporting success.
	assert.Equal(t, []string{
		"assets/deep/logo.png",
		"mod.toml",
		"scripts/init.lua",
	}, names, "the archive lost a folder")

	script, err := reader.Open("scripts/init.lua")
	require.NoError(t, err, "open scripts/init.lua")

	defer func() { _ = script.Close() }()

	raw, err := io.ReadAll(script)
	require.NoError(t, err)
	assert.Equal(t, "print('hi')\n", string(raw))

	// The folder the downloader produced is staging, not the cache: leaving it
	// would double the disk cost of every folder-shaped mod.
	staging := blob + ".partial"

	_, statErr := os.Stat(staging)
	assert.True(t, os.IsNotExist(statErr), "the staging folder survived: %s", staging)

	// The index has to describe the archive, not the folder it replaced.
	key, err := CacheKey(mod)
	require.NoError(t, err, "cache key")

	entry, err := cache.Index().Get(context.Background(), key)
	require.NoError(t, err, "index entry")

	info, err := os.Stat(blob)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), entry.SizeBytes, "the index records the wrong size")
}

// A second install must not redo the work: the blob is already a zip.
func TestGetOrDownloadServesAFolderModFromCacheOnASecondCall(t *testing.T) {
	m, _ := newRealCache(t)
	d := &folderDownloader{files: map[string]string{"mod.toml": "id = \"tree\"\n"}}
	m = withDownloader(t, m, d)
	mod := downloadMod("tree", "1.0.0", "https://example.test/tree")

	first, err := m.GetOrDownload(context.Background(), mod)
	require.NoError(t, err)

	second, err := m.GetOrDownload(context.Background(), mod)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

// An empty folder has nothing to pack, and saying so beats reporting a packing failure.
func TestGetOrDownloadRejectsAnEmptyFolderDownload(t *testing.T) {
	m, _ := newRealCache(t)
	m = withDownloader(t, m, &folderDownloader{files: map[string]string{}})
	mod := downloadMod("empty", "1.0.0", "https://example.test/empty")

	_, err := m.GetOrDownload(context.Background(), mod)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "empty")
}
