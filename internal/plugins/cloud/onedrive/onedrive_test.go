package onedrive

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObjectPathIsUnderTheDriveRoot(t *testing.T) {
	p, err := New(Config{ClientID: "id"})
	require.NoError(t, err)

	// The id the payload carries is the path under the drive root, and the
	// root-qualified form is what both the upload and the metadata read use.
	assert.Equal(t, "/DarktideModManager", p.folder)
	assert.Contains(t, p.objectPath("mod-a.zip"), "mod-a.zip")
}

func TestProviderRequiresAClientID(t *testing.T) {
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestConsentURLAsksForFileScopeOnly(t *testing.T) {
	// Files.ReadWrite reaches only what this app puts there, which is exactly
	// the set a share touches. Full drive scope would be a permission the
	// feature does not need.
	p, err := New(Config{ClientID: "id"})
	require.NoError(t, err)

	got := p.ConsentURL("http://127.0.0.1:8080/cb", "state")

	assert.Contains(t, got, "Files.ReadWrite")
	assert.NotContains(t, got, "Files.ReadWrite.All")
	assert.Contains(t, got, "offline_access")
}
