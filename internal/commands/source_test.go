package commands

import (
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSourcesDescribesEveryBundledSource(t *testing.T) {
	w := newWorkspace(t)
	w.writeModArchive(t, "better_ui", "1.2.0")
	app := w.mustApp(t)

	infos, err := app.Sources.ListSources()
	require.NoError(t, err, "ListSources")

	require.NotNil(t, infos, "ListSources returned nil")

	byID := make(map[string]api.ModSourceInfo, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}

	for _, id := range []string{"local", "github", "nexus"} {
		{
			_, ok := byID[id]
			assert.True(t, ok, "ListSources omits the bundled source %q; got %v", id, idsOf(infos))
		}
	}

	assert.False(
		t,
		byID["nexus"].Enabled,
		"a source disabled in the configuration was reported as enabled",
	)

	assert.True(
		t,
		byID["local"].Enabled,
		"the local source is enabled by default but was reported as disabled",
	)

	assert.True(
		t,
		byID["local"].SupportsSearch,
		"the local source supports search and should say so",
	)

	assert.False(
		t,
		byID["local"].RequiresAPIKey,
		"the local source needs no API key and should not ask for one",
	)

	assert.False(
		t,
		byID["local"].Name == "" || byID["local"].Version == "",
		"the local source is described as %+v",
		byID["local"],
	)
}

func TestSourceInfoDescribesOneSource(t *testing.T) {
	app := newWorkspace(t).mustApp(t)

	info, err := app.Sources.SourceInfo("local")
	require.NoError(t, err, "SourceInfo")

	assert.Equal(t, "local", info.ID, "SourceInfo returned %+v", info)
}

func TestSourceInfoReportsAnUnknownSource(t *testing.T) {
	app := newWorkspace(t).mustApp(t)
	{
		_, err := app.Sources.SourceInfo(
			"no-such-source",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "error = %v, want SOURCE_NOT_FOUND", err)
	}
}

func TestSearchSourceFindsALocalMod(t *testing.T) {
	w := newWorkspace(t)
	w.writeModArchive(t, "better_ui", "1.2.0")
	w.writeModArchive(t, "other_pack", "0.9.0")
	app := w.mustApp(t)

	mods, err := app.Sources.SearchSource("local", "better")
	require.NoError(t, err, "SearchSource")

	require.False(
		t,
		len(mods) != 1 || mods[0].ID != "better_ui",
		"SearchSource(better) = %v, want one mod called better_ui",
		idsOfMods(mods),
	)

	all, err := app.Sources.SearchSource("local", "")
	require.NoError(t, err, "SearchSource with an empty query")

	assert.Len(t, all, 2, "SearchSource(\"\") = %v, want both mods", idsOfMods(all))
}

func TestSearchSourceReportsAnUnknownSource(t *testing.T) {
	app := newWorkspace(t).mustApp(t)
	{
		_, err := app.Sources.SearchSource(
			"no-such-source",
			"ui",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "error = %v, want SOURCE_NOT_FOUND", err)
	}
}

func TestSearchAllUnionsTheSearchableSources(t *testing.T) {
	w := newWorkspace(t)
	w.writeModArchive(t, "better_ui", "1.2.0")
	app := w.mustApp(t)

	mods, err := app.Sources.SearchAll(api.SearchQuery{Query: "better"})
	require.NoError(t, err, "SearchAll")

	assert.False(
		t,
		len(mods) != 1 || mods[0].ID != "better_ui",
		"SearchAll = %v, want better_ui",
		idsOfMods(mods),
	)

	empty, err := app.Sources.SearchAll(api.SearchQuery{Query: "nothing-matches-this"})
	require.NoError(t, err, "SearchAll with no matches")

	assert.NotNil(t, empty, "SearchAll returned nil for no matches")

	assert.Empty(t, empty, "SearchAll = %v, want nothing", idsOfMods(empty))
}

func TestSearchAllReportsWhenNothingCanAnswer(t *testing.T) {
	w := newWorkspace(t)

	app := w.mustApp(t, func(o *di.Options) {
		o.SourceConfigs["local"] = map[string]any{"enabled": false}
	})
	{
		_, err := app.Sources.SearchAll(api.SearchQuery{Query: "ui"})
		assert.Error(t, err, "SearchAll reported success with no searchable source")
	}
}

func TestGetSourceModFetchesDetailsWithoutInstalling(t *testing.T) {
	w := newWorkspace(t)
	w.writeModArchive(t, "better_ui", "1.2.0")
	app := w.mustApp(t)

	mod, err := app.Sources.GetSourceMod("local", "better_ui")
	require.NoError(t, err, "GetSourceMod")

	assert.Equal(t, "local", mod.SourceID, "SourceID = %q, want local", mod.SourceID)

	installed, err := app.Mods.ListMods()
	require.NoError(t, err, "ListMods")

	assert.Empty(t, installed, "GetSourceMod installed the mod as a side effect")

	{
		_, err := app.Sources.GetSourceMod(
			"local",
			"no-such-mod",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeModNotFound,
		), "error = %v, want MOD_NOT_FOUND", err)
	}

	{
		_, err := app.Sources.GetSourceMod(
			"no-such-source",
			"better_ui",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "error = %v, want SOURCE_NOT_FOUND", err)
	}
}

func TestAuthenticateSourceRefusesCredentialsTheSourceDoesNotWant(t *testing.T) {
	w := newWorkspace(t)
	w.writeModArchive(t, "better_ui", "1.2.0")
	app := w.mustApp(t)

	err := app.Sources.AuthenticateSource("local", map[string]string{"token": "t"})
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeAuthRequired),
		"error = %v, want AUTH_REQUIRED from a source that needs no credentials",
		err,
	)
}

func TestAuthenticateSourceOnASourceThatNeedsCredentials(t *testing.T) {
	const sourceID = "commands-auth"
	registerBlockingSource(t, sourceID)
	app := newWorkspace(t).mustApp(t)

	{
		err := app.Sources.AuthenticateSource(
			sourceID,
			map[string]string{"token": "t"},
		)
		require.NoError(t, err, "AuthenticateSource: %v", err)
	}

	info, err := app.Sources.SourceInfo(sourceID)
	require.NoError(t, err, "SourceInfo after signing in")

	assert.True(
		t,
		info.Authenticated,
		"the source is not marked authenticated after a successful sign-in",
	)

	{
		err := app.Sources.AuthenticateSource(
			"no-such-source",
			nil,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "error = %v, want SOURCE_NOT_FOUND", err)
	}
}

func TestTestSourceConnectionOnTheLocalSourceIsSilent(t *testing.T) {
	w := newWorkspace(t)
	w.writeModArchive(t, "better_ui", "1.2.0")

	app := w.mustApp(t)
	{
		err := app.Sources.TestSourceConnection("local")
		assert.NoError(t, err, "TestSourceConnection on the local source: %v", err)
	}

	{
		err := app.Sources.TestSourceConnection(
			"no-such-source",
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeSourceNotFound,
		), "error = %v, want SOURCE_NOT_FOUND", err)
	}
}

func TestSetSourceAuthenticatedFlipsTheBadge(t *testing.T) {
	const sourceID = "commands-badge"
	registerBlockingSource(t, sourceID)
	app := newWorkspace(t).mustApp(t)

	{
		err := app.Sources.SetSourceAuthenticated(sourceID, true)
		require.NoError(t, err, "SetSourceAuthenticated(true): %v", err)
	}

	info, err := app.Sources.SourceInfo(sourceID)
	require.NoError(t, err, "SourceInfo")

	require.True(t, info.Authenticated, "the badge did not flip to authenticated")

	{
		err := app.Sources.SetSourceAuthenticated("  "+sourceID+"  ", false)
		require.NoError(t, err, "SetSourceAuthenticated(false): %v", err)
	}

	info, err = app.Sources.SourceInfo(sourceID)
	require.NoError(t, err, "SourceInfo after clearing")

	assert.False(t, info.Authenticated, "the badge did not clear")
}

func TestSetSourceAuthenticatedIsSilentAboutAnUnknownSource(t *testing.T) {
	app := newWorkspace(t).mustApp(t)
	{
		err := app.Sources.SetSourceAuthenticated("no-such-source", true)
		assert.NoError(t, err, "SetSourceAuthenticated on an unknown source: %v", err)
	}
}

func idsOf(infos []api.ModSourceInfo) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.ID)
	}

	return out
}

func idsOfMods(mods []api.Mod) []string {
	out := make([]string, 0, len(mods))
	for _, mod := range mods {
		out = append(out, mod.ID)
	}

	return out
}
