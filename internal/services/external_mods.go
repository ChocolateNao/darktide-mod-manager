package services

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// externalModPrefix names a folder the manager found but does not own. The
// prefix keeps an external folder from colliding with a real mod id.
const externalModPrefix = "external:"

func externalModID(folder string) string { return externalModPrefix + folder }

// externalFolder extracts the folder from an external id, rejecting anything
// that could escape the mods folder.
func externalFolder(id string) (string, bool) {
	if !strings.HasPrefix(id, externalModPrefix) {
		return "", false
	}

	folder := strings.TrimPrefix(id, externalModPrefix)
	if !validModFolderName(folder) {
		return "", false
	}

	return folder, true
}

func validModFolderName(folder string) bool {
	if folder == "" || folder == "." || folder == ".." {
		return false
	}

	return !strings.ContainsAny(folder, `/\`) && filepath.Base(folder) == folder
}

// loadOrderName returns the active game's load order file, and whether the game
// has one at all. A game without one keeps the legacy deploy/undeploy toggle.
func (s *ModService) loadOrderName() (string, bool) {
	if s.deps.Locator == nil {
		return "", false
	}

	def, _, err := s.deps.Locator.Active()
	if err != nil || def == nil {
		return "", false
	}

	name := strings.TrimSpace(def.LoadOrderFile())

	return name, name != ""
}

// readLoadOrder reads a load order file, tolerating its absence. Blank lines and
// repeats are dropped: the game only cares about the first mention.
func readLoadOrder(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}

	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read load order file %q", path)
	}

	var lines []string

	seen := map[string]bool{}

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || seen[line] {
			continue
		}

		seen[line] = true
		lines = append(lines, line)
	}

	return lines, nil
}

// scanGameFolder compares the mods folder and the load order file with the
// manager's records. Known mods follow the file, unknown folders become
// external mods, and a game without a load order file is left alone.
func (s *ModService) scanGameFolder(
	ctx context.Context,
	gameID string,
	known []domain.Mod,
) ([]domain.Mod, error) {
	loadOrderFile, ok := s.loadOrderName()
	if !ok {
		return known, nil
	}

	modsFolder, err := s.modsPath(ctx)
	if err != nil {
		return known, nil
	}

	order, err := readLoadOrder(filepath.Join(modsFolder, loadOrderFile))
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(modsFolder)
	if err != nil {
		if os.IsNotExist(err) {
			return known, nil
		}

		return nil, appErrors.Wrap(err, appErrors.ErrCodeModsPathNotDir,
			"read the mods folder %q", modsFolder)
	}

	folders := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if platform.IsDir(platform.RealFS(), filepath.Join(modsFolder, entry.Name())) {
			folders[entry.Name()] = true
		}
	}

	position := make(map[string]int, len(order))
	for i, folder := range order {
		position[folder] = i
	}

	managed := make(map[string]bool, len(known))
	dropped := make(map[string]bool)

	for i := range known {
		mod := &known[i]
		if mod.External || mod.InstallMode == domain.InstallModeGameRoot || mod.ModFolderName == "" {
			continue
		}

		managed[mod.ModFolderName] = true

		enabled := folders[mod.ModFolderName] && hasFolder(position, mod.ModFolderName)
		if mod.Enabled == enabled {
			continue
		}

		// UpdateEnabled, not Save: a list call can overlap an uninstall, and an
		// upsert here would write the deleted record back as a disabled row.
		if err := s.deps.Index.UpdateEnabled(ctx, mod.ID, enabled); err != nil {
			if appErrors.IsAppError(err, appErrors.ErrCodeModNotFound) {
				dropped[mod.ID] = true

				continue
			}

			return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO,
				"reconcile mod %q with the game folder", modLabel(mod))
		}

		mod.Enabled = enabled
		mod.UpdatedAt = s.now()
	}

	out := make([]domain.Mod, 0, len(known))

	for _, mod := range known {
		if !dropped[mod.ID] {
			out = append(out, mod)
		}
	}

	for folder := range folders {
		if managed[folder] {
			continue
		}

		mod := domain.Mod{
			ID:            externalModID(folder),
			Name:          folder,
			GameID:        gameID,
			ModFolderName: folder,
			LocalPath:     filepath.Join(modsFolder, folder),
			Issues:        manifestIssues(filepath.Join(modsFolder, folder)),
			InstallMode:   domain.InstallModeModsFolder,
			External:      true,
		}

		if pos, listed := position[folder]; listed {
			mod.Enabled = true
			mod.Priority = len(order) - pos
		}

		out = append(out, mod)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Enabled != out[j].Enabled {
			return out[i].Enabled
		}

		return out[i].ID < out[j].ID
	})

	return out, nil
}

func hasFolder(position map[string]int, folder string) bool {
	_, ok := position[folder]

	return ok
}

// loadOrderFromFile translates folder names into mod ids. Unknown folders keep
// the external prefix so the frontend can resolve them to a row.
func (s *ModService) loadOrderFromFile(ctx context.Context, gameID string) ([]string, error) {
	loadOrderFile, ok := s.loadOrderName()
	if !ok {
		return s.deps.Index.LoadOrderByGame(ctx, gameID)
	}

	modsFolder, err := s.modsPath(ctx)
	if err != nil {
		return s.deps.Index.LoadOrderByGame(ctx, gameID)
	}

	order, err := readLoadOrder(filepath.Join(modsFolder, loadOrderFile))
	if err != nil {
		return nil, err
	}

	known, err := s.deps.Index.ListByGame(ctx, gameID)
	if err != nil {
		return nil, err
	}

	byFolder := make(map[string]string, len(known))
	for _, mod := range known {
		if mod.ModFolderName != "" {
			byFolder[mod.ModFolderName] = mod.ID
		}
	}

	out := make([]string, 0, len(order))
	for _, folder := range order {
		if id, found := byFolder[folder]; found {
			out = append(out, id)
		} else {
			out = append(out, externalModID(folder))
		}
	}

	return out, nil
}

// reorderFile writes the given ids to the load order file. Disabled mods keep
// their position in the request but are left out of the file, so a drag never
// silently enables one. External folders are written by name.
func (s *ModService) reorderFile(ctx context.Context, gameID string, ids []string) error {
	loadOrderFile, ok := s.loadOrderName()
	if !ok {
		return nil
	}

	modsFolder, err := s.modsPath(ctx)
	if err != nil {
		return err
	}

	known, err := s.deps.Index.ListByGame(ctx, gameID)
	if err != nil {
		return err
	}

	byID := make(map[string]domain.Mod, len(known))
	for _, mod := range known {
		byID[mod.ID] = mod
	}

	existing, err := readLoadOrder(filepath.Join(modsFolder, loadOrderFile))
	if err != nil {
		return err
	}

	listed := make(map[string]bool, len(existing))
	for _, folder := range existing {
		listed[folder] = true
	}

	var lines []string

	seen := map[string]bool{}
	managed := make([]string, 0, len(ids))

	for _, id := range ids {
		if mod, found := byID[id]; found {
			managed = append(managed, id)

			if !mod.Enabled || mod.InstallMode == domain.InstallModeGameRoot || mod.ModFolderName == "" {
				continue
			}

			if !seen[mod.ModFolderName] {
				seen[mod.ModFolderName] = true
				lines = append(lines, mod.ModFolderName)
			}

			continue
		}

		folder, external := externalFolder(id)
		if !external {
			return appErrors.New(appErrors.ErrCodeModNotFound,
				"load order names mod %q, which is not installed for game %q", id, gameID)
		}

		// Only an enabled external folder is in the file, so a drag that carries
		// a disabled or vanished one must still write the rest.
		if !listed[folder] || seen[folder] ||
			!platform.IsDir(platform.RealFS(), filepath.Join(modsFolder, folder)) {
			continue
		}

		seen[folder] = true
		lines = append(lines, folder)
	}

	if err := s.deps.Index.SetLoadOrderForGame(ctx, gameID, managed); err != nil {
		return err
	}

	return writeLoadOrder(filepath.Join(modsFolder, loadOrderFile), lines)
}

func writeLoadOrder(path string, lines []string) error {
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}

	return platform.AtomicWriteFile(path, []byte(content), 0o644)
}

// syncFileFromFolder rewrites the load order file from the game folder and the
// index, preserving the position of external folders the manager did not create.
func (s *ModService) syncFileFromFolder(ctx context.Context, gameID, loadOrderFile string) error {
	modsFolder, err := s.modsPath(ctx)
	if err != nil {
		return nil
	}

	known, err := s.deps.Index.ListByGame(ctx, gameID)
	if err != nil {
		return err
	}

	byFolder := make(map[string]domain.Mod, len(known))
	for _, mod := range known {
		if mod.InstallMode != domain.InstallModeGameRoot && mod.ModFolderName != "" {
			byFolder[mod.ModFolderName] = mod
		}
	}

	existing, err := readLoadOrder(filepath.Join(modsFolder, loadOrderFile))
	if err != nil {
		return err
	}

	var lines []string

	seen := map[string]bool{}

	for _, folder := range existing {
		if seen[folder] {
			continue
		}

		if mod, managed := byFolder[folder]; managed {
			if mod.Enabled {
				seen[folder] = true
				lines = append(lines, folder)
			}

			continue
		}

		if platform.IsDir(platform.RealFS(), filepath.Join(modsFolder, folder)) {
			seen[folder] = true
			lines = append(lines, folder)
		}
	}

	enabled, err := s.deps.Index.ListEnabledByGame(ctx, gameID)
	if err != nil {
		return err
	}

	sort.SliceStable(enabled, func(i, j int) bool {
		if enabled[i].Priority != enabled[j].Priority {
			return enabled[i].Priority > enabled[j].Priority
		}

		return enabled[i].ID < enabled[j].ID
	})

	for _, mod := range enabled {
		if mod.InstallMode == domain.InstallModeGameRoot || mod.ModFolderName == "" || seen[mod.ModFolderName] {
			continue
		}

		seen[mod.ModFolderName] = true
		lines = append(lines, mod.ModFolderName)
	}

	return writeLoadOrder(filepath.Join(modsFolder, loadOrderFile), lines)
}

// uninstallExternalLocked deletes a folder the manager did not install. It
// refuses a folder that belongs to a managed mod, so the two delete paths cannot
// disagree about who owns a mod.
func (s *ModService) uninstallExternalLocked(
	ctx context.Context,
	folder string,
	opts UninstallOptions,
) error {
	if _, ok := s.loadOrderName(); !ok {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"this game has no load order file, so it has no external mods")
	}

	if !validModFolderName(folder) {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "invalid external mod folder")
	}

	modsFolder, err := s.modsPath(ctx)
	if err != nil {
		return err
	}

	target := filepath.Join(modsFolder, folder)
	if !domain.PathWithin(modsFolder, target) {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"external mod folder %q is outside the mods folder", folder)
	}

	if !platform.IsDir(platform.RealFS(), target) {
		return appErrors.New(appErrors.ErrCodeModNotFound,
			"external mod %q is not in the mods folder", folder)
	}

	gameID, err := s.activeGameID()
	if err != nil {
		return err
	}

	known, err := s.deps.Index.ListByGame(ctx, gameID)
	if err != nil {
		return err
	}

	for _, mod := range known {
		if mod.ModFolderName == folder {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"mod %q is managed by the manager; uninstall it instead", folder)
		}
	}

	if opts.Trash {
		if err := platform.MoveToTrash(target); err != nil {
			return err
		}
	} else if err := os.RemoveAll(target); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeUninstallFailed,
			"delete external mod %q", folder)
	}

	s.publish(Event{
		Type: EventModUninstallCompleted, ModID: externalModID(folder), ModName: folder,
	})
	s.log.Info().
		Str("op", "uninstall-external").
		Str("mod", externalModID(folder)).
		Str("path", target).
		Bool("trashed", opts.Trash).
		Msg("external mod deleted")

	if err := s.syncLoadOrderFile(ctx); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO,
			"sync load order file after deleting external mod")
	}

	return nil
}

// TrashAvailable reports whether the GUI should offer "move to trash" for an
// external mod.
func (s *ModService) TrashAvailable() bool { return platform.TrashAvailable() }
