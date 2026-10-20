package api

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forgettableSource is a source that can drop what it was given, which is what
// makes "forget" a two-part operation: the settings file and the live source.
type forgettableSource struct {
	stubSource
	mu      sync.Mutex
	forgets int
}

func (s *forgettableSource) ForgetCredentials() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.forgets++
}

func (s *forgettableSource) forgetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.forgets
}

// credentialClient wires a client to an in-memory configuration store and a
// registry holding one source.
func credentialClient(
	t *testing.T,
	source domain.ModSource,
	cfg *domain.Config,
) (*Client, *configStoreStub, *SourceRegistryForTest) {
	t.Helper()

	store := &configStoreStub{cfg: cfg}

	registry := services.NewSourceRegistry()
	if source != nil {
		require.NoError(t, registry.Register(source), "register %s", source.ID())
	}

	client, err := NewClientFromServices(ClientDeps{Config: store, Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	t.Cleanup(func() { _ = client.Close() })

	return client, store, &SourceRegistryForTest{registry}
}

// SourceRegistryForTest exists only so a test can hold on to the registry it
// built; it carries no behaviour of its own.
type SourceRegistryForTest struct{ registry *services.SourceRegistry }

func TestAuthenticateSourceStoresWhatTheSourceAccepted(t *testing.T) {
	// The registry's sign-in record dies with the process, so the key on disk is what
	// the source rebuilds itself from.
	source := &stubSource{id: "nexus"}
	cfg := domain.DefaultConfig()
	client, store, _ := credentialClient(t, source, &cfg)

	require.NoError(t, client.AuthenticateSource(
		context.Background(),
		"nexus",
		map[string]string{"api_key": "abc"},
	), "AuthenticateSource")

	block, ok := store.last().SourceConfigFor("nexus")
	require.True(t, ok, "the accepted key was not stored")

	assert.Equal(t, "abc", block.APIKey, "the stored key, want the one the source accepted")

	assert.True(t, block.Enabled,
		"storing a key for a source with no block wrote enabled:false, which switches it off",
	)

	assert.Equal(t, 1, store.saves, "the store was asked to save times, want 1")

	info, err := client.SourceInfo(context.Background(), "nexus")
	require.NoError(t, err, "SourceInfo")

	assert.True(
		t,
		info.HasAPIKey,
		"the source info reported no stored key right after one was stored",
	)

	assert.True(
		t,
		info.Authenticated,
		"the source info is not authenticated after a successful sign-in",
	)
}

func TestAKeyTheSourceRefusesIsNeverStored(t *testing.T) {
	// A rejected key left on disk is worse than none: the next command tries it again
	// and the user cannot tell it from a working one.
	source := &failingSource{stubSource: stubSource{id: "nexus"}, code: ErrCodeAuthFailed}
	cfg := domain.DefaultConfig()
	client, store, _ := credentialClient(t, source, &cfg)

	err := client.AuthenticateSource(
		context.Background(),
		"nexus",
		map[string]string{"api_key": "wrong"},
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeAuthFailed),
		"AuthenticateSource returned %v, want an auth failure",
		err,
	)

	assert.Equal(t, 0, store.saves, "a refused key was written to the settings file")
}

func TestStoringAKeySignsInTheLiveSourceAndWritesTheFile(t *testing.T) {
	// The registry caches built sources for the life of the process. A key that
	// only reached the settings file left the running source signing nothing, so
	// every download failed on AUTH_REQUIRED and the prompt came back — until the
	// next launch, when the source was rebuilt from the file that already had it.
	source := &stubSource{id: "nexus"}
	cfg := domain.DefaultConfig()
	client, store, _ := credentialClient(t, source, &cfg)

	require.NoError(t, client.SetSourceAPIKey(
		context.Background(),
		"nexus",
		"abc",
	), "SetSourceAPIKey")

	creds := source.lastCreds()
	assert.Equal(
		t,
		"abc",
		creds["api_key"],
		"the live source never received the key it was just given",
	)

	block, ok := store.last().SourceConfigFor("nexus")
	require.True(t, ok, "the accepted key was not stored")

	assert.Equal(t, "abc", block.APIKey, "the stored key, want the one the source accepted")

	info, err := client.SourceInfo(context.Background(), "nexus")
	require.NoError(t, err, "SourceInfo")

	assert.True(
		t,
		info.HasAPIKey,
		"the source info reports no stored key right after one was stored",
	)
	assert.True(t, info.Authenticated, "the source is not signed in after storing its key")
}

func TestAKeyTheSourceRefusesNeverReachesItOrTheFile(t *testing.T) {
	// Validating before storing is the point: a refused key in the settings file
	// looks configured on the next launch and fails on first use, which is
	// harder to diagnose than no configuration at all.
	source := &failingSource{stubSource: stubSource{id: "nexus"}, code: ErrCodeAuthFailed}
	cfg := domain.DefaultConfig()
	client, store, _ := credentialClient(t, source, &cfg)

	err := client.SetSourceAPIKey(context.Background(), "nexus", "wrong")
	assert.True(
		t,
		IsAppError(err, ErrCodeAuthFailed),
		"SetSourceAPIKey returned %v, want an auth failure",
		err,
	)

	assert.Len(t, source.lastCreds(), 0, "a refused key was handed to the source")

	assert.Equal(t, 0, store.saves, "a refused key was written to the settings file")
}

func TestCredentialsCanBeStoredWithoutAskingTheSource(t *testing.T) {
	// Verification is a round trip to the provider, which is the wrong thing to
	// insist on for a machine that cannot reach it yet.
	source := &stubSource{id: "nexus"}
	cfg := domain.DefaultConfig()
	client, store, _ := credentialClient(t, source, &cfg)

	// The id arrives capitalised, as a settings screen would send it.
	require.NoError(
		t,
		client.SetSourceCredentials(context.Background(), "Nexus", map[string]string{
			"api_key": "unchecked",
		}),
		"SetSourceCredentials",
	)

	creds := source.lastCreds()
	assert.Len(t, creds, 0, "storing a key without verifying handed the source")

	saved := store.last()
	_, ok := saved.SourceConfigFor("nexus")
	require.True(t, ok, "the key was not stored under the folded id: %+v", saved.Sources)

	for key := range saved.Sources {
		assert.Equal(t, "nexus", key, "storing one key created a second block")
	}

	block, _ := saved.SourceConfigFor("nexus")
	assert.Equal(
		t,
		"unchecked",
		block.APIKey,
		"the stored block, want an enabled block holding the key",
	)
	assert.True(t, block.Enabled, "the stored block, want an enabled block holding the key")

	require.NoError(t, client.SetSourceCredentials(context.Background(), "nexus", map[string]string{
		"api_key":  "unchecked",
		"username": "modder",
	}), "SetSourceCredentials")

	block, _ = store.last().SourceConfigFor("nexus")
	assert.Equal(
		t,
		"modder",
		block.Options["username"],
		"the username, want it stored as an option",
	)
}

func TestForgettingAKeyClearsTheFileAndTheLiveSource(t *testing.T) {
	// Removing it from the file alone would leave the built source sending the
	// key until the next launch, which is the opposite of what "forget" means.
	source := &forgettableSource{stubSource: stubSource{id: "nexus"}}
	cfg := domain.DefaultConfig()
	cfg.Sources = map[string]domain.SourceConfig{"nexus": {Enabled: true, APIKey: "abc"}}
	client, store, _ := credentialClient(t, source, &cfg)

	require.NoError(t, client.AuthenticateSource(
		context.Background(),
		"nexus",
		map[string]string{"api_key": "abc"},
	), "AuthenticateSource")

	require.NoError(t, client.ClearSourceAPIKey(context.Background(), "nexus"), "ClearSourceAPIKey")

	block, _ := store.last().SourceConfigFor("nexus")
	assert.Empty(t, strings.TrimSpace(block.APIKey), "the key is still in the settings file")

	assert.Equal(t, 1, source.forgetCount(), "the live source was told to forget times, want 1")

	info, _ := client.SourceInfo(context.Background(), "nexus")
	assert.False(
		t,
		info.Authenticated,
		"the source still reports itself authenticated after its key was forgotten",
	)
}

func TestForgettingAKeyThatWasNeverStored(t *testing.T) {
	source := &forgettableSource{stubSource: stubSource{id: "nexus"}}
	cfg := domain.DefaultConfig()
	cfg.Sources = map[string]domain.SourceConfig{"nexus": {Enabled: true}}
	client, store, _ := credentialClient(t, source, &cfg)

	err := client.ClearSourceAPIKey(
		context.Background(),
		"nexus",
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeNotFound),
		"ClearSourceAPIKey returned %v, want not-found",
		err,
	)

	err = client.ClearSourceAPIKey(
		context.Background(),
		"nosuchsource",
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeNotFound),
		"ClearSourceAPIKey for an unknown source returned %v, want not-found",
		err,
	)

	assert.Equal(t, 0, source.forgetCount(), "a source with no key was told to forget times")

	assert.Equal(t, 0, store.saves, "a failed forget wrote to the settings file")
}

func TestForgettingIsRefusedByASourceThatCannotForget(t *testing.T) {
	// Telling the user the key is gone while the process keeps using it is the
	// one outcome worse than not offering the operation at all.
	source := &stubSource{id: "nexus"}
	cfg := domain.DefaultConfig()
	cfg.Sources = map[string]domain.SourceConfig{"nexus": {Enabled: true, APIKey: "abc"}}
	client, store, _ := credentialClient(t, source, &cfg)

	err := client.ClearSourceAPIKey(context.Background(), "nexus")
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"ClearSourceAPIKey returned %v, want invalid-argument",
		err,
	)
	// The file is cleaned first, because that half can be guaranteed and the
	// error is what stops the caller from claiming the whole thing worked.
	block, _ := store.last().SourceConfigFor("nexus")
	assert.Empty(t, block.APIKey, "the key is still in the settings file after a refused forget")
}

func TestCredentialsNeedAConfigurationStoreToLandIn(t *testing.T) {
	// A client built straight from services has nowhere to persist. Reporting a
	// sign-in that will not survive a restart is worse than refusing it.
	registry := services.NewSourceRegistry()
	require.NoError(t, registry.Register(&stubSource{id: "nexus"}), "register")

	client, err := NewClientFromServices(ClientDeps{Sources: registry})
	require.NoError(t, err, "NewClientFromServices")

	defer client.Close()

	err = client.SetSourceAPIKey(
		context.Background(),
		"nexus",
		"abc",
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"SetSourceAPIKey returned %v, want invalid-argument",
		err,
	)

	err = client.ClearSourceAPIKey(
		context.Background(),
		"nexus",
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"ClearSourceAPIKey returned %v, want invalid-argument",
		err,
	)
}

func TestHalfACredentialIsNotAStoredCredential(t *testing.T) {
	cfg := domain.DefaultConfig()
	client, store, _ := credentialClient(t, &stubSource{id: "nexus"}, &cfg)

	for name, creds := range map[string]map[string]string{
		"nothing":     {},
		"only blanks": {"api_key": "   ", "username": ""},
		"only a name": {"": "abc"},
	} {
		err := client.SetSourceCredentials(
			context.Background(),
			"nexus",
			creds,
		)
		assert.True(
			t,
			IsAppError(err, ErrCodeInvalidArgument),
			"SetSourceCredentials(%s) returned %v, want invalid-argument",
			name,
			err,
		)
	}

	err := client.SetSourceCredentials(
		context.Background(),
		"nexus",
		map[string]string{"options.api_base": "x"},
	)
	assert.True(
		t,
		IsAppError(err, ErrCodeInvalidArgument),
		"a credential name with a dot returned %v, want invalid-argument",
		err,
	)

	assert.Equal(t, 0, store.saves,
		"an unusable credential set was written to the settings file",
	)
}
