package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModCloneIsDeep(t *testing.T) {
	orig := &Mod{
		ID:            "a",
		Tags:          []string{"ui"},
		Dependencies:  []string{"b"},
		ConflictsWith: []string{"c"},
		SourceData:    map[string]any{"k": "v"},
		Hooks: ModHooks{
			PreEnable: []HookScript{{Language: LangJS, Content: "x", Env: []string{"A=1"}}},
		},
	}
	clone := orig.Clone()

	clone.Tags[0] = "changed"
	clone.Dependencies[0] = "changed"
	clone.ConflictsWith[0] = "changed"
	clone.SourceData["k"] = "changed"
	clone.Hooks.PreEnable[0].Content = "changed"
	clone.Hooks.PreEnable[0].Env[0] = "changed"

	assert.Equal(t, "ui", orig.Tags[0], "slices shared with clone")
	assert.Equal(t, "b", orig.Dependencies[0], "slices shared with clone")
	assert.Equal(t, "c", orig.ConflictsWith[0], "slices shared with clone")

	assert.Equal(t, "v", orig.SourceData["k"], "SourceData map shared with clone")

	assert.Equal(t, "x", orig.Hooks.PreEnable[0].Content, "hooks shared with clone")
	assert.Equal(t, "A=1", orig.Hooks.PreEnable[0].Env[0], "hooks shared with clone")
}

func TestModCloneNil(t *testing.T) {
	var m *Mod
	assert.Nil(t, m.Clone(), "nil clone should stay nil")
}

func TestModClonePreservesNilSlices(t *testing.T) {
	clone := (&Mod{}).Clone()
	assert.Nil(t, clone.Tags, "nil fields should stay nil so JSON omits them")
	assert.Nil(t, clone.SourceData, "nil fields should stay nil so JSON omits them")

	assert.True(t, clone.Hooks.Empty(), "zero hooks should be empty")
}

func TestModLookups(t *testing.T) {
	m := &Mod{Dependencies: []string{"a", "b"}, ConflictsWith: []string{"x"}, Tags: []string{"UI"}}

	assert.True(
		t,
		m.HasDependency("b") && m.HasDependency("a"),
		"HasDependency should find existing deps",
	)

	assert.False(
		t,
		m.HasDependency("") || m.HasDependency("A"),
		"HasDependency must be exact and not match empty",
	)

	assert.True(t, m.ConflictsWithMod("x") && !m.ConflictsWithMod(""), "ConflictsWithMod incorrect")

	assert.True(t, m.HasTag("ui") && m.HasTag("UI"), "HasTag should be case-insensitive")

	assert.False(t, m.HasTag(""), "HasTag should not match empty string")
}

func TestModJSONRoundTrip(t *testing.T) {
	in := Mod{
		ID:           "better-ui",
		Name:         "Better UI",
		Version:      "1.2.0",
		SourceID:     "vortex",
		Enabled:      true,
		InstalledAt:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Tags:         []string{"ui"},
		Dependencies: []string{"core"},
		Hooks: ModHooks{
			PostEnable: []HookScript{{Language: LangLua, Content: "echo hi", Timeout: 5}},
		},
	}

	data, err := json.Marshal(in)
	require.NoError(t, err, "marshal")

	var out Mod

	err = json.Unmarshal(data, &out)
	require.NoError(t, err, "unmarshal")

	assert.Equal(t, in.ID, out.ID, "round trip mismatch")
	assert.True(t, out.Enabled, "round trip mismatch")
	assert.Equal(t, in.Version, out.Version, "round trip mismatch")

	assert.True(t, out.InstalledAt.Equal(in.InstalledAt), "timestamp mismatch")

	assert.Len(t, out.Hooks.PostEnable, 1)
	assert.Equal(t, 5, out.Hooks.PostEnable[0].Timeout, "hooks lost in round trip")
}

// --- paths ---

func TestCleanPath(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"/a/b/../c": "/a/c",
		"/a//b":     "/a/b",
		"/a/b/":     "/a/b",
		// Windows separators are folded even when running on Unix, so a config
		// written on Windows still compares equal on Linux.
		`C:\Games\Darktide`:     "C:/Games/Darktide",
		`C:\Games\\Darktide\`:   "C:/Games/Darktide",
		`C:/Games/Darktide`:     "C:/Games/Darktide",
		"./mods/../mods/x.json": "mods/x.json",
		"/":                     "/",
	}
	for in, want := range cases {
		assert.Equal(t, want, CleanPath(in), "CleanPath(%q)", in)
	}
}

func TestCleanPathCollapsesParentEscape(t *testing.T) {
	assert.Equal(t, "/", CleanPath("/a/b/../../../.."), "CleanPath escape")
}

func TestJoinPath(t *testing.T) {
	assert.Equal(t, "/a/b/d", JoinPath("/a", "", "b", "c/../d"), "JoinPath")

	assert.Equal(t, "", JoinPath(), "JoinPath()")

	assert.Equal(t, "", JoinPath("", ""), "JoinPath empty")

	assert.Equal(t, "/a/b", JoinPath("/a", "b/"), "JoinPath trailing")
}

func TestPathsEqual(t *testing.T) {
	assert.True(
		t,
		PathsEqual("/a/B/", "/a/b"),
		"case and trailing separator differences should be ignored",
	)

	assert.False(t, PathsEqual("/a/b", "/a/c"), "different paths must not be equal")

	assert.False(t, PathsEqual("", "/a"), "empty must not equal non-empty")

	assert.True(t, PathsEqual("", ""), "empty should equal empty")

	assert.False(t, StrictPathsEqual("/a/B", "/a/b"), "strict compare must be case sensitive")
}

func TestPathWithin(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"/a", "/a", true},
		{"/a", "/a/b", true},
		{"/a/b", "/a", false},
		{"/a", "/ab", false},
		{"/a/", "/a/b/c", true},
		{"/A", "/a/B", true},
		{"", "/a", false},
		{"/a", "", false},
		{"", "", false},
		{"/", "/a", true},
	}
	for _, tc := range cases {
		assert.Equal(
			t,
			tc.want,
			PathWithin(tc.parent, tc.child),
			"PathWithin(%q, %q)",
			tc.parent,
			tc.child,
		)
	}
}

func TestSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"better-ui":    "better-ui",
		"Better_UI":    "Better_UI",
		"1.2.3":        "1.2.3",
		"../../etc":    "etc",
		"..":           "_",
		".":            "_",
		"":             "_",
		"   ":          "_",
		"a/b\\c":       "abc",
		`C:\evil\path`: "Cevilpath",
		"mod:name*?":   "modname",
		"  spaced  ":   "spaced",
		"trailing.":    "trailing",
		".leading":     "leading",
	}
	for in, want := range cases {
		assert.Equal(t, want, SanitizeSegment(in), "SanitizeSegment(%q)", in)
	}
}

func TestSanitizeSegmentNeverEscapes(t *testing.T) {
	// Every sanitised segment must be a single relative component.
	for _, in := range []string{"..", "../..", "a/../../b", `\..\..`, "....//...."} {
		got := SanitizeSegment(in)
		assert.NotEqual(t, "", got, "SanitizeSegment(%q) = %q", in, got)
		assert.NotEqual(t, ".", got, "SanitizeSegment(%q) = %q", in, got)
		assert.NotEqual(t, "..", got, "SanitizeSegment(%q) = %q", in, got)

		for _, r := range got {
			assert.NotContains(
				t,
				string(r),
				"/",
				"SanitizeSegment(%q) = %q still contains a separator",
				in,
				got,
			)
			assert.NotContains(
				t,
				string(r),
				"\\",
				"SanitizeSegment(%q) = %q still contains a separator",
				in,
				got,
			)
		}
	}
}

func TestModStoragePathSanitizesEachSegment(t *testing.T) {
	got := ModStoragePath("/data", "darktide", "../evil", "1.0/../../etc")
	assert.True(
		t,
		PathWithin("/data/darktide", got),
		"ModStoragePath escaped storage root: %q",
		got,
	)
	// Separators are dropped but the leftover dot runs stay inside one segment,
	// which is the property that matters: the result cannot traverse upwards.
	want := "/data/darktide/evil/1.0....etc"
	assert.Equal(t, want, got, "ModStoragePath")
}

func TestModStoragePathEmptySegments(t *testing.T) {
	got := ModStoragePath("/data", "darktide", "", "")
	assert.Equal(t, "/data/darktide/_/_", got, "ModStoragePath empty")
}

func TestEqualFoldUnicode(t *testing.T) {
	assert.True(t, EqualFold("STRASSE", "strasse"), "EqualFold should fold case")

	assert.False(t, EqualFold("a", "b"), "different strings must not fold equal")
}
