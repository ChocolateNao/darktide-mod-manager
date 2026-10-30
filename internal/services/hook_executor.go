package services

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// defaultHookTimeout mirrors domain.DefaultConfig().Hooks.DefaultTimeout.
const defaultHookTimeout = 10 * time.Second

// maxHookTimeout is the compile-time safety ceiling: no configured MaxTimeout
// or permission cap may exceed it. It sits well above the default MaxTimeout so
// a deliberate config raise still works.
const maxHookTimeout = 600 * time.Second

// hookEnvAllowlist is the set of variable names inherited from the manager's own
// environment and handed to a hook. Mod authors are untrusted: a hook that could
// read the process environment would find the database path, and on a machine
// where the user has an API key exported, that key. Anything a hook needs beyond
// this list has to be declared explicitly in HookScript.Env. Windows names are
// matched case-insensitively, as the OS itself does; on POSIX the same names are
// simply the conventional ones.
var hookEnvAllowlist = []string{
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"SHELL",
	"TMPDIR",
	"TMP",
	"TEMP",
	"LANG",
	"LC_ALL",
	"TERM",
	"SystemRoot",
	"SystemDrive",
	"COMSPEC",
	"PATHEXT",
	"OS",
	"PROCESSOR_ARCHITECTURE",
	"APPDATA",
	"LOCALAPPDATA",
	"USERPROFILE",
	"PROGRAMDATA",
	"PROGRAMFILES",
	"PROGRAMFILES(X86)",
	"WINDIR",
}

// hookEnvAllowlistSet is the lookup form of hookEnvAllowlist, lower-cased.
var hookEnvAllowlistSet = func() map[string]bool {
	m := make(map[string]bool, len(hookEnvAllowlist))
	for _, name := range hookEnvAllowlist {
		m[strings.ToLower(name)] = true
	}

	return m
}()

// HookRunner executes one kind of hook script.
type HookRunner interface {
	Run(ctx context.Context, hook PreparedHook, mod *domain.Mod) error
}

// SyntaxChecker is the optional interface a runner implements when it can reject
// a script the VM would refuse to compile. Validate consults it, so a syntax
// error is reported before the script is stored or run.
type SyntaxChecker interface {
	Check(script domain.HookScript) error
}

// LanguageTyper is the optional self-description interface a runner implements
// so Register can place it in the map without the caller naming the language.
type LanguageTyper interface {
	Language() domain.ScriptLanguage
}

// HookContextFunc builds the data handed to a hook script for one phase.
type HookContextFunc func(mod *domain.Mod, phase domain.HookPhase, index int) domain.HookContext

// HookContextPaths is the manager state a hook script is allowed to see.
type HookContextPaths struct {
	GamePath     string
	GameModsPath string
	GameVersion  string
	CachePath    string
	TempPath     string
	StoragePath  string
}

// Context implements HookContextFunc.
func (p HookContextPaths) Context(
	mod *domain.Mod,
	phase domain.HookPhase,
	index int,
) domain.HookContext {
	hc := domain.HookContext{
		Game: domain.GameHookContext{
			Path:     p.GamePath,
			Version:  p.GameVersion,
			ModsPath: p.GameModsPath,
		},
		Config: domain.ConfigHookContext{
			CachePath:   p.CachePath,
			TempPath:    p.TempPath,
			StoragePath: p.StoragePath,
		},
		Phase: phase,
		Index: index,
	}
	if mod != nil {
		hc.Mod = domain.ModHookContext{
			ID:      mod.ID,
			Name:    mod.Name,
			Version: mod.Version,
			Path:    mod.LocalPath,
			Enabled: mod.Enabled,
		}
	}

	return hc
}

// HookExecutorDeps carries everything the executor cannot invent for itself.
type HookExecutorDeps struct {
	Events         EventSink
	Clock          Clock
	LookPath       platform.LookPath
	GamePath       func() string
	GameID         func() string
	Context        HookContextFunc
	Logger         *zerolog.Logger
	ConfigProvider func() domain.HooksConfig
	Platform       domain.Platform
	Config         domain.HooksConfig
}

// HookExecutor runs mod lifecycle hooks through a pluggable runner per script language.
type HookExecutor struct {
	events         EventSink
	runners        map[domain.ScriptLanguage]HookRunner
	lookPath       platform.LookPath
	gamePath       func() string
	gameID         func() string
	context        HookContextFunc
	log            *zerolog.Logger
	now            Clock
	configProvider func() domain.HooksConfig
	platform       domain.Platform
	config         domain.HooksConfig
	mu             sync.RWMutex
}

// NewHookExecutor returns an executor with no runners registered; a hooks plugin
// package registers the concrete ones (see internal/plugins/hooks.RegisterAll).
func NewHookExecutor(deps HookExecutorDeps) *HookExecutor {
	cfg := normalizeHooksConfig(deps.Config)

	p := deps.Platform
	if p == "" {
		p = domain.CurrentPlatform()
	}

	look := deps.LookPath
	if look == nil {
		look = exec.LookPath
	}

	now := deps.Clock
	if now == nil {
		now = time.Now
	}

	return &HookExecutor{
		runners:        make(map[domain.ScriptLanguage]HookRunner),
		config:         cfg,
		configProvider: deps.ConfigProvider,
		platform:       p,
		lookPath:       look,
		gamePath:       deps.GamePath,
		gameID:         deps.GameID,
		context:        deps.Context,
		events:         deps.Events,
		log:            loggerOf(deps.Logger),
		now:            now,
	}
}

// normalizeHooksConfig applies the invariants Timeout relies on: a usable
// default floor, and a ceiling that is never below the floor.
func normalizeHooksConfig(cfg domain.HooksConfig) domain.HooksConfig {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = domain.DurationOf(defaultHookTimeout)
	}

	if cfg.MaxTimeout > 0 && cfg.MaxTimeout < cfg.DefaultTimeout {
		// An unset MaxTimeout is left for Timeout to resolve against the
		// compile-time ceiling.
		cfg.MaxTimeout = cfg.DefaultTimeout
	}

	return cfg
}

// hooksConfig is the policy every permission and timeout question reads:
// the provider's answer when one is wired, the construction-time value
// otherwise.
func (h *HookExecutor) hooksConfig() domain.HooksConfig {
	if h.configProvider != nil {
		return normalizeHooksConfig(h.configProvider())
	}

	return h.config
}

// Register installs a runner, inferring its language from LanguageTyper when
// the runner implements it. Every runner serves exactly one language: Lua and
// JavaScript each own their binding file, and a runner claiming an unknown
// language is refused before anything is written.
func (h *HookExecutor) Register(r HookRunner) error {
	if r == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "cannot register a nil hook runner")
	}

	typed, ok := r.(LanguageTyper)
	if !ok {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"hook runner %T does not implement Language(); use RegisterRunner", r)
	}

	return h.RegisterRunner(typed.Language(), r)
}

// RegisterRunner installs a runner for one script language, replacing any
// previous one. Replacement is deliberate: a test can swap in a fake, and a
// runner that supersedes the bundled one needs no second entry point.
func (h *HookExecutor) RegisterRunner(lang domain.ScriptLanguage, r HookRunner) error {
	if !lang.Valid() {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"unknown hook script language %q", lang)
	}

	if r == nil {
		return appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"cannot register a nil hook runner for %q",
			lang,
		)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.runners[lang] = r

	return nil
}

// Unregister removes the runner for lang, reporting whether one was present.
func (h *HookExecutor) Unregister(lang domain.ScriptLanguage) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.runners[lang]; !ok {
		return false
	}

	delete(h.runners, lang)

	return true
}

// HasRunner reports whether a runner is registered for lang.
func (h *HookExecutor) HasRunner(lang domain.ScriptLanguage) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	_, ok := h.runners[lang]

	return ok
}

// RunnerLanguages returns the registered script languages, sorted, so a UI can
// list them without depending on map order.
func (h *HookExecutor) RunnerLanguages() []domain.ScriptLanguage {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]domain.ScriptLanguage, 0, len(h.runners))
	for lang := range h.runners {
		out = append(out, lang)
	}

	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}

func (h *HookExecutor) runner(lang domain.ScriptLanguage) (HookRunner, error) {
	h.mu.RLock()
	r, ok := h.runners[lang]
	h.mu.RUnlock()

	if !ok {
		return nil, appErrors.New(
			appErrors.ErrCodeRunnerNotFound,
			"no hook runner registered for language %q",
			lang,
		)
	}

	return r, nil
}

// Platform reports the platform hooks are resolved for.
func (h *HookExecutor) Platform() domain.Platform { return h.platform }

// Config returns a copy of the timeout policy.
func (h *HookExecutor) Config() domain.HooksConfig { return h.hooksConfig() }

// Permissions resolves the effective capability set for one run: the mod's
// scope over the game's over the default, first true win.
func (h *HookExecutor) Permissions(mod *domain.Mod) domain.ScriptPermissions {
	return h.hooksConfig().Permissions.For(h.currentGameID(), modIDOf(mod))
}

// Timeout returns the deadline applied to one script: the script's declared
// value (or the default when unset) clamped into [floor, ceiling], where floor
// is the default timeout and ceiling is the smallest of MaxTimeout and the
// effective timeout_seconds cap (mod > game > default). A cap below the default
// still wins over a larger declared value. A negative declared value is treated
// as unset here; Validate is what rejects it.
func (h *HookExecutor) Timeout(script domain.HookScript, mod *domain.Mod) time.Duration {
	cfg := h.hooksConfig()

	floor := cfg.DefaultTimeout.Duration()
	if floor <= 0 {
		floor = defaultHookTimeout
	}

	ceiling := cfg.MaxTimeout.Duration()
	if ceiling <= 0 {
		ceiling = maxHookTimeout
	}

	if ceiling < floor {
		ceiling = floor
	}

	if ceiling > maxHookTimeout {
		ceiling = maxHookTimeout
	}

	if cap := h.Permissions(mod).TimeoutSeconds; cap > 0 {
		if capped := time.Duration(cap) * time.Second; capped < ceiling {
			ceiling = capped
		}
	}

	declared := floor
	if script.Timeout > 0 {
		declared = time.Duration(script.Timeout) * time.Second
	}

	if declared < floor {
		declared = floor
	}

	// Applied last so a cap below the floor still wins over a larger declared
	// value: the ceiling is the effective upper bound in every case.
	if declared > ceiling {
		declared = ceiling
	}

	return declared
}

// Validate checks what the executor is responsible for before a script is handed
// to a runner: the language is known, the content is present, the timeout is
// not nonsensical, and the working directory stays inside the mod's or the
// game's folder.
func (h *HookExecutor) Validate(script domain.HookScript, mod *domain.Mod) error {
	if !script.Language.Valid() {
		return appErrors.New(
			appErrors.ErrCodeHookInvalid,
			"unknown script language %q",
			script.Language,
		)
	}

	if strings.TrimSpace(script.Content) == "" {
		return appErrors.New(
			appErrors.ErrCodeHookInvalid,
			"hook script in %q has no content",
			script.Language,
		)
	}

	if script.Timeout < 0 {
		return appErrors.New(appErrors.ErrCodeHookInvalid,
			"hook script timeout %d must not be negative", script.Timeout)
	}

	for _, entry := range script.Env {
		if _, _, err := parseEnvEntry(entry); err != nil {
			return err
		}
	}

	if _, err := h.WorkingDir(script, mod); err != nil {
		return err
	}

	// The runner owns the parser; a language this build cannot run is caught
	// above, so a missing runner here just skips the extra check.
	if runner, err := h.runner(script.Language); err == nil {
		if checker, ok := runner.(SyntaxChecker); ok {
			if err := checker.Check(script); err != nil {
				return err
			}
		}
	}

	return nil
}

// WorkingDir resolves the directory a script runs in. A blank WorkingDir means
// the mod's own folder, then the game's; with neither, the process working
// directory is inherited. An explicit WorkingDir is resolved relative to the mod
// folder (or the game folder) and must land inside one of them, so a
// mod-supplied path cannot make the manager run code anywhere else.
func (h *HookExecutor) WorkingDir(script domain.HookScript, mod *domain.Mod) (string, error) {
	modPath, gamePath := allowedRoots(mod, h.currentGamePath())

	explicit := strings.TrimSpace(script.WorkingDir)
	if explicit == "" {
		for _, candidate := range []string{modPath, gamePath} {
			if candidate == "" {
				continue
			}

			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				return candidate, nil
			}
		}

		return "", nil
	}

	base := modPath
	if base == "" {
		base = gamePath
	}

	if base == "" {
		return "", appErrors.New(
			appErrors.ErrCodeHookInvalid,
			"hook working directory %q cannot be resolved: the mod has no folder and no game path is configured",
			explicit,
		)
	}

	resolved := explicit
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(base, filepath.FromSlash(explicit))
	}

	ok := false

	for _, root := range []string{modPath, gamePath} {
		if root != "" && domain.PathWithin(root, resolved) {
			ok = true
			break
		}
	}

	if !ok {
		return "", appErrors.New(appErrors.ErrCodeHookInvalid,
			"hook working directory %q is outside the mod and game folders", explicit)
	}

	st, err := os.Stat(resolved)
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeHookInvalid,
			"hook working directory %q is not usable", explicit)
	}

	if !st.IsDir() {
		return "", appErrors.New(appErrors.ErrCodeHookInvalid,
			"hook working directory %q is not a directory", explicit)
	}

	return resolved, nil
}

// Env builds the environment for a script: the allowlisted inherited variables
// followed by the script's own declarations. A later declaration of the same
// name wins, matching how a shell would behave.
func (h *HookExecutor) Env(script domain.HookScript) ([]string, error) {
	out := make([]string, 0, len(hookEnvAllowlist)+len(script.Env))

	seen := make(map[string]int, len(hookEnvAllowlist)+len(script.Env))
	for _, name := range hookEnvAllowlist {
		value, ok := os.LookupEnv(name)
		// An empty inherited variable carries no information: PATH= makes a tool
		// look in the current directory.
		if !ok || value == "" {
			continue
		}

		seen[strings.ToLower(name)] = len(out)
		out = append(out, name+"="+value)
	}

	for _, entry := range script.Env {
		name, value, err := parseEnvEntry(entry)
		if err != nil {
			return nil, err
		}

		key := strings.ToLower(name)
		if at, ok := seen[key]; ok {
			out[at] = name + "=" + value
			continue
		}

		seen[key] = len(out)
		out = append(out, name+"="+value)
	}

	return out, nil
}

// parseEnvEntry splits a "KEY=VALUE" declaration. The key must be non-empty and
// must not itself contain '='; everything after the first '=' is the value,
// including further '=' characters, so a base64 secret survives intact.
func parseEnvEntry(entry string) (name, value string, err error) {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return "", "", appErrors.New(
			appErrors.ErrCodeHookInvalid,
			"hook environment entry is empty",
		)
	}

	name, value, found := strings.Cut(trimmed, "=")
	if !found || name == "" {
		return "", "", appErrors.New(appErrors.ErrCodeHookInvalid,
			"hook environment entry %q is not KEY=VALUE", entry)
	}

	return name, value, nil
}

// allowedRoots returns the two directories a hook may touch: the mod's own
// folder and the game's folder.
func allowedRoots(mod *domain.Mod, gamePath string) (modPath, game string) {
	if mod != nil {
		modPath = strings.TrimSpace(mod.LocalPath)
	}

	return modPath, strings.TrimSpace(gamePath)
}

// Execute runs every script in order against mod, stopping at the first
// failure. Hooks for a phase are ordered, and a phase that fails half-way has
// already changed the world; running the rest would compound it.
func (h *HookExecutor) Execute(
	ctx context.Context,
	scripts []domain.HookScript,
	mod *domain.Mod,
) error {
	return h.execute(ctx, "", scripts, mod)
}

// ExecutePhase runs the scripts registered for one phase. The phase is handed to
// the hook context so a script can branch on it.
func (h *HookExecutor) ExecutePhase(
	ctx context.Context,
	phase domain.HookPhase,
	hooks domain.ModHooks,
	mod *domain.Mod,
) error {
	if hooks.Empty() {
		return nil
	}

	return h.execute(ctx, phase, hooks.For(phase), mod)
}

func (h *HookExecutor) execute(
	ctx context.Context,
	phase domain.HookPhase,
	scripts []domain.HookScript,
	mod *domain.Mod,
) error {
	if h == nil {
		return appErrors.New(appErrors.ErrCodeHookInvalid, "nil hook executor")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	if len(scripts) == 0 {
		return nil
	}

	label := modLabel(mod)

	for i, script := range scripts {
		// A paused script is skipped, not an error: a failure here would make
		// every later hook look broken too.
		if script.Disabled {
			continue
		}

		if err := h.runOne(ctx, phase, i, script, mod, label); err != nil {
			return err
		}
	}

	return nil
}

// PreparedHook is the fully validated, ready-to-run form of a hook script.
// Runners receive one of these instead of the raw script so they never have to
// re-implement the executor's security policy.
type PreparedHook struct {
	WorkingDir  string
	Env         []string
	Context     domain.HookContext
	Script      domain.HookScript
	Permissions domain.ScriptPermissions
	// Output receives the run's buffered console lines plus the count dropped at
	// the cap. The runner reports before returning, so a failing script keeps
	// what it printed.
	Output func(lines []string, dropped int)
}

// currentGamePath resolves the game folder, tolerating a nil provider.
func (h *HookExecutor) currentGamePath() string {
	if h.gamePath == nil {
		return ""
	}

	return strings.TrimSpace(h.gamePath())
}

// currentGameID resolves the active game id, tolerating a nil provider.
func (h *HookExecutor) currentGameID() string {
	if h.gameID == nil {
		return ""
	}

	return strings.TrimSpace(h.gameID())
}

// Prepare validates a script and resolves its working directory and environment.
// Runners are expected to call it instead of reading Script.WorkingDir and
// Script.Env directly.
func (h *HookExecutor) Prepare(
	script domain.HookScript,
	mod *domain.Mod,
	phase domain.HookPhase,
	index int,
) (PreparedHook, error) {
	p := PreparedHook{Script: script}
	if err := h.Validate(script, mod); err != nil {
		return p, err
	}

	dir, err := h.WorkingDir(script, mod)
	if err != nil {
		return p, err
	}

	env, err := h.Env(script)
	if err != nil {
		return p, err
	}

	p.WorkingDir = dir
	p.Env = env
	p.Permissions = h.Permissions(mod)

	if h.context != nil {
		p.Context = h.context(mod, phase, index)
	}

	return p, nil
}

func (h *HookExecutor) runOne(
	ctx context.Context,
	phase domain.HookPhase,
	index int,
	script domain.HookScript,
	mod *domain.Mod,
	label string,
) error {
	// Which of ctx or runCtx expired decides the reported code: a cancelled parent
	// is the caller's doing, not a hook that ran too long.
	parent := ctx

	// A failure before the runner starts still reaches the console.
	publishFailure := func(runErr error) {
		h.publish(Event{
			Type:     EventHookFailed,
			ModID:    modIDOf(mod),
			ModName:  modNameOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase: string(phase),
				Index: index,
				Type:  string(script.Language),
				Error: runErr.Error(),
			},
		})
	}

	runner, err := h.runner(script.Language)
	if err != nil {
		wrapped := appErrors.Wrap(err, appErrors.ErrCodeHookInvalid, "hook %d for mod %s", index, label)
		publishFailure(wrapped)
		return wrapped
	}

	p, err := h.Prepare(script, mod, phase, index)
	if err != nil {
		publishFailure(err)
		return err
	}

	timeout := h.Timeout(script, mod)

	p.Output = func(lines []string, dropped int) {
		h.publish(Event{
			Type:     EventHookOutput,
			ModID:    modIDOf(mod),
			ModName:  modNameOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase:   string(phase),
				Index:   index,
				Type:    string(script.Language),
				Lines:   lines,
				Dropped: dropped,
			},
		})

		// An editor run already shows its output in the console.
		if phase == "" {
			return
		}

		event := h.log.Debug().
			Str("op", "hook.script").
			Str("phase", string(phase)).
			Str("type", string(script.Language)).
			Int("index", index).
			Str("mod", modIDOf(mod)).
			Str("game", h.currentGameID())

		if len(lines) > 0 {
			event = event.Strs("lines", lines)
		}

		if dropped > 0 {
			event = event.Int("dropped", dropped)
		}

		event.Msg("hook output")
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Debug, not info: a mod's hook chain runs on every enable, disable and install,
	// so info-level records would bury the state changes they sit next to.
	h.log.Debug().
		Str("op", "hook").
		Str("phase", string(phase)).
		Str("type", string(script.Language)).
		Int("index", index).
		Str("mod", modIDOf(mod)).
		Dur("timeout", timeout).
		Msg("running hook")

	h.publish(Event{
		Type:     EventHookStarted,
		ModID:    modIDOf(mod),
		ModName:  modNameOf(mod),
		SourceID: sourceIDOf(mod),
		Data: hookEventData{
			Phase:   string(phase),
			Index:   index,
			Type:    string(script.Language),
			Timeout: timeout,
		},
	})

	err = runner.Run(runCtx, p, mod)
	if err != nil {
		// Error level: a hook that fails changes what the game sees, and the log
		// must not swallow it.
		h.log.Error().
			Err(err).
			Str("op", "hook").
			Str("phase", string(phase)).
			Str("type", string(script.Language)).
			Int("index", index).
			Str("mod", modIDOf(mod)).
			Msg("hook failed")
	}

	switch {
	case err == nil:
		h.publish(Event{
			Type:     EventHookFinished,
			ModID:    modIDOf(mod),
			ModName:  modNameOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase: string(phase),
				Index: index,
				Type:  string(script.Language),
			},
		})

		return nil
	case errors.Is(parent.Err(), context.Canceled):
		h.publish(Event{
			Type:     EventHookFailed,
			ModID:    modIDOf(mod),
			ModName:  modNameOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase: string(phase),
				Index: index,
				Type:  string(script.Language),
				Error: parent.Err().Error(),
			},
		})

		return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled,
			"hook %d (%s) for mod %s was canceled", index, script.Language, label)
	case runCtx.Err() != nil:
		h.publish(Event{
			Type:     EventHookFailed,
			ModID:    modIDOf(mod),
			ModName:  modNameOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase:   string(phase),
				Index:   index,
				Type:    string(script.Language),
				Timeout: timeout,
				Error:   "timed out",
			},
		})

		return appErrors.Wrap(err, appErrors.ErrCodeHookTimeout,
			"hook %d (%s) for mod %s timed out after %s", index, script.Language, label, timeout)
	default:
		h.publish(Event{
			Type:     EventHookFailed,
			ModID:    modIDOf(mod),
			ModName:  modNameOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase: string(phase),
				Index: index,
				Type:  string(script.Language),
				Error: err.Error(),
			},
		})

		return appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"hook %d (%s) for mod %s failed", index, script.Language, label)
	}
}

func (h *HookExecutor) publish(ev Event) {
	if h.events == nil {
		return
	}

	h.events.Publish(ev)
}

// hookEventData is the payload of a hook lifecycle event.
type hookEventData struct {
	Phase   string        `json:"phase"`
	Type    string        `json:"type"`
	Error   string        `json:"error,omitempty"`
	Index   int           `json:"index"`
	Timeout time.Duration `json:"timeout,omitempty"`
	Lines   []string      `json:"lines,omitempty"`
	Dropped int           `json:"dropped,omitempty"`
}

func modIDOf(mod *domain.Mod) string {
	if mod == nil {
		return ""
	}

	return mod.ID
}

func modNameOf(mod *domain.Mod) string {
	if mod == nil {
		return ""
	}

	return mod.Name
}

func sourceIDOf(mod *domain.Mod) string {
	if mod == nil {
		return ""
	}

	return mod.SourceID
}
