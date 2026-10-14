// Package commands implements the darktide-mod-manager command line
// interface.
//
// # Where the wiring happens
//
// Nothing in this package builds a service. The one seam is Env.NewContainer,
// which the entry point in cmd/cli points at internal/di. That is deliberate:
// a command is a function of its arguments, its output writer and a container,
// and a test can therefore run a real command line end to end — a real
// container over a real SQLite database, a real archive, a real symlink — while
// still capturing every byte it printed. The alternative, mocking the services
// behind the commands, would have hidden the wiring bugs that only show up
// when the real collaborators are the ones disagreeing.
//
// # Output is a contract
//
// Every command that reads state writes a table, and every one of those tables
// has a --json form. The tables are for a person at a terminal; the JSON is for
// a script, and it is a deliberately separate set of types rather than the
// domain structs, because a domain struct's field names are not a promise
// anybody can make to a shell script. Nothing here writes colour, progress bars
// or a spinner: the output has to be diffable in a test and pipeable into
// another process.
//
// # Errors
//
// A failing command prints one line to standard error, prefixed with the
// manager's error code, and exits non-zero. Errors are never swallowed to keep
// a table tidy: a half-printed list is worse than none. The codes come from
// pkg/errors and callers can branch on them, so a script can tell "no such
// mod" from "the source is unreachable" without matching message text.
package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Version is the build stamp reported by `dmm version`. It is a variable
// rather than a constant so the release build can set it with -ldflags; the
// fallback says "dev" rather than lying about a version.
var Version = "dev"

// Exit codes. Only three, because a caller should not have to read the source
// to know what a failure meant.
const (
	// ExitOK is a command that did what it was asked.
	ExitOK = 0
	// ExitError is any failure.
	ExitError = 1
	// ExitCanceled is an operation the user interrupted, following the shell
	// convention for SIGINT (128 + 2). It is distinct from ExitError because
	// "I pressed ctrl-c" is not the same event as "this failed", and a retrying
	// script wants to know the difference.
	ExitCanceled = 130
)

// Env is everything a command needs from the outside world.
//
// The writers exist so a test can capture output instead of printing it, and
// the container factory exists so the tests can be handed a real container
// built over a temporary directory while the production entry point hands in
// the real di.NewContainer. Neither is a mock: a mock would let a command pass
// while the thing it calls is broken.
type Env struct {
	// Out receives command output.
	Out io.Writer
	// Err receives error messages and diagnostics.
	Err io.Writer
	// In supplies data for commands that read from standard input, such as
	// sourcing a credential without putting it in the process list.
	In io.Reader
	// NewContainer builds the object graph a command works against.
	NewContainer func(ctx context.Context, opts di.Options) (*di.Container, error)
}

// withDefaults fills in the writers that a caller may leave nil, so a command
// can write unconditionally instead of checking every time. Discarding is
// better than panicking: a nil Out is a mistake in a test, and a panic there
// tells the reader nothing about the command.
func (e Env) withDefaults() Env {
	if e.Out == nil {
		e.Out = io.Discard
	}

	if e.Err == nil {
		e.Err = io.Discard
	}

	if e.In == nil {
		e.In = strings.NewReader("")
	}

	return e
}

// globalFlags are the options every command shares.
type globalFlags struct {
	configPath  string
	cachePath   string
	storagePath string
	gameID      string
	logLevel    string
	logDir      string
	asJSON      bool
	quiet       bool
	logConsole  bool
	noLogFile   bool
	verbose     bool
}

// session is the per-invocation state shared by every command in one run: the
// flags, the writers, and the container.
//
// The container is built at most once and closed exactly once. Building it
// eagerly would pay for opening a database and running start-up diagnostics
// even for `dmm --help` or `dmm version`; building it per command would open
// and close the same database several times in one invocation, and a container
// that is rebuilt mid-run could disagree with itself.
type session struct {
	env       Env
	buildErr  error
	container *di.Container
	opts      globalFlags
	mu        sync.Mutex
	built     bool
}

// acquire returns the container, building it on first use. A build failure is
// remembered so a command that asks twice does not retry a start-up that has
// already been shown to fail.
func (s *session) acquire(ctx context.Context) (*di.Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.built {
		return s.container, s.buildErr
	}

	s.built = true
	if s.env.NewContainer == nil {
		s.buildErr = appErrors.New(appErrors.ErrCodeInvalidArgument,
			"this build cannot start the manager: no container factory is configured")

		return nil, s.buildErr
	}

	container, err := s.env.NewContainer(ctx, s.diOptions())
	if err != nil {
		s.buildErr = err
		return nil, err
	}

	s.container = container

	return container, nil
}

// client returns the API client, which every command talks to.
func (s *session) client(ctx context.Context) (*api.Client, error) {
	container, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}

	if container.Client == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"the container has no client")
	}

	return container.Client, nil
}

// close releases the container. A close failure is returned so the entry point
// can report it, but only after every other cleanup has run.
func (s *session) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.container == nil {
		s.container = nil
		return nil
	}

	container := s.container
	s.container = nil

	return container.Close()
}

// diOptions turns the global flags into the options the container takes.
//
// An unset path is passed through as the empty string rather than as a
// guess: di treats a blank path as "the user has no opinion", and inventing a
// default here would override the configuration file behind the user's back.
func (s *session) diOptions() di.Options {
	return di.Options{
		ConfigPath:  s.opts.configPath,
		GameID:      s.opts.gameID,
		CachePath:   s.opts.cachePath,
		StoragePath: s.opts.storagePath,
		Logger:      s.logger(),
		LogLevel:    s.logLevel(),
		LogDir:      strings.TrimSpace(s.opts.logDir),
		LogConsole:  s.opts.logConsole || s.opts.verbose,
		NoLogFile:   s.opts.noLogFile,
	}
}

// logLevel resolves the effective level for this run.
//
// --verbose is a shortcut for --log-level debug, and it is also what turns the
// console sink on: someone who asked for debug output is watching a terminal,
// and sending it only to a file they have not opened would be a strange reading
// of the request. The explicit flag wins over the shortcut so `dmm --verbose
// --log-level error mods list` means what it says.
func (s *session) logLevel() string {
	if level := strings.TrimSpace(s.opts.logLevel); level != "" {
		return level
	}

	if s.opts.verbose {
		return "debug"
	}

	return ""
}

// addLoggingFlags registers the global logging flags.
//
// They live on the root command so a script can put them anywhere on the line,
// which is the point of a global flag: `dmm --verbose mods list` and `dmm mods
// list --verbose` should not be different invocations.
func (s *session) addLoggingFlags(flags *pflag.FlagSet) {
	flags.BoolVar(&s.opts.verbose, "verbose", false, "log at debug level and print to the console")
	flags.StringVar(
		&s.opts.logLevel,
		"log-level",
		"",
		"log at this level: trace, debug, info, warn or error",
	)
	flags.StringVar(
		&s.opts.logDir,
		"log-dir",
		"",
		"write the log to this directory instead of the platform default",
	)
	flags.BoolVar(
		&s.opts.logConsole,
		"log-console",
		false,
		"print log lines to standard error as well as to the file",
	)
	flags.BoolVar(
		&s.opts.noLogFile,
		"no-log-file",
		false,
		"do not write a log file; log to the console only",
	)
}

// logger sends the container's start-up diagnostics to standard error, so they
// cannot be mistaken for a command's own output — which is what a script
// parses. --quiet silences it.
func (s *session) logger() api.Logger {
	if s.opts.quiet {
		return api.LoggerFunc(func(string, ...any) {})
	}

	return api.LoggerFunc(func(format string, args ...any) {
		// The writers are swapped in tests after the session is built, so the
		// logger reads them at call time rather than capturing one.
		fmt.Fprintf(s.env.Err, format+"\n", args...)
	})
}

// newCommand builds the command tree and the session that goes with it.
//
// The tree is assembled here rather than in a package-level variable because a
// cobra command holds flags and state: a shared one would carry the first
// invocation's flags into the second, which matters the moment a test runs two
// command lines in one process. The session comes back alongside so the caller
// that owns a process — rather than a single command — can close it.
func newCommand(env Env) (*cobra.Command, *session) {
	env = env.withDefaults()
	s := &session{env: env}

	root := &cobra.Command{
		Use:   "dmm",
		Short: "Manage Darktide mods",
		Long: "dmm installs, updates and enables Darktide mods from local folders\n" +
			"and from the sources configured in its settings file.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// A bare `dmm` prints help rather than doing something. Running an
		// unrequested operation because someone pressed enter at a prompt is
		// never the right default.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		Args: cobra.NoArgs,
	}
	root.SetOut(env.Out)
	root.SetErr(env.Err)
	root.SetIn(env.In)
	root.SetVersionTemplate("{{.Version}}\n")
	root.Version = Version

	flags := root.PersistentFlags()
	flags.StringVar(
		&s.opts.configPath,
		"config",
		"",
		"use this configuration file instead of the default",
	)
	flags.StringVar(&s.opts.cachePath, "cache", "", "use this download cache directory")
	flags.StringVar(&s.opts.storagePath, "storage", "", "use this mod storage directory")
	flags.StringVar(
		&s.opts.gameID,
		"game",
		"",
		"use this game id for this run (see `dmm game list`)",
	)
	flags.BoolVar(&s.opts.asJSON, "json", false, "print machine-readable JSON instead of a table")
	flags.BoolVar(&s.opts.quiet, "quiet", false, "suppress start-up diagnostics")
	s.addLoggingFlags(flags)

	root.AddCommand(
		newModCommand(s),
		newSourceCommand(s),
		newGameCommand(s),
		newCacheCommand(s),
		newConfigCommand(s),
		newLogCommand(s),
		newManifestCommand(s),
		newShareCommand(s),
		newActionsCommand(s),
		newVersionCommand(s),
	)

	return root, s
}

// NewRootCommand builds the whole command tree for a caller that wants to run
// it itself — the Wails process does not, but a test or an embedder might.
func NewRootCommand(env Env) *cobra.Command {
	root, _ := newCommand(env)
	return root
}

// Run executes the command line with a background context and returns the
// process exit code.
func Run(env Env, args []string) int {
	return RunContext(context.Background(), env, args)
}

// RunContext executes the command line under ctx and returns the process exit
// code.
//
// The context is given to the container as well as to the commands. That is
// deliberate and slightly unusual: the container is built once for the whole
// invocation, so a context that dies with the first command would leave a
// container that is already cancelled for the rest of the run. Each command
// therefore derives its own cancellable context from ctx — see base.ready in
// internal/commands for the same arrangement on the GUI side.
//
// The error, if there is one, is reported to standard error here so a caller
// sees it exactly once and in one place. A failure to close the container is
// reported too, and separately: it is a different problem from the command's
// own failure, and folding the two into one message would hide whichever is
// more interesting.
func RunContext(ctx context.Context, env Env, args []string) int {
	env = env.withDefaults()
	root, s := newCommand(env)

	if ctx == nil {
		ctx = context.Background()
	}

	root.SetContext(ctx)
	root.SetArgs(args)
	runErr := root.Execute()
	closeErr := s.close()

	if runErr != nil {
		fmt.Fprintf(env.Err, "error: %s\n", oneLine(runErr))
	}

	if closeErr != nil {
		fmt.Fprintf(
			env.Err,
			"warning: could not release the manager cleanly: %s\n",
			oneLine(closeErr),
		)
	}

	return exitCode(runErr)
}

// oneLine renders an error as a single line, because an error message with a
// newline in it turns one failure into a paragraph of output a caller has to
// parse. The cause is included: "NOT_FOUND: mod "x" is not installed: sql:
// no rows" is one line and still says everything.
func oneLine(err error) string {
	text := strings.TrimSpace(err.Error())
	text = strings.ReplaceAll(text, "\r\n", " ")

	text = strings.ReplaceAll(text, "\n", " ")
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}

	return text
}

// exitCode reports the code an error should produce, exposed for the tests that
// drive the entry point's decision without running a process.
func exitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		errors.Is(err, context.Canceled):
		return ExitCanceled
	default:
		return ExitError
	}
}

// newVersionCommand reports the build. It deliberately builds no container: a
// version query is the one command that must work when the configuration file
// is unreadable, the disk is full, or the manager is misconfigured.
//
// The program name is printed as well as the version, because `dmm version` is
// what someone pastes into a bug report and a bare "dev" in the middle of a
// pasted block says nothing about which program produced it.
func newVersionCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]string{
					"name":    rootCommandName,
					"version": Version,
				})
			}

			_, err := fmt.Fprintf(s.env.Out, "%s %s\n", rootCommandName, Version)

			return err
		},
	}
}

// rootCommandName is what this program calls itself, in the version output and in
// the few messages that name the command a person should run next.
const rootCommandName = "dmm"

// modLabelOf renders a mod id for a message without leaking a blank one.
func modLabelOf(id string) string {
	if strings.TrimSpace(id) == "" {
		return "<unknown>"
	}

	return id
}

// plural renders a count with a noun that agrees with it.
//
// A CLI is read more often than it is written, and "1 mods" is the kind of
// small wrongness that makes a person distrust everything else on the screen.
// "1 of 4 mods" is deliberately not handled here: the noun in that sentence
// agrees with the second number and is plural whatever the first is.
func plural(count int, singular, many string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}

	return fmt.Sprintf("%d %s", count, many)
}

// sortedKeys is used where a stable order matters and the input is a map. Go
// randomises map iteration, so anything a user reads has to be sorted before it
// is printed or it will differ between two runs over identical state.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
