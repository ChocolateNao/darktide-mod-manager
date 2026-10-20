package games

import (
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games/darktide"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubGame is a second game used only by these tests: the registry must hold
// more than one definition without either leaking into the other's lookups.
type stubGame struct{}

func (stubGame) ID() string                     { return "stub-game" }
func (stubGame) DisplayName() string            { return "Stub Game" }
func (stubGame) Description() string            { return "a test double" }
func (stubGame) SteamAppID() string             { return "" }
func (stubGame) InstallDirNames() []string      { return []string{"StubGame"} }
func (stubGame) BinaryNames() []string          { return []string{"stub.exe"} }
func (stubGame) VersionMarkers() []string       { return []string{"stub.exe"} }
func (stubGame) ModsPathCandidates() [][]string { return [][]string{{"mods"}} }
func (stubGame) ModsDirName() string            { return "mods" }
func (stubGame) ModInstallMethod() string       { return domain.InstallMethodDirect }
func (stubGame) ManifestNames() []string        { return []string{"mod.toml"} }
func (stubGame) LoadOrderFile() string          { return "" }
func (stubGame) InstallModes() []domain.ModInstallMode {
	return []domain.ModInstallMode{domain.InstallModeModsFolder}
}

func (stubGame) SourceDefaults() map[string]map[string]string {
	return map[string]map[string]string{}
}

func (stubGame) GameActions() []domain.GameAction {
	return nil
}

func withCleanRegistry(t *testing.T) {
	t.Helper()
	Reset()
	t.Cleanup(Reset)
}

// builtinIDs lives here rather than in the registry so a new built-in game makes
// these tests say what to add instead of silently passing.
var builtinIDs = []string{darktide.ID}

func TestEnsureBuiltinsRegistersEveryBuiltinIdempotently(t *testing.T) {
	withCleanRegistry(t)

	require.NoError(t, EnsureBuiltins(), "first EnsureBuiltins")
	require.NoError(t, EnsureBuiltins(), "second EnsureBuiltins must be a no-op")

	for _, id := range builtinIDs {
		def, ok := Get(id)
		require.True(t, ok, "%s registered", id)
		assert.Equal(t, id, def.ID())
	}

	assert.ElementsMatch(t, builtinIDs, IDs(), "exactly the built-in games registered")
	assert.Equal(t, darktide.ID, DefaultID(), "the first built-in game is the default")
}

func TestRegisterRejectsInvalidDefinitions(t *testing.T) {
	withCleanRegistry(t)

	assert.Error(t, Register(nil), "nil definition")

	assert.Error(t, Register(blankIDGame{}), "blank id")

	assert.Error(t, Register(badMethodGame{}), "unknown install method")

	require.NoError(t, Register(darktide.New()))
	assert.Error(t, Register(darktide.New()), "duplicate id")
}

func TestGetFoldsCaseAndPadding(t *testing.T) {
	withCleanRegistry(t)
	require.NoError(t, Register(stubGame{}))

	for _, spelling := range []string{"stub-game", "Stub-Game", "  STUB-GAME  "} {
		def, ok := Get(spelling)
		assert.True(t, ok, "Get(%q)", spelling)
		assert.Equal(t, "stub-game", def.ID(), "Get(%q)", spelling)
	}

	_, ok := Get("")
	assert.False(t, ok, "blank id")

	_, ok = Get("nope")
	assert.False(t, ok, "unknown id")
}

func TestMustGetPanicsOnUnknownID(t *testing.T) {
	withCleanRegistry(t)

	assert.Panics(t, func() { MustGet("missing") }, "MustGet(unknown)")

	require.NoError(t, Register(stubGame{}))
	assert.Equal(t, "stub-game", MustGet("stub-game").ID(), "MustGet(known)")
}

func TestAllIsSortedAndDefaultIsFirstRegistered(t *testing.T) {
	withCleanRegistry(t)
	require.NoError(t, Register(stubGame{}))
	require.NoError(t, Register(darktide.New()))

	assert.Equal(t, []string{"darktide", "stub-game"}, IDs(), "ids sorted")
	assert.Equal(t, "stub-game", DefaultID(), "first registered wins the default")

	all := All()
	require.Len(t, all, 2, "All()")
	assert.Equal(t, "darktide", all[0].ID(), "All() sorted by id")
	assert.Equal(t, "stub-game", all[1].ID(), "All() sorted by id")
}

func TestResetEmptiesTheRegistryAndBuiltinsFlag(t *testing.T) {
	withCleanRegistry(t)
	require.NoError(t, EnsureBuiltins())

	Reset()

	assert.Empty(t, IDs(), "registry not emptied")
	assert.Equal(t, "", DefaultID(), "default not cleared")

	// Builtins can be registered again after a Reset, so the flag was cleared too.
	require.NoError(t, EnsureBuiltins())
	assert.ElementsMatch(t, builtinIDs, IDs())
}

// blankIDGame and badMethodGame are minimal invalid definitions.
type blankIDGame struct{ stubGame }

func (blankIDGame) ID() string { return "  " }

type badMethodGame struct{ stubGame }

func (badMethodGame) ModInstallMethod() string { return "teleport" }

func (badMethodGame) GameActions() []domain.GameAction { return nil }

// hostedGame claims a sub-domain on Nexus, the way a real game does.
type hostedGame struct{ stubGame }

func (hostedGame) ID() string { return "hosted-game" }

func (hostedGame) SourceDefaults() map[string]map[string]string {
	return map[string]map[string]string{
		"nexus": {domain.SourceDomainKey: "someotherslug"},
	}
}

func TestBySourceDomainFindsTheGameThatClaimsASlug(t *testing.T) {
	withCleanRegistry(t)
	require.NoError(t, EnsureBuiltins(), "EnsureBuiltins")
	require.NoError(t, Register(hostedGame{}), "Register")

	def, ok := BySourceDomain("nexus", "someotherslug")
	require.True(t, ok, "a registered game's own slug must resolve back to it")
	assert.Equal(t, "hosted-game", def.ID())

	// A launcher has no say over how somebody typed the slug.
	_, ok = BySourceDomain("nexus", "SomeOtherSlug")
	assert.True(t, ok, "the lookup must ignore case")

	def, ok = BySourceDomain("nexus", "warhammer40kdarktide")
	require.True(t, ok, "a built-in game's slug must resolve")
	assert.Equal(t, darktide.ID, def.ID())
}

func TestBySourceDomainRefusesWhatNoGameClaims(t *testing.T) {
	withCleanRegistry(t)
	require.NoError(t, EnsureBuiltins(), "EnsureBuiltins")

	for _, slug := range []string{"", "   ", "someotherslug", "warhammer40kdarkti"} {
		_, ok := BySourceDomain("nexus", slug)
		assert.False(t, ok, "BySourceDomain resolved the unregistered slug %q", slug)
	}

	// The slug is per source: a game can claim one site's slug and another's not.
	_, ok := BySourceDomain("github", "warhammer40kdarktide")
	assert.False(t, ok, "a slug from one source must not answer for another")
}

func TestSourceDomainsListsWhatWouldBeAccepted(t *testing.T) {
	withCleanRegistry(t)
	require.NoError(t, EnsureBuiltins(), "EnsureBuiltins")

	assert.Equal(
		t,
		[]string{"warhammer40kdarktide"},
		SourceDomains("nexus"),
		"only games that claim a slug on this source are listed",
	)
	assert.Empty(t, SourceDomains("github"), "no built-in claims a slug on github")
}
