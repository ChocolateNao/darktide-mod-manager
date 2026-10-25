package storage

import (
	"context"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newShareStore(t *testing.T) *ShareStore {
	t.Helper()

	// The store is a view over a shared handle, so there is no Close here:
	// openMemory's cleanup owns the database.
	return NewShareStore(openMemory(t))
}

func shareRecord(id, gameID string) *domain.ShareRecord {
	return &domain.ShareRecord{
		ID:     id,
		GameID: gameID,
		Mods: []domain.Mod{{
			ID:      id + "-mod",
			Name:    "Shared Mod",
			Version: "1.0.0",
			GameID:  gameID,
		}},
		Links: []domain.ShareLink{{
			URL:        "https://dmm.app/s/s3/" + id,
			ProviderID: "s3",
		}},
		CreatedAt: fixedTime,
		UpdatedAt: fixedTime,
	}
}

func TestShareStoreSaveAndGet(t *testing.T) {
	store := newShareStore(t)
	ctx := context.Background()

	record := shareRecord("share-1", "darktide")

	require.NoError(t, store.Save(ctx, record))

	got, err := store.Get(ctx, "share-1")
	require.NoError(t, err)

	assert.Equal(t, "share-1", got.ID)
	assert.Equal(t, "darktide", got.GameID)
	require.Len(t, got.Mods, 1)
	assert.Equal(t, "Shared Mod", got.Mods[0].Name)
	assert.Equal(t, gameIDOf(got), "darktide")
	require.Len(t, got.Links, 1)
	assert.Equal(t, "https://dmm.app/s/s3/share-1", got.Links[0].URL)
}

// gameIDOf reads the game id off the persisted record.
func gameIDOf(r *domain.ShareRecord) string { return r.GameID }

func TestShareStoreSaveIsAnUpsert(t *testing.T) {
	store := newShareStore(t)
	ctx := context.Background()

	record := shareRecord("share-1", "darktide")
	require.NoError(t, store.Save(ctx, record))

	record.UpdatedAt = fixedTime.Add(time.Hour)
	record.Mods = append(record.Mods, domain.Mod{ID: "extra", Name: "Extra"})
	require.NoError(t, store.Save(ctx, record))

	got, err := store.Get(ctx, "share-1")
	require.NoError(t, err)

	assert.Len(t, got.Mods, 2)
	assert.WithinDuration(t, fixedTime.Add(time.Hour), got.UpdatedAt, time.Millisecond)
}

func TestShareStoreGetUnknownIsNotFound(t *testing.T) {
	store := newShareStore(t)

	_, err := store.Get(context.Background(), "nope")
	assert.ErrorIs(t, err, domain.ErrShareNotFound)
}

func TestShareStoreList(t *testing.T) {
	store := newShareStore(t)
	ctx := context.Background()

	require.NoError(t, store.Save(ctx, shareRecord("s1", "darktide")))
	require.NoError(t, store.Save(ctx, shareRecord("s2", "darktide")))
	require.NoError(t, store.Save(ctx, shareRecord("s3", "other")))

	all, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

func TestShareStoreListByGame(t *testing.T) {
	store := newShareStore(t)
	ctx := context.Background()

	require.NoError(t, store.Save(ctx, shareRecord("s1", "darktide")))
	require.NoError(t, store.Save(ctx, shareRecord("s2", "darktide")))
	require.NoError(t, store.Save(ctx, shareRecord("s3", "other")))

	darktide, err := store.ListByGame(ctx, "darktide")
	require.NoError(t, err)
	require.Len(t, darktide, 2)

	for _, r := range darktide {
		assert.Equal(t, "darktide", r.GameID, "share %s leaked another game", r.ID)
	}

	other, err := store.ListByGame(ctx, "other")
	require.NoError(t, err)
	assert.Len(t, other, 1)
}

func TestShareStoreDelete(t *testing.T) {
	store := newShareStore(t)
	ctx := context.Background()

	require.NoError(t, store.Save(ctx, shareRecord("s1", "darktide")))

	require.NoError(t, store.Delete(ctx, "s1"))

	_, err := store.Get(ctx, "s1")
	assert.ErrorIs(t, err, domain.ErrShareNotFound)
}

func TestShareStoreGetByLink(t *testing.T) {
	store := newShareStore(t)
	ctx := context.Background()

	require.NoError(t, store.Save(ctx, shareRecord("s1", "darktide")))

	record, err := store.GetByLink(ctx, domain.ShareLink{
		URL:        "https://dmm.app/s/s3/s1",
		ProviderID: "s3",
	})
	require.NoError(t, err)

	assert.Equal(t, "s1", record.ID)

	_, err = store.GetByLink(ctx, domain.ShareLink{
		URL:        "https://dmm.app/s/s3/unknown",
		ProviderID: "s3",
	})
	assert.ErrorIs(t, err, domain.ErrShareNotFound)
}

func TestShareStoreSchemaHasGameIDColumn(t *testing.T) {
	// The column is in the first schema, so there is no later migration: a store
	// over an older one fails here, not silently losing the game id.
	db := openMemory(t)

	rows, err := db.Query(`SELECT game_id FROM shares LIMIT 1`)
	require.NoError(t, err, "shares.game_id must exist in the schema")

	require.NoError(t, rows.Close())
}

func TestShareStoreErrorsAreTyped(t *testing.T) {
	store := newShareStore(t)

	_, err := store.Get(context.Background(), "missing")
	require.Error(t, err)

	assert.ErrorIs(t, err, domain.ErrShareNotFound)

	_, err = store.GetByLink(context.Background(), domain.ShareLink{
		URL:        "https://dmm.app/s/s3/none",
		ProviderID: "s3",
	})
	assert.ErrorIs(t, err, domain.ErrShareNotFound)
}
