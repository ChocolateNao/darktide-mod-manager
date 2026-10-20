package commands

import (
	"fmt"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/spf13/cobra"
)

// The share/import pair answers one question from both sides: "send me this mod".
// The launchers' formats are parsed here; which source a URI belongs to is the
// caller's job.

func newModShareCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "share <mod>",
		Short: "Print a link that installs this mod elsewhere",
		Long: "Print a link another machine can install from.\n\n" +
			"A Nexus or Vortex mod gives its launcher's own link, and a GitHub mod\n" +
			"gives its release page. A mod from a local folder has nothing to share\n" +
			"with — it only exists on this machine — and the command says so\n" +
			"rather than printing something that cannot be opened.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			mod, err := client.GetMod(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			link, err := domain.ModLinkForMod(*mod)
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]string{
					"mod": mod.ID, "source": mod.SourceID, "link": link,
				})
			}

			_, err = fmt.Fprintln(s.env.Out, link)

			return err
		},
	}
}

func newModImportLinkCommand(s *session) *cobra.Command {
	var (
		noInstall   bool
		installMode string
	)

	command := &cobra.Command{
		Use:   "import-link <link>",
		Short: "Install a mod from a link",
		Long: "Install a mod named by a Nexus or Vortex link.\n\n" +
			"The link is resolved to its source and mod id and then installed the\n" +
			"same way `dmm mod install` would, so the version the source currently\n" +
			"offers is the version that gets installed.\n\n" +
			"A Nexus link the website has already authorised — one carrying key,\n" +
			"expires and user_id — is honoured: a free account cannot ask the API\n" +
			"for a download URL, but the site can authorise one specific file, and\n" +
			"the manager spends that authorization instead of reporting the\n" +
			"account as unable to download.\n\n" +
			"Install mode controls where the mod's payload is deployed:\n" +
			"  mods_folder (default) - deploy into the game's mods folder\n" +
			"  game_root - merge into the game root folder (for mods that patch the game)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceID, modID, game, grant, err := domain.ParseModLink(args[0])
			if err != nil {
				return err
			}

			// The parser reports the game's sub-domain; only the registry knows which
			// games are installed. Otherwise a link for another game resolves against
			// the active game's index and answers with a real, wrong mod.
			if _, ok := games.BySourceDomain(sourceID, game); !ok {
				return appErrors.New(appErrors.ErrCodeInvalidArgument,
					"%q is a link for the %q version of %s, which this manager does not have; "+
						"it can install links for %s",
					args[0], game, sourceID, strings.Join(games.SourceDomains(sourceID), ", "))
			}

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if noInstall {
				mod, err := client.GetSourceMod(cmd.Context(), sourceID, modID)
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
				fields.Set("source", orDash(mod.SourceID))

				return fields.Render(s.env.Out)
			}

			sourceData := grant
			if strings.TrimSpace(installMode) != "" {
				if sourceData == nil {
					sourceData = make(map[string]any)
				}

				sourceData["install_mode"] = installMode
			}

			mod, err := client.Install(cmd.Context(), sourceID, modID, api.InstallOptions{
				SourceData: sourceData,
			})
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, newModView(*mod))
			}

			fmt.Fprintf(s.env.Out, "installed %s v%s (%s)\n",
				mod.ID, orDash(mod.Version), modState(*mod))

			return nil
		},
	}
	command.Flags().BoolVar(
		&noInstall,
		"no-install",
		false,
		"show what the link points at without installing",
	)
	command.Flags().StringVar(
		&installMode,
		"install-mode",
		"",
		"install mode: mods_folder (default) or game_root",
	)

	return command
}
