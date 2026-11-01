package darktide

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Action ids. They are code, not configuration: a game's actions are part of
// what this package means by "supporting Darktide", and callers address one by
// id and read its display name from the registry.
const (
	actionValidateLoadOrder = "validate_loadorder"
	actionOpenModsFolder    = "open_mods_folder"
	actionOpenWorkshop      = "open_workshop"
	actionLaunchGame        = "launch_game"
	actionSetModLoading     = "set_mod_loading"
	actionInstallPatcher    = "install_patcher"
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

// launchCommandFor picks the command that matches how the game was installed.
// Steam's URI works for every Steam layout; the Flatpak and Snap wrappers need
// their own binary because the Steam client is not on PATH there.
func launchCommandFor(actx domain.ActionContext) (domain.ActionCommand, bool) {
	install := strings.ToLower(actx.InstallPath)

	switch actx.Platform {
	case domain.PlatformLinux:
		switch {
		case strings.Contains(install, "/.var/app/com.valvesoftware.steam/"):
			return domain.ActionCommand{
				Path: "flatpak",
				Args: []string{
					"run", "com.valvesoftware.Steam", "steam://rungameid/" + SteamAppID,
				},
			}, true
		case strings.Contains(install, "/snap/steam/"):
			return domain.ActionCommand{
				Path: "snap",
				Args: []string{"run", "steam", "steam://rungameid/" + SteamAppID},
			}, true
		default:
			return domain.ActionCommand{
				Path: "steam",
				Args: []string{"steam://rungameid/" + SteamAppID},
			}, true
		}
	case domain.PlatformWindows:
		// The Microsoft Store build is not Steam's child; start its executable.
		if strings.Contains(install, "windowsapps") || strings.Contains(install, "xboxgames") {
			return domain.ActionCommand{
				Path: filepath.Join(actx.InstallPath, BinaryName),
			}, true
		}

		return domain.ActionCommand{
			Path: "rundll32",
			Args: []string{"url.dll,FileProtocolHandler", "steam://rungameid/" + SteamAppID},
		}, true
	case domain.PlatformDarwin:
		return domain.ActionCommand{
			Path: "open",
			Args: []string{"steam://rungameid/" + SteamAppID},
		}, true
	}

	return domain.ActionCommand{}, false
}

// validateLoadOrder reports the load-order problems a user can fix.
//
// It never rewrites the file: a load order is the user's decision.
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
				Success:    true,
				Message:    fmt.Sprintf("no %s in %s; the game loads mods in directory order", actx.Game.LoadOrderFile(), actx.ModsPath),
				MessageKey: "actions.darktide.validate.missing",
				MessageArgs: map[string]any{
					"file": actx.Game.LoadOrderFile(),
					"path": actx.ModsPath,
				},
				Data: map[string]any{"path": path, "present": false},
			}, nil
		}

		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	entries, errs := readLoadOrder(file, actx.ModsPath)
	duplicates := duplicateEntries(entries)

	result := &domain.GameActionResult{
		Success:     len(errs) == 0 && len(duplicates) == 0,
		Message:     fmt.Sprintf("%s lists %d entries", path, len(entries)),
		MessageKey:  "actions.darktide.validate.entries",
		MessageArgs: map[string]any{"path": path, "count": len(entries)},
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
		result.MessageKey = "actions.darktide.validate.problems"
		result.MessageArgs = map[string]any{"path": path, "count": len(errs)}
	case len(duplicates) > 0:
		result.Message = fmt.Sprintf("%s repeats %d entry/entries", path, len(duplicates))
		result.MessageKey = "actions.darktide.validate.repeats"
		result.MessageArgs = map[string]any{"path": path, "count": len(duplicates)}
	default:
		result.Message = fmt.Sprintf("%s looks correct", path)
		result.MessageKey = "actions.darktide.validate.correct"
		result.MessageArgs = map[string]any{"path": path}
	}

	return result, nil
}

// readLoadOrder parses the load-order file, returning the entries it named and
// a message for every line that cannot be honoured. Blank lines and comments are
// legal — a user annotating their own file should not be told off — but they are
// not entries.
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
// they were first seen. A repeat is how a load order silently loads one mod
// twice, which is hard to notice and easy to fix.
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
		Success:     true,
		Message:     fmt.Sprintf("opened %s", actx.ModsPath),
		MessageKey:  "actions.darktide.openModsFolder.opened",
		MessageArgs: map[string]any{"path": actx.ModsPath},
		Data:        map[string]any{"path": actx.ModsPath},
	}, nil
}

// openWorkshop shows a mod's Steam Workshop page in the Steam client.
//
// The file lives inside the client, so such a mod can only be updated by hand.
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
		Success:     true,
		Message:     fmt.Sprintf("opened %s", link),
		MessageKey:  "actions.darktide.openWorkshop.opened",
		MessageArgs: map[string]any{"url": link},
		Data:        map[string]any{"url": link, "workshop_id": workshopID},
	}, nil
}

// launchGame starts the game the way it was installed. The process is detached,
// so closing the manager does not close the game.
func launchGame(ctx context.Context, actx domain.ActionContext) (*domain.GameActionResult, error) {
	command, ok := launchCommandFor(actx)
	if !ok {
		return &domain.GameActionResult{
			Success:     false,
			Message:     fmt.Sprintf("starting the game is not configured on %s", actx.Platform),
			MessageKey:  "actions.darktide.launchGame.notConfigured",
			MessageArgs: map[string]any{"platform": actx.Platform},
		}, nil
	}

	if !actx.CommandsAllowed {
		return &domain.GameActionResult{
			Success:     false,
			Message:     fmt.Sprintf("%s needs permission to execute commands", actx.Action),
			MessageKey:  "actions.commandsDisabled",
			MessageArgs: map[string]any{"action": string(actx.Action)},
		}, nil
	}

	if actx.StartCommand == nil {
		return nil, fmt.Errorf("no command runner is available")
	}

	if err := actx.StartCommand(ctx, command); err != nil {
		return nil, fmt.Errorf("start %s: %w", command.Path, err)
	}

	commandLine := strings.TrimSpace(command.Path + " " + strings.Join(command.Args, " "))
	details := []domain.ActionDetail{{
		LabelKey: "actions.darktide.launchGame.command",
		Label:    "Launch command",
		Value:    commandLine,
	}}

	if actx.InstallPath != "" {
		details = append(details, domain.ActionDetail{
			LabelKey: "actions.darktide.launchGame.installedAt",
			Label:    "Installed at",
			Value:    actx.InstallPath,
		})
	}

	return &domain.GameActionResult{
		Success:     true,
		Message:     "started " + actx.Game.DisplayName(),
		MessageKey:  "actions.darktide.launchGame.start",
		MessageArgs: map[string]any{"game": actx.Game.DisplayName()},
		Details:     details,
		Data: map[string]any{
			"steam_app_id": actx.Game.SteamAppID(),
			"command":      command.Path,
			"args":         command.Args,
			"platform":     actx.Platform,
			"started":      true,
		},
	}, nil
}

// dtkitPatchCommand finds the patcher that matches how the game was set up.
// The native Linux build lives beside the Windows one in tools/; old DML
// installs only have the .exe, which needs Wine.
func dtkitPatchCommand(
	actx domain.ActionContext,
	flag string,
	lookPath func(string) (string, error),
) (domain.ActionCommand, bool) {
	if native, ok := patcherPath(actx.InstallPath, "dtkit-patch"); ok {
		return domain.ActionCommand{
			Path: native,
			Args: []string{flag, "bundle"},
		}, true
	}

	exe, ok := patcherPath(actx.InstallPath, "dtkit-patch.exe")
	if !ok {
		return domain.ActionCommand{}, false
	}

	for _, candidate := range []string{"wine", "wine64"} {
		wine, err := lookPath(candidate)
		if err != nil {
			continue
		}

		return domain.ActionCommand{
			Path: wine,
			Args: []string{exe, flag, "bundle"},
		}, true
	}

	return domain.ActionCommand{}, false
}

// patcherPath looks in the tools folder first, then the game root.
func patcherPath(installPath, name string) (string, bool) {
	for _, dir := range []string{filepath.Join(installPath, "tools"), installPath} {
		path := filepath.Join(dir, name)
		if isFile(path) {
			return path, true
		}
	}

	return "", false
}

// installedPatcher reports a patcher the game folder already holds.
func installedPatcher(installPath string) (string, bool) {
	for _, name := range []string{"dtkit-patch", "dtkit-patch.exe"} {
		if path, ok := patcherPath(installPath, name); ok {
			return path, true
		}
	}

	return "", false
}

func isFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && !info.IsDir()
}

// setModLoading patches or unpatches the bundle database so the game loads
// mods. dtkit-patch prints what it did, so its output is kept for the result.
func setModLoading(
	ctx context.Context,
	actx domain.ActionContext,
) (*domain.GameActionResult, error) {
	if !actx.CommandsAllowed {
		return &domain.GameActionResult{
			Success:     false,
			Message:     fmt.Sprintf("%s needs permission to execute commands", actx.Action),
			MessageKey:  "actions.commandsDisabled",
			MessageArgs: map[string]any{"action": string(actx.Action)},
		}, nil
	}

	if actx.RunCommand == nil {
		return nil, fmt.Errorf("no command runner is available")
	}

	mode := strings.ToLower(strings.TrimSpace(actx.ArgString("mode")))
	if mode == "" {
		mode = "enable"
	}

	flag := "--patch"
	if mode == "disable" {
		flag = "--unpatch"
	}

	command, ok := dtkitPatchCommand(actx, flag, exec.LookPath)
	if !ok {
		return &domain.GameActionResult{
			Success:     false,
			Message:     "dtkit-patch was not found in the game folder",
			MessageKey:  "actions.darktide.setModLoading.missingTool",
			MessageArgs: map[string]any{"path": filepath.Join(actx.InstallPath, "tools")},
		}, nil
	}

	output, runErr := actx.RunCommand(ctx, command)

	commandLine := strings.TrimSpace(command.Path + " " + strings.Join(command.Args, " "))
	details := []domain.ActionDetail{{
		LabelKey: "actions.darktide.setModLoading.command",
		Label:    "Patch command",
		Value:    commandLine,
	}}

	data := map[string]any{
		"mode":      mode,
		"command":   command.Path,
		"args":      command.Args,
		"exit_code": output.ExitCode,
	}

	if runErr != nil {
		return &domain.GameActionResult{
			Success:     false,
			Message:     fmt.Sprintf("dtkit-patch failed to %s mod loading", mode),
			MessageKey:  "actions.darktide.setModLoading.failed",
			MessageArgs: map[string]any{"mode": mode},
			Details:     details,
			Output:      strings.TrimSpace(output.Output),
			Data:        data,
		}, nil
	}

	messageKey := "actions.darktide.setModLoading.enabled"
	message := "mod loading enabled"

	if flag == "--unpatch" {
		messageKey = "actions.darktide.setModLoading.disabled"
		message = "mod loading disabled"
	}

	return &domain.GameActionResult{
		Success:    true,
		Message:    message,
		MessageKey: messageKey,
		Details:    details,
		Output:     strings.TrimSpace(output.Output),
		Data:       data,
	}, nil
}

// installPatcher puts the newest dtkit-patch release in the game's tools
// folder. An existing patcher is reported and left alone, so a Wine-only setup
// is not overwritten by accident.
func installPatcher(
	ctx context.Context,
	actx domain.ActionContext,
) (*domain.GameActionResult, error) {
	if path, ok := installedPatcher(actx.InstallPath); ok {
		return &domain.GameActionResult{
			Success:     true,
			Message:     fmt.Sprintf("dtkit-patch is already installed at %s", path),
			MessageKey:  "actions.darktide.installPatcher.alreadyInstalled",
			MessageArgs: map[string]any{"path": path},
			Details: []domain.ActionDetail{{
				LabelKey: "actions.darktide.installPatcher.path",
				Label:    "Patcher path",
				Value:    path,
			}},
			Data: map[string]any{"path": path, "installed": false},
		}, nil
	}

	installer := newPatcherInstaller(actx.Platform)

	release, err := installer.latest(ctx)
	if errors.Is(err, errNoPatcherAsset) {
		return &domain.GameActionResult{
			Success:     false,
			Message:     fmt.Sprintf("dtkit-patch has no release for %s/%s", actx.Platform, installer.arch),
			MessageKey:  "actions.darktide.installPatcher.noAsset",
			MessageArgs: map[string]any{"platform": string(actx.Platform), "arch": installer.arch},
		}, nil
	}

	if err != nil {
		return installPatcherFailure(ctx, err)
	}

	path, err := installer.install(ctx, release, filepath.Join(actx.InstallPath, "tools"))
	if err != nil {
		return installPatcherFailure(ctx, err)
	}

	return &domain.GameActionResult{
		Success:     true,
		Message:     fmt.Sprintf("dtkit-patch %s installed to %s", release.Version, path),
		MessageKey:  "actions.darktide.installPatcher.installed",
		MessageArgs: map[string]any{"version": release.Version, "path": path},
		Details: []domain.ActionDetail{
			{
				LabelKey: "actions.darktide.installPatcher.version",
				Label:    "Version",
				Value:    release.Version,
			},
			{
				LabelKey: "actions.darktide.installPatcher.path",
				Label:    "Patcher path",
				Value:    path,
			},
		},
		Data: map[string]any{
			"version":   release.Version,
			"asset":     release.Asset,
			"path":      path,
			"installed": true,
		},
	}, nil
}

// installPatcherFailure keeps a cancelled run reported as cancelled rather than
// as a failed download.
func installPatcherFailure(
	ctx context.Context,
	err error,
) (*domain.GameActionResult, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, appErrors.Wrap(ctxErr, appErrors.ErrCodeOperationCanceled,
			"installing dtkit-patch was cancelled")
	}

	return &domain.GameActionResult{
		Success:     false,
		Message:     fmt.Sprintf("could not install dtkit-patch: %v", err),
		MessageKey:  "actions.darktide.installPatcher.failed",
		MessageArgs: map[string]any{"reason": err.Error()},
	}, nil
}

// actions is the table every call to GameActions copies from.
//
// Building the slice per call keeps the definition stateless and keeps the
// handlers unexported: nothing outside this package can execute them.
func actions() []domain.GameAction {
	return []domain.GameAction{
		{
			ID:             domain.ActionID(ID + ":" + actionValidateLoadOrder),
			GameID:         ID,
			DisplayName:    "Validate Load Order",
			DisplayNameKey: "actions.darktide.validate.title",
			Description:    "Check mod_load_order.txt for missing, duplicated and malformed entries.",
			DescriptionKey: "actions.darktide.validate.description",
			Category:       categoryValidation,
			CategoryKey:    "actions.darktide.category.validation",
			RequiresGame:   true,
			Platform:       domain.ActionPlatformUniversal,
			Handler:        validateLoadOrder,
		},
		{
			ID:             domain.ActionID(ID + ":" + actionOpenModsFolder),
			GameID:         ID,
			DisplayName:    "Open Mods Folder",
			DisplayNameKey: "actions.darktide.openModsFolder.title",
			Description:    "Show the mods folder in the file manager.",
			DescriptionKey: "actions.darktide.openModsFolder.description",
			Category:       categoryFolder,
			CategoryKey:    "actions.darktide.category.folder",
			Icon:           "folder",
			RequiresGame:   true,
			Platform:       domain.ActionPlatformUniversal,
			Handler:        openModsFolder,
		},
		{
			ID:             domain.ActionID(ID + ":" + actionOpenWorkshop),
			GameID:         ID,
			DisplayName:    "Open Workshop Page",
			DisplayNameKey: "actions.darktide.openWorkshop.title",
			Description:    "Open a mod's Steam Workshop page, so it can be updated by hand.",
			DescriptionKey: "actions.darktide.openWorkshop.description",
			Category:       categoryGame,
			CategoryKey:    "actions.darktide.category.game",
			Icon:           "steam",
			Platform:       domain.ActionPlatformUniversal,
			Options: []domain.ActionOption{
				{
					Name:           "workshop_id",
					Type:           domain.ActionOptionString,
					Label:          "Workshop item id",
					LabelKey:       "actions.darktide.openWorkshop.workshopID.label",
					Description:    "The numeric id at the end of the mod's workshop URL.",
					DescriptionKey: "actions.darktide.openWorkshop.workshopID.description",
					Required:       true,
				},
			},
			Handler: openWorkshop,
		},
		{
			ID:             domain.ActionID(ID + ":" + actionLaunchGame),
			GameID:         ID,
			DisplayName:    "Launch Game",
			DisplayNameKey: "actions.darktide.launchGame.title",
			Description:    "Start the game through Steam, which is how mods get loaded.",
			DescriptionKey: "actions.darktide.launchGame.description",
			Category:       categoryGame,
			CategoryKey:    "actions.darktide.category.game",
			Icon:           "play",
			RequiresGame:   true,
			// Universal, because launchGame decides the command from the detected
			// installation; hiding the action would hide the answer too.
			Platform: domain.ActionPlatformUniversal,
			Handler:  launchGame,
		},
		{
			ID:             domain.ActionID(ID + ":" + actionSetModLoading),
			GameID:         ID,
			DisplayName:    "Set Mod Loading",
			DisplayNameKey: "actions.darktide.setModLoading.title",
			Description:    "Enable or disable the game's mod-loading patch with dtkit-patch.",
			DescriptionKey: "actions.darktide.setModLoading.description",
			Category:       categoryGame,
			CategoryKey:    "actions.darktide.category.game",
			Icon:           "wrench",
			RequiresGame:   true,
			Platform:       domain.ActionPlatformLinux,
			Options: []domain.ActionOption{
				{
					Name:           "mode",
					Type:           domain.ActionOptionSelect,
					Choices:        []string{"enable", "disable"},
					Default:        "enable",
					Label:          "Mod loading",
					LabelKey:       "actions.darktide.setModLoading.mode.label",
					Description:    "Enable patches the game; disable restores the vanilla bundle.",
					DescriptionKey: "actions.darktide.setModLoading.mode.description",
				},
			},
			Handler: setModLoading,
		},
		{
			ID:             domain.ActionID(ID + ":" + actionInstallPatcher),
			GameID:         ID,
			DisplayName:    "Install dtkit-patch",
			DisplayNameKey: "actions.darktide.installPatcher.title",
			Description:    "Download the latest dtkit-patch release for this platform into the game's tools folder.",
			DescriptionKey: "actions.darktide.installPatcher.description",
			Category:       categoryGame,
			CategoryKey:    "actions.darktide.category.game",
			Icon:           "download",
			RequiresGame:   true,
			Platform:       domain.ActionPlatformUniversal,
			Handler:        installPatcher,
		},
	}
}
