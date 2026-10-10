package commands

import (
	"fmt"

	manifestpkg "github.com/ChocolateNao/darktide-mod-manager/internal/manifest"
	"github.com/spf13/cobra"
)

// newManifestCommand builds the authoring commands for a mod's manifest.
//
// It is deliberately separate from `mod`: nothing here talks to the manager.
// A mod folder is something you write by hand, and scaffolding one should not
// need a database, a game installation or a configured source.
func newManifestCommand(s *session) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Create and inspect mod manifests",
		Long: "A mod folder can carry a manifest — " + manifestpkg.Names[0] +
			" or one of its accepted aliases — that names the mod and carries its\n" +
			"hook scripts. The manager reads the first one it finds in the folder,\n" +
			"and a folder with no manifest is still discovered under its own name.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		Args: cobra.NoArgs,
	}

	cmd.AddCommand(newManifestInitCommand(s))

	return cmd
}

// newManifestInitCommand writes a blank manifest into a mod folder.
//
// The folder is created when it does not exist: writing a manifest into a
// folder that is already there is the common case, and making the user mkdir
// first is a step with no information in it. An existing manifest is never
// overwritten — it holds work, and this command's whole job is to create a
// starter file.
func newManifestInitCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "init <path>",
		Short: "Write a blank manifest into a mod folder",
		Long: "Write a blank " + manifestpkg.Names[0] + " into the mod folder at <path>, creating the\n" +
			"folder if it is missing. Every field in the file is empty and commented,\n" +
			"so it is a starting point to edit rather than a claim about your mod.\n" +
			"\n" +
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
