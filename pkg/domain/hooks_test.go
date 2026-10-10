package domain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func timeNow() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) }

func TestScriptLanguageValid(t *testing.T) {
	for _, lang := range []ScriptLanguage{LangLua, LangJS} {
		assert.True(t, lang.Valid(), "%q should be valid", lang)
	}

	for _, lang := range []ScriptLanguage{"", "shell", "LUA", "python", "go", "inline"} {
		assert.False(t, lang.Valid(), "%q should not be valid", lang)
	}
}

func TestParseScriptLanguage(t *testing.T) {
	for _, in := range []string{"lua", "Lua", " LUA ", "javascript", "JavaScript", "js", "JS"} {
		lang, err := ParseScriptLanguage(in)
		assert.NoError(t, err, "ParseScriptLanguage(%q)", in)

		if lang == LangJS {
			assert.Equal(t, LangJS, lang)
		} else {
			assert.Equal(t, LangLua, lang)
		}
	}

	for _, in := range []string{"", "shell", "python", "inline"} {
		_, err := ParseScriptLanguage(in)
		assert.Error(t, err, "ParseScriptLanguage(%q) must fail", in)
	}
}

func TestParseHookPhase(t *testing.T) {
	for _, p := range HookPhases() {
		got, ok := ParseHookPhase(string(p))
		if !ok || got != p {
			assert.Fail(t, "ParseHookPhase(%q) = %q,%v", p, got, ok)
		}
		// Hyphenated spelling from CLI input must be accepted.
		got, ok = ParseHookPhase(hyphenate(string(p)))
		if !ok || got != p {
			assert.Fail(t, "ParseHookPhase(%q) = %q,%v", hyphenate(string(p)), got, ok)
		}
	}

	_, ok := ParseHookPhase("before_lunch")
	assert.False(t, ok, "unknown phase must not parse")

	got, ok := ParseHookPhase("")
	if ok || got != "" {
		assert.Fail(t, "empty phase = %q,%v", got, ok)
	}
}

func hyphenate(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '_' {
			out[i] = '-'
		} else {
			out[i] = s[i]
		}
	}

	return string(out)
}

func TestHookPhasesReturnsCopy(t *testing.T) {
	first := HookPhases()
	first[0] = "mutated"

	second := HookPhases()
	assert.NotEqual(t, "mutated", second[0], "HookPhases must return a fresh slice each call")

	assert.Len(t, first, len(HookPhases()), "phase count changed")
}

func TestModHooksForCoversEveryPhase(t *testing.T) {
	// Guard against a phase constant being added without a For/Set case: fill
	// every phase with a distinct script and check all are reachable.
	var hooks ModHooks
	for i, p := range HookPhases() {
		hooks.Set(p, []HookScript{{Language: LangJS, Content: string(rune('a' + i))}})
	}

	for i, p := range HookPhases() {
		got := hooks.For(p)
		if len(got) != 1 || got[0].Content != string(rune('a'+i)) {
			assert.Fail(t, "For(%q) = %+v", p, got)
		}
	}

	assert.Equal(t, len(HookPhases()), hooks.Count(), "Count")

	assert.False(t, hooks.Empty(), "populated hooks must not report empty")
}

func TestModHooksForUnknownPhase(t *testing.T) {
	assert.Nil(t, (ModHooks{}).For("nope"), "For(unknown)")
}

func TestModHooksSetUnknownPhaseIgnored(t *testing.T) {
	h := ModHooks{}
	h.Set("nope", []HookScript{{Language: LangJS}})

	assert.True(t, h.Empty(), "unknown phase must not be stored")
}

func TestModHooksSetNilReceiver(t *testing.T) {
	var h *ModHooks
	h.Set(PhasePreEnable, nil) // must not panic
}

func TestModHooksCloneIsDeep(t *testing.T) {
	orig := ModHooks{
		PreEnable: []HookScript{{Language: LangLua, Content: "a", Env: []string{"K=V"}}},
	}
	clone := orig.Clone()
	clone.PreEnable[0].Content = "b"
	clone.PreEnable[0].Env[0] = "K=W"

	assert.Equal(t, "a", orig.PreEnable[0].Content, "clone shared hook script data with original")
	assert.Equal(t, "K=V", orig.PreEnable[0].Env[0], "clone shared hook script data with original")
}

func TestModHooksClonePreservesNil(t *testing.T) {
	clone := ModHooks{PreEnable: []HookScript{{Language: LangJS}}}.Clone()
	assert.Nil(t, clone.PostEnable, "unset phases should stay nil after clone")
}

func TestModHooksEmptyAndCount(t *testing.T) {
	var zero ModHooks
	assert.True(
		t,
		zero.Empty() && zero.Count() == 0,
		"zero hooks: empty=%v count=%d",
		zero.Empty(),
		zero.Count(),
	)

	h := ModHooks{PostDisable: []HookScript{{}, {}}, PreInstall: []HookScript{{}}}
	assert.False(t, h.Empty() || h.Count() != 3, "count = %d, empty = %v", h.Count(), h.Empty())
}

// --- SearchQuery ---

func TestSearchQueryNormalized(t *testing.T) {
	got := SearchQuery{}.Normalized()
	assert.Equal(t, 1, got.Page, "Normalized zero")
	assert.Equal(t, 20, got.PageSize, "Normalized zero")
	assert.Equal(t, SortRelevance, got.SortBy, "Normalized zero")

	got = SearchQuery{Page: -3, PageSize: 0, SortBy: SortName}.Normalized()
	assert.Equal(t, 1, got.Page, "Normalized negative page")
	assert.Equal(t, 20, got.PageSize, "Normalized negative page")
	assert.Equal(t, SortName, got.SortBy, "Normalized negative page")

	got = SearchQuery{Page: 2, PageSize: 5}.Normalized()
	assert.Equal(t, 2, got.Page, "Normalized should keep explicit values")
	assert.Equal(t, 5, got.PageSize, "Normalized should keep explicit values")
}

// --- AuthCredentials ---

func TestAuthCredentials(t *testing.T) {
	c := AuthCredentials{"api_key": "  secret  ", "empty": "   "}

	v, ok := c.Get("api_key")
	assert.True(t, ok && v == "secret", "Get = %q,%v", v, ok)

	_, ok = c.Get("empty")
	assert.False(t, ok, "blank value must count as absent")

	_, ok = c.Get("missing")
	assert.False(t, ok, "missing key must be absent")

	assert.True(t, c.Has("api_key") && !c.Has("empty"), "Has incorrect")

	var nilCreds AuthCredentials
	assert.False(t, nilCreds.Has("api_key"), "nil map must have nothing")

	_, ok = nilCreds.Get("api_key")
	assert.False(t, ok, "nil map Get must be absent")
}

// --- SearchAll ---

type stubSource struct {
	err    error
	id     string
	mods   []Mod
	search bool
}

func (s *stubSource) ID() string           { return s.id }
func (s *stubSource) Name() string         { return s.id }
func (s *stubSource) Description() string  { return "" }
func (s *stubSource) Version() string      { return "1" }
func (s *stubSource) SupportsSearch() bool { return s.search }
func (s *stubSource) SupportsAuth() bool   { return false }
func (s *stubSource) RequiresAPIKey() bool { return false }
func (s *stubSource) Search(context.Context, SearchQuery) ([]Mod, error) {
	return s.mods, s.err
}
func (s *stubSource) GetMod(context.Context, string) (*Mod, error) { return nil, nil }
func (s *stubSource) Download(context.Context, *Mod, string) (string, error) {
	return "", nil
}
func (s *stubSource) Authenticate(context.Context, AuthCredentials) error { return nil }
func (s *stubSource) ValidateCredentials(context.Context) error           { return nil }

func TestSearchAllMergesSuccessfulSources(t *testing.T) {
	sources := []ModSource{
		&stubSource{id: "a", search: true, mods: []Mod{{ID: "1"}, {ID: "2"}}},
		&stubSource{id: "b", search: true, mods: []Mod{{ID: "3"}}},
	}

	got, err := SearchAll(context.Background(), sources, SearchQuery{Query: "ui"})
	require.NoError(t, err, "SearchAll")

	assert.Len(t, got, 3, "merged mods")
}

func TestSearchAllToleratesPartialFailure(t *testing.T) {
	boom := errors.New("boom")
	sources := []ModSource{
		&stubSource{id: "ok", search: true, mods: []Mod{{ID: "1"}}},
		&stubSource{id: "bad", search: true, err: boom},
	}

	got, err := SearchAll(context.Background(), sources, SearchQuery{})
	require.NoError(t, err, "partial failure must not surface an error")

	assert.Len(t, got, 1)
	assert.Equal(t, "1", got[0].ID)
}

func TestSearchAllReturnsErrorWhenAllFail(t *testing.T) {
	boom := errors.New("boom")

	sources := []ModSource{
		&stubSource{id: "a", search: true, err: boom},
		&stubSource{id: "b", search: true, err: boom},
	}
	_, err := SearchAll(context.Background(), sources, SearchQuery{})
	require.Error(t, err, "expected error when every provider failed")
}

func TestSearchAllSkipsNonSearchingSources(t *testing.T) {
	sources := []ModSource{
		&stubSource{id: "local", search: false, err: errors.New("must not be called")},
		&stubSource{id: "vortex", search: true, mods: []Mod{{ID: "1"}}},
		nil,
	}

	got, err := SearchAll(context.Background(), sources, SearchQuery{})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestSearchAllNoSources(t *testing.T) {
	got, err := SearchAll(context.Background(), nil, SearchQuery{})
	require.NoError(t, err)
	assert.Nil(t, got, "no sources should yield nothing")
}

func TestSearchAllEmptySourceListIsNotAnError(t *testing.T) {
	sources := []ModSource{&stubSource{id: "x", search: true, mods: nil, err: nil}}

	got, err := SearchAll(context.Background(), sources, SearchQuery{})
	require.NoError(t, err)
	assert.Len(t, got, 0)
}

func TestSortMods(t *testing.T) {
	now := timeNow()
	mods := []Mod{
		{
			ID:         "b",
			Name:       "beta",
			UpdatedAt:  now,
			SourceData: map[string]any{"download_count": float64(10)},
		},
		{
			ID:         "a",
			Name:       "Alpha",
			UpdatedAt:  now.Add(-time.Hour),
			SourceData: map[string]any{"download_count": float64(99)},
		},
		{
			ID:         "c",
			Name:       "gamma",
			UpdatedAt:  now.Add(time.Hour),
			SourceData: map[string]any{"download_count": float64(50)},
		},
	}

	SortMods(mods, SortUpdated)

	assert.Equal(t, "c", mods[0].ID, "SortUpdated")

	SortMods(mods, SortName)

	assert.Equal(t, "a", mods[0].ID, "SortName")
	assert.Equal(t, "b", mods[1].ID, "SortName")

	SortMods(mods, SortDownloads)

	assert.Equal(t, "a", mods[0].ID, "SortDownloads")

	// An unknown sort key falls back to plain ID order.
	SortMods(mods, "by-vibes")

	want := []string{"a", "b", "c"}
	assert.True(t, equalStrings(ids(mods), want), "default sort = %v, want %v", ids(mods), want)
}

func TestSortModsIsStableOnTies(t *testing.T) {
	mods := []Mod{
		{ID: "z", Name: "same", UpdatedAt: timeNow()},
		{ID: "a", Name: "same", UpdatedAt: timeNow()},
	}
	SortMods(mods, SortName)

	assert.Equal(t, "a", mods[0].ID, "tie should break by ID")
	assert.Equal(t, "z", mods[1].ID, "tie should break by ID")
}

func TestSortModsHandlesUnknownDownloadTypes(t *testing.T) {
	mods := []Mod{
		{ID: "a", SourceData: map[string]any{"download_count": "lots"}},
		{ID: "b", SourceData: map[string]any{"download_count": int64(5)}},
		{ID: "c", SourceData: nil},
	}
	SortMods(mods, SortDownloads)

	assert.Equal(t, "b", mods[0].ID, "unknown counts must sort as zero")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func ids(mods []Mod) []string {
	out := make([]string, len(mods))
	for i, m := range mods {
		out[i] = m.ID
	}

	return out
}
