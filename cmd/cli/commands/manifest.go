package commands

import (
	"fmt"

	manifestpkg "github.com/ChocolateNao/darktide-mod-manager/internal/manifest"
	"github.com/spf13/cobra"
)

// newManifestCommand builds the authoring commands for a mod's manifest. Nothing
// here talks to the manager, so scaffolding a folder needs no database or game
// installation.
func newManifestCommand(s *session) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Create and inspect mod manifests",
		Long: "A mod folder can carry a manifest — " + manifestpkg.Names[0] +
			" or one of its accepted aliases — that names the mod and carries its\n" +
			"hook scripts. The manager reads the first one it finds in the folder,\n" +
			"and a folder with no manifest is still discovered under its own name.\n\n" +
			"`init` scaffolds one by hand. `write` regenerates one for a mod that is\n" +
			"already installed, from everything the database knows about it.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		Args: cobra.NoArgs,
	}

	cmd.AddCommand(newManifestInitCommand(s), newManifestWriteCommand(s))

	return cmd
}

// newManifestInitCommand writes a blank manifest, creating the folder when
// missing. An existing manifest is never overwritten: it holds work.
func newManifestInitCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "init <path>",
		Short: "Write a blank manifest into a mod folder",
		Long: "Write a blank " + manifestpkg.Names[0] + " into the mod folder at <path>, creating the\n" +
			"folder if it is missing. Every field in the file is empty and commented.\n\n" +
			"An existing manifest is never overwritten: point this at the folder of a\n" +
			"new mod, or edit the file it already wrote.",
		Example: "  dmm manifest init mods/better-ui",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := manifestpkg.Init(args[0])
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]string{
					"path":     path,
					"template": manifestpkg.Template,
				})
			}

			_, err = fmt.Fprintf(
				s.env.Out,
				"Created %s\nEdit it to describe your mod, then point a local source at this folder.\n",
				path,
			)

			return err
		},
	}
}
