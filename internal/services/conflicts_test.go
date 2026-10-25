package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conflictMod builds an enabled mod with no payload on disk: conflict detection
// only ever looks at the record and its inventory.
func conflictMod(id string) domain.Mod {
	return domain.Mod{ID: id, Name: id, Version: "1.0.0", SourceID: "test", Enabled: true}
}

// withFiles records an inventory whose top-level entries are the given names:
// filesFor("a.dll", "textures/hud.dds") models a mod that puts a.dll and the
// whole textures directory into the game folder.
func withFiles(t *testing.T, h *harness, id string, names ...string) *harness {
	t.Helper()

	files := make([]domain.ModFile, 0, len(names))
	for _, name := range names {
		files = append(files, domain.ModFile{ModID: id, RelPath: name, SizeBytes: 1})
	}

	h.index.setFiles(id, files)

	return h
}

// reportFor finds the first report of type t mentioning modIDs.
func reportFor(
	reports []domain.ConflictReport,
	kind domain.ConflictType,
	modIDs ...string,
) *domain.ConflictReport {
	for i := range reports {
		if reports[i].Type != kind {
			continue
		}

		if len(modIDs) == 0 {
			return &reports[i]
		}

		first, second, _ := reports[i].ModIDs()

		want := append([]string(nil), modIDs...)
		sort.Strings(want)

		got := []string{first, second}
		sort.Strings(got)

		if len(got) == len(want) && got[0] == want[0] && got[1] == want[1] {
			return &reports[i]
		}
	}

	return nil
}

// reportTypes renders the reports as "type:a|b" so an ordering or content
// assertion reads as one line.
func reportTypes(reports []domain.ConflictReport) []string {
	out := make([]string, 0, len(reports))
	for _, r := range reports {
		a, b, _ := r.ModIDs()
		out = append(out, fmt.Sprintf("%s:%s|%s", r.Type, a, b))
	}

	return out
}

func TestDetectConflictsOnNothingInstalled(t *testing.T) {
	h := newHarness(t)

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	assert.Empty(t, reports, "expected no conflicts, got %v", reportTypes(reports))
}

func TestDetectConflictsNilService(t *testing.T) {
	var svc *ModService
	{
		_, err := svc.DetectConflicts(
			context.Background(),
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "nil service should be INVALID_ARGUMENT, got %v", err)
	}
}

func TestDetectConflictsRefusesServiceWithoutIndex(t *testing.T) {
	svc := &ModService{}
	{
		_, err := svc.DetectConflicts(
			context.Background(),
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "missing index should be INVALID_ARGUMENT, got %v", err)
	}
}

func TestDetectConflictsCanceledContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		_, err := h.svc.DetectConflicts(
			ctx,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "cancelled context should be OPERATION_CANCELED, got %v", err)
	}

	assert.Empty(t, h.index.saved, "a cancelled scan must not write")
}

func TestDetectConflictsPropagatesIndexFailure(t *testing.T) {
	h := newHarness(t)

	h.index.listErr = errBoom
	{
		_, err := h.svc.DetectConflicts(context.Background())
		assert.Error(t, err, "a failing index must fail the scan")
	}
}

func TestDetectConflictsDuplicateIDsThatShareAPayloadFolder(t *testing.T) {
	h := newHarness(t)
	// "a b" and "ab" are distinct keys, but SanitizeSegment drops the space, so
	// both land in the folder "ab".
	h.index.put(&domain.Mod{ID: "a b", Version: "1.0.0", Enabled: true})
	h.index.put(&domain.Mod{ID: "ab", Version: "2.0.0", Enabled: true})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	report := reportFor(reports, domain.ConflictDuplicateID)
	require.NotNil(t, report, "expected a duplicate id conflict, got %v", reportTypes(reports))

	assert.Equal(
		t,
		domain.SeverityError,
		report.Severity,
		"duplicate payload folders corrupt data, severity = %q, want error",
		report.Severity,
	)

	assert.Contains(
		t,
		report.Details,
		"ab",
		"details should name the shared folder, got %q",
		report.Details,
	)
	// Every id that folds into the folder must appear, or the user cannot tell
	// which mod to rename.
	for _, id := range []string{"a b", "ab"} {
		assert.Contains(t, report.Details, id, "details %q should mention %q", report.Details, id)
	}
}

func TestDetectConflictsThirdIDInTheSameFolderReportsAllThree(t *testing.T) {
	h := newHarness(t)
	// All three sanitise to "ab": one verbatim, one via a dropped space, one via
	// a dropped path separator.
	h.index.put(&domain.Mod{ID: "ab", Version: "1.0.0", Enabled: true})
	h.index.put(&domain.Mod{ID: "a b", Version: "1.0.0", Enabled: true})
	h.index.put(&domain.Mod{ID: "a/b", Version: "1.0.0", Enabled: true})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	dupes := 0

	for i := range reports {
		if reports[i].Type == domain.ConflictDuplicateID {
			dupes++
		}
	}
	// First pair, then the third mod against the first: two reports.
	assert.Equal(
		t,
		2,
		dupes,
		"got %d duplicate-id reports, want 2: %v",
		dupes,
		reportTypes(reports),
	)

	var sawAllThree bool

	for i := range reports {
		if reports[i].Type != domain.ConflictDuplicateID {
			continue
		}

		if strings.Contains(reports[i].Details, `"a b"`) &&
			strings.Contains(reports[i].Details, `"ab"`) &&
			strings.Contains(reports[i].Details, `"a/b"`) {
			sawAllThree = true
		}
	}

	assert.True(
		t,
		sawAllThree,
		"the second report should name all three ids, got %v",
		reportTypes(reports),
	)
}

func TestDetectConflictsIgnoresCollidingIDOfADisabledMod(t *testing.T) {
	h := newHarness(t)
	h.index.put(&domain.Mod{ID: "a b", Version: "1.0.0", Enabled: true})
	h.index.put(&domain.Mod{ID: "ab", Version: "1.0.0", Enabled: false})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		r := reportFor(reports, domain.ConflictDuplicateID)
		assert.Nil(t, r, "a disabled mod's payload is not deployed, so it cannot clash: %+v", r)
	}
}

func TestDetectConflictsDeclaredIncompatibilityIsSymmetric(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.ConflictsWith = []string{"beta"}
	h.index.put(&a)
	h.index.putValue(conflictMod("beta"))

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	report := reportFor(reports, domain.ConflictDeclaredIncompatibility)
	require.NotNil(t, report, "expected a declared conflict, got %v", reportTypes(reports))

	first, second, _ := report.ModIDs()
	assert.False(
		t,
		first != "alpha" || second != "beta",
		"report should name alpha and beta, got %q and %q",
		first,
		second,
	)

	assert.Contains(
		t,
		report.Details,
		"alpha declares",
		"details should say who declared it, got %q",
		report.Details,
	)
}

func TestDetectConflictsDeclaredIncompatibilityDeclaredByTheSecondMod(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))
	beta := conflictMod("beta")
	beta.ConflictsWith = []string{"alpha"}
	h.index.put(&beta)

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	report := reportFor(reports, domain.ConflictDeclaredIncompatibility)
	require.NotNil(t, report, "expected a declared conflict, got %v", reportTypes(reports))
	// The declaring mod is always ModA, whichever side of the scan found it.
	assert.False(
		t,
		report.ModA == nil || report.ModA.ID != "beta",
		"ModA should be the declaring mod beta, got %+v",
		report.ModA,
	)
}

func TestDetectConflictsDeclaredIncompatibilityIgnoresSelf(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.ConflictsWith = []string{"alpha"}
	h.index.put(&a)
	h.index.putValue(conflictMod("beta"))

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		r := reportFor(reports, domain.ConflictDeclaredIncompatibility)
		assert.Nil(t, r, "a mod listing itself is odd but not a conflict between two mods: %+v", r)
	}
}

func TestDetectConflictsFileOverlap(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))
	h.index.putValue(conflictMod("beta"))
	withFiles(t, h, "alpha", "hud.dds", "textures/logo.dds")
	withFiles(t, h, "beta", "textures/logo.dds")

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	report := reportFor(reports, domain.ConflictFileOverlap)
	require.NotNil(t, report, "expected a file overlap, got %v", reportTypes(reports))

	assert.Equal(
		t,
		domain.SeverityWarning,
		report.Severity,
		"shared files are a warning, got %q",
		report.Severity,
	)

	assert.Contains(
		t,
		report.Details,
		"textures",
		"details should name the shared top-level entry, got %q",
		report.Details,
	)
}

func TestDetectConflictsFileOverlapComparesTopLevelEntriesNotFullPaths(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))
	h.index.putValue(conflictMod("beta"))
	// Same directory, different leaf files: the game sees the textures directory
	// from both, so this *is* an overlap even though no single path matches.
	withFiles(t, h, "alpha", "textures/hud.dds")
	withFiles(t, h, "beta", "textures/logo.dds")

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		r := reportFor(reports, domain.ConflictFileOverlap)
		assert.NotNil(
			t,
			r,
			"expected an overlap on the shared directory, got %v",
			reportTypes(reports),
		)
	}
}

func TestDetectConflictsFileOverlapIsOneReportPerPairNotPerFile(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))
	h.index.putValue(conflictMod("beta"))
	withFiles(t, h, "alpha", "a.dll", "b.dll", "c.dll")
	withFiles(t, h, "beta", "a.dll", "b.dll", "c.dll")

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	count := 0

	for i := range reports {
		if reports[i].Type == domain.ConflictFileOverlap {
			count++
		}
	}

	assert.Equal(
		t,
		1,
		count,
		"three shared files should be one report, got %d: %v",
		count,
		reportTypes(reports),
	)

	if reportFor(reports, domain.ConflictFileOverlap) != nil {
		assert.Contains(
			t,
			reportTypes(reports)[0],
			"alpha",
			"details should mention the count, got %v",
			reportTypes(reports),
		)
	}

	report := reportFor(reports, domain.ConflictFileOverlap)
	assert.False(
		t,
		report != nil && !strings.Contains(report.Details, "3 entries"),
		"details should say how many entries clash, got %q",
		report.Details,
	)
}

func TestDetectConflictsFileOverlapOfThreeModsCoversEveryPair(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"alpha", "beta", "gamma"} {
		h.index.putValue(conflictMod(id))
		withFiles(t, h, id, "hud.dds")
	}

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	pairs := map[string]bool{}

	for i := range reports {
		if reports[i].Type != domain.ConflictFileOverlap {
			continue
		}

		a, b, _ := reports[i].ModIDs()
		pairs[a+"|"+b] = true
	}

	for _, want := range []string{"alpha|beta", "alpha|gamma", "beta|gamma"} {
		assert.True(t, pairs[want], "missing overlap between %s, got %v", want, pairs)
	}
}

func TestDetectConflictsNoFileOverlapForDisjointEntries(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))
	h.index.putValue(conflictMod("beta"))
	withFiles(t, h, "alpha", "a.dll")
	withFiles(t, h, "beta", "b.dll")

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		r := reportFor(reports, domain.ConflictFileOverlap)
		assert.Nil(t, r, "disjoint entries do not overlap: %+v", r)
	}
}

func TestDetectConflictsSurvivesAnUnreadableInventory(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))
	h.index.putValue(conflictMod("beta"))
	withFiles(t, h, "alpha", "hud.dds")

	beta := conflictMod("beta")
	beta.ConflictsWith = []string{"alpha"}
	h.index.put(&beta)
	h.index.filesErr = errBoom

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "one unreadable inventory must not fail the sweep")
	// File overlap is unknowable, but the declared conflict does not need files.
	assert.NotNil(
		t,
		reportFor(reports, domain.ConflictDeclaredIncompatibility),
		"the declared conflict should still be reported, got %v",
		reportTypes(reports),
	)

	{
		r := reportFor(reports, domain.ConflictFileOverlap)
		assert.Nil(t, r, "no files were read for beta, so no overlap can be claimed: %+v", r)
	}
}

func TestOverlapDetailsSingularAndPlural(t *testing.T) {
	{
		got, want := overlapDetails(
			[]string{"a.dll"},
		), `both expose "a.dll" in the game mods folder`
		assert.Equal(t, want, got, "one file: %q, want %q", got, want)
	}

	got := overlapDetails([]string{"c.dll", "a.dll", "b.dll"})
	assert.False(
		t,
		!strings.Contains(got, "3 entries") || !strings.Contains(got, `"a.dll"`),
		"three files: %q should name the count and the alphabetically first",
		got,
	)

	{
		got := overlapDetails(nil)
		assert.Equal(t, "no shared entries", got, "no files: %q, want %q", got, "no shared entries")
	}
}

func TestOverlapDetailsDoesNotMutateItsArgument(t *testing.T) {
	files := []string{"c.dll", "a.dll"}

	_ = overlapDetails(files)
	assert.False(
		t,
		files[0] != "c.dll" || files[1] != "a.dll",
		"overlapDetails sorted the caller's slice in place: %v",
		files,
	)
}

func TestDetectConflictsMissingDependencyHasNoModB(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"missing"}
	h.index.put(&a)

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	report := reportFor(reports, domain.ConflictLoadOrder)
	require.NotNil(t, report, "expected a load-order conflict, got %v", reportTypes(reports))

	assert.Nil(
		t,
		report.ModB,
		"a dependency that is not installed has no mod to point at, got %+v",
		report.ModB,
	)

	{
		_, _, both := report.ModIDs()
		assert.False(t, both, "a one-sided report must not claim two mods")
	}

	assert.Equal(
		t,
		domain.SeverityError,
		report.Severity,
		"an unsatisfiable dependency is an error, got %q",
		report.Severity,
	)
}

func TestDetectConflictsDisabledDependencyIsAWarning(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"beta"}
	h.index.put(&a)
	h.index.put(&domain.Mod{ID: "beta", Version: "1.0.0", Enabled: false})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	report := reportFor(reports, domain.ConflictLoadOrder, "alpha", "beta")
	require.NotNil(t, report, "expected a load-order conflict, got %v", reportTypes(reports))

	assert.False(
		t,
		report.ModB == nil || report.ModB.ID != "beta",
		"the disabled dependency should be named so it can be enabled, got %+v",
		report.ModB,
	)

	assert.Equal(
		t,
		domain.SeverityWarning,
		report.Severity,
		"the dependency is installed, just off: severity = %q, want warning",
		report.Severity,
	)
}

func TestDetectConflictsIgnoresBlankDependency(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"", "   "}
	h.index.put(&a)

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	assert.Empty(
		t,
		reports,
		"blank dependencies are not dependencies, got %v",
		reportTypes(reports),
	)
}

func TestDetectConflictsCycle(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"beta"}
	b := conflictMod("beta")
	b.Dependencies = []string{"gamma"}
	g := conflictMod("gamma")
	g.Dependencies = []string{"alpha"}

	h.index.put(&a)
	h.index.put(&b)
	h.index.put(&g)

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	var found bool

	for i := range reports {
		if reports[i].Type == domain.ConflictLoadOrder &&
			strings.Contains(reports[i].Details, "dependency cycle") {
			found = true

			assert.Equal(
				t,
				domain.SeverityError,
				reports[i].Severity,
				"a cycle is an error, got %q",
				reports[i].Severity,
			)
			// The report must name the cycle, not the path that led into it.
			assert.Contains(
				t,
				reports[i].Details,
				"->",
				"details should render the cycle, got %q",
				reports[i].Details,
			)

			assert.False(t, strings.Contains(reports[i].Details, " -> ") &&
				!strings.Contains(reports[i].Details, "gamma -> alpha") &&
				strings.Count(
					reports[i].Details,
					"->",
				) == 3, "the cycle should be alpha -> beta -> gamma -> alpha, got %q", reports[i].Details)
		}
	}

	assert.True(t, found, "expected a cycle report, got %v", reportTypes(reports))
}

func TestFindDependencyCycleCases(t *testing.T) {
	mod := func(id string, deps ...string) domain.Mod {
		return domain.Mod{ID: id, Dependencies: deps}
	}

	{
		got := findDependencyCycle(nil)
		assert.Nil(t, got, "no mods: %v, want nil", got)
	}

	{
		got := findDependencyCycle([]domain.Mod{mod("a")})
		assert.Nil(t, got, "no dependencies: %v, want nil", got)
	}

	{
		got := findDependencyCycle([]domain.Mod{mod("a", "b")})
		assert.Nil(t, got, "an edge to an absent mod cannot close a cycle: %v", got)
	}

	{
		got := findDependencyCycle(
			[]domain.Mod{mod("a", "b"), mod("b", "c"), mod("c", "a")},
		)
		assert.Len(t, got, 4, "three-mod cycle: %v, want four nodes with the start repeated", got)
	}

	if got := findDependencyCycle([]domain.Mod{mod("a", "b"), mod("b", "b")}); len(got) != 2 {
		// A self-dependency renders as "b -> b": the stack is trimmed to the
		// repeated node, so the node is not repeated a second time.
		assert.Len(t, got, 2, "self cycle: %v, want [b b]", got)
	}
	// A diamond is not a cycle.
	{
		got := findDependencyCycle([]domain.Mod{
			mod("a", "b", "c"), mod("b", "d"), mod("c", "d"), mod("d"),
		})
		assert.Nil(t, got, "a diamond has no cycle, got %v", got)
	}
}

func TestFindDependencyCycleIsDeterministic(t *testing.T) {
	mods := []domain.Mod{
		{ID: "zeta", Dependencies: []string{"alpha"}},
		{ID: "alpha", Dependencies: []string{"beta"}},
		{ID: "beta", Dependencies: []string{"zeta"}},
	}

	first := findDependencyCycle(mods)
	for i := 0; i < 8; i++ {
		got := findDependencyCycle(mods)
		require.Len(t, got, len(first), "run %d produced %v, first produced %v", i, got, first)

		for j := range got {
			require.Equal(t, first[j], got[j], "run %d differs at %d: %v vs %v", i, j, got, first)
		}
	}
}

func TestDetectConflictsExplicitLoadOrderPutsADependencyLast(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"beta"}
	h.index.put(&a)
	h.index.putValue(conflictMod("beta"))
	h.index.setOrder([]string{"alpha", "beta"})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	var found bool

	for i := range reports {
		if reports[i].Type == domain.ConflictLoadOrder &&
			strings.Contains(reports[i].Details, "loads before") {
			found = true

			assert.Equal(
				t,
				domain.SeverityWarning,
				reports[i].Severity,
				"load order is a warning, got %q",
				reports[i].Severity,
			)
		}
	}

	assert.True(t, found, "expected a load-order report, got %v", reportTypes(reports))
}

func TestDetectConflictsExplicitLoadOrderInTheRightOrderIsFine(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"beta"}
	h.index.put(&a)
	h.index.putValue(conflictMod("beta"))
	h.index.setOrder([]string{"beta", "alpha"})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	for i := range reports {
		assert.False(t, reports[i].Type == domain.ConflictLoadOrder &&
			strings.Contains(
				reports[i].Details,
				"loads before",
			), "beta loads first, so nothing is wrong: %q", reports[i].Details)
	}
}

func TestDetectConflictsLoadOrderSkipsModsAbsentFromIt(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"beta"}
	h.index.put(&a)
	h.index.putValue(conflictMod("beta"))
	h.index.setOrder([]string{"alpha"})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	for i := range reports {
		assert.NotContains(
			t,
			reports[i].Details,
			"loads before",
			"beta is not in the order, so its position is unknown: %q",
			reports[i].Details,
		)
	}
}

func TestDetectConflictsLoadOrderFailureIsNotFatal(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))
	h.index.loadOrderErr = errBoom

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "an unreadable load order must not fail the sweep")

	assert.Empty(t, reports, "expected no reports, got %v", reportTypes(reports))
}

func TestDetectConflictsVersionMismatch(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"beta"}
	a.SourceData = map[string]any{"required_versions": map[string]string{"beta": ">=2.0.0"}}
	h.index.put(&a)
	h.index.put(&domain.Mod{ID: "beta", Version: "1.0.0", Enabled: true})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	report := reportFor(reports, domain.ConflictVersionMismatch, "alpha", "beta")
	require.NotNil(t, report, "expected a version mismatch, got %v", reportTypes(reports))

	assert.False(
		t,
		!strings.Contains(report.Details, ">=2.0.0") || !strings.Contains(report.Details, "1.0.0"),
		"details should name both versions, got %q",
		report.Details,
	)
}

func TestDetectConflictsVersionSatisfiedIsNotReported(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"beta"}
	a.SourceData = map[string]any{"required_versions": map[string]string{"beta": ">=1.0.0"}}
	h.index.put(&a)
	h.index.put(&domain.Mod{ID: "beta", Version: "2.5.0", Enabled: true})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		r := reportFor(reports, domain.ConflictVersionMismatch)
		assert.Nil(t, r, "2.5.0 satisfies >=1.0.0: %+v", r)
	}
}

func TestDetectConflictsVersionMismatchAcceptsJSONRoundTrippedMap(t *testing.T) {
	// The mod travels through the database, so by the time it is scanned the map
	// has come back as map[string]any, not map[string]string.
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"beta"}
	a.SourceData = map[string]any{"required_versions": map[string]any{"beta": ">=9.0.0"}}
	h.index.put(&a)
	h.index.put(&domain.Mod{ID: "beta", Version: "1.0.0", Enabled: true})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	assert.NotNil(
		t,
		reportFor(reports, domain.ConflictVersionMismatch),
		"map[string]any should be understood too, got %v",
		reportTypes(reports),
	)
}

func TestDetectConflictsVersionMismatchSkipsMissingDependency(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.SourceData = map[string]any{"required_versions": map[string]string{"ghost": ">=1.0.0"}}
	h.index.put(&a)

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		r := reportFor(reports, domain.ConflictVersionMismatch)
		assert.Nil(
			t,
			r,
			"nothing is installed for 'ghost', so the load-order check owns it: %+v",
			r,
		)
	}
}

func TestDetectConflictsVersionMismatchSkipsBlankExpressionsAndVersions(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.SourceData = map[string]any{"required_versions": map[string]string{"beta": "   "}}
	h.index.put(&a)
	h.index.put(&domain.Mod{ID: "beta", Version: "1.0.0", Enabled: true})

	c := conflictMod("gamma")
	c.SourceData = map[string]any{"required_versions": map[string]string{"beta": ">=1.0.0"}}
	h.index.put(&c)
	h.index.put(&domain.Mod{ID: "beta", Version: "1.0.0", Enabled: false})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		r := reportFor(reports, domain.ConflictVersionMismatch)
		assert.Nil(t, r, "a blank expression cannot be unsatisfied: %+v", r)
	}
}

func TestDetectConflictsVersionMismatchSkipsModsWithoutAVersion(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.SourceData = map[string]any{"required_versions": map[string]string{"beta": ">=1.0.0"}}
	h.index.put(&a)
	h.index.put(&domain.Mod{ID: "beta", Version: "", Enabled: true})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		r := reportFor(reports, domain.ConflictVersionMismatch)
		assert.Nil(t, r, "an unversioned mod cannot mismatch: %+v", r)
	}
}

func TestRequiredVersionsShapes(t *testing.T) {
	{
		got := requiredVersions(nil)
		assert.Nil(t, got, "nil data: %v, want nil", got)
	}

	{
		got := requiredVersions(map[string]any{"required_versions": "not a map"})
		assert.Nil(t, got, "wrong shape: %v, want nil", got)
	}

	{
		got := requiredVersions(map[string]any{"other": "x"})
		assert.Nil(t, got, "missing key: %v, want nil", got)
	}

	got := requiredVersions(map[string]any{
		"required_versions": map[string]any{"a": ">=1", "b": 7},
	})
	assert.False(
		t,
		len(got) != 1 || got["a"] != ">=1",
		"non-string values should be dropped, got %v",
		got,
	)
	// The caller's map must not be reachable from the result.
	src := map[string]string{"a": ">=1"}
	clone := requiredVersions(map[string]any{"required_versions": src})
	clone["a"] = "mutated"

	assert.Equal(t, ">=1", src["a"], "requiredVersions aliased the caller's map")
}

func TestDetectConflictsIsOrdered(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"ghost"}
	a.SourceData = map[string]any{"required_versions": map[string]string{"beta": ">=5.0.0"}}
	h.index.put(&a)
	h.index.put(&domain.Mod{ID: "beta", Version: "1.0.0", Enabled: true})
	withFiles(t, h, "alpha", "hud.dds")
	withFiles(t, h, "beta", "hud.dds")

	first, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")
	// Map iteration order varies between runs, so repeat the scan: the order
	// must not vary with it.
	for i := 0; i < 10; i++ {
		got, err := h.svc.DetectConflicts(context.Background())
		require.NoError(t, err, "run %d: %v", i)

		require.Len(
			t,
			got,
			len(first),
			"run %d returned %d reports, first returned %d",
			i,
			len(got),
			len(first),
		)

		for j := range got {
			require.False(
				t,
				got[j].Identity() != first[j].Identity() || got[j].Type != first[j].Type,
				"run %d differs at %d: %q vs %q",
				i,
				j,
				got[j].Identity(),
				first[j].Identity(),
			)
		}
	}
	// And the ordering itself: types ascend, which puts file_overlap before
	// load_order before version_mismatch.
	want := []domain.ConflictType{
		domain.ConflictFileOverlap,
		domain.ConflictLoadOrder,
		domain.ConflictVersionMismatch,
	}
	require.Len(
		t,
		first,
		len(want),
		"got %d reports, want %d: %v",
		len(first),
		len(want),
		reportTypes(first),
	)

	for i, kind := range want {
		assert.Equal(
			t,
			kind,
			first[i].Type,
			"report %d is %q, want %q (full list %v)",
			i,
			first[i].Type,
			kind,
			reportTypes(first),
		)
	}
}

func TestSortReportsBreaksTiesOnDetails(t *testing.T) {
	a := domain.Mod{ID: "a"}
	b := domain.Mod{ID: "b"}
	reports := []domain.ConflictReport{
		{Type: domain.ConflictFileOverlap, ModA: &a, ModB: &b, Details: "zeta"},
		{Type: domain.ConflictFileOverlap, ModA: &a, ModB: &b, Details: "alpha"},
		{Type: domain.ConflictLoadOrder, ModA: &a, ModB: &b, Details: "last"},
	}
	sortReports(reports)

	assert.False(
		t,
		reports[0].Type != domain.ConflictFileOverlap || reports[0].Details != "alpha",
		"type should outrank details, got %+v",
		reports,
	)

	assert.Equal(
		t,
		"zeta",
		reports[1].Details,
		"equal keys should sort on details, got %+v",
		reports,
	)
}

func TestConflictReportIDIsSymmetric(t *testing.T) {
	a := domain.Mod{ID: "alpha"}
	b := domain.Mod{ID: "beta"}
	forward := domain.ConflictReport{Type: domain.ConflictFileOverlap, ModA: &a, ModB: &b}

	reverse := domain.ConflictReport{Type: domain.ConflictFileOverlap, ModA: &b, ModB: &a}
	assert.Equal(
		t,
		reverse.Identity(),
		forward.Identity(),
		"a report found with the mods swapped must keep its id: %q vs %q",
		forward.Identity(),
		reverse.Identity(),
	)

	{
		want := "file_overlap:alpha:beta"
		assert.Equal(
			t,
			want,
			forward.Identity(),
			"Identity() = %q, want %q",
			forward.Identity(),
			want,
		)
	}
}

func TestConflictReportIDHandlesAMissingSide(t *testing.T) {
	a := domain.Mod{ID: "alpha"}

	report := domain.ConflictReport{Type: domain.ConflictLoadOrder, ModA: &a}
	{
		want := "load_order:alpha:"
		assert.Equal(
			t,
			want,
			report.Identity(),
			"Identity() = %q, want %q",
			report.Identity(),
			want,
		)
	}
	// The missing side sorts last, so the id reads "load_order:alpha:".
	first, second, both := report.ModIDs()
	assert.False(
		t,
		first != "alpha" || second != "" || both,
		"ModIDs() = %q, %q, %v",
		first,
		second,
		both,
	)
}

func TestConflictReportIDCountsTypesApart(t *testing.T) {
	a := domain.Mod{ID: "alpha"}
	b := domain.Mod{ID: "beta"}
	overlap := domain.ConflictReport{Type: domain.ConflictFileOverlap, ModA: &a, ModB: &b}

	declared := domain.ConflictReport{
		Type: domain.ConflictDeclaredIncompatibility,
		ModA: &a,
		ModB: &b,
	}
	assert.NotEqual(
		t,
		declared.Identity(),
		overlap.Identity(),
		"two different conflicts between the same mods need different ids",
	)
}

func TestResolveConflictRequiresAnIDAndAResolution(t *testing.T) {
	h := newHarness(t)
	{
		err := h.svc.ResolveConflict(
			context.Background(),
			"  ",
			ResolutionKeepBoth,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "blank id: %v, want INVALID_ARGUMENT", err)
	}

	{
		err := h.svc.ResolveConflict(
			context.Background(),
			"x",
			ResolutionUnset,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "unset resolution: %v, want INVALID_ARGUMENT", err)
	}
}

func TestResolveConflictUnknownID(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))

	{
		err := h.svc.ResolveConflict(
			context.Background(),
			"file_overlap:a:b",
			ResolutionKeepBoth,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeNotFound,
		), "stale id: %v, want NOT_FOUND", err)
	}
}

func TestResolveConflictTrimsTheID(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.ConflictsWith = []string{"beta"}
	h.index.put(&a)
	h.index.putValue(conflictMod("beta"))

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	require.Len(t, reports, 1, "setup: expected one report, got %v", reportTypes(reports))

	{
		err := h.svc.ResolveConflict(
			context.Background(),
			"  "+reports[0].Identity()+"\t",
			ResolutionKeepBoth,
		)
		assert.NoError(t, err, "a padded id should still match: %v", err)
	}
}

func TestResolveConflictKeepBothDoesNothingButSaySo(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.ConflictsWith = []string{"beta"}
	h.index.put(&a)
	h.index.putValue(conflictMod("beta"))

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	before := h.index.enabledIDs()
	{
		err := h.svc.ResolveConflict(
			context.Background(),
			reports[0].Identity(),
			ResolutionKeepBoth,
		)
		require.NoError(t, err, "ResolveConflict: %v", err)
	}

	assert.True(
		t,
		equalStrings(h.index.enabledIDs(), before),
		"KeepBoth must not change any mod, before %v after %v",
		before,
		h.index.enabledIDs(),
	)

	assert.Empty(t, h.deployer.undeployCalls(), "KeepBoth must not undeploy anything")

	assert.True(
		t,
		hasEvent(h.events.types(), EventLog),
		"KeepBoth should say what it did, got %v",
		h.events.types(),
	)
}

func TestResolveConflictDisableFirstAndSecondSide(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		how    ConflictResolution
	}{
		{"disable A", "alpha", ResolutionDisableModA},
		{"disable B", "beta", ResolutionDisableModB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			a := conflictMod("alpha")
			a.ConflictsWith = []string{"beta"}
			h.index.put(&a)
			h.index.putValue(conflictMod("beta"))

			reports, err := h.svc.DetectConflicts(context.Background())
			require.NoError(t, err, "DetectConflicts")

			{
				err := h.svc.ResolveConflict(
					context.Background(),
					reports[0].Identity(),
					tc.how,
				)
				require.NoError(t, err, "ResolveConflict: %v", err)
			}

			flags := h.index.enabledMap()
			assert.False(t, flags[tc.target], "%s should be disabled, flags = %v", tc.target, flags)

			other := "beta"
			if tc.target == "beta" {
				other = "alpha"
			}

			assert.True(t, flags[other], "the other mod must be untouched, flags = %v", flags)
		})
	}
}

func TestResolveConflictUninstallsEitherSide(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		how    ConflictResolution
	}{
		{"uninstall A", "alpha", ResolutionUninstallModA},
		{"uninstall B", "beta", ResolutionUninstallModB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			a := conflictMod("alpha")
			a.ConflictsWith = []string{"beta"}
			h.index.put(&a)
			h.index.putValue(conflictMod("beta"))

			reports, err := h.svc.DetectConflicts(context.Background())
			require.NoError(t, err, "DetectConflicts")

			{
				err := h.svc.ResolveConflict(
					context.Background(),
					reports[0].Identity(),
					tc.how,
				)
				require.NoError(t, err, "ResolveConflict: %v", err)
			}

			assert.False(
				t,
				h.index.stored(tc.target),
				"%s should be gone, records = %v",
				tc.target,
				h.index.snapshot(),
			)
		})
	}
}

func TestResolveConflictRefusesOneSidedReports(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.Dependencies = []string{"ghost"}
	h.index.put(&a)

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		_, _, both := reports[0].ModIDs()
		require.False(t, both, "setup: expected a one-sided report, got %v", reportTypes(reports))
	}

	err = h.svc.ResolveConflict(context.Background(), reports[0].Identity(), ResolutionDisableModB)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"one-sided report: %v, want INVALID_ARGUMENT",
		err,
	)

	assert.True(t, h.index.enabledMap()["alpha"], "a refused resolution must not disable anything")
}

func TestResolveConflictRejectsAnUnknownResolution(t *testing.T) {
	h := newHarness(t)
	a := conflictMod("alpha")
	a.ConflictsWith = []string{"beta"}
	h.index.put(&a)
	h.index.putValue(conflictMod("beta"))

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	{
		err := h.svc.ResolveConflict(
			context.Background(),
			reports[0].Identity(),
			ConflictResolution(99),
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "unknown resolution: %v, want INVALID_ARGUMENT", err)
	}
}

func TestResolveConflictCanceledContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	{
		err := h.svc.ResolveConflict(
			ctx,
			"x",
			ResolutionKeepBoth,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeOperationCanceled,
		), "cancelled: %v, want OPERATION_CANCELED", err)
	}
}

func TestResolveConflictNilService(t *testing.T) {
	var svc *ModService
	{
		err := svc.ResolveConflict(
			context.Background(),
			"x",
			ResolutionKeepBoth,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "nil service: %v, want INVALID_ARGUMENT", err)
	}
}

func TestResolveConflictNilAndMissingIndex(t *testing.T) {
	ctx := context.Background()
	{
		err := (*ModService)(
			nil,
		).ResolveConflict(
			ctx,
			"x",
			ResolutionKeepBoth,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "nil service: %v, want INVALID_ARGUMENT", err)
	}

	{
		err := (&ModService{}).ResolveConflict(
			ctx,
			"x",
			ResolutionKeepBoth,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "no index: %v, want INVALID_ARGUMENT", err)
	}
}

func TestConflictResolutionStrings(t *testing.T) {
	cases := map[ConflictResolution]string{
		ResolutionUnset:         "unset",
		ResolutionKeepBoth:      "keep-both",
		ResolutionDisableModA:   "disable-mod-a",
		ResolutionDisableModB:   "disable-mod-b",
		ResolutionUninstallModA: "uninstall-mod-a",
		ResolutionUninstallModB: "uninstall-mod-b",
	}
	for resolution, want := range cases {
		{
			got := resolution.String()
			assert.Equal(
				t,
				want,
				got,
				"resolution %d String() = %q, want %q",
				resolution,
				got,
				want,
			)
		}
	}
}

// TestConflictInventoryUsesStoredNames guards the wiring between the deployer and
// detection: a Windows-style path must still compare on the name the game sees.
func TestConflictInventoryUsesStoredNames(t *testing.T) {
	h := newHarness(t)
	h.index.putValue(conflictMod("alpha"))
	h.index.putValue(conflictMod("beta"))
	h.index.setFiles("alpha", []domain.ModFile{{ModID: "alpha", RelPath: `textures\hud.dds`}})
	h.index.setFiles("beta", []domain.ModFile{{ModID: "beta", RelPath: "textures/logo.dds"}})

	reports, err := h.svc.DetectConflicts(context.Background())
	require.NoError(t, err, "DetectConflicts")

	assert.NotNil(
		t,
		reportFor(reports, domain.ConflictFileOverlap),
		"backslashes should fold to the same top-level entry, got %v",
		reportTypes(reports),
	)
}
