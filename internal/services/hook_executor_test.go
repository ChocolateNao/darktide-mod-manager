package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingRunner is a HookRunner that records what it was handed and returns a
// canned result. It also implements LanguageTyper so it can go through Register.
type recordingRunner struct {
	err      error
	block    chan struct{}
	observe  func(ctx context.Context, hook PreparedHook)
	language domain.ScriptLanguage
	calls    []servicesCall
	mu       sync.Mutex
	waitCtx  bool
}

type servicesCall struct {
	WorkingDir string
	Phase      domain.HookPhase
	Env        []string
	Context    domain.HookContext
	Index      int
}

func (r *recordingRunner) Language() domain.ScriptLanguage { return r.language }

func (r *recordingRunner) Run(ctx context.Context, hook PreparedHook, mod *domain.Mod) error {
	r.mu.Lock()
	r.calls = append(r.calls, servicesCall{
		WorkingDir: hook.WorkingDir,
		Env:        append([]string{}, hook.Env...),
		Phase:      hook.Context.Phase,
		Index:      hook.Context.Index,
		Context:    hook.Context,
	})
	observe := r.observe
	block := r.block
	waitCtx := r.waitCtx
	err := r.err
	r.mu.Unlock()

	if observe != nil {
		observe(ctx, hook)
	}

	if block != nil {
		<-block
	}

	if waitCtx {
		<-ctx.Done()
		return ctx.Err()
	}

	return err
}

func (r *recordingRunner) snapshot() []servicesCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]servicesCall{}, r.calls...)
}

// untypedRunner implements HookRunner but not LanguageTyper, so Register must
// refuse it.
type untypedRunner struct{}

func (untypedRunner) Run(context.Context, PreparedHook, *domain.Mod) error { return nil }

type collectingEvents struct {
	events []Event
	mu     sync.Mutex
}

func (c *collectingEvents) Publish(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.events = append(c.events, ev)
}

func (c *collectingEvents) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]Event{}, c.events...)
}

func (c *collectingEvents) types() []EventType {
	var out []EventType
	for _, ev := range c.snapshot() {
		out = append(out, ev.Type)
	}

	return out
}

func hookMod(dir string) *domain.Mod {
	return &domain.Mod{
		ID:        "better_ui",
		Name:      "Better UI",
		Version:   "1.2.0",
		SourceID:  "vortex",
		LocalPath: dir,
		Enabled:   true,
	}
}

// newGameDirs returns (gameRoot, modRoot). They are siblings under one temp dir
// so an "outside both" path is easy to construct and still absolute.
func newGameDirs(t *testing.T) (game, modDir string) {
	t.Helper()
	base := t.TempDir()
	game = filepath.Join(base, "game")

	modDir = filepath.Join(base, "storage", "mods", "better_ui", "1.2.0")
	for _, d := range []string{game, modDir} {
		{
			err := os.MkdirAll(d, 0o755)
			require.NoError(t, err, "create %s: %v", d, err)
		}
	}

	return game, modDir
}

func newExecutor(t *testing.T, mods ...HookExecutorDeps) *HookExecutor {
	t.Helper()

	if len(mods) == 0 {
		mods = []HookExecutorDeps{{}}
	}

	return NewHookExecutor(mods[0])
}

func envValue(env []string, name string) (string, bool) {
	for _, entry := range env {
		k, v, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(k, name) {
			return v, true
		}
	}

	return "", false
}

func TestNewHookExecutorAppliesDefaultTimeout(t *testing.T) {
	h := NewHookExecutor(HookExecutorDeps{})
	assert.Equal(
		t,
		domain.DurationOf(10*time.Second),
		h.Config().DefaultTimeout,
		"default timeout = %s, want 10s",
		h.Config().DefaultTimeout,
	)

	assert.Equal(
		t,
		domain.CurrentPlatform(),
		h.Platform(),
		"platform = %q, want %q",
		h.Platform(),
		domain.CurrentPlatform(),
	)
}

func TestNewHookExecutorKeepsConfiguredTimeout(t *testing.T) {
	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(5 * time.Second),
				MaxTimeout:     domain.DurationOf(120 * time.Second),
			},
		},
	)
	assert.False(
		t,
		h.Config().DefaultTimeout != domain.DurationOf(5*time.Second) ||
			h.Config().MaxTimeout != domain.DurationOf(120*time.Second),
		"config = %+v, want defaults 5 max 120",
		h.Config(),
	)
}

// A ceiling below the default would make every hook time out at the ceiling as
// soon as a per-hook value was clamped up to it, so the ceiling is raised.
func TestNewHookExecutorRaisesMaxBelowDefault(t *testing.T) {
	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(90 * time.Second),
				MaxTimeout:     domain.DurationOf(10 * time.Second),
			},
		},
	)
	{
		got := h.Config().MaxTimeout
		assert.Equal(
			t,
			domain.DurationOf(90*time.Second),
			got,
			"max timeout = %d, want it raised to the default 90",
			got,
		)
	}

	{
		got := h.Timeout(domain.HookScript{}, nil)
		assert.Equal(t, 90*time.Second, got, "effective timeout = %s, want 90s", got)
	}
}

func TestNewHookExecutorHonoursEmptyPlatform(t *testing.T) {
	h := NewHookExecutor(HookExecutorDeps{Platform: "not-a-platform"})
	assert.Equal(
		t,
		domain.Platform("not-a-platform"),
		h.Platform(),
		"explicit platform overwritten: %q",
		h.Platform(),
	)
}

func TestNewHookExecutorNilLookPathIsReplaced(t *testing.T) {
	// A nil LookPath must not panic on first use; GetShell/ResolveScriptRunner
	// fall back to os/exec.LookPath themselves.
	h := NewHookExecutor(HookExecutorDeps{})
	require.NotNil(t, h.lookPath, "lookPath left nil")
}

func TestTimeoutUsesDefaultWhenUnset(t *testing.T) {
	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(7 * time.Second),
				MaxTimeout:     domain.DurationOf(600 * time.Second),
			},
		},
	)
	{
		got := h.Timeout(domain.HookScript{Timeout: 0}, nil)
		assert.Equal(t, 7*time.Second, got, "timeout = %s, want 7s", got)
	}
}

// A declared timeout above the floor and within the ceiling is honoured as-is.
func TestTimeoutHonoursPerHookOverride(t *testing.T) {
	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(5 * time.Second),
				MaxTimeout:     domain.DurationOf(600 * time.Second),
			},
		},
	)
	{
		got := h.Timeout(domain.HookScript{Timeout: 10}, nil)
		assert.Equal(t, 10*time.Second, got, "timeout = %s, want 10s", got)
	}
}

// The default is a floor: a hook asking for less is raised to it.
func TestTimeoutRaisesShorterThanDefaultToFloor(t *testing.T) {
	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(30 * time.Second),
				MaxTimeout:     domain.DurationOf(600 * time.Second),
			},
		},
	)
	{
		got := h.Timeout(domain.HookScript{Timeout: 1}, nil)
		assert.Equal(t, 30*time.Second, got, "timeout = %s, want the 30s floor", got)
	}
}

// A permission cap below the floor still wins: the ceiling is the effective
// upper bound in every case.
func TestTimeoutCapOverridesLargerDeclaredValue(t *testing.T) {
	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(10 * time.Second),
				MaxTimeout:     domain.DurationOf(30 * time.Second),
				Permissions: domain.HooksPermissionsConfig{
					Mods: map[string]domain.ScriptPermissions{
						"better_ui": {TimeoutSeconds: 2},
					},
				},
			},
		},
	)
	{
		got := h.Timeout(domain.HookScript{Timeout: 15}, hookMod(t.TempDir()))
		assert.Equal(t, 2*time.Second, got, "timeout = %s, want the 2s cap", got)
	}

	{
		got := h.Timeout(domain.HookScript{}, hookMod(t.TempDir()))
		assert.Equal(t, 2*time.Second, got, "unset script = %s, want the 2s cap", got)
	}
}

func TestTimeoutClampsAboveMax(t *testing.T) {
	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(30 * time.Second),
				MaxTimeout:     domain.DurationOf(60 * time.Second),
			},
		},
	)
	{
		got := h.Timeout(domain.HookScript{Timeout: 9999}, nil)
		assert.Equal(t, 60*time.Second, got, "timeout = %s, want the 60s ceiling", got)
	}
}

// Even a hand-edited config cannot raise the ceiling past the compile-time
// maximum; a manifest-supplied timeout must not be able to buy unbounded runtime.
func TestTimeoutClampsPastHardCeiling(t *testing.T) {
	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(30 * time.Second),
				MaxTimeout:     domain.DurationOf(100000 * time.Second),
			},
		},
	)
	{
		got := h.Timeout(domain.HookScript{Timeout: 100000}, nil)
		assert.Equal(
			t,
			maxHookTimeout,
			got,
			"timeout = %s, want the %s hard ceiling",
			got,
			maxHookTimeout,
		)
	}

	{
		got := h.Timeout(domain.HookScript{Timeout: 0}, nil)
		assert.Equal(t, 30*time.Second, got, "default timeout = %s, want 30s", got)
	}
}

func TestValidateRejectsUnknownType(t *testing.T) {
	_, modDir := newGameDirs(t)
	h := newExecutor(t)

	err := h.Validate(domain.HookScript{Language: "powershell", Content: "x"}, hookMod(modDir))
	require.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v (err %v)",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
		err,
	)
}

func TestValidateRejectsBlankContent(t *testing.T) {
	_, modDir := newGameDirs(t)

	h := newExecutor(t)
	for _, content := range []string{"", "   ", "\n\t "} {
		err := h.Validate(
			domain.HookScript{Language: domain.LangLua, Content: content},
			hookMod(modDir),
		)
		assert.Equal(
			t,
			appErrors.ErrCodeHookInvalid,
			appErrors.CodeOf(err),
			"content %q: code = %q, want %v",
			content,
			appErrors.CodeOf(err),
			appErrors.ErrCodeHookInvalid,
		)
	}
}

func TestValidateRejectsNegativeTimeout(t *testing.T) {
	_, modDir := newGameDirs(t)
	h := newExecutor(t)

	err := h.Validate(
		domain.HookScript{Language: domain.LangLua, Content: "echo hi", Timeout: -1},
		hookMod(modDir),
	)
	assert.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
	)
}

func TestValidateRejectsMalformedEnv(t *testing.T) {
	_, modDir := newGameDirs(t)
	h := newExecutor(t)

	for _, entry := range []string{"", "  ", "NOEQUALS", "=value"} {
		script := domain.HookScript{
			Language: domain.LangLua,
			Content:  "echo hi",
			Env:      []string{entry},
		}

		err := h.Validate(script, hookMod(modDir))
		assert.Equal(
			t,
			appErrors.ErrCodeHookInvalid,
			appErrors.CodeOf(err),
			"env %q: code = %q, want %v",
			entry,
			appErrors.CodeOf(err),
			appErrors.ErrCodeHookInvalid,
		)
	}
}

func TestValidateAcceptsEnvValueContainingEquals(t *testing.T) {
	_, modDir := newGameDirs(t)
	h := newExecutor(t)

	script := domain.HookScript{
		Language: domain.LangLua,
		Content:  "echo hi",
		Env:      []string{"TOKEN=a=b=c"},
	}
	{
		err := h.Validate(script, hookMod(modDir))
		assert.NoError(t, err, "validate: %v", err)
	}
}

func TestValidateAcceptsEmptyEnvEntryInList(t *testing.T) {
	_, modDir := newGameDirs(t)
	h := newExecutor(t)

	script := domain.HookScript{
		Language: domain.LangLua,
		Content:  "echo hi",
		Env:      []string{"A=1", "", "B=2"},
	}
	{
		err := h.Validate(
			script,
			hookMod(modDir),
		)
		assert.Equal(t, appErrors.ErrCodeHookInvalid, appErrors.CodeOf(
			err,
		), "code = %q, want %v", appErrors.CodeOf(err), appErrors.ErrCodeHookInvalid)
	}
}

func TestValidateNilModIsFineForCommand(t *testing.T) {
	h := newExecutor(t)
	// A command hook with no mod folder and no game path has nothing to validate
	// against, but it is still a legal hook (a global maintenance script).
	{
		err := h.Validate(
			domain.HookScript{Language: domain.LangLua, Content: "echo hi"},
			nil,
		)
		assert.NoError(t, err, "validate with nil mod: %v", err)
	}
}

func TestWorkingDirDefaultsToModFolder(t *testing.T) {
	game, modDir := newGameDirs(t)
	h := NewHookExecutor(HookExecutorDeps{GamePath: func() string { return game }})

	got, err := h.WorkingDir(domain.HookScript{}, hookMod(modDir))
	require.NoError(t, err, "working dir")

	assert.Equal(t, modDir, got, "working dir = %q, want the mod folder %q", got, modDir)
}

func TestWorkingDirFallsBackToGameFolder(t *testing.T) {
	game, _ := newGameDirs(t)
	h := NewHookExecutor(HookExecutorDeps{GamePath: func() string { return game }})
	mod := &domain.Mod{ID: "x"}

	got, err := h.WorkingDir(domain.HookScript{}, mod)
	require.NoError(t, err, "working dir")

	assert.Equal(t, game, got, "working dir = %q, want the game folder %q", got, game)
}

// Neither the mod folder nor the game folder exists on disk, so there is nothing
// safe to chdir into and the process working directory is inherited.
func TestWorkingDirFallsBackToProcessDir(t *testing.T) {
	base := t.TempDir()
	h := newExecutor(t)

	got, err := h.WorkingDir(
		domain.HookScript{},
		&domain.Mod{ID: "x", LocalPath: filepath.Join(base, "missing")},
	)
	require.NoError(t, err, "working dir")

	assert.Empty(t, got, "working dir = %q, want empty (inherit)", got)
}

func TestWorkingDirEmptyWhenNoRootsAtAll(t *testing.T) {
	h := newExecutor(t)

	got, err := h.WorkingDir(domain.HookScript{}, nil)
	require.NoError(t, err, "working dir")

	assert.Empty(t, got, "working dir = %q, want empty", got)
}

func TestWorkingDirResolvesRelativeToMod(t *testing.T) {
	game, modDir := newGameDirs(t)
	{
		err := os.MkdirAll(filepath.Join(modDir, "sub"), 0o755)
		require.NoError(t, err)
	}

	h := NewHookExecutor(HookExecutorDeps{GamePath: func() string { return game }})

	got, err := h.WorkingDir(domain.HookScript{WorkingDir: "sub"}, hookMod(modDir))
	require.NoError(t, err, "working dir")

	assert.Equal(
		t,
		filepath.Join(modDir, "sub"),
		got,
		"working dir = %q, want %q",
		got,
		filepath.Join(modDir, "sub"),
	)
}

func TestWorkingDirResolvesRelativeToGameWhenModHasNoFolder(t *testing.T) {
	game, _ := newGameDirs(t)
	{
		err := os.MkdirAll(filepath.Join(game, "mods"), 0o755)
		require.NoError(t, err)
	}

	h := NewHookExecutor(HookExecutorDeps{GamePath: func() string { return game }})

	got, err := h.WorkingDir(domain.HookScript{WorkingDir: "mods"}, &domain.Mod{ID: "x"})
	require.NoError(t, err, "working dir")

	assert.Equal(
		t,
		filepath.Join(game, "mods"),
		got,
		"working dir = %q, want %q",
		got,
		filepath.Join(game, "mods"),
	)
}

// A hook that names its own working directory must not be able to leave the mod
// and game folders: this is the boundary the whole containment rule exists for.
func TestWorkingDirRejectsEscapeViaDotDot(t *testing.T) {
	game, modDir := newGameDirs(t)
	h := NewHookExecutor(HookExecutorDeps{GamePath: func() string { return game }})

	_, err := h.WorkingDir(domain.HookScript{WorkingDir: "../../../../etc"}, hookMod(modDir))
	require.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v (err %v)",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
		err,
	)
}

func TestWorkingDirRejectsAbsoluteOutsidePath(t *testing.T) {
	_, modDir := newGameDirs(t)
	outside := t.TempDir()
	h := newExecutor(t)

	_, err := h.WorkingDir(domain.HookScript{WorkingDir: outside}, hookMod(modDir))
	assert.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
	)
}

// A sibling directory sharing a name prefix with the mod folder must not pass:
// "/root/mods/a-evil" is not inside "/root/mods/a".
func TestWorkingDirRejectsSiblingWithSharedPrefix(t *testing.T) {
	base := t.TempDir()
	modDir := filepath.Join(base, "mod")

	evil := filepath.Join(base, "mod-evil")
	for _, d := range []string{modDir, evil} {
		{
			err := os.MkdirAll(d, 0o755)
			require.NoError(t, err)
		}
	}

	h := newExecutor(t)

	_, err := h.WorkingDir(
		domain.HookScript{WorkingDir: evil},
		&domain.Mod{ID: "x", LocalPath: modDir},
	)
	assert.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
	)
}

func TestWorkingDirRejectsMissingDirectory(t *testing.T) {
	_, modDir := newGameDirs(t)
	h := newExecutor(t)

	_, err := h.WorkingDir(domain.HookScript{WorkingDir: "nope"}, hookMod(modDir))
	assert.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
	)
}

func TestWorkingDirRejectsFileInsteadOfDirectory(t *testing.T) {
	_, modDir := newGameDirs(t)
	{
		err := os.WriteFile(filepath.Join(modDir, "file.txt"), []byte("x"), 0o644)
		require.NoError(t, err)
	}

	h := newExecutor(t)

	_, err := h.WorkingDir(domain.HookScript{WorkingDir: "file.txt"}, hookMod(modDir))
	assert.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
	)
}

func TestWorkingDirCannotResolveWithoutAnyRoot(t *testing.T) {
	h := newExecutor(t)

	_, err := h.WorkingDir(domain.HookScript{WorkingDir: "sub"}, nil)
	assert.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
	)
}

func TestEnvForwardsOnlyAllowlistedVariables(t *testing.T) {
	t.Setenv("DMM_ALLOWED_FIXTURE", "yes")
	t.Setenv("DMM_SECRET_TOKEN", "hunter2")

	h := newExecutor(t)

	env, err := h.Env(domain.HookScript{})
	require.NoError(t, err, "env")

	{
		v, ok := envValue(env, "DMM_ALLOWED_FIXTURE")
		assert.False(t, ok, "undeclared variable leaked: %q", v)
	}

	{
		_, ok := envValue(env, "DMM_SECRET_TOKEN")
		assert.False(t, ok, "secret leaked into the hook environment")
	}
}

func TestEnvIncludesRealAllowlistedVariables(t *testing.T) {
	t.Setenv("DMM_HOME_FIXTURE", "x")
	// PATH is always set in a test process; assert the mechanism, not the
	// exact contents of the ambient environment.
	h := newExecutor(t)

	env, err := h.Env(domain.HookScript{})
	require.NoError(t, err, "env")

	{
		_, ok := envValue(env, "PATH")
		assert.True(
			t,
			ok,
			"PATH missing from the hook environment; a shell hook would not resolve its interpreter",
		)
	}
}

func TestEnvAddsScriptDeclarations(t *testing.T) {
	h := newExecutor(t)

	env, err := h.Env(domain.HookScript{Env: []string{"MOD_DIR=/mods", "TOKEN=abc"}})
	require.NoError(t, err, "env")

	{
		v, _ := envValue(env, "MOD_DIR")
		assert.Equal(t, "/mods", v, "MOD_DIR = %q, want /mods", v)
	}

	{
		v, _ := envValue(env, "TOKEN")
		assert.Equal(t, "abc", v, "TOKEN = %q, want abc", v)
	}
}

// A later declaration replaces the inherited one, matching shell semantics, so
// a hook can deliberately override HOME.
func TestEnvScriptOverridesInheritedValue(t *testing.T) {
	t.Setenv("HOME", "/real/home")
	h := newExecutor(t)

	env, err := h.Env(domain.HookScript{Env: []string{"HOME=/fake/home"}})
	require.NoError(t, err, "env")

	{
		v, _ := envValue(env, "HOME")
		assert.Equal(t, "/fake/home", v, "HOME = %q, want the override /fake/home", v)
	}

	count := 0

	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); strings.EqualFold(k, "HOME") {
			count++
		}
	}

	assert.Equal(t, 1, count, "HOME appears %d times, want exactly 1", count)
}

func TestEnvRejectsMalformedDeclaration(t *testing.T) {
	h := newExecutor(t)
	{
		_, err := h.Env(
			domain.HookScript{Env: []string{"NOPE"}},
		)
		assert.Equal(t, appErrors.ErrCodeHookInvalid, appErrors.CodeOf(
			err,
		), "code = %q, want %v", appErrors.CodeOf(err), appErrors.ErrCodeHookInvalid)
	}
}

// A variable that is set but empty carries no information and would only make
// the child's environment harder to reason about, so it is dropped.
func TestEnvSkipsInheritedVariablesThatAreSetButEmpty(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USER", "dmm-fixture-user")
	h := newExecutor(t)

	env, err := h.Env(domain.HookScript{})
	require.NoError(t, err, "env")

	{
		_, ok := envValue(env, "HOME")
		assert.False(t, ok, "empty HOME was forwarded")
	}

	{
		v, ok := envValue(env, "USER")
		assert.False(
			t,
			!ok || v != "dmm-fixture-user",
			"USER = %q (present %v), want dmm-fixture-user",
			v,
			ok,
		)
	}
}

func TestRegisterUsesLanguageTyper(t *testing.T) {
	h := newExecutor(t)

	r := &recordingRunner{language: domain.LangJS}
	{
		err := h.Register(r)
		require.NoError(t, err, "register: %v", err)
	}

	assert.True(
		t,
		h.HasRunner(domain.LangJS),
		"runner not registered under the type it reported",
	)
}

func TestRegisterRejectsNil(t *testing.T) {
	h := newExecutor(t)
	{
		err := h.Register(nil)
		assert.Equal(
			t,
			appErrors.ErrCodeInvalidArgument,
			appErrors.CodeOf(err),
			"code = %q, want %v",
			appErrors.CodeOf(err),
			appErrors.ErrCodeInvalidArgument,
		)
	}
}

// A runner that cannot name its type would otherwise be stored under a guess.
func TestRegisterRejectsRunnerWithoutLanguage(t *testing.T) {
	h := newExecutor(t)

	err := h.Register(untypedRunner{})
	require.Equal(
		t,
		appErrors.ErrCodeInvalidArgument,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeInvalidArgument,
	)
}

func TestRegisterRunnerRejectsUnknownLanguage(t *testing.T) {
	h := newExecutor(t)

	err := h.RegisterRunner("ruby", &recordingRunner{})
	assert.Equal(
		t,
		appErrors.ErrCodeInvalidArgument,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeInvalidArgument,
	)
}

func TestRegisterRunnerRejectsNilRunner(t *testing.T) {
	h := newExecutor(t)

	err := h.RegisterRunner(domain.LangJS, nil)
	assert.Equal(
		t,
		appErrors.ErrCodeInvalidArgument,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeInvalidArgument,
	)
}

// A test (or a future runner that supersedes a bundled one) must be able to swap
// a runner without a second registration path.
func TestRegisterRunnerReplaces(t *testing.T) {
	h := newExecutor(t)
	first := &recordingRunner{language: domain.LangJS}

	second := &recordingRunner{language: domain.LangJS}
	{
		err := h.RegisterRunner(domain.LangJS, first)
		require.NoError(t, err)
	}

	{
		err := h.RegisterRunner(domain.LangJS, second)
		require.NoError(t, err, "replace: %v", err)
	}

	_, modDir := newGameDirs(t)
	{
		err := h.Execute(
			context.Background(),
			[]domain.HookScript{{Language: domain.LangJS, Content: "1"}},
			hookMod(modDir),
		)
		require.NoError(t, err, "execute: %v", err)
	}

	assert.Empty(t, first.snapshot(), "replaced runner was still invoked")

	assert.Len(
		t,
		second.snapshot(),
		1,
		"replacement runner ran %d times, want 1",
		len(second.snapshot()),
	)
}

func TestUnregisterRemovesRunner(t *testing.T) {
	h := newExecutor(t)
	{
		err := h.RegisterRunner(domain.LangLua, &recordingRunner{})
		require.NoError(t, err)
	}

	require.True(t, h.Unregister(domain.LangLua), "Unregister reported nothing removed")

	assert.False(t, h.Unregister(domain.LangLua), "second Unregister reported a removal")

	assert.False(t, h.HasRunner(domain.LangLua), "runner still present")
}

func TestRunnerLanguagesIsSorted(t *testing.T) {
	h := newExecutor(t)

	for _, lang := range []domain.ScriptLanguage{domain.LangLua, domain.LangJS} {
		{
			err := h.RegisterRunner(lang, &recordingRunner{language: lang})
			require.NoError(t, err)
		}
	}

	got := h.RunnerLanguages()

	want := []domain.ScriptLanguage{domain.LangJS, domain.LangLua}
	assert.True(t, reflect.DeepEqual(got, want), "languages = %v, want %v", got, want)
}

func TestRunnerLanguagesEmptyIsNotNil(t *testing.T) {
	{
		got := newExecutor(t).RunnerLanguages()
		assert.False(
			t,
			got == nil || len(got) != 0,
			"languages = %v, want an empty non-nil slice",
			got,
		)
	}
}

func TestExecuteRunsScriptsInOrder(t *testing.T) {
	game, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua}

	h := NewHookExecutor(HookExecutorDeps{GamePath: func() string { return game }})
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	scripts := []domain.HookScript{
		{Language: domain.LangLua, Content: "first"},
		{Language: domain.LangLua, Content: "second"},
	}
	{
		err := h.Execute(context.Background(), scripts, hookMod(modDir))
		require.NoError(t, err, "execute: %v", err)
	}

	{
		got := len(r.snapshot())
		require.Equal(t, 2, got, "runner called %d times, want 2", got)
	}
}

func TestExecuteWithNoScriptsIsANoop(t *testing.T) {
	{
		err := newExecutor(t).Execute(context.Background(), nil, nil)
		assert.NoError(t, err, "execute: %v", err)
	}

	{
		err := newExecutor(t).Execute(context.Background(), []domain.HookScript{}, nil)
		assert.NoError(t, err, "execute: %v", err)
	}
}

// Stopping at the first failure is deliberate: a phase that failed half-way has
// already changed the world.
func TestExecuteStopsAtFirstFailure(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua, err: errors.New("boom")}

	h := newExecutor(t)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}
	// The fake fails every call, so the first script must be the last one to run.
	scripts := []domain.HookScript{
		{Language: domain.LangLua, Content: "first"},
		{Language: domain.LangLua, Content: "never"},
		{Language: domain.LangLua, Content: "also never"},
	}

	err := h.Execute(context.Background(), scripts, hookMod(modDir))
	require.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookFailed,
	)

	{
		got := len(r.snapshot())
		assert.Equal(t, 1, got, "runner called %d times, want 1 (stop at the first failure)", got)
	}
}

func TestExecuteUnknownTypeReportsRunnerNotFound(t *testing.T) {
	_, modDir := newGameDirs(t)
	h := newExecutor(t)

	err := h.Execute(
		context.Background(),
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		hookMod(modDir),
	)
	require.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
	)

	assert.Contains(
		t,
		err.Error(),
		string(appErrors.ErrCodeRunnerNotFound),
		"error %q does not mention the missing runner",
		err,
	)
}

// A pre-cancelled context must stop before a single hook runs.
func TestExecuteRejectsPreCanceledContext(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua}

	h := newExecutor(t)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := h.Execute(
		ctx,
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		hookMod(modDir),
	)
	assert.Equal(
		t,
		appErrors.ErrCodeOperationCanceled,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeOperationCanceled,
	)

	{
		got := len(r.snapshot())
		assert.Equal(t, 0, got, "runner called %d times on a canceled context, want 0", got)
	}
}

func TestExecuteAcceptsNilContext(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua}

	h := newExecutor(t)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}
	//nolint:staticcheck // passing a nil context is exactly what is under test
	{
		err := h.Execute(
			nil,
			[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
			hookMod(modDir),
		)
		assert.NoError(t, err, "execute with nil ctx: %v", err)
	}
}

// The timeout must reach the runner as a real deadline, not merely be computed.
func TestExecuteAppliesTimeoutToContext(t *testing.T) {
	_, modDir := newGameDirs(t)

	var deadline time.Time

	r := &recordingRunner{
		language: domain.LangLua,
		observe: func(ctx context.Context, _ PreparedHook) {
			deadline, _ = ctx.Deadline()
		},
	}

	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(2 * time.Second),
				MaxTimeout:     domain.DurationOf(60 * time.Second),
			},
		},
	)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	{
		err := h.Execute(
			context.Background(),
			[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
			hookMod(modDir),
		)
		require.NoError(t, err, "execute: %v", err)
	}

	require.False(t, deadline.IsZero(), "no deadline was set on the hook context")

	{
		remaining := time.Until(deadline)
		assert.False(
			t,
			remaining > 2*time.Second || remaining < time.Second,
			"remaining deadline %s, want roughly 2s",
			remaining,
		)
	}
}

func TestExecuteReportsTimeoutWhenRunnerOutlivesDeadline(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua, waitCtx: true}

	h := NewHookExecutor(
		HookExecutorDeps{
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(1 * time.Second),
				MaxTimeout:     domain.DurationOf(2 * time.Second),
			},
		},
	)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	start := time.Now()

	err := h.Execute(
		context.Background(),
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		hookMod(modDir),
	)
	require.Equal(
		t,
		appErrors.ErrCodeHookTimeout,
		appErrors.CodeOf(err),
		"code = %q, want %v (err %v)",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookTimeout,
		err,
	)

	{
		elapsed := time.Since(start)
		assert.False(
			t,
			elapsed > 5*time.Second,
			"took %s, want the 1s timeout to cut it short",
			elapsed,
		)
	}
}

// A cancelled parent is the caller's doing, so it is reported as an operation
// cancellation, not as a hook that ran too long.
func TestExecuteReportsCancellationWhenParentCanceled(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua, waitCtx: true}

	h := newExecutor(t)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := h.Execute(
		ctx,
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		hookMod(modDir),
	)
	assert.Equal(
		t,
		appErrors.ErrCodeOperationCanceled,
		appErrors.CodeOf(err),
		"code = %q, want %v (err %v)",
		appErrors.CodeOf(err),
		appErrors.ErrCodeOperationCanceled,
		err,
	)
}

// A hook that fails while its context is still alive is a plain failure, not a
// timeout.
func TestExecutePlainFailureIsNotATimeout(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua, err: errors.New("nope")}

	h := newExecutor(t)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	err := h.Execute(
		context.Background(),
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		hookMod(modDir),
	)
	assert.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookFailed,
	)
}

func TestExecuteIncludesModLabelInFailure(t *testing.T) {
	h := newExecutor(t)

	r := &recordingRunner{language: domain.LangLua, err: errors.New("boom")}
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	err := h.Execute(
		context.Background(),
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		nil,
	)
	assert.False(
		t,
		err == nil || !strings.Contains(err.Error(), "<unknown>"),
		"error %q does not name the unknown mod",
		err,
	)
}

func TestExecuteOnNilExecutor(t *testing.T) {
	var h *HookExecutor
	//nolint:staticcheck // nil receiver is what is under test
	err := h.Execute(
		context.Background(),
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		nil,
	)
	assert.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		appErrors.CodeOf(err),
		"code = %q, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookInvalid,
	)
}

func TestExecutePhaseRunsOnlyThatPhase(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua}

	h := NewHookExecutor(HookExecutorDeps{Context: HookContextPaths{}.Context})
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	hooks := domain.ModHooks{
		PreEnable:  []domain.HookScript{{Language: domain.LangLua, Content: "pre"}},
		PostEnable: []domain.HookScript{{Language: domain.LangLua, Content: "post"}},
	}
	{
		err := h.ExecutePhase(
			context.Background(),
			domain.PhasePreEnable,
			hooks,
			hookMod(modDir),
		)
		require.NoError(t, err, "execute phase: %v", err)
	}

	calls := r.snapshot()
	require.Len(t, calls, 1, "runner called %d times, want 1", len(calls))

	assert.Equal(
		t,
		domain.PhasePreEnable,
		calls[0].Phase,
		"phase = %q, want %q",
		calls[0].Phase,
		domain.PhasePreEnable,
	)
}

func TestExecutePhaseWithNoHooksIsANoop(t *testing.T) {
	{
		err := newExecutor(
			t,
		).ExecutePhase(
			context.Background(),
			domain.PhasePostUpdate,
			domain.ModHooks{},
			nil,
		)
		assert.NoError(t, err, "execute phase: %v", err)
	}
}

func TestExecutePhaseCoversEveryPhase(t *testing.T) {
	_, modDir := newGameDirs(t)
	// Every phase in the domain must be reachable through ExecutePhase; a phase
	// added without a hooks test would otherwise be silently unexercised.
	for _, phase := range domain.HookPhases() {
		r := &recordingRunner{language: domain.LangLua}

		h := newExecutor(t)
		{
			err := h.Register(r)
			require.NoError(t, err)
		}

		hooks := domain.ModHooks{}
		hooks.Set(phase, []domain.HookScript{{Language: domain.LangLua, Content: "x"}})

		{
			err := h.ExecutePhase(context.Background(), phase, hooks, hookMod(modDir))
			require.NoError(t, err, "phase %s: %v", phase, err)
		}

		assert.Len(
			t,
			r.snapshot(),
			1,
			"phase %s: runner called %d times, want 1",
			phase,
			len(r.snapshot()),
		)
	}
}

func TestPreparePopulatesHookContext(t *testing.T) {
	game, modDir := newGameDirs(t)
	paths := HookContextPaths{
		GamePath:     game,
		GameModsPath: filepath.Join(game, "mods"),
		GameVersion:  "1.0.2",
		CachePath:    filepath.Join(game, "cache"),
		TempPath:     filepath.Join(game, "tmp"),
		StoragePath:  filepath.Join(game, "storage"),
	}
	h := NewHookExecutor(HookExecutorDeps{
		GamePath: func() string { return game },
		Context:  paths.Context,
	})

	p, err := h.Prepare(
		domain.HookScript{Language: domain.LangLua, Content: "x"},
		hookMod(modDir),
		domain.PhasePreEnable,
		2,
	)
	require.NoError(t, err, "prepare")

	assert.False(
		t,
		p.Context.Phase != domain.PhasePreEnable || p.Context.Index != 2,
		"phase/index = %q/%d, want %q/2",
		p.Context.Phase,
		p.Context.Index,
		domain.PhasePreEnable,
	)

	assert.False(
		t,
		p.Context.Mod.ID != "better_ui" || p.Context.Mod.Version != "1.2.0",
		"mod context = %+v",
		p.Context.Mod,
	)

	assert.True(t, p.Context.Mod.Enabled, "mod context should carry mod.Enabled")

	assert.False(
		t,
		p.Context.Game.Version != "1.0.2" || p.Context.Game.ModsPath == "",
		"game context = %+v",
		p.Context.Game,
	)
}

func TestHookContextPathsToleratesNilMod(t *testing.T) {
	paths := HookContextPaths{GamePath: "/game"}

	hc := paths.Context(nil, domain.PhasePreInstall, 0)
	assert.Empty(t, hc.Mod.ID, "mod context = %+v, want the zero value", hc.Mod)
}

func TestPrepareWithoutContextFuncLeavesEmptyContext(t *testing.T) {
	_, modDir := newGameDirs(t)
	h := newExecutor(t)

	p, err := h.Prepare(
		domain.HookScript{Language: domain.LangLua, Content: "x"},
		hookMod(modDir),
		domain.PhasePreInstall,
		0,
	)
	require.NoError(t, err, "prepare")

	assert.Empty(
		t,
		p.Context.Phase,
		"phase = %q, want empty when no Context func is configured",
		p.Context.Phase,
	)
}

func TestPrepareCarriesResolvedValues(t *testing.T) {
	game, modDir := newGameDirs(t)
	h := NewHookExecutor(HookExecutorDeps{GamePath: func() string { return game }})

	p, err := h.Prepare(domain.HookScript{
		Language:   domain.LangLua,
		Content:    "print('hi')",
		WorkingDir: ".",
		Env:        []string{"A=1"},
	}, hookMod(modDir), domain.PhasePostEnable, 1)
	require.NoError(t, err, "prepare")

	assert.Equal(t, modDir, p.WorkingDir, "working dir = %q, want %q", p.WorkingDir, modDir)

	assert.Equal(t, domain.LangLua, p.Script.Language, "language")
	assert.Equal(t, "print('hi')", p.Script.Content, "content")

	{
		v, _ := envValue(p.Env, "A")
		assert.Equal(t, "1", v, "env A = %q, want 1", v)
	}

	assert.Equal(
		t,
		h.Permissions(hookMod(modDir)),
		p.Permissions,
		"permissions should be resolved at prepare time",
	)
}

func TestExecutePublishesStartedAndFinished(t *testing.T) {
	_, modDir := newGameDirs(t)
	events := &collectingEvents{}
	r := &recordingRunner{language: domain.LangLua}

	h := NewHookExecutor(HookExecutorDeps{Events: events})
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	{
		err := h.Execute(
			context.Background(),
			[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
			hookMod(modDir),
		)
		require.NoError(t, err, "execute: %v", err)
	}

	got := events.types()

	want := []EventType{EventHookStarted, EventHookFinished}
	assert.True(t, reflect.DeepEqual(got, want), "events = %v, want %v", got, want)

	ev := events.snapshot()[0]
	assert.False(
		t,
		ev.ModID != "better_ui" || ev.SourceID != "vortex",
		"event identity = %q/%q",
		ev.ModID,
		ev.SourceID,
	)
}

func TestExecutePublishesScriptOutput(t *testing.T) {
	_, modDir := newGameDirs(t)
	events := &collectingEvents{}
	r := &recordingRunner{
		language: domain.LangLua,
		observe: func(_ context.Context, hook PreparedHook) {
			hook.Output([]string{"hello", "world"}, 3)
		},
	}

	h := NewHookExecutor(HookExecutorDeps{Events: events})
	require.NoError(t, h.Register(r), "register")

	require.NoError(t, h.Execute(
		context.Background(),
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		hookMod(modDir),
	))

	assert.Equal(t,
		[]EventType{EventHookStarted, EventHookOutput, EventHookFinished},
		events.types())

	data, ok := events.snapshot()[1].Data.(hookEventData)
	require.True(t, ok, "payload type = %T", events.snapshot()[1].Data)
	assert.Equal(t, []string{"hello", "world"}, data.Lines)
	assert.Equal(t, 3, data.Dropped)
}

func TestHookScriptOutputIsLoggedOncePerAutomaticRun(t *testing.T) {
	_, modDir := newGameDirs(t)
	output := func(_ context.Context, hook PreparedHook) {
		hook.Output([]string{"hello", "world"}, 2)
	}

	var automatic strings.Builder
	logger := zerolog.New(&automatic)
	h := NewHookExecutor(HookExecutorDeps{
		Events: &collectingEvents{},
		Logger: &logger,
		GameID: func() string { return "darktide" },
	})
	require.NoError(t, h.Register(&recordingRunner{language: domain.LangLua, observe: output}), "register")

	require.NoError(t, h.ExecutePhase(
		context.Background(),
		domain.PhasePostEnable,
		domain.ModHooks{PostEnable: []domain.HookScript{{Language: domain.LangLua, Content: "x"}}},
		hookMod(modDir),
	))

	require.Equal(t, 1, strings.Count(automatic.String(), `"op":"hook.script"`), "automatic run logged %s", automatic.String())
	assert.Contains(t, automatic.String(), `"lines":["hello","world"]`)
	assert.Contains(t, automatic.String(), `"mod":"better_ui"`)
	assert.Contains(t, automatic.String(), `"game":"darktide"`)
	assert.Contains(t, automatic.String(), `"dropped":2`)

	var editor strings.Builder
	editorLogger := zerolog.New(&editor)
	h = NewHookExecutor(HookExecutorDeps{Events: &collectingEvents{}, Logger: &editorLogger})
	require.NoError(t, h.Register(&recordingRunner{language: domain.LangLua, observe: output}), "register")

	require.NoError(t, h.Execute(
		context.Background(),
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		hookMod(modDir),
	))

	assert.NotContains(t, editor.String(), `"op":"hook.script"`, "editor run must not duplicate its console output into the log")
}

func TestExecutePublishesFailed(t *testing.T) {
	_, modDir := newGameDirs(t)
	events := &collectingEvents{}
	r := &recordingRunner{language: domain.LangLua, err: errors.New("boom")}

	h := NewHookExecutor(HookExecutorDeps{Events: events})
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	_ = h.Execute(
		context.Background(),
		[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
		hookMod(modDir),
	)
	got := events.types()

	want := []EventType{EventHookStarted, EventHookFailed}
	assert.True(t, reflect.DeepEqual(got, want), "events = %v, want %v", got, want)
}

func TestExecuteEventsCarryPhaseAndTimeout(t *testing.T) {
	_, modDir := newGameDirs(t)
	events := &collectingEvents{}
	r := &recordingRunner{language: domain.LangLua}

	h := NewHookExecutor(
		HookExecutorDeps{
			Events: events,
			Config: domain.HooksConfig{
				DefaultTimeout: domain.DurationOf(9 * time.Second),
				MaxTimeout:     domain.DurationOf(60 * time.Second),
			},
		},
	)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	hooks := domain.ModHooks{
		PostDisable: []domain.HookScript{{Language: domain.LangLua, Content: "x"}},
	}
	{
		err := h.ExecutePhase(
			context.Background(),
			domain.PhasePostDisable,
			hooks,
			hookMod(modDir),
		)
		require.NoError(t, err, "execute phase: %v", err)
	}

	data, ok := events.snapshot()[0].Data.(hookEventData)
	require.True(t, ok, "payload type = %T, want hookEventData", events.snapshot()[0].Data)

	assert.False(t, data.Phase != string(domain.PhasePostDisable) ||
		data.Type != string(domain.LangLua), "payload = %+v", data)

	assert.Equal(t, 9*time.Second, data.Timeout, "payload timeout = %s, want 9s", data.Timeout)
}

func TestExecuteWithoutEventSinkDoesNotPanic(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua}

	h := newExecutor(t)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	{
		err := h.Execute(
			context.Background(),
			[]domain.HookScript{{Language: domain.LangLua, Content: "x"}},
			hookMod(modDir),
		)
		assert.NoError(t, err, "execute: %v", err)
	}
}

// Registering while hooks run must not race: the map is guarded, and a hook
// dispatched mid-swap lands on either the old or the new runner, never neither.
func TestRegisterDuringExecuteIsRaceFree(t *testing.T) {
	game, modDir := newGameDirs(t)
	first := &recordingRunner{language: domain.LangLua}

	h := NewHookExecutor(HookExecutorDeps{GamePath: func() string { return game }})
	{
		err := h.RegisterRunner(domain.LangLua, first)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()

		for range 50 {
			_ = h.RegisterRunner(
				domain.LangLua,
				&recordingRunner{language: domain.LangLua},
			)
		}
	}()
	go func() {
		defer wg.Done()

		for range 50 {
			{
				err := h.Execute(
					context.Background(),
					[]domain.HookScript{
						{Language: domain.LangLua, Content: "x"},
					},
					hookMod(modDir),
				)
				assert.NoError(t, err, "execute: %v", err)
			}
		}
	}()

	wg.Wait()
}

func TestRunnerIsInvokedUnderTestWithoutRace(t *testing.T) {
	_, modDir := newGameDirs(t)
	r := &recordingRunner{language: domain.LangLua}

	h := newExecutor(t)
	{
		err := h.Register(r)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_ = h.Execute(context.Background(),
				[]domain.HookScript{{Language: domain.LangLua, Content: "x"}}, hookMod(modDir))
		}()
	}

	wg.Wait()

	{
		got := len(r.snapshot())
		assert.Equal(t, 8, got, "runner called %d times, want 8", got)
	}
}

// The allowlist is the manager's security boundary; a test pins its contents so
// adding a variable is a deliberate act, not an accident.
func TestHookEnvAllowlistHasNoSecrets(t *testing.T) {
	for _, name := range hookEnvAllowlist {
		upper := strings.ToUpper(name)
		for _, banned := range []string{"TOKEN", "SECRET", "KEY", "PASSWORD", "PASSWD", "CREDENTIAL"} {
			assert.NotContains(
				t,
				upper,
				banned,
				"allowlist contains %q, which matches %q",
				name,
				banned,
			)
		}
	}
}

func TestHookEnvAllowlistSetMatchesList(t *testing.T) {
	assert.Len(
		t,
		hookEnvAllowlistSet,
		len(hookEnvAllowlist),
		"set has %d entries, list has %d",
		len(hookEnvAllowlistSet),
		len(hookEnvAllowlist),
	)

	for _, name := range hookEnvAllowlist {
		assert.True(
			t,
			hookEnvAllowlistSet[strings.ToLower(name)],
			"%q missing from the lookup set",
			name,
		)
	}
}

// The executor stores the platform it was built for, so hooks written for a
// Windows install keep working when the tests run on Linux.
func TestPlatformIsFixedAtConstruction(t *testing.T) {
	h := NewHookExecutor(HookExecutorDeps{Platform: domain.PlatformWindows})
	assert.Equal(
		t,
		domain.PlatformWindows,
		h.Platform(),
		"platform = %q, want windows",
		h.Platform(),
	)
}

func TestLookPathIsUsable(t *testing.T) {
	// The executor's default LookPath must resolve a real interpreter, otherwise
	// every command hook fails at start with a confusing message.
	h := newExecutor(t)

	got := platform.GetShell(h.Platform(), h.lookPath)
	require.True(t, got.Valid(), "shell %q is not usable", got)

	assert.False(t, runtime.GOOS == "linux" && got.Name == "", "no shell resolved on linux")
}
