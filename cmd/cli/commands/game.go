package commands

import (
	"fmt"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/spf13/cobra"
)

// newGameCommand assembles `dmm game`: where Darktide is, and what the manager
// makes of that.
func newGameCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "game",
		Short: "Find and check the Darktide installation",
		// There is deliberately no `game set`: setting the path is a decision
		// about configuration rather than about the game, so it lives in
		// `dmm config set game.path <path>` and in the global `--game` flag.
		//
		// The help says so, because the first thing anyone types after
		// `game detect` finds nothing is `game set`, and being told "unknown
		// command" by a program that has an obvious alternative is a poor
		// welcome.
		Long: "Find and check the Darktide installation.\n\n" +
			"There is no `game set`: the installation path is configuration, so\n" +
			"set it with `dmm config set game.path <path>`, or for this one run\n" +
			"with `dmm --game <path> …`. Both remember the path; the flag also\n" +
			"validates it before anything is written.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newGameDetectCommand(s),
		newGamePathCommand(s),
		newGameValidateCommand(s),
		newGameInfoCommand(s),
	)

	return group
}

// installView is the JSON shape of one installation.
type installView struct {
	Path       string `json:"path"`
	ModsPath   string `json:"mods_path,omitempty"`
	Source     string `json:"source,omitempty"`
	Version    string `json:"version,omitempty"`
	AppID      string `json:"app_id,omitempty"`
	DetectedAt string `json:"detected_at,omitempty"`
	Notes      string `json:"notes,omitempty"`
	Valid      bool   `json:"valid"`
}

func newInstallView(install api.GameInstallation) installView {
	return installView{
		Path:       install.Path,
		ModsPath:   install.ModsPath,
		Source:     string(install.Source),
		Version:    install.Version,
		AppID:      install.AppID,
		Valid:      install.Valid,
		DetectedAt: formatStamp(install.DetectedAt),
		Notes:      install.Notes,
	}
}

func newGameDetectCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "detect",
		Short: "Find Darktide installations",
		Long: "Find Darktide installations on this machine.\n\n" +
			"The configured game path is checked as well as the usual launchers,\n" +
			"and the results are de-duplicated, so one installation found twice\n" +
			"appears once.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			installs, err := client.DetectGame(cmd.Context())
			if err != nil {
				return err
			}

			table := NewTable("PATH", "MODS", "SOURCE", "VERSION")

			views := make([]installView, 0, len(installs))
			for _, install := range installs {
				table.Add(install.Path, orDash(install.ModsPath),
					orDash(string(install.Source)), orDash(install.Version))
				views = append(views, newInstallView(install))
			}

			return s.writeJSONOrTable(s.env.Out, views, table,
				"No Darktide installation was found. Set one with: dmm --game <path> game info")
		},
	}
}

func newGamePathCommand(s *session) *cobra.Command {
	var showMods bool

	command := &cobra.Command{
		Use:   "path",
		Short: "Print the configured installation's mods folder",
		Long: "Print the mods folder of the installation the manager uses.\n\n" +
			"That is the folder the manager deploys into, which is not always the\n" +
			"one next to the executable: a game with an unusual layout keeps its\n" +
			"mods elsewhere, and this is the answer to \"where did they go\".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			install, err := client.CurrentGame(ctx)
			if err != nil {
				return err
			}

			modsPath, err := client.GetModsPath(ctx, install)
			if err != nil {
				return err
			}

			if showMods || s.opts.asJSON {
				view := newInstallView(*install)

				view.ModsPath = modsPath
				if s.opts.asJSON {
					return writeJSON(s.env.Out, view)
				}
			}

			_, err = fmt.Fprintln(s.env.Out, modsPath)

			return err
		},
	}
	command.Flags().BoolVar(
		&showMods,
		"with-install",
		false,
		"print the installation as a JSON object",
	)

	return command
}

func newGameValidateCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Check that a folder looks like a Darktide installation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			install, err := client.ValidateGamePath(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, newInstallView(*install))
			}

			fields := &Fields{}
			fields.Set("path", install.Path)
			fields.Set("valid", yesNo(install.Valid))
			fields.SetOrDash("mods folder", install.ModsPath)
			fields.SetOrDash("source", string(install.Source))
			fields.SetOrDash("version", install.Version)
			fields.SetOrDash("notes", install.Notes)

			return fields.Render(s.env.Out)
		},
	}
}

func newGameInfoCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show the installation the manager is using",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			install, err := client.CurrentGame(ctx)
			if err != nil {
				return err
			}

			modsPath := ""
			if path, err := client.GetModsPath(ctx, install); err == nil {
				modsPath = path
			} else if !s.opts.quiet {
				// The mods folder is the one thing that can be unresolvable on
				// its own — an override pointing at a directory that no longer
				// exists, say — and a mod cannot be enabled without it. Saying so
				// here is more use than failing the whole command, because the
				// installation itself was found and is worth reporting.
				fmt.Fprintf(s.env.Err, "warning: %s\n", oneLine(err))
			}

			if s.opts.asJSON {
				view := newInstallView(*install)
				view.ModsPath = modsPath

				return writeJSON(s.env.Out, view)
			}

			fields := &Fields{}
			fields.Set("path", install.Path)
			fields.Set("valid", yesNo(install.Valid))
			fields.SetOrDash("mods folder", modsPath)
			fields.SetOrDash("source", string(install.Source))
			fields.SetOrDash("version", install.Version)
			fields.SetOrDash("app id", install.AppID)
			fields.SetOrDash("notes", install.Notes)
			fields.Set("detected", orDash(formatStamp(install.DetectedAt)))

			return fields.Render(s.env.Out)
		},
	}
}
