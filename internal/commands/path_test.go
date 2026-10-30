package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsableDirAcceptsNothingTheRuntimeWouldReject(t *testing.T) {
	root := t.TempDir()

	folder := filepath.Join(root, "mods")
	require.NoError(t, os.MkdirAll(folder, 0o750))

	file := filepath.Join(folder, "mod.toml")
	require.NoError(t, os.WriteFile(file, []byte("id = 1\n"), 0o600))

	tests := []struct {
		name  string
		start string
		want  string
	}{
		{name: "an existing folder is kept", start: folder, want: folder},
		{name: "a file falls back to its folder", start: file, want: folder},
		{
			name:  "a missing path falls back to the folder above it",
			start: filepath.Join(root, "gone"),
			want:  root,
		},
		{
			name:  "a path with no live ancestor falls back to the platform default",
			start: filepath.Join(root, "gone", "deeper", "still"),
			want:  "",
		},
		{name: "blank means the platform default", start: "", want: ""},
		{name: "padded blank is still blank", start: "   ", want: ""},
		{
			name:  "the reset sentinel never becomes a dialog directory",
			start: DefaultPathRequest,
			want:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, usableDir(test.start))
		})
	}
}

func TestChooseDirectoryReportsAResetWithoutADialog(t *testing.T) {
	p := newPathCommands(newBase(Deps{}))

	path, cleared, err := p.ChooseDirectory(DefaultPathRequest)

	require.NoError(t, err)
	assert.True(t, cleared, "cleared = false, want the reset reported")
	assert.Empty(t, path, "path = %q, want no path on a reset", path)
}
