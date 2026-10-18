package domain

import (
	"context"
	"strings"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ActionIDSeparator is what joins a game id to an action's own name. The prefix is
// deliberate: it lets any of the three surfaces that speak action ids answer "which
// actions does this game have?" without a registry lookup.
const ActionIDSeparator = ":"

// ActionID identifies one game-specific action, e.g. "darktide:validate_loadorder". It
// is a string because the id travels as one through JSON-RPC, argv, and the registry;
// the helpers below are the only place its shape is decided.
type ActionID string

// String returns the id as a plain string.
func (a ActionID) String() string { return string(a) }

// Game returns the game id prefix, or "" when the id carries no separator. Callers
// needing a trustworthy prefix should use Validate, which rejects malformed ids.
func (a ActionID) Game() string {
	prefix, _, ok := strings.Cut(strings.TrimSpace(string(a)), ActionIDSeparator)
	if !ok {
		return ""
	}

	return prefix
}

// Suffix returns the action's own name with the game prefix removed.
func (a ActionID) Suffix() string {
	_, suffix, ok := strings.Cut(strings.TrimSpace(string(a)), ActionIDSeparator)
	if !ok {
		return strings.TrimSpace(string(a))
	}

	return suffix
}

// Validate reports whether the id is exactly "<gameID>:<name>" with a non-empty name on
// both sides. Strict rather than a prefix test, so "darktide:delete:everything" cannot
// be a valid id for the game "darktide:delete".
func (a ActionID) Validate(gameID string) error {
	game := strings.TrimSpace(gameID)
	whole := strings.TrimSpace(string(a))

	if game == "" || whole == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"an action id and a game id are both required")
	}

	prefix, suffix, ok := strings.Cut(whole, ActionIDSeparator)
	if !ok || prefix != game || strings.TrimSpace(suffix) == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action id %q must be %q followed by %q and a name", whole, game, ActionIDSeparator)
	}

	if strings.Contains(suffix, ActionIDSeparator) {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action id %q must contain exactly one %q", whole, ActionIDSeparator)
	}

	return nil
}

// NewActionID builds a validated id from a game id and an action name.
func NewActionID(gameID, name string) (ActionID, error) {
	game := strings.TrimSpace(gameID)
	trimmed := strings.TrimSpace(name)

	if trimmed == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"an action name is required")
	}

	id := ActionID(game + ActionIDSeparator + trimmed)

	if err := id.Validate(game); err != nil {
		return "", err
	}

	return id, nil
}

// ActionOptionType is the shape of an action option's value.
//
// The four kinds are those a frontend can render and a command line can parse without
// the manager knowing what the option means: text, checkbox, number, choice.
type ActionOptionType string

const (
	ActionOptionString ActionOptionType = "string"
	ActionOptionPath   ActionOptionType = "path"
	ActionOptionBool   ActionOptionType = "bool"
	ActionOptionNumber ActionOptionType = "number"
	ActionOptionSelect ActionOptionType = "select"
)

// IsValid reports whether the type is one this manager can coerce.
func (t ActionOptionType) IsValid() bool {
	switch t {
	case ActionOptionString, ActionOptionPath, ActionOptionBool,
		ActionOptionNumber, ActionOptionSelect:
		return true
	default:
		return false
	}
}

// ActionOption describes one argument an action takes. Options belong to the action's
// definition rather than to the caller: a frontend renders the form from them, the CLI
// parses flags from them, and the runner coerces them.
type ActionOption struct {
	Default     any
	Name        string
	Type        ActionOptionType
	Label       string
	Description string
	Placeholder string
	Choices     []string
	Required    bool
}

// Validate reports whether the option is well-formed on its own.
func (o ActionOption) Validate() error {
	if strings.TrimSpace(o.Name) == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"an action option needs a name")
	}

	if !o.Type.IsValid() {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q has unknown type %q", o.Name, o.Type)
	}

	if o.Type == ActionOptionSelect && len(o.Choices) == 0 {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q is a select with no choices", o.Name)
	}

	// A default on a required option would make "required" mean nothing.

	if o.Required && o.Default != nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q is required and cannot also have a default", o.Name)
	}

	if len(o.Choices) > 0 && o.Type != ActionOptionSelect {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q is not a select and cannot have choices", o.Name)
	}

	if o.Label == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q needs a label", o.Name)
	}

	return nil
}

// ActionPlatform restricts an action to the platforms it makes sense on. It is separate
// from Platform because an action with no restriction must be expressible without
// naming a platform.
type ActionPlatform string

const (
	ActionPlatformUniversal ActionPlatform = "universal"
	ActionPlatformWindows   ActionPlatform = "windows"
	ActionPlatformLinux     ActionPlatform = "linux"
	ActionPlatformDarwin    ActionPlatform = "darwin"
)

// ActionPlatformFor converts a runtime platform into the matching action restriction. A
// platform with no equivalent (the BSDs) maps to universal: nothing here is
// Windows-only by virtue of running on FreeBSD.
func ActionPlatformFor(p Platform) ActionPlatform {
	return ActionPlatform(p)
}

// IsValid reports whether the restriction is one this manager understands.
func (p ActionPlatform) IsValid() bool {
	switch p {
	case ActionPlatformUniversal, ActionPlatformWindows,
		ActionPlatformLinux, ActionPlatformDarwin:
		return true
	default:
		return false
	}
}

// Normalize returns the restriction when valid and universal otherwise. An unrecognised
// restriction is not an error at read time, or actions from a newer manager disappear.
func (p ActionPlatform) Normalize() ActionPlatform {
	if p.IsValid() {
		return p
	}

	return ActionPlatformUniversal
}

// Matches reports whether the action may run on target. An empty restriction means
// universal, so a definition that omits the field does the right thing.
func (p ActionPlatform) Matches(target Platform) bool {
	switch p.Normalize() {
	case ActionPlatformUniversal:
		return true
	default:
		return p == ActionPlatform(target)
	}
}

// ActionLogger is the slice of logging a handler needs — the same shape as api.Logger,
// redeclared so pkg/domain need not import pkg/api. A handler gets this rather than a
// *zerolog.Logger because its output belongs to whoever ran it.
type ActionLogger interface {
	Printf(format string, args ...any)
}

// ActionOpener lets a handler ask the operating system to open a folder or a URL. It is
// injected rather than imported so a handler stays testable without opening anything on
// the developer's desktop, and so the decision about which
// schemes may be opened lives in one place — the platform package — rather than
// being repeated by every handler.
type ActionOpener interface {
	OpenDirectory(ctx context.Context, dir string) error
	OpenURL(ctx context.Context, rawURL string) error
}

// ActionContext is everything a handler is allowed to depend on.
//
// The fields are values rather than lookups so that running an action is
// reproducible: the context is captured when the action is invoked, not
// re-read while it runs, and a game switch mid-run cannot change what an action
// is allowed to touch.
type ActionContext struct {
	Game         GameDefinition
	Opener       ActionOpener
	Logger       ActionLogger
	Args         map[string]any
	Platform     Platform
	GameID       string
	InstallPath  string
	ModsPath     string
	WorkingDir   string
	GameSettings GameSettings
}

// ArgString reads a string option, falling back to the empty string. A missing
// or wrongly typed option reads as empty rather than panicking: the runner has
// already validated the types, so this is a convenience for the common case.
func (a ActionContext) ArgString(name string) string {
	value, _ := a.Args[name].(string)

	return value
}

// ArgBool reads a boolean option, defaulting to false.
func (a ActionContext) ArgBool(name string) bool {
	value, _ := a.Args[name].(bool)

	return value
}

// ArgInt reads a whole-number option, defaulting to zero.
func (a ActionContext) ArgInt(name string) int {
	value, _ := a.Args[name].(int)

	return value
}

// Logf writes a line through the context's logger.
func (a ActionContext) Logf(format string, args ...any) {
	if a.Logger == nil {
		return
	}

	a.Logger.Printf(format, args...)
}

// GameAction is a code-defined action specific to one game.
//
// Actions live in Go, not configuration: a handler is a function, so a config
// could only name it, not supply it. Configuration chooses which are offered.
type GameAction struct {
	// `json:"-"` because `encoding/json` refuses a func type outright, and an
	// action list crosses the Wails bridge — an untagged Handler took the app
	// down. Behaviour is reached via ExecuteGameAction by ID.
	Handler      GameActionHandler `json:"-"`
	Script       *ActionScript
	ID           ActionID
	GameID       string
	DisplayName  string
	Description  string
	Category     string
	Icon         string
	Platform     ActionPlatform
	Options      []ActionOption
	RequiresGame bool
	Dangerous    bool
}

// AvailableOn reports whether the action may run on the given platform.
func (a GameAction) AvailableOn(target Platform) bool {
	return a.Platform.Matches(target)
}

// Handler runs an action's behaviour.
//
// The context.Context comes first and is the one the caller supplied, so a
// handler that shells out or waits on a file system operation honours the same
// cancellation the frontend's cancel button raises. The ActionContext is the
// captured state described above.
type GameActionHandler func(ctx context.Context, actx ActionContext) (*GameActionResult, error)

// GameActionResult is what running an action produced.
//
// Output carries the text a script wrote; Data carries whatever the handler
// chose to return as structured values, which is how an action reports findings
// (an out-of-date load order, a missing mod) without the frontend having to
// parse prose.
type GameActionResult struct {
	Data    any
	Message string
	Output  string
	Success bool
}

// ActionScript runs an action as an external program.
//
// The indirection exists because the useful operations for a game are often
// already written: launching the game, opening the mod folder, running a
// mod author's own validator. Declaring them as a per-platform command line
// means the manager does not have to grow a Go function per tool, while still
// keeping the decision in code where a reviewer can see it.
type ActionScript struct {
	Interpreter      string
	WorkDir          string
	Commands         []ActionCommand
	SuccessWhenEmpty bool
}

// Validate reports whether the script can be run on the host platform.
func (s ActionScript) Validate() error {
	if len(s.Commands) == 0 {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"an action script needs at least one command")
	}

	for i, command := range s.Commands {
		if err := command.Validate(); err != nil {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"action script command %d: %v", i, err)
		}
	}

	return nil
}

// CommandFor returns the command to run on target, preferring the most specific
// match so that a universal command can be overridden by a Windows one.
func (s ActionScript) CommandFor(target Platform) (ActionCommand, bool) {
	var fallback ActionCommand

	var found bool

	for _, command := range s.Commands {
		switch command.Platform.Normalize() {
		case ActionPlatformUniversal:
			if !found {
				fallback, found = command, true
			}
		case ActionPlatform(target):
			return command, true
		}
	}

	return fallback, found
}

// ActionCommand is one platform's spelling of a script action.
type ActionCommand struct {
	Path     string
	Platform ActionPlatform
	Args     []string
}

// Validate reports whether the command names a program.
func (c ActionCommand) Validate() error {
	if strings.TrimSpace(c.Path) == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "a command needs a path")
	}

	return nil
}

// ValidateActionID is the free-function form of ActionID.Validate, kept because
// callers that hold the two values separately read better with it.
func ValidateActionID(actionID ActionID, gameID string) error {
	return actionID.Validate(gameID)
}

// FormatActionArg is how an action option is named on a command line.
//
// It is one function rather than a convention so the CLI's flag name, the
// frontend's form field and the handler's Args key are the same string by
// construction: an option named "load_order_file" is always --load-order-file
// on the command line.
func FormatActionArg(name string) string {
	var out strings.Builder

	out.Grow(len(name))

	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			out.WriteRune(r + ('a' - 'A'))
		case r == '_' || r == '-' || r == '.':
			if i > 0 {
				out.WriteByte('-')
			}
		default:
			// A non-identifier character has no sensible flag form; dropping it
			// keeps the CLI usable rather than failing at parse time.
		}
	}

	if out.Len() == 0 {
		return "arg"
	}

	return out.String()
}
