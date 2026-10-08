package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCacheCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and maintain the download cache",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newCacheInfoCommand(s),
		newCacheClearCommand(s),
		newCachePruneCommand(s),
	)

	return group
}

// cacheView is the JSON shape of the cache report.
type cacheView struct {
	Path      string `json:"path,omitempty"`
	HumanSize string `json:"total_size"`
	Oldest    string `json:"oldest,omitempty"`
	Newest    string `json:"newest,omitempty"`
	Freed     string `json:"freed,omitempty"`
	Files     int    `json:"files"`
	TotalSize int64  `json:"total_bytes"`
	Removed   int    `json:"removed,omitempty"`
}

func newCacheInfoCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Report what the download cache holds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			info, err := client.GetCacheInfo(cmd.Context())
			if err != nil {
				return err
			}

			view := cacheView{
				Path:      info.Path,
				Files:     info.FileCount,
				TotalSize: info.TotalSize,
				HumanSize: humanSize(info.TotalSize),
				Oldest:    formatStamp(info.Oldest),
				Newest:    formatStamp(info.Newest),
			}
			if s.opts.asJSON {
				return writeJSON(s.env.Out, view)
			}

			fields := &Fields{}
			fields.SetOrDash("path", view.Path)
			fields.Setf("files", "%d", view.Files)
			fields.Setf("size", "%s (%d bytes)", view.HumanSize, view.TotalSize)
			fields.SetOrDash("oldest", view.Oldest)
			fields.SetOrDash("newest", view.Newest)

			return fields.Render(s.env.Out)
		},
	}
}

func newCacheClearCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Delete everything in the download cache",
		Long: "Delete every cached archive.\n\n" +
			"Installed mods are unaffected — their payloads live in the mod\n" +
			"storage folder, not here. The cost is re-downloading each archive\n" +
			"the next time one is installed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.ClearCache(cmd.Context()); err != nil {
				return err
			}

			_, err = fmt.Fprintln(s.env.Out, "cache cleared")

			return err
		},
	}
}

func newCachePruneCommand(s *session) *cobra.Command {
	var (
		maxSizeGB float64
		maxAge    int
	)

	command := &cobra.Command{
		Use:   "prune",
		Short: "Delete cache entries that are too old or too large",
		Long: "Delete cache entries that are older than --max-age-days or push the\n" +
			"cache over --max-size-gb.\n\n" +
			"With neither flag the limits from the settings file are used. A limit\n" +
			"of zero means no limit on that axis, so `prune --max-age-days 0`\n" +
			"keeps every size and only the age applies.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}
			// The cache is measured on both sides of the prune so the message can
			// report what was removed. Reporting the size afterwards as though it
			// were the size removed claimed that a prune which freed nothing had
			// freed everything, which is the one thing this command is asked to
			// answer.
			before, err := client.GetCacheInfo(cmd.Context())
			if err != nil {
				return err
			}

			if cmd.Flags().Changed("max-size-gb") || cmd.Flags().Changed("max-age-days") {
				if maxSizeGB < 0 {
					return fatalf("--max-size-gb cannot be negative, got %g", maxSizeGB)
				}

				if maxAge < 0 {
					return fatalf("--max-age-days cannot be negative, got %d", maxAge)
				}

				if err := client.PruneCache(cmd.Context(), int(maxSizeGB), maxAge); err != nil {
					return err
				}
			} else if err := client.PruneCacheWithDefaults(cmd.Context()); err != nil {
				return err
			}

			info, err := client.GetCacheInfo(cmd.Context())
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, cacheView{
					Path:      info.Path,
					Files:     info.FileCount,
					TotalSize: info.TotalSize,
					HumanSize: humanSize(info.TotalSize),
					Removed:   before.FileCount - info.FileCount,
					Freed:     humanSize(before.TotalSize - info.TotalSize),
				})
			}

			removed := before.FileCount - info.FileCount
			if removed <= 0 {
				_, err = fmt.Fprintf(s.env.Out,
					"cache pruned: nothing removed; %s still cached\n",
					humanSize(info.TotalSize))

				return err
			}

			_, err = fmt.Fprintf(s.env.Out, "cache pruned: removed %s, freed %s\n",
				plural(removed, "file", "files"), humanSize(before.TotalSize-info.TotalSize))

			return err
		},
	}
	flags := command.Flags()
	flags.Float64Var(
		&maxSizeGB,
		"max-size-gb",
		0,
		"keep the cache under this many gigabytes (0 is no limit)",
	)
	flags.IntVar(
		&maxAge,
		"max-age-days",
		0,
		"delete entries older than this many days (0 is no limit)",
	)

	return command
}
