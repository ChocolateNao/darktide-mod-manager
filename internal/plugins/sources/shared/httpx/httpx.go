// Package httpx is the shared HTTP plumbing for the network-backed mod sources.
//
// The sources differ in their payloads and auth schemes, but not in how they talk
// to a server: a timeout, a User-Agent (several mod hosts reject requests without
// one), JSON encode/decode, and a mapping from HTTP status onto the manager's
// typed error codes. One place means a status is interpreted the same way by every
// source and a test can pin the mapping once. None of it reaches pkg/api.
package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

const (
	// DefaultTimeout bounds a single request. Mod hosts are frequently slow, but
	// not this slow; a hung connection must not pin an install forever.
	DefaultTimeout = 10 * time.Second

	// DefaultUserAgent identifies the manager to hosts that rate-limit by client.
	DefaultUserAgent = "darktide-mod-manager"

	// MaxJSONBody caps a decoded JSON response. A source that answers with a
	// gigabyte of HTML (an error page, a captive portal) must not exhaust memory
	// before the status code is even looked at.
	MaxJSONBody = 16 << 20 // 16 MiB

	// MaxDownloadBody caps a payload download. Mod archives are hundreds of
	// megabytes at the very top end; 8 GiB leaves plenty of headroom while still
	// bounding a runaway transfer.
	MaxDownloadBody = 8 << 30 // 8 GiB

	// snippetLimit bounds how much of an error body is quoted back to the user.
	// Enough to identify the problem, not enough to flood the log.
	snippetLimit = 256
)

// Kind is the operation being performed. The status-to-code mapping depends on it:
// a 404 while listing mods means "no such mod", but a 404 while searching means the
// endpoint moved, and reporting the first sends the user after a typo they did not make.
type Kind int

const (
	// KindMeta is a metadata lookup such as fetching one mod.
	KindMeta Kind = iota
	// KindSearch is a listing query.
	KindSearch
	// KindAuth is a credential check or refresh.
	KindAuth
	// KindDownload is a payload transfer.
	KindDownload
)

func (k Kind) String() string {
	switch k {
	case KindMeta:
		return "metadata request"
	case KindSearch:
		return "search"
	case KindAuth:
		return "authentication"
	case KindDownload:
		return "download"
	default:
		return "request"
	}
}

// fallbackCode is the error code used for a status that no more specific rule
// claims.
func (k Kind) fallbackCode() appErrors.Code {
	switch k {
	case KindSearch:
		return appErrors.ErrCodeSearchFailed
	case KindDownload:
		return appErrors.ErrCodeDownloadFailed
	default:
		return appErrors.ErrCodeSourceUnavailable
	}
}

// Client is a small JSON-oriented HTTP client bound to one User-Agent.
type Client struct {
	http      *http.Client
	userAgent string
}

// New returns a client with the given per-request timeout. A non-positive
// timeout falls back to DefaultTimeout: zero would mean "no timeout", which for
// a network call is never what the caller meant.
func New(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &Client{
		http:      &http.Client{Timeout: timeout},
		userAgent: DefaultUserAgent,
	}
}

// WithUserAgent returns a copy of c that identifies itself as userAgent. A blank
// value keeps the default, because some hosts reject an empty User-Agent
// outright and that failure would be baffling.
func (c *Client) WithUserAgent(userAgent string) *Client {
	out := *c
	if trimmed := strings.TrimSpace(userAgent); trimmed != "" {
		out.userAgent = trimmed
	}

	return &out
}

// WithTimeout returns a copy of c whose per-request timeout is d. A non-positive
// value is ignored for the same reason New ignores it. The copy is required because
// a source builds its client once and several options may each want to tune it.
func (c *Client) WithTimeout(d time.Duration) *Client {
	out := *c
	if d > 0 {
		out.http = &http.Client{Timeout: d}
	}

	return &out
}

// HTTPClient exposes the underlying client so a source can use a feature this
// wrapper does not model (redirect control, for instance).
func (c *Client) HTTPClient() *http.Client {
	if c == nil || c.http == nil {
		return &http.Client{Timeout: DefaultTimeout}
	}

	return c.http
}

// Request describes one call.
type Request struct {
	Body   any
	Query  url.Values
	Header http.Header
	Form   url.Values
	Method string
	URL    string
	Kind   Kind
	limit  int64
}

// encodeBody renders a request body as JSON.
//
// httpx owns JSON encoding, so handing it bytes would double-encode the value and
// draw a 422 naming a request shape the caller never wrote. It refuses the two
// types json.Marshal would quote rather than render, and exempts json.RawMessage,
// which exists precisely to say "these bytes are already JSON".
func encodeBody(kind Kind, body any) ([]byte, error) {
	if alreadyEncoded(body) {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"a request body must be a value to encode, not %T; pass the value "+
				"itself, or json.RawMessage for bytes that are already JSON", body)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, appErrors.Wrap(err, kind.fallbackCode(), "encode %s body", kind)
	}

	return encoded, nil
}

// alreadyEncoded reports whether body is a type json.Marshal would wrap in a JSON
// string instead of rendering as a document. A nil body is not in that category:
// there is nothing to encode and the caller gets an empty payload either way.
func alreadyEncoded(body any) bool {
	if body == nil {
		return false
	}

	if _, ok := body.(json.RawMessage); ok {
		return false
	}

	switch value := reflect.ValueOf(body); value.Kind() {
	case reflect.String:
		return true
	case reflect.Slice:
		return value.Type().Elem().Kind() == reflect.Uint8
	default:
		return false
	}
}

// Limit overrides the response-size cap for this request. Zero keeps the
// per-kind default; a negative value means "no limit".
func (r Request) limitFor() int64 {
	switch {
	case r.limit > 0:
		return r.limit
	case r.limit < 0:
		return 0
	case r.Kind == KindDownload:
		return MaxDownloadBody
	default:
		return MaxJSONBody
	}
}

// Response is a completed request.
type Response struct {
	Header http.Header
	Body   []byte
	Status int
}

// OK reports a 2xx status.
func (r *Response) OK() bool {
	return r != nil && r.Status >= 200 && r.Status < 300
}

// Do executes req and returns the response for any status the status-to-code rules
// do not claim as an error. The body is read into memory and capped; a body over
// the cap is an error rather than a truncated success, since a half-read JSON
// document cannot be decoded and the fragment would turn a clear failure into a
// confusing one.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, ctxError(err)
	}

	if c == nil || c.http == nil {
		return nil, appErrors.New(appErrors.ErrCodeSourceUnavailable, "no HTTP client configured")
	}

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}

	target, err := buildURL(req.URL, req.Query)
	if err != nil {
		return nil, err
	}

	var payload io.Reader

	hasBody := false

	switch {
	case req.Form != nil:
		payload = strings.NewReader(req.Form.Encode())
		hasBody = true
	case req.Body != nil:
		encoded, err := encodeBody(req.Kind, req.Body)
		if err != nil {
			return nil, err
		}

		payload = bytes.NewReader(encoded)
		hasBody = true
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeInvalidArgument,
			"build %s for %q",
			req.Kind,
			target,
		)
	}

	httpReq.Header.Set("User-Agent", c.userAgent)
	// The Content-Type has to be set explicitly: net/http guesses it only for a
	// *bytes.Buffer, and the strings.Reader url.Values.Encode feeds to gets none.
	if hasBody {
		if req.Form != nil {
			httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			httpReq.Header.Set("Content-Type", "application/json")
		}
	}

	for name, values := range req.Header {
		for _, v := range values {
			httpReq.Header.Add(name, v)
		}
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxError(ctxErr)
		}

		return nil, appErrors.Wrap(err, req.Kind.fallbackCode(), "%s %s failed", req.Kind, target)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readCapped(resp.Body, req.limitFor())
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxError(ctxErr)
		}

		return nil, appErrors.Wrap(
			err,
			req.Kind.fallbackCode(),
			"read %s response from %s",
			req.Kind,
			target,
		)
	}

	out := &Response{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: body}
	if !out.OK() {
		return out, statusError(req.Kind, resp.StatusCode, target, body)
	}

	return out, nil
}

// GetJSON performs a GET and decodes the JSON body into out.
func (c *Client) GetJSON(
	ctx context.Context,
	rawURL string,
	out any,
	header http.Header,
	kind Kind,
) error {
	resp, err := c.Do(ctx, Request{Method: http.MethodGet, URL: rawURL, Header: header, Kind: kind})
	if err != nil {
		return err
	}

	if out == nil {
		return nil
	}
	// An empty body is not an error here: some providers answer 200 with nothing
	// for "no results", and decoding that would fail for the wrong reason.
	if len(bytes.TrimSpace(resp.Body)) == 0 {
		return nil
	}

	if err := json.Unmarshal(resp.Body, out); err != nil {
		return appErrors.Wrap(err, kind.fallbackCode(), "decode %s response from %q", kind, rawURL)
	}

	return nil
}

// buildURL validates and assembles the request target.
func buildURL(rawURL string, query url.Values) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "empty request URL")
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", appErrors.Wrap(
			err,
			appErrors.ErrCodeInvalidArgument,
			"parse request URL %q",
			trimmed,
		)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"request URL %q must be http or https", trimmed)
	}

	if len(query) == 0 {
		return u.String(), nil
	}
	// url.Values.Encode sorts the keys, which makes the URL stable and therefore
	// makes it safe to compare in a test.
	merged := u.Query()

	for key, values := range query {
		for _, v := range values {
			merged.Add(key, v)
		}
	}

	u.RawQuery = merged.Encode()

	return u.String(), nil
}

// statusError turns a non-2xx status into the typed error the manager expects. The
// rules are ordered from most specific to least:
//
//	401, 403 -> auth required/failed   the key is wrong, missing or expired
//	404     -> not found                (KindSearch: the endpoint, not the mod)
//	429     -> unavailable              rate limited, retry later
//	408, 5xx -> unavailable             the far end is unwell
//	other 4xx -> rejected               the request itself was refused
//	anything else -> the kind's fallback
//
// The 4xx rule is separate because of what it is not: a 422 "Invalid JSON input"
// means the service is up and disliked the request, and reporting that as
// SOURCE_UNAVAILABLE sends the user after a network problem they do not have and
// invites a retry loop against a source that is working as intended.
func statusError(kind Kind, status int, target string, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > snippetLimit {
		snippet = snippet[:snippetLimit] + "..."
	}
	// Hosts like to return HTML for an error, which is unreadable in a message.
	if strings.Contains(strings.ToLower(snippet), "<html") {
		snippet = ""
	}

	suffix := ""
	if snippet != "" {
		suffix = ": " + strconv.Quote(snippet)
	}

	switch {
	case status == http.StatusUnauthorized:
		return appErrors.New(appErrors.ErrCodeAuthRequired,
			"%s rejected the credentials for %s%s", kind, target, suffix)
	case status == http.StatusForbidden:
		return appErrors.New(appErrors.ErrCodeAuthFailed,
			"%s is forbidden for %s%s", kind, target, suffix)
	case status == http.StatusNotFound:
		if kind == KindSearch {
			return appErrors.New(appErrors.ErrCodeSourceUnavailable,
				"search endpoint not found at %s", target)
		}

		return appErrors.New(
			appErrors.ErrCodeNotFound,
			"%s not found at %s%s",
			kind,
			target,
			suffix,
		)
	case status == http.StatusTooManyRequests:
		return appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"rate limited by %s for %s, try again later", kind, target)
	case status == http.StatusRequestTimeout, status == http.StatusGatewayTimeout,
		status >= 500:
		return appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"%s to %s returned status %d%s", kind, target, status, suffix)
	case status >= 400:
		// The far end read the request and refused it, so both the status and the problem
		// document are quoted: a field name is the only thing that identifies which side
		// is wrong.
		return appErrors.New(appErrors.ErrCodeSourceRejected,
			"%s refused %s with status %d%s", kind, target, status, suffix)
	default:
		return appErrors.New(
			kind.fallbackCode(),
			"%s to %s returned status %d%s",
			kind,
			target,
			status,
			suffix,
		)
	}
}

// Download streams rawURL into destPath and returns the number of bytes written. The
// file is staged under a temporary name and renamed into place, so a cancelled or
// failed transfer never leaves something the extractor would happily unpack whole.
func (c *Client) Download(
	ctx context.Context,
	rawURL, destPath string,
	header http.Header,
	expectSHA string,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return 0, ctxError(err)
	}

	if strings.TrimSpace(destPath) == "" {
		return 0, appErrors.New(appErrors.ErrCodeInvalidArgument, "empty download destination")
	}

	if c == nil || c.http == nil {
		return 0, appErrors.New(appErrors.ErrCodeSourceUnavailable, "no HTTP client configured")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, appErrors.Wrap(
			err,
			appErrors.ErrCodeInvalidArgument,
			"build download for %q",
			rawURL,
		)
	}

	httpReq.Header.Set("User-Agent", c.userAgent)

	for name, values := range header {
		for _, v := range values {
			httpReq.Header.Add(name, v)
		}
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxError(ctxErr)
		}

		return 0, appErrors.Wrap(err, appErrors.ErrCodeDownloadFailed, "download %s", rawURL)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain a bounded amount so the connection can be reused, but do not let
		// a huge error body be buffered.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, snippetLimit))
		return 0, statusError(KindDownload, resp.StatusCode, rawURL, nil)
	}

	if dir := filepath.Dir(destPath); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return 0, appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"create download directory %q",
				dir,
			)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".dmm-download-*")
	if err != nil {
		return 0, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"create staging file for %q",
			destPath,
		)
	}

	tmpName := tmp.Name()
	committed := false

	defer func() {
		_ = tmp.Close()

		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	digest := sha256.New()
	// LimitReader one byte past the cap so "exactly at the cap" is allowed and
	// "one over" is detectable.
	limited := io.LimitReader(resp.Body, MaxDownloadBody+1)

	written, err := io.Copy(io.MultiWriter(tmp, digest), limited)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxError(ctxErr)
		}

		return 0, appErrors.Wrap(err, appErrors.ErrCodeDownloadFailed, "download %s", rawURL)
	}

	if written == 0 {
		return 0, appErrors.New(
			appErrors.ErrCodeDownloadFailed,
			"download %s returned no data",
			rawURL,
		)
	}

	if written > MaxDownloadBody {
		return 0, appErrors.New(appErrors.ErrCodeDownloadFailed,
			"download %s exceeds the %d byte limit", rawURL, int64(MaxDownloadBody))
	}

	if err := tmp.Sync(); err != nil {
		return 0, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "flush %q", tmpName)
	}

	if err := tmp.Close(); err != nil {
		return 0, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "close %q", tmpName)
	}

	if err := verifyDigest(
		destPath,
		tmpName,
		expectSHA,
		hex.EncodeToString(digest.Sum(nil)),
	); err != nil {
		return 0, err
	}

	if err := os.Chmod(tmpName, 0o600); err != nil {
		return 0, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "set permissions on %q", tmpName)
	}
	// A pre-existing file at destPath may itself be a symlink; Remove first so the
	// rename cannot follow it out of the cache directory.
	_ = os.Remove(destPath)
	if err := os.Rename(tmpName, destPath); err != nil {
		return 0, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "publish download to %q", destPath)
	}

	committed = true

	return written, nil
}

// verifyDigest compares the streamed digest against what the source promised. A blank
// expectation skips the check: plenty of hosts publish no checksum, and refusing to
// install unverified content would be worse than trusting the transport.
func verifyDigest(destPath, tmpName, expectSHA, got string) error {
	want := strings.ToLower(strings.TrimSpace(expectSHA))
	if want == "" {
		return nil
	}

	want = strings.TrimPrefix(want, "sha256:")
	if got == want {
		return nil
	}

	return appErrors.New(appErrors.ErrCodeIntegrity,
		"checksum mismatch for %s: expected %s, got %s", filepath.Base(destPath), want, got)
}

// readCapped reads at most limit bytes, reporting an error when the source has
// more to give.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}

	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}

	return data, nil
}

// ctxError maps a context failure onto the manager's cancellation code so the UI
// can tell "the user pressed stop" from "the network broke".
func ctxError(err error) error {
	if errors.Is(err, context.Canceled) {
		return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled, "request canceled")
	}

	return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled, "request stopped: %v", err)
}

// JoinErrors combines several failures into one. It keeps the first error's code so
// callers can still branch on it, and lists the rest as text so RegisterAll can
// report every broken source at once instead of only the first.
func JoinErrors(errs []error) error {
	var kept []error

	for _, err := range errs {
		if err != nil {
			kept = append(kept, err)
		}
	}

	switch len(kept) {
	case 0:
		return nil
	case 1:
		return kept[0]
	}

	parts := make([]string, 0, len(kept))
	for _, err := range kept {
		parts = append(parts, err.Error())
	}

	return appErrors.Wrap(kept[0], appErrors.CodeOf(kept[0]),
		"%d problems: %s", len(kept), strings.Join(parts, "; "))
}
