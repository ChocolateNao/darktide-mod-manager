package cloud

import (
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/dropbox"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/gdrive"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/onedrive"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/s3"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactoryIDsListsEveryRegisteredProvider(t *testing.T) {
	// The picker renders this list, so a provider missing here is never offered.
	assert.Equal(t, []string{"dropbox", "gdrive", "onedrive", "s3"}, FactoryIDs())
}

func TestBuildReturnsEachProvider(t *testing.T) {
	configs := map[string]map[string]string{
		"s3": {
			"endpoint":   "s3.example.com",
			"access_key": "AKIAEXAMPLE",
			"secret_key": "not-a-real-secret",
			"bucket":     "shares",
		},
		"gdrive": {
			"client_id":     "client-id",
			"client_secret": "not-a-real-secret",
			"folder_id":     "folder-id",
		},
		"dropbox": {
			"client_id":     "client-id",
			"client_secret": "not-a-real-secret",
		},
		"onedrive": {
			"client_id":     "client-id",
			"client_secret": "not-a-real-secret",
		},
	}

	for id, opts := range configs {
		t.Run(id, func(t *testing.T) {
			provider, err := Build(id, opts)
			require.NoError(t, err)

			assert.Equal(t, id, provider.ID())
			assert.NotEmpty(t, provider.Name())
			assert.NotEmpty(t, provider.Description())
			assert.True(t, provider.SupportsAuth(), "every provider authenticates")
		})
	}
}

func TestBuildRejectsAnUnknownProvider(t *testing.T) {
	_, err := Build("nonexistent", nil)

	assert.ErrorIs(t, err, domain.ErrCloudProviderNotFound)
}

func TestBuildRefusesAProviderMissingItsRequiredFields(t *testing.T) {
	// A provider built from an incomplete block would fail on its first call
	// not at wiring time, which is the wrong end of a share to find out.
	for _, id := range []string{s3.ProviderID, gdrive.ProviderID, dropbox.ProviderID, onedrive.ProviderID} {
		t.Run(id, func(t *testing.T) {
			_, err := Build(id, map[string]string{})
			assert.Error(t, err, "an empty settings block must not build")
		})
	}
}

func TestBuildRejectsAnIncompleteS3Settings(t *testing.T) {
	// The bucket is what an upload is aimed at, so credentials alone leave
	// nowhere to put anything.
	_, err := Build(s3.ProviderID, map[string]string{
		"endpoint":   "s3.example.com",
		"access_key": "AKIAEXAMPLE",
		"secret_key": "not-a-real-secret",
	})

	assert.Error(t, err)
}

func TestProviderErrorsNeverCarryCredentials(t *testing.T) {
	// A refusal has to be actionable without quoting the secret back, since the
	// error ends up in a log line and then in a support ticket.
	secret := "super-secret-value"

	for _, opts := range []map[string]string{
		{"endpoint": "s3.example.com"},
		{"client_id": "id", "client_secret": secret, "folder_id": ""},
		{"client_id": "id", "client_secret": secret},
	} {
		for id := range opts {
			_, err := Build(id, opts)
			if err == nil {
				continue
			}

			assert.NotContains(t, err.Error(), secret,
				"a build error must not quote the credential it was given")
		}
	}
}

func TestProviderIDsAreStable(t *testing.T) {
	// The id is the key in the config block and in a share's payload, so it
	// cannot drift with a rename.
	assert.Equal(t, "s3", s3.ProviderID)
	assert.Equal(t, "gdrive", gdrive.ProviderID)
	assert.Equal(t, "dropbox", dropbox.ProviderID)
	assert.Equal(t, "onedrive", onedrive.ProviderID)
}

func TestSortStrings(t *testing.T) {
	items := []string{"s3", "gdrive", "onedrive", "dropbox"}

	got := FactoryIDs()
	require.NotEmpty(t, got)

	for i := 1; i < len(got); i++ {
		assert.LessOrEqual(t, strings.Compare(got[i-1], got[i]), 0,
			"factory ids must come back sorted for a picker")
	}

	assert.ElementsMatch(t, items, got)
}
