package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFileURIPutsTheDriveInThePathNotTheAuthority guards the Windows file: URI
// form: a drive letter is a URI authority, so it must move into the path.
func TestFileURIPutsTheDriveInThePathNotTheAuthority(t *testing.T) {
	got := fileURI(`C:\state\dmm.db`)

	want := "file:///C:/state/dmm.db"
	require.Equal(t, want, got, "fileURI(%q) = %q, want %q", `C:\state\dmm.db`, got, want)

	require.NotContains(t, got, "%5C", "fileURI left backslashes unencoded: %q", got)
}

func TestFileURIRendersAPosixPathWithAnEmptyHost(t *testing.T) {
	got := fileURI("/home/u/.local/state/dmm.db")

	want := "file:///home/u/.local/state/dmm.db"

	require.Equal(t, want, got, "fileURI = %q, want %q", got, want)
}
