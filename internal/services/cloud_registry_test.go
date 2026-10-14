package services

import (
	"sync"
	"testing"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudRegistryRegisterAndGet(t *testing.T) {
	registry := NewCloudRegistry()
	provider := &fakeProvider{id: "s3"}

	require.NoError(t, registry.Register(provider))

	got, err := registry.Get("s3")
	require.NoError(t, err)

	assert.Equal(t, "s3", got.ID())
}

func TestCloudRegistryGetUnknown(t *testing.T) {
	registry := NewCloudRegistry()

	_, err := registry.Get("nope")
	require.Error(t, err)

	assert.Equal(t, appErrors.ErrCodeCloudProviderNotFound, appErrors.CodeOf(err))
}

func TestCloudRegistryRegisterOverwrites(t *testing.T) {
	// A provider registered twice replaces the first: a re-authenticated
	// provider must not leave the stale credentials behind.
	registry := NewCloudRegistry()

	first := &fakeProvider{id: "s3", needsAuth: true}
	second := &fakeProvider{id: "s3", needsAuth: false}

	require.NoError(t, registry.Register(first))
	require.NoError(t, registry.Register(second))

	got, err := registry.Get("s3")
	require.NoError(t, err)

	assert.False(t, got.SupportsAuth(), "the second registration must win")
}

func TestCloudRegistryRegisterNil(t *testing.T) {
	registry := NewCloudRegistry()

	assert.Error(t, registry.Register(nil))
}

func TestCloudRegistryList(t *testing.T) {
	registry := NewCloudRegistry()

	require.NoError(t, registry.Register(&fakeProvider{id: "s3"}))
	require.NoError(t, registry.Register(&fakeProvider{id: "dropbox"}))

	listed := registry.List()
	require.Len(t, listed, 2)

	ids := make([]string, 0, len(listed))
	for _, p := range listed {
		ids = append(ids, p.ID())
	}

	assert.ElementsMatch(t, []string{"s3", "dropbox"}, ids)
}

func TestCloudRegistryNilReceiverIsSafe(t *testing.T) {
	var registry *CloudRegistry

	assert.Error(t, registry.Register(&fakeProvider{id: "s3"}))

	_, err := registry.Get("s3")
	assert.Error(t, err)

	assert.Empty(t, registry.List())
}

func TestCloudRegistryConcurrentAccess(t *testing.T) {
	// The registry is read by every share operation and written when a
	// provider authenticates, so it has to be safe for concurrent readers and
	// one writer.
	registry := NewCloudRegistry()
	require.NoError(t, registry.Register(&fakeProvider{id: "s3"}))

	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for j := 0; j < 50; j++ {
				_, _ = registry.Get("s3")
				registry.List()
			}
		}()
	}

	wg.Add(1)

	go func() {
		defer wg.Done()

		for j := 0; j < 50; j++ {
			_ = registry.Register(&fakeProvider{id: "s3"})
		}
	}()

	wg.Wait()
}

func TestShareStoreInterfaceIsSatisfiedByMemStore(t *testing.T) {
	// The fake must be a drop-in for the real store, or the orchestrator tests
	// would pass against an interface it does not actually exercise.
	var store ShareStore = newMemShareStore()

	require.NotNil(t, store)
}
