package commands

import (
	"strings"

	"github.com/spf13/cobra"
)

func newShareCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "share",
		Short: "Share mods with other machines",
		Long: "Create and manage mod shares.\n\n" +
			"'dmm share p2p' connects two machines directly over WebRTC. The\n" +
			"archives never leave the two machines, and no third party holds them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(newShareP2PCommand(s))

	return group
}

func parseCSV(raw string) []string {
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}

	return result
}
