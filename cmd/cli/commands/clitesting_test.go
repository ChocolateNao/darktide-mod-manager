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

var updateGolden = flag.Bool(
	"update",
	false,
	"rewrite the golden transcripts instead of comparing them",
)

// workspace is a private manager installation, sharing nothing with the machine
// or between tests.
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
	// The mods directory is deliberately not created: the detector accepts a
	// folder holding one, so creating it would give every test a game.
	for _, dir := range []string{w.cacheRoot, w.storageRoot, w.localDir} {
		require.NoError(t, os.MkdirAll(dir, 0o755), "prepare %s", dir)
	}

	return w
}

// args prepends the path flags so a test reads as the command a person would
// type.
func (w *workspace) args(rest ...string) []string {
	args := []string{
		"--config", w.configPath,
		"--cache", w.cacheRoot,
		"--storage", w.storageRoot,
	}

	return append(args, rest...)
}

func (w *workspace) scrub(text string) string {
	// Longest first: the storage root is inside the tree, and replacing the
	// tree first would leave the more specific placeholder with nothing to match.
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
	// Padding is squeezed to two spaces once the paths are placeholders: the real
	// value was a temp directory whose random suffix changes digit count per run.
	return columnGap.ReplaceAllString(text, "  ")
}

var columnGap = regexp.MustCompile(` {3,}`)

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

// credentialProbe is a stub of the one Nexus endpoint a credential check needs.
type credentialProbe struct {
	url    string
	body   string
	keys   []string
	bodies []string
	status int
	mu     sync.Mutex
}

func (p *credentialProbe) answer(status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.status, p.body = status, body
}

func (p *credentialProbe) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.keys...)
}

func (p *credentialProbe) bodiesSeen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.bodies...)
}

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

// writeModArchive writes a real zip into the local source folder. The name
// carries a space because that is what the local source parses.
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

// golden records a command line and what it printed, so a failure is readable in a
// diff.
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

func (g *golden) withStdin(text string) *golden {
	g.stdin = text
	return g
}

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

// stream records one output stream, skipping it when empty.
func (g *golden) stream(w *workspace, label, text string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return
	}

	for _, line := range strings.Split(w.scrub(text), "\n") {
		g.body.WriteString(string(label[0]) + " " + line + "\n")
	}
}

func (g *golden) scrubbed(w *workspace, line string) {
	g.body.WriteString(w.scrub(line) + "\n")
}

func placeholders(w *workspace, args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, w.scrub(arg))
	}

	return out
}

// scrubTimestamp is duplicated from production on purpose: breaking one shows up
// as a failing transcript.
var scrubTimestamp = regexp.MustCompile(
	`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`,
)

func (g *golden) transcript() string {
	return scrubTimestamp.ReplaceAllString(g.body.String(), "<time>")
}

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

func defaultConfigFor(t *testing.T) domain.Config {
	t.Helper()

	cfg := domain.DefaultConfig()
	cfg.ActiveGame = "darktide"

	return cfg
}
