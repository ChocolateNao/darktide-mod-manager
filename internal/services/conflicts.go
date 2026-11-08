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
// File and declaration checks look at enabled mods only: a disabled mod's files
// are not in the game folder, so a collision with one is noise. Dependency checks
// include disabled mods because a missing requirement still needs installing.
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

	return s.conflictsFor(ctx, mods), nil
}

// conflictsFor runs every detector over an in-memory mod list. EnableMod calls
// it on a copy with the target enabled, so a refusal costs no writes.
func (s *ModService) conflictsFor(ctx context.Context, mods []domain.Mod) []domain.ConflictReport {
	declared := mods
	mods, byFamily := resolveDependencies(mods)

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
	reports = append(reports, staleDependencyConflicts(declared, mods, byFamily)...)
	reports = append(reports, dependencyConflicts(mods, byID, s.gameLoadOrder(ctx))...)
	reports = append(reports, versionMismatchConflicts(mods, byID)...)

	stampReportIDs(reports)
	reports = dedupeReports(reports)
	sortReports(reports)

	return reports
}

// dependencyLabel names a dependency in a report: the installed mod when it is
// present, otherwise the id the source declared.
func dependencyLabel(id string, byID map[string]domain.Mod) string {
	if dep, ok := byID[id]; ok {
		return userLabel(&dep)
	}

	return id
}

// dependencyLabels names a path of dependencies for one report.
func dependencyLabels(ids []string, byID map[string]domain.Mod) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, dependencyLabel(id, byID))
	}

	return out
}

// resolveDependencies points each declared dependency at the installed row that
// satisfies it. A source can publish several files for one mod page, so a
// dependency names one file while the installed mod is another; after this pass
// every detector can keep comparing ids exactly.
func resolveDependencies(mods []domain.Mod) ([]domain.Mod, map[string]int) {
	byID := make(map[string]bool, len(mods))
	byFamily := make(map[string]int, len(mods))

	for i := range mods {
		byID[mods[i].ID] = true

		family := domain.SourceFamily(mods[i].ID)
		if chosen, ok := byFamily[family]; !ok || (!mods[chosen].Enabled && mods[i].Enabled) {
			byFamily[family] = i
		}
	}

	resolve := func(dep string) string {
		if dep == "" || byID[dep] {
			return dep
		}

		if at, ok := byFamily[domain.SourceFamily(dep)]; ok {
			return mods[at].ID
		}

		return dep
	}

	out := make([]domain.Mod, len(mods))
	for i := range mods {
		resolved := mods[i].Clone()
		resolved.Dependencies = make([]string, 0, len(mods[i].Dependencies))

		for _, dep := range mods[i].Dependencies {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}

			resolved.Dependencies = append(resolved.Dependencies, resolve(dep))
		}

		resolved.SourceData = remapRequiredVersions(resolved.SourceData, resolve)

		out[i] = *resolved
	}

	return out, byFamily
}

// remapRequiredVersions points the version requirements at the same row the
// dependency list resolves to, so a requirement keyed to an older file of the
// page still lands on the installed mod.
func remapRequiredVersions(data map[string]any, resolve func(string) string) map[string]any {
	switch m := data[requiredVersionsKey].(type) {
	case map[string]string:
		next := make(map[string]string, len(m))
		for dep, expr := range m {
			next[resolve(dep)] = expr
		}

		data[requiredVersionsKey] = next
	case map[string]any:
		next := make(map[string]any, len(m))
		for dep, expr := range m {
			next[resolve(dep)] = expr
		}

		data[requiredVersionsKey] = next
	}

	return data
}

// staleDependencyConflicts reports a dependency that names a file of a mod page
// while only an older file of that page is installed. resolveDependencies treats
// any file of the page as satisfying the dependency, so without this check the
// report disappears while the game still loads the older file.
func staleDependencyConflicts(
	declared []domain.Mod,
	resolved []domain.Mod,
	byFamily map[string]int,
) []domain.ConflictReport {
	var out []domain.ConflictReport

	seen := map[string]bool{}

	installedIDs := make(map[string]bool, len(resolved))
	for i := range resolved {
		installedIDs[resolved[i].ID] = true
	}

	for i := range declared {
		m := declared[i]
		for _, dep := range m.Dependencies {
			dep = strings.TrimSpace(dep)
			if dep == "" || installedIDs[dep] {
				continue
			}

			required, ok := domain.SourceFileNumber(dep)
			if !ok {
				continue
			}

			at, ok := byFamily[domain.SourceFamily(dep)]
			if !ok {
				continue
			}

			installed := &resolved[at]

			number, ok := domain.SourceFileNumber(installed.ID)
			if !ok || number >= required {
				continue
			}

			key := m.ID + "\x00" + installed.ID
			if seen[key] {
				continue
			}

			seen[key] = true

			a, b := m, *installed
			out = append(out, domain.ConflictReport{
				Type:     domain.ConflictVersionMismatch,
				ModA:     &a,
				ModB:     &b,
				Severity: domain.SeverityError,
				Details: fmt.Sprintf(
					"%s requires a newer file of %s than %s",
					userLabel(&m),
					modName(installed),
					userLabel(installed),
				),
				Recommendation: "update " + userLabel(installed) +
					" to the version " + userLabel(&m) + " requires",
			})
		}
	}

	return out
}

// enforceBlockingConflicts disables every side of an error-severity conflict
// that must stop loading. keepID is the mod the caller just activated; otherwise
// the newest update wins. Nothing here re-enables a mod.
func (s *ModService) enforceBlockingConflicts(ctx context.Context, gameID, keepID string) {
	reports, err := s.DetectConflicts(ctx)
	if err != nil {
		s.log.Warn().
			Err(err).
			Str("op", "conflict-enforce").
			Str("game", gameID).
			Msg("could not check for blocking conflicts")

		return
	}

	disabled := map[string]bool{}

	for i := range reports {
		report := reports[i]
		if report.Severity != domain.SeverityError {
			continue
		}

		loser := blockingLoser(report, gameID, keepID)
		if loser == nil || !loser.Enabled || disabled[loser.ID] {
			continue
		}

		if err := s.DisableMod(ctx, loser.ID); err != nil {
			s.log.Warn().
				Err(err).
				Str("op", "conflict-enforce").
				Str("mod", loser.ID).
				Msg("could not disable a mod that blocks another")

			continue
		}

		disabled[loser.ID] = true

		s.publish(Event{
			Type: EventLog, ModID: loser.ID, ModName: loser.Name, SourceID: loser.SourceID,
			Data: fmt.Sprintf("disabled %s: %s", userLabel(loser), report.Details),
		})
	}
}

// blockingLoser names the side of a report that must stop loading, or nil when
// the report does not apply to gameID. A one-sided report already names the
// offending mod; a version mismatch always unloads the dependent, because the
// dependency's version is the thing that cannot change.
func blockingLoser(report domain.ConflictReport, gameID, keepID string) *domain.Mod {
	if report.ModA == nil || report.ModA.GameID != gameID {
		return nil
	}

	if report.ModB == nil {
		return report.ModA
	}

	if report.ModB.GameID != gameID {
		return nil
	}

	if report.Type == domain.ConflictVersionMismatch {
		return report.ModA
	}

	return conflictLoser(report.ModA, report.ModB, keepID)
}

// enableBlockers returns the reports that would unload mod again right after it
// was enabled. EnableMod refuses those instead of leaving the user with a toggle
// that succeeds and then flips back.
func (s *ModService) enableBlockers(ctx context.Context, mod *domain.Mod) []domain.ConflictReport {
	mods, err := s.deps.Index.List(ctx)
	if err != nil {
		return nil
	}

	enabled := mod.Clone()
	enabled.Enabled = true

	for i := range mods {
		if mods[i].ID == mod.ID {
			mods[i] = *enabled
		}
	}

	var out []domain.ConflictReport

	for _, report := range s.conflictsFor(ctx, mods) {
		if report.Severity != domain.SeverityError {
			continue
		}

		if loser := blockingLoser(report, mod.GameID, mod.ID); loser != nil &&
			loser.ID == mod.ID {
			out = append(out, report)
		}
	}

	return out
}

// conflictLoser picks the side that stops loading. The just-activated mod wins;
// the remaining order is a stable tie-break for mods nobody just touched.
func conflictLoser(a, b *domain.Mod, keepID string) *domain.Mod {
	switch {
	case a.ID == keepID && b.ID != keepID:
		return b
	case b.ID == keepID && a.ID != keepID:
		return a
	case a.UpdatedAt.After(b.UpdatedAt):
		return b
	case b.UpdatedAt.After(a.UpdatedAt):
		return a
	case a.Priority > b.Priority:
		return b
	case b.Priority > a.Priority:
		return a
	case a.ID < b.ID:
		return b
	default:
		return a
	}
}

// dedupeReports keeps one report per pair: a stale file and a version
// requirement can both describe the same disagreement, and the frontend keys
// resolution by the report id.
func dedupeReports(reports []domain.ConflictReport) []domain.ConflictReport {
	seen := make(map[string]bool, len(reports))
	out := reports[:0]

	for _, report := range reports {
		if seen[report.ID] {
			continue
		}

		seen[report.ID] = true
		out = append(out, report)
	}

	return out
}

// stampReportIDs fills the identity field on every report the detectors produced.
// All detectors share one derivation: the identity comes from the report itself,
// and a detector that set it by hand beside one that forgot would differ only
// in whether the frontend's "resolve" button works.
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
		ids    []string
		labels []string
		first  domain.Mod
		used   bool
	}

	byFolder := make(map[string]*owner, len(mods))

	var out []domain.ConflictReport

	for _, m := range mods {
		folder := domain.ModVersionRoot("", m.ID)

		prev, ok := byFolder[key(folder)]
		if !ok {
			byFolder[key(folder)] = &owner{
				first: m, ids: []string{m.ID}, labels: []string{userLabel(&m)},
			}

			continue
		}

		prev.ids = append(prev.ids, m.ID)
		prev.labels = append(prev.labels, userLabel(&m))

		collided := quoteJoin(prev.labels)
		if prev.used {
			// Already reported against this folder; a third mod folding into it
			// gets its own report against the first, not extending the pair.
			out = append(out, domain.ConflictReport{
				Type:     domain.ConflictDuplicateID,
				ModA:     &prev.first,
				ModB:     &m,
				Severity: domain.SeverityError,
				Details: fmt.Sprintf(
					"%s all share the payload folder %q",
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
				"%s share the payload folder %q",
				collided,
				folder,
			),
			Recommendation: "rename one of the mods so its id is unique on disk",
		})
	}

	return out
}

// quoteJoin renders names as `"a", "b", "c"`. They are quoted because they
// routinely contain spaces and slashes, and an unquoted "a b, ab" cannot be
// split back apart to tell the user which mod to rename.
func quoteJoin(labels []string) string {
	quoted := make([]string, 0, len(labels))
	for _, label := range labels {
		quoted = append(quoted, fmt.Sprintf("%q", label))
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
						userLabel(&a),
						userLabel(&b),
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
						userLabel(&b),
						userLabel(&a),
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
// colliding pair, not per file keeps the list readable.
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
			Details:  overlapDetails(&a, &b, files),
			// Whichever file wins is decided by load order, which the user
			// controls; a warning, not an error, since mods share files.
			Recommendation: "disable one mod or give the shared file a unique name",
		})
	}

	return out
}

// overlapDetails names both mods and the shared entries for a report.
func overlapDetails(a, b *domain.Mod, files []string) string {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	if len(sorted) == 0 {
		return fmt.Sprintf("%s and %s share no entries", userLabel(a), userLabel(b))
	}

	if len(sorted) == 1 {
		return fmt.Sprintf("%s and %s both expose %q in the game mods folder",
			userLabel(a), userLabel(b), sorted[0])
	}

	return fmt.Sprintf(
		"%s and %s both expose %d entries in the game mods folder, including %q",
		userLabel(a),
		userLabel(b),
		len(sorted),
		sorted[0],
	)
}

// dependencyConflicts reports missing dependencies, cycles, and an explicit load
// order that contradicts a declared dependency.
func dependencyConflicts(
	mods []domain.Mod,
	byID map[string]domain.Mod,
	order []string,
) []domain.ConflictReport {
	enabledIDs := make(map[string]bool, len(mods))
	enabled := make([]domain.Mod, 0, len(mods))

	for _, m := range mods {
		if m.Enabled {
			enabledIDs[m.ID] = true
			enabled = append(enabled, m)
		}
	}

	var out []domain.ConflictReport

	for i := range mods {
		m := mods[i]
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
					Type:         domain.ConflictLoadOrder,
					ModA:         &m,
					DependencyID: dep,
					Severity:     domain.SeverityError,
					Details: fmt.Sprintf(
						"%s requires %s, which is not installed",
						userLabel(&m),
						dependencyLabel(dep, byID),
					),
					Recommendation: "install the missing dependency or disable " + userLabel(&m),
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
						userLabel(&m),
						userLabel(&installed),
					),
					Recommendation: "enable " + userLabel(&installed),
				})
			}
		}
	}

	if cycle := findDependencyCycle(enabled); len(cycle) > 1 {
		a, b := byID[cycle[0]], byID[cycle[1]]
		out = append(out, domain.ConflictReport{
			Type:     domain.ConflictLoadOrder,
			ModA:     &a,
			ModB:     &b,
			Severity: domain.SeverityError,
			Details: "dependency cycle: " + strings.Join(
				dependencyLabels(cycle, byID), " -> ",
			),
			Recommendation: "break the cycle by removing one of the dependencies",
		})
	}

	out = append(out, loadOrderConflicts(enabled, byID, order)...)

	return out
}

// loadOrderConflicts reports an explicit load order that puts a mod before one of
// its dependencies. The report count is bounded by the number of mods: an order
// can be wrong in one direction per (mod, dependency) pair.
func loadOrderConflicts(
	mods []domain.Mod,
	byID map[string]domain.Mod,
	order []string,
) []domain.ConflictReport {
	if len(order) == 0 {
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
				Type:     domain.ConflictLoadOrder,
				ModA:     &a,
				ModB:     &b,
				Severity: domain.SeverityWarning,
				Details: fmt.Sprintf("%s loads before %s, which it depends on",
					userLabel(&m), dependencyLabel(dep, byID)),
				Recommendation: "move " + dependencyLabel(dep, byID) + " earlier in the load order",
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
					userLabel(&m),
					dependencyLabel(dep, byID),
					expr,
					userLabel(&installed),
				),
				Recommendation: "update or downgrade " + userLabel(&installed),
			})
		}
	}

	return out
}

// requiredVersions extracts the dependency → version expression map from a mod's
// source data. A malformed entry is skipped, not reported: the value came
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
// ErrCodeNotFound, not silently succeeding: the caller is working from a
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
