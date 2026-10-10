package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/spf13/cobra"
)

// newModHooksCommand assembles `dmm mod hooks`.
//
// Every subcommand here is a read-modify-write over one mod's whole hook set,
// because that is the only write the manager offers: UpdateModHooks replaces
// the set rather than merging into it. Reading the current set first is
// therefore not an optimisation, it is the only way to add one script without
// losing the others — and it is also why a change racing another change loses
// one of them, which is stated in the help rather than papered over.
func newModHooksCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "hooks",
		Short: "Inspect and edit the hook scripts of a mod",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newHooksListCommand(s),
		newHooksAddCommand(s),
		newHooksRemoveCommand(s),
		newHooksEditCommand(s),
	)

	return group
}

// hookView is the JSON shape of one script in `dmm mod hooks list`.
type hookView struct {
	Phase      string   `json:"phase"`
	Type       string   `json:"type"`
	WorkingDir string   `json:"working_dir,omitempty"`
	Content    string   `json:"content,omitempty"`
	Env        []string `json:"env,omitempty"`
	Position   int      `json:"position"`
	Timeout    int      `json:"timeout,omitempty"`
	// EffectiveTimeout is the timeout the executor would actually apply — the
	// declared one after the configured default, game, and permission caps —
	// in seconds. It differs from Timeout whenever the declared value is zero
	// or clamped.
	EffectiveTimeout int `json:"effective_timeout"`
}

func newHooksListCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "list <mod>",
		Short: "List a mod's hook scripts",
		Long: "List a mod's hook scripts.\n\n" +
			"TIMEOUT is what the script declares (default = nothing declared);\n" +
			"EFFECTIVE is what the executor will apply once the configured\n" +
			"floor, ceiling, and permission caps are taken into account.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			hooks, err := client.GetModHooks(ctx, args[0])
			if err != nil {
				return err
			}

			// The mod is what permission scopes hang off, so the effective
			// timeout is only exact with it in hand. The hooks were readable,
			// so the mod exists; a failure here is still tolerated rather than
			// failing the listing — the columns would then show the
			// game-and-default scopes only.
			mod, err := client.GetMod(ctx, args[0])
			if err != nil {
				mod = nil
			}

			table := NewTable("PHASE", "#", "TYPE", "TIMEOUT", "EFFECTIVE", "SCRIPT")

			views := make([]hookView, 0, hooks.Count())
			for _, phase := range domain.HookPhases() {
				for position, script := range hooks.For(phase) {
					effective := client.HookTimeout(ctx, script, mod)

					table.Add(string(phase), strconv.Itoa(position+1), string(script.Language),
						orDash(timeoutText(script.Timeout)),
						effectiveText(effective),
						Clip(script.Content))
					views = append(views, hookView{
						Phase:            string(phase),
						Position:         position + 1,
						Type:             string(script.Language),
						Timeout:          script.Timeout,
						EffectiveTimeout: int(effective.Seconds()),
						WorkingDir:       script.WorkingDir,
						Env:              script.Env,
						Content:          script.Content,
					})
				}
			}

			return s.writeJSONOrTable(s.env.Out, views, table,
				"This mod has no hook scripts.")
		},
	}
}

func newHooksAddCommand(s *session) *cobra.Command {
	var (
		phase     string
		hookType  string
		content   string
		file      string
		timeout   int
		workingTo string
		envVars   []string
	)

	command := &cobra.Command{
		Use:   "add <mod>",
		Short: "Add a hook script",
		Long: "Add a hook script to one lifecycle phase of a mod.\n\n" +
			"--content is the script itself; --file reads the content from a file\n" +
			"instead, which is how a long Lua or JavaScript hook is written without\n" +
			"fighting the shell's quoting.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedPhase, err := parseHookPhase(phase)
			if err != nil {
				return err
			}

			lang, err := parseHookLanguage(hookType)
			if err != nil {
				return err
			}

			body, err := hookContent(content, file)
			if err != nil {
				return err
			}

			for _, entry := range envVars {
				if err := validateEnvEntry(entry); err != nil {
					return err
				}
			}

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			modID := args[0]

			hooks, err := client.GetModHooks(cmd.Context(), modID)
			if err != nil {
				return err
			}

			script := domain.HookScript{
				Language:   lang,
				Content:    body,
				Timeout:    timeout,
				WorkingDir: strings.TrimSpace(workingTo),
				Env:        envVars,
			}
			existing := hooks.For(parsedPhase)
			existing = append(existing, script)
			hooks.Set(parsedPhase, existing)

			if err := client.UpdateModHooks(cmd.Context(), modID, hooks); err != nil {
				return err
			}

			_, err = fmt.Fprintf(s.env.Out, "added %s script to %s of %s\n",
				lang, parsedPhase, modID)

			return err
		},
	}
	flags := command.Flags()
	flags.StringVar(&phase, "phase", "", "lifecycle phase: "+phaseNames())
	flags.StringVar(
		&hookType,
		"type",
		string(domain.LangLua),
		"script language: "+hookLanguageNames(),
	)
	flags.StringVar(&content, "content", "", "the script itself")
	flags.StringVar(&file, "file", "", "read the script from this file")
	flags.IntVar(
		&timeout,
		"timeout",
		0,
		"seconds this script may run for (0 uses the configured default)",
	)
	flags.StringVar(&workingTo, "workdir", "", "directory to run in, inside the mod or game folder")
	flags.StringArrayVar(&envVars, "env", nil, "environment variable as NAME=VALUE (repeatable)")

	return command
}

func newHooksRemoveCommand(s *session) *cobra.Command {
	var (
		phase string
		index int
		all   bool
	)

	command := &cobra.Command{
		Use:   "remove <mod>",
		Short: "Remove hook scripts",
		Long: "Remove a mod's hook scripts for one phase.\n\n" +
			"Without --index the whole phase is cleared; with it only that one\n" +
			"script goes. The position is 1-based, matching what `hooks list`\n" +
			"prints, and counts only the scripts in that phase.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedPhase, err := parseHookPhase(phase)
			if err != nil {
				return err
			}
			// Whether --index was given at all has to be asked of the flag set, not
			// of the value: 0 is the zero value of the variable whether the user
			// typed --index 0 or typed nothing, and reading the value would make
			// "--index 0" — which names no script, because the position is 1-based —
			// silently clear the whole phase instead.
			givenIndex := cmd.Flags().Changed("index")
			if givenIndex && index <= 0 {
				return fatalf("--index is 1-based, got %d; 0 names no script", index)
			}

			if givenIndex && all {
				return fatalf("--index and --all cannot be combined")
			}

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			modID := args[0]

			hooks, err := client.GetModHooks(cmd.Context(), modID)
			if err != nil {
				return err
			}

			existing := hooks.For(parsedPhase)
			switch {
			case all || !givenIndex:
				removed := len(existing)

				hooks.Set(parsedPhase, nil)

				if err := client.UpdateModHooks(cmd.Context(), modID, hooks); err != nil {
					return err
				}

				_, err = fmt.Fprintf(s.env.Out, "removed %s from %s of %s\n",
					plural(removed, "script", "scripts"), parsedPhase, modID)

				return err
			case index > len(existing):
				return fatalf("%s of %s has %s; there is no script %d",
					parsedPhase, modID, plural(len(existing), "script", "scripts"), index)
			default:
				// Removing from the middle keeps the other scripts' order, which
				// matters: two hooks in one phase run in listing order and a
				// setup script that ran after the one it prepares would break.
				kept := make([]domain.HookScript, 0, len(existing)-1)
				kept = append(kept, existing[:index-1]...)
				kept = append(kept, existing[index:]...)
				hooks.Set(parsedPhase, kept)

				if err := client.UpdateModHooks(cmd.Context(), modID, hooks); err != nil {
					return err
				}

				_, err = fmt.Fprintf(s.env.Out, "removed script %d of %s from %s\n",
					index, parsedPhase, modID)

				return err
			}
		},
	}
	flags := command.Flags()
	flags.StringVar(&phase, "phase", "", "lifecycle phase: "+phaseNames())
	flags.IntVar(&index, "index", 0, "remove only this 1-based position in the phase")
	flags.BoolVar(&all, "all", false, "remove every script in the phase (the default)")

	return command
}

func newHooksEditCommand(s *session) *cobra.Command {
	var from string

	command := &cobra.Command{
		Use:   "edit <mod>",
		Short: "Print a mod's hooks, or replace them from a file",
		Long: "Print a mod's hook set as JSON, or replace it with the JSON read\n" +
			"from --from.\n\n" +
			"This command has to work over a pipe and in a test, and an editor\n" +
			"it launches is neither. Print, edit the file, and feed it back with\n" +
			"--from instead; the printed shape is exactly what --from reads.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			modID := args[0]

			hooks, err := client.GetModHooks(cmd.Context(), modID)
			if err != nil {
				return err
			}

			if strings.TrimSpace(from) == "" {
				return writeJSON(s.env.Out, hooks)
			}

			replacement, err := readHooksFile(from)
			if err != nil {
				return err
			}

			if err := client.UpdateModHooks(cmd.Context(), modID, replacement); err != nil {
				return err
			}

			_, err = fmt.Fprintf(s.env.Out, "replaced the hooks of %s with %s (%d scripts)\n",
				modID, from, replacement.Count())

			return err
		},
	}
	command.Flags().StringVar(
		&from,
		"from",
		"",
		"read the replacement hook set from this JSON file",
	)

	return command
}

// readHooksFile reads a hook set from JSON on disk.
//
// Every failure is reported with the file name: "unexpected end of JSON input"
// with no path sends the user looking through their shell history for which
// file they just wrote.
func readHooksFile(path string) (api.ModHooks, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return api.ModHooks{}, appErrors.Wrap(err, appErrors.ErrCodeNotFound,
			"cannot read hooks from %s", path)
	}

	if len(strings.TrimSpace(string(data))) == 0 {
		return api.ModHooks{}, fatalf("%s is empty; it must hold a JSON hook set", path)
	}

	var hooks api.ModHooks
	if err := json.Unmarshal(data, &hooks); err != nil {
		return api.ModHooks{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeInvalidArgument,
			"cannot parse hooks from %s: it must be a JSON object of phase names to script lists",
			path,
		)
	}

	return hooks, nil
}

// hookContent returns the script body from --content or --file.
//
// Exactly one of the two must be given. Two blank bodies reach the executor as
// a hook that does nothing, and a hook that does nothing still runs the
// process and still fails on a timeout; a script with no content is a mistake,
// so it is refused here where the message can say so.
func hookContent(content, file string) (string, error) {
	content = strings.TrimSpace(content)

	file = strings.TrimSpace(file)
	switch {
	case content == "" && file == "":
		return "", fatalf("give the script with --content or --file")
	case content != "" && file != "":
		return "", fatalf("--content and --file cannot be combined")
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", appErrors.Wrap(err, appErrors.ErrCodeNotFound, "cannot read script %s", file)
		}

		return string(data), nil
	default:
		return content, nil
	}
}

// validateEnvEntry checks a NAME=VALUE pair before it reaches the manager.
//
// The executor refuses a malformed entry too, but it refuses it after the mod
// has been fetched and the command has printed nothing useful. Checking here
// means the mistake is reported before any work is done.
func validateEnvEntry(entry string) error {
	name, _, found := strings.Cut(entry, "=")
	if !found {
		return fatalf("--env %q is not NAME=VALUE", entry)
	}

	if strings.TrimSpace(name) == "" {
		return fatalf("--env %q has an empty name", entry)
	}

	return nil
}

// parseHookPhase turns a --phase argument into a phase.
func parseHookPhase(text string) (domain.HookPhase, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", fatalf("--phase is required; use one of %s", phaseNames())
	}

	phase, ok := domain.ParseHookPhase(trimmed)
	if !ok {
		return "", fatalf("unknown phase %q; use one of %s", trimmed, phaseNames())
	}

	return phase, nil
}

// parseHookLanguage turns a --type argument into a script language.
//
// The comparison is case-insensitive because a shell user types `LUA` and
// means lua, but the stored value is always the canonical lower-case one: the
// executor's registry is keyed by the exact language, so a capitalised value
// would find no runner and fail at enable time. The flag keeps its --type
// name because that is what scripts and muscle memory already use.
func parseHookLanguage(text string) (domain.ScriptLanguage, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", fatalf("--type is required; use one of %s", hookLanguageNames())
	}

	lang, err := domain.ParseScriptLanguage(trimmed)
	if err != nil {
		return "", fatalf(
			"unknown script language %q; use one of %s",
			trimmed,
			hookLanguageNames(),
		)
	}

	return lang, nil
}

func phaseNames() string {
	phases := make([]string, 0, len(domain.HookPhases()))
	for _, phase := range domain.HookPhases() {
		phases = append(phases, string(phase))
	}

	return strings.Join(phases, ", ")
}

func hookLanguageNames() string {
	langs := make([]string, 0, len(domain.ScriptLanguages()))
	for _, lang := range domain.ScriptLanguages() {
		langs = append(langs, string(lang))
	}

	return strings.Join(langs, ", ")
}

// timeoutText renders a script's timeout, where zero means "use the configured
// default" and printing 0s would read as "no time at all".
func timeoutText(seconds int) string {
	if seconds <= 0 {
		return "default"
	}

	return strconv.Itoa(seconds) + "s"
}

// effectiveText renders the timeout the executor will apply. Zero here means
// no executor is wired (or the duration rounded away), and "-" says that
// better than "default" would: this column is an answer, not a declaration.
func effectiveText(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Second:
		return "<1s"
	case d%time.Second == 0:
		return strconv.Itoa(int(d.Seconds())) + "s"
	default:
		return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
	}
}
