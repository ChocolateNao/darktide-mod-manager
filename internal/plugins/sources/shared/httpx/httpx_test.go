package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClampsTimeout(t *testing.T) {
	// A zero timeout means "no timeout" to net/http, which for a network call is
	// never what a caller wants, so it must become the default rather than hang.
	for _, timeout := range []time.Duration{0, -time.Second} {
		got := New(timeout).HTTPClient().Timeout
		assert.Equal(
			t,
			DefaultTimeout,
			got,
			"New(%v).Timeout = %v, want %v",
			timeout,
			got,
			DefaultTimeout,
		)
	}

	got := New(time.Minute).HTTPClient().Timeout
	assert.Equal(t, time.Minute, got, "explicit timeout not kept: %v", got)
}

func TestNilClientReportsNoHTTPClient(t *testing.T) {
	// A nil *Client must not panic: a source built from a zero struct is a
	// programming error the UI should see as an error, not a crash.
	var c *Client
	if _, err := c.Do(
		context.Background(),
		Request{URL: "http://example.test"},
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeSourceUnavailable,
	) {
		assert.Fail(t, "Do on nil client = %v, want source unavailable", err)
	}

	_, err := c.Download(
		context.Background(),
		"http://example.test",
		"x",
		nil,
		"",
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeSourceUnavailable,
	), "Download on nil client = %v, want source unavailable", err)

	if c.HTTPClient() == nil {
		assert.Fail(t, "HTTPClient on nil client should still hand back a usable client")
	}
}

func TestWithUserAgent(t *testing.T) {
	base := New(time.Second)
	// Some hosts reject an empty User-Agent outright, so a blank override has to
	// keep the default rather than blank the header.
	for _, blank := range []string{"", "   "} {
		if got := base.WithUserAgent(
			blank,
		); got.http != base.http ||
			got.userAgent != DefaultUserAgent {
			assert.Fail(t, "blank user agent override changed the client: %+v", got)
		}
	}

	got := base.WithUserAgent("custom/1").userAgent
	assert.Equal(t, "custom/1", got, "WithUserAgent did not apply: %q", got)
	// WithUserAgent must copy, not mutate: sources derive per-host clients.
	assert.Equal(
		t,
		DefaultUserAgent,
		base.userAgent,
		"WithUserAgent mutated the receiver: %q",
		base.userAgent,
	)
}

// WithTimeout is how a source narrows or widens the deadline it was built with,
// so it has to copy rather than mutate: a source derives several clients from
// one base, and mutating would let the last option win for all of them.
func TestWithTimeout(t *testing.T) {
	base := New(30 * time.Second)
	base = base.WithUserAgent("dmm-test/1")

	got := base.WithTimeout(time.Second)
	if got.http == base.http {
		assert.Fail(t, "WithTimeout reused the receiver's http.Client; it must copy")
	}

	assert.Equal(
		t,
		time.Second,
		got.http.Timeout,
		"WithTimeout timeout = %v, want %v",
		got.http.Timeout,
		time.Second,
	)
	// Everything the copy does not replace has to come along: a source that
	// tunes the timeout after setting a user agent must keep it.
	assert.Equal(
		t,
		"dmm-test/1",
		got.userAgent,
		"WithTimeout lost the user agent: %q",
		got.userAgent,
	)

	assert.Equal(
		t,
		30*time.Second,
		base.http.Timeout,
		"WithTimeout mutated the receiver: %v",
		base.http.Timeout,
	)

	// A zero read out of a config file means "unset", not "wait forever" — the
	// same rule New follows, and the reason the copy keeps its own deadline.
	for _, ignored := range []time.Duration{0, -time.Second} {
		kept := got.WithTimeout(ignored)
		assert.Equal(
			t,
			time.Second,
			kept.http.Timeout,
			"WithTimeout(%v) = %v, want the existing %v untouched",
			ignored,
			kept.http.Timeout,
			time.Second,
		)
	}
}

// TestWithTimeoutApplies checks the copy is actually wired up, not just
// structurally different: a request against a server that never answers has to
// fail on the new deadline instead of hanging.
func TestWithTimeoutApplies(t *testing.T) {
	released := make(chan struct{})
	defer close(released)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	// Long enough that the deadline, not the server, is what ends the request.
	c := New(time.Minute).WithTimeout(150 * time.Millisecond)
	start := time.Now()

	err := c.GetJSON(context.Background(), srv.URL, nil, nil, KindMeta)
	if err == nil {
		require.FailNow(t, "GetJSON against a stalled server returned no error")
	}
	// A per-request timeout is a transport failure, not a cancellation. Only the
	// caller's own context being done means somebody asked to stop, and the UI
	// treats a cancellation as "not an error"; reporting a slow source that way
	// would swallow it.
	assert.False(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"error = %v, want a stalled request reported as a failure, not a cancellation",
		err,
	)

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"error = %v, want %s for a timed-out metadata request",
		err,
		appErrors.ErrCodeSourceUnavailable,
	)

	elapsed := time.Since(start)
	assert.False(
		t,
		elapsed > 10*time.Second,
		"the request took %v; the 150ms deadline was not applied",
		elapsed,
	)
}

func TestDoSendsUserAgentAndHeaders(t *testing.T) {
	var seen *http.Request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	header := http.Header{}
	header.Set("Authorization", "Bearer t0ken")

	c := New(time.Second).WithUserAgent("dmm-test/1")
	err := c.GetJSON(context.Background(), srv.URL, nil, header, KindMeta)
	require.NoError(t, err, "GetJSON: %v", err)

	got := seen.Header.Get("User-Agent")
	assert.Equal(t, "dmm-test/1", got, "User-Agent = %q", got)

	got = seen.Header.Get("Authorization")
	assert.Equal(t, "Bearer t0ken", got, "Authorization = %q", got)
}

func TestDoEncodesJSONAndFormBodies(t *testing.T) {
	var (
		body        string
		contentType string
		method      string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
	}))
	defer srv.Close()

	c := New(time.Second)

	if _, err := c.Do(
		context.Background(),
		Request{Method: http.MethodPost, URL: srv.URL, Body: map[string]int{"n": 2}},
	); err != nil {
		require.FailNow(t, "JSON post: %v", err)
	}

	if method != http.MethodPost || body != `{"n":2}` ||
		!strings.HasPrefix(contentType, "application/json") {
		assert.Fail(
			t,
			"JSON post sent method=%q body=%q content-type=%q",
			method,
			body,
			contentType,
		)
	}

	form := url.Values{"grant_type": {"password"}}
	if _, err := c.Do(
		context.Background(),
		Request{Method: http.MethodPost, URL: srv.URL, Form: form},
	); err != nil {
		require.FailNow(t, "form post: %v", err)
	}

	if body != "grant_type=password" || contentType != "application/x-www-form-urlencoded" {
		assert.Fail(t, "form post sent body=%q content-type=%q", body, contentType)
	}

	// A nil Form must not become an empty form body: the switch has to keep
	// preferring JSON.
	if _, err := c.Do(
		context.Background(),
		Request{Method: http.MethodPost, URL: srv.URL, Body: map[string]int{"n": 3}},
	); err != nil {
		require.FailNow(t, "JSON post again: %v", err)
	}

	assert.Equal(t, `{"n":3}`, body, "second JSON post sent %q", body)
}

func TestDoDefaultsToGet(t *testing.T) {
	var method string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
	}))
	defer srv.Close()

	if _, err := New(time.Second).Do(context.Background(), Request{URL: srv.URL}); err != nil {
		require.FailNow(t, "Do: %v", err)
	}

	assert.Equal(t, http.MethodGet, method, "default method = %q", method)
}

func TestBuildURL(t *testing.T) {
	_, err := buildURL("  ", nil)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"blank URL = %v",
		err,
	)
	// Only http(s) is accepted: a "file://" or "ftp://" URL reaching here would be
	// a source plugin reading the local disk or shelling out.
	for _, bad := range []string{"file:///etc/passwd", "ftp://example.test/x", "example.test/x", "javascript:alert(1)"} {
		_, err := buildURL(
			bad,
			nil,
		)
		assert.True(t, appErrors.IsAppError(
			err,
			appErrors.ErrCodeInvalidArgument,
		), "buildURL(%q) = %v, want invalid argument", bad, err)
	}

	got, err := buildURL("https://example.test/search?a=1", url.Values{"b": {"2"}, "a": {"9"}})
	require.NoError(t, err, "buildURL")
	// a=1 then a=9 both survive (Add, not Set) and the encoding is sorted.
	if !strings.Contains(got, "a=1") || !strings.Contains(got, "a=9") ||
		!strings.Contains(got, "b=2") {
		assert.Fail(t, "merged query = %q", got)
	}

	assert.Contains(t, got, "a=1&a=9&b=2", "query keys not sorted in %q", got)
	// With no extra query the URL is returned as-is: re-encoding it would reorder
	// parameters the caller deliberately put in that order.
	same, err := buildURL("https://example.test/x?b=2&a=1", nil)
	require.NoError(t, err, "buildURL")

	assert.Contains(t, same, "b=2&a=1", "existing query was re-encoded: %q", same)
}

func TestStatusErrorMapping(t *testing.T) {
	// The mapping is the reason this package exists, so it is pinned exhaustively.
	cases := []struct {
		name   string
		body   string
		want   appErrors.Code
		quote  string
		absent string
		kind   Kind
		status int
	}{
		{
			name:   "401 needs auth",
			kind:   KindMeta,
			status: 401,
			body:   "",
			want:   appErrors.ErrCodeAuthRequired,
			quote:  "rejected the credentials",
			absent: "",
		},
		{
			name:   "403 auth failed",
			kind:   KindMeta,
			status: 403,
			body:   "",
			want:   appErrors.ErrCodeAuthFailed,
			quote:  "forbidden",
			absent: "",
		},
		{
			name:   "404 meta is not found",
			kind:   KindMeta,
			status: 404,
			body:   "",
			want:   appErrors.ErrCodeNotFound,
			quote:  "not found",
			absent: "",
		},
		{
			name:   "404 search is an endpoint problem",
			kind:   KindSearch,
			status: 404,
			body:   "",
			want:   appErrors.ErrCodeSourceUnavailable,
			quote:  "search endpoint not found",
			absent: "metadata request not found",
		},
		{
			name:   "429 rate limited",
			kind:   KindMeta,
			status: 429,
			body:   "",
			want:   appErrors.ErrCodeSourceUnavailable,
			quote:  "rate limited",
			absent: "",
		},
		{
			name:   "408 timeout",
			kind:   KindMeta,
			status: 408,
			body:   "",
			want:   appErrors.ErrCodeSourceUnavailable,
			quote:  "status 408",
			absent: "",
		},
		{
			name:   "500",
			kind:   KindMeta,
			status: 500,
			body:   "",
			want:   appErrors.ErrCodeSourceUnavailable,
			quote:  "status 500",
			absent: "",
		},
		{
			name:   "503",
			kind:   KindMeta,
			status: 503,
			body:   "",
			want:   appErrors.ErrCodeSourceUnavailable,
			quote:  "status 503",
			absent: "",
		},
		// A 4xx that is none of the four cases above is a request the far end read
		// and refused. That is the case this mapping used to get wrong: it fell
		// through to the kind's fallback, which for a credential check is
		// SOURCE_UNAVAILABLE, so a payload the API did not like was reported as an
		// unreachable service. It is the most expensive kind of wrong answer
		// because it sends the user after a network problem they do not have.
		{
			name:   "400 on search",
			kind:   KindSearch,
			status: 400,
			body:   "",
			want:   appErrors.ErrCodeSourceRejected,
			quote:  "refused",
			absent: "SEARCH_FAILED",
		},
		{
			name:   "400 on download",
			kind:   KindDownload,
			status: 400,
			body:   "",
			want:   appErrors.ErrCodeSourceRejected,
			quote:  "refused",
			absent: "",
		},
		{
			name:   "418 on meta",
			kind:   KindMeta,
			status: 418,
			body:   "",
			want:   appErrors.ErrCodeSourceRejected,
			quote:  "refused",
			absent: "unavailable",
		},
		{
			name:   "422 on auth",
			kind:   KindAuth,
			status: 422,
			body:   "",
			want:   appErrors.ErrCodeSourceRejected,
			quote:  "refused",
			absent: "unavailable",
		},
		// The availability code is still reachable for a status that is neither a
		// refusal nor an answer: 3xx that the client did not follow.
		{
			name:   "302 is not answered",
			kind:   KindMeta,
			status: 302,
			body:   "",
			want:   appErrors.ErrCodeSourceUnavailable,
			quote:  "status 302",
			absent: "",
		},
	}
	for _, tc := range cases {
		err := statusError(tc.kind, tc.status, "https://example.test/x", []byte(tc.body))
		assert.True(
			t,
			appErrors.IsAppError(err, tc.want),
			"%s: code = %v (%v), want %s",
			tc.name,
			appErrors.CodeOf(err),
			err,
			tc.want,
		)

		assert.Contains(
			t,
			err.Error(),
			tc.quote,
			"%s: %q missing from %q",
			tc.name,
			tc.quote,
			err.Error(),
		)

		if tc.absent != "" && strings.Contains(err.Error(), tc.absent) {
			assert.Fail(t, "%s: %q should not appear in %q", tc.name, tc.absent, err.Error())
		}
	}
}

func TestStatusErrorQuirksOfErrorBodies(t *testing.T) {
	// A JSON error body is worth quoting: it usually names the offending field.
	err := statusError(KindMeta, 422, "https://example.test/x", []byte(`{"error":"bad version"}`))
	assert.Contains(t, err.Error(), "bad version", "JSON error body not quoted: %q", err.Error())
	// An HTML body is not: every host has one and it tells the user nothing.
	html := statusError(
		KindMeta,
		502,
		"https://example.test/x",
		[]byte("<html><body>oops</body></html>"),
	)
	if strings.Contains(html.Error(), "oops") || strings.Contains(html.Error(), "html") {
		assert.Fail(t, "HTML error body leaked into the message: %q", html.Error())
	}
	// Long bodies are truncated with a visible ellipsis.
	long := statusError(KindMeta, 400, "https://example.test/x", []byte(strings.Repeat("x", 400)))
	assert.Contains(t, long.Error(), "...", "long error body not truncated: %q", long.Error())

	if strings.Contains(long.Error(), strings.Repeat("x", 400)) {
		assert.Fail(t, "long error body was not actually cut")
	}
	// Whitespace-only bodies are dropped rather than quoted as "".
	blank := statusError(KindMeta, 400, "https://example.test/x", []byte("   \n  "))
	assert.False(
		t,
		strings.HasSuffix(blank.Error(), `""`),
		"blank error body quoted: %q",
		blank.Error(),
	)
}

func TestKindStrings(t *testing.T) {
	for kind, want := range map[Kind]string{
		KindMeta:     "metadata request",
		KindSearch:   "search",
		KindAuth:     "authentication",
		KindDownload: "download",
		Kind(99):     "request",
	} {
		got := kind.String()
		assert.Equal(t, want, got, "Kind(%d).String() = %q, want %q", int(kind), got, want)
	}
}

func TestDoReturnsResponseAndErrorForFailureStatus(t *testing.T) {
	// A 404 still hands back the response: the caller may want the headers. What
	// it must not do is treat the error as a transport failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	resp, err := New(time.Second).Do(context.Background(), Request{URL: srv.URL, Kind: KindMeta})
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeNotFound),
		"err = %v, want not found",
		err,
	)

	if resp == nil || resp.Status != 404 {
		require.FailNow(t, "response = %+v, want status 404", resp)
	}

	if resp.OK() {
		assert.Fail(t, "404 response reported OK")
	}

	if (&Response{Status: 204}).OK() != true {
		assert.Fail(t, "204 should be OK")
	}

	if (*Response)(nil).OK() {
		assert.Fail(t, "nil response should not be OK")
	}
}

func TestDoRejectsUnencodableBody(t *testing.T) {
	// A channel cannot be JSON encoded; that is a plugin bug and must be reported
	// as one rather than panicking inside json.Marshal's caller.
	_, err := New(time.Second).Do(context.Background(), Request{
		URL:  "https://example.test/x",
		Body: make(chan int),
		Kind: KindMeta,
	})
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"unencodable body = %v",
		err,
	)
}

func TestDoRefusesAPreEncodedBody(t *testing.T) {
	// The regression this guards: a plugin that marshalled its own body and passed
	// the resulting bytes here had them encoded a second time, because Body is a
	// value to encode rather than bytes to send. The result was a quoted base64
	// string, which the far end refused with a complaint about a request shape the
	// plugin had never written — and the refusal was then reported as the source
	// being unavailable, which sent the reader looking in the wrong place.
	//
	// Refusing the type makes that mistake a compile-time-shaped argument error
	// instead of a wrong request that only the live API notices.
	type payload []byte

	for name, body := range map[string]any{
		"bytes":              []byte(`{"mod_ids":["0"]}`),
		"a named byte slice": payload(`{"mod_ids":["0"]}`),
		"string":             `{"mod_ids":["0"]}`,
	} {
		var sent bool

		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			sent = true
		}))
		_, err := New(time.Second).Do(context.Background(), Request{
			Method: http.MethodPost, URL: srv.URL, Body: body, Kind: KindAuth,
		})
		srv.Close()

		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
			"Body of type %s (%s) = %v, want ErrCodeInvalidArgument: a body that is already JSON must be handed over as json.RawMessage",
			name,
			body,
			err,
		)

		assert.Contains(
			t,
			appErrors.Message(err),
			"json.RawMessage",
			"complaint for %s does not say how to send pre-encoded bytes: %v",
			name,
			err,
		)

		assert.False(
			t,
			sent,
			"a %s body was sent anyway, so the request left this machine malformed",
			name,
		)
	}
}

func TestDoSendsRawMessageThroughUnchanged(t *testing.T) {
	// json.RawMessage is the declared way to send bytes that are already JSON, so
	// it must arrive exactly as given rather than quoted or escaped.
	var (
		body        string
		contentType string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
	}))
	defer srv.Close()

	if _, err := New(time.Second).Do(context.Background(), Request{
		Method: http.MethodPost,
		URL:    srv.URL,
		Body:   json.RawMessage(`{"mod_ids":["0"],"cursor":null}`),
		Kind:   KindAuth,
	}); err != nil {
		require.FailNow(t, "RawMessage post: %v", err)
	}

	assert.Equal(
		t,
		`{"mod_ids":["0"],"cursor":null}`,
		body,
		"RawMessage body arrived as %q, want it byte for byte",
		body,
	)

	assert.True(
		t,
		strings.HasPrefix(contentType, "application/json"),
		"RawMessage body sent content-type %q",
		contentType,
	)
}

func TestEncodeBodyTreatsABase64NamedTypeAsBytes(t *testing.T) {
	// A plugin is free to name its own types, and reflect sees through the name:
	// without this the mistake is invisible to the check and only a live API
	// notices.
	type requestBody []byte

	if !alreadyEncoded(requestBody(nil)) {
		assert.Fail(t, "a named byte slice was treated as a value to encode")
	}

	if alreadyEncoded(json.RawMessage(nil)) {
		assert.Fail(t, "json.RawMessage must stay the escape hatch, not be refused")
	}

	if alreadyEncoded(nil) {
		assert.Fail(t, "a nil body is the absence of a body and must not be refused")
	}

	if alreadyEncoded(map[string]string{}) {
		assert.Fail(t, "a map is a value to encode")
	}
}

func TestGetJSONDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Better UI","version":"1.2.0"}`))
	}))
	defer srv.Close()

	var out struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	err := New(
		time.Second,
	).GetJSON(
		context.Background(),
		srv.URL,
		&out,
		nil,
		KindMeta,
	)
	require.NoError(t, err, "GetJSON: %v", err)

	if out.Name != "Better UI" || out.Version != "1.2.0" {
		assert.Fail(t, "decoded %+v", out)
	}
}

func TestGetJSONTreatsEmptyBodyAsNoResults(t *testing.T) {
	// Several providers answer 200 with an empty body for "nothing matched".
	// Decoding that would fail with a syntax error, which reads as "the source is
	// broken" instead of "no results".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	var out map[string]any

	err := New(
		time.Second,
	).GetJSON(
		context.Background(),
		srv.URL,
		&out,
		nil,
		KindSearch,
	)
	require.NoError(t, err, "empty body: %v", err)

	assert.Nil(t, out, "empty body decoded to %+v", out)
}

func TestGetJSONRejectsGarbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	var out map[string]any

	err := New(
		time.Second,
	).GetJSON(
		context.Background(),
		srv.URL,
		&out,
		nil,
		KindSearch,
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeSearchFailed,
	), "garbage body = %v, want search failed", err)
}

func TestGetJSONNilTargetSkipsDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json at all"))
	}))
	defer srv.Close()

	err := New(
		time.Second,
	).GetJSON(
		context.Background(),
		srv.URL,
		nil,
		nil,
		KindMeta,
	)
	assert.NoError(t, err, "nil target with garbage body = %v, want nil", err)
}

func TestDoRejectsOversizedResponse(t *testing.T) {
	// A source that answers 200 with a huge body must fail loudly. Returning the
	// fragment would decode into a half-populated struct with no error at all.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	}))
	defer srv.Close()

	_, err := New(
		time.Second,
	).Do(
		context.Background(),
		Request{URL: srv.URL, Kind: KindMeta, limit: 1024},
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeSourceUnavailable),
		"oversized response = %v",
		err,
	)
}

func TestRequestLimitFor(t *testing.T) {
	if got := (Request{}).limitFor(); got != MaxJSONBody {
		assert.Fail(t, "default limit = %d, want %d", got, MaxJSONBody)
	}

	if got := (Request{Kind: KindDownload}).limitFor(); got != MaxDownloadBody {
		assert.Fail(t, "download limit = %d, want %d", got, MaxDownloadBody)
	}

	if got := (Request{limit: 7}).limitFor(); got != 7 {
		assert.Fail(t, "explicit limit = %d", got)
	}
	// A negative limit means "no limit", which is how a source opts out of the
	// cap for a body it has already bounded itself.
	if got := (Request{limit: -1}).limitFor(); got != 0 {
		assert.Fail(t, "negative limit = %d, want 0", got)
	}
	// An explicit limit beats the per-kind default.
	if got := (Request{Kind: KindDownload, limit: 5}).limitFor(); got != 5 {
		assert.Fail(t, "explicit download limit = %d", got)
	}
}

// names lists directory entry names for a failure message.
func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}

	return out
}

func TestDownloadWritesFile(t *testing.T) {
	payload := []byte("archive bytes")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "nested", "download.bin")

	got, err := New(time.Second).Download(context.Background(), srv.URL, dest, nil, "")
	require.NoError(t, err, "Download")

	assert.Equal(t, int64(len(payload)), got, "wrote %d bytes, want %d", got, len(payload))

	data, err := os.ReadFile(dest)
	require.NoError(t, err, "read back")

	assert.Equal(t, string(payload), string(data), "content = %q", data)
	// A staged download must not leave its temporary file behind.
	entries, err := os.ReadDir(filepath.Dir(dest))
	require.NoError(t, err, "read dir")

	if len(entries) != 1 || entries[0].Name() != "download.bin" {
		assert.Fail(t, "directory contains %v, want only download.bin", names(entries))
	}
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	payload := []byte("verified payload")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	// Both spellings a host might use must be accepted, upper or lower case.
	for _, form := range []string{digest, strings.ToUpper(digest), "sha256:" + digest} {
		dest := filepath.Join(t.TempDir(), "d.bin")
		_, err := New(
			time.Second,
		).Download(
			context.Background(),
			srv.URL,
			dest,
			nil,
			form,
		)
		assert.NoError(t, err, "Download with digest form %q: %v", form, err)
	}

	bad := filepath.Join(t.TempDir(), "d.bin")

	_, err := New(
		time.Second,
	).Download(
		context.Background(),
		srv.URL,
		bad,
		nil,
		strings.Repeat("0", 64),
	)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeIntegrity),
		"digest mismatch = %v, want integrity",
		err,
	)
	// The rejected payload must not be published: an install must never see an
	// archive the source did not vouch for.
	_, statErr := os.Stat(bad)
	assert.True(t, os.IsNotExist(statErr), "mismatched download was published to %q", bad)
}

func TestDownloadRejectsEmptyBody(t *testing.T) {
	// A zero-byte "success" would be unpacked into an empty mod folder and look
	// like a successful install of nothing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "d.bin")
	_, err := New(
		time.Second,
	).Download(
		context.Background(),
		srv.URL,
		dest,
		nil,
		"",
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeDownloadFailed,
	), "empty download = %v", err)

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		assert.Fail(t, "empty download produced a file")
	}
}

func TestDownloadReplacesExistingSymlink(t *testing.T) {
	// os.Rename onto an existing symlink replaces the link on Unix, but the
	// destination is removed first so the behaviour cannot depend on the platform.
	outside := filepath.Join(t.TempDir(), "outside.bin")
	err := os.WriteFile(outside, []byte("must survive"), 0o644)
	require.NoError(t, err)

	dir := t.TempDir()

	dest := filepath.Join(dir, "d.bin")
	if err := os.Symlink(outside, dest); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fresh"))
	}))
	defer srv.Close()

	_, err = New(
		time.Second,
	).Download(
		context.Background(),
		srv.URL,
		dest,
		nil,
		"",
	)
	require.NoError(t, err, "Download over a symlink: %v", err)

	data, _ := os.ReadFile(outside)
	assert.Equal(t, "must survive", string(data), "the symlink target was overwritten: %q", data)
}

func TestDownloadErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "d.bin")

	_, err := New(time.Second).Download(context.Background(), srv.URL, dest, nil, "")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeAuthFailed),
		"403 download = %v, want auth failed",
		err,
	)

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		assert.Fail(t, "failed download left a file")
	}
}

func TestDownloadTransportFailure(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "d.bin")
	// Port 0 on the loopback interface has nothing listening.
	_, err := New(time.Second).Download(context.Background(), "http://127.0.0.1:0/x", dest, nil, "")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeDownloadFailed),
		"transport failure = %v, err",
		err,
	)
}

func TestDownloadBlankDestination(t *testing.T) {
	_, err := New(
		time.Second,
	).Download(
		context.Background(),
		"https://example.test/x",
		"  ",
		nil,
		"",
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	), "blank destination = %v", err)
}

func TestDownloadSendsHeaders(t *testing.T) {
	var auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	header := http.Header{}
	header.Set("Authorization", "Bearer abc")

	_, err := New(
		time.Second,
	).Download(
		context.Background(),
		srv.URL,
		filepath.Join(t.TempDir(), "d"),
		header,
		"",
	)
	require.NoError(t, err, "Download: %v", err)

	assert.Equal(t, "Bearer abc", auth, "Authorization = %q", auth)
}

func TestCanceledContextShortCircuits(t *testing.T) {
	// The cancellation must be reported before any socket is opened, so the code
	// is the manager's cancellation code rather than a network error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := New(
		time.Second,
	).Do(
		ctx,
		Request{URL: "https://example.test/x"},
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeOperationCanceled,
	) {
		assert.Fail(t, "Do = %v, want operation canceled", err)
	}

	_, err := New(
		time.Second,
	).Download(
		ctx,
		"https://example.test/x",
		filepath.Join(t.TempDir(), "d"),
		nil,
		"",
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeOperationCanceled,
	), "Download = %v, want operation canceled", err)
}

func TestDeadlineExceededIsCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := New(
		time.Second,
	).Do(
		ctx,
		Request{URL: srv.URL},
	); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeOperationCanceled,
	) {
		assert.Fail(t, "expired deadline = %v, want operation canceled", err)
	}
}

func TestNilContextTolerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	//nolint:staticcheck // nil context passing is being tested here.
	err := New(time.Second).GetJSON(nil, srv.URL, nil, nil, KindMeta)
	assert.NoError(t, err, "nil context = %v", err)

	//nolint:staticcheck // nil context passing is being tested here
	_, err = New(
		time.Second,
	).Download(
		nil,
		srv.URL,
		filepath.Join(t.TempDir(), "d"),
		nil,
		"",
	)
	assert.NoError(t, err, "nil context download = %v", err)
}

func TestReadCapped(t *testing.T) {
	_, err := readCapped(strings.NewReader("abc"), 0)
	assert.NoError(t, err, "no limit: %v", err)

	if data, err := readCapped(strings.NewReader("abc"), 3); err != nil || string(data) != "abc" {
		assert.Fail(t, "exactly at the limit = %q, %v", data, err)
	}

	if _, err := readCapped(strings.NewReader("abcd"), 3); err == nil {
		assert.Fail(t, "one byte over the limit was accepted")
	}
}

func TestJoinErrors(t *testing.T) {
	err := JoinErrors(nil)
	assert.NoError(t, err, "nil = %v", err)
	// A slice of nils (what a loop that found no problems produces) is not an error.
	if err := JoinErrors([]error{nil, nil}); err != nil {
		assert.Fail(t, "all nil = %v", err)
	}

	first := appErrors.New(appErrors.ErrCodeSourceUnavailable, "github: bad key")

	single := JoinErrors([]error{nil, first})
	assert.Equal(t, first, single, "single error was rewrapped: %v", single)

	both := JoinErrors(
		[]error{first, appErrors.New(appErrors.ErrCodeSourceUnavailable, "nexus: offline")},
	)
	assert.True(
		t,
		appErrors.IsAppError(both, appErrors.ErrCodeSourceUnavailable),
		"joined error lost its code: %v",
		both,
	)
	// Both problems must be visible: the caller wants to know every source broke.
	if !strings.Contains(both.Error(), "github") || !strings.Contains(both.Error(), "nexus") {
		assert.Fail(t, "joined error dropped a problem: %v", both)
	}

	assert.Contains(t, both.Error(), "2 problems", "joined error does not count: %v", both)
}

func TestClientDoAcceptsLargeDeclaredResponses(t *testing.T) {
	// A guard that the JSON cap is not accidentally applied to a field the
	// manager does not decode: a 2 MiB body under the 16 MiB cap must decode.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"notes":"` + strings.Repeat("y", 2<<20) + `"}`))
	}))
	defer srv.Close()

	var out struct {
		Notes string `json:"notes"`
	}

	err := New(
		5*time.Second,
	).GetJSON(
		context.Background(),
		srv.URL,
		&out,
		nil,
		KindMeta,
	)
	require.NoError(t, err, "large body: %v", err)

	assert.Len(t, out.Notes, 2<<20, "decoded %d bytes", len(out.Notes))
}

func TestResponseHeaderIsCloned(t *testing.T) {
	// A caller that mutates the returned header must not corrupt the transport's
	// view of the response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Trace", "abc")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	resp, err := New(time.Second).Do(context.Background(), Request{URL: srv.URL, Kind: KindMeta})
	require.NoError(t, err, "Do")

	resp.Header.Set("X-Trace", "mutated")

	if resp.Header.Get("X-Trace") != "mutated" {
		assert.Fail(t, "header was not the caller's own copy")
	}
}
