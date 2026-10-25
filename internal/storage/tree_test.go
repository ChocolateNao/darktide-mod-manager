package storage

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTreeFile creates a file, and any folders above it, under root.
func writeTreeFile(t *testing.T, root, rel, body string) string {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

// A Darktide mod is normally a folder of folders. A walk that stopped at the top
// level would return scripts/ and assets/ as nothing at all, which is how a mod
// ends up "installed" and quietly doing nothing.
func TestCollectTreeDescendsIntoSubfolders(t *testing.T) {
	root := t.TempDir()

	writeTreeFile(t, root, "mod.toml", "id = \"x\"")
	writeTreeFile(t, root, "scripts/init.lua", "return 1")
	writeTreeFile(t, root, "scripts/deep/nested/helper.lua", "return 2")
	writeTreeFile(t, root, "assets/icon.png", "not really a png")

	entries, err := CollectTree(root)
	require.NoError(t, err)

	var paths []string
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}

	assert.Equal(t, []string{
		"assets/icon.png",
		"mod.toml",
		"scripts/deep/nested/helper.lua",
		"scripts/init.lua",
	}, paths)
}

func TestCollectTreeReportsSizesAndSources(t *testing.T) {
	root := t.TempDir()
	path := writeTreeFile(t, root, "mod.toml", "id = \"x\"")

	entries, err := CollectTree(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	assert.Equal(t, int64(len("id = \"x\"")), entries[0].Size)
	assert.Equal(t, path, entries[0].Source)
	assert.Equal(t, fsFileMode(t, path), entries[0].Mode.Perm())
}

// The order of the entries decides the bytes of the archive and therefore its
// hash. Two identical trees packed on two machines have to produce the same
// archive, or a shared mod would fail verification on the far side for no reason
// a user could understand.
func TestCollectTreeIsOrdered(t *testing.T) {
	root := t.TempDir()

	for _, rel := range []string{"zebra.lua", "alpha.lua", "scripts/middle.lua", "scripts/aaa.lua"} {
		writeTreeFile(t, root, rel, "x")
	}

	first, err := CollectTree(root)
	require.NoError(t, err)

	for range 5 {
		again, err := CollectTree(root)
		require.NoError(t, err)
		assert.Equal(t, first, again, "the walk is not stable")
	}

	assert.Equal(t, "alpha.lua", first[0].Path, "entries come back sorted by path")
}

// A link inside a mod folder is expected; a link that points out of it would let
// a "local mod" read arbitrary files and index them as mod content.
func TestCollectTreeRefusesALinkOutOfTheFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows needs elevation for symlinks")
	}

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("private"), 0o600))

	require.NoError(t, os.Symlink(outside, filepath.Join(root, "leak.txt")))

	_, err := CollectTree(root)
	require.Error(t, err)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod), "got %v", err)
}

// A link that stays inside the folder is fine, and its contents are what the mod
// actually ships.
func TestCollectTreeFollowsALinkInsideTheFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows needs elevation for symlinks")
	}

	root := t.TempDir()
	writeTreeFile(t, root, "scripts/real.lua", "return 1")
	require.NoError(t, os.Symlink(filepath.Join(root, "scripts", "real.lua"),
		filepath.Join(root, "linked.lua")))

	entries, err := CollectTree(root)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, "linked.lua", entries[0].Path)
	assert.Equal(t, "scripts/real.lua", entries[1].Path)
}

// A socket or a FIFO cannot appear in a mod archive, and following one would
// block forever.
func TestCollectTreeSkipsEntriesThatAreNotRegularFiles(t *testing.T) {
	root := t.TempDir()
	writeTreeFile(t, root, "mod.toml", "id = \"x\"")

	if runtime.GOOS != "windows" {
		fifo := filepath.Join(root, "pipe")
		require.NoError(t, mkfifo(fifo))
	}

	entries, err := CollectTree(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "mod.toml", entries[0].Path)
}

func TestCollectTreeRefusesANonDirectoryRoot(t *testing.T) {
	root := t.TempDir()
	path := writeTreeFile(t, root, "mod.toml", "id = \"x\"")

	_, err := CollectTree(path)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument), "got %v", err)

	// A missing folder is an IO problem, not a bad argument, and the two
	// lead the user to different fixes.
	_, err = CollectTree(filepath.Join(root, "nope"))
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeConfigIO), "got %v", err)

	_, err = CollectTree("")
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument), "got %v", err)
}

func TestCollectTreeOnAnEmptyFolder(t *testing.T) {
	entries, err := CollectTree(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// PackDirectory is how a folder-shaped download becomes something the installer
// can open: the extractor reads zips and nothing else.
func TestPackDirectoryProducesAReadableArchive(t *testing.T) {
	root := t.TempDir()

	writeTreeFile(t, root, "mod.toml", "id = \"x\"")
	writeTreeFile(t, root, "scripts/init.lua", "return 1")

	dest := filepath.Join(t.TempDir(), "mod.zip")

	result, err := PackDirectory(context.Background(), root, dest)
	require.NoError(t, err)
	require.Len(t, result.Files, 2)
	assert.Equal(t, "mod.toml", result.Files[0].Name, "the archive keeps the walk's order")

	reader, err := zip.OpenReader(dest)
	require.NoError(t, err)

	defer func() { assert.NoError(t, reader.Close()) }()

	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}

	assert.Equal(t, []string{"mod.toml", "scripts/init.lua"}, names)

	for _, file := range reader.File {
		if file.Name != "scripts/init.lua" {
			continue
		}

		handle, err := file.Open()
		require.NoError(t, err)

		body, err := io.ReadAll(handle)
		require.NoError(t, handle.Close())
		require.NoError(t, err)
		assert.Equal(t, "return 1", string(body))
	}
}

func TestPackDirectoryRefusesAnEmptyTree(t *testing.T) {
	_, err := PackDirectory(
		context.Background(),
		t.TempDir(),
		filepath.Join(t.TempDir(), "mod.zip"),
	)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument), "got %v", err)
}

func fsFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)

	return info.Mode().Perm()
}

// mkfifo wraps syscall.Mkfifo so the windows branch can skip it cleanly.
func mkfifo(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
