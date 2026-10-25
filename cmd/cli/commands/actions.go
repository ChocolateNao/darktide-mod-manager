package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/spf13/cobra"
)

// newActionsCommand builds the group that lists and runs the game-specific
// actions. Both the CLI and the frontend render what the manager reports, so a
// new game ships actions without a client release.
func newActionsCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "actions",
		Short: "List and run the actions a game publishes",
		Long: "Actions are small, game-specific operations such as validating a load\n" +
			"order or opening the mods folder. Their ids are defined in code by the\n" +
			"game, not in configuration, so a listing here is exactly what this build\n" +
			"can run.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newActionsListCommand(s),
		newActionsRunCommand(s),
	)

	return group
}

type actionView struct {
	ID           string             `json:"id"`
	GameID       string             `json:"game_id"`
	DisplayName  string             `json:"display_name"`
	Description  string             `json:"description,omitempty"`
	Category     string             `json:"category,omitempty"`
	Icon         string             `json:"icon,omitempty"`
	Platform     string             `json:"platform"`
	Options      []actionOptionView `json:"options,omitempty"`
	RequiresGame bool               `json:"requires_game"`
	Dangerous    bool               `json:"dangerous"`
	Available    bool               `json:"available"`
}

type actionOptionView struct {
	Default     any      `json:"default,omitempty"`
	Name        string   `json:"name"`
	Flag        string   `json:"flag"`
	Type        string   `json:"type"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Choices     []string `json:"choices,omitempty"`
	Required    bool     `json:"required"`
}

func newActionsListCommand(s *session) *cobra.Command {
	var (
		gameFlag     string
		allFlag      bool
		platformFlag string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the actions a game publishes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			// Two calls: which actions exist, and which could run here; listing
			// Windows-only actions on Linux is useless.
			gameID := strings.TrimSpace(gameFlag)

			list, err := client.ListGameActions(gameID)
			if err != nil {
				return err
			}

			available, err := client.ListAvailableGameActions(
				gameID,
				domain.Platform(strings.TrimSpace(platformFlag)),
			)
			if err != nil {
				return err
			}

			runnable := make(map[domain.ActionID]bool, len(available))
			for _, action := range available {
				runnable[action.ID] = true
			}

			views := make([]actionView, 0, len(list))

			for _, action := range list {
				if allFlag && !runnable[action.ID] {
					continue
				}

				views = append(views, viewOfAction(action, runnable[action.ID]))
			}

			table := NewTable("ID", "Name", "Game", "Platform", "Runs here", "Options")
			for _, view := range views {
				table.Add(
					view.ID,
					view.DisplayName,
					view.GameID,
					view.Platform,
					yesNo(view.Available),
					optionNames(view.Options),
				)
			}

			return s.writeJSONOrTable(s.env.Out, views, table, "No actions available.")
		},
	}

	cmd.Flags().StringVar(&gameFlag, "game", "", "filter by game id (defaults to every game)")
	cmd.Flags().BoolVar(&allFlag, "available-only", false,
		"only list the actions that can run on this platform")
	cmd.Flags().StringVar(&platformFlag, "platform", "",
		"pretend to be another platform when filtering")

	return cmd
}

func viewOfAction(action domain.GameAction, available bool) actionView {
	entry := actionView{
		ID:           action.ID.String(),
		GameID:       action.GameID,
		DisplayName:  action.DisplayName,
		Description:  action.Description,
		Category:     action.Category,
		Icon:         action.Icon,
		Platform:     string(action.Platform.Normalize()),
		RequiresGame: action.RequiresGame,
		Dangerous:    action.Dangerous,
		Options:      make([]actionOptionView, 0, len(action.Options)),
		Available:    available,
	}

	for _, option := range action.Options {
		entry.Options = append(entry.Options, actionOptionView{
			Name:        option.Name,
			Flag:        domain.FormatActionArg(option.Name),
			Type:        string(option.Type),
			Label:       option.Label,
			Description: option.Description,
			Required:    option.Required,
			Default:     option.Default,
			Choices:     option.Choices,
		})
	}

	return entry
}

func optionNames(options []actionOptionView) string {
	names := make([]string, 0, len(options))
	for _, option := range options {
		if option.Required {
			names = append(names, option.Flag+"*")

			continue
		}

		names = append(names, option.Flag)
	}

	return strings.Join(names, ",")
}

func newActionsRunCommand(s *session) *cobra.Command {
	var setFlag []string

	cmd := &cobra.Command{
		Use:   "run <action-id>",
		Short: "Run one action",
		Long: "Runs a single action. Options are supplied with --set, one\n" +
			"--set name=value per option:\n\n" +
			"  dmm actions run darktide:open_workshop --set workshop_id=123456\n\n" +
			"Values are converted to the type the action declared, so a checkbox\n" +
			"takes yes/no and a count takes a number. An option the action does not\n" +
			"declare is refused.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			actionID := domain.ActionID(strings.TrimSpace(args[0]))

			action, err := client.GetGameAction(actionID)
			if err != nil {
				return err
			}

			values, err := parseActionArgs(setFlag)
			if err != nil {
				return err
			}

			// Refusing an unknown key turns a `--set` typo into an error.
			if err := checkActionArgs(action, values); err != nil {
				return err
			}

			result, err := client.ExecuteGameAction(ctx, actionID, values)
			if err != nil {
				return err
			}

			return s.writeActionResult(result)
		},
	}

	cmd.Flags().StringArrayVar(&setFlag, "set", nil,
		"an action option as name=value; repeat for more than one")

	return cmd
}

// writeActionResult prints what the action produced. The message goes out before
// the JSON so `--json` still shows the tool's own words.
func (s *session) writeActionResult(result *domain.GameActionResult) error {
	if result == nil {
		return nil
	}

	if result.Output != "" {
		fmt.Fprintln(s.env.Out, strings.TrimRight(result.Output, "\n"))
	}

	if result.Message != "" {
		fmt.Fprintln(s.env.Out, result.Message)
	}

	if s.opts.asJSON {
		return writeJSON(s.env.Out, result)
	}

	if !result.Success {
		return fmt.Errorf("the action reported that it did not succeed")
	}

	return nil
}

// parseActionArgs turns repeated --set flags into a map. Everything arrives as a
// string because that is all a shell can express; the action layer converts it.
func parseActionArgs(pairs []string) (map[string]any, error) {
	values := make(map[string]any, len(pairs))

	for _, pair := range pairs {
		name, raw, found := strings.Cut(pair, "=")
		if !found {
			return nil, fmt.Errorf("option %q is not in name=value form", pair)
		}

		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("option %q has no name", pair)
		}

		values[name] = raw
	}

	return values, nil
}

func checkActionArgs(action *domain.GameAction, values map[string]any) error {
	if action == nil {
		return nil
	}

	declared := make(map[string]bool, len(action.Options))
	for _, option := range action.Options {
		declared[option.Name] = true
		declared[domain.FormatActionArg(option.Name)] = true
	}

	unknown := make([]string, 0, len(values))

	for name := range values {
		if !declared[name] {
			unknown = append(unknown, name)
		}
	}

	if len(unknown) == 0 {
		return nil
	}

	sort.Strings(unknown)

	return fmt.Errorf(
		"action %s does not declare %s; it accepts: %s",
		action.ID,
		plural(len(unknown), "option", "options"),
		strings.Join(strings.FieldsFunc(optionNames(viewOfAction(*action, true).Options),
			func(r rune) bool { return r == ',' }), ", "),
	)
}
