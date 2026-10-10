package commands

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateGolden rewrites the recorded transcripts instead of comparing against
// them. It is a flag rather than a build tag so a deliberate output change is
// one command: `go test ./cmd/cli/commands/ -update`, and then the diff in the
// golden files is the review of that change.
var updateGolden = flag.Bool(
	"update",
	false,
	"rewrite the golden transcripts instead of comparing them",
)

// workspace is a private manager installation: its own home directory, its own
// settings file, its own cache and mod storage, and a Darktide folder to deploy
// into. Nothing in it is shared with the machine running the test, and nothing
// in it is shared between tests.
//
// The home directory is a separate temporary directory rather than a
// subdirectory of the workspace, because the two are scrubbed to different
// placeholders: a golden transcript that cannot tell "the user's home" from
// "this test's tree" is a transcript that quietly stops being checked.
type workspace struct {
	root         string
	home         string
	configPath   string
	cacheRoot    string
	storageRoot  string
	databasePath string
	gameDir      string
	modsDir      string
	localDir     string
	stubBases    []string
}

// newWorkspace builds an isolated tree and points the environment at it.
//
// Every variable platform.ResolveDirs consults is set, not just HOME: on
// Windows the same call reads APPDATA and LOCALAPPDATA, and a test that only
// set HOME would read the real user's settings file on that platform. A test
// that writes to somebody's real configuration is worse than a test that
// fails.
func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	root := t.TempDir()

	home := t.TempDir()
	for _, name := range []string{
		"HOME", "USERPROFILE",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"APPDATA", "LOCALAPPDATA",
	} {
		t.Setenv(name, home)
	}

	w := &workspace{
		root:        root,
		home:        home,
		configPath:  filepath.Join(root, "config.json"),
		cacheRoot:   filepath.Join(root, "cache"),
		storageRoot: filepath.Join(root, "data", "mods"),
		gameDir:     filepath.Join(root, "darktide"),
		modsDir:     filepath.Join(root, "darktide", "mods"),
		localDir:    filepath.Join(root, "local"),
	}
	w.databasePath = filepath.Join(filepath.Dir(w.storageRoot), "mods.db")
	// The mods directory inside the game folder is deliberately not created
	// here. The detector accepts a folder holding a `mods` directory as a
	// Darktide installation, so creating it in every workspace would give every
	// test a game — and a test that believes it has no game while quietly having
	// one is worse than no test at all. makeGame creates it.
	for _, dir := range []string{w.cacheRoot, w.storageRoot, w.localDir} {
		require.NoError(t, os.MkdirAll(dir, 0o755), "prepare %s", dir)
	}

	return w
}

// args prepends the path flags, so a test reads as the command a person would
// type rather than as a wall of temporary paths.
func (w *workspace) args(rest ...string) []string {
	args := []string{
		"--config", w.configPath,
		"--cache", w.cacheRoot,
		"--storage", w.storageRoot,
	}

	return append(args, rest...)
}

// scrub replaces the machine-specific parts of a transcript with placeholders.
func (w *workspace) scrub(text string) string {
	// Longest first: the storage root is inside the tree, and replacing the
	// tree first would leave the more specific placeholder with nothing to
	// match. A stub server's base URL joins them because its port is assigned
	// by the operating system and changes on every run.
	pairs := []struct{ from, to string }{
		{w.storageRoot, "<storage>"},
		{w.databasePath, "<database>"},
		{w.gameDir, "<game>"},
		{w.modsDir, "<mods>"},
		{w.localDir, "<local>"},
		{w.cacheRoot, "<cache>"},
		{w.configPath, "<config>"},
		{w.root, "<work>"},
		{w.home, "<home>"},
	}
	for _, base := range w.stubBases {
		if base != "" {
			pairs = append(pairs, struct{ from, to string }{base, "<nexus>"})
		}
	}

	for _, pair := range pairs {
		text = strings.ReplaceAll(text, pair.from, pair.to)
	}
	// Column padding is squeezed to two spaces after the paths are gone.
	//
	// A table pads each column to its widest cell, and in this program the widest
	// cell is usually a path. The paths have just been replaced by placeholders
	// that are shorter than the values they stood for, so the padding left behind
	// is the padding the real value needed — and the real value is a temporary
	// directory whose name ends in a random number whose digit count changes from
	// run to run. Without this, a transcript recorded on one machine fails on the
	// next for no reason at all.
	//
	// Alignment is not lost by this. It is asserted directly in render_test.go,
	// where the values are fixed strings and the measurement is the thing under
	// test. Here it would be a measurement of a temporary directory name.
	return columnGap.ReplaceAllString(text, "  ")
}

// columnGap is a run of three or more spaces, which in aligned output is a cell
// boundary plus the padding that made the columns line up. A single space inside
// a value is untouched.
var columnGap = regexp.MustCompile(` {3,}`)

// writeConfig stores a settings file describing this workspace.
//
// The three network sources are switched off. They are the ones that would
// otherwise be queried for real: a test that reaches the internet is a test
// whose result depends on someone else's uptime, and one that authenticates
// against a real account is a test that can lock it out.
func (w *workspace) writeConfig(t *testing.T) {
	t.Helper()

	cfg := domain.DefaultConfig()
	cfg.ActiveGame = "darktide"
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	cfg.Cache.Path = w.cacheRoot
	cfg.Storage.Path = w.storageRoot
	cfg.Sources = map[string]domain.SourceConfig{
		"local":  {Enabled: true, Paths: []string{w.localDir}},
		"github": {Enabled: false},
		"nexus":  {Enabled: false},
		"vortex": {Enabled: false},
	}
	w.writeConfigWith(t, &cfg)
}

func (w *workspace) writeConfigWith(t *testing.T, cfg *domain.Config) {
	t.Helper()

	store := storage.NewConfigStore(w.configPath)
	require.NoError(t, store.Save(cfg), "write settings")
}

// credentialProbe is a stub of the one Nexus endpoint a credential check needs,
// together with the keys it was handed, the bodies it was sent and the answer it
// will give.
type credentialProbe struct {
	url    string
	body   string
	keys   []string
	bodies []string
	status int
	mu     sync.Mutex
}

// answer changes what the stub replies with next time. It exists so a
// transcript can record a rejected key as well as an accepted one without a
// second server.
func (p *credentialProbe) answer(status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.status, p.body = status, body
}

// seen returns the keys the stub was handed, in order.
func (p *credentialProbe) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.keys...)
}

// bodiesSeen returns the request bodies the stub was sent, in order.
func (p *credentialProbe) bodiesSeen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.bodies...)
}

// reject reports the complaint POST /mods/batch makes about body, or the empty
// string when the body is one the endpoint accepts.
//
// A stub that answers every request with the status its test chose cannot tell a
// correctly encoded request from an incorrectly encoded one: the credential probe
// used to hand already-encoded bytes to a client that encoded them again, every
// transcript below recorded "authenticated nexus", and the live API answered 422
// with a complaint about a body shape the plugin had never written. Mirroring the
// real endpoint is what turns that class of mistake into a failing transcript.
func reject(body []byte) (int, string) {
	var request struct {
		ModIDs []string `json:"mod_ids"`
	}

	detail := "Invalid JSON input. Require object with parameters as keys."
	if err := json.Unmarshal(body, &request); err == nil {
		detail = "#/components/schemas/ModsBatchRequest missing required parameters: mod_ids"

		if len(request.ModIDs) > 0 {
			return 0, ""
		}
	}

	return http.StatusUnprocessableEntity,
		`{"type":"about:blank","title":"Unprocessable Content","status":422,` +
			`"detail":"` + detail + `","instance":"/v3/mods/batch"}`
}

// newCredentialProbe starts a stub Nexus API answering the credential check
// with the given status and body, then writes a settings file in which the
// nexus source is enabled and pointed at it.
//
// Without this, a transcript about authenticating records whatever
// api.nexusmods.com happened to answer at the time, which is not a property of
// this program: the wording of an error changes without notice, an outage
// changes which path runs, and a machine with a working route records something
// different from one without. A transcript is only worth keeping while it is a
// record of the code under test. The same reasoning removed the accepted-link
// import from the mod-share transcript.
func (w *workspace) newCredentialProbe(t *testing.T, status int, body string) *credentialProbe {
	t.Helper()

	probe := &credentialProbe{status: status, body: body}
	server := httptest.NewServer(
		http.HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
			received, _ := io.ReadAll(r.Body)

			probe.mu.Lock()
			probe.keys = append(probe.keys, r.Header.Get("apikey"))
			probe.bodies = append(probe.bodies, string(received))
			status, body := probe.status, probe.body
			probe.mu.Unlock()

			if refusal, problem := reject(received); refusal != 0 {
				status, body = refusal, problem
			}

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		}),
	)
	t.Cleanup(server.Close)
	probe.url = server.URL
	w.stubBases = append(w.stubBases, server.URL)

	cfg := domain.DefaultConfig()
	cfg.ActiveGame = "darktide"
	cfg.UpdateActiveSettings(func(gs *domain.GameSettings) { gs.Path = w.gameDir })
	cfg.Cache.Path = w.cacheRoot
	cfg.Storage.Path = w.storageRoot
	cfg.Sources = map[string]domain.SourceConfig{
		"local":  {Enabled: true, Paths: []string{w.localDir}},
		"github": {Enabled: false},
		"nexus": {
			Enabled: true,
			Options: map[string]string{"api_base": server.URL},
		},
		"vortex": {Enabled: false},
	}
	w.writeConfigWith(t, &cfg)

	return probe
}

// makeGame builds a folder the detector accepts as a Darktide installation:
// the executable, a data directory and a mods directory.
func (w *workspace) makeGame(t *testing.T) {
	t.Helper()

	require.NoError(
		t,
		os.MkdirAll(filepath.Join(w.gameDir, "data"), 0o755),
		"create data directory",
	)

	require.NoError(t, os.MkdirAll(w.modsDir, 0o755), "create mods directory")

	require.NoError(t, os.WriteFile(
		filepath.Join(w.gameDir, "Darktide.exe"),
		[]byte("MZ"),
		0o644,
	), "write executable")

	require.NoError(t, os.WriteFile(
		filepath.Join(w.gameDir, "version.txt"),
		[]byte("1.0.3\n"),
		0o644,
	), "write version")
}

// writeModArchive writes a real zip into the local source's folder and returns
// the mod id the local source will derive from it.
//
// The file name carries a space before the version because that is what the
// local source looks for: "Better UI 1.2.0.zip" becomes the id "betterui",
// while "Better_UI_1.2.0.zip" would keep the version in the id and the id a
// test then has to carry everywhere.
func (w *workspace) writeModArchive(t *testing.T, name, version string) string {
	t.Helper()

	path := filepath.Join(w.localDir, fmt.Sprintf("%s %s.zip", name, version))
	writeZip(t, path, map[string]string{
		"mod.toml": fmt.Sprintf(
			"[mod]\nid = %q\nname = %q\nversion = %q\nauthor = %q\ndescription = %q\n",
			sanitizeID(name), name, version, "modder", "A mod called "+name+"."),
		"textures/hud.dds": "payload for " + name,
	})

	return sanitizeID(name)
}

func sanitizeID(name string) string {
	var out strings.Builder

	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out.WriteRune(r)
		}
	}

	return out.String()
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755), "create archive directory")

	var buf bytes.Buffer

	archive := zip.NewWriter(&buf)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		entry, err := archive.Create(name)
		require.NoError(t, err, "add %s to archive", name)

		_, err = entry.Write([]byte(files[name]))
		require.NoError(t, err, "write %s", name)
	}

	require.NoError(t, archive.Close(), "finish archive")

	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644), "write archive")
}

// golden records a command line and everything it printed, and compares the
// result with a stored transcript.
//
// The transcript format is a shell session: the command line, its output
// prefixed with "|", its diagnostics with "!", and the exit code at the end.
// That shape is not decoration — it makes a failure readable in a diff, because
// a golden test whose output is one undifferentiated blob cannot show which of
// four command lines changed.
type golden struct {
	t      *testing.T
	name   string
	stdin  string
	body   strings.Builder
	builds int
}

// newGolden starts a transcript. The name is the file under testdata.
func newGolden(t *testing.T, name string) *golden {
	return &golden{t: t, name: name}
}

// withStdin sets what the next command lines read from standard input.
func (g *golden) withStdin(text string) *golden {
	g.stdin = text
	return g
}

// run executes one command line against a workspace and records it.
func (g *golden) run(w *workspace, args ...string) {
	g.t.Helper()

	full := w.args(args...)

	var out, errOut bytes.Buffer

	built := 0
	env := Env{
		Out: &out,
		Err: &errOut,
		In:  strings.NewReader(g.stdin),
		NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
			built++
			return di.NewContainer(ctx, opts)
		},
	}
	code := RunContext(context.Background(), env, full)
	g.builds += built

	g.scrubbed(w, fmt.Sprintf("$ dmm %s", strings.Join(placeholders(w, full), " ")))
	g.stream(w, "out", out.String())
	g.stream(w, "err", errOut.String())
	fmt.Fprintf(&g.body, "! exit %d\n", code)
}

// stream records one output stream, skipping it when it is empty so a
// transcript is not padded with blank blocks.
//
// The text is scrubbed on the way in. Almost every command prints a path, and a
// transcript that recorded this run's temporary directories would fail on the
// next run for no reason at all — which teaches a reader to re-record
// transcripts without reading them, and then the transcripts stop being worth
// anything.
func (g *golden) stream(w *workspace, label, text string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return
	}

	for _, line := range strings.Split(w.scrub(text), "\n") {
		g.body.WriteString(string(label[0]) + " " + line + "\n")
	}
}

// scrubbed records an already-formatted line, with the workspace scrubbed.
func (g *golden) scrubbed(w *workspace, line string) {
	g.body.WriteString(w.scrub(line) + "\n")
}

// placeholders rewrites the path arguments of a command line for display.
func placeholders(w *workspace, args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, w.scrub(arg))
	}

	return out
}

// timestampPattern is duplicated here rather than reused from the production
// package on purpose: the test's normaliser and the program's formatter are
// independent implementations of the same idea, so a change to one that
// silently broke the other would show up as a failing transcript rather than
// as two agreeing mistakes.
var scrubTimestamp = regexp.MustCompile(
	`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`,
)

// transcript renders the whole session.
func (g *golden) transcript() string {
	return scrubTimestamp.ReplaceAllString(g.body.String(), "<time>")
}

// want compares the transcript with the stored one, or rewrites it under
// -update.
func (g *golden) want() {
	g.t.Helper()
	got := g.transcript()

	path := filepath.Join("testdata", g.name+".golden")
	if *updateGolden {
		require.NoError(g.t, os.MkdirAll(filepath.Dir(path), 0o755), "create testdata")

		require.NoError(g.t, os.WriteFile(path, []byte(got), 0o644), "write %s", path)

		g.t.Logf("wrote %s", path)

		return
	}

	want, err := os.ReadFile(path)
	require.NoError(
		g.t,
		err,
		"read %s: %v\nrun `go test ./cmd/cli/commands/ -update` to create it.\n"+
			"transcript so far:\n%s",
		path,
		err,
		got,
	)

	assert.Equal(
		g.t,
		string(want),
		got,
		"transcript differs from %s\n--- want ---\n%s\n--- got ---\n%s",
		path,
		string(want),
		got,
	)
}

// defaultConfigFor builds a configuration with nothing pointed anywhere, for a
// test that needs a settings file without a game or a cache in it.
func defaultConfigFor(t *testing.T) domain.Config {
	t.Helper()

	cfg := domain.DefaultConfig()
	cfg.ActiveGame = "darktide"

	return cfg
}
