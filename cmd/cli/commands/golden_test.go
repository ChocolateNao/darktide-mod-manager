package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file records what the command line looks like from the outside.
//
// The transcripts live in testdata and are rewritten with
// `go test ./cmd/cli/commands/ -update`. Every one of them is produced by
// running the real command against a real container over a real database, a
// real archive and a real mod folder, so a change in the wiring shows up here
// rather than in a user's terminal.
//
// Two things are deliberately kept out of the transcripts: the temporary
// directories (scrubbed to placeholders by the harness) and timestamps
// (scrubbed to <time>). Everything else is pinned, including the wording of
// every message, because the wording is the interface.

func TestModLifecycle(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	modID := w.writeModArchive(t, "Better UI", "1.2.0")

	g := newGolden(t, "mod-lifecycle")
	g.run(w, "mod", "list")
	g.run(w, "mod", "install", "local", modID)
	g.run(w, "mod", "list")
	g.run(w, "mod", "list", "--json")
	// Padding around an id is corrected for: it comes from a copy-paste and the
	// user cannot see it. Case is not, and the transcript pins that too — two
	// mods may differ only in case on a case-sensitive volume, so folding it
	// would be a guess about which one is meant.
	g.run(w, "mod", "show", "  "+modID+"  ")
	g.run(w, "mod", "show", strings.ToUpper(modID))
	g.run(w, "mod", "path", modID)
	g.run(w, "mod", "symlink-status")
	g.run(w, "mod", "disable", modID)
	g.run(w, "mod", "list")
	g.run(w, "mod", "enable", modID)
	g.run(w, "mod", "relink", modID)
	g.run(w, "mod", "reorder", modID)
	g.run(w, "mod", "conflicts")
	g.run(w, "mod", "update", "--check", modID)
	g.run(w, "mod", "uninstall", modID)
	g.run(w, "mod", "list")
	g.run(w, "mod", "uninstall", modID)
	g.want()
}

func TestModInstallWithoutAGameStaysDisabled(t *testing.T) {
	// The interesting case: a mod installed before the game has been found is
	// installed but not enabled, and every command that follows has to report
	// that honestly rather than pretending the deploy happened.
	w := newWorkspace(t)
	w.writeConfig(t)
	modID := w.writeModArchive(t, "Better UI", "1.2.0")

	g := newGolden(t, "mod-no-game")
	g.run(w, "game", "detect")
	g.run(w, "mod", "install", "local", modID)
	g.run(w, "mod", "list")
	g.run(w, "mod", "enable", modID)
	g.run(w, "mod", "symlink-status")
	g.run(w, "game", "info")
	g.want()
}

func TestModInstallFlags(t *testing.T) {
	// Every install flag is a decision the user makes deliberately, so each one
	// is exercised against a real install and its effect is in the transcript.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	first := w.writeModArchive(t, "Better UI", "1.2.0")
	second := w.writeModArchive(t, "Damage Numbers", "2.0.0")
	third := w.writeModArchive(t, "Sound Overhaul", "4.0.0")

	g := newGolden(t, "mod-install-flags")
	g.run(w, "mod", "install", "local", first, "--no-enable")
	g.run(w, "mod", "install", "local", first)
	g.run(w, "mod", "install", "local", first, "--force")
	g.run(w, "mod", "install", "local", second, "--version", "3.0.0")
	// A local archive is named by its file name with the version suffix stripped
	// and its version is always 0.0.0, so the version a user would type from the
	// file name is the one that does not match. Both directions are recorded: a
	// check that only ever failed would look like a check that cannot pass.
	g.run(w, "mod", "install", "local", second, "--version", "2.0.0")
	g.run(w, "mod", "install", "local", second, "--version", "0.0.0")
	g.run(w, "cache", "info")
	g.run(w, "mod", "install", "local", third, "--purge-cache")
	g.run(w, "cache", "info")
	g.run(w, "mod", "install", "nosuchsource", third)
	g.run(w, "mod", "install", "local", "nosuchmod")
	g.run(w, "mod", "install", "local")
	g.run(w, "mod", "uninstall", first, "--purge")
	g.run(w, "mod", "uninstall", "nothing-here")
	g.want()
}

func TestModHooksEditing(t *testing.T) {
	// A hook set is read-modify-write, so the transcript has to show each step
	// landing where the previous one left it.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	modID := w.writeModArchive(t, "Better UI", "1.2.0")

	g := newGolden(t, "mod-hooks")
	g.run(w, "mod", "install", "local", modID)
	g.run(w, "mod", "hooks", "list", modID)
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable",
		"--type", "javascript", "--content", "console.log(dmm.context.mod.id)")
	g.run(w, "mod", "hooks", "list", modID)
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable",
		"--content", "console.log('second')", "--timeout", "5", "--env", "TOKEN=a=b")
	g.run(w, "mod", "hooks", "list", modID)
	g.run(w, "mod", "hooks", "list", modID, "--json")
	g.run(w, "mod", "hooks", "remove", modID, "--phase", "pre_enable", "--index", "1")
	g.run(w, "mod", "hooks", "list", modID)
	g.run(w, "mod", "hooks", "remove", modID, "--phase", "pre_enable", "--index", "9")
	// --index 0 is here because the flag's zero value is also what the flag set
	// holds when the flag was never given. Reading the value instead of asking
	// the flag set would make this silently clear the whole phase.
	g.run(w, "mod", "hooks", "remove", modID, "--phase", "pre_enable", "--index", "0")
	g.run(w, "mod", "hooks", "remove", modID, "--phase", "pre_enable", "--all", "--index", "1")
	g.run(w, "mod", "hooks", "remove", modID, "--phase", "not_a_phase", "--all")
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable", "--type", "powershell",
		"--content", "Write-Host x")
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable")
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable",
		"--content", "a", "--file", "b")
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable",
		"--content", "a", "--env", "NOEQUALS")
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable",
		"--content", "   ")
	g.run(w, "mod", "hooks", "add", "nothing-here", "--phase", "pre_enable", "--content", "a")
	// No --index and no --all is a different code path from --all, and the one a
	// person reaches for first, so both are recorded.
	g.run(w, "mod", "hooks", "remove", modID, "--phase", "pre_enable")
	g.run(w, "mod", "hooks", "list", modID)
	g.run(w, "mod", "hooks", "remove", modID, "--phase", "pre_enable", "--all")
	g.run(w, "mod", "hooks", "list", modID)
	g.want()
}

func TestModHooksFromAFile(t *testing.T) {
	// --file and --from are the paths a person takes when a hook is longer than
	// a command line, and both have to name the file in every failure.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	modID := w.writeModArchive(t, "Better UI", "1.2.0")

	script := filepath.Join(w.root, "hook.js")
	require.NoError(
		t,
		os.WriteFile(script, []byte("console.log('from a file')\n"), 0o644),
		"write script",
	)

	empty := filepath.Join(w.root, "empty.js")
	require.NoError(t, os.WriteFile(empty, nil, 0o644), "write empty script")
	// A hook set as the edit command accepts it: a pre_enable script and a
	// post_enable one, so the whole set is replaced rather than merged.
	hooks := filepath.Join(w.root, "hooks.json")
	require.NoError(
		t,
		os.WriteFile(hooks, []byte(`{"PreEnable":[{"language":"javascript","content":"a"}],`+
			`"PostEnable":[{"language":"javascript","content":"b"},{"language":"lua","content":"c"}]}`), 0o644),
		"write hooks",
	)

	g := newGolden(t, "mod-hooks-file")
	g.run(w, "mod", "install", "local", modID)
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable", "--file", script)
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable", "--file", empty)
	g.run(w, "mod", "hooks", "add", modID, "--phase", "pre_enable", "--file", "no-such-file.js")
	g.run(w, "mod", "hooks", "edit", modID)
	g.run(w, "mod", "hooks", "edit", modID, "--from", hooks)
	g.run(w, "mod", "hooks", "list", modID)
	g.run(w, "mod", "hooks", "edit", modID, "--from", "no-such-file.json")
	g.run(w, "mod", "hooks", "edit", modID, "--from", script)
	g.run(w, "mod", "hooks", "edit", "nothing-here")
	g.want()
}

func TestModShareAndImport(t *testing.T) {
	// share and import are two halves of one feature, so both the refusals and
	// the accepted forms belong in one transcript.
	//
	// No accepted link is imported here. A link is only worth importing when the
	// provider answers, so a transcript containing one records whether the
	// machine running the test had a working route to nexusmods.com and to
	// vortex.com — which makes the file fail somewhere else for no reason. The
	// accepted forms are covered exhaustively in modlink_test.go, and the fetch
	// itself is the source plugins' own tested responsibility.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	modID := w.writeModArchive(t, "Better UI", "1.2.0")

	g := newGolden(t, "mod-share")
	g.run(w, "mod", "install", "local", modID)
	g.run(w, "mod", "share", modID)
	g.run(w, "mod", "share", modID, "--json")
	g.run(w, "mod", "share", "nothing-here")
	g.run(w, "mod", "import-link", "https://www.nexusmods.com/darktide/mods/7001")
	g.run(w, "mod", "import-link", "900/7001")
	g.run(w, "mod", "import-link", "nxm link fallout4/900/7001")
	g.run(w, "mod", "import-link", "nxm link darktide/900")
	g.run(w, "mod", "import-link", "")
	g.run(w, "mod", "import-link")
	g.want()
}

func TestModUpdate(t *testing.T) {
	// A local mod is the awkward case for an update check, and the awkwardness
	// is worth recording rather than hiding: the local source names an archive
	// mod after the file and strips the version suffix, so the mod's version is
	// 0.0.0 and there is never an update to offer. The transcript says "up to
	// date" because that is the truth, and a reader who disagrees has the
	// evidence in front of them.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	modID := w.writeModArchive(t, "Better UI", "1.2.0")

	g := newGolden(t, "mod-update")
	g.run(w, "mod", "install", "local", modID)
	g.run(w, "mod", "update", "--check", modID)
	g.run(w, "mod", "update", "--check", modID, "--json")
	g.run(w, "mod", "update", "--check", "nothing-here")
	g.run(w, "mod", "update", "--check")
	g.run(w, "mod", "update", "--check", "--json")
	// --check must not install anything, whether or not a mod is named. Both
	// of these would have applied the update if the flag were ignored.
	g.run(w, "mod", "list")
	g.run(w, "mod", "update", modID)
	g.run(w, "mod", "update", "nothing-here")
	g.run(w, "mod", "update")
	g.want()
}

func TestTwoLocalVersionsOfOneModCollide(t *testing.T) {
	// Both files resolve to the same id, and the local source refuses rather
	// than picking one: letting install order decide would mean the payload
	// under a deployed mod changed without anybody asking.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	first := w.writeModArchive(t, "Better UI", "1.0.0")

	second := w.writeModArchive(t, "Better UI", "1.3.0")
	require.Equal(
		t,
		first,
		second,
		"the fixture did not produce two files claiming one id: %q and %q",
		first,
		second,
	)

	g := newGolden(t, "mod-update-collision")
	g.run(w, "source", "search", "better", "--source", "local")
	g.run(w, "mod", "install", "local", first)
	g.run(w, "mod", "install", "local", first, "--force")
	g.run(w, "mod", "list")
	g.want()
}

func TestModRelinkAndStatusWithoutDamage(t *testing.T) {
	// relink with no argument only touches what is broken, so a healthy install
	// has to say so rather than doing work and reporting nothing.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	modID := w.writeModArchive(t, "Better UI", "1.2.0")

	g := newGolden(t, "mod-relink-healthy")
	g.run(w, "mod", "install", "local", modID)
	g.run(w, "mod", "relink")
	g.run(w, "mod", "symlink-status", "--json")
	g.run(w, "mod", "relink", modID)
	g.run(w, "mod", "relink", "nothing-here")
	g.want()
}

func TestModReorderWithUnlistedMods(t *testing.T) {
	// A mod that is not in the list loses its place. The command has to say how
	// many, or the user has no way to know the order they set is not the order
	// the game will use.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)
	first := w.writeModArchive(t, "Better UI", "1.2.0")
	second := w.writeModArchive(t, "Damage Numbers", "2.0.0")

	g := newGolden(t, "mod-reorder")
	g.run(w, "mod", "install", "local", first)
	g.run(w, "mod", "install", "local", second)
	g.run(w, "mod", "reorder", first)
	g.run(w, "mod", "reorder", second, first)
	g.run(w, "mod", "reorder", first, first)
	g.run(w, "mod", "reorder", "")
	g.run(w, "mod", "reorder", "nothing-here")
	g.want()
}

func TestSourceListingAndSearch(t *testing.T) {
	// The three network sources are switched off in the settings file, so
	// nothing here reaches the internet. That is also what makes the "disabled"
	// column in the transcript worth reading.
	w := newWorkspace(t)
	w.writeConfig(t)
	w.writeModArchive(t, "Better UI", "1.2.0")

	g := newGolden(t, "source-list")
	g.run(w, "source", "list")
	g.run(w, "source", "list", "--json")
	g.run(w, "source", "list", "--details")
	g.run(w, "source", "list", "--details", "--json")
	g.run(w, "source", "search", "better")
	g.run(w, "source", "search", "better", "--source", "local")
	g.run(w, "source", "search", "better", "--source", "local", "--tag", "archive")
	g.run(w, "source", "search", "nothing-matches-this")
	g.run(w, "source", "search", "better", "--sort", "downloads")
	g.run(w, "source", "search", "better", "--sort", "sideways")
	g.run(w, "source", "search", "better", "--page", "0", "--page-size", "1")
	g.run(w, "source", "search", "better", "--page", "99")
	g.run(w, "source", "search", "better", "--source", "nosuchsource")
	g.want()
}

func TestSourceEnableDisable(t *testing.T) {
	// enable and disable write one dotted key each, so the settings file keeps
	// whatever else the user put in it. The transcript shows the round trip.
	w := newWorkspace(t)
	w.writeConfig(t)

	g := newGolden(t, "source-enable")
	g.run(w, "source", "list")
	g.run(w, "source", "enable", "nexus")
	g.run(w, "source", "list")
	g.run(w, "config", "get", "sources.nexus.enabled")
	g.run(w, "source", "disable", "nexus")
	g.run(w, "config", "get", "sources.nexus.enabled")
	g.run(w, "source", "disable", "nosuchsource")
	g.run(w, "source", "enable")
	g.want()
}

func TestSourceAuthentication(t *testing.T) {
	// A key typed on a command line is visible in the process list and lands in
	// shell history, so the flag set is deliberately awkward and each refusal
	// has to explain itself.
	//
	// The Nexus source is pointed at a stub. Every refusal below is decided by
	// this program, and the one line that is not — whether Nexus accepts the
	// key — has to come from somewhere this test controls, or the transcript
	// records the mood of somebody else's server instead of this program's
	// behaviour.
	w := newWorkspace(t)
	w.makeGame(t)
	probe := w.newCredentialProbe(t, 200, `{"data":{"mods":[]}}`)
	t.Setenv("DMM_GITHUB_API_KEY", "from-the-environment")

	g := newGolden(t, "source-auth")
	g.run(w, "source", "auth", "local", "--user", "modder", "--api-key", "secret")
	g.run(w, "source", "auth", "nexus", "--user", "modder")
	g.run(w, "source", "auth", "nosuchsource", "--user", "u", "--api-key", "k")
	g.run(w, "source", "auth", "nexus", "--user", "u", "--api-key", "a", "--api-key-stdin")
	g.run(w, "source", "auth", "nexus", "--api-key-env", "DMM_MISSING_VARIABLE")
	g.withStdin("").run(w, "source", "auth", "nexus", "--api-key-stdin")
	g.withStdin("read-from-stdin\n").run(w, "source", "auth", "nexus", "--api-key-stdin")
	// The key has to reach the source, or the transcript is proving nothing
	// about the one line that matters.
	g.run(w, "config", "get", "sources.nexus.api_key")

	// A key the service rejects has to come back as a rejected credential
	// rather than as "the service is down", and a rejected key must not be left
	// in the settings file for the next command to try again.
	probe.answer(
		401,
		`{"title":"Unauthorized","status":401,"detail":"Please provide a valid API Key"}`,
	)
	g.withStdin("rejected-key\n").run(w, "source", "auth", "nexus", "--api-key-stdin")
	g.run(w, "config", "get", "sources.nexus.api_key")
	g.want()

	for _, key := range probe.seen() {
		assert.NotEmpty(t, key, "the source was asked to check a credential without one")
	}
}

func TestSourceCredentialsSurviveWithoutTheSourceAndCanBeTakenBack(t *testing.T) {
	// A key stored without being checked is still a key: the machine may simply
	// not be able to reach Nexus yet. So the user has to be able to see that one
	// is stored, and take it back out again, and neither may depend on the source
	// answering -- otherwise "sign in when you are back online" is impossible and
	// a key pasted by mistake can only be removed by hand-editing JSON.
	w := newWorkspace(t)
	w.makeGame(t)
	probe := w.newCredentialProbe(t, 200, `{"data":{"mods":[]}}`)

	g := newGolden(t, "source-credentials")
	g.run(w, "source", "list")
	g.run(w, "source", "auth", "nexus", "--api-key", "unchecked-key", "--no-verify")
	g.run(w, "source", "list")
	// Write-only means write-only: the key is on disk and nowhere the user can
	// read it back out.
	g.run(w, "config", "get", "sources.nexus.api_key")
	g.run(w, "source", "auth", "nexus", "--forget")
	g.run(w, "source", "list")
	g.run(w, "source", "auth", "nexus", "--forget")
	g.run(w, "source", "test", "nexus")
	// A key typed alongside --forget is a contradiction, not something to ignore:
	// the user would otherwise be left with whichever of the two they did not
	// mean.
	g.run(w, "source", "auth", "nexus", "--forget", "--api-key", "k")
	g.run(w, "source", "auth", "nexus", "--forget", "--no-verify")
	g.run(w, "source", "auth", "local", "--forget")
	g.want()

	// Nothing above may reach the source: the whole point is that storing,
	// listing and forgetting are local operations.
	for _, key := range probe.seen() {
		assert.NotEqual(
			t,
			"unchecked-key",
			key,
			"storing a key with --no-verify contacted the source",
		)
	}
}

func TestSourceTestChecksEverySource(t *testing.T) {
	// `source test` with no name checks everything, so one broken source does
	// not hide the state of the rest — and the exit code reflects the failures.
	w := newWorkspace(t)
	w.writeConfig(t)

	g := newGolden(t, "source-test")
	g.run(w, "source", "test")
	g.run(w, "source", "test", "--json")
	g.run(w, "source", "test", "local")
	g.run(w, "source", "test", "nosuchsource")
	g.want()
}

func TestGameCommands(t *testing.T) {
	// The game commands answer different questions: the registry (list,
	// current, use), the machine (detect), and the configured installation
	// (info, path, validate). `info` is allowed to warn about an unresolvable
	// mods folder without failing: the installation itself was found and is
	// worth reporting.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	g := newGolden(t, "game-found")
	g.run(w, "game", "list")
	g.run(w, "game", "list", "--json")
	g.run(w, "game", "current")
	g.run(w, "game", "current", "--json")
	g.run(w, "game", "use", "darktide")
	g.run(w, "game", "use", "darktide", "--json")
	// An unknown id is refused rather than falling back to whatever game was
	// already active — a typo must not silently run the wrong game.
	g.run(w, "game", "use", "nope")
	g.run(w, "game", "switch", "darktide")
	g.run(w, "game", "detect")
	g.run(w, "game", "detect", "--json")
	g.run(w, "game", "info")
	g.run(w, "game", "info", "--json")
	g.run(w, "game", "path")
	g.run(w, "game", "path", "--with-install")
	g.run(w, "game", "validate", w.gameDir)
	g.run(w, "game", "validate", w.localDir)
	g.run(w, "game", "validate", "  ")
	g.run(w, "game", "validate")
	// `game set` does not exist — the path is configuration — and a user who
	// types it must be told so without changing anything. The pair is recorded
	// with a config read either side so a change slipping through is visible.
	g.run(w, "game", "set", w.gameDir)
	g.run(w, "config", "get", "game.path")
	g.run(w, "game", "set", w.localDir)
	g.run(w, "config", "get", "game.path")
	g.want()
}

func TestGameCommandsWithNothingFound(t *testing.T) {
	// No installation and no configured path: every command has to say so
	// rather than print an empty table, and none of them may exit non-zero for
	// a state that is not a failure. The registry answers (list, current) do
	// not depend on an installation at all and must work regardless.
	w := newWorkspace(t)
	cfg := defaultConfigFor(t)
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = "" })
	w.writeConfigWith(t, &cfg)

	g := newGolden(t, "game-missing")
	g.run(w, "game", "list")
	g.run(w, "game", "current")
	g.run(w, "game", "detect")
	g.run(w, "game", "info")
	g.run(w, "game", "path")
	g.run(w, "game", "validate", w.localDir)
	g.run(w, "game", "set", w.gameDir)
	g.run(w, "game", "detect")
	g.want()
}

func TestCacheCommands(t *testing.T) {
	// clear and prune are destructive, so the transcript pins both the wording
	// and the size that is left afterwards.
	w := newWorkspace(t)
	w.writeConfig(t)
	modID := w.writeModArchive(t, "Better UI", "1.2.0")

	g := newGolden(t, "cache")
	g.run(w, "cache", "info")
	g.run(w, "cache", "info", "--json")
	g.run(w, "mod", "install", "local", modID)
	g.run(w, "cache", "info")
	g.run(w, "cache", "prune", "--max-age-days", "0")
	g.run(w, "cache", "prune", "--max-size-gb", "-1")
	g.run(w, "cache", "prune", "--max-age-days", "-1")
	g.run(w, "cache", "prune", "--max-size-gb", "0.0001")
	g.run(w, "cache", "info")
	g.run(w, "cache", "clear")
	g.run(w, "cache", "clear")
	g.run(w, "cache", "info")
	g.run(w, "cache", "prune", "--max-size-gb", "not-a-number")
	g.want()
}

func TestConfigCommands(t *testing.T) {
	// `config show` prints what the manager believes rather than the file, so
	// a hand-written layout is resolved before anybody reads it.
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	g := newGolden(t, "config")
	g.run(w, "config", "show")
	g.run(w, "config", "edit")
	g.run(w, "config", "get", "game.path")
	g.run(w, "config", "get", "  CACHE.max_size_gb  ")
	g.run(w, "config", "get", "cache.max_size_gb")
	g.run(w, "config", "set", "cache.max_size_gb", "12")
	g.run(w, "config", "get", "cache.max_size_gb")
	g.run(w, "config", "set", "ui.compact_mode", "yes")
	g.run(w, "config", "set", "ui.compact_mode", "maybe")
	g.run(w, "config", "set", "cache.max_size_gb", "twelve")
	g.run(w, "config", "set", "cache.max_size_gb", "-1")
	g.run(w, "config", "set", "no.such.key", "x")
	g.run(w, "config", "set", "  ", "x")
	g.run(w, "config", "get", "no.such.key")
	g.run(w, "config", "get", "  ")
	g.run(w, "config", "get")
	g.run(w, "config", "set", "cache.max_size_gb")
	g.run(w, "config", "set", "sources.local.paths", w.localDir+",/another/folder")
	g.run(w, "config", "get", "sources.local.paths")
	g.run(w, "config", "set", "sources.local.options.max_releases", "5")
	g.run(w, "config", "get", "sources.local.options.max_releases")
	g.run(w, "config", "set", "sources.local.api_key", "written-not-read-back")
	g.run(w, "config", "get", "sources.local.api_key")
	g.run(w, "config", "set", "sources.nosuchsource.enabled", "yes")
	g.run(w, "config", "get", "sources.nosuchsource.enabled")
	g.run(w, "config", "get", "sources.local.nosuchfield")
	g.run(w, "config", "set", "sources.local.", "x")
	g.want()
}

func TestCommandsAgainstAnUnreadableSettingsFile(t *testing.T) {
	// A settings file that is not JSON is the failure a user is most likely to
	// cause by hand, and every command has to report it the same way rather
	// than each rediscovering it in its own words.
	w := newWorkspace(t)
	require.NoError(
		t,
		os.WriteFile(w.configPath, []byte("{not json"), 0o644),
		"write broken settings",
	)

	g := newGolden(t, "config-broken")
	g.run(w, "mod", "list")
	g.run(w, "source", "list")
	g.run(w, "game", "info")
	g.run(w, "cache", "info")
	g.run(w, "config", "get", "game.path")
	// The two commands that build no container still work, because a person
	// whose settings file is broken still needs to read the version and the
	// help.
	g.run(w, "version")
	g.want()
}

func TestCommandsWithNoPathsAtAll(t *testing.T) {
	// No --config, no --cache, no --storage: the manager falls back to the
	// platform directories, which the harness has pointed at a temporary home.
	// Nothing here may touch the real user's files.
	w := newWorkspace(t)
	w.writeConfig(t)

	g := newGolden(t, "platform-defaults")
	g.run(w, "mod", "list")
	g.run(w, "config", "show")
	g.run(w, "cache", "info")
	g.run(w, "config", "get", "game.path")
	g.want()
}
