package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/spf13/cobra"
)

// cloudShareNotice is printed alongside every cloud share result.
//
// The cloud path still works and is not removed, but it is on its way out: every
// mod it moves passes through a third party's storage, and that is exactly what
// the peer-to-peer path exists to avoid. Saying so on the command that would use
// it is cheaper than a surprise later.
const cloudShareNotice = "cloud sharing is deprecated: " +
	"it stores the mods on a third-party provider. Use `dmm share p2p` instead."

func newShareCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "share",
		Short: "Share mods with other machines",
		Long: "Create and manage mod shares.\n\n" +
			"There are two ways to move a mod from this machine to another.\n\n" +
			"'dmm share p2p' connects the two machines directly over WebRTC and is\n" +
			"what new work should use: the archives never leave the two machines, and\n" +
			"no provider holds them.\n\n" +
			"The cloud commands below predate that and are deprecated: they leave the\n" +
			"mods on a third-party provider, which means somebody else holds the files\n" +
			"and can serve them to anyone who guesses the identifier.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newShareP2PCommand(s),
		newShareCreateCommand(s),
		newShareListCommand(s),
		newShareShowCommand(s),
		newShareRevokeCommand(s),
		newShareUpdateCommand(s),
	)

	// The group itself is deliberately NOT marked Deprecated. Cobra hides a
	// deprecated command together with everything under it, so marking `share`
	// would hide `share p2p` — the replacement — behind the very notice telling
	// users to find it. The deprecation belongs on the cloud leaves, where it
	// costs nothing to carry.

	return group
}

// warnCloudShare writes the deprecation notice where the user will actually see
// it: next to the result, not only in the help text they may never have read.
//
// It goes to stderr because stdout is the machine-readable channel here — a
// script reading JSON must not have to strip a sentence out of it first.
func warnCloudShare(s *session) {
	fmt.Fprintln(s.env.Err, "// warning: "+cloudShareNotice)
}

type shareView struct {
	ID        string   `json:"id"`
	GameID    string   `json:"game_id"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Providers []string `json:"providers"`
	ModCount  int      `json:"mod_count"`
}

type shareDetailView struct {
	ID        string          `json:"id"`
	GameID    string          `json:"game_id"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	Mods      []shareModView  `json:"mods"`
	Links     []shareLinkView `json:"links"`
}

type shareModView struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Author  string `json:"author"`
}

type shareLinkView struct {
	URL       string `json:"url"`
	Provider  string `json:"provider"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

func newShareCreateCommand(s *session) *cobra.Command {
	var (
		modsFlag     string
		providerFlag string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Upload mods to a cloud provider and get a shareable link (deprecated)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			warnCloudShare(s)

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			modNames := parseCSV(modsFlag)
			if len(modNames) == 0 {
				return fmt.Errorf("no mods specified: use --mods \"mod1,mod2\"")
			}

			var mods []domain.Mod

			for _, name := range modNames {
				mod, err := client.GetMod(cmd.Context(), name)
				if err != nil {
					return fmt.Errorf("mod %q: %w", name, err)
				}

				mods = append(mods, *mod)
			}

			providerID := providerFlag
			if providerID == "" {
				providerID = client.DefaultShareProvider()
			}

			result, err := client.CreateShare(cmd.Context(), mods, []string{providerID})
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, result)
			}

			fmt.Fprintf(s.env.Out, "Share created: %s\n", result.Primary.URL)
			fmt.Fprintf(s.env.Out, "Share ID: %s\n", result.ShareID)

			for _, fb := range result.Fallbacks {
				fmt.Fprintf(s.env.Out, "Fallback: %s\n", fb.URL)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&modsFlag, "mods", "", "comma-separated mod ids to share")
	cmd.Flags().StringVar(&providerFlag, "provider", "", "cloud provider id")

	return cmd
}

func newShareListCommand(s *session) *cobra.Command {
	var gameFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all cloud shares (deprecated)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			warnCloudShare(s)

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			shares, err := client.ListShares(cmd.Context())
			if err != nil {
				return err
			}

			if gameFilter != "" {
				shares, err = client.ListSharesByGame(cmd.Context(), gameFilter)
				if err != nil {
					return err
				}
			}

			views := make([]shareView, 0, len(shares))
			for _, share := range shares {
				providers := make([]string, 0, len(share.Links))
				for _, link := range share.Links {
					providers = append(providers, link.ProviderID)
				}

				views = append(views, shareView{
					ID:        share.ID,
					GameID:    share.GameID,
					ModCount:  len(share.Mods),
					Providers: providers,
					CreatedAt: share.CreatedAt.Format(time.RFC3339),
					UpdatedAt: share.UpdatedAt.Format(time.RFC3339),
				})
			}

			return s.writeJSONOrTable(s.env.Out, views, shareTable(views), "No shares found.")
		},
	}

	cmd.Flags().StringVar(&gameFilter, "game", "", "filter by game id")

	return cmd
}

func newShareShowCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "show <share-id>",
		Short: "Show details of a cloud share (deprecated)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnCloudShare(s)

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			record, err := client.GetShare(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, record)
			}

			mods := make([]shareModView, 0, len(record.Mods))
			for _, m := range record.Mods {
				mods = append(
					mods,
					shareModView{Name: m.Name, Version: m.Version, Author: m.Author},
				)
			}

			links := make([]shareLinkView, 0, len(record.Links))
			for _, l := range record.Links {
				view := shareLinkView{URL: l.URL, Provider: l.ProviderID}
				if l.ExpiresAt != nil {
					view.ExpiresAt = l.ExpiresAt.Format(time.RFC3339)
				}

				links = append(links, view)
			}

			fmt.Fprintf(s.env.Out, "Share %s (%s)\n", record.ID, record.GameID)
			fmt.Fprintln(s.env.Out)

			modTable := NewTable("Mod", "Version", "Author")
			for _, m := range mods {
				modTable.Add(m.Name, m.Version, m.Author)
			}

			if err := modTable.Render(s.env.Out); err != nil {
				return err
			}

			fmt.Fprintln(s.env.Out)

			linkTable := NewTable("Provider", "URL", "Expires")
			for _, l := range links {
				linkTable.Add(l.Provider, l.URL, l.ExpiresAt)
			}

			return linkTable.Render(s.env.Out)
		},
	}
}

func newShareRevokeCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <share-id>",
		Short: "Revoke a cloud share and delete remote files (deprecated)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnCloudShare(s)

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.RevokeShare(cmd.Context(), args[0]); err != nil {
				return err
			}

			fmt.Fprintf(s.env.Out, "Share %s revoked.\n", args[0])

			return nil
		},
	}
}

func newShareUpdateCommand(s *session) *cobra.Command {
	var (
		addFlag    string
		removeFlag string
	)

	cmd := &cobra.Command{
		Use:   "update <share-id>",
		Short: "Add or remove mods from an existing share (deprecated)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnCloudShare(s)

			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			addNames := parseCSV(addFlag)
			removeNames := parseCSV(removeFlag)

			var addMods []domain.Mod

			for _, name := range addNames {
				mod, err := client.GetMod(cmd.Context(), name)
				if err != nil {
					return fmt.Errorf("mod %q: %w", name, err)
				}

				addMods = append(addMods, *mod)
			}

			var removeMods []domain.Mod

			for _, name := range removeNames {
				mod, err := client.GetMod(cmd.Context(), name)
				if err != nil {
					return fmt.Errorf("mod %q: %w", name, err)
				}

				removeMods = append(removeMods, *mod)
			}

			record, err := client.UpdateShare(cmd.Context(), args[0], addMods, removeMods)
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, record)
			}

			fmt.Fprintf(s.env.Out, "Share %s now has %d mods.\n", record.ID, len(record.Mods))

			return nil
		},
	}

	cmd.Flags().StringVar(&addFlag, "add", "", "comma-separated mod ids to add")
	cmd.Flags().StringVar(&removeFlag, "remove", "", "comma-separated mod ids to remove")

	return cmd
}

func shareTable(views []shareView) *Table {
	table := NewTable("ID", "Game", "Mods", "Providers", "Created")
	for _, v := range views {
		table.Add(
			v.ID,
			v.GameID,
			fmt.Sprintf("%d", v.ModCount),
			strings.Join(v.Providers, ","),
			v.CreatedAt,
		)
	}

	return table
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
