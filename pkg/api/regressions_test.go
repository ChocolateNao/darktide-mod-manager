package api

import (
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSourceInfoReportsAnUnknownSource. Describing a source must not build it, so
// the registry answers from the configuration — which means an unknown id and a
// source that merely cannot be built produce the same empty description. Has is
// what separates them: without it a frontend holding a stale row renders an empty
// settings page and reports nothing wrong.
func TestSourceInfoReportsAnUnknownSource(t *testing.T) {
	store := &configStoreStub{cfg: configPtr(domain.DefaultConfig())}
	deps := ClientDeps{
		Sources: services.NewSourceRegistryWithStore(store),
		Config:  store,
	}

	client, err := NewClientFromServices(deps)
	require.NoError(t, err, "NewClientFromServices")

	t.Cleanup(func() { _ = client.Close() })

	_, err = client.SourceInfo(
		t.Context(),
		"no-such-source",
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceNotFound),
		"error = %v, want SOURCE_NOT_FOUND",
		err,
	)

	_, err = client.SourceInfo(
		t.Context(),
		"   ",
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT for a blank id",
		err,
	)
	// The bundled sources are all registered by the time the api package is
	// loaded, so one of them has to be described — otherwise the refusal above
	// would be the only answer this method ever gives.
	_, err = client.SourceInfo(t.Context(), "local")
	assert.NoError(t, err, "SourceInfo for a bundled source")
}

// TestParseConflictResolutionIsReExported covers the delegation: both entry points
// that pass a resolution as a string work in terms of this package, so the parser
// has to be reachable from here or one of them has to reach into the service
// layer for it.
func TestParseConflictResolutionIsReExported(t *testing.T) {
	for _, text := range []string{"keep-both", "disable_mod_a", "UNINSTALL-MOD-B"} {
		got, ok := ParseConflictResolution(text)
		if !ok {
			assert.Fail(t, "ParseConflictResolution(%q) refused it", text)
			continue
		}

		round, ok := ParseConflictResolution(got.String())
		assert.True(
			t,
			ok && round == got,
			"%q did not survive the round trip through %q",
			text,
			got.String(),
		)
	}

	_, ok := ParseConflictResolution("disable-everything")
	assert.False(t, ok, "an unrecognised resolution was accepted")
}

// TestSetConfigValueNamesTheFailingKey is the complaint a settings form needs: the
// value "maybe" is meaningless on its own, and the user is looking at a form with
// twenty boxes in it.
func TestSetConfigValueNamesTheFailingKey(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())

	cases := []struct {
		key   string
		value string
	}{
		{"advanced.enable_experimental", "maybe"},
		{"cache.max_size_gb", "not a number"},
		{"cache.max_age_days", "-4"},
		// game.path is deliberately absent: it is a plain string, and clearing it is
		// how a user goes back to auto-detection, so an empty value is a request
		// rather than a mistake.
	}
	for _, tc := range cases {
		err := SetConfigValue(cfg, tc.key, tc.value)
		if err == nil {
			assert.Fail(t, "SetConfigValue(%q, %q) reported success", tc.key, tc.value)
			continue
		}

		assert.Contains(t, err.Error(), tc.key, "the error for %q does not name the key", tc.key)

		assert.True(
			t,
			appErrors.IsAnyCode(err, appErrors.ErrCodeInvalidArgument),
			"the error for %q is %v, want INVALID_ARGUMENT",
			tc.key,
			appErrors.CodeOf(err),
		)
	}

	// A source key is named the same way, with the id spelled as the caller wrote
	// it, because that is the key they will look for in the file.
	err := SetConfigValue(cfg, "sources.nexus.enabled", "perhaps")
	require.Error(t, err, "a source setting accepted a bad boolean")

	assert.Contains(
		t,
		err.Error(),
		"sources.nexus.enabled",
		"the error does not name the source key",
	)
}

// TestSetConfigValueStillReportsUnknownKeysSeparately: naming the key must not
// swallow the distinction between "no such setting" and "no such value".
func TestSetConfigValueStillReportsUnknownKeysSeparately(t *testing.T) {
	cfg := configPtr(domain.DefaultConfig())
	err := SetConfigValue(
		cfg,
		"no.such.key",
		"1",
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeNotFound),
		"error = %v, want NOT_FOUND",
		err,
	)

	err = SetConfigValue(
		nil,
		"game.path",
		"/x",
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT for a nil configuration",
		err,
	)

	err = SetConfigValue(
		cfg,
		"  ",
		"1",
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT for a blank key",
		err,
	)
}
