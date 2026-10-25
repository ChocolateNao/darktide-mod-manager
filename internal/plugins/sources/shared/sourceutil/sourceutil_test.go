package sourceutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaginateAlwaysReturnsASlice(t *testing.T) {
	// An empty result set is the common case for a search that matched nothing, and
	// a nil range silently skips the loop instead of reporting "no results".
	got := Paginate(nil, 1, 20)
	if got == nil {
		require.FailNow(t, "Paginate(nil) returned nil; callers cannot tell it from a bug")
	}

	assert.Empty(t, got, "Paginate(nil) returned %d mods, want 0", len(got))
}

func TestPaginatePageAndSizeBounds(t *testing.T) {
	mods := []domain.Mod{
		{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"},
	}

	got := Paginate(mods, 2, 2)
	if len(got) != 2 || got[0].ID != "c" || got[1].ID != "d" {
		assert.Fail(t, "page 2 size 2 = %v, want [c d]", modIDs(got))
	}

	// The last page is allowed to be short.
	got = Paginate(mods, 3, 2)
	if len(got) != 1 || got[0].ID != "e" {
		assert.Fail(t, "page 3 size 2 = %v, want [e]", modIDs(got))
	}

	// A page size larger than the result set returns everything, once.
	got = Paginate(mods, 1, 500)
	assert.Len(t, got, 5, "oversized page returned %d mods, want all 5", len(got))

	// Past the end: an empty slice, not a panic and not a partial page.
	got = Paginate(mods, 99, 2)
	if got == nil || len(got) != 0 {
		assert.Fail(t, "page 99 = %v, want empty non-nil", modIDs(got))
	}
}

func TestPaginateRepairsOutOfRangePageNumbers(t *testing.T) {
	mods := []domain.Mod{{ID: "a"}, {ID: "b"}}

	// Page 0 or negative would otherwise compute a negative start index.
	for _, page := range []int{0, -1, -100} {
		got := Paginate(mods, page, 1)
		if len(got) != 1 || got[0].ID != "a" {
			assert.Fail(t, "Paginate(page=%d) = %v, want [a]", page, modIDs(got))
		}
	}

	// A page size of zero would divide the set into nothing; it falls back to the
	// documented default instead of returning an empty page.
	got := Paginate(mods, 1, 0)
	assert.Len(t, got, 2, "Paginate(pageSize=0) returned %d mods, want both", len(got))
}

func TestPaginateDoesNotAliasItsInput(t *testing.T) {
	mods := []domain.Mod{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := Paginate(mods, 1, 3)

	got[0].ID = "mutated"
	if mods[0].ID != "a" {
		assert.Fail(t, "Paginate returned a slice aliasing the caller's mods")
	}
}

func TestMatchesQueryTextIsCaseInsensitiveAcrossFields(t *testing.T) {
	m := domain.Mod{
		ID:          "better_ui",
		Name:        "Better UI",
		Description: "Scales the HUD",
		Author:      "somebody",
	}

	// Every field is searched, because a user types one term and does not know
	// which field it landed in.
	for _, needle := range []string{"better", "BETTER_", "hud", "scales", "somebody", "ui"} {
		if !MatchesQuery(m, domain.SearchQuery{Query: needle}) {
			assert.Fail(t, "query %q did not match %+v", needle, m)
		}
	}

	if MatchesQuery(m, domain.SearchQuery{Query: "minimap"}) {
		assert.Fail(t, "query for an absent word matched")
	}

	// Surrounding whitespace in the query is a typo, not a stricter filter.
	if !MatchesQuery(m, domain.SearchQuery{Query: "  better  "}) {
		assert.Fail(t, "a whitespace-padded query should match")
	}
}

func TestMatchesQueryEmptyQueryMatchesEverything(t *testing.T) {
	m := domain.Mod{ID: "anything"}
	for _, q := range []domain.SearchQuery{
		{},
		{Query: "   "},
		{Tags: nil},
		{GameVersion: ""},
	} {
		assert.True(t, MatchesQuery(m, q), "MatchesQuery(%+v) = false for an unrestricted query", q)
	}
}

func TestMatchesQueryTagsAreAnded(t *testing.T) {
	m := domain.Mod{ID: "a", Tags: []string{"ui", "hd"}}

	if !MatchesQuery(m, domain.SearchQuery{Tags: []string{"ui"}}) {
		assert.Fail(t, "single matching tag should pass")
	}

	if !MatchesQuery(m, domain.SearchQuery{Tags: []string{"ui", "hd"}}) {
		assert.Fail(t, "both matching tags should pass")
	}
	// Listing two tags narrows the search. OR would silently widen it and return
	// mods the user did not ask for.
	if MatchesQuery(m, domain.SearchQuery{Tags: []string{"ui", "audio"}}) {
		assert.Fail(t, "one missing tag should fail even though the other matches")
	}

	// A blank tag in the list is ignored: the UI
	// sends an empty filter box as an empty tag.
	if !MatchesQuery(m, domain.SearchQuery{Tags: []string{"", "  ", "ui"}}) {
		assert.Fail(t, "blank tags should be skipped, not treated as required")
	}
}

func TestMatchesQueryGameVersionOnlyAppliesWhenTheModDeclaresOne(t *testing.T) {
	// A mod that does not declare a game version cannot violate the
	// constraint, so a version query must not filter it out.
	if !MatchesQuery(
		domain.Mod{ID: "a", Version: "1.0.0"},
		domain.SearchQuery{GameVersion: ">=2.0.0"},
	) {
		assert.Fail(t, "a mod with no declared game version was filtered out by a version query")
	}

	if !MatchesQuery(domain.Mod{ID: "a", Version: "1.0.0", GameVersion: "  "},
		domain.SearchQuery{GameVersion: ">=2.0.0"}) {
		assert.Fail(t, "a blank declared game version was treated as a constraint")
	}

	if !MatchesQuery(domain.Mod{ID: "a", Version: "1.0.0", GameVersion: ">=1.0.0"},
		domain.SearchQuery{GameVersion: ">=1.0.0"}) {
		assert.Fail(t, "a satisfied game version constraint was rejected")
	}

	if MatchesQuery(domain.Mod{ID: "a", Version: "1.0.0", GameVersion: ">=2.0.0"},
		domain.SearchQuery{GameVersion: ">=1.0.0"}) {
		assert.Fail(t, "an unsatisfied game version constraint was accepted")
	}
}

func TestSelectModsFiltersThenSortsThenPages(t *testing.T) {
	mods := []domain.Mod{
		{ID: "zebra", Name: "Zebra UI", SourceData: map[string]any{"download_count": 5}},
		{ID: "alpha", Name: "Alpha UI"},
		{ID: "mid", Name: "Middle"},
	}

	// Sorting happens after filtering, so a match that sorts first on page 1 is
	// really first; if sorting ran before filtering, page boundaries would move.
	got := SelectMods(
		mods,
		domain.SearchQuery{Query: "ui", SortBy: domain.SortName, Page: 1, PageSize: 2},
	)
	require.Len(t, got, 2, "SelectMods returned %d mods, want 2: %v", len(got), modIDs(got))

	if got[0].ID != "alpha" || got[1].ID != "zebra" {
		assert.Fail(t, "SelectMods = %v, want [alpha zebra]", modIDs(got))
	}

	got = SelectMods(mods, domain.SearchQuery{Query: "nothing matches"})
	if got == nil || len(got) != 0 {
		assert.Fail(t, "SelectMods with no matches = %v, want empty non-nil", modIDs(got))
	}

	// A zero-valued query still returns everything, sorted by relevance, because
	// that is what a plain browse call sends.
	got = SelectMods(mods, domain.SearchQuery{})
	assert.Len(t, got, 3, "an unrestricted SelectMods returned %d of 3 mods", len(got))
}

func TestSelectModsDoesNotReorderTheCallersSlice(t *testing.T) {
	mods := []domain.Mod{{ID: "zebra"}, {ID: "alpha"}}
	SelectMods(mods, domain.SearchQuery{SortBy: domain.SortName, Page: 1, PageSize: 10})

	if mods[0].ID != "zebra" {
		assert.Fail(t, "SelectMods sorted the caller's slice in place")
	}
}

func TestSummarizeNamesAtMostThreeAndCountsTheRest(t *testing.T) {
	// Forty failing repositories must not bury the useful part under a wall of
	// near-identical text, but the user still has to learn that 37 went unsaid.
	five := []error{
		appErrors.New(appErrors.ErrCodeNotFound, "first"),
		appErrors.New(appErrors.ErrCodeNotFound, "second"),
		appErrors.New(appErrors.ErrCodeNotFound, "third"),
		appErrors.New(appErrors.ErrCodeNotFound, "fourth"),
		appErrors.New(appErrors.ErrCodeNotFound, "fifth"),
	}

	got := Summarize(five)
	for _, want := range []string{"first", "second", "third", "and 2 more"} {
		assert.Contains(t, got, want, "Summarize(%v) = %q, missing %q", messages(five), got, want)
	}

	for _, unwanted := range []string{"fourth", "fifth"} {
		assert.NotContains(
			t,
			got,
			unwanted,
			"Summarize named %q beyond the first three: %q",
			unwanted,
			got,
		)
	}

	// Exactly three is not truncated; there is no "and 0 more".
	three := five[:3]

	got = Summarize(three)
	assert.NotContains(t, got, "more", "Summarize of exactly three errors said \"more\": %q", got)

	for _, want := range []string{"first", "second", "third"} {
		assert.Contains(t, got, want, "Summarize(%v) = %q, missing %q", messages(three), got, want)
	}

	one := []error{appErrors.New(appErrors.ErrCodeNotFound, "only")}
	got = Summarize(one)
	assert.Equal(t, "only", got, "Summarize of one error = %q, want %q", got, "only")
}

func TestSummarizeSkipsNilAndEmpty(t *testing.T) {
	// A nil entry is what a loop over optional results produces when a provider
	// returned no error; it must not become a blank segment in the message.
	got := Summarize([]error{nil, nil})
	assert.Equal(t, "no detail", got, "Summarize(nil, nil) = %q, want %q", got, "no detail")

	got = Summarize(nil)
	assert.Equal(t, "no detail", got, "Summarize(nil slice) = %q, want %q", got, "no detail")

	mixed := []error{
		nil,
		appErrors.New(appErrors.ErrCodeNotFound, "real"),
		nil,
	}
	got = Summarize(mixed)
	assert.Equal(t, "real", got, "Summarize with nil gaps = %q, want %q", got, "real")
}

func TestSummarizeDistinguishesNoProblemFromUnnameableProblem(t *testing.T) {
	// A nil entry is "this provider was fine" and must not appear anywhere; a
	// non-nil error with an empty message stays in the count.
	bare := errors.New("")
	four := []error{
		appErrors.New(appErrors.ErrCodeNotFound, "one"),
		appErrors.New(appErrors.ErrCodeNotFound, "two"),
		appErrors.New(appErrors.ErrCodeNotFound, "three"),
		bare,
	}

	got := Summarize(four)
	assert.Contains(
		t,
		got,
		"and 1 more",
		"Summarize = %q, want the unnameable error counted as \"and 1 more\"",
		got,
	)

	assert.Equal(
		t,
		"one; two; three; and 1 more",
		got,
		"Summarize = %q, want %q",
		got,
		"one; two; three; and 1 more",
	)

	// The same error count with a nil in place of the bare error must drop it.
	withNil := []error{four[0], four[1], four[2], nil}
	got = Summarize(withNil)
	assert.NotContains(t, got, "more", "Summarize counted a nil as a problem: %q", got)
}

func TestFirstNonBlankReturnsTheOriginalSpelling(t *testing.T) {
	// Values become identifiers and display names. Trimming here would silently
	// change what the user sees and what ends up in a payload folder name.
	got := FirstNonBlank("  ", "  padded name  ", "third")
	assert.Equal(
		t,
		"  padded name  ",
		got,
		"FirstNonBlank = %q, want the untrimmed %q",
		got,
		"  padded name  ",
	)

	got = FirstNonBlank("", "   ", "\t\n")
	assert.Equal(t, "", got, "FirstNonBlank of all blanks = %q, want empty", got)

	got = FirstNonBlank()
	assert.Equal(t, "", got, "FirstNonBlank() = %q, want empty", got)

	got = FirstNonBlank("only")
	assert.Equal(t, "only", got, "FirstNonBlank(one) = %q, want %q", got, "only")
}

func TestSplitListSeparatorsAndBlanks(t *testing.T) {
	got := SplitList("a, b\nc\r\nd,,   ,e")

	want := []string{"a", "b", "c", "d", "e"}
	require.Len(t, got, len(want), "SplitList = %v, want %v", got, want)

	for i := range want {
		assert.Equal(t, want[i], got[i], "SplitList[%d] = %q, want %q", i, got[i], want[i])
	}

	// A trailing comma is a typo in a hand-edited config, not an empty path that
	// would become a scan of the current directory.
	if got := SplitList("a,"); len(got) != 1 || got[0] != "a" {
		assert.Fail(t, "SplitList(\"a,\") = %v, want [a]", got)
	}

	if got := SplitList(""); got == nil {
		assert.Fail(t, "SplitList(\"\") returned nil")
	} else if len(got) != 0 {
		assert.Fail(t, "SplitList(\"\") = %v, want empty", got)
	}

	got = SplitList("   ,  ")
	assert.Empty(t, got, "SplitList of only separators = %v, want empty", got)
}

func TestOptionListAcceptsEveryJSONShape(t *testing.T) {
	// The same option arrives as a string when hand-written and as []any when it
	// round-trips through the JSON config, so both spellings have to work.
	want := []string{"owner/one", "owner/two"}

	got := OptionList("owner/one, owner/two")
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		assert.Fail(t, "OptionList(string) = %v, want %v", got, want)
	}

	got = OptionList([]any{"owner/one", "owner/two"})
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		assert.Fail(t, "OptionList([]any) = %v, want %v", got, want)
	}

	got = OptionList([]string{"owner/one", "owner/two"})
	assert.Len(t, got, 2, "OptionList([]string) = %v, want %v", got, want)

	// A []any holding something that is not a string must not panic on a type
	// assertion; the unusable entries are dropped.
	got = OptionList([]any{"owner/one", 42, nil, "owner/two"})
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		assert.Fail(t, "OptionList with mixed types = %v, want %v", got, want)
	}

	if OptionList(nil) != nil {
		assert.Fail(t, "OptionList(nil) should be nil")
	}

	if OptionList(42) != nil {
		assert.Fail(t, "OptionList of an unsupported type should be nil")
	}
}

func TestStringOfSurvivesAJSONRoundTrip(t *testing.T) {
	// A value written by a plugin comes back as a string; one written by hand as a
	// JSON number or boolean comes back as float64/bool. Neither may crash a caller.
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"nil", nil, ""},
		{"int", 42, "42"},
		{"float", 1.5, "1.5"},
		{"bool", true, "true"},
		{"stringer", stringerValue{"x"}, "stringer:x"},
	}
	for _, c := range cases {
		got := StringOf(c.in)
		assert.Equal(t, c.want, got, "StringOf(%s) = %q, want %q", c.name, got, c.want)
	}
}

type stringerValue struct{ s string }

func (v stringerValue) String() string { return "stringer:" + v.s }

func TestEnsureDirIsANoopForTheCurrentDirectory(t *testing.T) {
	// filepath.Dir("file.zip") is "."; creating "." would be a wasted syscall and
	// on some sandboxes an outright failure.
	for _, dir := range []string{"", ".", "  "} {
		err := EnsureDir(dir)
		assert.NoError(t, err, "EnsureDir(%q) = %v, want nil", dir, err)
	}
}

func TestEnsureDirCreatesAndReportsFailures(t *testing.T) {
	root := t.TempDir()

	nested := filepath.Join(root, "a", "b", "c")
	err := EnsureDir(nested)
	require.NoError(t, err, "EnsureDir: %v", err)

	info, err := os.Stat(nested)
	require.NoError(t, err, "stat created dir")

	if !info.IsDir() {
		assert.Fail(t, "EnsureDir created something that is not a directory")
	}

	// A file where a directory is needed is the realistic failure.
	blocker := filepath.Join(root, "file")
	err = os.WriteFile(blocker, []byte("x"), 0o644)
	require.NoError(t, err, "write blocker: %v", err)

	err = EnsureDir(filepath.Join(blocker, "child"))
	if err == nil {
		require.FailNow(t, "EnsureDir under a regular file returned nil")
	}

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeConfigIO),
		"EnsureDir failure code = %v, want CONFIG_IO (%v)",
		appErrors.CodeOf(err),
		err,
	)

	// An existing directory is not an error, but an existing *file* is: the caller
	// asked for a directory, so reporting success defers a much vaguer failure.
	err = EnsureDir(filepath.Join(root, "a"))
	assert.NoError(t, err, "EnsureDir over an existing directory = %v, want nil", err)

	err = EnsureDir(blocker)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeConfigIO),
		"EnsureDir over an existing file code = %v, want CONFIG_IO (%v)",
		appErrors.CodeOf(err),
		err,
	)
}

func TestFileSHA256MissingFileIsACacheMiss(t *testing.T) {
	// The cache manager branches on CACHE_MISS to decide it should download; a
	// generic I/O error would make it treat a vanished blob as a fatal failure.
	_, err := FileSHA256(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		require.FailNow(t, "FileSHA256 on a missing file returned nil")
	}

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"code = %v, want CACHE_MISS (%v)",
		appErrors.CodeOf(err),
		err,
	)
}

func TestFileSHA256BlankPathIsAnInvalidArgument(t *testing.T) {
	for _, path := range []string{"", "   "} {
		_, err := FileSHA256(path)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
			"FileSHA256(%q) code = %v, want INVALID_ARGUMENT",
			path,
			appErrors.CodeOf(err),
		)
	}
}

func TestFileSHA256HashesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.bin")
	err := os.WriteFile(path, []byte("abc"), 0o644)
	require.NoError(t, err, "write: %v", err)
	// sha256("abc") is a fixed, well-known value; asserting it here pins that the
	// function hashes the content and not, say, the path.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

	got, err := FileSHA256(path)
	require.NoError(t, err, "FileSHA256")

	assert.Equal(t, want, got, "FileSHA256 = %q, want %q", got, want)

	// The hash has to change when the content does, or Verify would pass on a
	// corrupted download.
	err = os.WriteFile(path, []byte("abd"), 0o644)
	require.NoError(t, err, "rewrite: %v", err)

	got, err = FileSHA256(path)
	require.NoError(t, err, "FileSHA256 after rewrite")

	if got == want {
		assert.Fail(t, "FileSHA256 returned the old digest after the content changed")
	}
}

func TestFileSHA256ReportsADirectoryAsAnIOError(t *testing.T) {
	// A directory exists, so it is not a cache miss, and hashing it would fail
	// with a confusing read error from deep inside the hasher.
	dir := t.TempDir()

	_, err := FileSHA256(dir)
	if err == nil {
		require.FailNow(t, "FileSHA256 on a directory returned nil")
	}

	assert.False(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeCacheMiss),
		"a directory was reported as a cache miss: %v",
		err,
	)
}

func TestModLabelNeverDereferencesNil(t *testing.T) {
	// A nil mod reaches here when a download fails after the source lost track of
	// which mod it was fetching; dereferencing it turns a reportable error into a panic.
	got := ModLabel(nil)
	assert.Equal(t, "<unknown>", got, "ModLabel(nil) = %q, want %q", got, "<unknown>")

	if got := ModLabel(&domain.Mod{}); got != "<unknown>" {
		assert.Fail(t, "ModLabel(empty) = %q, want %q", got, "<unknown>")
	}

	if got := ModLabel(&domain.Mod{ID: "  "}); got != "<unknown>" {
		assert.Fail(t, "ModLabel(blank) = %q, want %q", got, "<unknown>")
	}

	if got := ModLabel(&domain.Mod{ID: "better_ui"}); got != "better_ui" {
		assert.Fail(t, "ModLabel = %q, want %q", got, "better_ui")
	}
}

func TestCredValueAcceptsEverySpelling(t *testing.T) {
	// The same config file is written by hand and by other tooling, so both
	// snake_case and camelCase spellings have to resolve.
	creds := domain.AuthCredentials{"apiKey": "camel", "api_key": "snake"}
	got := CredValue(creds, "token", "api_key", "apiKey")
	assert.Equal(
		t,
		"snake",
		got,
		"CredValue = %q, want the first listed spelling that exists (snake)",
		got,
	)

	if got := CredValue(
		domain.AuthCredentials{"apiKey": "camel"},
		"token",
		"apiKey",
	); got != "camel" {
		assert.Fail(t, "CredValue = %q, want camel", got)
	}

	got = CredValue(nil, "token")
	assert.Equal(t, "", got, "CredValue(nil creds) = %q, want empty", got)
}

func TestCredValueIgnoresBlankValues(t *testing.T) {
	// An empty API key in the config means "not configured"; returning it would make
	// Authenticate succeed with no token and every later request fail with a 401.
	creds := domain.AuthCredentials{"token": "   ", "api_key": "real"}
	got := CredValue(creds, "token", "api_key")
	assert.Equal(t, "real", got, "CredValue = %q, want real", got)

	got = CredValue(creds, "token")
	assert.Equal(t, "", got, "CredValue of a blank token = %q, want empty", got)

	got = CredValue(creds)
	assert.Equal(t, "", got, "CredValue with no keys = %q, want empty", got)
}

func modIDs(mods []domain.Mod) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, m.ID)
	}

	return out
}

func messages(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, appErrors.Message(e))
	}

	return out
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
