package storage

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackArchiveWritesEntriesAndInventory(t *testing.T) {
	dir := t.TempDir()

	a := writePackFile(t, dir, "one.lua", "contents one")
	b := writePackFile(t, dir, "two.lua", "contents two")

	dest := filepath.Join(t.TempDir(), "mod.zip")

	result, err := PackArchive(context.Background(), dest, []PackEntry{
		{Source: a, Name: "one.lua"},
		{Source: b, Name: "nested/two.lua"},
	})
	require.NoError(t, err)

	require.Len(t, result.Files, 2)
	assert.Equal(t, int64(24), result.TotalSize, "total is the uncompressed payload")
	assert.Equal(t, "one.lua", result.Files[0].Name)
	assert.Equal(t, "nested/two.lua", result.Files[1].Name)
	assert.NotEmpty(t, result.Files[0].Checksum, "each entry gets a checksum")

	// The archive has to be a zip the extractor will accept, and the entry
	// names have to survive the round trip — they become the mod's file paths.
	zr, err := zip.OpenReader(dest)
	require.NoError(t, err)

	defer func() { _ = zr.Close() }()

	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
		assert.Equal(t, os.FileMode(0o644), f.Mode(), "entry modes are fixed, not copied")
	}

	assert.Equal(t, []string{"one.lua", "nested/two.lua"}, names)
}

func TestPackArchiveRoundTripsThroughTheExtractor(t *testing.T) {
	// The two halves of a share: what the packer writes is what the recipient's
	// extractor reads back, with the same bytes and the same hashes.
	src := t.TempDir()
	content := "return { name = 'mod' }"

	writePackFile(t, src, "mod.lua", content)

	dest := filepath.Join(t.TempDir(), "mod.zip")

	result, err := PackArchive(context.Background(), dest, []PackEntry{
		{Source: filepath.Join(src, "mod.lua"), Name: "mod.lua"},
	})
	require.NoError(t, err)

	destDir := t.TempDir()

	extracted, err := NewArchiveExtractor().Extract(context.Background(), dest, destDir)
	require.NoError(t, err)

	require.Len(t, extracted.Files, 1)
	assert.Equal(t, "mod.lua", extracted.Files[0].RelPath)
	assert.Equal(t, result.Files[0].Checksum, extracted.Files[0].Checksum,
		"the checksum the packer reported must be the one the extractor recomputes")

	got, err := os.ReadFile(filepath.Join(destDir, "mod.lua"))
	require.NoError(t, err)
	assert.Equal(t, content, string(got))
}

func TestPackArchiveRejectsEmptyDestination(t *testing.T) {
	_, err := PackArchive(context.Background(), "", []PackEntry{{Source: "x", Name: "x"}})

	assert.Equal(t, "INVALID_ARGUMENT", codeOf(err))
}

func TestPackArchiveRejectsNoEntries(t *testing.T) {
	_, err := PackArchive(context.Background(), filepath.Join(t.TempDir(), "m.zip"), nil)

	assert.Equal(t, "INVALID_ARGUMENT", codeOf(err))
}

func TestPackArchiveRejectsAnUnnamedEntry(t *testing.T) {
	dir := t.TempDir()

	src := writePackFile(t, dir, "one.lua", "contents")

	_, err := PackArchive(context.Background(), filepath.Join(t.TempDir(), "m.zip"), []PackEntry{
		{Source: src, Name: ""},
	})

	assert.Equal(t, "INVALID_ARGUMENT", codeOf(err))
}

func TestPackArchiveLeavesNothingBehindOnFailure(t *testing.T) {
	dir := t.TempDir()

	good := writePackFile(t, dir, "one.lua", "contents")
	missing := filepath.Join(dir, "does-not-exist.lua")

	dest := filepath.Join(t.TempDir(), "mod.zip")

	_, err := PackArchive(context.Background(), dest, []PackEntry{
		{Source: good, Name: "one.lua"},
		{Source: missing, Name: "missing.lua"},
	})
	require.Error(t, err)

	// A partial archive is indistinguishable from a finished one, so neither it nor
	// the temp file may survive the failure.
	assert.NoFileExists(t, dest)

	entries, err := os.ReadDir(filepath.Dir(dest))
	require.NoError(t, err)
	assert.Empty(t, entries, "no staging file may survive a failed pack")
}

func TestPackArchiveHonoursCancellation(t *testing.T) {
	dir := t.TempDir()

	src := writePackFile(t, dir, "one.lua", "contents")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := PackArchive(ctx, filepath.Join(t.TempDir(), "m.zip"), []PackEntry{
		{Source: src, Name: "one.lua"},
	})

	assert.ErrorIs(t, err, context.Canceled)
}

func writePackFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}

	return string(appErrors.CodeOf(err))
}
