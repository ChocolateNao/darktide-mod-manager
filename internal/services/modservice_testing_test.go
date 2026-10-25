package services

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games/gamestest"
	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/require"
)

// Test doubles for ModService.
// modNow pins every timestamp the service writes, so assertions can compare
// exact values instead of ranges.
var modNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func modClock() Clock { return func() time.Time { return modNow } }

// fakeIndex is an in-memory ModIndex. mods is the source of truth; every method
// that mutates records what it saw so a test can assert the service wrote what
// it meant to.
type fakeIndex struct {
	deleteErr      error
	countErr       error
	saveFilesErr   error
	setOrderErr    error
	saveErr        error
	getErr         error
	listErr        error
	listEnabledErr error
	filesErr       error
	loadOrderErr   error
	mods           map[string]*domain.Mod
	files          map[string][]domain.ModFile
	order          []string
	saved          []string
	saveFilesFor   []string
	enabledCalls   []bool
	priorityCalls  []int
	deleted        []string
	mu             sync.Mutex
	closed         bool
}

func newFakeIndex() *fakeIndex {
	return &fakeIndex{
		mods:  map[string]*domain.Mod{},
		files: map[string][]domain.ModFile{},
	}
}

func (f *fakeIndex) Save(_ context.Context, m *domain.Mod) error {
	if f.saveErr != nil {
		return f.saveErr
	}

	if m == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "nil mod")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.saved = append(f.saved, m.ID)
	f.mods[m.ID] = m.Clone()

	return nil
}

func (f *fakeIndex) GetByID(_ context.Context, id string) (*domain.Mod, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	m, ok := f.mods[id]
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound, "mod %q not installed", id)
	}

	return m.Clone(), nil
}

func (f *fakeIndex) List(_ context.Context) ([]domain.Mod, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}

	return f.snapshot(), nil
}

func (f *fakeIndex) ListEnabled(ctx context.Context) ([]domain.Mod, error) {
	if f.listEnabledErr != nil {
		return nil, f.listEnabledErr
	}

	all, err := f.List(ctx)
	if err != nil {
		return nil, err
	}

	out := []domain.Mod{}

	for _, m := range all {
		if m.Enabled {
			out = append(out, m)
		}
	}

	return out, nil
}

func (f *fakeIndex) ListByGame(ctx context.Context, gameID string) ([]domain.Mod, error) {
	all, err := f.List(ctx)
	if err != nil {
		return nil, err
	}

	out := []domain.Mod{}

	for _, m := range all {
		if m.GameID == gameID {
			out = append(out, m)
		}
	}

	return out, nil
}

func (f *fakeIndex) ListEnabledByGame(ctx context.Context, gameID string) ([]domain.Mod, error) {
	if f.listEnabledErr != nil {
		return nil, f.listEnabledErr
	}

	all, err := f.ListByGame(ctx, gameID)
	if err != nil {
		return nil, err
	}

	out := []domain.Mod{}

	for _, m := range all {
		if m.Enabled {
			out = append(out, m)
		}
	}

	return out, nil
}

func (f *fakeIndex) snapshot() []domain.Mod {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []domain.Mod{}
	for _, m := range f.mods {
		out = append(out, *m.Clone())
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}

func (f *fakeIndex) UpdateEnabled(_ context.Context, id string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.enabledCalls = append(f.enabledCalls, enabled)

	m, ok := f.mods[id]
	if !ok {
		return appErrors.New(appErrors.ErrCodeModNotFound, "mod %q not installed", id)
	}

	m.Enabled = enabled

	return nil
}

func (f *fakeIndex) UpdatePriority(_ context.Context, id string, priority int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.priorityCalls = append(f.priorityCalls, priority)

	m, ok := f.mods[id]
	if !ok {
		return appErrors.New(appErrors.ErrCodeModNotFound, "mod %q not installed", id)
	}

	m.Priority = priority

	return nil
}

func (f *fakeIndex) SaveFiles(_ context.Context, modID string, files []domain.ModFile) error {
	if f.saveFilesErr != nil {
		return f.saveFilesErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.saveFilesFor = append(f.saveFilesFor, modID)
	// The real repository stamps ModID from the argument; mirroring that keeps
	// the fake honest about what the service is expected to pass.
	copied := make([]domain.ModFile, len(files))
	for i, file := range files {
		copied[i] = file
		copied[i].ModID = modID
	}

	f.files[modID] = copied

	return nil
}

func (f *fakeIndex) GetFilesByMod(_ context.Context, modID string) ([]domain.ModFile, error) {
	if f.filesErr != nil {
		return nil, f.filesErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	files, ok := f.files[modID]
	if !ok {
		return []domain.ModFile{}, nil
	}

	out := make([]domain.ModFile, len(files))
	copy(out, files)

	return out, nil
}

func (f *fakeIndex) Delete(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, id)
	delete(f.mods, id)
	delete(f.files, id)

	return nil
}

func (f *fakeIndex) SetLoadOrder(_ context.Context, ids []string) error {
	if f.setOrderErr != nil {
		return f.setOrderErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.order = append([]string(nil), ids...)

	return nil
}

func (f *fakeIndex) LoadOrder(_ context.Context) ([]string, error) {
	if f.loadOrderErr != nil {
		return nil, f.loadOrderErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string{}, f.order...), nil
}

// SetLoadOrderForGame replaces the order of ONE game, leaving the other games'
// rows in the shared slice as the sqlite repository does.
func (f *fakeIndex) SetLoadOrderForGame(_ context.Context, gameID string, ids []string) error {
	if f.setOrderErr != nil {
		return f.setOrderErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, id := range ids {
		m, ok := f.mods[id]
		if !ok || m.GameID != gameID {
			return appErrors.New(
				appErrors.ErrCodeModNotFound,
				"load order names mod %q, which is not installed for game %q",
				id,
				gameID,
			)
		}
	}

	kept := make([]string, 0, len(f.order)+len(ids))
	for _, id := range f.order {
		if m, ok := f.mods[id]; ok && m.GameID == gameID {
			continue
		}

		kept = append(kept, id)
	}

	f.order = append(kept, ids...)

	return nil
}

// LoadOrderByGame filters the shared order down to one game's mods, in order.
func (f *fakeIndex) LoadOrderByGame(_ context.Context, gameID string) ([]string, error) {
	if f.loadOrderErr != nil {
		return nil, f.loadOrderErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	out := []string{}

	for _, id := range f.order {
		if m, ok := f.mods[id]; ok && m.GameID == gameID {
			out = append(out, id)
		}
	}

	return out, nil
}

func (f *fakeIndex) Count(_ context.Context) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}

	return len(f.snapshot()), nil
}

func (f *fakeIndex) Close() error {
	f.closed = true
	return nil
}

// put installs a mod directly, bypassing the service, to set up a starting state.
func (f *fakeIndex) put(m *domain.Mod) *domain.Mod {
	f.mu.Lock()
	defer f.mu.Unlock()

	c := m.Clone()
	if c.LocalPath != "" {
		f.files[c.ID] = []domain.ModFile{
			{ModID: c.ID, RelPath: "pack.dll", StoredPath: filepath.Join(c.LocalPath, "pack.dll")},
		}
	}

	f.mods[c.ID] = c

	return c
}

// putValue is put for a fixture literal, since Go cannot take the address of a
// function result.
func (f *fakeIndex) putValue(m domain.Mod) *domain.Mod { return f.put(&m) }

// enabledMap reads the Enabled flag out of the stored records, which is what
// "which mods are on after that operation" actually means.
func (f *fakeIndex) enabledMap() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make(map[string]bool, len(f.mods))
	for id, m := range f.mods {
		out[id] = m.Enabled
	}

	return out
}

// enabledIDs lists the enabled mods, sorted, so one equality check compares a
// whole state.
func (f *fakeIndex) enabledIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []string{}

	for id, m := range f.mods {
		if m.Enabled {
			out = append(out, id)
		}
	}

	sort.Strings(out)

	return out
}

// stored reports whether a mod is still recorded at all.
func (f *fakeIndex) stored(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	_, ok := f.mods[id]

	return ok
}

func (f *fakeIndex) setFiles(modID string, files []domain.ModFile) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.files[modID] = files
}

func (f *fakeIndex) enabledFlags() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]bool(nil), f.enabledCalls...)
}

// scriptSource is a domain.ModSource backed by a map, letting a test decide
// exactly which mods exist and which calls fail.
type scriptSource struct {
	mods     map[string]*domain.Mod
	getErr   map[string]error
	id       string
	getCalls []string
	mu       sync.Mutex
}

func (s *scriptSource) ID() string           { return s.id }
func (s *scriptSource) Name() string         { return "Script " + s.id }
func (s *scriptSource) Description() string  { return "scripted source" }
func (s *scriptSource) Version() string      { return "1.0.0" }
func (s *scriptSource) SupportsSearch() bool { return false }
func (s *scriptSource) SupportsAuth() bool   { return false }
func (s *scriptSource) RequiresAPIKey() bool { return false }

func (s *scriptSource) Search(context.Context, domain.SearchQuery) ([]domain.Mod, error) {
	return []domain.Mod{}, nil
}

func (s *scriptSource) GetMod(_ context.Context, modID string) (*domain.Mod, error) {
	s.mu.Lock()
	s.getCalls = append(s.getCalls, modID)
	s.mu.Unlock()

	if err, ok := s.getErr[modID]; ok {
		return nil, err
	}

	m, ok := s.mods[modID]
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound, "mod %q not on %s", modID, s.id)
	}

	return m.Clone(), nil
}

func (s *scriptSource) Download(context.Context, *domain.Mod, string) (string, error) {
	return "", appErrors.New(appErrors.ErrCodeDownloadFailed, "script source cannot download")
}

func (s *scriptSource) Authenticate(context.Context, domain.AuthCredentials) error { return nil }
func (s *scriptSource) ValidateCredentials(context.Context) error                  { return nil }

// failGet scripts (or, with a nil error, un-scripts) a GetMod failure. A nil
// error has to delete the entry: GetMod reads a present key as "fail", so a
// stored nil would look like "return (nil, nil)".
func (s *scriptSource) failGet(modID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err == nil {
		delete(s.getErr, modID)
		return
	}

	s.getErr[modID] = err
}

func (s *scriptSource) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.getCalls...)
}

// fakeSourceProvider hands out scripted sources and records which ids the
// service asked for.
type fakeSourceProvider struct {
	err     error
	sources map[string]domain.ModSource
	asked   []string
	mu      sync.Mutex
}

func newFakeSourceProvider() *fakeSourceProvider {
	return &fakeSourceProvider{sources: map[string]domain.ModSource{}}
}

func (p *fakeSourceProvider) add(id string, s domain.ModSource) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.sources[id] = s
}

func (p *fakeSourceProvider) Get(_ context.Context, id string) (domain.ModSource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.asked = append(p.asked, id)
	if p.err != nil {
		return nil, p.err
	}

	s, ok := p.sources[id]
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeSourceNotFound, "no source %q", id)
	}

	return s, nil
}

func (p *fakeSourceProvider) requested() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.asked...)
}

// fakeCache returns a pre-written archive for each mod it knows. The archive is a
// real zip so the real extractor is exercised, not a stub.
type fakeCache struct {
	getErr   error
	archives map[string]string
	keys     []string
	released []string
	removed  []string
	mu       sync.Mutex
}

func newFakeCache() *fakeCache { return &fakeCache{archives: map[string]string{}} }

func (c *fakeCache) GetOrDownload(_ context.Context, mod *domain.Mod) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.getErr != nil {
		return "", c.getErr
	}

	key, err := CacheKey(mod)
	if err != nil {
		return "", err
	}

	c.keys = append(c.keys, key)

	path, ok := c.archives[mod.ID]
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeDownloadFailed, "no archive for %q", mod.ID)
	}

	return path, nil
}

func (c *fakeCache) Release(_ context.Context, hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.released = append(c.released, hash)

	return nil
}

func (c *fakeCache) Remove(_ context.Context, hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.removed = append(c.removed, hash)

	return nil
}

func (c *fakeCache) keysRequested() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.keys...)
}

func (c *fakeCache) releasedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.released...)
}

func (c *fakeCache) removedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.removed...)
}

// fakeHooks records the phases the service executed and can be told to fail a
// specific one, so the Pre*/Post* asymmetry can be tested directly.
type fakeHooks struct {
	fail    map[domain.HookPhase]error
	block   chan struct{}
	onEnter func(domain.HookPhase)
	phases  []domain.HookPhase
	mu      sync.Mutex
}

func newFakeHooks() *fakeHooks { return &fakeHooks{fail: map[domain.HookPhase]error{}} }

func (h *fakeHooks) setFail(phase domain.HookPhase, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.fail[phase] = err
}

func (h *fakeHooks) ExecutePhase(
	ctx context.Context,
	phase domain.HookPhase,
	_ domain.ModHooks,
	mod *domain.Mod,
) error {
	if h.onEnter != nil {
		h.onEnter(phase)
	}

	if h.block != nil {
		select {
		case <-h.block:
		case <-ctx.Done():
			return canceledError(ctx.Err())
		}
	}

	h.mu.Lock()
	h.phases = append(h.phases, phase)
	err := h.fail[phase]
	h.mu.Unlock()

	if err != nil {
		return err
	}

	if mod != nil && mod.ID == "" {
		return appErrors.New(appErrors.ErrCodeInvalidMod, "hook got a mod with no id")
	}

	return nil
}

func (h *fakeHooks) executed() []domain.HookPhase {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]domain.HookPhase(nil), h.phases...)
}

func (h *fakeHooks) countOf(phase domain.HookPhase) int {
	n := 0

	for _, p := range h.executed() {
		if p == phase {
			n++
		}
	}

	return n
}

// fakeLocator stands in for GameLocator. modsPath == "" models "a game is
// configured but has no mods folder yet", which the service must treat as "no
// game", not as an error.
type fakeLocator struct {
	err               error
	install           *domain.GameInstallation
	modsPath          string
	gameID            string
	installMethod     string
	gameInstallMethod string
	loadOrderFile     string
	calls             int
	mu                sync.Mutex
}

// fakeGameDef is a GameDefinition whose id can be the empty string, which
// gamestest.Def refuses to represent; the harness keeps the legacy, not-yet
// assigned game id by default.
type fakeGameDef struct {
	gamestest.Def
	id            string
	installMethod string
	loadOrderFile string
}

func (d fakeGameDef) ID() string { return d.id }

// LoadOrderFile reports the file the test asked for, defaulting to no file.
func (d fakeGameDef) LoadOrderFile() string { return d.loadOrderFile }

// ModInstallMethod reports the preference the test asked for, falling back to
// the gamestest default when it asked for nothing.
func (d fakeGameDef) ModInstallMethod() string {
	if d.installMethod == "" {
		return gamestest.Def{}.ModInstallMethod()
	}

	return d.installMethod
}

// newFakeLocator reports a game whose folder lives under root, so the tests
// never write outside their own temp directory.
func newFakeLocator(root string) *fakeLocator {
	game := filepath.Join(root, "game")
	install := &domain.GameInstallation{
		Path:     game,
		Source:   domain.GameSourceSteam,
		ModsPath: filepath.Join(game, "mods"),
		Valid:    true,
	}

	return &fakeLocator{install: install, modsPath: install.ModsPath}
}

// withoutGame returns a locator that reports no installation at all.
func withoutGame() *fakeLocator { return &fakeLocator{} }

// withGameID makes Active report the given game id, so a test can exercise
// cross-game scoping without a registry.
func (l *fakeLocator) withGameID(id string) *fakeLocator {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.gameID = id

	return l
}

// withInstallMethod makes Active report a per-game configuration override, the
// setting that decides whether mods are linked or copied.
func (l *fakeLocator) withInstallMethod(method string) *fakeLocator {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.installMethod = method

	return l
}

// withGameInstallMethod makes the game definition itself prefer a method, as
// it would when the user has expressed no opinion in the configuration.
func (l *fakeLocator) withGameInstallMethod(method string) *fakeLocator {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.gameInstallMethod = method

	return l
}

// withLoadOrderFile makes Active report a game that switches mods through a
// load order file, which is what enables external mod detection.
func (l *fakeLocator) withLoadOrderFile(name string) *fakeLocator {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.loadOrderFile = name

	return l
}

// Active implements ModLocator. It reports the configured game id (the empty
// string by default) independently of installation errors: a missing
// installation is not a missing registry entry.
func (l *fakeLocator) Active() (domain.GameDefinition, domain.GameSettings, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	settings := domain.DefaultGameSettings()
	settings.InstallMethod = l.installMethod

	def := fakeGameDef{
		id:            l.gameID,
		installMethod: l.gameInstallMethod,
		loadOrderFile: l.loadOrderFile,
	}

	return def, settings, nil
}

func (l *fakeLocator) Current(context.Context) (*domain.GameInstallation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls++
	if l.err != nil {
		return nil, l.err
	}

	if l.install == nil {
		return nil, appErrors.New(appErrors.ErrCodeGameNotFound, "no game installed")
	}

	c := *l.install

	return &c, nil
}

func (l *fakeLocator) GetModsPath(*domain.GameInstallation) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.err != nil {
		return "", l.err
	}

	return l.modsPath, nil
}

func (l *fakeLocator) Installations(context.Context) ([]domain.GameInstallation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.err != nil {
		return nil, l.err
	}

	if l.install == nil {
		return nil, nil
	}

	install := *l.install
	install.GameID = l.gameID

	return []domain.GameInstallation{install}, nil
}

func (l *fakeLocator) currentCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.calls
}

// recordingDeployer models the game mods folder as a set of deployed names. It
// never touches the disk; the real deployer has its own suite.
type recordingDeployer struct {
	deployErr   error
	undeployErr error
	verifyErr   error
	repairErr   error
	deployed    map[string]string
	repaired    []string
	undeployed  [][]string
	deployCalls []string
	deployModes []platform.DeployMode
	verifyCalls []string
	mu          sync.Mutex
}

func newRecordingDeployer() *recordingDeployer {
	return &recordingDeployer{deployed: map[string]string{}}
}

func (d *recordingDeployer) Mode() platform.DeployMode { return platform.DeploySymlink }

// ModeFor mirrors the real rule for a host that can always create symlinks: a
// request for direct copies copies, everything else links.
func (d *recordingDeployer) ModeFor(preferred string) platform.DeployMode {
	if strings.TrimSpace(preferred) == domain.InstallMethodDirect {
		return platform.DeployCopy
	}

	return platform.DeploySymlink
}

func (d *recordingDeployer) DeployAs(src, _ string, mode platform.DeployMode) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.deployCalls = append(d.deployCalls, src)

	d.deployModes = append(d.deployModes, mode)
	if d.deployErr != nil {
		return d.deployErr
	}
	// The real deployer exposes one game-folder entry per top-level payload
	// entry, so read the folder, not inventing a scheme.
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, e := range entries {
		// The real deployer never exposes a manifest, so neither does this.
		if platform.IsManifestName(e.Name()) {
			continue
		}

		d.deployed[e.Name()] = src
	}

	return nil
}

func (d *recordingDeployer) UndeployEntries(_ string, names []string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.undeployed = append(d.undeployed, append([]string(nil), names...))
	if d.undeployErr != nil {
		return 0, d.undeployErr
	}

	n := 0

	for _, name := range names {
		if _, ok := d.deployed[name]; ok {
			delete(d.deployed, name)

			n++
		}
	}

	return n, nil
}

func (d *recordingDeployer) VerifyFolder(_ string) (map[string]platform.LinkState, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.verifyCalls = append(d.verifyCalls, "")
	if d.verifyErr != nil {
		return nil, 0, d.verifyErr
	}

	out := map[string]platform.LinkState{}
	total := 0

	for name := range d.deployed {
		out[name] = platform.LinkOK
		total++
	}

	return out, total, nil
}

func (d *recordingDeployer) Repair(src, _ string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.repairErr != nil {
		return nil, d.repairErr
	}

	d.repaired = append(d.repaired, src)
	for name := range d.deployed {
		if d.deployed[name] == src {
			d.deployed[name] = src
		}
	}

	return nil, nil
}

func (d *recordingDeployer) Owns(_ string, name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, ok := d.deployed[name]

	return ok
}

func (d *recordingDeployer) deployedSources() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]string, 0, len(d.deployed))
	for _, src := range d.deployed {
		out = append(out, src)
	}

	sort.Strings(out)

	return out
}

func (d *recordingDeployer) repairCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.repaired...)
}

func (d *recordingDeployer) undeployCalls() [][]string {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([][]string, len(d.undeployed))
	for i, names := range d.undeployed {
		out[i] = append([]string(nil), names...)
	}

	return out
}

func (d *recordingDeployer) deployTargets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.deployCalls...)
}

// staticConfig is a ConfigStore that always returns the same config, so a test
// can control storage.path and the hook/backup settings without a file.
type staticConfig struct {
	cfg *domain.Config

	saveErr   error
	loadErr   error
	loadCalls int
	saves     int
}

func newStaticConfig(cfg *domain.Config) *staticConfig {
	if cfg == nil {
		c := domain.DefaultConfig()
		cfg = &c
	}

	return &staticConfig{cfg: cfg}
}

func (s *staticConfig) Path() string { return "/config/config.json" }

func (s *staticConfig) Load() (*domain.Config, error) {
	s.loadCalls++
	if s.loadErr != nil {
		return nil, s.loadErr
	}

	return s.cfg.Clone(), nil
}

func (s *staticConfig) Save(cfg *domain.Config) error {
	if s.saveErr != nil {
		return s.saveErr
	}

	s.saves++
	s.cfg = cfg.Clone()

	return nil
}

func (s *staticConfig) SaveIfChanged(cfg *domain.Config) (bool, error) {
	if err := s.Save(cfg); err != nil {
		return false, err
	}

	return true, nil
}

func (s *staticConfig) saveCount() int { return s.saves }

type modEvents struct {
	events []Event
	mu     sync.Mutex
}

func (e *modEvents) Publish(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.events = append(e.events, ev)
}

func (e *modEvents) types() []EventType {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]EventType, len(e.events))
	for i, ev := range e.events {
		out[i] = ev.Type
	}

	return out
}

func (e *modEvents) forMod(id string) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()

	var out []Event

	for _, ev := range e.events {
		if ev.ModID == id {
			out = append(out, ev)
		}
	}

	return out
}

func (e *modEvents) countOf(t EventType) int {
	n := 0

	for _, got := range e.types() {
		if got == t {
			n++
		}
	}

	return n
}

// writeZipArchive builds a real zip at path with the given entries: a broken
// extractor stub would hide exactly the bugs these tests exist to catch.
func writeZipArchive(t *testing.T, path string, names ...string) string {
	t.Helper()

	{
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		require.NoError(t, err, "create archive dir: %v", err)
	}

	f, err := os.Create(path)
	require.NoError(t, err, "create archive")

	defer f.Close()

	zw := zip.NewWriter(f)
	for _, name := range names {
		w, err := zw.Create(name)
		require.NoError(t, err, "add %s: %v", name)

		{
			_, err := fmt.Fprintf(w, "payload of %s\n", filepath.Base(name))
			require.NoError(t, err, "write %s: %v", name, err)
		}
	}

	{
		err := zw.Close()
		require.NoError(t, err, "close archive: %v", err)
	}

	return path
}

// modFixture is the standard mod the tests install.
func modFixture(id, version string) *domain.Mod {
	m := &domain.Mod{
		ID:       id,
		Name:     strings.ReplaceAll(id, "_", " "),
		Version:  version,
		SourceID: "vortex",
		Enabled:  true,
		SourceData: map[string]any{
			"download_url": "https://example.invalid/" + id + "-" + version + ".zip",
		},
	}
	// The service deliberately skips a phase with no scripts, so a fixture
	// without hooks would make every hook test pass for the wrong reason.
	for _, phase := range domain.HookPhases() {
		m.Hooks.Set(
			phase,
			[]domain.HookScript{{Language: domain.LangJS, Content: "// " + string(phase)}},
		)
	}

	return m
}

// bareMod is modFixture without hooks, for the tests that must not trigger any.
func bareMod(id, version string) *domain.Mod {
	m := modFixture(id, version)
	m.Hooks = domain.ModHooks{}

	return m
}

// harness bundles a wired ModService with the fakes behind it, so a test can
// reach both the service and the recorded calls.
type harness struct {
	svc      *ModService
	index    *fakeIndex
	sources  *fakeSourceProvider
	source   *scriptSource
	cache    *fakeCache
	hooks    *fakeHooks
	locator  *fakeLocator
	deployer *recordingDeployer
	events   *modEvents
	config   *staticConfig
	storage  string
	archives string
}

type harnessOpts struct {
	locator *fakeLocator
	config  *staticConfig
	entries []string
}

func newHarness(t *testing.T, opts ...func(*harnessOpts)) *harness {
	t.Helper()
	root := t.TempDir()

	o := harnessOpts{
		locator: newFakeLocator(root),
		entries: []string{"better_ui.dll", "better_ui.json"},
	}
	for _, fn := range opts {
		fn(&o)
	}

	h := &harness{
		index:   newFakeIndex(),
		sources: newFakeSourceProvider(),
		source: &scriptSource{
			id:     "vortex",
			mods:   map[string]*domain.Mod{},
			getErr: map[string]error{},
		},
		cache:    newFakeCache(),
		hooks:    newFakeHooks(),
		locator:  o.locator,
		deployer: newRecordingDeployer(),
		events:   &modEvents{},
		config:   o.config,
		storage:  filepath.Join(root, "storage"),
		archives: filepath.Join(root, "archives"),
	}
	if h.config == nil {
		cfg := domain.DefaultConfig()
		cfg.Storage.Path = h.storage
		h.config = newStaticConfig(&cfg)
	}

	{
		err := os.MkdirAll(h.storage, 0o755)
		require.NoError(t, err, "create storage: %v", err)
	}

	if o.locator.modsPath != "" {
		{
			err := os.MkdirAll(o.locator.modsPath, 0o755)
			require.NoError(t, err, "create game mods folder: %v", err)
		}
	}

	h.sources.add("vortex", h.source)

	svc, err := NewModService(ModServiceDeps{
		Sources:     h.sources,
		Cache:       h.cache,
		Index:       h.index,
		Hooks:       h.hooks,
		Locator:     h.locator,
		Deployer:    h.deployer,
		Extractor:   storage.NewArchiveExtractor(),
		Config:      h.config,
		Events:      h.events,
		Clock:       modClock(),
		StoragePath: h.storage,
	})
	require.NoError(t, err, "build service")

	h.svc = svc

	return h
}

// withArchive makes the scripted source able to serve mod and gives the cache a
// real archive for it.
func (h *harness) withArchive(t *testing.T, mod *domain.Mod, entries ...string) *harness {
	t.Helper()

	h.source.mods[mod.ID] = mod.Clone()
	if len(entries) == 0 {
		entries = []string{mod.ID + ".dll"}
	}

	h.cache.archives[mod.ID] = writeZipArchive(
		t,
		filepath.Join(h.archives, mod.ID+".zip"),
		entries...)

	return h
}

// installed returns the stored mod, failing the test when it is missing.
func (h *harness) installed(t *testing.T, id string) *domain.Mod {
	t.Helper()

	m, err := h.index.GetByID(context.Background(), id)
	require.NoError(t, err, "mod %q should be installed: %v", id)

	return m
}

func (h *harness) payloadPath(id, version string) string {
	return domain.ModStoragePath(h.storage, "", id, version)
}

// payloadFiles lists the files that ended up in the mod's payload folder.
func (h *harness) payloadFiles(id, version string) []string {
	root := h.payloadPath(id, version)

	var out []string

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		out = append(out, filepath.ToSlash(rel))

		return nil
	})

	sort.Strings(out)

	return out
}

// newModServiceOrFail builds a service for tests that need only one helper.
func newModServiceOrFail(t *testing.T, deps ModServiceDeps) *ModService {
	t.Helper()

	svc, err := NewModService(deps)
	require.NoError(t, err, "build service")

	return svc
}

func hasEvent(types []EventType, want EventType) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}

	return false
}

func hasPhase(phases []domain.HookPhase, want domain.HookPhase) bool {
	for _, p := range phases {
		if p == want {
			return true
		}
	}

	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

var errBoom = errors.New("boom")

// setOrder seeds an explicit load order without going through the service.
func (f *fakeIndex) setOrder(ids []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.order = append([]string(nil), ids...)
}
