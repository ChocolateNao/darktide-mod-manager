package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newConfigCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "config",
		Short: "Read and change the settings",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newConfigShowCommand(s),
		newConfigGetCommand(s),
		newConfigSetCommand(s),
		newConfigEditCommand(s),
	)

	return group
}

func newConfigShowCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the whole settings file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			path, err := client.ConfigPath()
			if err != nil {
				return err
			}

			cfg, err := client.GetConfig(cmd.Context())
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, cfg)
			}

			fmt.Fprintf(s.env.Out, "# %s\n", path)
			// The decoded configuration is printed deliberately: this shows what the
			// manager believes, defaults and all.
			return writeJSON(s.env.Out, cfg)
		},
	}
}

func newConfigGetCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Read one setting",
		Long: "Read one setting by its dotted key, for example:\n" +
			"  dmm config get cache.max_size_gb\n" +
			"  dmm config get sources.nexus.enabled",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			value, err := client.GetConfigValue(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			_, err = fmt.Fprintln(s.env.Out, value)

			return err
		},
	}
}

func newConfigSetCommand(s *session) *cobra.Command {
	command := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change one setting",
		Long: "Change one setting by its dotted key.\n\n" +
			"Run `dmm config show` for the whole shape, and note that a source's\n" +
			"api_key can be written but never printed back.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.SetConfigValue(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}

			_, err = fmt.Fprintf(s.env.Out, "%s = %s\n", strings.TrimSpace(args[0]),
				strings.TrimSpace(args[1]))

			return err
		},
	}
	// The value is whatever the user typed, even something that looks like a flag:
	// `dmm config set cache.max_size_gb -1` would otherwise be refused as an unknown shorthand.
	command.Flags().SetInterspersed(false)

	return command
}

func newConfigEditCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Print the settings file to edit in place",
		Long: "Print where the settings file is and what the manager reads from it.\n\n" +
			"This command has to work over a pipe and in a test, and an editor\n" +
			"it launches is neither. The file is plain JSON and the manager\n" +
			"re-reads it on the next run.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			path, err := client.ConfigPath()
			if err != nil {
				return err
			}

			fmt.Fprintf(s.env.Out, "settings file: %s\n", path)

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]string{"path": path})
			}

			return nil
		},
	}
}
