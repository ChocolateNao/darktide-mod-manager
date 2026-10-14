package dropbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObjectPathKeepsTheArchiveName(t *testing.T) {
	// The id the payload carries is the path the archive was uploaded to, so
	// the same string has to name it both ways.
	p, err := New(Config{ClientID: "id"})
	require.NoError(t, err)

	assert.Equal(t, "/DarktideModManager/mod-a.zip", p.objectPath("mod-a.zip"))
}

func TestObjectPathDefaultsTheFolder(t *testing.T) {
	p, err := New(Config{ClientID: "id"})
	require.NoError(t, err)

	assert.Equal(t, "/DarktideModManager", p.folder)
}

func TestObjectPathAcceptsAConfiguredFolder(t *testing.T) {
	p, err := New(Config{ClientID: "id", Folder: "/shares/mods"})
	require.NoError(t, err)

	assert.Equal(t, "/shares/mods", p.folder)
	assert.Equal(t, "/shares/mods/mod-a.zip", p.objectPath("mod-a.zip"))
}

func TestLastSlash(t *testing.T) {
	assert.Equal(t, 2, lastSlash("ab/cd"))
	assert.Equal(t, -1, lastSlash("nope"))
}

func TestProviderRequiresAClientID(t *testing.T) {
	_, err := New(Config{})
	assert.Error(t, err)
}
