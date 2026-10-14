package domain

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encodeTestPayload(t *testing.T, p SharePayload) string {
	t.Helper()

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	return base64.URLEncoding.EncodeToString(raw)
}

func TestParseShareURLWebLink(t *testing.T) {
	payload := SharePayload{
		Version:  1,
		Provider: "s3",
		GameID:   "darktide",
		Files:    []SharedFile{{FileID: "a.zip", Path: "a.zip", Size: 10, Hash: "abc"}},
		Mods:     []SharedMod{{Name: "MyMod", Version: "1.0.0", FileIDs: []string{"a.zip"}}},
	}

	encoded := encodeTestPayload(t, payload)
	url := ShareLinkFormat("s3") + encoded

	providerID, gotEncoded, err := ParseShareURL(url)
	require.NoError(t, err)

	assert.Equal(t, "s3", providerID)
	assert.Equal(t, encoded, gotEncoded)
}

func TestParseShareURLDeepLink(t *testing.T) {
	payload := SharePayload{
		Version:  1,
		Provider: "s3",
		GameID:   "darktide",
		Files:    []SharedFile{{FileID: "a.zip", Path: "a.zip"}},
	}

	encoded := encodeTestPayload(t, payload)
	url := DeepLinkFormat("s3") + encoded

	assert.True(t, IsDeepLink(url))
	assert.False(t, IsDeepLink(ShareLinkFormat("s3")+encoded))

	providerID, gotEncoded, err := ParseShareURL(url)
	require.NoError(t, err)

	assert.Equal(t, "s3", providerID)
	assert.Equal(t, encoded, gotEncoded)
}

func TestParseShareURLRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not-a-link",
		"https://example.com/s/s3/abc",
		"dmm://share/",
		"dmm://share/s3/",
		"https://dmm.app/s/s3",
		"dmm://other/s3/abc",
	}

	for _, url := range cases {
		t.Run(url, func(t *testing.T) {
			_, _, err := ParseShareURL(url)
			assert.ErrorIs(t, err, ErrShareInvalid)
		})
	}
}

func TestParseDeepLinkRejectsWebLink(t *testing.T) {
	_, _, err := ParseDeepLink(ShareLinkFormat("s3") + "abc")
	assert.ErrorIs(t, err, ErrShareInvalid)
}

func TestSharePayloadRoundTrip(t *testing.T) {
	expiry := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)

	original := SharePayload{
		Version:  1,
		Provider: "s3",
		GameID:   "darktide",
		Files: []SharedFile{{
			FileID:     "obj-1",
			Path:       "MyMod/shared.zip",
			Size:       2048,
			Hash:       "deadbeef",
			RemoteName: "shared.zip",
		}},
		Mods: []SharedMod{{
			Name:         "MyMod",
			Version:      "1.2.3",
			Author:       "someone",
			Description:  "a mod",
			FileIDs:      []string{"obj-1"},
			Dependencies: []string{"other@1.0.0"},
		}},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		ExpiresAt: &expiry,
	}

	raw, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded SharePayload
	require.NoError(t, json.Unmarshal(raw, &decoded))

	assert.Equal(t, original.Version, decoded.Version)
	assert.Equal(t, original.Provider, decoded.Provider)
	assert.Equal(t, original.GameID, decoded.GameID)
	require.Len(t, decoded.Files, 1)
	assert.Equal(t, original.Files[0], decoded.Files[0])
	require.Len(t, decoded.Mods, 1)
	assert.Equal(t, original.Mods[0], decoded.Mods[0])
	require.NotNil(t, decoded.ExpiresAt)
	assert.True(t, original.ExpiresAt.Equal(*decoded.ExpiresAt))
}

func TestShareLinkIsExpired(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	noExpiry := ShareLink{}
	assert.False(t, noExpiry.IsExpired(base))

	future := base.Add(time.Hour)
	notExpired := ShareLink{ExpiresAt: &future}
	assert.False(t, notExpired.IsExpired(base))

	past := base.Add(-time.Hour)
	expired := ShareLink{ExpiresAt: &past}
	assert.True(t, expired.IsExpired(base))
}

func TestShareLinkHelpersFormatBothForms(t *testing.T) {
	assert.Equal(t, "https://dmm.app/s/s3/", ShareLinkFormat("s3"))
	assert.Equal(t, "dmm://share/s3/", DeepLinkFormat("s3"))
	assert.Equal(t, "dmm://", ShareURLScheme)
}
