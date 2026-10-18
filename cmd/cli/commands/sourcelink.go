package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/spf13/cobra"
)

// newSourceLinkCommand builds the commands that answer "where did this come from,
// and how do I get it again". Not every source can be downloaded on demand — Nexus
// serves files only to a signed-in client — so the link has to be one command away
// from the mod list rather than something the user reconstructs by hand.
func newSourceLinkCommand(s *session) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Show where an installed mod came from",
		Long: "Show the web locations recorded for an installed mod.\n\n" +
			"Some sources cannot be fetched automatically: a Nexus file needs a\n" +
			"signed-in client, and some mods are re-released as a new archive under\n" +
			"the same name. For those, follow the link and install the newest version\n" +
			"by hand — the manager keeps the link so you never have to remember it.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		Args: cobra.NoArgs,
	}

	cmd.AddCommand(newModSourceCommand(s), newModSourceListCommand(s))

	return cmd
}

// sourceLinkView is the JSON and text shape of one mod's links. It flattens the
// fallback into a sentence rather than a code, because the only question is "what do I do now".
type sourceLinkView struct {
	ModID       string `json:"mod_id"`
	SourceID    string `json:"source_id,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	UpdateURL   string `json:"update_url,omitempty"`
	Fallback    string `json:"fallback,omitempty"`
	Direct      bool   `json:"direct_download"`
}

func newModSourceCommand(s *session) *cobra.Command {
	var (
		target string
		open   bool
	)

	cmd := &cobra.Command{
		Use:   "show <mod-id>",
		Short: "Show or open one mod's source links",
		Long: "Print the links recorded for <mod-id>.\n\n" +
			"--target picks which one --open uses: source for the mod's page,\n" +
			"download for the file itself, update for the page that shows whether a\n" +
			"newer version exists, or auto to let the manager choose the most useful\n" +
			"link the mod actually has.",
		Example: "  dmm mod source show nexus:better-ui-1234\n" +
			"  dmm mod source show nexus:better-ui-1234 --open",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			modID := strings.TrimSpace(args[0])

			if open {
				fallback, err := client.OpenModSource(ctx, modID, target)
				if err != nil {
					return err
				}

				return describeOpenedLink(s, modID, fallback)
			}

			link, err := client.ModSourceLink(ctx, modID)
			if err != nil {
				return err
			}

			view := viewOfLink(modID, link)

			if s.opts.asJSON {
				return writeJSON(s.env.Out, view)
			}

			return writeLink(s, view)
		},
	}

	cmd.Flags().
		StringVar(&target, "target", "auto", "which link to open: auto, source, download or update")
	cmd.Flags().BoolVar(&open, "open", false, "open the link in a browser")

	return cmd
}

// describeOpenedLink says what was opened, and what to do if the chosen link was
// not the one that would have worked.
func describeOpenedLink(s *session, modID string, fallback *domain.ModLinkFallback) error {
	if s.opts.asJSON {
		return writeJSON(s.env.Out, map[string]any{"mod_id": modID, "opened": true})
	}

	_, err := fmt.Fprintf(s.env.Out, "Opened the page for %s.\n", modID)

	return err
}

func newModSourceListCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List source links for every installed mod",
		Long: "List the links of every installed mod, one line each.\n\n" +
			"Mods with no recorded link are listed as such: they came from a local\n" +
			"folder, and there is nothing on the web to go back to.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			links, err := client.ModSourceLinks(ctx)
			if err != nil {
				return err
			}

			views := make([]sourceLinkView, 0, len(links))
			for modID := range links {
				link := links[modID]
				views = append(views, viewOfLink(modID, &link))
			}

			sort.Slice(views, func(i, j int) bool {
				return views[i].ModID < views[j].ModID
			})

			if s.opts.asJSON {
				return writeJSON(s.env.Out, views)
			}

			return writeLinkTable(s, views)
		},
	}
}

// viewOfLink flattens a link into what the CLI prints. Direct is the interesting
// bit: a mod the manager could fetch has a download link, and one it could not a page.
func viewOfLink(modID string, link *domain.ModSourceLink) sourceLinkView {
	view := sourceLinkView{ModID: modID}
	if link == nil {
		return view
	}

	view.SourceID = link.SourceID
	view.SourceURL = link.SourceURL
	view.DownloadURL = link.DownloadURL
	view.UpdateURL = link.UpdateURL
	view.Direct = link.DownloadURL != ""

	if !view.Direct {
		view.Fallback = link.FallbackKind().Describe()
	}

	return view
}

func writeLink(s *session, view sourceLinkView) error {
	if view.SourceURL == "" && view.DownloadURL == "" && view.UpdateURL == "" {
		_, err := fmt.Fprintf(s.env.Out, "%s has no recorded web link.\n", view.ModID)
		return err
	}

	fields := [][2]string{
		{"page", view.SourceURL},
		{"download", view.DownloadURL},
		{"update", view.UpdateURL},
	}

	for _, field := range fields {
		if field[1] == "" {
			continue
		}

		if _, err := fmt.Fprintf(s.env.Out, "%-9s %s\n", field[0], field[1]); err != nil {
			return err
		}
	}

	if view.Fallback != "" {
		if _, err := fmt.Fprintf(s.env.Out, "\n%s\n", view.Fallback); err != nil {
			return err
		}
	}

	return nil
}

func writeLinkTable(s *session, views []sourceLinkView) error {
	if len(views) == 0 {
		_, err := fmt.Fprintln(s.env.Out, "No mods are installed.")
		return err
	}

	for _, view := range views {
		url := bestLink(view)
		if url == "" {
			url = "(none)"
		}

		if _, err := fmt.Fprintf(s.env.Out, "%-40s %s\n", view.ModID, url); err != nil {
			return err
		}
	}

	return nil
}

// bestLink prefers the file, then the page, then the update page — the order a
// user would visit them in.
func bestLink(view sourceLinkView) string {
	for _, url := range []string{view.DownloadURL, view.SourceURL, view.UpdateURL} {
		if url != "" {
			return url
		}
	}

	return ""
}

// newManifestWriteCommand refreshes an installed mod's manifest from the database,
// the other half of the manual-update path: a mod whose author re-releases the
// same archive gains metadata the manager's database now knows, so writing it again
// makes the folder self-describing for somebody installing it from disk.
func newManifestWriteCommand(s *session) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "write <mod-id>",
		Short: "Rewrite an installed mod's manifest from the database",
		Long: "Rewrite the manifest inside the folder of installed mod <mod-id>, using\n" +
			"everything the manager knows about it: name, version, author,\n" +
			"requirements, tags, source links and hook scripts.\n\n" +
			"This is the manual-update path. When a source cannot be downloaded\n" +
			"automatically, follow the mod's link, take the newest archive, install\n" +
			"it, and run this so the folder documents what it now contains.\n\n" +
			"An existing manifest is overwritten. Anything a mod author hand-wrote\n" +
			"that the database does not know about is lost, so read it first if you\n" +
			"care about it.",
		Example: "  dmm manifest write nexus:better-ui-1234",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			path, err := client.WriteModManifest(ctx, strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]string{"path": path, "mod_id": args[0]})
			}

			_, err = fmt.Fprintf(s.env.Out, "Wrote %s\n", path)

			return err
		},
	}

	return cmd
}
