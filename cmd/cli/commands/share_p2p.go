package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/spf13/cobra"
)

// newShareP2PCommand builds the peer-to-peer group, shaped around a share being
// a conversation between two people probably not at the same machine at the same
// time: `create` prints an offer link and waits, `accept` prints an answer and
// waits. Both sides stay running until the transfer is done.
func newShareP2PCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "p2p",
		Short: "Share mods directly between two machines",
		Long: "Shares mods over a direct WebRTC connection: the bytes go from one\n" +
			"machine to the other over DTLS and no cloud provider ever sees them.\n" +
			"The exchange is two links — an offer out, an answer back — and both sides\n" +
			"stay running until the transfer finishes.\n\n" +
			"A STUN server may be consulted to discover a public address, but it never\n" +
			"carries mod data; see 'dmm share p2p create --help' for the defaults.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newShareP2PCreateCommand(s),
		newShareP2PAcceptCommand(s),
		newShareP2PPreviewCommand(s),
		newShareP2PRejectCommand(s),
		newShareP2PInstallCommand(s),
		newShareP2PSessionsCommand(s),
		newShareP2PCancelCommand(s),
	)

	return group
}

// p2pSessionView is the machine-readable shape of one share in progress.
type p2pSessionView struct {
	OfferID     string `json:"offer_id"`
	Role        string `json:"role"`
	GameID      string `json:"game_id"`
	State       string `json:"state"`
	Peer        string `json:"peer,omitempty"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ModCount    int    `json:"mod_count"`
	TotalSize   int64  `json:"total_size"`
	Transferred int64  `json:"transferred_bytes"`
}

func viewOfSession(session domain.P2PShareSession) p2pSessionView {
	view := p2pSessionView{
		OfferID:     session.OfferID,
		Role:        session.Role,
		GameID:      session.GameID,
		State:       string(session.State),
		ModCount:    len(session.ModIDs),
		TotalSize:   session.TotalSize,
		Transferred: session.TransferredBytes,
		CreatedAt:   session.CreatedAt.Format(time.RFC3339),
		Reason:      session.FailReason,
	}

	if session.ExpiresAt != nil {
		view.ExpiresAt = session.ExpiresAt.Format(time.RFC3339)
	}

	return view
}

func newShareP2PCreateCommand(s *session) *cobra.Command {
	var (
		modsFlag   string
		expiryFlag string
		stunFlag   []string
		waitFlag   bool
		answerFlag string
		nameFlag   string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Pack mods and print an offer link to hand to somebody",
		Long: "Packs the given mods, gathers a connection description, and prints the\n" +
			"link to send to the recipient.\n\n" +
			"By default the command then waits: paste the recipient's answer link into\n" +
			"this terminal and the transfer starts. Pass --no-wait to print the link and\n" +
			"exit — but be aware that the offer only lives while this process does, so a\n" +
			"link printed that way will not connect to anything afterwards.\n\n" +
			"Add --expires to limit how long the link stays usable. Without it the share\n" +
			"is permanent, which is rarely what you want on a public channel.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			modNames := parseCSV(modsFlag)
			if len(modNames) == 0 {
				return fmt.Errorf("no mods specified: use --mods \"mod1,mod2\"")
			}

			mods := make([]domain.Mod, 0, len(modNames))

			for _, name := range modNames {
				mod, err := client.GetMod(ctx, name)
				if err != nil {
					return fmt.Errorf("mod %q: %w", name, err)
				}

				mods = append(mods, *mod)
			}

			opts, err := p2pCreateOptions(expiryFlag, stunFlag)
			opts.SenderName = strings.TrimSpace(nameFlag)

			if err != nil {
				return err
			}

			link, err := client.CreateP2PShare(ctx, mods, opts)
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				if err := writeJSON(s.env.Out, link); err != nil {
					return err
				}
			} else {
				s.describeOffer(link)
			}

			answer := strings.TrimSpace(answerFlag)
			if answer == "" {
				if !waitFlag {
					return nil
				}

				fmt.Fprintf(s.env.Err, "\nPaste the recipient's answer link (dmm://p2pa/...): ")

				answer, err = readAnswerLink(s.env.In)
				if err != nil {
					return fmt.Errorf("reading the answer link: %w", err)
				}
			}

			if answer == "" {
				return fmt.Errorf("no answer link was given, so nothing was sent")
			}

			if err := client.SendP2PShare(ctx, link.Offer.OfferID, answer); err != nil {
				return err
			}

			return s.writeSessionOutcome(ctx, client, link.Offer.OfferID)
		},
	}

	cmd.Flags().StringVar(&modsFlag, "mods", "", "comma-separated mod ids to share")
	cmd.Flags().StringVar(&expiryFlag, "expires", "",
		"when the link stops working, as an RFC 3339 timestamp or a duration such as 24h")
	cmd.Flags().StringArrayVar(&stunFlag, "stun", nil,
		"STUN server used to discover a public address (repeatable)")
	cmd.Flags().BoolVar(&waitFlag, "wait", true,
		"keep running and read the recipient's answer link from stdin")
	cmd.Flags().
		StringVar(&answerFlag, "answer", "", "the recipient's answer link, if you already have it")
	cmd.Flags().StringVar(
		&nameFlag,
		"name",
		"",
		"a name shown to the recipient before they accept; it is not verified, so treat it as a hint",
	)

	return cmd
}

// p2pCreateOptions interprets the expiry in whichever form the user found natural:
// a duration ("24h") or an absolute timestamp, which both mean the same thing to
// the protocol.
func p2pCreateOptions(expiry string, stun []string) (domain.P2PCreateOptions, error) {
	opts := domain.P2PCreateOptions{}

	if trimmed := strings.TrimSpace(expiry); trimmed != "" {
		when, err := time.Parse(time.RFC3339, trimmed)
		if err != nil {
			parsed, perr := time.ParseDuration(trimmed)
			if perr != nil {
				return domain.P2PCreateOptions{}, fmt.Errorf(
					"expiry %q is neither an RFC 3339 timestamp nor a duration such as 24h", expiry)
			}

			when = time.Now().Add(parsed)
		}

		opts.ExpiresAt = &when
	}

	for _, server := range stun {
		if trimmed := strings.TrimSpace(server); trimmed != "" {
			opts.STUNServers = append(opts.STUNServers, trimmed)
		}
	}

	return opts, nil
}

// describeOffer prints everything the sender needs before handing a link to
// somebody: what is in it, whether it expires, and that STUN never carries data.
func (s *session) describeOffer(link *domain.P2PShareLink) {
	if link == nil {
		return
	}

	offer := link.Offer

	fmt.Fprintf(s.env.Out, "Offer %s for %s\n", offer.OfferID, offer.GameID)
	fmt.Fprintf(s.env.Out, "Mods: %s\n", strings.Join(modNames(offer.Mods), ", "))

	if offer.ExpiresAt != nil {
		fmt.Fprintf(s.env.Out, "Expires: %s\n", offer.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Fprintln(s.env.Out, "Expires: never")
	}

	if offer.STUNServers != nil {
		fmt.Fprintf(s.env.Out, "STUN: %s\n", strings.Join(offer.STUNServers, ", "))
	}

	fmt.Fprintln(s.env.Out)
	fmt.Fprintln(s.env.Out, "Link:")
	fmt.Fprintln(s.env.Out, link.Link)
}

func modNames(mods []domain.SharedMod) []string {
	names := make([]string, 0, len(mods))

	for _, mod := range mods {
		names = append(names, mod.Name)
	}

	return names
}

func newShareP2PPreviewCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "preview <offer-link>",
		Short: "Show what an offer link contains without connecting",
		Long: "Opens an offer link locally and reports what it claims to contain: the\n" +
			"mods, their sizes, where each can be downloaded instead, and whether the\n" +
			"link has already expired.\n\n" +
			"Nothing is sent and no connection is made. This is the safe way to look\n" +
			"at a link somebody sent you.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			preview, err := client.PreviewP2PShare(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, preview)
			}

			return s.describePreview(preview)
		},
	}
}

// describePreview is the answer to "should I trust this link?": what it holds,
// what it costs, and where every mod can be fetched instead, so declining costs
// the recipient nothing but the sender's bandwidth.
func (s *session) describePreview(preview *domain.P2PSharePreview) error {
	if preview == nil {
		return nil
	}

	fmt.Fprintf(s.env.Out, "Offer %s from %s (%s)\n",
		preview.OfferID, unknownSender(preview.SenderName), preview.GameID)

	if preview.Expired {
		fmt.Fprintln(s.env.Out, "This link has expired.")
	}

	if preview.ExpiresAt != nil {
		fmt.Fprintf(s.env.Out, "Expires: %s\n", preview.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Fprintln(s.env.Out, "Expires: never")
	}

	fmt.Fprintln(s.env.Out)

	// The manifest arrives as two lists, joined here so one table can answer "how
	// big is this share, and where do I get it instead".
	sizes := archiveSizesByMod(preview.Files)
	names := modNamesByID(preview)

	modTable := NewTable("Mod", "Version", "Size")
	for _, mod := range preview.Mods {
		modTable.Add(mod.Name, mod.Version, humanSize(sizeForMod(sizes, mod)))
	}

	if err := modTable.Render(s.env.Out); err != nil {
		return err
	}

	if len(preview.SourceLinks) == 0 {
		return nil
	}

	fmt.Fprintln(s.env.Out)

	linkTable := NewTable("Mod", "Source", "Download elsewhere")
	for _, link := range preview.SourceLinks {
		linkTable.Add(unknownName(names[link.ModID], link.ModID),
			link.SourceID, bestSourceURL(link))
	}

	return linkTable.Render(s.env.Out)
}

// archiveSizesByMod totals each archive's size, keyed by archive name because that
// is what a mod's FileIDs holds — joining on anything else silently yields zero.
func archiveSizesByMod(files []domain.P2PSharedFile) map[string]int64 {
	sizes := make(map[string]int64, len(files))

	for _, file := range files {
		sizes[file.Name] = file.Size
	}

	return sizes
}

// sizeForMod sums the mod's archives. A mod declaring no files reports zero rather
// than failing: the listing describes somebody else's share.
func sizeForMod(sizes map[string]int64, mod domain.SharedMod) int64 {
	var total int64

	for _, id := range mod.FileIDs {
		total += sizes[id]
	}

	return total
}

// modNamesByID maps a mod id to the name the user recognises it by, so a source
// link can sit next to it. Names are recorded for the archives' mod ids too.
func modNamesByID(preview *domain.P2PSharePreview) map[string]string {
	names := make(map[string]string, len(preview.Mods))
	archiveToMod := make(map[string]string, len(preview.Files))

	for _, file := range preview.Files {
		archiveToMod[file.Name] = file.ModID
	}

	for _, mod := range preview.Mods {
		for _, archive := range mod.FileIDs {
			if id, ok := archiveToMod[archive]; ok && id != "" {
				names[id] = mod.Name
			}
		}
	}

	return names
}

// unknownName is what a table prints for a mod it could not name. The id is
// preferred over a placeholder: it is what `accept` and `install` take.
func unknownName(name, id string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}

	if strings.TrimSpace(id) != "" {
		return id
	}

	return "an unnamed mod"
}

// unknownSender is what a preview prints when the link names nobody — deliberately
// unlike an unnamed mod, which means a manifest was malformed.
func unknownSender(name string) string {
	if strings.TrimSpace(name) == "" {
		return "an unknown sender"
	}

	return name
}

// bestSourceURL picks the one link to print: a recipient who declines needs one
// place to go, not three.
func bestSourceURL(link domain.ModSourceLink) string {
	switch {
	case link.DownloadURL != "":
		return link.DownloadURL
	case link.SourceURL != "":
		return link.SourceURL
	default:
		return link.UpdateURL
	}
}

func newShareP2PAcceptCommand(s *session) *cobra.Command {
	var (
		nameFlag    string
		rejectFlag  bool
		reasonFlag  string
		installFlag bool
		noWaitFlag  bool
	)

	cmd := &cobra.Command{
		Use:   "accept <offer-link>",
		Short: "Answer an offer, receive the mods, and verify them",
		Long: "Prints what the offer contains and where each mod can be downloaded\n" +
			"instead, then answers it and waits for the transfer.\n\n" +
			"Every archive is checked against the hash the offer carried before this\n" +
			"command reports success, so a transfer that was cut short or altered fails\n" +
			"here rather than at install time.\n\n" +
			"Pass --reject to decline. The sender is told, and the source links above\n" +
			"are how you get the mods without them.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			link := strings.TrimSpace(args[0])

			// The preview comes first and always: a recipient who has not seen the
			// contents has not really been offered a choice.
			preview, err := client.PreviewP2PShare(ctx, link)
			if err != nil {
				return err
			}

			if !s.opts.asJSON {
				if err := s.describePreview(preview); err != nil {
					return err
				}

				fmt.Fprintln(s.env.Out)
			}

			if preview.Expired {
				return fmt.Errorf("this link expired at %s",
					preview.ExpiresAt.Format(time.RFC3339))
			}

			if rejectFlag {
				reason := strings.TrimSpace(reasonFlag)
				if reason == "" {
					reason = "declined: I would rather download from the source"
				}

				return s.rejectOffer(cmd, client, link, reason)
			}

			answer, err := client.AcceptP2PShare(ctx, link, strings.TrimSpace(nameFlag))
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				if err := writeJSON(s.env.Out, answer); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(s.env.Out, "Answer link — send this back to the sender:")
				fmt.Fprintln(s.env.Out, answer.Link)
			}

			if noWaitFlag {
				fmt.Fprintln(s.env.Out,
					"Not waiting for the transfer; this offer will be gone when the command exits.")

				return nil
			}

			if err := client.AwaitP2PShare(ctx, answer.OfferID); err != nil {
				return err
			}

			fmt.Fprintf(s.env.Out, "Received and verified %s.\n",
				plural(len(preview.Mods), "mod", "mods"))

			if !installFlag {
				fmt.Fprintln(s.env.Out, "Nothing was installed. Re-run with --install to install.")

				return nil
			}

			installed, err := client.InstallP2PShare(
				ctx,
				answer.OfferID,
				domain.InstallOptions{AutoInstall: true},
			)
			if err != nil {
				return err
			}

			fmt.Fprintf(s.env.Out, "Installed %s.\n", plural(len(installed), "mod", "mods"))

			return nil
		},
	}

	cmd.Flags().StringVar(&nameFlag, "name", "", "your name, shown to the sender")
	cmd.Flags().BoolVar(&rejectFlag, "reject", false, "decline the offer instead of accepting it")
	cmd.Flags().StringVar(&reasonFlag, "reason", "", "why you are declining, shown to the sender")
	cmd.Flags().BoolVar(&installFlag, "install", true, "install the mods once they are verified")
	cmd.Flags().
		BoolVar(
			&noWaitFlag,
			"no-wait",
			false,
			"print the answer link and exit instead of receiving",
		)

	return cmd
}

func newShareP2PRejectCommand(s *session) *cobra.Command {
	var reasonFlag string

	cmd := &cobra.Command{
		Use:   "reject <offer-link>",
		Short: "Decline an offer, tell the sender why, and print the answer link",
		Long: "Declines an offer.\n\n" +
			"The sender holds the connection offer, so the refusal cannot travel until a\n" +
			"channel opens — which needs the answer link this command prints. Send it back,\n" +
			"then leave this command running until it says the refusal was delivered.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			reason := strings.TrimSpace(reasonFlag)
			if reason == "" {
				reason = "declined: I would rather download from the source"
			}

			return s.rejectOffer(cmd, client, strings.TrimSpace(args[0]), reason)
		},
	}

	cmd.Flags().StringVar(&reasonFlag, "reason", "", "why you are declining, shown to the sender")

	return cmd
}

// rejectOffer is shared by `reject` and `accept --reject`, which differ only in
// whether the caller already printed a preview. The answer link goes out before
// the wait: the sender cannot open the connection until they have it.
func (s *session) rejectOffer(
	cmd *cobra.Command,
	client p2pClient,
	link, reason string,
) error {
	ctx := cmd.Context()

	answer, err := client.RejectP2PShare(ctx, link, reason)
	if err != nil {
		return err
	}

	fmt.Fprintln(s.env.Out, "Answer link — send this back to the sender:")
	fmt.Fprintln(s.env.Out, answer.Link)

	if err := client.AwaitRejectedP2PShare(ctx, answer.OfferID); err != nil {
		return err
	}

	fmt.Fprintf(s.env.Out, "Refusal delivered: %s\n", reason)
	fmt.Fprintln(s.env.Out, "The mods are still available from the links above.")

	return nil
}

// newShareP2PInstallCommand installs a share already received in this session.
// It lives with accept on purpose: the received archives are staged in a
// temporary directory owned by this process.
func newShareP2PInstallCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install a share that has already been received in this session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			sessions, err := client.ListP2PShareSessions(ctx)
			if err != nil {
				return err
			}

			for _, session := range sessions {
				if session.State == domain.P2PShareStateCompleted {
					installed, err := client.InstallP2PShare(ctx, session.OfferID,
						domain.InstallOptions{AutoInstall: true})
					if err != nil {
						return err
					}

					fmt.Fprintf(s.env.Out, "Installed %s from %s.\n",
						plural(len(installed), "mod", "mods"), session.OfferID)

					return nil
				}
			}

			return fmt.Errorf(
				"no completed share in this session; use `dmm share p2p accept --install` to receive and install",
			)
		},
	}
}

func newShareP2PSessionsCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List the shares this process knows about",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			sessions, err := client.ListP2PShareSessions(cmd.Context())
			if err != nil {
				return err
			}

			views := make([]p2pSessionView, 0, len(sessions))
			for _, session := range sessions {
				views = append(views, viewOfSession(session))
			}

			table := NewTable("Offer", "Role", "Game", "State", "Mods", "Received")
			for _, view := range views {
				table.Add(
					view.OfferID,
					view.Role,
					view.GameID,
					view.State,
					fmt.Sprintf("%d", view.ModCount),
					humanSize(view.Transferred),
				)
			}

			return s.writeJSONOrTable(
				s.env.Out,
				views,
				table,
				"No shares in this session. A share lives for as long as the process that created it.",
			)
		},
	}
}

func newShareP2PCancelCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <offer-id>",
		Short: "Abandon a share and delete its staged archives",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.CancelP2PShare(cmd.Context(), strings.TrimSpace(args[0])); err != nil {
				return err
			}

			fmt.Fprintf(s.env.Out, "Share %s cancelled.\n", args[0])

			return nil
		},
	}
}

// writeSessionOutcome reports how a transfer ended. The sender can never ask the
// recipient afterwards, so this line is the only confirmation they get — and "the
// recipient declined" must be distinguishable from a failure.
func (s *session) writeSessionOutcome(ctx context.Context, client p2pClient, offerID string) error {
	session, err := client.GetP2PShareSession(ctx, offerID)
	if err != nil {
		return err
	}

	if session == nil {
		return nil
	}

	fmt.Fprintf(s.env.Out, "Share %s: %s\n", session.OfferID, session.State)

	if session.FailReason != "" {
		fmt.Fprintf(s.env.Out, "%s\n", session.FailReason)
	}

	if session.State == domain.P2PShareStateRejected {
		return fmt.Errorf("the recipient declined the transfer: %s", session.FailReason)
	}

	if session.State != domain.P2PShareStateCompleted {
		return fmt.Errorf("the share ended as %s", session.State)
	}

	return nil
}

// p2pClient is the slice of the client the outcome reporting and the refusal
// path need, declared here to keep this file's dependency to three methods.
type p2pClient interface {
	GetP2PShareSession(ctx context.Context, offerID string) (*domain.P2PShareSession, error)
	RejectP2PShare(ctx context.Context, link string, reason string) (*domain.P2PAnswerLink, error)
	AwaitRejectedP2PShare(ctx context.Context, offerID string) error
}

// readAnswerLink reads the answer link a user pasted in: one token on one line, so
// the whole read is one trimmed line. An empty answer is reported as an error by
// the caller, the only place that knows what an empty offer means.
func readAnswerLink(in io.Reader) (string, error) {
	if in == nil {
		return "", fmt.Errorf("this command needs an input stream to read the answer link from")
	}

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	return strings.TrimSpace(line), nil
}
