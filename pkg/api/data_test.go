package api

import (
	"context"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteDataRejectsAnEmptySelection(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	err = client.DeleteData(context.Background(), DeleteDataOptions{})
	require.Error(t, err, "DeleteData with nothing selected returned no error")
	assert.True(t, IsAppError(err, ErrCodeInvalidArgument), "DeleteData returned %v, want an invalid-argument error", err)
}

func TestDeleteDataWithModDataNeedsAModService(t *testing.T) {
	client, err := NewClientFromServices(ClientDeps{})
	require.NoError(t, err, "NewClientFromServices")

	err = client.DeleteData(context.Background(), DeleteDataOptions{ModData: true})
	require.Error(t, err, "DeleteData without a mod service returned no error")
	assert.True(t, IsAppError(err, ErrCodeInvalidArgument), "DeleteData returned %v, want an invalid-argument error", err)
}

func TestDeleteDataConfigOnlyRewritesTheDefaults(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.UI.Theme = "dark"

	store := &configStoreStub{cfg: &cfg}
	active := cfg

	client, err := NewClientFromServices(ClientDeps{Config: store, ActiveConfig: &active})
	require.NoError(t, err, "NewClientFromServices")

	require.NoError(t, client.DeleteData(context.Background(), DeleteDataOptions{Config: true}), "DeleteData")

	want := domain.DefaultConfig()
	services.ResolveActiveGame(&want)

	assert.Equal(t, 1, store.saves, "the configuration was saved times, want 1")
	assert.Equal(t, want, *store.last(), "the saved configuration is not the defaults")
	assert.Equal(t, want, active, "the live configuration was not reset")
}
