package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games/gamestest"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubServer serves the three endpoints the source touches plus a file host for
// release assets. Handlers may be nil, in which case the endpoint 404s — the
// source must not crash.
type stubServer struct {
	*httptest.Server

	search   http.HandlerFunc
	releases http.HandlerFunc
	user     http.HandlerFunc

	// requests records "METHOD path?query" for every call, so a test can assert
	// that discovery really did append the topic.
	requests []string
	// bearer records the Authorization header of every request that carried one.
	bearer []string
	// assetBody is what the /asset endpoint returns.
	assetBody []byte
}

func newStubServer(t *testing.T) *stubServer {
	t.Helper()

	st := &stubServer{assetBody: []byte("payload")}
	st.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.requests = append(st.requests, r.Method+" "+r.URL.RequestURI())
		if v := r.Header.Get("Authorization"); v != "" {
			st.bearer = append(st.bearer, v)
		}

		switch {
		case strings.HasPrefix(r.URL.Path, "/asset"):
			w.Write(st.assetBody)
		case strings.HasPrefix(r.URL.Path, "/search/repositories"):
			if st.search != nil {
				st.search(w, r)
				return
			}

			http.NotFound(w, r)
		case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/releases"):
			if st.releases != nil {
				st.releases(w, r)
				return
			}

			http.NotFound(w, r)
		case r.URL.Path == "/user":
			if st.user != nil {
				st.user(w, r)
				return
			}

			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(st.Close)

	return st
}

func (st *stubServer) url() string { return st.URL }

func (st *stubServer) sawRequest(substr string) bool {
	for _, r := range st.requests {
		if strings.Contains(r, substr) {
			return true
		}
	}

	return false
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()

	err := json.NewEncoder(w).Encode(body)
	assert.NoError(t, err, "encode fixture: %v", err)
}

func newSource(t *testing.T, st *stubServer, opts ...Option) *Source {
	t.Helper()

	opts = append([]Option{WithAPIBase(st.url())}, opts...)

	s, err := New(domain.SourceConfig{}, opts...)
	require.NoError(t, err, "New")

	return s
}

func modIDs(mods []domain.Mod) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, m.ID)
	}

	return out
}

func releaseFixture(
	tag string,
	draft, prerelease bool,
	assetURL, digest string,
	size int64,
) release {
	return release{
		TagName:     tag,
		Name:        "Release " + tag,
		Body:        "notes for " + tag,
		Draft:       draft,
		Prerelease:  prerelease,
		PublishedAt: "2026-02-01T10:00:00Z",
		HTMLURL:     "https://example.test/r/" + tag,
		Assets: []asset{
			{Name: "README.md", Size: 10, BrowserDownloadURL: "https://example.test/readme"},
			{
				Name:               "pack.zip",
				Size:               size,
				ContentType:        "application/zip",
				BrowserDownloadURL: assetURL,
				Digest:             digest,
			},
		},
	}
}

func TestSourceIdentity(t *testing.T) {
	s, err := New(domain.SourceConfig{})
	require.NoError(t, err, "New")

	assert.Equal(t, SourceID, s.ID(), "ID() = %q, want %q", s.ID(), SourceID)

	if strings.TrimSpace(s.Name()) == "" || strings.TrimSpace(s.Description()) == "" {
		assert.Fail(t, "Name/Description must not be blank: %q %q", s.Name(), s.Description())
	}

	assert.Equal(
		t,
		pluginVersion,
		s.Version(),
		"Version() = %q, want %q",
		s.Version(),
		pluginVersion,
	)

	if !s.SupportsSearch() {
		assert.Fail(t, "a repository index is searchable")
	}

	if !s.SupportsAuth() {
		assert.Fail(t, "a user with a token must be able to supply it")
	}

	if s.RequiresAPIKey() {
		assert.Fail(t, "anonymous GitHub access works, so the source must not demand a key")
	}
}

func TestNewAppliesConfigOptions(t *testing.T) {
	s, err := New(domain.SourceConfig{
		APIKey: "  cfg-token  ",
		Options: map[string]string{
			"api_base":        "  https://example.test/api/  ",
			"repositories":    "owner/one, owner/two\nnot-a-ref",
			"timeout_seconds": "7",
			"topic":           " darktide-mod-v2 ",
			"max_discover":    "3",
			"max_releases":    "4",
		},
	})
	require.NoError(t, err, "New")

	assert.Equal(
		t,
		"https://example.test/api",
		s.apiBase,
		"api_base = %q, want the trimmed value",
		s.apiBase,
	)

	assert.Equal(t, "cfg-token", s.token, "token = %q, want the trimmed APIKey", s.token)

	assert.Equal(
		t,
		"owner/one,owner/two",
		strings.Join(s.repos, ","),
		"repositories = %v, want only the well-formed refs",
		s.repos,
	)

	assert.Equal(t, "darktide-mod-v2", s.topic, "topic = %q", s.topic)

	if s.maxDisc != 3 || s.relLmit != 4 {
		assert.Fail(t, "limits = %d/%d, want 3/4", s.maxDisc, s.relLmit)
	}
}

func TestNewIgnoresMalformedConfigOptions(t *testing.T) {
	s, err := New(domain.SourceConfig{Options: map[string]string{
		"api_base":        "   ",
		"timeout_seconds": "not-a-number",
		"topic":           "",
		"max_discover":    "-1",
		"max_releases":    "0",
	}})
	require.NoError(t, err, "New")

	assert.Equal(
		t,
		DefaultAPIBase,
		s.apiBase,
		"a blank api_base must keep the default, got %q",
		s.apiBase,
	)

	assert.Equal(t, SearchTopic, s.topic, "a blank topic must keep the default, got %q", s.topic)

	if s.maxDisc != defaultMaxDiscover || s.relLmit != defaultReleaseLimit {
		assert.Fail(
			t,
			"non-positive limits must keep the defaults, got %d/%d",
			s.maxDisc,
			s.relLmit,
		)
	}

	assert.Equal(t, "", s.token, "a blank APIKey must not become a token, got %q", s.token)
}

func TestConfigRepositoriesAppendToOptionOnes(t *testing.T) {
	// A user who both calls WithRepositories and configures the source expects
	// both lists, not one silently replacing the other.
	s, err := New(domain.SourceConfig{Options: map[string]string{"repositories": "cfg/one"}},
		WithRepositories("opt/one", "bad"))
	require.NoError(t, err, "New")

	assert.Equal(
		t,
		"opt/one,cfg/one",
		strings.Join(s.Repositories(), ","),
		"repositories = %v, want option entries then config entries",
		s.Repositories(),
	)
}

func TestRepositoriesReturnsACopy(t *testing.T) {
	s, _ := New(domain.SourceConfig{}, WithRepositories("a/b"))

	got := s.Repositories()
	require.Len(t, got, 1, "Repositories() = %v, want one entry", got)

	got[0] = "mutated/x"

	if s.Repositories()[0] != "a/b" {
		assert.Fail(
			t,
			"Repositories() handed out the backing array; callers can corrupt the source",
		)
	}
}

func TestSearchConfiguredRepos(t *testing.T) {
	st := newStubServer(t)
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []release{
			releaseFixture("v1.0.0", false, false, "https://example.test/a", "", 100),
			releaseFixture("v1.1.0", false, false, "https://example.test/b", "", 200),
		})
	}
	s := newSource(t, st, WithRepositories("owner/pack"))

	mods, err := s.Search(context.Background(), domain.SearchQuery{Query: "pack"})
	require.NoError(t, err, "Search")

	require.Len(t, mods, 2, "got %d mods (%v), want one per release", len(mods), modIDs(mods))

	assert.Equal(
		t,
		ModID("owner", "pack"),
		mods[0].ID,
		"mod id = %q, want %q",
		mods[0].ID,
		ModID("owner", "pack"),
	)

	if mods[0].SourceID != SourceID || mods[0].Author != "owner" {
		assert.Fail(t, "mod not attributed: %+v", mods[0])
	}

	if mods[0].Version != "1.0.0" || mods[1].Version != "1.1.0" {
		assert.Fail(t,
			"versions = %q/%q, want 1.0.0/1.1.0 in release order",
			mods[0].Version,
			mods[1].Version,
		)
	}

	if got, ok := mods[0].SourceData["download_url"].(string); !ok ||
		got != "https://example.test/a" {
		assert.Fail(t, "download_url = %v, want the asset URL", mods[0].SourceData["download_url"])
	}

	if _, ok := mods[0].SourceData["prerelease"]; ok {
		assert.Fail(t, "prerelease must only be recorded when the release is one")
	}

	assert.False(
		t,
		st.sawRequest("/search/repositories"),
		"Search must not call the discovery endpoint when repositories are configured; saw %v",
		st.requests,
	)
}

func TestSearchSkipsDraftReleases(t *testing.T) {
	st := newStubServer(t)
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []release{
			releaseFixture("v0.1.0", true, false, "https://example.test/draft", "", 100),
			releaseFixture("v0.2.0", false, false, "https://example.test/live", "", 100),
		})
	}
	s := newSource(t, st, WithRepositories("owner/pack"))

	mods, err := s.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	if len(mods) != 1 || mods[0].Version != "0.2.0" {
		require.FailNow(t, "got %v, want only the published release", modIDs(mods))
	}
}

func TestSearchRecordsPrereleaseFlag(t *testing.T) {
	st := newStubServer(t)
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []release{
			releaseFixture("v2.0.0-rc1", false, true, "https://example.test/rc", "", 100),
		})
	}
	s := newSource(t, st, WithRepositories("owner/pack"))

	mods, err := s.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	if v, ok := mods[0].SourceData["prerelease"].(bool); !ok || !v {
		assert.Fail(t, "prerelease = %v, want true", mods[0].SourceData["prerelease"])
	}
}

func TestSearchToleratesOneFailingRepo(t *testing.T) {
	st := newStubServer(t)
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/broken/") {
			http.Error(w, "gone", http.StatusInternalServerError)
			return
		}

		writeJSON(
			t,
			w,
			[]release{releaseFixture("v1.0.0", false, false, "https://example.test/a", "", 100)},
		)
	}
	s := newSource(t, st, WithRepositories("broken/pack", "owner/pack"))

	mods, err := s.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "one broken repository must not fail the search")

	assert.Len(t, mods, 1, "got %v, want the healthy repository's release", modIDs(mods))
}

func TestSearchFailsWhenEveryRepoFails(t *testing.T) {
	st := newStubServer(t) // every /releases call 404s
	s := newSource(t, st, WithRepositories("a/one", "a/two"))

	_, err := s.Search(context.Background(), domain.SearchQuery{})
	if err == nil {
		require.FailNow(t, "Search must fail when no repository could be read")
	}

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSearchFailed),
		"code = %s, want SEARCH_FAILED",
		appErrors.CodeOf(err),
	)
}

func TestSearchDiscoveredRepos(t *testing.T) {
	st := newStubServer(t)
	st.search = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"items": []map[string]string{{"full_name": "found/pack"}}})
	}
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(
			t,
			w,
			[]release{releaseFixture("v3.0.0", false, false, "https://example.test/a", "", 100)},
		)
	}
	s := newSource(t, st)

	mods, err := s.Search(context.Background(), domain.SearchQuery{Query: "release"})
	require.NoError(t, err, "Search")

	if len(mods) != 1 || mods[0].ID != ModID("found", "pack") {
		require.FailNow(t, "got %v, want the discovered repository's release", modIDs(mods))
	}

	if !st.sawRequest("topic%3Adarktide-mod") && !st.sawRequest("topic:darktide-mod") {
		assert.Fail(t, "discovery must always append the topic; requests were %v", st.requests)
	}
}

func TestSearchDiscoveredDropsInvalidRefsAndHonoursLimit(t *testing.T) {
	st := newStubServer(t)
	st.search = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"items": []map[string]string{
			{"full_name": "no-slash"},
			{"full_name": "too/many/slashes"},
			{"full_name": "one/pack"},
			{"full_name": "two/pack"},
			{"full_name": "three/pack"},
		}})
	}
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(
			t,
			w,
			[]release{releaseFixture("v1.0.0", false, false, "https://example.test/a", "", 100)},
		)
	}
	s := newSource(t, st, WithDiscoveryLimit(2))

	mods, err := s.Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	require.Len(t, mods, 2, "got %v, want the discovery limit honoured", modIDs(mods))

	for _, id := range modIDs(mods) {
		if id == "no-slash" || strings.Count(id, ".") == 0 {
			assert.Fail(t, "invalid repository ref leaked into the results: %q", id)
		}
	}
}

func TestSearchDiscoveryEndpointMissing(t *testing.T) {
	// GitHub answers a search that matches nothing with an empty items list, so a 404
	// really means the endpoint moved; an empty result list would hide that.
	st := newStubServer(t) // /search/repositories 404s

	_, err := newSource(t, st).Search(context.Background(), domain.SearchQuery{})
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"err = %v, want SOURCE_UNAVAILABLE",
		err,
	)
}

func TestSearchDiscoveryWithNoMatches(t *testing.T) {
	st := newStubServer(t)
	st.search = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"items": []any{}})
	}

	mods, err := newSource(t, st).Search(context.Background(), domain.SearchQuery{})
	require.NoError(t, err, "an empty discovery result is not an error")

	if mods == nil {
		assert.Fail(t, "an empty result must still be a non-nil slice")
	}

	assert.Empty(t, mods, "got %v, want nothing", modIDs(mods))
}

func TestSearchFailsWhenEveryDiscoveredRepoFails(t *testing.T) {
	st := newStubServer(t)
	st.search = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"items": []map[string]string{{"full_name": "found/pack"}}})
	}
	s := newSource(t, st)

	_, err := s.Search(context.Background(), domain.SearchQuery{})
	if err == nil {
		require.FailNow(t, "Search must fail when every discovered repository could not be read")
	}

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSearchFailed),
		"code = %s, want SEARCH_FAILED",
		appErrors.CodeOf(err),
	)
}

func TestSearchHonoursCancellation(t *testing.T) {
	st := newStubServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newSource(t, st, WithRepositories("a/b")).Search(ctx, domain.SearchQuery{})
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"err = %v, want OPERATION_CANCELED",
		err,
	)

	assert.Empty(
		t,
		st.requests,
		"a cancelled context must stop before any request; saw %v",
		st.requests,
	)
}

func TestSearchToleratesNilContext(t *testing.T) {
	st := newStubServer(t)
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(
			t,
			w,
			[]release{releaseFixture("v1.0.0", false, false, "https://example.test/a", "", 100)},
		)
	}
	//nolint:staticcheck // a nil context must not panic in a public API.
	if _, err := newSource(
		t,
		st,
		WithRepositories("a/b"),
	).Search(
		nil,
		domain.SearchQuery{},
	); err != nil {
		assert.Fail(t, "Search(nil ctx) = %v, want success", err)
	}
}

func TestGetMod(t *testing.T) {
	st := newStubServer(t)
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(
			t,
			w,
			[]release{releaseFixture("v1.4.0", false, false, "https://example.test/a", "", 100)},
		)
	}
	s := newSource(t, st)

	mod, err := s.GetMod(context.Background(), ModID("owner", "pack"))
	require.NoError(t, err, "GetMod")

	assert.Equal(t, "1.4.0", mod.Version, "version = %q, want 1.4.0", mod.Version)

	assert.True(
		t,
		st.sawRequest("per_page=1"),
		"GetMod should ask for a single release; requests were %v",
		st.requests,
	)
}

func TestGetModWithoutReleases(t *testing.T) {
	// "exists but has no releases" answers with an empty array and "no such
	// repository" with 404; both read as MOD_NOT_FOUND, not a raw code.
	st := newStubServer(t)

	st.releases = func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, []release{}) }
	_, err := newSource(
		t,
		st,
	).GetMod(
		context.Background(),
		ModID("owner", "pack"),
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeModNotFound,
	), "empty release list: err = %v, want MOD_NOT_FOUND", err)

	gone := newStubServer(t) // 404 on /releases
	_, err = newSource(
		t,
		gone,
	).GetMod(
		context.Background(),
		ModID("owner", "pack"),
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeModNotFound,
	), "missing repository: err = %v, want MOD_NOT_FOUND", err)
}

func TestGetModRejectsMalformedIDs(t *testing.T) {
	st := newStubServer(t)
	for _, id := range []string{"", "noseparator", ".leading", "trailing.", "with/slash", "own.er/repo"} {
		_, err := newSource(t, st).GetMod(context.Background(), id)
		assert.Error(t, err, "GetMod(%q) must fail", id)
	}

	assert.Empty(
		t,
		st.requests,
		"a malformed id must be refused before any request; saw %v",
		st.requests,
	)
}

func TestDownload(t *testing.T) {
	st := newStubServer(t)
	st.assetBody = []byte("mod payload")
	digest := sha256Of(st.assetBody)
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(
			t,
			w,
			[]release{
				releaseFixture(
					"v1.0.0",
					false,
					false,
					st.url()+"/asset/pack.zip",
					"sha256:"+digest,
					int64(len(st.assetBody)),
				),
			},
		)
	}
	s := newSource(t, st)
	dest := filepath.Join(t.TempDir(), "nested", "pack.zip")

	mod, err := s.GetMod(context.Background(), ModID("owner", "pack"))
	require.NoError(t, err, "GetMod")

	got, err := s.Download(context.Background(), mod, dest)
	require.NoError(t, err, "Download")

	assert.Equal(t, dest, got, "Download returned %q, want the destination %q", got, dest)

	body, err := os.ReadFile(dest)
	require.NoError(t, err, "read download")

	assert.Equal(
		t,
		string(st.assetBody),
		string(body),
		"downloaded %q, want %q",
		body,
		st.assetBody,
	)
}

func TestDownloadVerifiesAdvertisedDigest(t *testing.T) {
	st := newStubServer(t)
	st.assetBody = []byte("mod payload")
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(
			t,
			w,
			[]release{
				releaseFixture(
					"v1.0.0",
					false,
					false,
					st.url()+"/asset/pack.zip",
					"sha256:"+strings.Repeat("0", 64),
					12,
				),
			},
		)
	}
	s := newSource(t, st)
	mod, _ := s.GetMod(context.Background(), ModID("owner", "pack"))

	dest := filepath.Join(t.TempDir(), "pack.zip")
	_, err := s.Download(
		context.Background(),
		mod,
		dest,
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeIntegrity,
	), "err = %v, want INTEGRITY when the digest does not match", err)

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		assert.Fail(t, "a failed integrity check must not leave a file behind")
	}
}

func TestDownloadRejectsBadArguments(t *testing.T) {
	st := newStubServer(t)

	s := newSource(t, st)
	_, err := s.Download(
		context.Background(),
		nil,
		filepath.Join(t.TempDir(), "a.zip"),
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidMod,
	), "nil mod err = %v, want INVALID_MOD", err)

	mod := &domain.Mod{ID: ModID("owner", "pack"), SourceID: SourceID}
	_, err = s.Download(
		context.Background(),
		mod,
		"  ",
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	), "blank destination err = %v, want INVALID_ARGUMENT", err)
}

func TestDownloadWithoutAnArchiveAsset(t *testing.T) {
	st := newStubServer(t)
	st.releases = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []release{{TagName: "v1.0.0"}}) // no assets at all
	}
	s := newSource(t, st)
	mod := &domain.Mod{ID: ModID("owner", "pack"), SourceID: SourceID, Version: "1.0.0"}

	_, err := s.Download(context.Background(), mod, filepath.Join(t.TempDir(), "a.zip"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeDownloadFailed),
		"err = %v, want DOWNLOAD_FAILED",
		err,
	)
}

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pack.zip")

	body := []byte("payload")
	err := os.WriteFile(path, body, 0o644)
	require.NoError(t, err, "write: %v", err)

	digest := sha256Of(body)
	s := &Source{}

	err = s.Verify(context.Background(), nil, path)
	assert.NoError(t, err, "Verify(nil mod) = %v, want success", err)

	if err := s.Verify(
		context.Background(),
		&domain.Mod{SourceData: map[string]any{"sha256": ""}},
		path,
	); err != nil {
		assert.Fail(t, "Verify(no digest) = %v, want success: there is nothing to check", err)
	}

	upper := strings.ToUpper(digest)
	for _, stored := range []string{digest, upper, "sha256:" + digest, "sha256:" + upper} {
		mod := &domain.Mod{SourceData: map[string]any{"sha256": stored}}
		err := s.Verify(context.Background(), mod, path)
		assert.NoError(t, err, "Verify(%q) = %v, want success", stored, err)
	}

	wrong := &domain.Mod{SourceData: map[string]any{"sha256": strings.Repeat("a", 64)}}
	err = s.Verify(
		context.Background(),
		wrong,
		path,
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeIntegrity,
	), "mismatch err = %v, want INTEGRITY", err)

	err = s.Verify(
		context.Background(),
		wrong,
		filepath.Join(dir, "absent"),
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeCacheMiss,
	), "missing file err = %v, want CACHE_MISS", err)
}

func TestAuthenticate(t *testing.T) {
	st := newStubServer(t)
	st.user = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]string{"login": "octocat"})
	}

	s := newSource(t, st)
	if err := s.Authenticate(
		context.Background(),
		domain.AuthCredentials{},
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeAuthRequired,
	) {
		assert.Fail(t, "blank credentials err = %v, want AUTH_REQUIRED", err)
	}

	for _, key := range []string{"token", "api_key", "apiKey"} {
		fresh := newSource(t, st)
		if err := fresh.Authenticate(
			context.Background(),
			domain.AuthCredentials{key: "secret"},
		); err != nil {
			require.FailNow(t, "Authenticate via %q: %v", key, err)
		}

		assert.Equal(t, "secret", fresh.token, "token via %q = %q", key, fresh.token)
	}

	if len(st.bearer) == 0 || !strings.HasPrefix(st.bearer[0], "Bearer ") {
		assert.Fail(t, "the token must be sent as a bearer header; saw %v", st.bearer)
	}
}

func TestAuthenticateRejectsABadToken(t *testing.T) {
	st := newStubServer(t)
	st.user = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
	}

	err := newSource(
		t,
		st,
	).Authenticate(
		context.Background(),
		domain.AuthCredentials{"token": "bad"},
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeAuthFailed),
		"err = %v, want AUTH_FAILED",
		err,
	)
}

func TestValidateCredentials(t *testing.T) {
	st := newStubServer(t)
	// No token: anonymous access is valid, so no request is made at all.
	anon := newSource(t, st)
	err := anon.ValidateCredentials(context.Background())
	assert.NoError(t, err, "anonymous ValidateCredentials = %v, want success", err)

	assert.Empty(t, st.requests, "anonymous validation must not call the API; saw %v", st.requests)

	st.user = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]string{"login": "octocat"})
	}
	err = newSource(
		t,
		st,
		WithToken("secret"),
	).ValidateCredentials(
		context.Background(),
	)
	assert.NoError(t, err, "ValidateCredentials = %v, want success", err)

	st.user = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]string{})
	}

	err = newSource(t, st, WithToken("secret")).ValidateCredentials(context.Background())
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeAuthFailed),
		"an empty login means the token was not accepted: err = %v, want AUTH_FAILED",
		err,
	)

	st.user = nil // 404

	err = newSource(t, st, WithToken("secret")).ValidateCredentials(context.Background())
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeAuthFailed),
		"transport failure err = %v, want AUTH_FAILED",
		err,
	)
}

// sha256Of is the hex SHA256 of b, derived the way httpx derives it so a
// hard-coded constant cannot drift.
func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestForgetCredentialsStopsSendingTheBearer: signing out of GitHub has to mean
// the next request is anonymous, not a token that keeps acting as the user.
func TestForgetCredentialsStopsSendingTheBearer(t *testing.T) {
	st := newStubServer(t)
	st.user = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]string{"login": "octocat"})
	}

	s := newSource(t, st, WithToken("secret"))
	err := s.ValidateCredentials(context.Background())
	require.NoError(t, err, "ValidateCredentials: %v", err)

	s.ForgetCredentials()

	got := s.tokenIfAny()
	assert.Equal(t, "", got, "tokenIfAny() = %q after forgetting, want empty", got)

	before := len(st.bearer)

	err = s.ValidateCredentials(context.Background())
	require.NoError(t, err, "ValidateCredentials after signing out = %v, want success", err)
	// Anonymous access is valid, so validation short-circuits; what matters is
	// that nothing was sent on the strength of the forgotten token.
	assert.Len(t, st.bearer, before, "the sign-out sent %v", st.bearer[before:])

	assert.Len(
		t,
		st.requests,
		1,
		"signing out made %d requests, want the validation call to be the only one",
		len(st.requests),
	)
}

// ------------------------------------------ game defaults through the factory

// theFactory returns the factory the package registered from init: asserting on
// New alone would leave the game-default wiring untested.
func theFactory(t *testing.T) services.SourceFactory {
	t.Helper()

	factory, ok := services.SourceFactoryFor(SourceID)
	require.True(t, ok, "the %s factory must register itself from init", SourceID)

	return factory
}

func TestFactoryAppliesTheGamesTopicDefault(t *testing.T) {
	got, err := theFactory(t)(services.SourceBuildContext{
		Game: gamestest.Def{
			IDValue: "test-game",
			Defaults: map[string]map[string]string{
				SourceID: {"topic": "other-game-mod"},
			},
		},
	})
	require.NoError(t, err, "factory with a game default: %v", err)

	src, ok := got.(*Source)
	require.True(t, ok, "factory returned %T, want *Source", got)
	assert.Equal(t, "other-game-mod", src.topic,
		"the game's default must win over the source's own")
}

func TestFactoryLetsTheConfigBlockOverrideTheGamesTopic(t *testing.T) {
	got, err := theFactory(t)(services.SourceBuildContext{
		Game: gamestest.Def{
			Defaults: map[string]map[string]string{SourceID: {"topic": "from-game"}},
		},
		Config: domain.SourceConfig{Options: map[string]string{"topic": "from-config"}},
	})
	require.NoError(t, err, "factory with both a game default and a config value: %v", err)

	src, ok := got.(*Source)
	require.True(t, ok, "factory returned %T, want *Source", got)
	assert.Equal(t, "from-config", src.topic,
		"the user's configuration must win over the game's default")
}

func TestFallsBackToTheNativeTopicWithoutAGame(t *testing.T) {
	got, err := theFactory(t)(services.SourceBuildContext{})
	require.NoError(t, err, "factory with no game: %v", err)

	src, ok := got.(*Source)
	require.True(t, ok, "factory returned %T, want *Source", got)
	assert.Equal(t, SearchTopic, src.topic,
		"without a game the source's own default must apply")
}
