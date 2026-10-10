package domain

import (
	"strings"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ScriptLanguage selects which sandboxed runtime executes a hook script.
type ScriptLanguage string

const (
	// LangLua runs the Content as Lua inside a gopher-lua state opened with no
	// standard libraries beyond base, string, table and math.
	LangLua ScriptLanguage = "lua"
	// LangJS runs the Content as JavaScript inside a fresh goja VM per
	// execution.
	LangJS ScriptLanguage = "javascript"
)

// ScriptLanguages returns every script language the manager knows, in a stable
// order: lua first because it is the lighter sandbox, javascript second because
// it is the one that existed first.
func ScriptLanguages() []ScriptLanguage {
	return []ScriptLanguage{LangLua, LangJS}
}

// Valid reports whether the language is one of the known runtimes.
func (l ScriptLanguage) Valid() bool {
	switch l {
	case LangLua, LangJS:
		return true
	default:
		return false
	}
}

// ParseScriptLanguage converts a manifest or user supplied string to a
// ScriptLanguage. It is deliberately strict: a blank or unknown language is an
// error rather than a fallback, because a hook that silently runs in the wrong
// runtime is worse than a mod that refuses to load. Matching is
// case-insensitive, and "js" is accepted as the common short spelling of
// "javascript".
func ParseScriptLanguage(s string) (ScriptLanguage, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "lua":
		return LangLua, nil
	case "javascript", "js":
		return LangJS, nil
	default:
		return "", appErrors.New(appErrors.ErrCodeHookInvalid,
			"unknown script language %q; use one of lua, javascript",
			strings.TrimSpace(s))
	}
}

// HookPhase is one point in a mod's lifecycle at which scripts may run. Phases
// are ordered: PreInstall precedes the install itself, PostInstall follows it,
// and so on. The ordinal is used for iteration and for reporting order in the
// CLI, so the ordering below is significant.
type HookPhase string

const (
	PhasePreInstall    HookPhase = "pre_install"
	PhasePostInstall   HookPhase = "post_install"
	PhasePreEnable     HookPhase = "pre_enable"
	PhasePostEnable    HookPhase = "post_enable"
	PhasePreDisable    HookPhase = "pre_disable"
	PhasePostDisable   HookPhase = "post_disable"
	PhasePreUninstall  HookPhase = "pre_uninstall"
	PhasePostUninstall HookPhase = "post_uninstall"
	PhasePreUpdate     HookPhase = "pre_update"
	PhasePostUpdate    HookPhase = "post_update"
)

var hookPhases = []HookPhase{
	PhasePreInstall,
	PhasePostInstall,
	PhasePreEnable,
	PhasePostEnable,
	PhasePreDisable,
	PhasePostDisable,
	PhasePreUninstall,
	PhasePostUninstall,
	PhasePreUpdate,
	PhasePostUpdate,
}

// HookPhases returns the known phases in lifecycle order. The returned slice is
// a fresh copy, so callers may sort or filter it freely.
func HookPhases() []HookPhase {
	out := make([]HookPhase, len(hookPhases))
	copy(out, hookPhases)

	return out
}

// ParseHookPhase converts a string to a HookPhase, accepting the hyphenated
// spelling as well as the underscored one so that CLI input and JSON agree.
func ParseHookPhase(s string) (HookPhase, bool) {
	for _, p := range hookPhases {
		if p == HookPhase(s) || equalUnderscored(string(p), s) {
			return p, true
		}
	}

	return "", false
}

func equalUnderscored(a, b string) bool {
	if len(a) != len(b) {
		return false
	}

	norm := func(r rune) rune {
		if r == '-' {
			return '_'
		}

		return r
	}
	for i := range len(a) {
		if norm(rune(a[i])) != norm(rune(b[i])) {
			return false
		}
	}

	return true
}

// HookScript is a single script bound to a lifecycle phase.
type HookScript struct {
	// Language names the sandboxed runtime that executes Content. Local
	// manifests spell this as `type = "lua"`; the JSON form says `language`
	// because that is what the field is.
	Language   ScriptLanguage `json:"language"`
	Content    string         `json:"content"`
	WorkingDir string         `json:"working_dir,omitempty"`
	Env        []string       `json:"env,omitempty"`
	// Timeout is the script's own request, in seconds. Zero defers to the
	// configured default; whatever is asked for is still clamped by the
	// effective-timeout rule in the executor.
	Timeout int `json:"timeout,omitempty"`
}

// ModHooks groups every hook phase for one mod. Slices are ordered: scripts
// within a phase run in the order listed.
type ModHooks struct {
	PreInstall    []HookScript `json:"pre_install,omitempty"`
	PostInstall   []HookScript `json:"post_install,omitempty"`
	PreEnable     []HookScript `json:"pre_enable,omitempty"`
	PostEnable    []HookScript `json:"post_enable,omitempty"`
	PreDisable    []HookScript `json:"pre_disable,omitempty"`
	PostDisable   []HookScript `json:"post_disable,omitempty"`
	PreUninstall  []HookScript `json:"pre_uninstall,omitempty"`
	PostUninstall []HookScript `json:"post_uninstall,omitempty"`
	PreUpdate     []HookScript `json:"pre_update,omitempty"`
	PostUpdate    []HookScript `json:"post_update,omitempty"`
}

// For returns the scripts registered for phase, or nil when the phase has none.
func (h ModHooks) For(phase HookPhase) []HookScript {
	switch phase {
	case PhasePreInstall:
		return h.PreInstall
	case PhasePostInstall:
		return h.PostInstall
	case PhasePreEnable:
		return h.PreEnable
	case PhasePostEnable:
		return h.PostEnable
	case PhasePreDisable:
		return h.PreDisable
	case PhasePostDisable:
		return h.PostDisable
	case PhasePreUninstall:
		return h.PreUninstall
	case PhasePostUninstall:
		return h.PostUninstall
	case PhasePreUpdate:
		return h.PreUpdate
	case PhasePostUpdate:
		return h.PostUpdate
	default:
		return nil
	}
}

// Set replaces the scripts for phase. An unknown phase is ignored.
func (h *ModHooks) Set(phase HookPhase, scripts []HookScript) {
	if h == nil {
		return
	}

	switch phase {
	case PhasePreInstall:
		h.PreInstall = scripts
	case PhasePostInstall:
		h.PostInstall = scripts
	case PhasePreEnable:
		h.PreEnable = scripts
	case PhasePostEnable:
		h.PostEnable = scripts
	case PhasePreDisable:
		h.PreDisable = scripts
	case PhasePostDisable:
		h.PostDisable = scripts
	case PhasePreUninstall:
		h.PreUninstall = scripts
	case PhasePostUninstall:
		h.PostUninstall = scripts
	case PhasePreUpdate:
		h.PreUpdate = scripts
	case PhasePostUpdate:
		h.PostUpdate = scripts
	}
}

// Empty reports whether no hooks are defined in any phase.
func (h ModHooks) Empty() bool {
	for _, p := range hookPhases {
		if len(h.For(p)) > 0 {
			return false
		}
	}

	return true
}

// Count returns the total number of hook scripts across all phases.
func (h ModHooks) Count() int {
	total := 0
	for _, p := range hookPhases {
		total += len(h.For(p))
	}

	return total
}

// Clone deep copies the hooks, including each script's Env slice.
func (h ModHooks) Clone() ModHooks {
	out := ModHooks{}

	for _, p := range hookPhases {
		scripts := h.For(p)
		if scripts == nil {
			continue
		}

		cloned := make([]HookScript, len(scripts))
		for i, s := range scripts {
			s.Env = cloneStrings(s.Env)
			cloned[i] = s
		}

		out.Set(p, cloned)
	}

	return out
}

// HookContext is the data made available to a hook script.
type HookContext struct {
	Game   GameHookContext   `json:"game"`
	Config ConfigHookContext `json:"config"`
	Phase  HookPhase         `json:"phase"`
	Mod    ModHookContext    `json:"mod"`
	Index  int               `json:"index"`
}

// ModHookContext is the mod-specific slice of a hook context.
type ModHookContext struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// GameHookContext is the game-specific slice of a hook context.
type GameHookContext struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	ModsPath string `json:"mods_path"`
}

// ConfigHookContext is the manager-specific slice of a hook context.
type ConfigHookContext struct {
	CachePath   string `json:"cache_path"`
	TempPath    string `json:"temp_path"`
	StoragePath string `json:"storage_path"`
}
