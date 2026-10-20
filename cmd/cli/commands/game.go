package commands

import (
	"fmt"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/spf13/cobra"
)

func newGameCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "game",
		Short: "List, switch, and inspect games",
		// There is deliberately no `game set`: the path is configuration, set with
		// `dmm config set game.path`.
		Long: "List, switch, and inspect games.\n\n" +
			"`game list` shows the registered games and which one is active;\n" +
			"`game use <id>` switches the active game and remembers it; and\n" +
			"`game detect`/`info`/`path`/`validate` work on the active game's\n" +
			"installation. There is no `game set`: the installation path is\n" +
			"configuration, so set it with `dmm config set game.path <path>`.\n\n" +
			"How mods reach the game folder is configuration too: each game\n" +
			"uses the install method its definition prefers unless you override\n" +
			"it with `dmm config set games.<id>.install_method direct`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newGameListCommand(s),
		newGameUseCommand(s),
		newGameCurrentCommand(s),
		newGameDetectCommand(s),
		newGamePathCommand(s),
		newGameValidateCommand(s),
		newGameInfoCommand(s),
	)

	return group
}

type gameView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default"`
	Active      bool   `json:"active"`
}

func newGameView(info api.GameInfo) gameView {
	return gameView{
		ID:          info.ID,
		Name:        info.DisplayName,
		Description: info.Description,
		Default:     info.IsDefault,
		Active:      info.IsActive,
	}
}

// activeGameView finds the active entry in a SupportedGames result. That always
// marks exactly one game active, so a miss means the API and this view disagreed.
func activeGameView(infos []api.GameInfo) (gameView, bool) {
	for _, info := range infos {
		if info.IsActive {
			return newGameView(info), true
		}
	}

	return gameView{}, false
}

type installView struct {
	GameID     string `json:"game_id,omitempty"`
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
		GameID:     install.GameID,
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

func newGameListCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the games the manager supports",
		Long: "List the registered games.\n\n" +
			"DEFAULT is the game used when no active game is configured, and\n" +
			"ACTIVE is the one the manager is working on right now. Switch with\n" +
			"`dmm game use <id>`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			infos, err := client.SupportedGames()
			if err != nil {
				return err
			}

			table := NewTable("ID", "NAME", "DEFAULT", "ACTIVE")

			views := make([]gameView, 0, len(infos))
			for _, info := range infos {
				table.Add(info.ID, orDash(info.DisplayName),
					yesNo(info.IsDefault), yesNo(info.IsActive))
				views = append(views, newGameView(info))
			}

			return s.writeJSONOrTable(s.env.Out, views, table,
				"No games are registered.")
		},
	}
}

func newGameUseCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:     "use <id>",
		Aliases: []string{"switch"},
		Short:   "Switch the active game",
		Long: "Switch the active game and remember the choice.\n\n" +
			"The id is one from `dmm game list`. An unknown id is refused: a\n" +
			"typo silently running the wrong game is worse than a failed\n" +
			"switch. The installation path is per-game configuration, so set\n" +
			"it afterwards with `dmm config set game.path <path>` if the new\n" +
			"game has not been detected.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			if err := client.UseGame(ctx, args[0]); err != nil {
				return err
			}

			if s.opts.asJSON {
				infos, err := client.SupportedGames()
				if err != nil {
					return err
				}

				view, ok := activeGameView(infos)
				if !ok {
					return fatalf("the active game vanished after the switch")
				}

				return writeJSON(s.env.Out, view)
			}

			_, err = fmt.Fprintf(s.env.Out, "switched to %s\n", strings.TrimSpace(args[0]))

			return err
		},
	}
}

func newGameCurrentCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the active game",
		Long: "Print the id of the active game.\n\n" +
			"This is the registry answer — which game the manager is for — not\n" +
			"the installation check; for the installation, use `game info`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			infos, err := client.SupportedGames()
			if err != nil {
				return err
			}

			view, ok := activeGameView(infos)
			if !ok {
				return fatalf("no active game is registered")
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, view)
			}

			fields := &Fields{}
			fields.Set("game", view.ID)
			fields.SetOrDash("name", view.Name)
			fields.Set("default", yesNo(view.Default))

			return fields.Render(s.env.Out)
		},
	}
}

func newGameDetectCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "detect",
		Short: "Find game installations",
		Long: "Find installations on for a selected game on this machine.\n\n" +
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
				"No installation was found. Set the game path with: "+
					"dmm config set game.path <path>")
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
		Short: "Check that a folder looks like a supported installation",
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
				// The mods folder can be unresolvable on its own, but the
				// installation was found, so warn.
				fmt.Fprintf(s.env.Err, "warning: %s\n", oneLine(err))
			}

			if s.opts.asJSON {
				view := newInstallView(*install)
				view.ModsPath = modsPath

				return writeJSON(s.env.Out, view)
			}

			fields := &Fields{}
			fields.SetOrDash("game", install.GameID)
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
