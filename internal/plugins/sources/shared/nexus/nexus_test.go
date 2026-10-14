package nexus

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games/gamestest"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The error codes this file asserts on, named once so a rename in pkg/errors
// fails here rather than as a silently passing comparison.
const (
	appCodeAuthFailed        = appErrors.ErrCodeAuthFailed
	appCodeAuthRequired      = appErrors.ErrCodeAuthRequired
	appCodeCanceled          = appErrors.ErrCodeOperationCanceled
	appCodeDownloadFailed    = appErrors.ErrCodeDownloadFailed
	appCodeInvalidArgument   = appErrors.ErrCodeInvalidArgument
	appCodeInvalidMod        = appErrors.ErrCodeInvalidMod
	appCodeModNotFound       = appErrors.ErrCodeModNotFound
	appCodeSearchFailed      = appErrors.ErrCodeSearchFailed
	appCodeSourceUnavailable = appErrors.ErrCodeSourceUnavailable
)

// isCode reports whether err carries the given application error code.
func isCode(err error, code appErrors.Code) bool {
	return appErrors.IsAppError(err, code)
}

// message renders an error's message, or the empty string for no error.
func message(err error) string {
	if err == nil {
		return ""
	}

	return appErrors.Message(err)
}

// The tests in this file drive the plugin against a local server, because the
// things worth checking about a transport are all about the wire: which verb
// reaches which path, which header is on which request, and what a status code
// turns into. Those cannot be observed through the pure conversions in
// models_test.go.

// stubServer answers the endpoints the plugin uses, with per-endpoint overrides
// so a test can put any endpoint into any state.
//
// Every branch honours its own status override. A stub that ignores the override
// it was given makes the test that sets it pass for an unrelated reason, which is
// how a status-mapping test ends up proving nothing at all.
type stubServer struct {
	srv                   *httptest.Server
	versionsBody          string
	versionBody           string
	dlLinkBody            string
	versionByScopedIDBody string
	trendingBody          string
	depBody               string
	modBody               string
	dlBody                string
	filesBody             string
	url                   string
	fileBody              string
	missingFileID         string
	// dlLinkFileID names the one file id the legacy download_link endpoint
	// authorizes. Blank authorizes whatever is asked for.
	dlLinkFileID            string
	requests                []string
	bodies                  []string
	assetBody               []byte
	appNames                []string
	keys                    []string
	trendingStatus          int
	versionStatus           int
	versionsStatus          int
	modStatus               int
	versionByScopedIDStatus int
	dlLinkStatus            int
	batchStatus             int
	filesStatus             int
	depStatus               int
	dlStatus                int
	fileStatus              int
	mu                      sync.Mutex
}

func newStubServer(t *testing.T) *stubServer {
	t.Helper()

	s := &stubServer{assetBody: []byte("nexus archive bytes")}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	s.url = s.srv.URL
	t.Cleanup(s.srv.Close)

	return s
}

// close stops the server so a test can reach the transport-failure path.
func (s *stubServer) close() { s.srv.Close() }

func (s *stubServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	s.mu.Lock()
	// The separator is written out rather than assumed: a request log that
	// concatenates the path and the query without it cannot tell a query string
	// from a path suffix, which is the difference between a mint that carried
	// its authorization and one that carried a mangled path.
	target := r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	s.requests = append(s.requests, r.Method+" "+target)
	s.keys = append(s.keys, r.Header.Get("apikey"))
	s.appNames = append(s.appNames, r.Header.Get("Application-Name"))
	s.bodies = append(s.bodies, string(body))
	s.mu.Unlock()

	path := r.URL.Path
	switch {
	case path == "/v3/mods/batch":
		// The body is checked before the status is applied, because a stub that
		// only watches statuses cannot tell a plugin that encodes its request
		// correctly from one that does not: the credential probe once handed
		// pre-encoded bytes to a client that encoded them again, and every test
		// here passed while the live API answered 422 with a body shape this
		// plugin has never written. Mirroring the real endpoint is what makes
		// that class of bug fail here instead of in a user's terminal.
		if detail := batchBodyProblem(body); detail != "" {
			s.writeJSON(w, http.StatusUnprocessableEntity,
				`{"type":"about:blank","title":"Unprocessable Content","status":422,`+
					`"detail":"`+detail+`","instance":"/v3/mods/batch"}`)

			break
		}

		s.writeJSON(w, s.batchStatus, `{"data":{"mods":[]}}`)
	case path == "/v3/games/darktide/trending-mods":
		s.writeJSON(w, s.trendingStatus, s.trendingBody)
	case strings.HasSuffix(path, "/mods/900"):
		s.writeJSON(w, s.modStatus, s.modBody)
	case strings.HasPrefix(path, "/v3/games/darktide/mods/"):
		// Any other mod the feed names is answered with a record echoing its own
		// id back, so a test about the feed does not depend on which mod happens to
		// be hard-coded here — and so two feed entries cannot collapse into one.
		id := strings.TrimPrefix(path, "/v3/games/darktide/mods/")
		s.writeJSON(w, s.modStatus, `{"data":{"id":"uid-`+id+`","game_scoped_id":"`+id+
			`","game_id":"3834","name":"Mod `+id+`"}}`)
	case strings.HasSuffix(path, "/files"):
		s.writeJSON(w, s.filesStatus, s.filesBody)
	case strings.HasSuffix(path, "/download-repacked"):
		s.writeJSON(w, s.dlStatus, s.dlBody)
	case strings.HasSuffix(path, "/download_link"):
		// Which number this endpoint accepts is not the manager's business: the
		// same path has answered for the number a pasted link carries and for the
		// version's game-scoped id. The fixture can therefore name the one id it
		// authorizes and refuse every other, which is what the fallback needs to
		// be tested against.
		if want, got := s.dlLinkFileID, fileIDOfDownloadLink(path); want != "" && got != want {
			s.writeJSON(w, http.StatusNotFound,
				`{"code":404,"message":"No File found for mod"}`)

			break
		}

		s.writeJSON(w, s.dlLinkStatus, s.dlLinkBody)
	case strings.HasSuffix(path, "/dependencies"):
		s.writeJSON(w, s.depStatus, s.depBody)
	// The single-version read is the only path that is exactly
	// /v3/mod-file-versions/{id}; every other endpoint under that prefix has a
	// further segment, and a prefix match tested before the suffixes above would
	// swallow them.
	case strings.HasPrefix(path, "/v3/mod-file-versions/"):
		s.writeJSON(w, s.versionStatus, s.versionBody)
	// A version read by the legacy file number a pasted link carries. It is the
	// one read that also names the mod file, which is what makes such a link
	// resolvable, so it is routed separately from the read by v3 version id.
	case strings.Contains(path, "/games/") && strings.Contains(path, "/mod-file-versions/"):
		s.writeJSON(w, s.versionByScopedIDStatus, s.versionByScopedIDBody)
	case strings.HasSuffix(path, "/versions"):
		s.writeJSON(w, s.versionsStatus, s.versionsBody)
	case strings.Contains(path, "/mod-files/"):
		status := s.fileStatus
		if s.missingFileID != "" && strings.HasSuffix(path, "/"+s.missingFileID) {
			status = http.StatusNotFound
		}

		s.writeJSON(w, status, s.fileBody)
	case path == "/asset":
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(s.assetBody)
	default:
		// Anything unrouted is a 404 with the API's own problem shape, so a
		// request the plugin should not have made is reported as such rather than
		// quietly answered.
		s.writeJSON(
			w,
			http.StatusNotFound,
			`{"type":"about:blank","title":"Not Found","status":404,"detail":"no stub for `+path+`","instance":"`+path+`"}`,
		)
	}
}

// writeJSON writes body with the given status, defaulting to 200.
// fileIDOfDownloadLink pulls the file id out of a legacy download_link path.
//
// The path is /v1/games/{domain}/mods/{mod}/files/{id}/download_link, so the id
// is read from the end rather than counted from the front.
func fileIDOfDownloadLink(path string) string {
	_, rest, found := strings.Cut(path, "/files/")
	if !found {
		return ""
	}

	id, _, _ := strings.Cut(rest, "/download_link")

	return id
}

func (s *stubServer) writeJSON(w http.ResponseWriter, status int, body string) {
	if status == 0 {
		status = http.StatusOK
	}

	if body == "" {
		// An empty answer is what a proxy returns on a bad gateway, and it must
		// not be mistaken for a successful one.
		body = `{"type":"about:blank","title":"Empty","status":500,"detail":"stub had no body","instance":"/v3/"}`
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// callLog returns a copy of the recorded requests, so a test can read them after
// the calls it made have all returned.
func (s *stubServer) callLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.requests...)
}

func (s *stubServer) keyLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.keys...)
}

func (s *stubServer) appNameLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.appNames...)
}

func (s *stubServer) bodyLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.bodies...)
}

func (s *stubServer) countOf(want string) int {
	n := 0

	for _, entry := range s.callLog() {
		if entry == want {
			n++
		}
	}

	return n
}

func (s *stubServer) saw(want string) bool { return s.countOf(want) > 0 }

// batchBodyProblem returns the complaint POST /mods/batch makes about body, or the
// empty string when the body is one the endpoint accepts. The wording is the
// live API's own, so a test that trips over one here trips over the same text a
// user would.
//
// The accepted shape is an object carrying mod_ids as a non-empty array of
// strings. Everything else — a bare array, a bare string, a number, or bytes that
// were encoded once too often and arrived as a quoted base64 string — decodes
// into neither, which is exactly how the real endpoint turns it away.
func batchBodyProblem(body []byte) string {
	var request struct {
		ModIDs []string `json:"mod_ids"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "Invalid JSON input. Require object with parameters as keys."
	}

	if len(request.ModIDs) == 0 {
		return "#/components/schemas/ModsBatchRequest missing required parameters: mod_ids"
	}

	return ""
}

// ---------------------------------------------------------------------------
// Fixtures. Each is the exact shape the specification declares for its endpoint,
// including the wrappers, because a fixture that omits one would let a decoding
// bug pass.
// ---------------------------------------------------------------------------

func modFixtureJSON() string {
	return `{"data":{"id":"1234567890123","game_scoped_id":"900","game_id":"3834","name":"Better UI"}}`
}

func filesFixtureJSON() string {
	return `{"data":{"mod_files":[
		{"id":"7001","name":"Better UI Main","is_active":true,
		 "last_file_uploaded_at":"2026-02-01T10:00:00Z","versions_count":2,
		 "archived_count":0,"removed_count":0},
		{"id":"7002","name":"Better UI Optional","is_active":true,
		 "last_file_uploaded_at":null,"versions_count":1,
		 "archived_count":0,"removed_count":0}]}}`
}

func fileFixtureJSON() string {
	return `{"data":{"id":"7001","name":"Better UI Main","is_active":true,
		"last_file_uploaded_at":"2026-02-01T10:00:00Z","versions_count":2,
		"archived_count":0,"removed_count":0}}`
}

func versionsFixtureJSON() string {
	// 8003 is old_version — a release the author has superseded. That is the one
	// category the manager refuses, because a file's newest version is often
	// exactly that, and installing it in place of the current one would be a
	// downgrade the user never asked for. A category the manager has never heard
	// of is offered rather than skipped; see TestPickVersionOffersAnUnrecognisedCategory.
	return `{"data":{"versions":[
		{"id":"8001","file":{"id":"7001","name":"Better UI Main"},"position":"1.0",
		 "game_scoped_id":"900","name":"Better UI 1.0.0","version":"1.0.0",
		 "category":"main","uploaded_at":"2024-01-01T00:00:00Z","is_primary":true},
		{"id":"8002","file":{"id":"7001","name":"Better UI Main"},"position":"2.0",
		 "game_scoped_id":"900","name":"Better UI 1.2.0","version":"1.2.0",
		 "category":"main","uploaded_at":"2026-02-01T09:30:00Z","is_primary":false},
		{"id":"8003","file":{"id":"7001","name":"Better UI Main"},"position":"3.0",
		 "game_scoped_id":"900","name":"Better UI 1.3.0","version":"1.3.0",
		 "category":"old_version","uploaded_at":"2026-06-01T00:00:00Z","is_primary":false}]}}`
}

func dependenciesFixtureJSON() string {
	// No "data" wrapper: this endpoint answers with the schema at the top level.
	return `{"dependency_definitions":[{"id":"d1","ranges":[
		{"id":"r1","min_version":"1.0","max_version":null,
		 "target_mod_file":{"id":"8001","name":"Requirement",
			"mod":{"id":"999","game_scoped_id":"950","name":"Requirement Mod",
			"game":{"id":"3834","name":"Darktide","domain_name":"darktide"}}}}]}],
		"dlc_dependency_definitions":[{"id":"l1","dlc_targets":[
		{"id":"1","dlc_id":"10","name":"Some DLC"}]}]}`
}

func trendingFixtureJSON() string {
	return `{"data":{"mods":[
		{"name":"Better UI","author":"modder","summary":"UI tweaks",
		 "picture_url":"https://images.invalid/better.png",
		 "mod_page_url":"https://www.nexusmods.com/darktide/mods/900"},
		{"name":"WIP","author":"someone","summary":"unfinished",
		 "picture_url":null,
		 "mod_page_url":"https://www.nexusmods.com/darktide/mods/901"}]}}`
}

func downloadFixtureJSON(assetURL string) string {
	body, _ := json.Marshal(map[string]string{
		"download_url": assetURL,
		"expires_at":   "2026-02-01T11:00:00Z",
	})

	return string(body)
}

func versionFixtureJSON() string {
	return `{"data":{"id":"8001","file":{"id":"7001","name":"Better UI Main"},
	 "position":"2.0","game_scoped_id":"8847","name":"Better UI 1.2.0",
	 "version":"1.2.0","category":"main","uploaded_at":"2026-02-01T09:30:00Z",
	 "is_primary":true}}`
}

// downloadLinkFixtureJSON offers two mirrors, the second with no URI, because a
// list that is present is not a list that is usable: the first usable mirror is
// the one a manager can act on.
func downloadLinkFixtureJSON(assetURL string) string {
	return `[{"name":"Nexus Global Content Delivery Network","short_name":"Nexus CDN",` +
		`"URI":"` + assetURL + `"},` +
		`{"name":"Nexus EU Content Delivery Network","short_name":"Nexus EU CDN","URI":"  "}]`
}

// newSource builds a source pointed at the stub, with a key already stored so
// most tests are authenticated. Tests about the absence of a key build their own
// with an empty config.
func newSource(t *testing.T, s *stubServer, opts ...Option) *Source {
	t.Helper()

	all := append([]Option{WithAPIBase(s.url + "/v3"), WithGameDomain("darktide")}, opts...)

	src, err := New(domain.SourceConfig{APIKey: "key-1"}, all...)
	require.NoError(t, err, "New")

	return src
}

// armedStub returns a stub with every fixture in place, which is the starting
// point for most tests.
func armedStub(t *testing.T) *stubServer {
	t.Helper()
	s := newStubServer(t)
	s.batchStatus = http.StatusOK
	s.trendingBody = trendingFixtureJSON()
	s.modBody = modFixtureJSON()
	s.filesBody = filesFixtureJSON()
	s.fileBody = fileFixtureJSON()
	s.versionsBody = versionsFixtureJSON()
	s.depBody = dependenciesFixtureJSON()

	return s
}

func TestIdentityAndCapabilities(t *testing.T) {
	src := newSource(t, armedStub(t))
	assert.Equal(t, SourceID, src.ID(), "ID() = %q, want %q", src.ID(), SourceID)

	assert.Equal(t, "Nexus Mods", src.Name(), "Name() = %q, want Nexus Mods", src.Name())

	if src.Description() == "" || src.Version() == "" {
		assert.Fail(t, "Description() and Version() must both answer something")
	}

	if !src.SupportsSearch() {
		assert.Fail(t, "SupportsSearch() = false, want true: the trending feed is public")
	}

	if !src.SupportsAuth() {
		assert.Fail(t, "SupportsAuth() = false, want true: a user can supply an API key")
	}
	// Only three endpoints are public, so a source with no key can show five
	// trending mods and install none of them.
	if !src.RequiresAPIKey() {
		assert.Fail(
			t,
			"RequiresAPIKey() = false, want true: everything needed to install needs a key",
		)
	}
}

func TestNewAppliesConfigurationOverOptions(t *testing.T) {
	// The configuration file is the user's own statement and wins over whatever
	// the wiring layer passed in.
	stub := newStubServer(t)

	src, err := New(domain.SourceConfig{
		APIKey: "key-1",
		Options: map[string]string{
			optionAPIBase:    stub.url + "/v3/",
			optionGameDomain: "skyrimspecialedition",
		},
	}, WithGameDomain("darktide"), WithAPIBase("https://example.invalid/v3"))
	require.NoError(t, err, "New")

	want := stub.url + "/v3"
	assert.Equal(
		t,
		want,
		src.apiBase,
		"apiBase = %q, want %q: the configuration wins and its trailing slash is trimmed",
		src.apiBase,
		want,
	)

	assert.Equal(
		t,
		"skyrimspecialedition",
		src.gameDomain,
		"gameDomain = %q, want the configuration's value",
		src.gameDomain,
	)

	assert.Equal(t, "key-1", src.apiKey, "apiKey = %q, want the configuration's key", src.apiKey)
}

func TestNewAppliesAValidOptionWhenTheConfigurationIsSilent(t *testing.T) {
	stub := newStubServer(t)

	src, err := New(domain.SourceConfig{}, WithAPIBase(stub.url+"/v3/"), WithGameDomain("fallout4"))
	require.NoError(t, err, "New")

	want := stub.url + "/v3"
	assert.Equal(t, want, src.apiBase, "apiBase = %q, want %q", src.apiBase, want)

	assert.Equal(
		t,
		"fallout4",
		src.gameDomain,
		"gameDomain = %q, want fallout4 from the option",
		src.gameDomain,
	)
}

func TestNewKeepsDefaultsForUnusableConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		options map[string]string
		apiKey  string
	}{
		{"blank api base", map[string]string{optionAPIBase: "   "}, ""},
		{"game domain with a slash", map[string]string{optionGameDomain: "dark/tide"}, ""},
		{"game domain with a dot", map[string]string{optionGameDomain: "darktide.mods"}, ""},
		{"game domain too long", map[string]string{optionGameDomain: strings.Repeat("a", 65)}, ""},
		{"blank game domain", map[string]string{optionGameDomain: "  "}, ""},
		{"non-numeric timeout", map[string]string{optionTimeout: "soon"}, ""},
		{"zero timeout", map[string]string{optionTimeout: "0"}, ""},
		{"negative timeout", map[string]string{optionTimeout: "-5"}, ""},
		{"blank key in api_key option", map[string]string{"api_key": "  "}, ""},
	}
	for _, tc := range cases {
		src, err := New(domain.SourceConfig{APIKey: tc.apiKey, Options: tc.options})
		if err != nil {
			assert.Fail(t,
				"%s: New returned %v; an unusable value must fall back to the default, not fail",
				tc.name,
				err,
			)

			continue
		}

		assert.Equal(
			t,
			DefaultAPIBase,
			src.apiBase,
			"%s: apiBase = %q, want the default",
			tc.name,
			src.apiBase,
		)

		assert.Equal(
			t,
			DefaultGameDomain,
			src.gameDomain,
			"%s: gameDomain = %q, want the default",
			tc.name,
			src.gameDomain,
		)

		assert.Equal(
			t,
			"",
			src.apiKey,
			"%s: apiKey = %q, want empty: a blank key is not a credential",
			tc.name,
			src.apiKey,
		)
	}
}

func TestNewRefusesAnUnusableAPIBase(t *testing.T) {
	// Falling back to the production endpoint would send the user's requests
	// somewhere they did not ask for, which is worse than refusing to start.
	for _, base := range []string{
		"not a url", "ftp://nexus.invalid", "file:///etc",
		"://missing-scheme", "https://", "://",
	} {
		src, err := New(domain.SourceConfig{Options: map[string]string{optionAPIBase: base}})
		assert.True(
			t,
			isCode(err, appCodeInvalidArgument),
			"api_base %q gave %v, want ErrCodeInvalidArgument",
			base,
			err,
		)

		assert.Nil(t, src, "api_base %q returned a source alongside its error", base)
	}
	// An http or https base with a host is accepted.
	for _, base := range []string{
		"http://127.0.0.1:8080/v3", "https://api.nexusmods.com/v3",
		"HTTPS://api.nexusmods.com/v3",
	} {
		if _, err := New(
			domain.SourceConfig{Options: map[string]string{optionAPIBase: base}},
		); err != nil {
			assert.Fail(t, "api_base %q was refused: %v", base, err)
		}
	}
}

func TestNewReadsTheKeyFromTheOptionsBlock(t *testing.T) {
	// A configuration written by hand often puts the key in the options map
	// rather than in the dedicated field.
	src, err := New(domain.SourceConfig{Options: map[string]string{"api_key": "from-options"}})
	require.NoError(t, err, "New")

	assert.Equal(t, "from-options", src.apiKey, "apiKey = %q, want from-options", src.apiKey)
	// The dedicated field wins: it is the one the manager writes.
	src, err = New(
		domain.SourceConfig{APIKey: "primary", Options: map[string]string{"api_key": "secondary"}},
	)
	require.NoError(t, err, "New")

	assert.Equal(
		t,
		"primary",
		src.apiKey,
		"apiKey = %q, want primary: the dedicated field is the one the manager writes",
		src.apiKey,
	)
}

func TestValidGameDomain(t *testing.T) {
	for _, ok := range []string{"darktide", "skyrimspecialedition", "warhammer40000", "Darktide", "a_b", "A1", "x"} {
		assert.True(t, validGameDomain(ok), "validGameDomain(%q) = false, want true", ok)
	}
	// A domain is interpolated into a request path, so anything that could change
	// which request is made has to be refused rather than escaped.
	for _, bad := range []string{
		"", " ", "dark/tide", "dark tide", "dark.tide", "dark;tide",
		"dark?x=1", "dark#frag", "../darktide", "darktide/../other", "dark\\tide",
		strings.Repeat("a", 65),
	} {
		assert.False(t, validGameDomain(bad), "validGameDomain(%q) = true, want false", bad)
	}
}

func TestOptionsIgnoreBlankInput(t *testing.T) {
	base := newSource(t, armedStub(t))
	WithAPIBase("   ")(base)
	WithGameDomain("   ")(base)
	WithTimeout(0)(base)
	WithTimeout(-time.Second)(base)

	if base.apiBase == "" || base.gameDomain == "" {
		assert.Fail(t, "a blank option must leave the value alone rather than blanking it")
	}

	if base.client == nil {
		assert.Fail(t, "WithTimeout with a non-positive duration must leave the client alone")
	}
}

func TestValidateCredentialsWithoutAKeyMakesNoRequest(t *testing.T) {
	// The trending feed is public, so a user with no credential is browsing
	// rather than broken. Collecting a 401 to say so would be wrong.
	s := armedStub(t)
	src := newSource(t, s, WithGameDomain("darktide"))

	src.apiKey = ""
	err := src.ValidateCredentials(t.Context())
	assert.NoError(
		t,
		err,
		"ValidateCredentials with no key = %v, want nil: the public feed still works",
		err,
	)

	calls := s.callLog()
	assert.Empty(
		t,
		calls,
		"ValidateCredentials with no key made %v; it should have asked Nexus nothing",
		calls,
	)

	if src.Authenticated() {
		assert.Fail(t, "Authenticated() = true with no key stored")
	}
}

// TestBatchBodyProblemRefusesAnOverEncodedBody is the regression test for the bug
// this file's stub used to hide. The credential probe handed already-encoded
// bytes to a client that encoded them again, and the double encoding arrived as a
// quoted base64 string; the live API answered 422 with a complaint about a body
// shape this plugin has never written. Asserting the acceptance boundary here
// means that mistake now fails in a unit test rather than in a user's terminal.
func TestBatchBodyProblemRefusesAnOverEncodedBody(t *testing.T) {
	encoded, err := json.Marshal(modsBatchRequest{ModIDs: []string{credentialProbeID}})
	require.NoError(t, err, "marshal the documented probe")

	detail := batchBodyProblem(encoded)
	assert.Equal(t, "", detail, "the documented probe body was refused: %s", detail)

	overEncoded := `"` + base64.StdEncoding.EncodeToString(encoded) + `"`
	detail = batchBodyProblem([]byte(overEncoded))
	assert.NotEqual(
		t,
		"",
		detail,
		"body %s was accepted, so the probe can regress into double encoding unnoticed",
		overEncoded,
	)

	for _, body := range []string{`["0"]`, `"0"`, `0`, `null`, `{}`, `[]`, `{"mod_ids":[]}`, `{"mod_ids":[0]}`} {
		detail := batchBodyProblem([]byte(body))
		assert.NotEqual(t, "", detail, "body %s was accepted, want a complaint", body)
	}
}

func TestValidateCredentialsProbesWithTheKey(t *testing.T) {
	s := armedStub(t)

	src := newSource(t, s)
	err := src.ValidateCredentials(t.Context())
	require.NoError(t, err, "ValidateCredentials: %v", err)

	assert.True(
		t,
		s.saw("POST /v3/mods/batch"),
		"ValidateCredentials did not probe the credential; it made %v",
		s.callLog(),
	)

	for i, key := range s.keyLog() {
		assert.Equal(
			t,
			"key-1",
			key,
			"request %d carried apikey %q, want key-1: the header name is lower case and unprefixed",
			i,
			key,
		)
	}
	// The probe body must be the documented object — an id that does not exist, so
	// a good key answers with an empty list rather than a 404 that could also mean a
	// hidden key. Comparing the decoded value rather than searching the raw text
	// keeps a body that merely mentions the id, such as a quoted base64 blob, from
	// passing.
	bodies := s.bodyLog()
	require.NotEmpty(t, bodies, "probe sent no body; it made %v", s.callLog())

	var probe struct {
		ModIDs []string `json:"mod_ids"`
	}

	err = json.Unmarshal([]byte(bodies[0]), &probe)
	require.NoError(
		t,
		err,
		"probe body = %q, which is not the object the endpoint documents: %v",
		bodies[0],
		err,
	)

	if len(probe.ModIDs) != 1 || probe.ModIDs[0] != credentialProbeID {
		assert.Fail(t,
			"probe body = %q, want exactly the non-existent id %q",
			bodies[0],
			credentialProbeID,
		)
	}

	if !src.Authenticated() {
		assert.Fail(t, "Authenticated() = false after a key was accepted")
	}
}

func TestValidateCredentialsReportsARejectedKey(t *testing.T) {
	s := armedStub(t)
	s.batchStatus = http.StatusUnauthorized
	src := newSource(t, s)

	err := src.ValidateCredentials(t.Context())
	if err == nil {
		require.FailNow(t, "ValidateCredentials accepted a key the API refused")
	}
	// A key was supplied and was refused, so it is wrong rather than missing.
	assert.True(
		t,
		isCode(err, appCodeAuthFailed),
		"error = %v, want ErrCodeAuthFailed: reporting a refused key as missing sends the user to a field they filled in",
		err,
	)

	if src.Authenticated() {
		assert.Fail(
			t,
			"a refused key was kept: the next attempt would start from what was rejected",
		)
	}
}

func TestAuthenticateRefusesABlankKeyWithoutARequest(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)

	src.apiKey = ""
	for _, creds := range []domain.AuthCredentials{nil, {}, {"api_key": "  "}, {"token": ""}, {"key": "\t"}} {
		err := src.Authenticate(t.Context(), creds)
		assert.True(
			t,
			isCode(err, appCodeAuthRequired),
			"Authenticate(%v) = %v, want ErrCodeAuthRequired",
			creds,
			err,
		)
	}

	calls := s.callLog()
	assert.Empty(
		t,
		calls,
		"Authenticate with a blank key made %v; there was nothing to authenticate",
		calls,
	)
}

func TestAuthenticateAcceptsEveryKeySpelling(t *testing.T) {
	for _, key := range []string{"api_key", "apikey", "apiKey", "key", "token"} {
		s := armedStub(t)
		src := newSource(t, s)

		src.apiKey = ""
		if err := src.Authenticate(
			t.Context(),
			domain.AuthCredentials{key: "typed-key"},
		); err != nil {
			assert.Fail(t, "Authenticate with %q: %v", key, err)
			continue
		}

		assert.Equal(
			t,
			"typed-key",
			src.apiKey,
			"Authenticate with %q stored %q, want typed-key",
			key,
			src.apiKey,
		)
	}
}

func TestAuthenticateReportsARejectedKeyAndForgetsIt(t *testing.T) {
	s := armedStub(t)
	s.batchStatus = http.StatusForbidden
	src := newSource(t, s)
	src.apiKey = ""

	err := src.Authenticate(t.Context(), domain.AuthCredentials{"api_key": "wrong"})
	assert.True(
		t,
		isCode(err, appCodeAuthFailed),
		"Authenticate with a refused key = %v, want ErrCodeAuthFailed",
		err,
	)

	if src.Authenticated() {
		assert.Fail(t, "Authenticate kept a key the API refused")
	}
}

func TestGetModWalksTheResourcesInOrder(t *testing.T) {
	// The order matters: the version list is what decides whether there is
	// anything to install, so it is asked before the mod's own record.
	s := armedStub(t)
	src := newSource(t, s)

	mod, err := src.GetMod(t.Context(), ModID("900", "7001"))
	require.NoError(t, err, "GetMod")

	if mod == nil {
		require.FailNow(t, "GetMod returned no mod")
	}

	assert.Equal(t, "nexus-900-7001", mod.ID, "mod.ID = %q, want nexus-900-7001", mod.ID)
	// The fixture's newest installable version is 1.2.0; the 1.3.0 entry is
	// superseded (old_version) and must not be offered.
	assert.Equal(
		t,
		"1.2.0",
		mod.Version,
		"mod.Version = %q, want 1.2.0: the newest installable version, not the superseded one",
		mod.Version,
	)

	assert.Equal(t, "Better UI", mod.Name, "mod.Name = %q, want the mod record's name", mod.Name)

	want := []string{
		"GET /v3/mod-files/7001",
		"GET /v3/mod-files/7001/versions",
		"GET /v3/games/darktide/mods/900",
		"GET /v3/mod-file-versions/8002/dependencies",
	}

	got := s.callLog()
	require.Len(t, got, len(want), "GetMod made %v, want %v", got, want)

	for i := range want {
		assert.Equal(t, want[i], got[i], "request %d = %q, want %q", i, got[i], want[i])
	}
}

func TestGetModReadsTheDependencyResponse(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)

	mod, err := src.GetMod(t.Context(), ModID("900", "7001"))
	require.NoError(t, err, "GetMod")
	// The dependency endpoint has no "data" wrapper, so decoding it into the
	// wrong shape yields nothing rather than an error.
	want := []string{"nexus-950-8001"}
	if len(mod.Dependencies) != 1 || mod.Dependencies[0] != want[0] {
		assert.Fail(t,
			"mod.Dependencies = %v, want %v: the dependency response is unwrapped",
			mod.Dependencies,
			want,
		)
	}

	if got := mod.SourceData["dlc_dependencies"]; got == nil {
		assert.Fail(t, "SourceData carries no dlc_dependencies although the response named one")
	}

	if _, ok := mod.SourceData["dependency_error"]; ok {
		assert.Fail(t, "SourceData records a dependency_error although the read succeeded")
	}
}

func TestGetModRecordsADependencyFailureInsteadOfFailing(t *testing.T) {
	// The mod is installable either way. Losing the distinction between "needs
	// nothing" and "could not be read" would make a conflict report look
	// authoritative when it is not.
	s := armedStub(t)
	s.depStatus = http.StatusInternalServerError
	src := newSource(t, s)

	mod, err := src.GetMod(t.Context(), ModID("900", "7001"))
	require.NoError(t, err, "GetMod: %v; a dependency read must not fail the whole fetch")

	message, ok := mod.SourceData["dependency_error"].(string)
	if !ok || message == "" {
		assert.Fail(t,
			"SourceData[dependency_error] = %#v, want the reason as text",
			mod.SourceData["dependency_error"],
		)
	}

	assert.Empty(
		t,
		mod.Dependencies,
		"mod.Dependencies = %v, want none: nothing was read",
		mod.Dependencies,
	)
}

func TestGetModRefusesAMalformedIDWithoutARequest(t *testing.T) {
	s := armedStub(t)

	src := newSource(t, s)
	for _, id := range []string{
		"", "   ", "900", "nexus-900", "nexus-900-", "nexus-",
		"nexus-abc-7001", "github-900-7001", "nexus-900-7001-5", "-900-7001",
	} {
		mod, err := src.GetMod(t.Context(), id)
		assert.True(
			t,
			isCode(err, appCodeInvalidArgument),
			"GetMod(%q) = %v, want ErrCodeInvalidArgument",
			id,
			err,
		)

		assert.Nil(t, mod, "GetMod(%q) returned a mod alongside its error", id)
	}

	calls := s.callLog()
	assert.Empty(
		t,
		calls,
		"a malformed id made %v; nothing should have been asked of the API",
		calls,
	)
}

func TestGetModReportsAMissingFile(t *testing.T) {
	s := armedStub(t)
	s.fileStatus = http.StatusNotFound
	src := newSource(t, s)

	mod, err := src.GetMod(t.Context(), ModID("900", "7001"))
	assert.True(
		t,
		isCode(err, appCodeModNotFound),
		"GetMod with a missing file = %v, want ErrCodeModNotFound",
		err,
	)

	if mod != nil {
		assert.Fail(t, "GetMod returned a mod for a file the API does not have")
	}
	// A file the v3 endpoint does not have is then asked about as the legacy
	// number a pasted link carries, because the two numberings differ and only
	// the link knows which one it has. Both failing is still "no such file"; what
	// must not happen is going on to ask about versions of a file that is not
	// there.
	want := []string{
		"GET /v3/mod-files/7001",
		"GET /v3/games/darktide/mod-file-versions/7001",
	}

	got := s.callLog()
	require.Len(t, got, len(want), "GetMod made %v, want %v", got, want)

	for i := range want {
		assert.Equal(t, want[i], got[i], "request %d = %q, want %q", i, got[i], want[i])
	}
}

// TestGetModResolvesALegacyFileIDFromALink covers the reason the file half of a
// mod id is read through modFileForID.
//
// A link pasted from the website numbers a file the legacy way, and that number
// is not the v3 mod file id: the two differ for the same file, and only the v3
// id is what the file endpoint understands. The legacy number is a version's
// game-scoped id, and one request against the version endpoint turns it into
// the v3 id — which is also the id the mod ends up stored under, so a mod
// installed from a link and a mod installed from a stored id are the same entry.
func TestGetModResolvesALegacyFileIDFromALink(t *testing.T) {
	s := armedStub(t)
	s.missingFileID = "7001"
	s.versionByScopedIDBody = `{"data":{"id":"8002","position":"2.0",` +
		`"game_scoped_id":"7001","name":"BossThresholds 0.3.0","version":"2",` +
		`"category":"main","uploaded_at":"2026-10-07T03:49:20Z","is_primary":true,` +
		`"file":{"id":"8094444","name":"BossThresholds 0.2.0"}}}`
	s.fileBody = `{"data":{"id":"8094444","name":"BossThresholds 0.2.0",` +
		`"is_active":true,"versions_count":1}}`
	s.versionsBody = `{"data":{"versions":[` +
		`{"id":"8002","position":"2.0","game_scoped_id":"7001","name":"BossThresholds 0.3.0",` +
		`"version":"2","category":"main","uploaded_at":"2026-10-07T03:49:20Z","is_primary":true}]}}`
	src := newSource(t, s)

	mod, err := src.GetMod(t.Context(), ModID("1419", "7001"))
	require.NoError(t, err, "GetMod from a legacy file id = %v, want no error")

	if mod == nil {
		require.FailNow(t, "GetMod returned no mod for a legacy file id the API does have")
	}
	// The mod is stored under the v3 file id, not the legacy number that was
	// pasted: the two differ, and the manager's identity for a mod should not
	// depend on which link happened to name it.
	wantID := ModID("1419", "8094444")
	assert.Equal(t, wantID, mod.ID, "mod id = %q, want %q", mod.ID, wantID)

	// The link named one specific file version, and the lookup that resolved it
	// already read that version. The version list is not consulted: re-picking
	// from it would install a release the site's one-time grant never authorised,
	// and would fail outright on a file whose newest entry is superseded.
	want := []string{
		"GET /v3/mod-files/7001",
		"GET /v3/games/darktide/mod-file-versions/7001",
		"GET /v3/mod-files/8094444",
		"GET /v3/games/darktide/mods/1419",
		"GET /v3/mod-file-versions/8002/dependencies",
	}

	got := s.callLog()
	require.Len(t, got, len(want), "GetMod made %v, want %v", got, want)

	for i := range want {
		assert.Equal(t, want[i], got[i], "request %d = %q, want %q", i, got[i], want[i])
	}

	for _, call := range got {
		assert.NotContains(
			t,
			call,
			"/versions",
			"GetMod re-listed the file's versions: %v",
			got,
		)
	}
}

func TestGetModReportsAFileWithNoInstallableVersion(t *testing.T) {
	s := armedStub(t)
	s.versionsBody = `{"data":{"versions":[
		{"id":"8003","position":"3.0","game_scoped_id":"900","name":"Old",
		 "version":"1.3.0","category":"old_version","uploaded_at":"2026-06-01T00:00:00Z"}]}}`
	src := newSource(t, s)

	_, err := src.GetMod(t.Context(), ModID("900", "7001"))
	assert.True(
		t,
		isCode(err, appCodeModNotFound),
		"GetMod with only a superseded version = %v, want ErrCodeModNotFound",
		err,
	)

	assert.Contains(
		t,
		message(err),
		"downloadable",
		"error %q should say the file has no downloadable version",
		message(err),
	)

	// "has no downloadable version" on its own sent the user looking at the
	// Nexus site for an explanation the API already had. The categories are the
	// explanation, so they are in the message.
	assert.Contains(
		t,
		message(err),
		"old_version",
		"error %q should name the categories it refused",
		message(err),
	)
}

// A file whose versions all carry a category this build has never heard of must
// still resolve. This is the case that produced "has no downloadable version"
// on a perfectly good link: the filter refused everything it did not recognise,
// so the candidate set came back empty and the grant went unused.
func TestGetModAcceptsAnUnrecognisedCategory(t *testing.T) {
	s := armedStub(t)
	s.versionsBody = `{"data":{"versions":[
		{"id":"8002","position":"2.0","game_scoped_id":"900","name":"New",
		 "version":"1.2.0","category":"rewrapped","uploaded_at":"2026-02-01T00:00:00Z"}]}}`
	src := newSource(t, s)

	mod, err := src.GetMod(t.Context(), ModID("900", "7001"))
	require.NoError(t, err, "GetMod with an unfamiliar category = %v, want no error")
	require.NotNil(t, mod)

	assert.Equal(t, "1.2.0", mod.Version, "mod.Version = %q, want 1.2.0", mod.Version)
}

func TestGetModReportsAShapeItCannotUse(t *testing.T) {
	// A 200 whose body has nothing in it means the payload changed shape. Saying
	// "no such file" would send the user looking for a mod they already
	// installed.
	for name, body := range map[string]string{
		"empty object":       `{}`,
		"data with no file":  `{"data":{}}`,
		"not json":           `<html>a proxy answered instead</html>`,
		"bare file, no wrap": fileAggregateBare,
	} {
		s := armedStub(t)
		s.fileBody = body
		src := newSource(t, s)

		_, err := src.GetMod(t.Context(), ModID("900", "7001"))
		if err == nil {
			assert.Fail(t, "%s: GetMod succeeded on a shape it cannot read", name)
			continue
		}

		if !isCode(err, appCodeModNotFound) && !isCode(err, appCodeSourceUnavailable) {
			assert.Fail(t, "%s: error = %v, want a mod-not-found or unavailable code", name, err)
		}
	}
}

// fileAggregateBare is the same file without its "data" wrapper. The API always
// wraps it, so accepting this shape would mean accepting a response that is not
// the documented one.
const fileAggregateBare = `{"id":"7001","name":"Better UI Main","is_active":true}`

func TestGetModFailsWhenTheModRecordIsThin(t *testing.T) {
	// The mod record is where the name comes from, and the file and version
	// responses carry none, so a mod with no name has to fall back rather than
	// present as empty.
	s := armedStub(t)
	s.modBody = `{"data":{"id":"1234567890123","game_scoped_id":"900","game_id":"3834","name":null}}`
	src := newSource(t, s)

	mod, err := src.GetMod(t.Context(), ModID("900", "7001"))
	require.NoError(t, err, "GetMod")

	if mod.Name == "" {
		assert.Fail(t, "mod.Name is empty; the version's name should have been used instead")
	}

	assert.Equal(t, "Better UI 1.2.0", mod.Name, "mod.Name = %q, want the version's name", mod.Name)
}

func TestGetModToleratesACancelledContext(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := src.GetMod(ctx, ModID("900", "7001"))
	assert.True(
		t,
		isCode(err, appCodeCanceled),
		"GetMod with a cancelled context = %v, want ErrCodeOperationCanceled",
		err,
	)

	calls := s.callLog()
	assert.Empty(t, calls, "a cancelled context still made %v", calls)
}

func TestGetModToleratesANilContext(t *testing.T) {
	//nolint:staticcheck // a nil context is exactly what this asserts is survivable
	_, err := newSource(t, armedStub(t)).GetMod(nil, ModID("900", "7001"))
	assert.NoError(t, err, "GetMod with a nil context = %v, want nil error", err)
}

func TestSearchExpandsTheTrendingFeed(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)

	mods, err := src.Search(t.Context(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	if len(mods) == 0 {
		require.FailNow(t, "Search returned nothing from a feed with two installable mods")
	}

	for _, mod := range mods {
		assert.True(
			t,
			strings.HasPrefix(mod.ID, "nexus-"),
			"mod id %q is not a Nexus id: the feed carries no id, so it must be recovered from the page URL",
			mod.ID,
		)

		assert.NotNil(
			t,
			mod.SourceData["mod_id"],
			"mod %q carries no mod_id, which a shared link is built from",
			mod.ID,
		)
	}
	// The feed knows the author and the summary; the mod endpoint knows neither.
	var found bool

	for _, mod := range mods {
		if mod.SourceData["mod_id"] == "900" {
			found = true

			assert.Equal(
				t,
				"modder",
				mod.Author,
				"mod.Author = %q, want modder from the trending feed",
				mod.Author,
			)

			assert.Equal(
				t,
				"UI tweaks",
				mod.Description,
				"mod.Description = %q, want the feed's summary",
				mod.Description,
			)

			assert.Equal(
				t,
				"https://images.invalid/better.png",
				mod.SourceData["thumbnail_url"],
				"thumbnail_url = %#v, want the feed's picture",
				mod.SourceData["thumbnail_url"],
			)
		}
	}

	if !found {
		assert.Fail(
			t,
			"the mod whose id was recovered from the page URL is missing from the results",
		)
	}
	// The trending feed is public, so it is asked without a credential.
	for i, key := range s.keyLog() {
		if i == 0 && key != "" {
			assert.Fail(t, "the trending feed request carried apikey %q; it is public", key)
		}
	}
}

func TestSearchReportsAMissingKeyRatherThanNothingMatching(t *testing.T) {
	// Every entry has to be expanded before it can be listed, and that needs a
	// key. An empty list would read as "nothing trending matched", which is a
	// statement about Nexus rather than about the user's configuration.
	s := armedStub(t)
	src := newSource(t, s)
	src.apiKey = ""

	mods, err := src.Search(t.Context(), domain.SearchQuery{})
	require.Error(
		t,
		err,
		"Search with no key returned %d mods and no error; it must say the key is missing",
		len(mods),
	)

	assert.True(t, isCode(err, appCodeAuthRequired), "error = %v, want ErrCodeAuthRequired", err)

	assert.Nil(t, mods, "Search returned %d mods alongside its error", len(mods))
}

func TestSearchReportsARejectedKey(t *testing.T) {
	s := armedStub(t)
	s.modStatus = http.StatusUnauthorized

	src := newSource(t, s)
	if _, err := src.Search(t.Context(), domain.SearchQuery{}); !isCode(err, appCodeAuthFailed) {
		assert.Fail(t, "Search with a refused key = %v, want ErrCodeAuthFailed", err)
	}
}

func TestSearchToleratesOneUnreachableMod(t *testing.T) {
	// One broken mod must not empty a feed of five. The rule is the same one the
	// registry's fan-out uses: an empty feed caused entirely by one cause is that
	// cause, and a partial feed is still a feed.
	s := armedStub(t)
	src := newSource(t, s)
	// One mod's record is made unreadable. The route is by mod id, so the second
	// feed entry (901) is the one to break while 900 stays reachable.
	inner := s.srv.Config.Handler
	s.srv.Config.Handler = failingMod(inner, "/mods/901")

	mods, err := src.Search(t.Context(), domain.SearchQuery{})
	require.NoError(t, err, "Search: %v; one unreachable mod must not fail the feed")

	if len(mods) == 0 {
		assert.Fail(t, "Search returned nothing although one of the two mods was reachable")
	}
}

// failingMod wraps a handler and answers 500 for any path containing marker.
func failingMod(inner http.Handler, marker string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, marker) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(
				w,
				`{"type":"about:blank","title":"Error","status":500,"detail":"broken","instance":"/v3/"}`,
			)

			return
		}

		inner.ServeHTTP(w, r)
	})
}

func TestSearchSkipsEntriesWithNoRecoverableID(t *testing.T) {
	// A feed entry whose page URL has no id in it cannot be installed, and a
	// guessed id could be a different mod entirely.
	s := armedStub(t)
	s.trendingBody = `{"data":{"mods":[
		{"name":"No URL","mod_page_url":""},
		{"name":"Wrong Shape","mod_page_url":"https://www.nexusmods.com/darktide"},
		{"name":"Good","mod_page_url":"https://www.nexusmods.com/darktide/mods/900"}]}}`
	src := newSource(t, s)

	mods, err := src.Search(t.Context(), domain.SearchQuery{})
	require.NoError(t, err, "Search")

	if len(mods) == 0 {
		require.FailNow(t, "Search dropped the one entry it could identify")
	}

	for _, mod := range mods {
		assert.Equal(
			t,
			"900",
			mod.SourceData["mod_id"],
			"mod %q came from an entry that should have been skipped",
			mod.ID,
		)
	}
}

func TestSearchFiltersLocally(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)

	mods, err := src.Search(t.Context(), domain.SearchQuery{Query: "wip"})
	require.NoError(t, err, "Search")

	if len(mods) == 0 {
		require.FailNow(
			t,
			"Search for wip returned nothing; the query has to be applied to the feed",
		)
	}
	// Every hit must come from the entry the query named. The other feed entry is
	// called "Better UI", so a result from it would mean the filter did nothing.
	for _, mod := range mods {
		assert.Equal(
			t,
			"901",
			mod.SourceData["mod_id"],
			"Search for wip returned %q from mod %v, want only mod 901",
			mod.ID,
			mod.SourceData["mod_id"],
		)

		assert.Contains(
			t,
			strings.ToLower(mod.Name),
			"wip",
			"Search for wip returned a mod named %q; the feed's name is what the user searched for",
			mod.Name,
		)
	}
}

func TestSearchReportsAMovedEndpoint(t *testing.T) {
	// A 404 on the feed means the endpoint moved or the game is not served. An
	// empty list would say nothing was trending, which is a different statement.
	s := armedStub(t)
	s.trendingStatus = http.StatusNotFound

	src := newSource(t, s)
	if _, err := src.Search(
		t.Context(),
		domain.SearchQuery{},
	); !isCode(
		err,
		appCodeSourceUnavailable,
	) {
		assert.Fail(t, "Search against a missing endpoint = %v, want ErrCodeSourceUnavailable", err)
	}
}

func TestSearchReportsAnUnservedGame(t *testing.T) {
	// Darktide is not served by v3 today, and "Game not found" is a plain 404 on
	// this endpoint. The user needs to be told that, not shown an empty list.
	s := armedStub(t)
	s.trendingStatus = http.StatusNotFound
	s.trendingBody = `{"type":"about:blank","title":"Not Found","status":404,
		"detail":"Game not found: darktide","instance":"/v3/games/darktide/trending-mods"}`
	src := newSource(t, s)

	_, err := src.Search(t.Context(), domain.SearchQuery{})
	if err == nil {
		require.FailNow(t, "Search against an unserved game reported no error")
	}

	assert.Contains(
		t,
		message(err),
		"darktide",
		"error %q should name the game the API could not find",
		message(err),
	)
}

func TestSearchToleratesCancellationAndANilContext(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := src.Search(ctx, domain.SearchQuery{}); !isCode(err, appCodeCanceled) {
		assert.Fail(t, "Search with a cancelled context = %v, want ErrCodeOperationCanceled", err)
	}

	calls := s.callLog()
	assert.Empty(t, calls, "a cancelled context still made %v", calls)
	//nolint:staticcheck // a nil context is exactly what this asserts is survivable
	if _, err := src.Search(nil, domain.SearchQuery{}); err != nil {
		assert.Fail(t, "Search with a nil context = %v, want nil error", err)
	}
}

func TestSearchReportsAnUnreachableServer(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.close()

	_, err := src.Search(t.Context(), domain.SearchQuery{})
	if !isCode(err, appCodeSearchFailed) && !isCode(err, appCodeSourceUnavailable) {
		assert.Fail(t, "Search against a closed server = %v, want a typed error", err)
	}
}

func TestDownloadPostsForTheURLThenFetchesIt(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.dlBody = downloadFixtureJSON(s.url + "/asset")

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{"version_id": "8002"}

	dest := filepath.Join(t.TempDir(), "nested", "better_ui.zip")

	path, err := src.Download(t.Context(), mod, dest)
	require.NoError(t, err, "Download")

	assert.Equal(t, dest, path, "Download = %q, want %q", path, dest)

	body, err := os.ReadFile(dest)
	require.NoError(t, err, "the archive was not written")

	assert.Equal(
		t,
		string(s.assetBody),
		string(body),
		"the archive holds %q, want %q",
		body,
		s.assetBody,
	)
	// The mint is a POST. A GET would be answered 404 or 405 for an endpoint
	// that exists, which reads as "this version is not downloadable".
	calls := s.callLog()
	require.False(t, len(calls) < 2, "Download made %v, want a mint request and a fetch", calls)

	assert.Equal(
		t,
		"POST /v3/mod-file-versions/8002/download-repacked",
		calls[0],
		"the mint request was %q, want a POST to download-repacked",
		calls[0],
	)

	assert.Equal(
		t,
		"GET /asset",
		calls[1],
		"the second request was %q, want a GET of the minted URL",
		calls[1],
	)

	keys := s.keyLog()
	assert.Equal(
		t,
		"key-1",
		keys[0],
		"the mint request carried apikey %q, want the key: it goes to the API",
		keys[0],
	)
	// The minted URL points at a content host. The key belongs to the account,
	// not to that host, so it must not travel there.
	assert.Equal(
		t,
		"",
		keys[1],
		"the content-host request carried apikey %q; the URL is already signed",
		keys[1],
	)

	names := s.appNameLog()
	assert.NotEqual(
		t,
		"",
		names[0],
		"the mint request carried no Application-Name header; the API records who is downloading",
	)
}

// ---------------------------------------------------------------------------
// The keyed download.
//
// A free account cannot mint a download URL: POST download-repacked answers 403
// to a key without premium. The website's own answer is to hand a registered
// nxm:// handler a link carrying key, expires and user_id, and those three are
// what the legacy download_link endpoint needs in exchange for a signed URL.
// These tests pin the whole path, because every step in it is a place the real
// API has already disagreed with a plausible assumption.
// ---------------------------------------------------------------------------

func TestV1BaseFollowsTheConfiguredBase(t *testing.T) {
	for _, test := range []struct {
		name string
		base string
		want string
	}{
		{"the default base", "https://api.nexusmods.com/v3", "https://api.nexusmods.com/v1"},
		{"a trailing slash is not part of the segment", "https://api.nexusmods.com/v3/", "https://api.nexusmods.com/v1"},
		{"a mirror on another host", "https://nexus.example.com/v3", "https://nexus.example.com/v1"},
		{"a base with no version segment is left alone", "https://api.nexusmods.com", "https://api.nexusmods.com"},
		{"a base whose last segment is not a version is left alone", "https://api.nexusmods.com/api", "https://api.nexusmods.com/api"},
	} {
		t.Run(test.name, func(t *testing.T) {
			src := &Source{apiBase: test.base}
			got := src.v1Base()
			assert.Equal(t, test.want, got, "v1Base() = %q, want %q", got, test.want)
		})
	}
}

func TestIsVersionSegment(t *testing.T) {
	for _, test := range []struct {
		segment string
		want    bool
	}{
		{"v3", true},
		{"v1", true},
		{"v10", true},
		{"v", false},
		{"", false},
		{"3", false},
		{"darktide", false},
		{"v3x", false},
	} {
		got := isVersionSegment(test.segment)
		assert.Equal(
			t,
			test.want,
			got,
			"isVersionSegment(%q) = %v, want %v",
			test.segment,
			got,
			test.want,
		)
	}
}

func TestNXMGrantRequiresAllThreeParts(t *testing.T) {
	for _, test := range []struct {
		data map[string]any
		name string
		want bool
	}{
		{nil, "no SourceData at all", false},
		{map[string]any{}, "an empty SourceData", false},
		{map[string]any{sourceDataNXKey: "k"}, "only a key", false},
		{map[string]any{sourceDataNXKey: "k", sourceDataNXExpires: "123"}, "a key and an expiry", false},
		{map[string]any{sourceDataNXKey: "k", sourceDataNXExpires: "123", sourceDataNXUserID: "42"}, "all three", true},
		{map[string]any{sourceDataNXKey: "  ", sourceDataNXExpires: "123", sourceDataNXUserID: "42"}, "a blank key is no key", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			mod := &domain.Mod{ID: ModID("900", "7001"), SourceData: test.data}
			_, _, _, ok := nxmGrant(mod)
			assert.Equal(t, test.want, ok, "nxmGrant() ok = %v, want %v", ok, test.want)
		})
	}
	// A nil mod is a caller bug, not a state to report on.
	if _, _, _, ok := nxmGrant(nil); ok {
		assert.Fail(t, "nxmGrant(nil) = ok, want not ok")
	}
}

func TestForgetNXMGrantDropsOnlyTheAuthorization(t *testing.T) {
	mod := &domain.Mod{ID: ModID("900", "7001"), SourceData: map[string]any{
		sourceDataNXKey:     "k",
		sourceDataNXExpires: "123",
		sourceDataNXUserID:  "42",
		"version_id":        "8001",
	}}
	forgetNXMGrant(mod)

	for _, key := range []string{sourceDataNXKey, sourceDataNXExpires, sourceDataNXUserID} {
		_, exists := mod.SourceData[key]
		assert.False(t, exists, "SourceData[%q] survived the forget", key)
	}

	if _, exists := mod.SourceData["version_id"]; !exists {
		assert.Fail(t, "forgetNXMGrant dropped an unrelated key")
	}
	// Neither call may panic: both sit on a download path that has already
	// checked the mod, and a panic there would take a worker down with it.
	forgetNXMGrant(nil)
	forgetNXMGrant(&domain.Mod{ID: ModID("900", "7001")})
}

// A pasted link names the file by the number the site itself used, and that is
// the number the legacy endpoint is asked for first. Asking for anything else
// first costs a request the user's one-time grant can only be spent on once.
func TestDownloadSpendsAKeyedGrantUnderTheFileIDTheLinkNames(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.versionBody = versionFixtureJSON()
	s.dlLinkBody = downloadLinkFixtureJSON(s.url + "/asset")

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":        "8001",
		sourceDataNXKey:     "one-time-key",
		sourceDataNXExpires: "1791538669",
		sourceDataNXUserID:  "189193053",
	}

	dest := filepath.Join(t.TempDir(), "boss_thresholds.zip")
	_, err := src.Download(t.Context(), mod, dest)
	require.NoError(t, err, "Download: %v", err)

	body, err := os.ReadFile(dest)
	require.NoError(t, err, "the archive was not written")

	assert.Equal(
		t,
		string(s.assetBody),
		string(body),
		"the archive holds %q, want %q",
		body,
		s.assetBody,
	)

	calls := s.callLog()
	require.Len(t, calls, 2, "Download made %v, want a mint and a fetch", calls)
	// The query is the three parts of the grant, and all three are required:
	// the endpoint answers 403 to a partial set.
	wantMint := "GET /v1/games/darktide/mods/900/files/7001/download_link" +
		"?expires=1791538669&key=one-time-key&user_id=189193053"
	assert.Equal(t, wantMint, calls[0], "the mint request was %q, want %q", calls[0], wantMint)

	assert.Equal(
		t,
		"GET /asset",
		calls[1],
		"the fetch was %q, want a GET of the minted URL",
		calls[1],
	)
	// The version was never read: the number the link carried was accepted, so
	// there was nothing left to look up.
	assert.False(
		t,
		s.saw("/mod-file-versions/"),
		"the version was read (%v) even though the link's own file id worked",
		calls,
	)
	// The mint goes to the API, so it carries the key; the content host is
	// where the signed URL points, so it must not.
	keys := s.keyLog()
	assert.Equal(
		t,
		"key-1",
		keys[0],
		"the mint request carried apikey %q, want the account key",
		keys[0],
	)

	assert.Equal(
		t,
		"",
		keys[1],
		"the content-host request carried apikey %q; the URL is already signed",
		keys[1],
	)
	// The grant is one-time, so it must not outlive the download it authorized.
	if _, _, _, ok := nxmGrant(mod); ok {
		assert.Fail(t, "the spent grant is still in the mod's SourceData")
	}
}

// When the link's own number is refused, the version's game-scoped id is the
// second guess — and it costs one extra read, which is worth making only after
// the first number has actually been refused.
func TestDownloadFallsBackToTheGameScopedFileID(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.versionBody = versionFixtureJSON()
	s.dlLinkBody = downloadLinkFixtureJSON(s.url + "/asset")
	// The endpoint authorizes 8847 and nothing else.
	s.dlLinkFileID = "8847"

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":           "8001",
		sourceDataNXKey:        "one-time-key",
		sourceDataNXExpires:    "1791538669",
		sourceDataNXUserID:     "189193053",
		sourceDataFileScopedID: "8847",
	}

	dest := filepath.Join(t.TempDir(), "boss_thresholds.zip")
	_, err := src.Download(t.Context(), mod, dest)
	require.NoError(t, err, "Download: %v", err)

	body, err := os.ReadFile(dest)
	require.NoError(t, err, "the archive was not written")
	assert.Equal(t, string(s.assetBody), string(body))

	calls := s.callLog()
	require.Len(t, calls, 3, "Download made %v, want a refusal, a mint and a fetch", calls)
	assert.Equal(
		t,
		"GET /v1/games/darktide/mods/900/files/7001/download_link?expires=1791538669&key=one-time-key&user_id=189193053",
		calls[0],
		"the link's own number should be asked for first",
	)
	assert.Equal(
		t,
		"GET /v1/games/darktide/mods/900/files/8847/download_link?expires=1791538669&key=one-time-key&user_id=189193053",
		calls[1],
		"the game-scoped id should be the second guess",
	)
	assert.Equal(t, "GET /asset", calls[2])
}

// The version's game-scoped id is not always recorded on the mod, so the
// fallback sometimes has to read it. The read happens only after a refusal.
func TestDownloadFallsBackToAFileIDItHasToRead(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.versionBody = versionFixtureJSON()
	s.dlLinkBody = downloadLinkFixtureJSON(s.url + "/asset")
	s.dlLinkFileID = "8847"

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":        "8001",
		sourceDataNXKey:     "one-time-key",
		sourceDataNXExpires: "1791538669",
		sourceDataNXUserID:  "189193053",
	}

	dest := filepath.Join(t.TempDir(), "boss_thresholds.zip")
	_, err := src.Download(t.Context(), mod, dest)
	require.NoError(t, err, "Download: %v", err)

	calls := s.callLog()
	require.Len(t, calls, 4, "Download made %v, want a refusal, a read, a mint and a fetch", calls)
	assert.Equal(
		t,
		"GET /v3/mod-file-versions/8001",
		calls[1],
		"the version read came after the refusal",
	)
	assert.Contains(t, calls[2], "/files/8847/download_link")
	assert.Equal(t, "GET /asset", calls[3])
}

// Two refusals is the end of the guessing. The error has to name both numbers,
// because the user can check them against the page they pasted the link from.
func TestDownloadReportsBothFileIDsWhenNeitherIsAuthorized(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.versionBody = versionFixtureJSON()
	// An id the endpoint never authorizes, so both candidates are refused.
	s.dlLinkFileID = "9999"

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":           "8001",
		sourceDataNXKey:        "one-time-key",
		sourceDataNXExpires:    "1791538669",
		sourceDataNXUserID:     "189193053",
		sourceDataFileScopedID: "8847",
	}

	dest := filepath.Join(t.TempDir(), "boss_thresholds.zip")
	_, err := src.Download(t.Context(), mod, dest)
	require.Error(t, err)
	assert.True(t, isCode(err, appErrors.ErrCodeModNotFound), "got %v", err)

	got := message(err)
	assert.Contains(t, got, `"7001"`, "the link's own number should be named")
	assert.Contains(t, got, `"8847"`, "the second guess should be named")
	assert.NoFileExists(t, dest, "a refused download must not leave a file behind")
}

// An id the server refuses is worth guessing past; a key it rejects, a network
// failure or a 500 will answer the same way for every number, and repeating the
// request would turn one failure into several — with a one-time grant,
// possibly spending it.
func TestDownloadDoesNotRetryTheSecondIDOnANonRefusalError(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.versionBody = versionFixtureJSON()
	s.dlLinkStatus = http.StatusInternalServerError
	s.dlLinkBody = `{"code":500,"message":"upstream is having a bad day"}`

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":           "8001",
		sourceDataNXKey:        "one-time-key",
		sourceDataNXExpires:    "1791538669",
		sourceDataNXUserID:     "189193053",
		sourceDataFileScopedID: "8847",
	}

	dest := filepath.Join(t.TempDir(), "boss_thresholds.zip")
	_, err := src.Download(t.Context(), mod, dest)
	require.Error(t, err)

	calls := s.callLog()
	require.Len(t, calls, 1, "Download made %v, want one attempt", calls)
	assert.Contains(t, calls[0], "/files/7001/download_link")
}

func TestDownloadLeavesAPartialGrantToThePremiumEndpoint(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.dlBody = downloadFixtureJSON(s.url + "/asset")

	// A link carrying only some of the three parts is not a grant. Sending it
	// anyway would draw a 403 that blames the account rather than the link.
	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":    "8001",
		sourceDataNXKey: "one-time-key",
	}

	dest := filepath.Join(t.TempDir(), "better_ui.zip")
	_, err := src.Download(t.Context(), mod, dest)
	require.NoError(t, err, "Download: %v", err)

	assert.True(
		t,
		s.saw("POST /v3/mod-file-versions/8001/download-repacked"),
		"a partial grant was sent to the legacy endpoint: %v",
		s.callLog(),
	)

	if s.saw("/download_link") {
		assert.Fail(t, "the legacy endpoint was called without a complete grant")
	}
}

func TestDownloadReportsAKeyedMintThatOffersNoMirror(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.versionBody = versionFixtureJSON()
	s.dlLinkBody = `[{"name":"Nexus CDN","short_name":"Nexus CDN","URI":"  "}]`

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":        "8001",
		sourceDataNXKey:     "k",
		sourceDataNXExpires: "123",
		sourceDataNXUserID:  "42",
	}

	dest := filepath.Join(t.TempDir(), "better_ui.zip")

	_, err := src.Download(t.Context(), mod, dest)
	if err == nil {
		require.FailNow(t, "Download succeeded with no mirror to fetch")
	}

	assert.True(
		t,
		isCode(err, appCodeDownloadFailed),
		"the failure is %v, want DOWNLOAD_FAILED",
		err,
	)

	assert.Contains(
		t,
		message(err),
		"no mirror",
		"the message %q does not say there was no mirror",
		message(err),
	)
}

func TestDownloadReportsAKeyedMintTheEndpointRefused(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.versionBody = versionFixtureJSON()
	s.dlLinkStatus = http.StatusForbidden
	s.dlLinkBody = `{"code":403,"message":"You don't have permission to get download links from the API"}`

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":        "8001",
		sourceDataNXKey:     "k",
		sourceDataNXExpires: "123",
		sourceDataNXUserID:  "42",
	}

	dest := filepath.Join(t.TempDir(), "better_ui.zip")

	_, err := src.Download(t.Context(), mod, dest)
	assert.True(t, isCode(err, appCodeAuthFailed), "the failure is %v, want AUTH_FAILED", err)
	// The grant survives a refusal. The endpoint answered 403 rather than
	// minting a URL, which means the one-time key was not spent — and a key
	// burned on a failure that might have succeeded on a retry is a key the
	// user has to go back to the website to replace.
	if _, _, _, ok := nxmGrant(mod); !ok {
		assert.Fail(t, "a refused mint dropped the grant; the key was never spent")
	}
}

// Neither number can authorize a download when the version publishes no
// game-scoped id and the link's own number is refused too. There is nothing
// left to guess, and the error has to say so with both numbers in it.
func TestDownloadReportsWhenNeitherFileIDCanAuthorize(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.versionBody = `{"data":{"id":"8001","file":{"id":"7001","name":"Better UI Main"},
	 "position":"2.0","game_scoped_id":"","name":"Better UI 1.2.0","version":"1.2.0",
	 "category":"main","uploaded_at":"2026-02-01T09:30:00Z","is_primary":true}}`
	// An id the endpoint never authorizes, so both candidates are refused.
	s.dlLinkFileID = "9999"

	mod := &domain.Mod{ID: ModID("900", "7001"), SourceID: SourceID}
	mod.SourceData = map[string]any{
		"version_id":        "8001",
		sourceDataNXKey:     "k",
		sourceDataNXExpires: "123",
		sourceDataNXUserID:  "42",
	}

	dest := filepath.Join(t.TempDir(), "better_ui.zip")

	_, err := src.Download(t.Context(), mod, dest)
	if err == nil {
		require.FailNow(t, "Download succeeded with nothing to authorize it with")
	}

	assert.True(
		t,
		isCode(err, appCodeModNotFound),
		"the failure is %v, want MOD_NOT_FOUND",
		err,
	)

	got := message(err)
	assert.Contains(t, got, `"7001"`, "the link's own number should be named: %q", got)
	assert.Contains(
		t,
		got,
		"authorized no download",
		"the message %q does not explain what happened",
		got,
	)
}

func TestDownloadResolvesTheVersionForAHandBuiltMod(t *testing.T) {
	// A caller that never went through Search or GetMod has no version id, so
	// the file's versions are listed and the newest installable one is used.
	s := armedStub(t)
	src := newSource(t, s)
	s.dlBody = downloadFixtureJSON(s.url + "/asset")

	dest := filepath.Join(t.TempDir(), "better_ui.zip")
	if _, err := src.Download(
		t.Context(),
		&domain.Mod{ID: ModID("900", "7001")},
		dest,
	); err != nil {
		require.FailNow(t, "Download: %v", err)
	}

	calls := s.callLog()

	var sawVersions bool

	for _, call := range calls {
		if call == "GET /v3/mod-files/7001/versions" {
			sawVersions = true
		}
	}

	assert.True(
		t,
		sawVersions,
		"Download for a mod with no version id made %v; the versions should have been listed",
		calls,
	)
	// The fixture's newest installable version is 8002, not the archived 8003.
	assert.True(
		t,
		s.saw("POST /v3/mod-file-versions/8002/download-repacked"),
		"Download did not mint a link for the newest installable version; it made %v",
		calls,
	)
}

func TestDownloadHonoursAPinnedVersion(t *testing.T) {
	// This is what makes "install version 1.0.0" install 1.0.0.
	s := armedStub(t)
	src := newSource(t, s)
	s.dlBody = downloadFixtureJSON(s.url + "/asset")

	mod := &domain.Mod{ID: ModID("900", "7001")}
	mod.SourceData = map[string]any{"version_id": "8001"}

	dest := filepath.Join(t.TempDir(), "better_ui.zip")
	_, err := src.Download(t.Context(), mod, dest)
	require.NoError(t, err, "Download: %v", err)

	assert.True(
		t,
		s.saw("POST /v3/mod-file-versions/8001/download-repacked"),
		"Download did not use the pinned version; it made %v",
		s.callLog(),
	)

	if s.saw("GET /v3/mod-files/7001/versions") {
		assert.Fail(t, "Download listed the versions although the mod already named one")
	}
}

func TestDownloadRefusesItsBadArguments(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	dest := filepath.Join(t.TempDir(), "out.zip")

	_, err := src.Download(t.Context(), nil, dest)
	assert.True(
		t,
		isCode(err, appCodeInvalidMod),
		"Download with no mod = %v, want ErrCodeInvalidMod",
		err,
	)

	if _, err := src.Download(
		t.Context(),
		&domain.Mod{ID: ModID("900", "7001")},
		"  ",
	); !isCode(
		err,
		appCodeInvalidArgument,
	) {
		assert.Fail(t, "Download with no destination = %v, want ErrCodeInvalidArgument", err)
	}

	if _, err := src.Download(
		t.Context(),
		&domain.Mod{ID: "nexus-"},
		dest,
	); !isCode(
		err,
		appCodeInvalidArgument,
	) {
		assert.Fail(t, "Download for a mod with no Nexus id = %v, want ErrCodeInvalidArgument", err)
	}

	calls := s.callLog()
	assert.Empty(
		t,
		calls,
		"a bad argument made %v; nothing should have been asked of the API",
		calls,
	)
}

func TestDownloadReportsABlankLink(t *testing.T) {
	// A 200 with no download_url is not a download.
	s := armedStub(t)
	src := newSource(t, s)
	s.dlBody = `{"download_url":"  ","expires_at":"2026-02-01T11:00:00Z"}`

	mod := &domain.Mod{ID: ModID("900", "7001")}
	mod.SourceData = map[string]any{"version_id": "8002"}

	_, err := src.Download(t.Context(), mod, filepath.Join(t.TempDir(), "out.zip"))
	assert.True(
		t,
		isCode(err, appCodeDownloadFailed),
		"Download with a blank link = %v, want ErrCodeDownloadFailed",
		err,
	)
}

func TestDownloadReportsALinkThatIsNotJSON(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.dlBody = `<html>a proxy answered instead</html>`

	mod := &domain.Mod{ID: ModID("900", "7001")}
	mod.SourceData = map[string]any{"version_id": "8002"}

	_, err := src.Download(t.Context(), mod, filepath.Join(t.TempDir(), "out.zip"))
	if err == nil {
		require.FailNow(t, "Download accepted a link response that was not JSON")
	}

	assert.True(
		t,
		isCode(err, appCodeDownloadFailed),
		"Download with an undecodable link = %v, want ErrCodeDownloadFailed",
		err,
	)
}

func TestDownloadReportsAMissingKey(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	src.apiKey = ""
	mod := &domain.Mod{ID: ModID("900", "7001")}
	mod.SourceData = map[string]any{"version_id": "8002"}

	_, err := src.Download(t.Context(), mod, filepath.Join(t.TempDir(), "out.zip"))
	assert.True(
		t,
		isCode(err, appCodeAuthRequired),
		"Download with no key = %v, want ErrCodeAuthRequired",
		err,
	)

	calls := s.callLog()
	assert.Empty(t, calls, "Download with no key made %v", calls)
}

func TestDownloadReportsAMissingVersion(t *testing.T) {
	s := armedStub(t)
	s.versionsBody = `{"data":{"versions":[]}}`
	src := newSource(t, s)

	_, err := src.Download(
		t.Context(),
		&domain.Mod{ID: ModID("900", "7001")},
		filepath.Join(t.TempDir(), "out.zip"),
	)
	assert.True(
		t,
		isCode(err, appCodeModNotFound),
		"Download for a file with no version = %v, want ErrCodeModNotFound",
		err,
	)
}

func TestDownloadLeavesNothingBehindWhenItFails(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	s.dlStatus = http.StatusNotFound

	mod := &domain.Mod{ID: ModID("900", "7001")}
	mod.SourceData = map[string]any{"version_id": "8002"}

	dest := filepath.Join(t.TempDir(), "out.zip")
	if _, err := src.Download(t.Context(), mod, dest); err == nil {
		require.FailNow(t, "Download succeeded although the mint failed")
	}

	_, err := os.Stat(dest)
	assert.True(
		t,
		os.IsNotExist(err),
		"a failed Download left a file behind; a later run would find a half-written archive",
	)
}

func TestDownloadToleratesCancellation(t *testing.T) {
	s := armedStub(t)
	src := newSource(t, s)
	mod := &domain.Mod{ID: ModID("900", "7001")}
	mod.SourceData = map[string]any{"version_id": "8002"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := src.Download(ctx, mod, filepath.Join(t.TempDir(), "out.zip"))
	if err == nil {
		require.FailNow(t, "Download with a cancelled context reported no error")
	}
}

func TestVerifyAcceptsEverything(t *testing.T) {
	// v3 publishes no digest, size or checksum for any downloadable object, so
	// there is nothing to compare against. A check that cannot fail is worse than
	// no check, because it is reported as one.
	src := newSource(t, armedStub(t))

	cases := []struct {
		name string
		mod  *domain.Mod
		path string
	}{
		{"no mod", nil, ""},
		{"no source data", &domain.Mod{ID: "nexus-900-7001"}, "/nonexistent"},
		{
			"a path that does not exist",
			&domain.Mod{ID: "nexus-900-7001", SourceData: map[string]any{"sha256": "abc"}},
			"/nonexistent",
		},
	}
	for _, tc := range cases {
		err := src.Verify(t.Context(), tc.mod, tc.path)
		assert.NoError(t, err, "Verify with %s = %v, want nil", tc.name, err)
	}
}

func TestValidateCredentialsDiscardsOnlyAKeyThatWasRefused(t *testing.T) {
	// A refused key has to go, or the next attempt fails with the same message
	// while the user has been told to paste a new one. A key that was never
	// refused has to stay: a timeout says nothing about whether the credential is
	// good, and making somebody retype a working key because the network
	// hiccuped is a punishment for somebody else's outage.
	s := armedStub(t)

	src := newSource(t, s)
	if err := src.Authenticate(
		t.Context(),
		domain.AuthCredentials{"api_key": "key-1"},
	); err != nil {
		require.FailNow(t, "Authenticate: %v", err)
	}

	s.close()

	if err := src.ValidateCredentials(t.Context()); err == nil {
		require.FailNow(t, "validating against a closed server reported success")
	}

	if !src.HasCredentials() {
		assert.Fail(t, "the key was discarded because the server was unreachable")
	}

	s2 := armedStub(t)

	src2 := newSource(t, s2)
	if err := src2.Authenticate(
		t.Context(),
		domain.AuthCredentials{"api_key": "key-1"},
	); err != nil {
		require.FailNow(t, "Authenticate: %v", err)
	}

	s2.batchStatus = http.StatusUnauthorized

	err := src2.ValidateCredentials(t.Context())
	assert.True(
		t,
		isCode(err, appErrors.ErrCodeAuthFailed),
		"a refused key gave %v, want %s",
		err,
		appErrors.ErrCodeAuthFailed,
	)

	if src2.HasCredentials() {
		assert.Fail(t, "a refused key was kept")
	}
}

func TestCredentialStateDoesNotRace(t *testing.T) {
	// The key is read by every request and written by Authenticate, which the
	// manager may run from an event handler while a search is in flight.
	s := armedStub(t)
	src := newSource(t, s)
	done := make(chan struct{})

	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()

			for j := 0; j < 25; j++ {
				src.Authenticated()
				src.HasCredentials()
				src.apiKeyIfAny()
			}
		}()
	}

	go func() {
		defer func() { done <- struct{}{} }()

		for j := 0; j < 25; j++ {
			_ = src.Authenticate(t.Context(), domain.AuthCredentials{"api_key": "rotating"})
		}
	}()

	for i := 0; i < 5; i++ {
		<-done
	}
}

// TestForgetCredentialsStopsSendingTheHeader: deleting the key in the settings
// has to be the end of it. A forgotten key that kept riding along in the header
// would make "sign out" a lie the user discovers one 403 at a time.
func TestForgetCredentialsStopsSendingTheHeader(t *testing.T) {
	s := armedStub(t)

	src := newSource(t, s)
	if err := src.Authenticate(
		t.Context(),
		domain.AuthCredentials{"api_key": "key-1"},
	); err != nil {
		require.FailNow(t, "Authenticate: %v", err)
	}

	src.ForgetCredentials()

	if src.HasCredentials() || src.Authenticated() {
		assert.Fail(t, "a forgotten key is still reported as stored")
	}

	before := len(s.keys)

	if _, err := src.Search(t.Context(), domain.SearchQuery{}); !isCode(err, appCodeAuthRequired) {
		assert.Fail(t, "Search after forgetting = %v, want ErrCodeAuthRequired", err)
	} else if !strings.Contains(err.Error(), "dmm source auth nexus --api-key-stdin") {
		assert.Fail(t, "error %q does not say how to store a key", err)
	}

	for i, key := range s.keys[before:] {
		assert.Equal(
			t,
			"",
			key,
			"request %d still carried apikey %q after the key was forgotten",
			i,
			key,
		)
	}
}

// ------------------------------------------ game defaults through the factory

// theFactory returns the factory the package registered from init, which is
// what the registry actually calls: asserting on New alone would leave the
// game-default wiring untested.
func theFactory(t *testing.T) services.SourceFactory {
	t.Helper()

	factory, ok := services.SourceFactoryFor(SourceID)
	require.True(t, ok, "the %s factory must register itself from init", SourceID)

	return factory
}

func TestFactoryAppliesTheGamesGameDomainDefault(t *testing.T) {
	got, err := theFactory(t)(services.SourceBuildContext{
		Game: gamestest.Def{
			IDValue: "test-game",
			Defaults: map[string]map[string]string{
				SourceID: {optionGameDomain: "some-other-game"},
			},
		},
	})
	require.NoError(t, err, "factory with a game default: %v", err)

	src, ok := got.(*Source)
	require.True(t, ok, "factory returned %T, want *Source", got)
	assert.Equal(t, "some-other-game", src.gameDomain,
		"the game's default must win over the source's own")
}

func TestFactoryLetsTheConfigBlockOverrideTheGamesGameDomain(t *testing.T) {
	got, err := theFactory(t)(services.SourceBuildContext{
		Game: gamestest.Def{
			Defaults: map[string]map[string]string{
				SourceID: {optionGameDomain: "from-game"},
			},
		},
		Config: domain.SourceConfig{Options: map[string]string{optionGameDomain: "from-config"}},
	})
	require.NoError(t, err, "factory with both a game default and a config value: %v", err)

	src, ok := got.(*Source)
	require.True(t, ok, "factory returned %T, want *Source", got)
	assert.Equal(t, "from-config", src.gameDomain,
		"the user's configuration must win over the game's default")
}

func TestFallsBackToTheNativeGameDomainWithoutAGame(t *testing.T) {
	got, err := theFactory(t)(services.SourceBuildContext{})
	require.NoError(t, err, "factory with no game: %v", err)

	src, ok := got.(*Source)
	require.True(t, ok, "factory returned %T, want *Source", got)
	assert.Equal(t, DefaultGameDomain, src.gameDomain,
		"without a game the source's own default must apply")
}
