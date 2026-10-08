package services

import (
	"context"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the one case the registry could previously get wrong: the
// composition root builds the effective configuration in memory — the file with
// the caller's overrides merged over it — and deliberately does not write it back,
// because a token supplied on a command line should not end up in a file. The
// registry was reading the file instead, so it disagreed with RegisterAll about
// which sources are switched on: a source the caller had disabled was never built
// and then reported as enabled.

// TestWithConfigWinsOverTheStoreForEnabledSources.
func TestWithConfigWinsOverTheStoreForEnabledSources(t *testing.T) {
	fileCfg := domain.DefaultConfig()
	fileCfg.Sources["github"] = domain.SourceConfig{Enabled: true}
	fileCfg.Sources["nexus"] = domain.SourceConfig{Enabled: false}

	// What the caller asked for at start-up: nexus on, github off. Nothing of
	// this is in the file.
	merged := domain.DefaultConfig()
	merged.Sources["github"] = domain.SourceConfig{Enabled: false}
	merged.Sources["nexus"] = domain.SourceConfig{Enabled: true}

	r := NewSourceRegistryWithStore(newFakeStore(&fileCfg)).
		WithConfig(func() *domain.Config { return &merged })

	for _, id := range []string{"github", "nexus"} {
		{
			err := r.Register(&fakeSource{id: id})
			require.NoError(t, err)
		}
	}

	got := idsOf(r.EnabledSources())
	assert.Equal(
		t,
		"nexus",
		strings.Join(got, ","),
		"EnabledSources() = %v, want [nexus]; the registry read the file, not the merged configuration",
		got,
	)

	{
		info := r.Info("github")
		assert.False(
			t,
			info.Enabled,
			"Info reported a source as enabled although the merged configuration switched it off",
		)
	}

	{
		info := r.Info("nexus")
		assert.True(
			t,
			info.Enabled,
			"Info reported a source as disabled although the merged configuration switched it on",
		)
	}

	for _, info := range r.Infos() {
		assert.False(t, info.ID == "github" && info.Enabled, "Infos reported github as enabled")
	}
}

// TestWithConfigAlsoReachesFactories: the block handed to a factory has to be the
// merged one too, or a source the caller configured in memory would be built from
// the file's block and find no token.
func TestWithConfigAlsoReachesFactories(t *testing.T) {
	fileCfg := domain.DefaultConfig()
	merged := domain.DefaultConfig()
	merged.SetSource("withcfg", domain.SourceConfig{APIKey: "from-memory"})

	var built domain.SourceConfig

	registerFactory(t, "withcfg", func(cfg domain.SourceConfig) (domain.ModSource, error) {
		built = cfg
		return &fakeSource{id: "withcfg"}, nil
	})

	r := NewSourceRegistryWithStore(newFakeStore(&fileCfg)).
		WithConfig(func() *domain.Config { return &merged })
	{
		_, err := r.Get("withcfg")
		require.NoError(t, err, "Get: %v", err)
	}

	assert.Equal(
		t,
		"from-memory",
		built.APIKey,
		"the factory was handed api key %q, want the merged one",
		built.APIKey,
	)
}

// TestWithConfigFallsBackToTheStore: a provider that has no opinion — nil, or a
// read that failed — must not leave the registry with an empty configuration,
// which would make every source count as missing rather than enabled.
func TestWithConfigFallsBackToTheStore(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.Sources["steam"] = domain.SourceConfig{Enabled: false}

	cases := []struct {
		provider ConfigProvider
		name     string
	}{
		{nil, "nil provider"},
		{func() *domain.Config { return nil }, "provider with no opinion"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSourceRegistryWithStore(newFakeStore(&cfg)).WithConfig(tc.provider)
			{
				err := r.Register(&fakeSource{id: "steam"})
				require.NoError(t, err)
			}

			{
				err := r.Register(&fakeSource{id: "local"})
				require.NoError(t, err)
			}

			got := idsOf(r.EnabledSources())
			assert.Equal(
				t,
				"local",
				strings.Join(got, ","),
				"EnabledSources() = %v, want [local] from the store",
				got,
			)
		})
	}
}

// TestWithConfigToleratesANilRegistry: it is a chainable setter, so it has to
// survive being called on nothing rather than panicking inside the wiring.
func TestWithConfigToleratesANilRegistry(t *testing.T) {
	var r *SourceRegistry
	{
		got := r.WithConfig(func() *domain.Config { return nil })
		assert.Nil(t, got, "WithConfig on a nil registry returned %v", got)
	}

	assert.False(t, r.Has("anything"), "Has on a nil registry reported a source")
}

// TestSearchAllSkipsDisabledSources is the other half of the fix. Asking a source
// the user switched off is not a cosmetic problem: a source whose anonymous quota
// is spent still spends it, and one that would need a credential sends one the
// user chose not to give.
func TestSearchAllSkipsDisabledSources(t *testing.T) {
	// The counters are written from the fan-out goroutines and read after it has
	// joined, which the wait inside the fan-out makes safe.
	asked := map[string]int{}
	counting := func(id string) *fakeSource {
		return &fakeSource{
			id: id,
			search: func(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
				asked[id]++
				return []domain.Mod{{ID: id, Version: "1", SourceID: id}}, nil
			},
		}
	}

	cfg := domain.DefaultConfig()
	cfg.Sources["off"] = domain.SourceConfig{Enabled: false}

	r := NewSourceRegistryWithStore(newFakeStore(&cfg))

	for _, source := range []*fakeSource{counting("on"), counting("off")} {
		{
			err := r.Register(source)
			require.NoError(t, err)
		}
	}

	mods, err := r.SearchAll(t.Context(), domain.SearchQuery{})
	require.NoError(t, err, "SearchAll")

	assert.False(
		t,
		len(mods) != 1 || mods[0].ID != "on",
		"SearchAll = %v, want only the enabled source",
		modIDsOf(mods),
	)

	assert.Equal(t, 0, asked["off"], "a disabled source was asked %d times", asked["off"])

	assert.Equal(t, 1, asked["on"], "the enabled source was asked %d times, want 1", asked["on"])
}

// TestSearchAllReportsWhenEverythingIsDisabled: with every source switched off the
// fan-out has nothing to ask, and an empty result would read as "no such mod".
func TestSearchAllReportsWhenEverythingIsDisabled(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.Sources["steam"] = domain.SourceConfig{Enabled: false}
	r := NewSourceRegistryWithStore(newFakeStore(&cfg))

	source := &fakeSource{
		id: "steam",
		search: func(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
			assert.Fail(t, "a disabled source was asked")
			return nil, nil
		},
	}
	{
		err := r.Register(source)
		require.NoError(t, err)
	}

	{
		_, err := r.SearchAll(t.Context(), domain.SearchQuery{})
		assert.Error(t, err, "SearchAll reported success with every source switched off")
	}
}

// TestHasDistinguishesUnknownFromUnbuildable. Describing a source must not build
// it — a source switched off has to stay unbuilt — so Has is the only way to tell
// "switched off" from "no such source", and the two are the same empty
// description without it.
func TestHasDistinguishesUnknownFromUnbuildable(t *testing.T) {
	registerFactory(t, "hasfactory", func(domain.SourceConfig) (domain.ModSource, error) {
		return &fakeSource{id: "hasfactory"}, nil
	})

	r := NewSourceRegistryWithStore(newFakeStore(&domain.Config{}))

	assert.False(t, r.Has("") || r.Has("   "), "a blank id reported a source")

	assert.False(t, r.Has("nosuchsource"), "an unknown id reported a source")

	assert.True(
		t,
		r.Has("hasfactory"),
		"a registered factory is not reported; it has to be, or a source that was never built would look missing",
	)

	{
		err := r.Register(&fakeSource{id: "registered"})
		require.NoError(t, err)
	}

	assert.True(t, r.Has("registered"), "a registered source is not reported")

	assert.True(t, r.Has("  REGISTERED  "), "Has did not normalise the id")

	registerFactory(t, "hasbroken", func(domain.SourceConfig) (domain.ModSource, error) {
		return nil, errBoom
	})

	{
		_, err := r.Get("hasbroken")
		require.Error(t, err, "the broken factory reported success")
	}

	assert.True(
		t,
		r.Has("hasbroken"),
		"a source whose factory failed is not reported; it is known and merely unusable",
	)
}

// modIDsOf lists the mod ids a search reported.
func modIDsOf(mods []domain.Mod) []string {
	out := make([]string, 0, len(mods))
	for _, mod := range mods {
		out = append(out, mod.ID)
	}

	return out
}
