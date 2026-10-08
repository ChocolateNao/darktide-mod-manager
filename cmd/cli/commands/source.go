package commands

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/spf13/cobra"
)

// newSourceCommand assembles `dmm source`.
func newSourceCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "source",
		Short: "List, search and authenticate mod sources",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newSourceListCommand(s),
		newSourceSearchCommand(s),
		newSourceAuthCommand(s),
		newSourceTestCommand(s),
		newSourceEnableCommand(s),
		newSourceDisableCommand(s),
	)

	return group
}

// sourceView is the JSON shape of one source.
type sourceView struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	Description    string `json:"description,omitempty"`
	Version        string `json:"version,omitempty"`
	SupportsSearch bool   `json:"supports_search"`
	SupportsAuth   bool   `json:"supports_auth"`
	RequiresAPIKey bool   `json:"requires_api_key"`
	HasAPIKey      bool   `json:"has_api_key"`
	Enabled        bool   `json:"enabled"`
	Authenticated  bool   `json:"authenticated"`
}

func newSourceView(info api.ModSourceInfo) sourceView {
	return sourceView{
		ID:             info.ID,
		Name:           info.Name,
		Description:    info.Description,
		Version:        info.Version,
		SupportsSearch: info.SupportsSearch,
		SupportsAuth:   info.SupportsAuth,
		RequiresAPIKey: info.RequiresAPIKey,
		HasAPIKey:      info.HasAPIKey,
		Enabled:        info.Enabled,
		Authenticated:  info.Authenticated,
	}
}

func newSourceListCommand(s *session) *cobra.Command {
	var withDetails bool

	command := &cobra.Command{
		Use:   "list",
		Short: "List the configured sources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			infos := client.ListSources(cmd.Context())
			table := NewTable("ID", "NAME", "ENABLED", "AUTH", "SEARCH", "NEEDS KEY", "HAS KEY")

			views := make([]sourceView, 0, len(infos))
			for _, info := range infos {
				table.Add(info.ID, orDash(info.Name), yesNo(info.Enabled),
					yesNo(info.Authenticated), yesNo(info.SupportsSearch),
					yesNo(info.RequiresAPIKey), yesNo(info.HasAPIKey))
				views = append(views, newSourceView(info))
			}

			if !withDetails {
				return s.writeJSONOrTable(s.env.Out, views, table, "No sources are registered.")
			}
			// With --details every source gets a field block too, so a
			// description is not squeezed into a column. The table still comes
			// first, because the table is what a person scans.
			if err := s.writeJSONOrTable(
				s.env.Out,
				views,
				table,
				"No sources are registered.",
			); err != nil {
				return err
			}

			if s.opts.asJSON {
				return nil
			}

			for _, info := range infos {
				fields := &Fields{}
				fields.Set("id", info.ID)
				fields.SetOrDash("name", info.Name)
				fields.SetOrDash("version", info.Version)
				fields.SetOrDash("description", info.Description)
				fields.Set("enabled", yesNo(info.Enabled))
				fields.Set("authenticated", yesNo(info.Authenticated))
				fields.Set("can search", yesNo(info.SupportsSearch))
				fields.Set("can authenticate", yesNo(info.SupportsAuth))
				fields.Set("needs a key", yesNo(info.RequiresAPIKey))
				fields.Set("has a key", yesNo(info.HasAPIKey))

				if err := fields.Render(s.env.Out); err != nil {
					return err
				}

				fmt.Fprintln(s.env.Out)
			}

			return nil
		},
	}
	command.Flags().BoolVar(&withDetails, "details", false, "also print a detail block per source")

	return command
}

func newSourceSearchCommand(s *session) *cobra.Command {
	var (
		sourceID string
		tags     []string
		gameVers string
		page     int
		pageSize int
		sortBy   string
	)

	command := &cobra.Command{
		Use:   "search [query]",
		Short: "Search one source, or every enabled source",
		Long: "Search for mods.\n\n" +
			"Without --source the search fans out to every enabled source that can\n" +
			"search, and a source that fails is skipped rather than failing the\n" +
			"whole command: a user asking \"what is out there\" wants an answer\n" +
			"from the sources that are working.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			text := ""
			if len(args) == 1 {
				text = args[0]
			}

			ctx := cmd.Context()

			mods, err := searchMods(
				ctx,
				client,
				sourceID,
				text,
				tags,
				gameVers,
				page,
				pageSize,
				sortBy,
			)
			if err != nil {
				return err
			}

			table := NewTable("ID", "VERSION", "SOURCE", "NAME")

			views := make([]modView, 0, len(mods))
			for _, mod := range mods {
				table.Add(mod.ID, orDash(mod.Version), orDash(mod.SourceID), Clip(mod.Name))
				views = append(views, newModView(mod))
			}

			nothing := "Nothing matched."
			if strings.TrimSpace(text) == "" {
				nothing = "No mods were found."
			}

			return s.writeJSONOrTable(s.env.Out, views, table, nothing)
		},
	}
	flags := command.Flags()
	flags.StringVar(&sourceID, "source", "", "search only this source")
	flags.StringArrayVar(&tags, "tag", nil, "require this tag (repeatable, all must match)")
	flags.StringVar(&gameVers, "game-version", "", "require compatibility with this game version")
	flags.IntVar(&page, "page", 0, "page number (0 means the first)")
	flags.IntVar(&pageSize, "page-size", 0, "results per page (0 uses the source's default)")
	flags.StringVar(&sortBy, "sort", "", "sort: relevance, updated, downloads or name")

	return command
}

// searchMods performs the search the command asked for, through the public
// client.
//
// The two forms are separate questions, and the single-source form can only
// carry a text query: SearchSource takes a string, not a SearchQuery, because
// it is the one a person types. The extra filters are therefore refused rather
// than quietly dropped — a user who typed --tag and got unfiltered results has
// been told something untrue by omission, and would have to notice to catch it.
func searchMods(ctx context.Context, client *api.Client, sourceID, text string,
	tags []string, gameVersion string, page, pageSize int, sortBy string,
) ([]api.Mod, error) {
	sourceID = strings.TrimSpace(sourceID)
	if err := validateSort(strings.TrimSpace(sortBy)); err != nil {
		return nil, err
	}

	if sourceID == "" {
		query := api.SearchQuery{
			Query:       strings.TrimSpace(text),
			Tags:        tags,
			GameVersion: strings.TrimSpace(gameVersion),
			Page:        page,
			PageSize:    pageSize,
			SortBy:      strings.TrimSpace(sortBy),
		}

		return client.Search(ctx, query)
	}

	if extra := extraFilterNames(tags, gameVersion, page, pageSize, sortBy); len(extra) > 0 {
		return nil, fatalf(
			"--source searches one source with a text query only, so %s cannot be used with it; "+
				"drop --source to search every enabled source with those filters",
			strings.Join(extra, " and "))
	}

	return client.SearchSource(ctx, sourceID, strings.TrimSpace(text))
}

// extraFilterNames lists the filters a single-source search cannot carry.
func extraFilterNames(
	tags []string,
	gameVersion string,
	page, pageSize int,
	sortBy string,
) []string {
	var names []string
	if len(tags) > 0 {
		names = append(names, "--tag")
	}

	if strings.TrimSpace(gameVersion) != "" {
		names = append(names, "--game-version")
	}

	if page != 0 {
		names = append(names, "--page")
	}

	if pageSize != 0 {
		names = append(names, "--page-size")
	}

	if strings.TrimSpace(sortBy) != "" {
		names = append(names, "--sort")
	}

	return names
}

// validateSort checks the --sort argument rather than letting an unknown value
// reach the sources, which each handle it differently and one of which would
// silently ignore it.
func validateSort(sortBy string) error {
	switch sortBy {
	case "", domain.SortRelevance, domain.SortUpdated, domain.SortDownloads, domain.SortName:
		return nil
	default:
		return fatalf("unknown sort %q; use relevance, updated, downloads or name", sortBy)
	}
}

func newSourceAuthCommand(s *session) *cobra.Command {
	var (
		user        string
		apiKey      string
		apiKeyStdin bool
		envVar      string
		noVerify    bool
		forget      bool
	)

	command := &cobra.Command{
		Use:   "auth <source>",
		Short: "Store or forget a source's credentials",
		Long: "Store a source's credentials, or forget them again.\n\n" +
			"The key can come from --api-key, from standard input with\n" +
			"--api-key-stdin, or from an environment variable. Prefer one of the\n" +
			"last two: a key passed as an argument is visible in the process list\n" +
			"to every other user on the machine and lands in the shell history.\n\n" +
			"With none of those given, the key is asked for on the terminal, with\n" +
			"the typing hidden. Nothing is stored and nothing is sent to the source\n" +
			"until you press Enter; Ctrl-C, Ctrl-D and an empty line all cancel.\n" +
			"Standard output stays free of the prompt, so this is still safe to\n" +
			"pipe. The prompt appears only when standard input is a terminal, so a\n" +
			"script is never made to answer one and can set the environment\n" +
			"variable named below instead.\n\n" +
			"The key is checked against the source before it is stored, so a typo\n" +
			"fails here rather than at the first download. Pass --no-verify to store\n" +
			"it anyway, which is what you want on a machine that cannot reach the\n" +
			"source yet.\n\n" +
			"--forget removes the stored key instead of setting one.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceID := strings.TrimSpace(args[0])

			if forget {
				if key, fromStdin, named := keyFlagsGiven(
					apiKey,
					apiKeyStdin,
					envVar,
				); key != "" || fromStdin ||
					named {
					return fatalf("--forget cannot be combined with a key or with --user")
				}

				if strings.TrimSpace(user) != "" {
					return fatalf("--forget cannot be combined with a key or with --user")
				}

				if noVerify {
					return fatalf("--forget and --no-verify ask for opposite things")
				}

				client, err := s.client(cmd.Context())
				if err != nil {
					return err
				}

				if err := client.ClearSourceAPIKey(cmd.Context(), sourceID); err != nil {
					return err
				}

				_, err = fmt.Fprintf(s.env.Out, "forgot credentials for %s\n", sourceID)

				return err
			}

			key, err := s.resolveAPIKey(cmd.Context(), sourceID, apiKey, apiKeyStdin, envVar)
			if err != nil {
				return err
			}

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			creds := map[string]string{"api_key": key}
			if name := strings.TrimSpace(user); name != "" {
				creds["username"] = name
			}

			if noVerify {
				if err := client.SetSourceCredentials(cmd.Context(), sourceID, creds); err != nil {
					return err
				}

				_, err = fmt.Fprintf(
					s.env.Out,
					"stored credentials for %s (not verified)\n",
					sourceID,
				)

				return err
			}
			// AuthenticateSource stores what the source accepted, so a key it
			// rejects never reaches the settings file.
			if err := client.AuthenticateSource(cmd.Context(), sourceID, creds); err != nil {
				return err
			}

			_, err = fmt.Fprintf(s.env.Out, "authenticated %s\n", sourceID)

			return err
		},
	}
	flags := command.Flags()
	// The username is passed through to the source and stored, because a source
	// plugin may ask for one and the credentials map is free-form. None of the
	// bundled sources use it — GitHub and Vortex take a single token, and Nexus
	// v3 has no account name anywhere in its protocol — so the help says so
	// rather than implying that typing one does something today.
	flags.StringVar(&user, "user", "",
		"account name, for a source that asks for one (none of the bundled sources do)")
	flags.StringVar(
		&apiKey,
		"api-key",
		"",
		"the key (prefer --api-key-stdin or an environment variable)",
	)
	flags.BoolVar(&apiKeyStdin, "api-key-stdin", false,
		"read the key from standard input, asking for it on the terminal if that is a terminal")
	flags.StringVar(&envVar, "api-key-env", "", "read the key from this environment variable")
	flags.BoolVar(&noVerify, "no-verify", false,
		"store the key without asking the source whether it works")
	flags.BoolVar(&forget, "forget", false, "forget the stored key instead of setting one")

	return command
}

// resolveAPIKey works out which credential to authenticate with.
//
// The sources are tried in the order the help lists them, and the terminal comes
// last rather than first. An explicit choice — a flag, a named environment
// variable — is never second-guessed by a prompt, because a prompt that appears
// when somebody asked for something specific is a prompt they have to dismiss
// instead of answer. Prompting only fills a gap, so `dmm source auth nexus` at a
// terminal asks while the same command in a script still fails with the error
// that names the environment variable to set.
//
// An environment variable the caller named is treated as a mistake to report
// when it is empty, rather than as a gap to fill from somewhere else: somebody
// who typed --api-key-env has said where the key is, and quietly reading a
// different one would make the flag's name a lie.
func (s *session) resolveAPIKey(
	ctx context.Context,
	sourceID, apiKey string,
	fromStdin bool,
	envVar string,
) (string, error) {
	key := strings.TrimSpace(apiKey)
	if fromStdin {
		if key != "" {
			return "", fatalf("--api-key and --api-key-stdin cannot be combined")
		}

		read, err := s.readKey(ctx, sourceID)
		if err != nil {
			return "", err
		}

		key = read
	}

	if name := strings.TrimSpace(envVar); key == "" && name != "" {
		key = strings.TrimSpace(os.Getenv(name))
		if key == "" {
			return "", fatalf("environment variable %s is empty or unset", name)
		}
	}

	if key == "" {
		key = strings.TrimSpace(os.Getenv(defaultKeyEnvVar(sourceID)))
	}

	if key == "" {
		if ask := s.prompter(); ask != nil {
			typed, err := promptForKey(ctx, s.env.Err, ask, sourceID)
			if err != nil {
				return "", err
			}

			key = typed
		}
	}

	if key == "" {
		return "", fatalf("no API key given; use --api-key-stdin or set %s",
			defaultKeyEnvVar(sourceID))
	}

	return key, nil
}

// keyFlagsGiven reports whether any flag that supplies a key was used. --forget
// has to refuse a key rather than quietly ignore it: a user who typed a key and
// asked to forget would otherwise end up with whichever they did not mean.
func keyFlagsGiven(
	apiKey string,
	apiKeyStdin bool,
	envVar string,
) (key string, fromStdin bool, named bool) {
	return strings.TrimSpace(apiKey), apiKeyStdin, strings.TrimSpace(envVar) != ""
}

// defaultKeyEnvVar is the environment variable a source's key is read from when
// the user does not say. It is derived from the source id, so the name is
// predictable from `dmm source auth nexus` without consulting documentation.
func defaultKeyEnvVar(sourceID string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(sourceID))

	return "DMM_" + name + "_API_KEY"
}

func newSourceTestCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "test [source]",
		Short: "Check a source's credentials",
		Long: "Check that a source's stored credentials still work.\n\n" +
			"Every source is checked when none is named, so one broken source\n" +
			"does not hide the state of the rest.\n\n" +
			"A source with no credential stored is reported as `not configured`\n" +
			"rather than as working. Both Nexus and Vortex can be browsed without\n" +
			"one, so a check that never contacted anything must not claim a\n" +
			"success it did not observe.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			ids := args
			if len(ids) == 0 {
				ids = sourceIDs(client.ListSources(ctx))
			}

			if len(ids) == 0 {
				return s.writeJSONOrTable(s.env.Out, []any{}, nil, "No sources are registered.")
			}

			type result struct {
				Source string `json:"source"`
				State  string `json:"state"`
				Error  string `json:"error,omitempty"`
			}

			results := make([]result, 0, len(ids))
			failures := 0

			for _, id := range ids {
				entry := result{Source: id, State: sourceTestState(ctx, client, id)}
				if entry.State == sourceTestFailed {
					failures++
					entry.Error = oneLine(client.TestSourceConnection(ctx, id))
				}

				results = append(results, entry)
			}

			if s.opts.asJSON {
				if err := writeJSON(s.env.Out, results); err != nil {
					return err
				}
			} else {
				table := NewTable("SOURCE", "STATE", "DETAIL")

				for _, entry := range results {
					var detail string

					switch entry.State {
					case sourceTestFailed:
						detail = describeError(entry.Error)
					case sourceTestNotConfigured:
						detail = "no credential stored; browsing still works"
					}

					table.Add(entry.Source, entry.State, detail)
				}

				if err := table.Render(s.env.Out); err != nil {
					return err
				}
			}

			if failures > 0 {
				return appErrors.New(appErrors.ErrCodeAuthFailed,
					"%d of %d sources could not be reached with the stored credentials",
					failures, len(ids))
			}

			return nil
		},
	}
}

// The three states `source test` can report. They are strings because they are
// printed and serialised, and a single table of them keeps the JSON view and the
// table view from being able to disagree.
const (
	sourceTestOK            = "ok"
	sourceTestFailed        = "failed"
	sourceTestNotConfigured = "not configured"
)

// sourceTestState decides what can honestly be said about one source.
//
// A source that takes no credentials has nothing to check and is reported as
// working. A source that takes them but has none stored is reported as not
// configured, because the underlying check is a no-op in that state and calling
// it a success would be reporting the absence of a check as its result. Only a
// stored credential is actually put to the test.
func sourceTestState(ctx context.Context, client *api.Client, id string) string {
	info, err := client.SourceInfo(ctx, id)
	if err != nil {
		// An unknown source is a failure, and the reason is collected by the
		// caller, which runs the real check to learn what to say about it.
		return sourceTestFailed
	}

	if !info.SupportsAuth {
		return sourceTestOK
	}

	cfg, found, err := client.SourceConfig(ctx, id)
	if err != nil {
		return sourceTestFailed
	}

	if !found || strings.TrimSpace(cfg.APIKey) == "" {
		return sourceTestNotConfigured
	}

	if err := client.TestSourceConnection(ctx, id); err != nil {
		return sourceTestFailed
	}

	return sourceTestOK
}

func newSourceEnableCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "enable <source>",
		Short: "Turn a source on",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return s.setSourceEnabled(cmd, args[0], true)
		},
	}
}

func newSourceDisableCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <source>",
		Short: "Turn a source off",
		Long: "Turn a source off.\n\n" +
			"A disabled source is neither built nor searched. Its credentials stay\n" +
			"in the settings file, so switching it back on does not need them\n" +
			"again.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return s.setSourceEnabled(cmd, args[0], false)
		},
	}
}

func (s *session) setSourceEnabled(cmd *cobra.Command, sourceID string, enabled bool) error {
	client, err := s.client(cmd.Context())
	if err != nil {
		return err
	}
	// The source has to exist before anything is written. SetConfigValue creates
	// a settings block on demand, so without this check `source disable typo`
	// would print "typo disabled" and leave a block for a source that is not
	// there in the user's file — a claim the program cannot keep up.
	if _, err := client.SourceInfo(cmd.Context(), sourceID); err != nil {
		return err
	}
	// The key is written through the dotted settings key rather than a struct so
	// the file on disk keeps whatever the user hand-edited into it: rewriting
	// the whole configuration from a decoded copy would drop a comment-shaped
	// key this version does not know about yet.
	if err := client.SetConfigValue(cmd.Context(),
		fmt.Sprintf("sources.%s.enabled", sourceID), yesNo(enabled)); err != nil {
		return err
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}

	_, err = fmt.Fprintf(s.env.Out, "%s %s\n", sourceID, state)

	return err
}

// sourceIDs extracts the ids of a source list, sorted, for a command that was
// not told which sources to act on.
func sourceIDs(infos []api.ModSourceInfo) []string {
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}

	sort.Strings(ids)

	return ids
}
