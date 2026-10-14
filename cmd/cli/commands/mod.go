package commands

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/spf13/cobra"
)

// newModCommand assembles the `dmm mod` group: everything that acts on a single
// mod or on the installed set.
//
// The group is split across files by subject rather than by size — the mod
// family owns install state, the hook family owns scripts — because a reader
// looking for "how does install work" should not have to read the link-format
// code to find it.
func newModCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "mod",
		Short: "Install, update and enable mods",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newModListCommand(s),
		newModShowCommand(s),
		newModInstallCommand(s),
		newModUninstallCommand(s),
		newModEnableCommand(s),
		newModDisableCommand(s),
		newModUpdateCommand(s),
		newModReorderCommand(s),
		newModHooksCommand(s),
		newSourceLinkCommand(s),
		newModPathCommand(s),
		newModRelinkCommand(s),
		newModSymlinkStatusCommand(s),
		newModConflictsCommand(s),
		newModShareCommand(s),
		newModImportLinkCommand(s),
	)

	return group
}

// modView is the JSON shape of one mod.
//
// It is not domain.Mod. A domain struct is the manager's internal
// representation, with hooks, per-source metadata and install bookkeeping that
// a script has no use for; the JSON a command prints is a promise, and the
// promise only needs the fields below.
type modView struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Version       string   `json:"version,omitempty"`
	Author        string   `json:"author,omitempty"`
	Source        string   `json:"source,omitempty"`
	Path          string   `json:"path,omitempty"`
	InstallMode   string   `json:"install_mode,omitempty"`
	ModFolderName string   `json:"mod_folder_name,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Priority      int      `json:"priority,omitempty"`
	Hooks         int      `json:"hooks,omitempty"`
	Enabled       bool     `json:"enabled"`
}

func newModView(mod api.Mod) modView {
	return modView{
		ID:            mod.ID,
		Name:          mod.Name,
		Version:       mod.Version,
		Author:        mod.Author,
		Source:        mod.SourceID,
		Enabled:       mod.Enabled,
		Priority:      mod.Priority,
		Path:          mod.LocalPath,
		Tags:          mod.Tags,
		Hooks:         mod.Hooks.Count(),
		InstallMode:   string(mod.InstallMode),
		ModFolderName: mod.ModFolderName,
	}
}

func newModListCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed mods",
		Long: "List the installed mods, enabled ones first and then in load order.\n" +
			"That order is the one the game will read them in, which is why it is\n" +
			"the default rather than alphabetical.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			mods, err := client.ListMods(cmd.Context())
			if err != nil {
				return err
			}

			table := NewTable("ID", "VERSION", "SOURCE", "STATE", "NAME")

			views := make([]modView, 0, len(mods))
			for _, mod := range mods {
				table.Add(mod.ID, orDash(mod.Version), orDash(mod.SourceID),
					modState(mod), Clip(mod.Name))
				views = append(views, newModView(mod))
			}

			return s.writeJSONOrTable(s.env.Out, views, table, "No mods are installed.")
		},
	}
}

func newModShowCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "show <mod>",
		Short: "Show everything known about one mod",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			mod, err := client.GetMod(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, newModView(*mod))
			}

			fields := &Fields{}
			fields.Set("id", mod.ID)
			fields.SetOrDash("name", mod.Name)
			fields.SetOrDash("version", mod.Version)
			fields.SetOrDash("author", mod.Author)
			fields.SetOrDash("source", mod.SourceID)
			fields.Set("state", modState(*mod))
			fields.Setf("priority", "%d", mod.Priority)
			fields.SetOrDash("path", mod.LocalPath)
			fields.Set("installed", orDash(formatStamp(mod.InstalledAt)))
			fields.Set("updated", orDash(formatStamp(mod.UpdatedAt)))
			fields.SetOrDash("description", mod.Description)
			fields.Set("tags", joinNames(mod.Tags))
			fields.Set("dependencies", joinNames(mod.Dependencies))
			fields.Set("conflicts", joinNames(mod.ConflictsWith))
			fields.Setf("hooks", "%d", mod.Hooks.Count())

			if notes := sourceDataText(mod.SourceData, "notes"); notes != "" {
				fields.Set("notes", notes)
			}

			return fields.Render(s.env.Out)
		},
	}
}

func newModInstallCommand(s *session) *cobra.Command {
	var (
		version     string
		noEnable    bool
		force       bool
		purgeCache  bool
		installMode string
	)

	command := &cobra.Command{
		Use:   "install <source> <mod>",
		Short: "Install a mod from a source",
		Long: "Install a mod by source id and mod id.\n\n" +
			"The source decides which version exists, so --version is a check\n" +
			"rather than a request: asking for a version the source does not\n" +
			"offer is refused instead of installing the newest one, because\n" +
			"quietly installing something other than what was asked for is worse\n" +
			"than an error.\n\n" +
			"Install mode controls where the mod's payload is deployed:\n" +
			"  mods_folder (default) - deploy into the game's mods folder\n" +
			"  game_root - merge into the game root folder (for mods that patch the game)",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			sourceID, modID := args[0], args[1]
			if want := strings.TrimSpace(version); want != "" {
				offered, err := client.GetSourceMod(cmd.Context(), sourceID, modID)
				if err != nil {
					return err
				}

				if !domain.PathsEqual(offered.Version, want) {
					return fatalf("source %q offers %s %s, not %s",
						sourceID, modLabelOf(modID), orDash(offered.Version), want)
				}
			}

			options := api.InstallOptions{Force: force, PurgeCache: purgeCache}

			if noEnable {
				disabled := false
				options.Enable = &disabled
			}

			if strings.TrimSpace(installMode) != "" {
				options.SourceData = map[string]any{
					"install_mode": installMode,
				}
			}

			mod, err := client.Install(cmd.Context(), sourceID, modID, options)
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, newModView(*mod))
			}

			fmt.Fprintf(s.env.Out, "installed %s %s (%s)\n",
				mod.ID, orDash(mod.Version), modState(*mod))

			return nil
		},
	}
	flags := command.Flags()
	flags.StringVar(&version, "version", "", "fail unless the source offers this version")
	flags.BoolVar(&noEnable, "no-enable", false, "install without enabling")
	flags.BoolVar(&force, "force", false, "reinstall over an existing install")
	flags.BoolVar(&purgeCache, "purge-cache", false, "delete the archive once it is extracted")
	flags.StringVar(
		&installMode,
		"install-mode",
		"",
		"install mode: mods_folder (default) or game_root",
	)

	return command
}

func newModUninstallCommand(s *session) *cobra.Command {
	var purge bool

	command := &cobra.Command{
		Use:     "uninstall <mod>",
		Short:   "Remove a mod from the manager and the game folder",
		Aliases: []string{"remove"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.Uninstall(
				cmd.Context(),
				args[0],
				api.UninstallOptions{PurgeCache: purge},
			); err != nil {
				return err
			}

			_, err = fmt.Fprintf(s.env.Out, "uninstalled %s\n", args[0])

			return err
		},
	}
	command.Flags().BoolVar(&purge, "purge", false, "also delete the cached archive")

	return command
}

func newModEnableCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "enable <mod>",
		Short: "Deploy a mod into the game folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.EnableMod(cmd.Context(), args[0]); err != nil {
				return err
			}

			_, err = fmt.Fprintf(s.env.Out, "enabled %s\n", args[0])

			return err
		},
	}
}

func newModDisableCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <mod>",
		Short: "Undeploy a mod but keep it installed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.DisableMod(cmd.Context(), args[0]); err != nil {
				return err
			}

			_, err = fmt.Fprintf(s.env.Out, "disabled %s\n", args[0])

			return err
		},
	}
}

// updateView is the JSON shape of one row of `dmm mod update --check`.
type updateView struct {
	ModID         string `json:"mod_id"`
	Source        string `json:"source,omitempty"`
	LocalVersion  string `json:"local_version,omitempty"`
	RemoteVersion string `json:"remote_version,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Available     bool   `json:"available"`
}

func newModUpdateCommand(s *session) *cobra.Command {
	var checkOnly bool

	command := &cobra.Command{
		Use:   "update [mod]",
		Short: "Apply available updates",
		Long: "Apply available updates to every installed mod, or to one named mod.\n" +
			"Use --check to report what is available without changing anything.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			if len(args) == 1 {
				// --check has to be honoured here too, not only on the path
				// with no mod named. A person who asked what is available and
				// got an install would be right to be annoyed, and a script
				// polling for updates would be installing them.
				if checkOnly {
					updates, err := client.CheckModUpdates(ctx)
					if err != nil {
						return err
					}

					return s.reportUpdates(updates, args[0], true)
				}

				mod, err := client.ApplyUpdate(ctx, args[0])
				if err != nil {
					return err
				}

				if s.opts.asJSON {
					return writeJSON(s.env.Out, newModView(*mod))
				}

				fmt.Fprintf(s.env.Out, "updated %s to %s\n", mod.ID, orDash(mod.Version))

				return nil
			}

			updates, err := client.CheckModUpdates(ctx)
			if err != nil {
				return err
			}

			if !checkOnly {
				if err := s.applyPendingUpdates(ctx, updates); err != nil {
					return err
				}
			}

			return s.reportUpdates(updates, "", checkOnly)
		},
	}
	command.Flags().BoolVar(&checkOnly, "check", false, "report what is available without updating")

	return command
}

// reportUpdates prints what a check found, optionally narrowed to one mod.
//
// The narrow case exists because `dmm mod update --check <id>` and
// `dmm mod update --check` are the same question with different scopes, and
// they have to give the same answer for the same mod.
//
// checking says whether this is a report or the tail of an apply, which is the
// only thing the empty result means differently: a report that found nothing
// says so, and an apply that changed nothing says so.
//
// An id that matched nothing is reported rather than silently producing an
// empty table: a mod whose source cannot be reached is exactly the mod a person
// is checking for, and "no updates" would be a lie about it.
func (s *session) reportUpdates(updates []api.ModUpdateInfo, onlyID string, checking bool) error {
	table := NewTable("MOD", "INSTALLED", "REMOTE", "STATE", "SOURCE")
	views := make([]updateView, 0, len(updates))
	matched := false

	for _, update := range updates {
		if onlyID != "" && update.ModID != onlyID {
			continue
		}

		matched = true

		table.Add(update.ModID, orDash(update.LocalVersion), orDash(update.RemoteVersion),
			updateState(update), orDash(update.SourceID))
		views = append(views, updateView{
			ModID:         update.ModID,
			Source:        update.SourceID,
			LocalVersion:  update.LocalVersion,
			RemoteVersion: update.RemoteVersion,
			Available:     update.Available,
		})
	}

	if onlyID != "" && !matched {
		// MOD_NOT_FOUND, not INVALID_ARGUMENT, because the other path through
		// this same command — `dmm mod update <id>` — reports a mod that is not
		// installed with that code, and a script branching on the code should
		// not get two different answers from one command.
		return appErrors.New(appErrors.ErrCodeModNotFound,
			"mod %q is not installed", modLabelOf(onlyID))
	}

	nothing := "No mod was updated."
	if checking {
		nothing = "Every installed mod is up to date."
	}

	return s.writeJSONOrTable(s.env.Out, views, table, nothing)
}

// updateState words the answer to "is there an update" rather than leaving the
// reader to infer it from two version columns.
//
// A dash in the REMOTE column means the source could not be asked, and an
// unlabelled asterisk would leave both of those cases looking like the same
// thing. This is the column a person reads.
func updateState(update api.ModUpdateInfo) string {
	switch {
	case strings.TrimSpace(update.RemoteVersion) == "":
		return "unknown"
	case update.Available:
		return "update available"
	default:
		return "up to date"
	}
}

// applyPendingUpdates updates every mod with an available update.
//
// One failure does not abandon the rest: a user running `dmm mod update` with
// twelve mods installed wants the eleven that worked, plus a list of what did
// not. The failures are reported at the end, so the exit code still says the
// command did not fully succeed.
func (s *session) applyPendingUpdates(ctx context.Context, updates []api.ModUpdateInfo) error {
	var failures []error

	updated := 0

	for _, update := range updates {
		if !update.Available {
			continue
		}

		if _, err := s.updateOne(ctx, update.ModID); err != nil {
			failures = append(failures, err)
			continue
		}

		updated++
	}

	if len(failures) == 0 {
		return nil
	}
	// errors.Join keeps every failure's message in the chain, and the code of
	// the first one so a caller can still branch on it.
	joined := errors.Join(failures...)

	return appErrors.Wrap(joined, appErrors.CodeOf(joined),
		"updated %d of %d mods; %s failed",
		updated, updated+len(failures), plural(len(failures), "mod", "mods"))
}

func (s *session) updateOne(ctx context.Context, modID string) (*api.Mod, error) {
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}

	return client.ApplyUpdate(ctx, modID)
}

func newModReorderCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "reorder <mod> [mod...]",
		Short: "Set the load order",
		Long: "Set the load order to exactly the mods listed, replacing any\n" +
			"previous order. An installed mod that is not listed loses its place\n" +
			"and falls back to its priority, so a partial list is a real change\n" +
			"and the command reports how many mods it left out.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			installed, err := client.ListMods(ctx)
			if err != nil {
				return err
			}

			if err := client.ReorderMods(ctx, args); err != nil {
				return err
			}

			listed := make(map[string]bool, len(args))
			for _, id := range args {
				listed[id] = true
			}

			leftOut := 0

			for _, mod := range installed {
				if !listed[mod.ID] {
					leftOut++
				}
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]any{
					"order": args, "left_out": leftOut,
				})
			}

			fmt.Fprintf(s.env.Out, "load order set to %s\n", plural(len(args), "mod", "mods"))

			if leftOut > 0 {
				// A mod left out of the order silently loses its place, so
				// saying how many is the difference between the user knowing
				// and not knowing why the game loaded them differently.
				fmt.Fprintf(s.env.Out,
					"%s not in the order, falling back to their priority\n",
					plural(leftOut, "installed mod is", "installed mods are"))
			}

			return nil
		},
	}
}

func newModPathCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "path <mod>",
		Short: "Print the payload folder of an installed mod",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			path, err := client.ModPath(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			_, err = fmt.Fprintln(s.env.Out, path)

			return err
		},
	}
}

func newModRelinkCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "relink <mod>",
		Short: "Recreate the game folder links of a mod",
		Long: "Recreate the links of one mod, or of every enabled mod when no mod is\n" +
			"named. This is what to run after the storage folder moved: the links\n" +
			"still point at the old location.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			ids := args
			if len(ids) == 0 {
				statuses, err := client.SymlinkStatus(ctx)
				if err != nil {
					return err
				}

				for _, status := range statuses {
					if status.Damaged > 0 {
						ids = append(ids, status.ModID)
					}
				}

				if len(ids) == 0 {
					fmt.Fprintln(s.env.Out, "Every link is intact.")
					return nil
				}
			}

			repaired := 0

			for _, id := range ids {
				entries, err := client.RelinkMod(ctx, id)
				if err != nil {
					return err
				}

				repaired += len(entries)

				if !s.opts.asJSON {
					// Zero repaired is the common case for a healthy mod, and
					// "relinked x (0 entries)" reads like something went wrong.
					detail := "nothing to repair"
					if len(entries) > 0 {
						detail = "repaired " + plural(len(entries), "entry", "entries")
					}

					fmt.Fprintf(s.env.Out, "relinked %s (%s)\n", id, detail)
				}
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]any{"relinked": ids, "entries": repaired})
			}

			return nil
		},
	}
}

func newModSymlinkStatusCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "symlink-status",
		Short: "Report the condition of every deployed link",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			statuses, err := client.SymlinkStatus(cmd.Context())
			if err != nil {
				return err
			}

			table := NewTable("MOD", "ENTRY", "STATE")

			type entryView struct {
				ModID string `json:"mod_id"`
				Entry string `json:"entry"`
				State string `json:"state"`
			}

			views := make([]entryView, 0)

			damaged := 0
			for _, status := range statuses {
				damaged += status.Damaged
				for _, entry := range sortedKeys(status.Entries) {
					state := string(status.Entries[entry])
					table.Add(status.ModID, entry, state)
					views = append(
						views,
						entryView{ModID: status.ModID, Entry: entry, State: state},
					)
				}
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, views)
			}

			if table.Empty() {
				fmt.Fprintln(s.env.Out, "No mod has anything deployed.")
				return nil
			}

			if err := table.Render(s.env.Out); err != nil {
				return err
			}

			if damaged == 0 {
				fmt.Fprintln(s.env.Out, "Every deployed link is intact.")
				return nil
			}

			fmt.Fprintf(s.env.Out, "%s need relinking; run: dmm mod relink\n",
				plural(damaged, "link", "links"))

			return nil
		},
	}
}

// conflictView is the JSON shape of one reported conflict.
type conflictView struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Severity       string `json:"severity"`
	ModA           string `json:"mod_a,omitempty"`
	ModB           string `json:"mod_b,omitempty"`
	Details        string `json:"details,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
}

func newConflictView(report api.ConflictReport) conflictView {
	first, second := "", ""
	if report.ModA != nil {
		first = report.ModA.ID
	}

	if report.ModB != nil {
		second = report.ModB.ID
	}

	return conflictView{
		ID:             report.ID(),
		Type:           string(report.Type),
		Severity:       string(report.Severity),
		ModA:           first,
		ModB:           second,
		Details:        report.Details,
		Recommendation: report.Recommendation,
	}
}

func newModConflictsCommand(s *session) *cobra.Command {
	var (
		resolveID string
		how       string
	)

	command := &cobra.Command{
		Use:   "conflicts",
		Short: "Report conflicts between installed mods",
		Long: "Report conflicts between installed mods.\n\n" +
			"A report has an id, which is what --resolve takes, so a script can\n" +
			"read the table once and act on one line of it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if want := strings.TrimSpace(resolveID); want != "" {
				resolution, ok := api.ParseConflictResolution(how)
				if !ok {
					return fatalf("unknown resolution %q; use one of %s",
						strings.TrimSpace(how), resolutionNames())
				}

				if err := client.ResolveConflict(cmd.Context(), want, resolution); err != nil {
					return err
				}

				_, err = fmt.Fprintf(s.env.Out, "resolved %s as %s\n", want, resolution)

				return err
			}

			reports, err := client.DetectConflicts(cmd.Context())
			if err != nil {
				return err
			}

			table := NewTable("SEVERITY", "TYPE", "MODS", "DETAILS", "ID")

			views := make([]conflictView, 0, len(reports))
			for _, report := range reports {
				first, second := "", ""
				if report.ModA != nil {
					first = report.ModA.ID
				}

				if report.ModB != nil {
					second = report.ModB.ID
				}

				both := first
				if second != "" {
					both += " + " + second
				}

				table.Add(string(report.Severity), string(report.Type), orDash(both),
					Clip(report.Details), report.ID())
				views = append(views, newConflictView(report))
			}

			return s.writeJSONOrTable(s.env.Out, views, table, "No conflicts were found.")
		},
	}
	flags := command.Flags()
	flags.StringVar(&resolveID, "resolve", "", "resolve the report with this id")
	flags.StringVar(&how, "how", "", "how to resolve it: "+resolutionNames())

	return command
}

// resolutionNames lists the spellings ParseConflictResolution accepts, for use
// in a usage message. It is built from the values rather than written out, so a
// resolution added later appears in the help without a second edit.
func resolutionNames() string {
	names := make([]string, 0, 5)
	for _, resolution := range []api.ConflictResolution{
		api.ResolutionKeepBoth,
		api.ResolutionDisableModA,
		api.ResolutionDisableModB,
		api.ResolutionUninstallModA,
		api.ResolutionUninstallModB,
	} {
		names = append(names, resolution.String())
	}

	sort.Strings(names)

	return strings.Join(names, ", ")
}

// modState renders a mod's enabled flag as the word a person uses.
func modState(mod api.Mod) string {
	if mod.Enabled {
		return "enabled"
	}

	return "disabled"
}

// formatStamp renders a timestamp the way the rest of the manager writes them:
// RFC 3339 in UTC, or a dash for the zero time. The zero time is not "the
// beginning of time" in this program; it means the value was never set.
func formatStamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}

	return value.UTC().Format(time.RFC3339)
}

// sourceDataText reads a string out of a mod's source metadata.
//
// The type assertion is checked rather than assumed: SourceData comes from
// JSON written by somebody else's source plugin, so a mod carrying a number
// where a string was expected is entirely possible, and a bare assertion would
// take the whole command down over a field nobody asked for.
func sourceDataText(data map[string]any, key string) string {
	if data == nil {
		return ""
	}

	text, _ := data[key].(string)

	return strings.TrimSpace(text)
}

// sourceDataInt reads a number out of a mod's source metadata. JSON decodes
// every number into a float64, but a plugin that put an int there would give an
// int, so both are accepted.
//
// A fractional value is refused rather than truncated. Both mod stores number
// their files with whole numbers, so a fraction is corruption — and truncating
// corruption produces an id that names a different, real mod, which is worse
// than admitting the value is wrong.
func sourceDataInt(data map[string]any, key string) (int64, bool) {
	if data == nil {
		return 0, false
	}

	switch value := data[key].(type) {
	case int64:
		return value, true
	case int:
		return int64(value), true
	case float64:
		if value != math.Trunc(value) {
			return 0, false
		}

		return int64(value), true
	default:
		return 0, false
	}
}
