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

// defaultHookTimeout mirrors domain.DefaultConfig().Hooks.DefaultTimeout. It is
// duplicated rather than imported from the config so the executor stays usable
// with a zero-valued domain.HooksConfig.
const defaultHookTimeout = 30 * time.Second

// maxHookTimeout is the hard ceiling applied even when a hook asks for more.
// domain.DefaultConfig uses 600s; the constant exists so the executor's
// behaviour is defined for a config that never went through ApplyDefaults.
const maxHookTimeout = 600 * time.Second

// hookEnvAllowlist is the set of variable names inherited from the manager's own
// environment and handed to a hook. Mod authors are untrusted: a hook that could
// read the process environment would find the database path, and on a machine
// where the user has an API key exported, that key. Anything a hook needs beyond
// this list has to be declared explicitly in HookScript.Env.
//
// Windows names are matched case-insensitively (as the OS itself does); on POSIX
// the same names are simply the conventional ones.
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

// HookRunner executes one kind of hook script. The interface is deliberately one
// method wide: a runner only ever runs a script, and keeping it minimal means a
// third-party runner needs no access to the manager's state.
//
// The argument is a PreparedHook rather than the raw domain.HookScript, because
// the resolved WorkingDir and Env are exactly what makes a hook safe: handing
// a runner the raw script would let it read an unfiltered environment or an
// uncontained working directory. The original declaration is still available as
// hook.Script.
type HookRunner interface {
	Run(ctx context.Context, hook PreparedHook, mod *domain.Mod) error
}

// HookTyper is the optional self-description interface a runner implements when
// it wants Register to place it in the map without the caller naming the type.
type HookTyper interface {
	HookType() domain.HookType
}

// MultiHookTyper is implemented by a runner that serves more than one script
// type. The process-spawning runner handles both `file` and `command`, because
// the two differ only in how the interpreter is chosen; declaring them here is
// what stops `file` hooks from silently having no runner at all.
type MultiHookTyper interface {
	HookTypes() []domain.HookType
}

// HookContextFunc builds the data handed to a hook script for one phase.
type HookContextFunc func(mod *domain.Mod, phase domain.HookPhase, index int) domain.HookContext

// HookContextPaths is the manager state a hook script is allowed to see. It is
// a value rather than a pointer so a caller can capture one and rebuild it as
// the config changes.
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
	Events   EventSink
	LookPath platform.LookPath
	GamePath func() string
	Context  HookContextFunc
	Clock    Clock
	Logger   *zerolog.Logger
	Platform domain.Platform
	Config   domain.HooksConfig
}

// HookExecutor runs mod lifecycle hooks through a pluggable runner per script
// type. It owns the security policy — timeouts, allowed working directories and
// the environment allowlist — so that a runner cannot be talked into exceeding
// it.
type HookExecutor struct {
	events   EventSink
	runners  map[domain.HookType]HookRunner
	lookPath platform.LookPath
	gamePath func() string
	context  HookContextFunc
	log      *zerolog.Logger
	now      Clock
	platform domain.Platform
	config   domain.HooksConfig
	mu       sync.RWMutex
}

// NewHookExecutor returns an executor with no runners registered. A hooks
// plugin package registers the concrete runners; see
// internal/plugins/hooks.RegisterAll.
func NewHookExecutor(deps HookExecutorDeps) *HookExecutor {
	cfg := deps.Config
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = int(defaultHookTimeout / time.Second)
	}

	if cfg.MaxTimeout < cfg.DefaultTimeout {
		// A ceiling below the default would make every hook time out at the
		// ceiling the moment a per-hook value was clamped to it.
		cfg.MaxTimeout = cfg.DefaultTimeout
	}

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
		runners:  make(map[domain.HookType]HookRunner),
		config:   cfg,
		platform: p,
		lookPath: look,
		gamePath: deps.GamePath,
		context:  deps.Context,
		events:   deps.Events,
		log:      loggerOf(deps.Logger),
		now:      now,
	}
}

// Register installs a runner, inferring the script type from HookTyper when the
// runner implements it. A runner that does not implement HookTyper must be
// registered with RegisterRunner.
//
// A runner that also implements MultiHookTyper is installed for every type it
// lists, HookType() included. The map is filled first and only then written to,
// so a runner declaring one bad type leaves nothing half-installed.
func (h *HookExecutor) Register(r HookRunner) error {
	if r == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "cannot register a nil hook runner")
	}

	typed, ok := r.(HookTyper)
	if !ok {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"hook runner %T does not implement HookType(); use RegisterRunner", r)
	}

	types := []domain.HookType{typed.HookType()}
	if multi, ok := r.(MultiHookTyper); ok {
		types = multi.HookTypes()
		if len(types) == 0 {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"hook runner %T declared no script types", r)
		}
	}

	for _, t := range types {
		if !t.Valid() {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"hook runner %T claims unknown script type %q", r, t)
		}
	}

	for _, t := range types {
		if err := h.RegisterRunner(t, r); err != nil {
			return err
		}
	}

	return nil
}

// RegisterRunner installs a runner for one script type, replacing any previous
// one. Replacement is allowed on purpose: a test can swap in a fake, and a
// future runner that supersedes the bundled one must not need a second entry
// point.
func (h *HookExecutor) RegisterRunner(t domain.HookType, r HookRunner) error {
	if !t.Valid() {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "unknown hook script type %q", t)
	}

	if r == nil {
		return appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"cannot register a nil hook runner for %q",
			t,
		)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.runners[t] = r

	return nil
}

// Unregister removes the runner for t, reporting whether one was present.
func (h *HookExecutor) Unregister(t domain.HookType) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.runners[t]; !ok {
		return false
	}

	delete(h.runners, t)

	return true
}

// HasRunner reports whether a runner is registered for t.
func (h *HookExecutor) HasRunner(t domain.HookType) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	_, ok := h.runners[t]

	return ok
}

// RunnerTypes returns the registered script types, sorted, so a UI can list
// them without depending on map order.
func (h *HookExecutor) RunnerTypes() []domain.HookType {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]domain.HookType, 0, len(h.runners))
	for t := range h.runners {
		out = append(out, t)
	}

	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}

func (h *HookExecutor) runner(t domain.HookType) (HookRunner, error) {
	h.mu.RLock()
	r, ok := h.runners[t]
	h.mu.RUnlock()

	if !ok {
		return nil, appErrors.New(
			appErrors.ErrCodeRunnerNotFound,
			"no hook runner registered for type %q",
			t,
		)
	}

	return r, nil
}

// Platform reports the platform hooks are resolved for.
func (h *HookExecutor) Platform() domain.Platform { return h.platform }

// Config returns a copy of the timeout policy.
func (h *HookExecutor) Config() domain.HooksConfig { return h.config }

// Timeout returns the deadline applied to one script: the hook's own value
// clamped into [default, max], or the default when it does not set one. A
// negative value is treated as unset here; Validate is what rejects it.
func (h *HookExecutor) Timeout(script domain.HookScript) time.Duration {
	d := time.Duration(h.config.DefaultTimeout) * time.Second
	if d <= 0 {
		d = defaultHookTimeout
	}

	max := time.Duration(h.config.MaxTimeout) * time.Second
	if max < d {
		max = d
	}

	if max > maxHookTimeout {
		max = maxHookTimeout
	}

	if script.Timeout > 0 {
		if want := time.Duration(script.Timeout) * time.Second; want < d {
			d = want
		} else if want > max {
			d = max
		} else {
			d = want
		}
	}

	return d
}

// Validate checks everything the executor is responsible for before a script is
// handed to a runner: the type is known, the content is present, the timeout is
// not nonsensical, and the working directory and script file stay inside the
// mod's own folder or the game's folder.
func (h *HookExecutor) Validate(script domain.HookScript, mod *domain.Mod) error {
	if !script.Type.Valid() {
		return appErrors.New(
			appErrors.ErrCodeHookInvalid,
			"unknown hook script type %q",
			script.Type,
		)
	}

	if strings.TrimSpace(script.Content) == "" {
		return appErrors.New(
			appErrors.ErrCodeHookInvalid,
			"hook script of type %q has no content",
			script.Type,
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

	if script.Type == domain.HookTypeFile {
		if _, err := h.ScriptPath(script, mod); err != nil {
			return err
		}
	}

	return nil
}

// WorkingDir resolves the directory a script runs in. A blank WorkingDir means
// the mod's own folder, then the game's; with neither, the process working
// directory is inherited. An explicit WorkingDir is resolved relative to the mod
// folder (or the game folder) and must land inside one of them: a mod-supplied
// path must not be able to make the manager run code in, say, the user's home
// directory.
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

// ScriptPath resolves the file a `file` hook points at and applies the same
// containment rule as WorkingDir. Hook content is a path, not a command, so the
// restriction is about not letting a mod's manifest name an arbitrary script
// elsewhere on the disk.
func (h *HookExecutor) ScriptPath(script domain.HookScript, mod *domain.Mod) (string, error) {
	raw := strings.TrimSpace(script.Content)
	if raw == "" {
		return "", appErrors.New(appErrors.ErrCodeHookInvalid, "file hook has no script path")
	}

	modPath, gamePath := allowedRoots(mod, h.currentGamePath())

	base := modPath
	if base == "" {
		base = gamePath
	}

	resolved := raw
	if !filepath.IsAbs(resolved) {
		if base == "" {
			return "", appErrors.New(
				appErrors.ErrCodeHookInvalid,
				"hook script path %q cannot be resolved: the mod has no folder and no game path is configured",
				raw,
			)
		}

		resolved = filepath.Join(base, filepath.FromSlash(raw))
	}

	resolved = filepath.Clean(resolved)
	ok := false

	for _, root := range []string{modPath, gamePath} {
		if root != "" && domain.PathWithin(root, resolved) {
			ok = true
			break
		}
	}

	if !ok {
		return "", appErrors.New(appErrors.ErrCodeHookInvalid,
			"hook script path %q is outside the mod and game folders", raw)
	}

	st, err := os.Stat(resolved)
	if err != nil {
		return "", appErrors.Wrap(
			err,
			appErrors.ErrCodeHookInvalid,
			"hook script %q is not readable",
			raw,
		)
	}

	if st.IsDir() {
		return "", appErrors.New(
			appErrors.ErrCodeHookInvalid,
			"hook script path %q is a directory",
			raw,
		)
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
		// An inherited variable that is set but empty carries no information and
		// would only pad the child's environment: PATH= makes a tool look in the
		// current directory, and HOME= can confuse a shell that checks it exists.
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
	WorkingDir string
	ScriptPath string
	Env        []string
	Context    domain.HookContext
	Script     domain.HookScript
}

// currentGamePath resolves the game folder, tolerating a nil provider.
func (h *HookExecutor) currentGamePath() string {
	if h.gamePath == nil {
		return ""
	}

	return strings.TrimSpace(h.gamePath())
}

// Prepare validates a script and resolves its working directory and
// environment. Runners are expected to call it rather than reading Script.WorkingDir
// and Script.Env directly; a test can call it to assert the policy without
// executing anything.
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

	if script.Type == domain.HookTypeFile {
		resolved, err := h.ScriptPath(script, mod)
		if err != nil {
			return p, err
		}

		p.ScriptPath = resolved
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
	// ctx is the caller's context; runCtx below is this script's. Which of the
	// two expired decides the reported code: a cancelled parent is the caller's
	// doing, not a hook that ran too long.
	parent := ctx

	runner, err := h.runner(script.Type)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeHookInvalid, "hook %d for mod %s", index, label)
	}

	p, err := h.Prepare(script, mod, phase, index)
	if err != nil {
		return err
	}

	timeout := h.Timeout(script)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Debug, not info: a mod's hook chain runs on every enable, disable and
	// install, so info-level records would bury the state changes they sit next
	// to. What is worth seeing at the default level is the outcome, below.
	h.log.Debug().
		Str("op", "hook").
		Str("phase", string(phase)).
		Str("type", string(script.Type)).
		Int("index", index).
		Str("mod", modIDOf(mod)).
		Dur("timeout", timeout).
		Msg("running hook")

	h.publish(Event{
		Type:     EventHookStarted,
		ModID:    modIDOf(mod),
		SourceID: sourceIDOf(mod),
		Data: hookEventData{
			Phase:   string(phase),
			Index:   index,
			Type:    string(script.Type),
			Timeout: timeout,
		},
	})

	err = runner.Run(runCtx, p, mod)
	if err != nil {
		// Error level: a hook that fails changes what the game sees, so it is
		// one of the few things in this layer the log must not swallow.
		h.log.Error().
			Err(err).
			Str("op", "hook").
			Str("phase", string(phase)).
			Str("type", string(script.Type)).
			Int("index", index).
			Str("mod", modIDOf(mod)).
			Msg("hook failed")
	}

	switch {
	case err == nil:
		h.publish(Event{
			Type:     EventHookFinished,
			ModID:    modIDOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase: string(phase),
				Index: index,
				Type:  string(script.Type),
			},
		})

		return nil
	case errors.Is(parent.Err(), context.Canceled):
		h.publish(Event{
			Type:     EventHookFailed,
			ModID:    modIDOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase: string(phase),
				Index: index,
				Type:  string(script.Type),
				Error: parent.Err().Error(),
			},
		})

		return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled,
			"hook %d (%s) for mod %s was canceled", index, script.Type, label)
	case runCtx.Err() != nil:
		h.publish(Event{
			Type:     EventHookFailed,
			ModID:    modIDOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase:   string(phase),
				Index:   index,
				Type:    string(script.Type),
				Timeout: timeout,
				Error:   "timed out",
			},
		})

		return appErrors.Wrap(err, appErrors.ErrCodeHookTimeout,
			"hook %d (%s) for mod %s timed out after %s", index, script.Type, label, timeout)
	default:
		h.publish(Event{
			Type:     EventHookFailed,
			ModID:    modIDOf(mod),
			SourceID: sourceIDOf(mod),
			Data: hookEventData{
				Phase: string(phase),
				Index: index,
				Type:  string(script.Type),
				Error: err.Error(),
			},
		})

		return appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"hook %d (%s) for mod %s failed", index, script.Type, label)
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
}

func modIDOf(mod *domain.Mod) string {
	if mod == nil {
		return ""
	}

	return mod.ID
}

func sourceIDOf(mod *domain.Mod) string {
	if mod == nil {
		return ""
	}

	return mod.SourceID
}
