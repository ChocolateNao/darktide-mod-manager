package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCSV(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty"},
		// Separators and whitespace only yield an empty slice.
		{name: "whitespace only", in: "   ", want: []string{}},
		{name: "one", in: "better-ui", want: []string{"better-ui"}},
		{name: "three trimmed", in: " a , b , c ", want: []string{"a", "b", "c"}},
		// An empty segment is dropped: "a,,b" is a typo, and "mod \"\" not found"
		// points at a mod never asked for.
		{name: "empty segments dropped", in: "a,,b,", want: []string{"a", "b"}},
		{name: "no spaces needed", in: "one,two", want: []string{"one", "two"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseCSV(tt.in))
		})
	}
}

func TestShareTableRendersProviders(t *testing.T) {
	table := shareTable([]shareView{
		{ID: "abc", GameID: "darktide", ModCount: 2, Providers: []string{"s3"}},
		{ID: "def", GameID: "sietch", ModCount: 1, Providers: []string{"s3", "dropbox"}},
	})

	require.Equal(t, 2, table.Len(), "one row per share")

	var buf strings.Builder
	require.NoError(t, table.Render(&buf))

	out := buf.String()
	assert.Contains(t, out, "abc")
	assert.Contains(t, out, "darktide")
	assert.Contains(t, out, "s3,dropbox")
}

func TestShareTableViewJSONShape(t *testing.T) {
	// These field names are the interface the frontend and any script consume, so
	// they are pinned.
	views := []shareView{{
		ID:        "abc",
		GameID:    "darktide",
		ModCount:  2,
		Providers: []string{"s3"},
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}}

	raw, err := json.Marshal(views)
	require.NoError(t, err)

	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	assert.Equal(t, map[string]any{
		"id":         "abc",
		"game_id":    "darktide",
		"mod_count":  float64(2),
		"providers":  []any{"s3"},
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
	}, decoded[0])
}

func TestShareLinkViewOmitsMissingExpiry(t *testing.T) {
	// A share with no expiry must omit the key: a reader that renders an empty
	// string shows a blank line.
	raw, err := json.Marshal(shareLinkView{URL: "https://dmm.app/s/s3/x", Provider: "s3"})
	require.NoError(t, err)

	assert.NotContains(t, string(raw), "expires_at")

	expiring := shareLinkView{
		URL:       "https://dmm.app/s/s3/x",
		Provider:  "s3",
		ExpiresAt: "2026-02-01T00:00:00Z",
	}
	raw, err = json.Marshal(expiring)
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"expires_at":"2026-02-01T00:00:00Z"`)
}
