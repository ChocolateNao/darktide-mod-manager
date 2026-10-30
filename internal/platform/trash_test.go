package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMoveToTrashMovesFolderAndWritesInfo(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "mod")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "file.txt"), []byte("x"), 0o644))

	filesDir := filepath.Join(root, "Trash", "files")
	infoDir := filepath.Join(root, "Trash", "info")
	when := time.Date(2026, 10, 23, 12, 30, 0, 0, time.UTC)

	require.NoError(t, moveToTrash(src, filesDir, infoDir, when))

	_, err := os.Stat(src)
	assert.True(t, os.IsNotExist(err), "source must be gone")

	_, err = os.Stat(filepath.Join(filesDir, "mod", "sub", "file.txt"))
	require.NoError(t, err, "folder must be in the trash")

	info, err := os.ReadFile(filepath.Join(infoDir, "mod.trashinfo"))
	require.NoError(t, err)
	assert.Contains(t, string(info), "[Trash Info]")
	assert.Contains(t, string(info), "Path=")
	assert.Contains(t, string(info), "DeletionDate=2026-10-23T12:30:00")
}

func TestMoveToTrashKeepsBothCopiesWithTheSameName(t *testing.T) {
	root := t.TempDir()
	filesDir := filepath.Join(root, "files")

	for _, name := range []string{"first", "second"} {
		src := filepath.Join(root, name, "mod")
		require.NoError(t, os.MkdirAll(src, 0o755))
		require.NoError(t, moveToTrash(src, filesDir, "", time.Now()))
	}

	entries, err := os.ReadDir(filesDir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "the second move must not overwrite the first")
}

func TestFreeTrashNameAvoidsExistingEntries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mod"), []byte("x"), 0o644))

	name, _ := freeTrashName(dir, "mod")
	assert.True(t, strings.HasPrefix(name, "mod."), "name = %q, want a suffix", name)
}

func TestWindowsTrashScriptPicksTheRightMethodAndEscapesQuotes(t *testing.T) {
	t.Parallel()

	dirScript := windowsTrashScript(`C:\Mods\Bob's mod`, true)
	assert.Contains(t, dirScript, "DeleteDirectory")
	assert.Contains(t, dirScript, `'C:\Mods\Bob''s mod'`)
	assert.Contains(t, dirScript, "'SendToRecycleBin'")

	fileScript := windowsTrashScript(`C:\Mods\mod.mod`, false)
	assert.Contains(t, fileScript, "DeleteFile")
	assert.NotContains(t, fileScript, "DeleteDirectory")
}
