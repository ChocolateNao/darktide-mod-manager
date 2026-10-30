package local

import (
	"os"
	"path/filepath"
	"testing"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeMod lays out a mod folder the way the manifest reader expects.
func writeMod(t *testing.T, root, id, name, version string) string {
	t.Helper()

	dir := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(dir, 0o750))

	manifest := "[mod]\nid = \"" + id + "\"\nname = \"" + name + "\"\nversion = \"" + version + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mod.toml"), []byte(manifest), 0o600))

	return dir
}

func TestInspectPathRecognisesAModFolderOutsideTheConfiguredRoots(t *testing.T) {
	t.Parallel()

	// The whole point of the drop overlay: the folder the user dragged is not in
	// any path the manager was configured with, and it still has to resolve.
	elsewhere := t.TempDir()
	modDir := writeMod(t, elsewhere, "sample", "Sample Mod", "1.2.0")

	source := New([]string{filepath.Join(elsewhere, "configured-but-empty")})

	mod, recognised, err := source.InspectPath(t.Context(), modDir)
	require.NoError(t, err)
	require.True(t, recognised)
	require.NotNil(t, mod)
	assert.Equal(t, "sample", mod.ID)
	assert.Equal(t, "Sample Mod", mod.Name)
	assert.Equal(t, "1.2.0", mod.Version)
	assert.Equal(t, SourceID, mod.SourceID)
}

func TestInspectPathAcceptsASingleModFolderInsideTheDroppedFolder(t *testing.T) {
	t.Parallel()

	container := t.TempDir()
	writeMod(t, container, "only-mod", "Only Mod", "0.1.0")

	source := New(nil)

	mod, recognised, err := source.InspectPath(t.Context(), container)
	require.NoError(t, err)
	require.True(t, recognised)
	require.NotNil(t, mod)
	assert.Equal(t, "only-mod", mod.ID)
}

func TestInspectPathAcceptsAnArchiveInsideTheDroppedFolder(t *testing.T) {
	t.Parallel()

	container := t.TempDir()
	archive := filepath.Join(container, "Cool Mod 2.3.1.zip")
	require.NoError(t, os.WriteFile(archive, []byte("not really a zip"), 0o600))

	source := New(nil)

	mod, recognised, err := source.InspectPath(t.Context(), container)
	require.NoError(t, err)
	require.True(t, recognised)
	require.NotNil(t, mod)
	assert.Equal(t, "coolmod", mod.ID)
	assert.Equal(t, archive, mod.LocalPath)
}

func TestInspectPathAcceptsADroppedFolderWithoutAManifest(t *testing.T) {
	t.Parallel()

	modDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(modDir, "mods"), 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(modDir, "mods", "sample.mod"),
		[]byte("payload"),
		0o600,
	))

	source := New(nil)

	mod, recognised, err := source.InspectPath(t.Context(), modDir)
	require.NoError(t, err)
	require.True(t, recognised)
	require.NotNil(t, mod)
	assert.Equal(t, SanitizeID(filepath.Base(modDir)), mod.ID)
	assert.Equal(t, modDir, mod.LocalPath)
}

func TestInspectPathRefusesAFolderWithSeveralArchives(t *testing.T) {
	t.Parallel()

	container := t.TempDir()
	for _, name := range []string{"one.zip", "two.zip"} {
		require.NoError(t, os.WriteFile(
			filepath.Join(container, name),
			[]byte("payload"),
			0o600,
		))
	}

	source := New(nil)

	mod, recognised, err := source.InspectPath(t.Context(), container)
	require.NoError(t, err)
	assert.False(t, recognised, "two archives is not a single answer")
	assert.Nil(t, mod)
}

func TestGetModReturnsAnInspectedDroppedPath(t *testing.T) {
	t.Parallel()

	modDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(modDir, "data.bin"),
		[]byte("payload"),
		0o600,
	))

	source := New(nil)

	inspected, recognised, err := source.InspectPath(t.Context(), modDir)
	require.NoError(t, err)
	require.True(t, recognised)
	require.NotNil(t, inspected)

	got, err := source.GetMod(t.Context(), inspected.ID)
	require.NoError(t, err)
	assert.Equal(t, modDir, got.LocalPath)
}

func TestInspectPathRefusesAnAmbiguousFolderRatherThanGuessing(t *testing.T) {
	t.Parallel()

	container := t.TempDir()
	writeMod(t, container, "one", "One", "1.0.0")
	writeMod(t, container, "two", "Two", "1.0.0")

	source := New(nil)

	mod, recognised, err := source.InspectPath(t.Context(), container)
	require.NoError(t, err)
	assert.False(t, recognised, "two candidate mods is not a single answer")
	assert.Nil(t, mod)
}

func TestInspectPathReadsAnArchiveWithoutOpeningIt(t *testing.T) {
	t.Parallel()

	archive := filepath.Join(t.TempDir(), "Cool Mod 2.3.1.zip")
	require.NoError(t, os.WriteFile(archive, []byte("not really a zip"), 0o600))

	source := New(nil)

	mod, recognised, err := source.InspectPath(t.Context(), archive)
	require.NoError(t, err)
	require.True(t, recognised)
	require.NotNil(t, mod)
	assert.Equal(t, "coolmod", mod.ID, "the version suffix must not become part of the id")
}

func TestInspectPathDeclinesWhatItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := New(nil)

	for _, name := range []string{"notes.txt", "photo.png"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(dir, name)
			require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

			mod, recognised, err := source.InspectPath(t.Context(), path)
			require.NoError(t, err, "an unrecognised path is an answer, not a failure")
			assert.False(t, recognised)
			assert.Nil(t, mod)
		})
	}

	t.Run("empty folder", func(t *testing.T) {
		t.Parallel()

		mod, recognised, err := source.InspectPath(t.Context(), t.TempDir())
		require.NoError(t, err)
		assert.False(t, recognised)
		assert.Nil(t, mod)
	})
}

func TestInspectPathReportsAMissingPathAsNotFound(t *testing.T) {
	t.Parallel()

	source := New(nil)

	_, _, err := source.InspectPath(t.Context(), filepath.Join(t.TempDir(), "gone"))
	require.Error(t, err)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeNotFound,
	), "a path that is not there is a not-found, not a silent decline")
}
