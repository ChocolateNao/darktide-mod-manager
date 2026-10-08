package commands

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/spf13/cobra"
)

// The share/import pair answers one question from both sides: "send me this
// mod". A Nexus or Vortex link is the currency mod communities trade in, and a
// manager that can neither print one nor accept one forces the user to open a
// browser and retype an id.
//
// The formats are the launchers' own, not this program's invention, and they
// are parsed here rather than handed to a source plugin: a link is a URI, and
// deciding which source a URI belongs to is a job for the caller that has every
// source registered.

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

			link, err := shareLink(*mod)
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
			sourceID, modID, grant, err := parseModLink(args[0])
			if err != nil {
				return err
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

			sourceData := grant.sourceData()
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

// shareLink builds the link that installs a mod somewhere else.
//
// The two mod stores number a *file* separately from a mod, and the file id is
// what the manager installs, so the link must carry both: a link with only the
// mod id resolves to a different file, and with only the file id there is
// nothing to show a person. The Nexus source keys a mod by the pair, the Vortex
// source by the file id alone, and parseModLink below builds the same shape
// each of them does — if the two disagreed, a shared link would install
// something else.
func shareLink(mod api.Mod) (string, error) {
	modID, hasModID := sourceDataInt(mod.SourceData, "mod_id")

	fileID, hasFileID := sourceDataInt(mod.SourceData, "file_id")
	switch strings.ToLower(strings.TrimSpace(mod.SourceID)) {
	case "nexus":
		if !hasModID || !hasFileID {
			return "", shareUnavailable(mod)
		}
		// The personal key is deliberately left out. It is a secret, and a
		// link is the one string in this program that gets pasted into a chat.
		//
		// The game name is nexusGame rather than a literal, because a link that
		// names a game this manager does not handle is refused on the way back
		// in — and a share/import pair that disagreed about the game would
		// produce links nothing could install.
		return fmt.Sprintf("nxm link %s/%d/%d", nexusGame, modID, fileID), nil
	case "github":
		if release := sourceDataText(mod.SourceData, "release_url"); release != "" {
			return release, nil
		}

		return "", shareUnavailable(mod)
	default:
		return "", shareUnavailable(mod)
	}
}

// shareUnavailable explains why a mod cannot be shared, naming the source so
// the message is actionable: the fix is to get the mod from a shareable source.
func shareUnavailable(mod api.Mod) error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"mod %q came from %s, which has no shareable link; "+
			"only nexus and github mods can be turned into a link",
		modLabelOf(mod.ID), orDash(mod.SourceID))
}

// parseModLink turns a launcher's link into the source and mod id to install.
//
// Two spellings are accepted for each store, because both are in circulation:
// the launcher's own scheme (nxm://) and the plain "nxm link a/b/c"
// form that people paste into chat because a launcher link is a long URL. The
// game name is accepted and ignored: a link names the game so the launcher
// knows which one to open, and a manager that only manages Darktide has
// nothing to do with it — but a link naming a different game is refused, since
// installing it would be wrong rather than merely useless.
//
// A Nexus link can also carry a download authorization in its query string,
// which is returned alongside the ids so the caller can hand it to the source
// that knows how to spend it.
func parseModLink(link string) (string, string, nxmGrant, error) {
	trimmed := strings.TrimSpace(link)

	if trimmed == "" {
		return "", "", nxmGrant{}, fatalf("no link given")
	}

	lower := strings.ToLower(trimmed)

	switch {
	case strings.HasPrefix(lower, "nxm://"), strings.HasPrefix(lower, "nxm link "):
		return parseNexusLink(trimmed)
	case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"):
		return "", "", nxmGrant{}, fatalf(
			"%q is a web address; give a Nexus link (nxm link <game>/<mod>/<file>)", trimmed)
	default:
		return "", "", nxmGrant{}, fatalf(
			"%q is not a mod link; expected nxm link <game>/<mod>/<file>", trimmed)
	}
}

// nexusGame is the game name the manager answers to. A link naming anything
// else is refused rather than ignored.
const nexusGame = "warhammer40kdarktide"

// nxmGrant is the one-time download authorization a keyed nxm link carries.
//
// The Nexus website puts these in the query string of a link it hands to a
// registered nxm:// handler: a free account cannot ask the API for a download
// URL, but the site can authorize one specific file for one specific download.
// The manager spends them at download time, which is the only thing they are
// good for — they are one-time and they expire.
type nxmGrant struct {
	key     string
	expires string
	userID  string
}

// authorized reports whether the grant is complete enough to spend. All three
// parts are required: the endpoint answers 403 to a partial set, and a 403
// reads as "this account cannot download" rather than "this link has no key".
func (g nxmGrant) authorized() bool {
	return g.key != "" && g.expires != "" && g.userID != ""
}

// sourceData renders the grant as the SourceData entries the Nexus source reads.
//
// The key names match the ones the Nexus plugin defines; the two spellings are
// a contract between this package and that one, and a link that is not a
// complete grant renders as nothing rather than as a partial one.
func (g nxmGrant) sourceData() map[string]any {
	if !g.authorized() {
		return nil
	}

	return map[string]any{
		"nxm_key":     g.key,
		"nxm_expires": g.expires,
		"nxm_user_id": g.userID,
	}
}

func parseNexusLink(link string) (string, string, nxmGrant, error) {
	body := link
	if idx := strings.Index(body, "://"); idx >= 0 {
		body = body[idx+3:]
	} else {
		body = strings.TrimSpace(body[len("nxm link"):])
	}
	// A launcher link can carry a query string. The ids this program understands
	// are all before the question mark; what follows it is the website's own
	// authorization for this one download, which is read separately below.
	query := url.Values{}

	if idx := strings.Index(body, "?"); idx >= 0 {
		parsed, err := url.ParseQuery(body[idx+1:])
		if err != nil {
			return "", "", nxmGrant{}, fatalf("%q has a query string that does not parse", link)
		}

		query = parsed
		body = body[:idx]
	}

	parts := strings.Split(strings.Trim(body, "/"), "/")
	// The launcher's own grammar is <game>/mods/<mod id>/files/<file id>, and the
	// plain form this program prints is <game>/<mod id>/<file id>. Both are in
	// circulation and differ only by the literal segments between the game and
	// the ids, so those are dropped rather than counted: what identifies a mod is
	// the last two numbers, and counting a literal as though it were an id would
	// read the word "files" as a mod.
	shape := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.EqualFold(part, "mods") || strings.EqualFold(part, "files") {
			continue
		}

		shape = append(shape, part)
	}

	parts = shape
	if len(parts) < 3 {
		return "", "", nxmGrant{}, fatalf(
			"%q is not a complete Nexus link; expected nxm://<game>/<mod id>/<file id>", link)
	}

	if err := checkGameName(parts[0], link); err != nil {
		return "", "", nxmGrant{}, err
	}
	// Both ids are part of the manager's identity for a Nexus mod, so both go
	// into it. The Nexus API numbers a mod and a mod file separately and the
	// manager installs a file, so a mod id alone would collapse every file on a
	// mod page into one entry, and a file id alone cannot survive being pasted
	// into a chat: the link is what carries the pair. This is the same shape
	// nexus.ModID(scoped, file) builds, and the two must agree.
	modID, ok := positiveInt(parts[len(parts)-2])
	if !ok {
		return "", "", nxmGrant{}, fatalf("%q has no numeric mod id", link)
	}

	fileID, ok := positiveInt(parts[len(parts)-1])
	if !ok {
		return "", "", nxmGrant{}, fatalf("%q has no numeric file id", link)
	}

	return "nexus", fmt.Sprintf("nexus-%d-%d", modID, fileID), nxmGrantFromQuery(query), nil
}

// nxmGrantFromQuery reads the download authorization out of a link's query
// string.
//
// A missing or partial authorization is not an error: a plain link is the
// common case and it installs exactly as it always has. What it must not do is
// look like a grant, because the endpoint answers 403 to a partial set and a
// 403 blames the account rather than the link.
func nxmGrantFromQuery(query url.Values) nxmGrant {
	return nxmGrant{
		key:     strings.TrimSpace(query.Get("key")),
		expires: strings.TrimSpace(query.Get("expires")),
		userID:  strings.TrimSpace(query.Get("user_id")),
	}
}

func checkGameName(name, link string) error {
	if strings.EqualFold(strings.TrimSpace(name), nexusGame) {
		return nil
	}

	return fatalf("%q is a link for %q, and this manager only handles %q",
		link, name, nexusGame)
}

func positiveInt(text string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}

	return value, true
}
