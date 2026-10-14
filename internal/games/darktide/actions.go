package darktide

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// Action ids. They are code, not configuration: a game's actions are part of
// what this package means by "supporting Darktide", so they are named here and
// validated at registration like every other id. Nothing outside this package
// needs to know the strings — callers address an action by id and read its
// display name from the registry.
const (
	actionValidateLoadOrder = "validate_loadorder"
	actionOpenModsFolder    = "open_mods_folder"
	actionOpenWorkshop      = "open_workshop"
	actionLaunchGame        = "launch_game"
)

// Category names group actions in the UI and in the CLI's listing.
const (
	categoryValidation = "validation"
	categoryFolder     = "folder"
	categoryGame       = "game"
)

// steamURL builds a steam:// URL for a workshop item. Steam treats workshop
// items as their own namespace, so the game's app id is deliberately absent:
// addressing the workshop browser needs the item id and nothing else.
func steamURL(workshopID string) string {
	return fmt.Sprintf("steam://url/CommunityFilePage/%s", strings.TrimSpace(workshopID))
}

// launchCommand is the per-platform command that starts the game from an
// installation directory.
//
// Each platform gets a different command because that is a fact about the
// operating system, not about the action: the action stays one action, and the
// command table decides how it is carried out. Declaring the table as data
// means a new platform is a new row rather than a new branch in the handler.
var launchCommand = []domain.ActionCommand{
	{Path: "steam", Args: []string{"-applaunch", SteamAppID}},
	{
		Path:     filepath.Join("steamapps", "common", "Darktide", "Darktide.exe"),
		Args:     nil,
		Platform: domain.ActionPlatformWindows,
	},
	{
		Path:     "steam",
		Args:     []string{"steam://rungameid/" + SteamAppID},
		Platform: domain.ActionPlatformLinux,
	},
	{
		Path:     "open",
		Args:     []string{"steam://rungameid/" + SteamAppID},
		Platform: domain.ActionPlatformDarwin,
	},
}

// validateLoadOrder reads the game's load-order file and reports the problems a
// user can actually fix.
//
// The check is deliberately conservative: it reports what is definitely wrong
// (entries naming folders that do not exist, an entry repeated, an empty or
// unparsable line) and never rewrites the file. A load order is the user's
// decision, and a tool that "fixes" it silently removes control from them.
func validateLoadOrder(
	_ context.Context,
	actx domain.ActionContext,
) (*domain.GameActionResult, error) {
	if actx.ModsPath == "" {
		return nil, fmt.Errorf("the mods folder for %s has not been resolved yet", actx.GameID)
	}

	path := filepath.Join(actx.ModsPath, actx.Game.LoadOrderFile())

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &domain.GameActionResult{
				Success: true,
				Message: fmt.Sprintf("no %s in %s; the game loads mods in directory order",
					actx.Game.LoadOrderFile(), actx.ModsPath),
				Data: map[string]any{"path": path, "present": false},
			}, nil
		}

		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	entries, errs := readLoadOrder(file, actx.ModsPath)
	duplicates := duplicateEntries(entries)

	result := &domain.GameActionResult{
		Success: len(errs) == 0 && len(duplicates) == 0,
		Message: fmt.Sprintf("%s lists %d entries", path, len(entries)),
		Data: map[string]any{
			"path":      path,
			"present":   true,
			"entries":   entries,
			"errors":    errs,
			"duplicate": duplicates,
		},
	}

	switch {
	case len(errs) > 0:
		result.Message = fmt.Sprintf("%s has %d problem(s)", path, len(errs))
	case len(duplicates) > 0:
		result.Message = fmt.Sprintf("%s repeats %d entry/entries", path, len(duplicates))
	default:
		result.Message = fmt.Sprintf("%s looks correct", path)
	}

	return result, nil
}

// readLoadOrder parses the load-order file, returning the entries it named and
// a message for every line that cannot be honoured.
//
// Blank lines and comments are legal — a user annotating their own file should
// not be told off — but they are not entries.
func readLoadOrder(file *os.File, modsPath string) ([]string, []string) {
	var (
		entries []string
		errs    []string
		lineNo  int
	)

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for scanner.Scan() {
		lineNo++

		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}

		name := strings.TrimSuffix(raw, ".subsystem")
		if name == "" {
			errs = append(errs, fmt.Sprintf("line %d is empty after trimming", lineNo))

			continue
		}

		if strings.ContainsAny(name, `/\`) {
			errs = append(
				errs,
				fmt.Sprintf("line %d must name a mod folder, not the path %q", lineNo, raw),
			)

			continue
		}

		if _, err := os.Stat(filepath.Join(modsPath, name)); err != nil {
			errs = append(
				errs,
				fmt.Sprintf("line %d names %q, which is not in the mods folder", lineNo, name),
			)

			continue
		}

		entries = append(entries, name)
	}

	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("the file could not be read: %v", err))
	}

	return entries, errs
}

// duplicateEntries returns the entries that appear more than once, in the order
// they were first seen. A repeated entry is how a load order ends up silently
// loading the same mod twice, which is hard to notice and easy to fix.
func duplicateEntries(entries []string) []string {
	seen := make(map[string]struct{}, len(entries))
	dupes := make([]string, 0, len(entries))

	for _, entry := range entries {
		if _, ok := seen[entry]; ok {
			dupes = append(dupes, entry)

			continue
		}

		seen[entry] = struct{}{}
	}

	return dupes
}

// openModsFolder shows the mods folder in the desktop's file manager.
func openModsFolder(
	ctx context.Context,
	actx domain.ActionContext,
) (*domain.GameActionResult, error) {
	if actx.ModsPath == "" {
		return nil, fmt.Errorf("the mods folder for %s has not been resolved yet", actx.GameID)
	}

	if actx.Opener == nil {
		return nil, fmt.Errorf("no opener is available on this platform")
	}

	if err := actx.Opener.OpenDirectory(ctx, actx.ModsPath); err != nil {
		return nil, fmt.Errorf("open %s: %w", actx.ModsPath, err)
	}

	return &domain.GameActionResult{
		Success: true,
		Message: fmt.Sprintf("opened %s", actx.ModsPath),
		Data:    map[string]any{"path": actx.ModsPath},
	}, nil
}

// openWorkshop shows a mod's Steam Workshop page in the Steam client.
//
// Some sources — the workshop above all — cannot hand a manager a fresh download
// because the file lives inside the client. Opening the page is what lets a
// user update such a mod by hand, so the action takes the item id as an option
// rather than hard-coding a link.
func openWorkshop(
	ctx context.Context,
	actx domain.ActionContext,
) (*domain.GameActionResult, error) {
	if actx.Opener == nil {
		return nil, fmt.Errorf("no opener is available on this platform")
	}

	workshopID := actx.ArgString("workshop_id")
	if workshopID == "" {
		return nil, fmt.Errorf("workshop_id is required")
	}

	link := steamURL(workshopID)

	if err := actx.Opener.OpenURL(ctx, link); err != nil {
		return nil, fmt.Errorf("open %s: %w", link, err)
	}

	return &domain.GameActionResult{
		Success: true,
		Message: fmt.Sprintf("opened %s", link),
		Data:    map[string]any{"url": link, "workshop_id": workshopID},
	}, nil
}

// launchGame reports how the game is started.
//
// It does not start anything itself: doing so from a manager window would make
// the manager the parent of a game process, which is a decision the user should
// make in Steam where they can see it. What the action does return is the exact
// command for the platform, so the CLI can print it and the frontend can offer
// it as a copyable line — and so the platform table below is verifiable.
func launchGame(_ context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
	command, ok := domain.ActionScript{
		Commands: launchCommand,
	}.CommandFor(actx.Platform)
	if !ok {
		return &domain.GameActionResult{
			Success: false,
			Message: fmt.Sprintf("starting the game is not configured on %s", actx.Platform),
		}, nil
	}

	if actx.InstallPath != "" {
		actx.Logf("launch command: %s %s (installed at %s)",
			command.Path, strings.Join(command.Args, " "), actx.InstallPath)
	}

	return &domain.GameActionResult{
		Success: true,
		Message: "start " + actx.Game.DisplayName() + " from Steam to load mods",
		Data: map[string]any{
			"steam_app_id": actx.Game.SteamAppID(),
			"command":      command.Path,
			"args":         command.Args,
			"platform":     actx.Platform,
		},
	}, nil
}

// actions is the table every call to GameActions copies from.
//
// Building the slice per call keeps the definition stateless, as the rest of
// this package is, and keeps the handlers unexported: nothing outside the
// package can execute them, only this package can offer them.
func actions() []domain.GameAction {
	return []domain.GameAction{
		{
			ID:           domain.ActionID(ID + ":" + actionValidateLoadOrder),
			GameID:       ID,
			DisplayName:  "Validate Load Order",
			Description:  "Check mod_load_order.txt for missing, duplicated and malformed entries.",
			Category:     categoryValidation,
			RequiresGame: true,
			Platform:     domain.ActionPlatformUniversal,
			Handler:      validateLoadOrder,
		},
		{
			ID:           domain.ActionID(ID + ":" + actionOpenModsFolder),
			GameID:       ID,
			DisplayName:  "Open Mods Folder",
			Description:  "Show the mods folder in the file manager.",
			Category:     categoryFolder,
			Icon:         "folder",
			RequiresGame: true,
			Platform:     domain.ActionPlatformUniversal,
			Handler:      openModsFolder,
		},
		{
			ID:          domain.ActionID(ID + ":" + actionOpenWorkshop),
			GameID:      ID,
			DisplayName: "Open Workshop Page",
			Description: "Open a mod's Steam Workshop page, so it can be updated by hand.",
			Category:    categoryGame,
			Icon:        "steam",
			Platform:    domain.ActionPlatformUniversal,
			Options: []domain.ActionOption{
				{
					Name:        "workshop_id",
					Type:        domain.ActionOptionString,
					Label:       "Workshop item id",
					Description: "The numeric id at the end of the mod's workshop URL.",
					Required:    true,
				},
			},
			Handler: openWorkshop,
		},
		{
			ID:           domain.ActionID(ID + ":" + actionLaunchGame),
			GameID:       ID,
			DisplayName:  "Launch Game",
			Description:  "Show the command that starts the game through Steam, which is how mods get loaded.",
			Category:     categoryGame,
			Icon:         "play",
			RequiresGame: true,
			// Universal, because which command applies is a fact the handler
			// decides from the platform table above — the action itself is the
			// same everywhere, and hiding it would hide the answer too.
			Platform: domain.ActionPlatformUniversal,
			Handler:  launchGame,
		},
	}
}
