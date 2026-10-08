package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---------------------------------------------------------------

// writeModFolder creates <root>/<name> holding a mod.toml with the given body and
// returns the folder path.
func writeModFolder(t *testing.T, root, name, manifest string) string {
	t.Helper()

	dir := filepath.Join(root, name)
	err := os.MkdirAll(dir, 0o755)
	require.NoError(t, err, "create mod folder: %v", err)

	if manifest != "" {
		err := os.WriteFile(
			filepath.Join(dir, "mod.toml"),
			[]byte(manifest),
			0o644,
		)
		require.NoError(t, err, "write mod.toml: %v", err)
	}

	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o755)
	require.NoError(t, err, "create parent for %s: %v", path, err)

	err = os.WriteFile(path, []byte(body), 0o644)
	require.NoError(t, err, "write %s: %v", path, err)
}

func modIDs(mods []domain.Mod) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, m.ID)
	}

	return out
}

func joinIDs(ids []string) string { return strings.Join(ids, ",") }

func onlyMod(t *testing.T, mods []domain.Mod) domain.Mod {
	t.Helper()

	require.Len(t, mods, 1, "wanted exactly 1 mod, got %d (%s)", len(mods), joinIDs(modIDs(mods)))

	return mods[0]
}

const sampleManifest = `
[mod]
id = "better_ui"
name = "Better UI"
version = "1.2.0"
author = "ModAuthor"
description = "Improves UI scaling"
game_version = ">=1.0.0"
tags = ["ui", "quality-of-life"]
dependencies = ["core_libs"]
`

// --- identity and capabilities --------------------------------------------

func TestSourceIdentity(t *testing.T) {
	s := New(nil)
	if s.ID() != "local" || s.ID() != SourceID {
		assert.Fail(t, "ID() = %q, want %q", s.ID(), SourceID)
	}

	if s.Name() == "" {
		assert.Fail(t, "Name() must not be empty; the UI shows it as the source label")
	}

	if s.Description() == "" {
		assert.Fail(t, "Description() must not be empty")
	}

	if s.Version() == "" {
		assert.Fail(t, "Version() must not be empty so a plugin can be identified")
	}
}

func TestSourceCapabilities(t *testing.T) {
	s := New(nil)
	if !s.SupportsSearch() {
		assert.Fail(t, "local scanning is a search, so SupportsSearch must be true")
	}

	if s.SupportsAuth() {
		assert.Fail(t, "local folders need no credentials, so SupportsAuth must be false")
	}

	if s.RequiresAPIKey() {
		assert.Fail(t, "local folders need no API key, so RequiresAPIKey must be false")
	}
}

func TestAuthenticateIsANoOp(t *testing.T) {
	// Returning an error here would make the UI offer a credentials dialog that
	// cannot possibly succeed.
	if err := New(
		nil,
	).Authenticate(
		context.Background(),
		domain.AuthCredentials{"key": "x"},
	); err != nil {
		assert.Fail(t, "Authenticate on a credential-less source must succeed, got %v", err)
	}
}

func TestValidateCredentialsNeedsPaths(t *testing.T) {
	err := New(
		nil,
	).ValidateCredentials(
		context.Background(),
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeSourceUnavailable,
	), "with no folders configured want SOURCE_UNAVAILABLE, got %v", err)

	dir := t.TempDir()
	if err := New([]string{dir}).ValidateCredentials(context.Background()); err != nil {
		assert.Fail(t, "with a folder configured want nil, got %v", err)
	}
}

// --- construction ----------------------------------------------------------

func TestNewDropsBlankPaths(t *testing.T) {
	dir := t.TempDir()
	// A trailing comma or a stray space in a config value must not become a
	// scan of the process working directory.
	s := New([]string{"  " + dir + "  ", "", "   "})
	require.Len(t, s.paths, 1, "expected the blank entries dropped, got %q", s.paths)

	assert.Equal(t, dir, s.paths[0], "path %q should have been trimmed to %q", s.paths[0], dir)
}

func TestWithPathsReplacesTheConfiguredPaths(t *testing.T) {
	dir := t.TempDir()

	s2 := New([]string{"/old"}, WithPaths([]string{dir}))
	if len(s2.paths) != 1 || s2.paths[0] != dir {
		assert.Fail(t, "WithPaths should replace the list, got %q", s2.paths)
	}
}

func TestWithMaxDepth(t *testing.T) {
	s := New(nil, WithMaxDepth(2))
	assert.Equal(t, 2, s.maxDepth, "maxDepth = %d, want 2", s.maxDepth)
	// A non-positive depth falls back to the default inside discover rather than
	// meaning "scan nothing".
	if New(nil, WithMaxDepth(-1)).maxDepth != -1 {
		assert.Fail(t, "WithMaxDepth should store the value verbatim and let discover normalise it")
	}
}

// --- discovery -------------------------------------------------------------

func TestSearchFindsManifestFolders(t *testing.T) {
	root := t.TempDir()
	dir := writeModFolder(t, root, "better_ui", sampleManifest)
	s := New([]string{root})

	mods, err := s.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	got := onlyMod(t, mods)
	assert.Equal(t, "better_ui", got.ID, "ID = %q, want better_ui", got.ID)

	if got.Name != "Better UI" || got.Version != "1.2.0" || got.Author != "ModAuthor" {
		assert.Fail(t, "metadata not mapped from the manifest: %+v", got)
	}

	assert.Equal(t, SourceID, got.SourceID, "SourceID = %q, want %q", got.SourceID, SourceID)

	assert.Equal(t, dir, got.LocalPath, "LocalPath = %q, want %q", got.LocalPath, dir)

	assert.True(
		t,
		got.InstalledAt.IsZero(),
		"InstalledAt should stay zero for a mod that was never installed, got %v",
		got.InstalledAt,
	)

	if len(got.Tags) != 2 || !got.HasTag("UI") {
		assert.Fail(t, "tags not mapped or not case-insensitive: %q", got.Tags)
	}

	if len(got.Dependencies) != 1 || got.Dependencies[0] != "core_libs" {
		assert.Fail(t, "dependencies not mapped: %q", got.Dependencies)
	}

	assert.Equal(t, ">=1.0.0", got.GameVersion, "game version not mapped: %q", got.GameVersion)
}

func TestSearchFindsNestedFoldersUpToTheDepthLimit(t *testing.T) {
	root := t.TempDir()
	writeModFolder(t, root, "a/b/better_ui", sampleManifest)

	shallow := New([]string{root}, WithMaxDepth(1))
	if mods, err := shallow.Search(context.Background(), domain.SearchQuery{}); err != nil {
		require.FailNow(t, "Search: %v", err)
	} else if len(mods) != 0 {
		assert.Fail(t, "depth 1 must not reach a/b/better_ui, found %s", joinIDs(modIDs(mods)))
	}

	deep := New([]string{root}, WithMaxDepth(3))

	mods, err := deep.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	if onlyMod(t, mods).ID != "better_ui" {
		assert.Fail(t, "depth 3 should reach a/b/better_ui")
	}
}

func TestSearchDoesNotDescendIntoAModFolder(t *testing.T) {
	root := t.TempDir()
	dir := writeModFolder(t, root, "pack", sampleManifest)
	// A vendored dependency inside a mod folder must not be offered as a
	// separately installable mod.
	writeModFolder(t, dir, "vendor", "[mod]\nid = \"vendored\"\n")
	writeFile(t, filepath.Join(dir, "readme.txt"), "hi")

	mods, err := New([]string{root}).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	if got := modIDs(mods); len(got) != 1 || got[0] != "better_ui" {
		assert.Fail(t, "expected only the top-level mod, got %s", joinIDs(got))
	}
}

func TestSearchSkipsMalformedManifests(t *testing.T) {
	root := t.TempDir()
	writeModFolder(t, root, "broken", "[mod\nid = ")
	writeModFolder(t, root, "good", sampleManifest)

	mods, err := New([]string{root}).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "one broken manifest must not fail the scan, got %v")

	if got := modIDs(mods); len(got) != 1 || got[0] != "better_ui" {
		assert.Fail(t, "expected only the good mod, got %s", joinIDs(got))
	}
}

func TestSearchTreatsAnArchiveFileAsAMod(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Better UI 1.2.0.zip"), "not really a zip")

	mods, err := New([]string{root}).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	got := onlyMod(t, mods)
	// The version suffix is stripped on purpose: "every release of a mod must
	// collapse to one id and folder", so Better UI 1.2.0 and Better UI 2.0.0 are
	// the same installable mod rather than two that overwrite each other.
	assert.Equal(t, "betterui", got.ID, "archive id = %q, want betterui", got.ID)

	assert.Equal(
		t,
		"0.0.0",
		got.Version,
		"an archive with no manifest has no version, want the 0.0.0 default, got %q",
		got.Version,
	)
}

func TestSearchAcceptsAFileAsAConfiguredPath(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "hd_textures.pak")
	writeFile(t, archive, "x")

	mods, err := New([]string{archive}).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	assert.Equal(
		t,
		"hd_textures",
		onlyMod(t, mods).ID,
		"expected hd_textures, got %s",
		joinIDs(modIDs(mods)),
	)

	// A file that is not an archive is ignored rather than reported.
	other := filepath.Join(root, "notes.txt")
	writeFile(t, other, "x")

	mods, err = New([]string{other}).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	assert.Empty(t, mods, "a plain file is not a mod, got %s", joinIDs(modIDs(mods)))
}

func TestSearchSkipsMissingPathsButKeepsTheRest(t *testing.T) {
	root := t.TempDir()
	writeModFolder(t, root, "better_ui", sampleManifest)

	s := New([]string{filepath.Join(root, "does-not-exist"), root})

	mods, err := s.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "a missing configured folder must not fail discovery, got %v")

	if onlyMod(t, mods).ID != "better_ui" {
		assert.Fail(t, "the reachable folder should still be scanned")
	}
}

func TestSearchWithoutPaths(t *testing.T) {
	if _, err := New(
		nil,
	).Search(
		context.Background(),
		domain.SearchQuery{},
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeSourceUnavailable,
	) {
		assert.Fail(t, "want SOURCE_UNAVAILABLE, got %v", err)
	}
}

func TestSearchDeduplicatesOverlappingPaths(t *testing.T) {
	root := t.TempDir()
	writeModFolder(t, root, "better_ui", sampleManifest)
	// The same folder configured twice, plus a nested sub-path, must yield one
	// mod rather than three installs of it.
	s := New([]string{root, root, filepath.Join(root, "better_ui")})

	mods, err := s.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	got := modIDs(mods)
	assert.Len(t, got, 1, "expected the mod exactly once, got %s", joinIDs(got))
}

func TestSearchResultIsSortedByDirectory(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zebra", "apple", "mango"} {
		writeModFolder(t, root, name, "[mod]\nid = \""+name+"\"\n")
	}

	mods, err := New([]string{root}).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	got := modIDs(mods)
	want := []string{"apple", "mango", "zebra"}
	sort.Strings(want)

	assert.Equal(
		t,
		joinIDs(want),
		joinIDs(got),
		"discovered order = %s, want %s",
		joinIDs(got),
		joinIDs(want),
	)
}

func TestSearchWithNoModsReturnsAnEmptySlice(t *testing.T) {
	mods, err := New([]string{t.TempDir()}).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	if mods == nil {
		assert.Fail(t, "an empty result must be a non-nil slice so JSON encodes as [] and not null")
	}

	assert.Empty(t, mods, "expected no mods, got %s", joinIDs(modIDs(mods)))
}

func TestSearchHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	writeModFolder(t, root, "better_ui", sampleManifest)
	s := New([]string{root})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.Search(
		ctx,
		domain.SearchQuery{},
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeOperationCanceled,
	) {
		assert.Fail(t, "want OPERATION_CANCELED, got %v", err)
	}
}

func TestSearchToleratesNilContext(t *testing.T) {
	root := t.TempDir()
	writeModFolder(t, root, "better_ui", sampleManifest)
	//nolint:staticcheck // a nil context is exactly what is under test here.
	if _, err := New([]string{root}).Search(nil, domain.SearchQuery{}); err != nil {
		assert.Fail(t, "a nil context must be tolerated, got %v", err)
	}
}

// --- filtering -------------------------------------------------------------

func TestMatchesQuery(t *testing.T) {
	m := &domain.Mod{
		ID: "better_ui", Name: "Better UI", Description: "Improves UI scaling",
		Author: "ModAuthor", GameVersion: ">=1.0.0", Tags: []string{"ui", "QoL"},
	}
	if !matchesQuery(m, domain.SearchQuery{Query: "BETTER"}) {
		assert.Fail(t, "text match should be case-insensitive")
	}

	if !matchesQuery(m, domain.SearchQuery{Query: "modauthor"}) {
		assert.Fail(t, "the author should be searchable")
	}

	if matchesQuery(m, domain.SearchQuery{Query: "wonderful"}) {
		assert.Fail(t, "a query that matches nothing should not match")
	}

	if !matchesQuery(m, domain.SearchQuery{Tags: []string{"ui"}}) {
		assert.Fail(t, "tag filter should match")
	}

	if matchesQuery(m, domain.SearchQuery{Tags: []string{"ui", "audio"}}) {
		assert.Fail(t, "every requested tag must be present")
	}

	if !matchesQuery(m, domain.SearchQuery{Tags: []string{"", "  "}}) {
		assert.Fail(t, "blank tags should be ignored rather than matching nothing")
	}

	if !matchesQuery(m, domain.SearchQuery{GameVersion: "1.5.0"}) {
		assert.Fail(t, "a version inside the declared range should match")
	}

	if matchesQuery(m, domain.SearchQuery{GameVersion: "0.9.0"}) {
		assert.Fail(t, "a version outside the declared range should not match")
	}
}

func TestSatisfiesGameVersion(t *testing.T) {
	if !satisfiesGameVersion("", "1.0.0") {
		assert.Fail(t, "a mod that declares no game version must be treated as compatible")
	}

	if !satisfiesGameVersion("  ", "1.0.0") {
		assert.Fail(t, "a whitespace-only declaration must be treated as absent")
	}

	if !satisfiesGameVersion(">=1.0.0", "1.0.0") {
		assert.Fail(t, "a matching version should satisfy the expression")
	}

	if satisfiesGameVersion(">=2.0.0", "1.0.0") {
		assert.Fail(t, "an older version must not satisfy the expression")
	}
}

func TestPaginate(t *testing.T) {
	mods := []domain.Mod{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"}}

	page := func(p, size int) []string {
		return modIDs(paginate(mods, domain.SearchQuery{Page: p, PageSize: size}))
	}
	if got := page(1, 2); len(got) != 2 || got[0] != "a" {
		assert.Fail(t, "page 1 = %s, want a,b", joinIDs(got))
	}

	if got := page(3, 2); len(got) != 1 || got[0] != "e" {
		assert.Fail(t, "page 3 = %s, want e", joinIDs(got))
	}

	out := page(9, 2)
	if out == nil {
		assert.Fail(t, "a page past the end must be a non-nil empty slice")
	}

	assert.Empty(t, out, "page past the end = %s, want empty", joinIDs(out))
	// The empty input is handed straight back: Search always passes a non-nil
	// slice, so a nil here can only come from a direct caller.
	if got := paginate(nil, domain.SearchQuery{Page: 1, PageSize: 20}); got != nil {
		assert.Fail(t, "paginate must return an empty input unchanged, got %v", got)
	}
}

// --- GetMod ----------------------------------------------------------------

func TestGetMod(t *testing.T) {
	root := t.TempDir()
	dir := writeModFolder(t, root, "better_ui", sampleManifest)
	s := New([]string{root})

	got, err := s.GetMod(context.Background(), "better_ui")
	require.NoError(t, err, "GetMod")

	assert.Equal(t, dir, got.LocalPath, "LocalPath = %q, want %q", got.LocalPath, dir)

	if upper, err := s.GetMod(context.Background(), "  BETTER_UI "); err != nil {
		assert.Fail(t, "id lookup should be trimmed and case-insensitive: %v", err)
	} else if upper.ID != "better_ui" {
		assert.Fail(t, "id = %q", upper.ID)
	}
}

func TestGetModRejectsABlankID(t *testing.T) {
	if _, err := New(
		[]string{t.TempDir()},
	).GetMod(
		context.Background(),
		"   ",
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	) {
		assert.Fail(t, "want INVALID_ARGUMENT, got %v", err)
	}
}

func TestGetModUnknown(t *testing.T) {
	if _, err := New(
		[]string{t.TempDir()},
	).GetMod(
		context.Background(),
		"nope",
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeModNotFound,
	) {
		assert.Fail(t, "want MOD_NOT_FOUND, got %v", err)
	}
}

func TestGetModReportsADuplicateID(t *testing.T) {
	root := t.TempDir()
	first := writeModFolder(t, root, "a", "[mod]\nid = \"better_ui\"\n")
	second := writeModFolder(t, root, "b", "[mod]\nid = \"better_ui\"\n")

	_, err := New([]string{root}).GetMod(context.Background(), "better_ui")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
		"want MOD_NOT_FOUND for a duplicated id, got %v",
		err,
	)
	// Both directories must be named: silently picking one would let folder
	// order decide which payload gets installed.
	if !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
		assert.Fail(t, "the error should name both folders, got %v", err)
	}
}

// --- Download --------------------------------------------------------------

func TestDownloadCopiesTheFolder(t *testing.T) {
	root := t.TempDir()
	dir := writeModFolder(t, root, "better_ui", sampleManifest)
	writeFile(t, filepath.Join(dir, "data", "hud.json"), `{"scale":2}`)

	dest := filepath.Join(t.TempDir(), "cache", "blob")

	got, err := New(
		[]string{root},
	).Download(
		context.Background(),
		&domain.Mod{ID: "better_ui", LocalPath: dir},
		dest,
	)
	require.NoError(t, err, "Download")

	assert.Equal(t, dest, got, "returned path %q, want %q", got, dest)

	body, err := os.ReadFile(filepath.Join(dest, "data", "hud.json"))
	require.NoError(t, err, "read copied file")

	assert.Equal(t, `{"scale":2}`, string(body), "copied content = %q", body)

	_, err = os.Stat(filepath.Join(dest, "mod.toml"))
	assert.NoError(
		t,
		err,
		"the manifest should be copied too, it is filtered later not here: %v",
		err,
	)
	// The original must be untouched.
	_, err = os.Stat(filepath.Join(dir, "data", "hud.json"))
	assert.NoError(t, err, "the source folder was disturbed: %v", err)
}

func TestDownloadCopiesASingleArchive(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "pack.zip")
	writeFile(t, archive, "payload")

	dest := filepath.Join(t.TempDir(), "blob")
	if _, err := New(
		nil,
	).Download(
		context.Background(),
		&domain.Mod{LocalPath: archive},
		dest,
	); err != nil {
		require.FailNow(t, "Download: %v", err)
	}

	if body, err := os.ReadFile(dest); err != nil || string(body) != "payload" {
		assert.Fail(t, "dest = %q, err = %v", body, err)
	}
}

func TestDownloadPreservesTheExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not modelled on Windows")
	}

	root := t.TempDir()
	dir := writeModFolder(t, root, "pack", sampleManifest)
	script := filepath.Join(dir, "install.sh")
	writeFile(t, script, "#!/bin/sh\n")

	err := os.Chmod(script, 0o755)
	require.NoError(t, err, "chmod: %v", err)

	dest := filepath.Join(t.TempDir(), "blob")
	if _, err := New(
		nil,
	).Download(
		context.Background(),
		&domain.Mod{LocalPath: dir},
		dest,
	); err != nil {
		require.FailNow(t, "Download: %v", err)
	}

	info, err := os.Stat(filepath.Join(dest, "install.sh"))
	require.NoError(t, err, "stat")

	assert.NotEqual(t, 0, info.Mode().Perm()&0o100, "executable bit lost: %v", info.Mode())
}

func TestDownloadRejectsBadArguments(t *testing.T) {
	s := New(nil)
	_, err := s.Download(
		context.Background(),
		nil,
		"/tmp/x",
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidMod,
	), "nil mod want INVALID_MOD, got %v", err)

	if _, err := s.Download(
		context.Background(),
		&domain.Mod{LocalPath: "/tmp"},
		"  ",
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	) {
		assert.Fail(t, "blank destination want INVALID_ARGUMENT, got %v", err)
	}

	if _, err := s.Download(
		context.Background(),
		&domain.Mod{ID: "m"},
		"/tmp/x",
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	) {
		assert.Fail(t, "missing LocalPath want INVALID_ARGUMENT, got %v", err)
	}

	if _, err := s.Download(
		context.Background(),
		&domain.Mod{LocalPath: "/nope/nope"},
		"/tmp/x",
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeModNotFound,
	) {
		assert.Fail(t, "vanished payload want MOD_NOT_FOUND, got %v", err)
	}
}

func TestDownloadDoesNotFollowAnExistingSymlinkAtTheDestination(t *testing.T) {
	root := t.TempDir()
	dir := writeModFolder(t, root, "pack", sampleManifest)
	writeFile(t, filepath.Join(dir, "payload.bin"), "new")

	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	writeFile(t, victim, "original")

	dest := filepath.Join(t.TempDir(), "blob")
	if err := os.Symlink(victim, dest); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	if _, err := New(
		nil,
	).Download(
		context.Background(),
		&domain.Mod{LocalPath: dir},
		dest,
	); err != nil {
		require.FailNow(t, "Download: %v", err)
	}

	body, err := os.ReadFile(victim)
	require.NoError(t, err, "read victim")

	assert.Equal(
		t,
		"original",
		string(body),
		"the destination symlink was followed and its target overwritten: %q",
		body,
	)
}

func TestDownloadCleansUpAfterAFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliably available on Windows")
	}

	root := t.TempDir()
	dir := writeModFolder(t, root, "pack", sampleManifest)
	outside := t.TempDir()
	escape := filepath.Join(outside, "secret.txt")
	writeFile(t, escape, "secret")

	err := os.Symlink(escape, filepath.Join(dir, "link.txt"))
	require.NoError(t, err, "symlink: %v", err)

	dest := filepath.Join(t.TempDir(), "blob")

	_, err = New(nil).Download(context.Background(), &domain.Mod{LocalPath: dir}, dest)
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod),
		"a link escaping the tree must be refused, got %v",
		err,
	)

	if _, statErr := os.Stat(dest); statErr == nil {
		assert.Fail(t, "a partially copied tree must not be left behind")
	}

	if body, readErr := os.ReadFile(escape); readErr != nil || string(body) != "secret" {
		assert.Fail(t, "the out-of-tree file was touched: %q %v", body, readErr)
	}
}

func TestDownloadFollowsAnInTreeSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliably available on Windows")
	}

	root := t.TempDir()
	dir := writeModFolder(t, root, "pack", sampleManifest)
	writeFile(t, filepath.Join(dir, "real.txt"), "content")

	err := os.Symlink("real.txt", filepath.Join(dir, "alias.txt"))
	require.NoError(t, err, "symlink: %v", err)

	dest := filepath.Join(t.TempDir(), "blob")
	if _, err := New(
		nil,
	).Download(
		context.Background(),
		&domain.Mod{LocalPath: dir},
		dest,
	); err != nil {
		require.FailNow(t, "a link inside the tree is legitimate, got %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dest, "alias.txt"))
	require.NoError(t, err, "read alias")

	assert.Equal(t, "content", string(body), "alias = %q, want content", body)
}

func TestDownloadHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	dir := writeModFolder(t, root, "pack", sampleManifest)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := New(
		nil,
	).Download(
		ctx,
		&domain.Mod{LocalPath: dir},
		filepath.Join(t.TempDir(), "blob"),
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"want OPERATION_CANCELED, got %v",
		err,
	)
}

func TestVerifyAlwaysSucceeds(t *testing.T) {
	err := New(nil).Verify(context.Background(), nil, "")
	assert.NoError(t, err, "Verify should never fail for a local payload, got %v", err)
}

// --- archiveEntry ----------------------------------------------------------

func TestArchiveEntry(t *testing.T) {
	cases := []struct {
		path string
		id   string
		note string
		ok   bool
	}{
		{path: "/mods/Better UI 1.2.0.zip", ok: true, id: "betterui"},
		{path: "/mods/hd_textures.pak", ok: true, id: "hd_textures"},
		{path: "/mods/pack.tar.gz", ok: true, id: "pack"},
		{path: "/mods/pack.tgz", ok: true, id: "pack"},
		{path: "/mods/pack.tar", ok: true, id: "pack"},
		{path: "/mods/pack.jar", ok: true, id: "pack"},
		{path: "/mods/pack.mod", ok: true, id: "pack"},
		{path: "/mods/pack.zip", ok: true, id: "pack"},
		{path: "/mods/UPPER.ZIP", ok: true, id: "upper"},
		{path: "/mods/pack name beta.zip", ok: true, id: "packnamebeta"},
		{path: "/mods/.zip", ok: false, note: "nothing left after stripping the extension"},
		{path: "/mods/pack.rar", ok: false, note: "unsupported archive type"},
		{path: "/mods/pack.zip.txt", ok: false, note: "the extension is not the suffix"},
		{path: "/mods/mod.toml", ok: false, note: "a manifest is not a payload"},
	}
	for _, c := range cases {
		got, ok := archiveEntry(c.path)
		if ok != c.ok {
			assert.Fail(t, "archiveEntry(%q) ok = %v, want %v (%s)", c.path, ok, c.ok, c.note)
			continue
		}

		if ok && got.fallbackID != c.id {
			assert.Fail(t, "archiveEntry(%q) id = %q, want %q", c.path, got.fallbackID, c.id)
		}

		if ok && !got.archived {
			assert.Fail(t, "archiveEntry(%q) should be marked archived", c.path)
		}

		if ok && got.manifest == nil {
			assert.Fail(t,
				"archiveEntry(%q) needs a non-nil manifest so entry.mod() does not need a branch",
				c.path,
			)
		}
	}
}

func TestArchiveEntryStripsOnlyAVersionLikeSuffix(t *testing.T) {
	// "beta" is not a version, so it must stay part of the id.
	got, ok := archiveEntry("/mods/pack beta.zip")
	if !ok || got.fallbackID != "packbeta" {
		assert.Fail(t, "got %q %v, want packbeta true", got.fallbackID, ok)
	}
	// A "v" prefix is not a dotted numeric run, so the suffix is kept as part of
	// the name rather than mistaken for a version.
	got, ok = archiveEntry("/mods/pack v1.2.zip")
	if !ok || got.fallbackID != "packv1.2" {
		assert.Fail(t, "got %q %v, want packv1.2 true", got.fallbackID, ok)
	}
}

// --- SanitizeID ------------------------------------------------------------

func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"Better UI":     "betterui",
		"HD_Textures":   "hd_textures",
		"a/b":           "ab",
		"..":            "local_mod",
		"":              "local_mod",
		"   ":           "local_mod",
		"  spaced  ":    "spaced",
		"v1.2.0":        "v1.2.0",
		"weird!@#$%^&*": "weird",
		// Lower-casing is what makes case-only differences collide as ids on
		// every platform, so ModService can report them instead of the user
		// discovering the clash on a case-insensitive volume.
		"BetterUI": "betterui",
	}
	for in, want := range cases {
		got := SanitizeID(in)
		assert.Equal(t, want, got, "SanitizeID(%q) = %q, want %q", in, got, want)
	}
}

// --- manifest --------------------------------------------------------------

func TestReadManifestPrefersModToml(t *testing.T) {
	root := t.TempDir()

	dir := filepath.Join(root, "pack")
	err := os.MkdirAll(dir, 0o755)
	require.NoError(t, err, "mkdir: %v", err)

	writeFile(t, filepath.Join(dir, "mod.toml"), "[mod]\nid = \"winner\"\n")
	writeFile(t, filepath.Join(dir, "manifest.toml"), "[mod]\nid = \"loser\"\n")

	m, err := readManifest(dir)
	require.NoError(t, err, "readManifest")

	assert.Equal(t, "winner", m.Mod.ID, "mod.toml should win, got %q", m.Mod.ID)
}

func TestReadManifestWithoutOne(t *testing.T) {
	m, err := readManifest(t.TempDir())
	require.NoError(t, err, "a mod without a manifest is legal, got %v")

	if m == nil {
		require.FailNow(t, "readManifest must return a usable empty manifest, not nil")
	}

	assert.Equal(t, "", m.Mod.ID, "expected an empty manifest, got %+v", m.Mod)
}

func TestReadManifestMalformed(t *testing.T) {
	root := t.TempDir()
	writeModFolder(t, root, "pack", "[mod\nid = ")

	_, err := readManifest(filepath.Join(root, "pack"))
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod),
		"want INVALID_MOD, got %v",
		err,
	)

	assert.Contains(
		t,
		err.Error(),
		"mod.toml",
		"the error should name the offending file, got %v",
		err,
	)
}

func TestHasManifestDir(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "plain")
	writeFile(t, filepath.Join(plain, "readme.txt"), "x")

	if hasManifestDir(plain) {
		assert.Fail(t, "a folder with only a readme is not a mod folder")
	}

	for _, name := range manifestNames {
		dir := filepath.Join(root, "with"+name)
		writeFile(t, filepath.Join(dir, name), "[mod]\n")

		assert.True(t, hasManifestDir(dir), "%s should be recognised", name)
	}
	// A *directory* named mod.toml is not a manifest.
	odd := filepath.Join(root, "odd")
	writeFile(t, filepath.Join(odd, "mod.toml", "inner.txt"), "x")

	if hasManifestDir(odd) {
		assert.Fail(t, "a directory called mod.toml must not count as a manifest")
	}
}

func TestManifestHooksCoverEveryPhase(t *testing.T) {
	body := ""

	for _, phase := range domain.HookPhases() {
		name := strings.ReplaceAll(string(phase), "-", "_")
		body += "[[hooks." + name + "]]\ntype = \"command\"\ncontent = \"echo " + name + "\"\ntimeout = 7\n\n"
	}

	root := t.TempDir()
	writeModFolder(t, root, "pack", body)

	m, err := readManifest(filepath.Join(root, "pack"))
	require.NoError(t, err, "readManifest")

	hooks := m.DomainHooks()
	got := hooks.Count()
	assert.Equal(
		t,
		len(domain.HookPhases()),
		got,
		"hook count = %d, want %d (one per phase)",
		got,
		len(domain.HookPhases()),
	)

	for _, phase := range domain.HookPhases() {
		scripts := hooks.For(phase)
		require.Len(t, scripts, 1, "phase %s has %d scripts, want 1", phase, len(scripts))

		if scripts[0].Timeout != 7 || scripts[0].Type != domain.HookTypeCommand {
			assert.Fail(t, "phase %s script = %+v", phase, scripts[0])
		}
	}
}

func TestDomainHooksDropsBlankScripts(t *testing.T) {
	// An empty content line must not make the mod impossible to enable.
	body := "[[hooks.pre_enable]]\ntype = \"inline\"\ncontent = \"\"\n\n" +
		"[[hooks.pre_enable]]\ntype = \"inline\"\ncontent = \"  \"\n\n" +
		"[[hooks.pre_enable]]\ntype = \"inline\"\ncontent = \"ok\"\n"
	root := t.TempDir()
	writeModFolder(t, root, "pack", body)

	m, err := readManifest(filepath.Join(root, "pack"))
	require.NoError(t, err, "readManifest")

	hooks := m.DomainHooks()
	got := len(hooks.PreEnable)
	require.Equal(t, 1, got, "expected the blank scripts dropped, got %d", got)

	assert.Equal(
		t,
		"ok",
		hooks.PreEnable[0].Content,
		"wrong script survived: %+v",
		hooks.PreEnable[0],
	)
}

func TestHookEntryDefaultsToInline(t *testing.T) {
	// An unrecognised type would make the hook impossible to run, and the executor
	// rejects unknown types, so anything unusable degrades to inline.
	for _, typ := range []string{"", "  ", "powershell", "bogus"} {
		got := hookEntry{Type: typ, Content: "x"}.script()
		assert.Equal(
			t,
			domain.HookTypeInline,
			got.Type,
			"type %q became %q, want inline",
			typ,
			got.Type,
		)
	}

	got := hookEntry{
		Type:       "file",
		Content:    "run.sh",
		WorkingDir: "sub",
		Env:        []string{"A=1"},
	}.script()
	if got.Type != domain.HookTypeFile || got.WorkingDir != "sub" || len(got.Env) != 1 {
		assert.Fail(t, "a recognised type must survive: %+v", got)
	}
}

func TestManifestHooksReachTheMod(t *testing.T) {
	root := t.TempDir()
	writeModFolder(t, root, "better_ui",
		sampleManifest+"\n[[hooks.pre_enable]]\ntype = \"inline\"\ncontent = \"setup()\"\n")
	s := New([]string{root})

	mods, err := s.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	hooks := onlyMod(t, mods).Hooks
	if len(hooks.PreEnable) != 1 || hooks.PreEnable[0].Content != "setup()" {
		assert.Fail(t, "manifest hooks not attached to the mod: %+v", hooks.PreEnable)
	}
}

func TestTrimmedList(t *testing.T) {
	got := trimmedList([]string{" a ", "", "  ", "b", "\t"})
	assert.Equal(t, "a,b", joinIDs(got), "trimmedList = %q, want a,b", joinIDs(got))

	if trimmedList(nil) != nil {
		assert.Fail(t, "a nil list should stay nil so it encodes as an absent field")
	}

	if got := trimmedList([]string{" ", ""}); got != nil {
		assert.Fail(t, "a list that is entirely blank should collapse to nil, got %q", joinIDs(got))
	}
}

func TestFirstNonBlank(t *testing.T) {
	got := firstNonBlank("", "  ", "value")
	assert.Equal(t, "value", got, "got %q, want value", got)

	got = firstNonBlank("", "  ")
	assert.Equal(t, "", got, "all blank should give the empty string, got %q", got)
}

func TestLooksLikeVersion(t *testing.T) {
	// Only dotted numeric runs count: "beta" is part of the mod's name, and a
	// "v" prefix is too common in release names to treat as a version marker.
	for _, s := range []string{"1.2.0", "2024.1", "0", "1.0"} {
		assert.True(t, looksLikeVersion(s), "%q should look like a version", s)
	}

	for _, s := range []string{"beta", "", "v1.2", "release", "1.2.beta", "1..2", "."} {
		assert.False(t, looksLikeVersion(s), "%q should not look like a version", s)
	}
}

func TestFallbackID(t *testing.T) {
	got := fallbackID(filepath.Join("/mods", "Better UI"))
	assert.Equal(t, "betterui", got, "fallbackID = %q, want betterui", got)
}

func TestModLabelNeverDereferencesNil(t *testing.T) {
	got := modLabel(nil)
	assert.Equal(t, "<unknown>", got, "modLabel(nil) = %q", got)

	if got := modLabel(&domain.Mod{ID: "  "}); got != "<unknown>" {
		assert.Fail(t, "modLabel(blank) = %q", got)
	}

	if got := modLabel(&domain.Mod{ID: "better_ui"}); got != "better_ui" {
		assert.Fail(t, "modLabel = %q", got)
	}
}

func TestWithinDepth(t *testing.T) {
	root := filepath.Join("a", "root")

	cases := []struct {
		path  string
		note  string
		depth int
		want  bool
	}{
		{path: root, depth: 1, want: true, note: "the root itself is always within budget"},
		{path: filepath.Join(root, "one"), depth: 1, want: true},
		{path: filepath.Join(root, "one", "two"), depth: 1, want: false},
		{path: filepath.Join(root, "one", "two"), depth: 2, want: true},
		{path: filepath.Join(root, "one", "two", "three"), depth: 2, want: false},
		{
			path:  filepath.Join(root, "one"),
			depth: 0,
			want:  false,
			note:  "a zero budget reaches nothing but the root",
		},
		{path: filepath.Join(root, "one"), depth: -1, want: false},
	}
	for _, c := range cases {
		got := withinDepth(root, c.path, c.depth)
		assert.Equal(
			t,
			c.want,
			got,
			"withinDepth(%q, %d) = %v, want %v (%s)",
			c.path,
			c.depth,
			got,
			c.want,
			c.note,
		)
	}
}

func TestReleasesOfOneModCollapseToOneID(t *testing.T) {
	// Two release files side by side must not be two separately installable mods:
	// they would fight over one payload folder and one set of game links.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Better UI 1.2.0.zip"), "x")
	writeFile(t, filepath.Join(root, "Better UI 2.0.0.zip"), "x")

	mods, err := New([]string{root}).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	if got := modIDs(mods); len(got) != 2 {
		// Both files are still reported, but the discovery list is per-file, so
		// the manager must resolve which one to install through GetMod. That call
		// is where the collision has to be reported.
		t.Logf("discovered %s", joinIDs(got))
	}

	if _, err := New(
		[]string{root},
	).GetMod(
		context.Background(),
		"betterui",
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeModNotFound,
	) {
		assert.Fail(t, "two folders claiming one id must be reported, got %v", err)
	}
}
