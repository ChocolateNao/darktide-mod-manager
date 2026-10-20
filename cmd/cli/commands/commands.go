// Package commands implements the darktide-mod-manager command line interface.
// The one seam is Env.NewContainer, which cmd/cli points at internal/di, so a
// test runs a real command line end to end while capturing every byte it printed.
// Output is tables or --json, so it stays diffable and pipeable.
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

// Version is the build stamp reported by `dmm version`; it is a variable so the
// release build can set it with -ldflags.
var Version = "dev"

// Exit codes; three, so a caller can branch without reading the source.
const (
	// ExitOK is a command that did what it was asked.
	ExitOK = 0
	// ExitError is any failure.
	ExitError = 1
	// ExitCanceled is an interrupted operation (SIGINT convention, 128 + 2),
	// separate from ExitError so a retrying script can tell them apart.
	ExitCanceled = 130
)

// Env is everything a command needs from the outside world. The writers let a
// test capture output; they are real writers, never mocks.
type Env struct {
	Out io.Writer
	Err io.Writer
	// In supplies data for commands that read standard input, such as a
	// credential that must not land in the process list.
	In io.Reader
	// NewContainer builds the object graph a command works against.
	NewContainer func(ctx context.Context, opts di.Options) (*di.Container, error)
}

// withDefaults fills in writers a caller may leave nil. Discarding beats
// panicking: a panic says nothing about the command.
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

// session is the per-invocation state shared by every command in one run. The
// container is built at most once; eagerly would pay for a database even for
// `dmm --help`.
type session struct {
	env       Env
	buildErr  error
	container *di.Container
	opts      globalFlags
	mu        sync.Mutex
	built     bool
}

// acquire returns the container, building it on first use. A build failure is
// remembered so a second ask does not retry it.
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

// close releases the container, returning a failure for the entry point to print.
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

// diOptions turns the global flags into the options the container takes. An
// unset path stays empty: di treats a blank as "no opinion", and a default here
// would override the config file.
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

// logLevel resolves the effective level. --verbose is a shortcut for --log-level
// debug and turns the console sink on; the explicit flag wins.
func (s *session) logLevel() string {
	if level := strings.TrimSpace(s.opts.logLevel); level != "" {
		return level
	}

	if s.opts.verbose {
		return "debug"
	}

	return ""
}

// addLoggingFlags registers the logging flags on the root command, so a script
// can put them anywhere: `dmm --verbose mods list` equals `dmm mods list --verbose`.
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

// logger sends start-up diagnostics to standard error, where they cannot be
// mistaken for a command's own output. --quiet silences them.
func (s *session) logger() api.Logger {
	if s.opts.quiet {
		return api.LoggerFunc(func(string, ...any) {})
	}

	return api.LoggerFunc(func(format string, args ...any) {
		// Writers are swapped in tests after the session is built, so read them at
		// call time.
		fmt.Fprintf(s.env.Err, format+"\n", args...)
	})
}

// newCommand builds the command tree and its session. It is assembled per
// invocation: a cobra command carries flags and state that would leak.
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
		// A bare `dmm` prints help: an unrequested operation from someone pressing
		// enter is never right.
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

// NewRootCommand builds the whole command tree for a caller that runs it itself,
// such as a test or an embedder.
func NewRootCommand(env Env) *cobra.Command {
	root, _ := newCommand(env)
	return root
}

// Run executes the command line with a background context.
func Run(env Env, args []string) int {
	return RunContext(context.Background(), env, args)
}

// RunContext executes the command line under ctx. The context reaches the
// container and each command derives its own cancellable context from it, so one
// command finishing does not cancel the rest of the run.
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

// oneLine flattens an error to one line; a newline turns one failure into a
// paragraph. The cause is kept.
func oneLine(err error) string {
	text := strings.TrimSpace(err.Error())
	text = strings.ReplaceAll(text, "\r\n", " ")

	text = strings.ReplaceAll(text, "\n", " ")
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}

	return text
}

// exitCode reports the code an error should produce.
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

// newVersionCommand reports the build. It builds no container, so a version query
// works when the configuration is unreadable.
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

// rootCommandName is what this program calls itself in output and messages.
const rootCommandName = "dmm"

func modLabelOf(id string) string {
	if strings.TrimSpace(id) == "" {
		return "<unknown>"
	}

	return id
}

// plural agrees the noun with the count: "1 mods" makes a person distrust the
// rest of the screen.
func plural(count int, singular, many string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}

	return fmt.Sprintf("%d %s", count, many)
}

// sortedKeys returns a map's keys in order, because Go randomises map iteration
// and anything a user reads has to be identical across two runs over one state.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
