package github

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidRepoRef(t *testing.T) {
	good := []string{"owner/repo", "a/b", "Some.One/some_repo", "owner/repo.js"}
	for _, ref := range good {
		assert.True(t, validRepoRef(ref), "validRepoRef(%q) = false, want true", ref)
	}
	// A ref is used to build "{apiBase}/repos/{ref}/releases", so a different number
	// of slashes would address a different resource, or with ".." one outside the API.
	bad := []string{
		"",
		"owner",
		"owner/",
		"/repo",
		"owner/repo/extra",
		"a/b/c",
		"owner/ /repo",
		"/",
	}
	for _, ref := range bad {
		assert.False(t, validRepoRef(ref), "validRepoRef(%q) = true, want false", ref)
	}
}

func TestSplitRef(t *testing.T) {
	owner, repo := splitRef("modsmith/darktide.ui")
	if owner != "modsmith" || repo != "darktide.ui" {
		assert.Fail(t, "splitRef = %q/%q, want modsmith/darktide.ui", owner, repo)
	}
	// An invalid ref must not panic and must not invent a plausible answer.
	owner, repo = splitRef("nodots")
	if owner != "" || repo != "" {
		assert.Fail(t, "splitRef(%q) = %q/%q, want empty/empty", "nodots", owner, repo)
	}
}

func TestModIDRoundTripsThroughRepoFromModID(t *testing.T) {
	// Only references validRepoRef accepts are round-trippable; the one-sided
	// cases are covered separately below.
	cases := []struct{ owner, repo string }{
		{"modsmith", "darktide"},
		{"a", "b.c.d"},
		{"Owner", "Repo-Name_1.0"},
		{"", ""},
	}
	for _, tc := range cases {
		id := ModID(tc.owner, tc.repo)
		if id == "" {
			continue
		}

		ref, err := repoFromModID(id)
		if err != nil {
			assert.Fail(t, "repoFromModID(%q) failed: %v", id, err)
			continue
		}

		want := tc.owner + "/" + tc.repo
		assert.Equal(
			t,
			want,
			ref,
			"round trip of %q/%q gave %q, want %q",
			tc.owner,
			tc.repo,
			ref,
			want,
		)
	}
}

// ModID with only one half present produces an id with no separator in it, which
// repoFromModID refuses. That is deliberate: the caller recovers the repository
// from a stored mod id, and validRepoRef means neither half is ever blank.
func TestModIDWithOneHalfIsNotDecodable(t *testing.T) {
	for _, tc := range []struct{ owner, repo, want string }{
		{"owner", "", "owner"},
		{"", "pack", "pack"},
	} {
		id := ModID(tc.owner, tc.repo)
		assert.Equal(t, tc.want, id, "ModID(%q, %q) = %q, want %q", tc.owner, tc.repo, id, tc.want)

		_, err := repoFromModID(id)
		assert.Error(t, err, "repoFromModID(%q) succeeded on a one-sided id", id)
	}
}

// ModID strips dots from the owner so that the pair really is an inverse.
// GitHub logins cannot contain dots, so this costs nothing for real data, but
// without it the id could not be decoded back to the repository it came from.
func TestModIDStripsDotsFromTheOwner(t *testing.T) {
	id := ModID("some.one", "pack")
	require.Equal(
		t,
		"someone.pack",
		id,
		"ModID(%q, %q) = %q, want %q",
		"some.one",
		"pack",
		id,
		"someone.pack",
	)

	ref, err := repoFromModID(id)
	require.NoError(t, err, "repoFromModID(%q) failed: %v", id, err)

	assert.Equal(t, "someone/pack", ref, "round trip gave %q, want %q", ref, "someone/pack")
}

// Splitting at the first dot is what keeps "someone/darktide.mods" and
// "someone/darktide" apart.
func TestModIDSeparatesOwnerFromDottedRepo(t *testing.T) {
	ref, err := repoFromModID("someone.darktide.mods")
	require.NoError(t, err, "repoFromModID failed")

	assert.Equal(
		t,
		"someone/darktide.mods",
		ref,
		"repoFromModID = %q, want someone/darktide.mods",
		ref,
	)
}

func TestRepoFromModIDRejectsMalformedIDs(t *testing.T) {
	bad := []string{
		"",           // nothing at all
		"nodot",      // no separator
		".leading",   // empty owner half
		"trailing.",  // empty repo half
		"owner/repo", // a raw ref, not an id
		"   .   ",    // blank halves after trimming
		".",          // separator with nothing either side
	}
	for _, id := range bad {
		if _, err := repoFromModID(id); err == nil {
			assert.Fail(t, "repoFromModID(%q) succeeded, want an error", id)
		} else if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
			assert.Fail(t, "repoFromModID(%q) gave %v, want INVALID_ARGUMENT", id, err)
		}
	}
}

func TestRepoFromModIDTrimsPadding(t *testing.T) {
	ref, err := repoFromModID("  owner.pack  ")
	require.NoError(t, err, "repoFromModID failed")

	assert.Equal(t, "owner/pack", ref, "repoFromModID = %q, want owner/pack", ref)
}

func TestNormalizeVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.0", "1.2.0"},
		{"v1.2.0", "1.2.0"},
		{"V1.2.0", "1.2.0"},
		{"refs/tags/v2.0.0", "2.0.0"},
		{"refs/tags/3.1", "3.1"},
		// A leading v is only a version prefix when a digit follows it: "vanilla"
		// is a word, and mangling it would change the tag a maintainer chose.
		{"vanilla", "vanilla"},
		{"vNext", "vNext"},
		// A maintainer is entitled to tag a release something the semver parser
		// will not understand; it is kept verbatim rather than discarded.
		{"hotfix-2024-01", "hotfix-2024-01"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		got := normalizeVersion(tc.in)
		assert.Equal(t, tc.want, got, "normalizeVersion(%q) = %q, want %q", tc.in, got, tc.want)
	}
}

func TestPayloadAssetPicksTheLargestArchive(t *testing.T) {
	assets := []asset{
		{Name: "README.md", Size: 9_000_000, BrowserDownloadURL: "https://example.test/readme"},
		{Name: "pack.zip", Size: 10, BrowserDownloadURL: "https://example.test/zip"},
		{
			Name:               "debug-symbols.zip",
			Size:               50_000_000,
			BrowserDownloadURL: "https://example.test/debug",
		},
	}

	got, ok := payloadAsset(assets)
	if !ok {
		require.FailNow(t, "payloadAsset found no archive")
	}

	assert.Equal(
		t,
		"debug-symbols.zip",
		got.Name,
		"payloadAsset chose %q, want the largest archive",
		got.Name,
	)
}

// A release whose only assets are documentation has no payload to install, so
// payloadAsset must report nothing rather than hand back the README. A zero-sized
// archive is still a payload: size only picks between candidates, never rejects.
func TestPayloadAssetIgnoresNonArchives(t *testing.T) {
	if _, ok := payloadAsset(nil); ok {
		assert.Fail(t, "payloadAsset claimed to have found something in an empty asset list")
	}

	if _, ok := payloadAsset(
		[]asset{{Name: "README.md", Size: 10}, {Name: "LICENSE", Size: 5}},
	); ok {
		assert.Fail(t, "payloadAsset claimed a README was the payload")
	}

	got, ok := payloadAsset([]asset{{Name: "pack.zip", Size: 0}})
	if !ok || got.Name != "pack.zip" {
		assert.Fail(t, "payloadAsset on a zero-size archive gave (%q, %v)", got.Name, ok)
	}
}

func TestIsArchiveName(t *testing.T) {
	for _, name := range []string{
		"pack.zip", "pack.ZIP", "pack.tar.gz", "pack.tgz", "pack.tar.bz2", "pack.rar", "pack.7z",
	} {
		assert.True(t, isArchiveName(name), "isArchiveName(%q) = false, want true", name)
	}

	for _, name := range []string{"pack", "pack.zip.txt", "README.md", "pack.zipper", ""} {
		assert.False(t, isArchiveName(name), "isArchiveName(%q) = true, want false", name)
	}
}

// The download digest reaches the cache manager through SourceData, so it must
// be stored without the "sha256:" prefix that the GitHub API adds. Leaving the
// prefix in place would make every GitHub download fail its integrity check.
func TestModForStripsTheDigestPrefixAndRecordsTheAsset(t *testing.T) {
	r := release{
		TagName: "v3.0.0",
		Name:    "Release v3.0.0",
		Body:    "notes",
		Assets: []asset{{
			Name:               "pack.zip",
			Size:               4242,
			BrowserDownloadURL: "https://example.test/pack.zip",
			Digest:             "sha256:ABCDEF",
		}},
	}

	m, ok := modFor("owner/pack", r)
	if !ok {
		require.FailNow(t, "modFor rejected a usable release")
	}

	got := m.SourceData["sha256"]
	assert.Equal(t, "ABCDEF", got, "sha256 = %v, want ABCDEF with the prefix stripped", got)

	got = m.SourceData["download_url"]
	assert.Equal(t, "https://example.test/pack.zip", got, "download_url = %v", got)

	got = m.SourceData["size_bytes"]
	assert.Equal(t, int64(4242), got, "size_bytes = %v (%T), want an int64", got, got)

	if _, present := m.SourceData["prerelease"]; present {
		assert.Fail(t, "prerelease must be absent unless the release is one")
	}
}

func TestModForRecordsPrereleaseOnlyWhenSet(t *testing.T) {
	r := release{TagName: "v1.0.0-rc1", Prerelease: true, Assets: []asset{
		{Name: "pack.zip", BrowserDownloadURL: "https://example.test/p.zip"},
	}}

	m, ok := modFor("owner/pack", r)
	if !ok {
		require.FailNow(t, "modFor rejected a prerelease")
	}

	assert.Equal(
		t,
		true,
		m.SourceData["prerelease"],
		"prerelease = %v, want true",
		m.SourceData["prerelease"],
	)
}

func TestModForSkipsReleasesWithoutAUsableVersion(t *testing.T) {
	for _, tag := range []string{"", "   ", "refs/tags/"} {
		r := release{TagName: tag, Assets: []asset{
			{Name: "pack.zip", BrowserDownloadURL: "https://example.test/p.zip"},
		}}
		_, ok := modFor("owner/pack", r)
		assert.False(t, ok, "modFor accepted tag %q", tag)
	}
}

// Every release of one repository is a *version of the same mod*, so all three
// deliberately share one id and are told apart by Version. That is what lets a
// user install an old release and lets ApplyUpdate compare against the newest.
func TestModForProducesOneModPerRelease(t *testing.T) {
	rels := []release{
		{
			TagName: "v3.0.0",
			Assets:  []asset{{Name: "p.zip", BrowserDownloadURL: "https://example.test/3"}},
		},
		{
			TagName: "v2.0.0",
			Assets:  []asset{{Name: "p.zip", BrowserDownloadURL: "https://example.test/2"}},
		},
		// A draft still maps to a mod here because its tag parses; the caller (releases)
		// is what filters drafts, since indexing one would offer a 404 download.
		{
			Draft:   true,
			TagName: "v9.9.9",
			Assets:  []asset{{Name: "p.zip", BrowserDownloadURL: "https://example.test/9"}},
		},
	}

	mods := modsFor("owner/pack", rels)
	require.Len(t, mods, 3, "modsFor returned %d mods, want one per release", len(mods))

	versions := map[string]bool{}

	for _, m := range mods {
		assert.Equal(
			t,
			"owner.pack",
			m.ID,
			"ID = %q, want owner.pack for every release of the repository",
			m.ID,
		)

		assert.False(t, versions[m.Version], "duplicate version %q", m.Version)

		versions[m.Version] = true
		assert.Equal(t, SourceID, m.SourceID, "SourceID = %q, want %q", m.SourceID, SourceID)

		assert.Equal(t, "owner", m.Author, "Author = %q, want owner", m.Author)

		if !m.HasTag("github") || !m.HasTag("Owner") {
			assert.Fail(t, "tags = %v, want github and the lower-cased owner", m.Tags)
		}
	}

	for _, want := range []string{"3.0.0", "2.0.0", "9.9.9"} {
		assert.True(t, versions[want], "version %q missing from %v", want, versions)
	}
}

func TestModForPropagatesTheDeclaredGameVersion(t *testing.T) {
	r := release{
		TagName: "1.0.0",
		Assets:  []asset{{Name: "p.zip", BrowserDownloadURL: "https://example.test/p"}},
	}

	m, ok := modFor("owner/pack", r)
	if !ok {
		require.FailNow(t, "modFor failed")
	}

	assert.Equal(
		t,
		"",
		m.GameVersion,
		"GameVersion = %q, want empty: a GitHub release declares nothing",
		m.GameVersion,
	)
}

// searchQuery renders GitHub's repository-search syntax: the user's text followed
// by the topic qualifier, appended unconditionally so a query cannot widen the
// search to all of GitHub and burn the anonymous rate limit in seconds.
func TestSearchQueryAlwaysCarriesTheTopic(t *testing.T) {
	got, want := searchQuery(
		"better ui",
		"darktide-mod",
	), "better ui topic:darktide-mod"
	assert.Equal(
		t,
		want,
		got,
		"searchQuery(%q, %q) = %q, want %q",
		"better ui",
		"darktide-mod",
		got,
		want,
	)
	// A blank query is not an error; the topic qualifier alone is a valid
	// GitHub search and is exactly what "list everything" should ask for.
	got, want = searchQuery("", "darktide-mod"), "topic:darktide-mod"
	assert.Equal(t, want, got, "searchQuery with a blank query = %q, want %q", got, want)

	got, want = searchQuery("   \t\n ", "darktide-mod"), "topic:darktide-mod"
	assert.Equal(t, want, got, "searchQuery with a whitespace-only query = %q, want %q", got, want)
	// A query that already names the topic keeps it: the text matches repository
	// metadata, the qualifier filters on topics, so they are different constraints.
	got, want = searchQuery(
		"darktide-mod",
		"darktide-mod",
	), "darktide-mod topic:darktide-mod"
	assert.Equal(
		t,
		want,
		got,
		"searchQuery(%q, %q) = %q, want %q",
		"darktide-mod",
		"darktide-mod",
		got,
		want,
	)
}

func TestAbsPathLeavesAbsolutePathsAlone(t *testing.T) {
	abs, err := filepath.Abs("relative")
	require.NoError(t, err, "Abs failed")

	got := absPath(abs)
	assert.Equal(t, abs, got, "absPath(%q) = %q, want it unchanged", abs, got)

	got = absPath(abs)
	assert.True(t, filepath.IsAbs(got), "absPath returned a relative path %q", got)

	got = absPath("")
	assert.True(t, filepath.IsAbs(got), "absPath(\"\") = %q, want an absolute path", got)
}

// The delegating wrappers exist so this package's call sites stay readable; they
// must not drift from sourceutil.
func TestModelHelpersDelegateToSourceutil(t *testing.T) {
	creds := domain.AuthCredentials{"token": "  abc  ", "empty": "   "}
	got := credValue(creds, "token", "api_key")
	assert.Equal(t, "abc", got, "credValue = %q, want the trimmed token", got)

	got = credValue(creds, "empty", "missing")
	assert.Equal(t, "", got, "credValue = %q, want blank when every key is blank", got)

	got = firstNonBlank("", "  ", "third")
	assert.Equal(t, "third", got, "firstNonBlank = %q", got)

	got = stringOf(nil)
	assert.Equal(t, "", got, "stringOf(nil) = %q", got)

	got = modLabel(nil)
	assert.Equal(t, "<unknown>", got, "modLabel(nil) = %q", got)

	parts := splitList("a, b\nc,,d")
	assert.Len(t, parts, 4, "splitList gave %v, want 4 entries", parts)

	if got := optionList([]string{"x", "y"}); len(got) != 2 {
		assert.Fail(t, "optionList gave %v, want 2 entries", got)
	}

	err := ensureDir("")
	assert.NoError(t, err, "ensureDir(\"\") = %v, want nil", err)

	_, err = fileSHA256("")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"fileSHA256(\"\") = %v, want INVALID_ARGUMENT",
		err,
	)
}

func TestFileSHA256MatchesTheRealDigest(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "payload.bin")
	err := os.WriteFile(path, []byte("abc"), 0o644)
	require.NoError(t, err, "write: %v", err)

	got, err := fileSHA256(path)
	require.NoError(t, err, "fileSHA256 failed")

	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	assert.Equal(t, want, got, "fileSHA256 = %q, want %q", got, want)

	_, err = fileSHA256(
		filepath.Join(dir, "missing"),
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeCacheMiss,
	), "fileSHA256 on a missing file = %v, want CACHE_MISS", err)
}

// The mod id must survive domain.SanitizeSegment as a single path segment,
// because that is what ModService turns into a payload folder.
func TestModIDIsUsableAsOnePathSegment(t *testing.T) {
	id := ModID("some.one", "darktide.mods")
	got := domain.SanitizeSegment(id)
	assert.Equal(t, id, got, "SanitizeSegment(%q) = %q, want the id unchanged", id, got)

	assert.False(t, strings.ContainsAny(id, `/\`), "ModID produced a separator: %q", id)

	if runtime.GOOS == "windows" && strings.ContainsAny(id, `:`) {
		assert.Fail(t, "ModID produced a drive separator: %q", id)
	}
}
