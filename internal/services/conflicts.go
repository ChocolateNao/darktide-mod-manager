package services

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// requiredVersionsKey is the SourceData field a source may fill with a map of
// dependency id → version expression. It is the only way a source can express
// "this mod needs version 1.4 or newer of X" without abusing Dependencies, which
// only carries ids.
const requiredVersionsKey = "required_versions"

// DetectConflicts reports every conflict it can find among the installed mods.
// Only enabled mods are examined: a disabled mod's files are not in the game
// folder, so reporting a collision with one is noise the user cannot act on
// without enabling it first. The result is ordered by type and mod id so a CLI
// listing and a UI list agree, and one unreadable inventory narrows the report
// instead of aborting the sweep.
func (s *ModService) DetectConflicts(ctx context.Context) ([]domain.ConflictReport, error) {
	if err := checkInitialized(s); err != nil {
		return nil, err
	}

	if err := s.precheck(ctx); err != nil {
		return nil, err
	}

	mods, err := s.deps.Index.List(ctx)
	if err != nil {
		return nil, err
	}

	enabled := make([]domain.Mod, 0, len(mods))

	byID := make(map[string]domain.Mod, len(mods))
	for _, m := range mods {
		byID[m.ID] = m
		if m.Enabled {
			enabled = append(enabled, m)
		}
	}

	reports := make([]domain.ConflictReport, 0, len(enabled))

	reports = append(reports, s.duplicateIDConflicts(enabled)...)
	reports = append(reports, s.declaredConflicts(enabled)...)
	inventories := s.inventories(ctx, enabled)
	reports = append(reports, fileOverlapConflicts(enabled, inventories)...)
	reports = append(reports, dependencyConflicts(ctx, s.deps.Index, enabled, byID)...)
	reports = append(reports, versionMismatchConflicts(enabled, byID)...)

	stampReportIDs(reports)
	sortReports(reports)

	return reports, nil
}

// stampReportIDs fills the identity field on every report the detectors produced.
// One stamp site: not one per detector, because the identity is derived
// a detector that set it by hand and one that forgot would differ only in whether
// the frontend's "resolve" button works.
func stampReportIDs(reports []domain.ConflictReport) {
	for i := range reports {
		reports[i].ID = reports[i].Identity()
	}
}

// duplicateIDConflicts finds two distinct mod ids whose storage folders collide.
// The index keys mods by id, so a literal duplicate cannot be stored; what can
// happen is two ids that sanitise to the same folder, and those would overwrite
// each other's payload on install and undeploy each other's files on disable.
// Case is folded only where the filesystem folds it, so a Linux report stays free
// of clashes that do not exist there.
func (s *ModService) duplicateIDConflicts(mods []domain.Mod) []domain.ConflictReport {
	fold := s.deps.Platform == domain.PlatformWindows || s.deps.Platform == domain.PlatformDarwin
	key := func(folder string) string {
		if fold {
			return strings.ToLower(folder)
		}

		return folder
	}
	// owner remembers the first mod to claim a folder plus every id that has since
	// folded into it, so the third mod's report can name which two ids collided.
	type owner struct {
		ids   []string
		first domain.Mod
		used  bool
	}

	byFolder := make(map[string]*owner, len(mods))

	var out []domain.ConflictReport

	for _, m := range mods {
		folder := domain.ModVersionRoot("", m.ID)

		prev, ok := byFolder[key(folder)]
		if !ok {
			byFolder[key(folder)] = &owner{first: m, ids: []string{m.ID}}
			continue
		}

		prev.ids = append(prev.ids, m.ID)

		collided := quoteJoin(prev.ids)
		if prev.used {
			// Already reported against this folder; a third mod folding into it
			// gets its own report against the first rather than extending the pair.
			out = append(out, domain.ConflictReport{
				Type:     domain.ConflictDuplicateID,
				ModA:     &prev.first,
				ModB:     &m,
				Severity: domain.SeverityError,
				Details: fmt.Sprintf(
					"ids %s all map to the payload folder %q",
					collided,
					folder,
				),
				Recommendation: "rename one of the mods so its id is unique on disk",
			})

			continue
		}

		prev.used = true
		out = append(out, domain.ConflictReport{
			Type:     domain.ConflictDuplicateID,
			ModA:     &prev.first,
			ModB:     &m,
			Severity: domain.SeverityError,
			Details: fmt.Sprintf(
				"ids %s both map to the payload folder %q",
				collided,
				folder,
			),
			Recommendation: "rename one of the mods so its id is unique on disk",
		})
	}

	return out
}

// quoteJoin renders ids as `"a", "b", "c"`. They are quoted because they
// routinely contain spaces and slashes, and an unquoted "a b, ab" cannot be
// split back apart to tell the user which mod to rename.
func quoteJoin(ids []string) string {
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		quoted = append(quoted, fmt.Sprintf("%q", id))
	}

	return strings.Join(quoted, ", ")
}

// declaredConflicts reports mods that declare each other as incompatible.
func (s *ModService) declaredConflicts(mods []domain.Mod) []domain.ConflictReport {
	var out []domain.ConflictReport

	for i := range mods {
		for j := i + 1; j < len(mods); j++ {
			a, b := mods[i], mods[j]
			switch {
			case a.ConflictsWithMod(b.ID):
				out = append(out, domain.ConflictReport{
					Type:     domain.ConflictDeclaredIncompatibility,
					ModA:     &a,
					ModB:     &b,
					Severity: domain.SeverityError,
					Details: fmt.Sprintf(
						"%s declares an incompatibility with %s",
						a.ID,
						b.ID,
					),
					Recommendation: "disable or uninstall one of the two mods",
				})
			case b.ConflictsWithMod(a.ID):
				out = append(out, domain.ConflictReport{
					Type:     domain.ConflictDeclaredIncompatibility,
					ModA:     &b,
					ModB:     &a,
					Severity: domain.SeverityError,
					Details: fmt.Sprintf(
						"%s declares an incompatibility with %s",
						b.ID,
						a.ID,
					),
					Recommendation: "disable or uninstall one of the two mods",
				})
			}
		}
	}

	return out
}

// inventories loads every enabled mod's file inventory. A mod whose inventory
// cannot be read contributes no files but still takes part in the other checks.
func (s *ModService) inventories(
	ctx context.Context,
	mods []domain.Mod,
) map[string][]domain.ModFile {
	out := make(map[string][]domain.ModFile, len(mods))
	for _, m := range mods {
		files, err := s.deps.Index.GetFilesByMod(ctx, m.ID)
		if err != nil {
			continue
		}

		out[m.ID] = files
	}

	return out
}

// fileOverlapConflicts finds enabled mods that expose the same file name into
// the game's mods folder. The comparison is on the top-level entry name, since
// that is what lands in the game folder whatever the contents are; one report per
// colliding pair rather than per file keeps the list readable.
func fileOverlapConflicts(
	mods []domain.Mod,
	inventories map[string][]domain.ModFile,
) []domain.ConflictReport {
	owners := make(map[string][]string) // entry name -> mod ids

	byID := make(map[string]domain.Mod, len(mods))
	for _, m := range mods {
		byID[m.ID] = m
	}

	for _, m := range mods {
		files, ok := inventories[m.ID]
		if !ok {
			continue
		}

		for _, name := range topLevelNames(files) {
			owners[name] = append(owners[name], m.ID)
		}
	}

	type pair struct{ a, b string }

	collisions := make(map[pair][]string)

	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		ids := owners[name]
		if len(ids) < 2 {
			continue
		}

		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				collisions[pair{ids[i], ids[j]}] = append(collisions[pair{ids[i], ids[j]}], name)
			}
		}
	}

	keys := make([]pair, 0, len(collisions))
	for k := range collisions {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].a != keys[j].a {
			return keys[i].a < keys[j].a
		}

		return keys[i].b < keys[j].b
	})

	out := make([]domain.ConflictReport, 0, len(keys))
	for _, k := range keys {
		files := collisions[k]
		a, b := byID[k.a], byID[k.b]
		out = append(out, domain.ConflictReport{
			Type:     domain.ConflictFileOverlap,
			ModA:     &a,
			ModB:     &b,
			Severity: domain.SeverityWarning,
			Details:  overlapDetails(files),
			// Whichever file wins is decided by load order, which the user
			// controls; a warning rather than an error, since mods share files.
			Recommendation: "disable one mod or give the shared file a unique name",
		})
	}

	return out
}

// overlapDetails renders the shared file list for a report.
func overlapDetails(files []string) string {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	if len(sorted) == 0 {
		return "no shared entries"
	}

	if len(sorted) == 1 {
		return fmt.Sprintf("both expose %q in the game mods folder", sorted[0])
	}

	return fmt.Sprintf(
		"both expose %d entries in the game mods folder, including %q",
		len(sorted),
		sorted[0],
	)
}

// dependencyConflicts reports missing dependencies, cycles, and an explicit load
// order that contradicts a declared dependency.
func dependencyConflicts(
	ctx context.Context,
	index ModIndex,
	mods []domain.Mod,
	byID map[string]domain.Mod,
) []domain.ConflictReport {
	enabledIDs := make(map[string]bool, len(mods))
	for _, m := range mods {
		enabledIDs[m.ID] = true
	}

	enabled := make([]domain.Mod, len(mods))
	copy(enabled, mods)

	var out []domain.ConflictReport

	for i := range enabled {
		m := enabled[i]
		for _, dep := range m.Dependencies {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}

			installed, ok := byID[dep]
			if !ok {
				// No ModB: the dependency is not installed, so there is no mod to
				// point at and no "disable mod B" action to offer.
				out = append(out, domain.ConflictReport{
					Type:     domain.ConflictLoadOrder,
					ModA:     &m,
					Severity: domain.SeverityError,
					Details: fmt.Sprintf(
						"%s requires %s, which is not installed",
						m.ID,
						dep,
					),
					Recommendation: "install the missing dependency or disable " + m.ID,
				})

				continue
			}

			if !enabledIDs[dep] {
				out = append(out, domain.ConflictReport{
					Type:     domain.ConflictLoadOrder,
					ModA:     &m,
					ModB:     &installed,
					Severity: domain.SeverityWarning,
					Details: fmt.Sprintf(
						"%s requires %s, which is installed but disabled",
						m.ID,
						dep,
					),
					Recommendation: "enable " + dep,
				})
			}
		}
	}

	if cycle := findDependencyCycle(enabled); len(cycle) > 1 {
		a, b := byID[cycle[0]], byID[cycle[1]]
		out = append(out, domain.ConflictReport{
			Type:           domain.ConflictLoadOrder,
			ModA:           &a,
			ModB:           &b,
			Severity:       domain.SeverityError,
			Details:        fmt.Sprintf("dependency cycle: %s", strings.Join(cycle, " -> ")),
			Recommendation: "break the cycle by removing one of the dependencies",
		})
	}

	out = append(out, loadOrderConflicts(ctx, index, enabled, byID)...)

	return out
}

// loadOrderConflicts reports an explicit load order that puts a mod before one of
// its dependencies. The report count is bounded by the number of mods: an order
// can be wrong in one direction per (mod, dependency) pair.
func loadOrderConflicts(
	ctx context.Context,
	index ModIndex,
	mods []domain.Mod,
	byID map[string]domain.Mod,
) []domain.ConflictReport {
	order, err := index.LoadOrder(ctx)
	if err != nil || len(order) == 0 {
		return nil
	}

	position := make(map[string]int, len(order))
	for i, id := range order {
		position[id] = i
	}

	var out []domain.ConflictReport

	for _, m := range mods {
		self, ok := position[m.ID]
		if !ok {
			continue
		}

		for _, dep := range m.Dependencies {
			dep = strings.TrimSpace(dep)

			at, ok := position[dep]
			if !ok || at < self {
				continue
			}

			a, b := m, byID[dep]
			out = append(out, domain.ConflictReport{
				Type:           domain.ConflictLoadOrder,
				ModA:           &a,
				ModB:           &b,
				Severity:       domain.SeverityWarning,
				Details:        fmt.Sprintf("%s loads before %s, which it depends on", m.ID, dep),
				Recommendation: "move " + dep + " earlier in the load order",
			})
		}
	}

	return out
}

// findDependencyCycle returns the first cycle it can prove among the enabled
// mods, or nil. A dependency on a mod that is not installed cannot close a cycle
// — the edge does not exist in the enabled graph — so only enabled ids are
// followed.
func findDependencyCycle(mods []domain.Mod) []string {
	graph := make(map[string][]string, len(mods))

	known := make(map[string]bool, len(mods))
	for _, m := range mods {
		known[m.ID] = true
	}

	for _, m := range mods {
		for _, dep := range m.Dependencies {
			dep = strings.TrimSpace(dep)
			if dep != "" && known[dep] {
				graph[m.ID] = append(graph[m.ID], dep)
			}
		}
	}

	const (
		white = 0
		grey  = 1
		black = 2
	)

	colour := make(map[string]int, len(graph))

	var (
		stack []string
		found []string
		visit func(id string) bool
	)

	visit = func(id string) bool {
		colour[id] = grey

		stack = append(stack, id)
		for _, next := range graph[id] {
			switch colour[next] {
			case white:
				if visit(next) {
					return true
				}
			case grey:
				// Trim everything before the repeated node so the report names
				// the cycle, not the path that led into it.
				for i, seen := range stack {
					if seen == next {
						found = append(append([]string(nil), stack[i:]...), next)
						break
					}
				}

				return true
			}
		}

		stack = stack[:len(stack)-1]
		colour[id] = black

		return false
	}

	ids := make([]string, 0, len(graph))
	for id := range graph {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, id := range ids {
		if colour[id] == white && visit(id) {
			return found
		}
	}

	return nil
}

// versionMismatchConflicts reports dependencies whose installed version does not
// satisfy what the dependent mod asks for. The requirement comes from
// SourceData["required_versions"], a map of dependency id → expression; both the
// map[string]string a source hands over and the map[string]any a JSON round trip
// produces are accepted, since the mod travels through the database.
func versionMismatchConflicts(
	mods []domain.Mod,
	byID map[string]domain.Mod,
) []domain.ConflictReport {
	var out []domain.ConflictReport

	for i := range mods {
		m := mods[i]
		for dep, expr := range requiredVersions(m.SourceData) {
			installed, ok := byID[dep]
			if !ok {
				// A missing dependency is already reported by the load-order pass;
				// saying "version unknown" on top of it helps nobody.
				continue
			}

			if strings.TrimSpace(expr) == "" || strings.TrimSpace(installed.Version) == "" {
				continue
			}

			if domain.SatisfiesExpression(installed.Version, expr) {
				continue
			}

			a, b := m, installed
			out = append(out, domain.ConflictReport{
				Type:     domain.ConflictVersionMismatch,
				ModA:     &a,
				ModB:     &b,
				Severity: domain.SeverityError,
				Details: fmt.Sprintf(
					"%s requires %s %s, but %s is installed",
					m.ID,
					dep,
					expr,
					installed.Version,
				),
				Recommendation: "update or downgrade " + dep,
			})
		}
	}

	return out
}

// requiredVersions extracts the dependency → version expression map from a mod's
// source data. A malformed entry is skipped rather than reported: the value came
// from a third-party API, and refusing to scan a mod over it would be worse.
func requiredVersions(data map[string]any) map[string]string {
	if len(data) == 0 {
		return nil
	}

	switch raw := data[requiredVersionsKey].(type) {
	case map[string]string:
		out := make(map[string]string, len(raw))
		for k, v := range raw {
			out[k] = v
		}

		return out
	case map[string]any:
		out := make(map[string]string, len(raw))
		for k, v := range raw {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}

		return out
	default:
		return nil
	}
}

// sortReports orders reports so repeated runs produce the same list.
func sortReports(reports []domain.ConflictReport) {
	sort.SliceStable(reports, func(i, j int) bool {
		a, b := reports[i], reports[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}

		ai, bi, _ := a.ModIDs()

		bj, bj2, _ := b.ModIDs()
		if ai != bj {
			return ai < bj
		}

		if bi != bj2 {
			return bi < bj2
		}

		return a.Details < b.Details
	})
}

// ResolveConflict applies a resolution to a currently reported conflict. The
// report is re-detected first, and one that no longer exists is reported as
// ErrCodeNotFound rather than silently succeeding: the caller is working from a
// stale view, and a no-op would read as success.
func (s *ModService) ResolveConflict(
	ctx context.Context,
	reportID string,
	how ConflictResolution,
) error {
	if err := checkInitialized(s); err != nil {
		return err
	}

	if err := s.precheck(ctx); err != nil {
		return err
	}

	id := strings.TrimSpace(reportID)
	if id == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "a conflict id is required")
	}

	if how == ResolutionUnset {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "a conflict resolution is required")
	}

	reports, err := s.DetectConflicts(ctx)
	if err != nil {
		return err
	}

	var report *domain.ConflictReport

	for i := range reports {
		if reports[i].Identity() == id {
			report = &reports[i]
			break
		}
	}

	if report == nil {
		return appErrors.New(appErrors.ErrCodeNotFound, "conflict %q is no longer reported", id)
	}

	if how == ResolutionKeepBoth {
		s.publish(
			Event{Type: EventLog, Data: fmt.Sprintf("conflict %s accepted as intentional", id)},
		)

		return nil
	}

	first, second, both := report.ModIDs()
	if !both {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"conflict %s does not name two mods, so %s cannot be chosen", id, how)
	}

	var (
		target  string
		disable bool
	)

	switch how {
	case ResolutionDisableModA:
		target, disable = first, true
	case ResolutionDisableModB:
		target, disable = second, true
	case ResolutionUninstallModA:
		target = first
	case ResolutionUninstallModB:
		target = second
	default:
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"unknown conflict resolution %d", int(how))
	}

	if disable {
		return s.DisableMod(ctx, target)
	}

	return s.Uninstall(ctx, target, UninstallOptions{})
}
