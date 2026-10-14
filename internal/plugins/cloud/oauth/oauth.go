// Package oauth holds the token half of an OAuth2 cloud provider.
//
// Google Drive, Dropbox and OneDrive differ in every detail of where a file
// lives and what a link looks like, and are identical in how they prove who is
// asking. That half lives here so each adapter only describes its own API.
//
// A token is the one thing in this package that must never be written down: it
// is read from credentials, held in memory, and never placed in a URL, a share
// link or a log line. Everything that carries it travels in a header.
package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

const userAgent = "darktide-mod-manager/1.0"

// refreshMargin is how long before expiry a token is renewed. Refreshing on
// expiry rather than before it loses whatever request was in flight when the
// old one ran out.
const refreshMargin = 2 * time.Minute

// responseLimit caps a token response. The document is small and uninteresting;
// the cap is only there so a provider answering with something enormous cannot
// be read into memory.
const responseLimit = 1 << 20

// Endpoints names the OAuth2 URLs a provider uses.
type Endpoints struct {
	TokenURL string
	// ConsentURL opens the page a user approves the app on. It is only reached
	// by ConsentURL, which builds it from the session's client id.
	ConsentURL string
}

// Config is what a provider knows before anyone authenticates.
type Config struct {
	ClientID     string
	ClientSecret string
	Endpoints    Endpoints
}

// Token is an access token plus what is needed to renew it.
type Token struct {
	Expiry       time.Time
	AccessToken  string
	RefreshToken string
}

// Valid reports whether the token can still carry a request.
func (t Token) Valid() bool {
	return t.AccessToken != "" && time.Now().Before(t.Expiry.Add(-refreshMargin))
}

// Session holds one provider's credentials for the lifetime of the process.
//
// The mutex is held across a refresh so two concurrent uploads do not each
// spend the same refresh token: providers invalidate one of the two, which
// turns a working second upload into an auth failure.
type Session struct {
	token Token
	cfg   Config
	mu    sync.Mutex
}

func New(cfg Config) *Session {
	return &Session{cfg: cfg}
}

// ConsentURL is the page the user approves the app on. It carries the client id
// and the requested scopes — never a secret, because it goes into a browser
// address bar and into browser history.
func (s *Session) ConsentURL(redirectURL, state, scopes string) string {
	if s == nil || s.cfg.Endpoints.ConsentURL == "" {
		return ""
	}

	query := url.Values{}
	query.Set("client_id", s.cfg.ClientID)
	query.Set("response_type", "code")

	if redirectURL != "" {
		query.Set("redirect_uri", redirectURL)
	}

	if state != "" {
		query.Set("state", state)
	}

	if scopes != "" {
		query.Set("scope", scopes)
	}

	return s.cfg.Endpoints.ConsentURL + "?" + query.Encode()
}

// Authenticate accepts credentials from the caller.
//
// The usual input is an access token the user obtained through the provider's
// own consent screen, optionally with the refresh token that came with it. A
// refresh token is what makes a session survive a restart; without one the
// user re-authenticates when the access token expires.
func (s *Session) Authenticate(creds domain.CloudCredentials) error {
	if s == nil {
		return domain.ErrCloudAuthRequired
	}

	access := valueOf(creds, "access_token", "token")
	if access == "" {
		return &AuthError{Reason: "an access token is required"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.token = Token{
		AccessToken:  access,
		RefreshToken: valueOf(creds, "refresh_token"),
		Expiry:       expiryFrom(creds),
	}

	return nil
}

// Authorize exchanges a consent code for a token and stores the result.
//
// This is the path the GUI takes: it opens the provider's consent page, the
// user approves, and the provider redirects to a loopback address carrying a
// single-use code. No credential appears in a command line or a log; the code
// is short-lived and tells an attacker nothing on its own.
func (s *Session) Authorize(ctx context.Context, code, redirectURL string) error {
	if s == nil {
		return domain.ErrCloudAuthRequired
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)

	if redirectURL != "" {
		form.Set("redirect_uri", redirectURL)
	}

	token, err := s.exchange(ctx, form)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.token = token

	return nil
}

// ValidateCredentials reports whether the session can still reach the provider.
//
// It refreshes when it can and only fails when there is nothing left to try,
// so a caller asking "is this configured?" is not told no because a token
// expired an hour ago.
func (s *Session) ValidateCredentials(ctx context.Context) error {
	if s == nil {
		return domain.ErrCloudAuthRequired
	}

	_, err := s.Access(ctx)

	return err
}

// Forget drops every credential the session holds. The next call has to
// re-authenticate.
func (s *Session) Forget() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.token = Token{}
}

// Access returns a usable access token, refreshing it when the current one is
// close to expiry.
//
// The token itself is returned rather than a ready-made header so an adapter
// can put it wherever its API expects it, and so no caller is handed something
// it could log by reflex.
func (s *Session) Access(ctx context.Context) (string, error) {
	if s == nil {
		return "", domain.ErrCloudAuthRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token.AccessToken == "" {
		return "", &AuthError{Reason: "no token has been provided"}
	}

	if s.token.Valid() {
		return s.token.AccessToken, nil
	}

	if s.token.RefreshToken == "" {
		return "", &AuthError{Reason: "the token expired and there is no refresh token"}
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", s.token.RefreshToken)

	refreshed, err := s.exchangeLocked(ctx, form)
	if err != nil {
		return "", err
	}

	// A refresh response that omits the refresh token is the provider keeping
	// the one it was already sent, not revoking it.
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = s.token.RefreshToken
	}

	s.token = refreshed

	return s.token.AccessToken, nil
}

// exchangeLocked performs a token request. The caller holds s.mu.
func (s *Session) exchangeLocked(ctx context.Context, form url.Values) (Token, error) {
	if s.cfg.Endpoints.TokenURL == "" {
		return Token{}, &AuthError{Reason: "no token endpoint is configured"}
	}

	body := strings.NewReader(form.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoints.TokenURL, body)
	if err != nil {
		return Token{}, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"build the token request")
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	httpClient := &http.Client{Timeout: 60 * time.Second}

	resp, err := httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Token{}, ctxErr
		}

		return Token{}, appErrors.Wrap(err, appErrors.ErrCodeSourceUnavailable,
			"reach the token endpoint")
	}

	defer func() { _ = resp.Body.Close() }()

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
		ExpiresIn    int64  `json:"expires_in"`
	}

	// The body is read before the status is checked because a provider puts
	// the reason a token was refused in the body, not in the status line.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	if err != nil {
		return Token{}, appErrors.Wrap(err, appErrors.ErrCodeSourceUnavailable,
			"read the token response")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Token{}, &AuthError{Reason: reasonFrom(resp.StatusCode, raw)}
	}

	if err := json.Unmarshal(raw, &payload); err != nil {
		return Token{}, appErrors.Wrap(err, appErrors.ErrCodeSourceRejected,
			"decode the token response")
	}

	if payload.AccessToken == "" {
		return Token{}, &AuthError{Reason: reasonFrom(resp.StatusCode, raw)}
	}

	return Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

// exchange is the unlocked form, for a caller that has not stored a token yet.
func (s *Session) exchange(ctx context.Context, form url.Values) (Token, error) {
	if s == nil {
		return Token{}, domain.ErrCloudAuthRequired
	}

	return s.exchangeLocked(ctx, form)
}

// reasonFrom explains a refused token request.
//
// The machine-readable code is preferred over the free-text description, and the
// description is not used at all: it is prose the provider chose, and providers
// have been known to echo the request back into it — which held the client
// secret. A status number is a worse message than a good one, but it is one that
// cannot leak a credential into a log line or a support ticket.
func reasonFrom(status int, raw []byte) string {
	var payload struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}

	if err := json.Unmarshal(raw, &payload); err == nil && payload.Error != "" {
		return payload.Error
	}

	return "the provider returned status " + strconv.Itoa(status)
}

// AuthError is a failure to authenticate. It carries no credential text, so it
// is safe to log and safe to show a user.
type AuthError struct {
	Reason string
}

func (e *AuthError) Error() string {
	if e == nil || e.Reason == "" {
		return "cloud authentication failed"
	}

	return "cloud authentication failed: " + e.Reason
}

// IsMissing reports whether an error is the provider saying "there is no such
// object".
//
// A revoke asks the provider to remove files it may already have dropped, and
// being told one is gone is the answer it wanted. Anything else is a real
// failure and has to be reported as one.
func IsMissing(err error) bool {
	return appErrors.CodeOf(err) == appErrors.ErrCodeNotFound
}

// IsAuthError reports whether an error is one of these.
func IsAuthError(err error) bool {
	var authErr *AuthError

	return errors.As(err, &authErr)
}

// valueOf looks a credential up by any of its aliases.
func valueOf(creds domain.CloudCredentials, names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(creds[name]); v != "" {
			return v
		}
	}

	return ""
}

// expiryFrom reads an explicit expiry out of credentials. A caller that knows
// when its token dies can say so; otherwise the token is treated as expiring
// immediately, which forces the refresh that will tell the user the truth.
func expiryFrom(creds domain.CloudCredentials) time.Time {
	raw := valueOf(creds, "expires_at", "expiry")
	if raw == "" {
		return time.Time{}
	}

	if at, err := time.Parse(time.RFC3339, raw); err == nil {
		return at
	}

	return time.Time{}
}

// bodyReader turns a caller's bytes into a reader. A nil body is a request
// with no payload, which is what a GET or a DELETE is.
func bodyReader(body []byte) io.Reader {
	if body == nil {
		return nil
	}

	return bytes.NewReader(body)
}

// Send performs an authenticated request with a caller-built body.
//
// Adapters use this for the small metadata calls that make up the rest of a
// provider's API. File transfers go through Put and Get, which stream instead of
// buffering.
func (s *Session) Send(
	ctx context.Context,
	method, rawURL string,
	body []byte,
	header http.Header,
) (*Result, error) {
	if s == nil {
		return nil, domain.ErrCloudAuthRequired
	}

	t := &transport{session: s}

	got, gotHeader, err := t.raw(ctx, method, rawURL, bodyReader(body), header)
	if err != nil {
		return nil, err
	}

	return &Result{Body: got, Header: gotHeader}, nil
}

// Get streams an authenticated download into destPath.
func (s *Session) Get(ctx context.Context, rawURL, destPath string) error {
	if s == nil {
		return domain.ErrCloudAuthRequired
	}

	return (&transport{session: s}).get(ctx, rawURL, destPath)
}

// Put streams a local file up.
func (s *Session) Put(
	ctx context.Context,
	method, rawURL, localPath string,
	header http.Header,
	size int64,
) (*Result, error) {
	if s == nil {
		return nil, domain.ErrCloudAuthRequired
	}

	got, err := (&transport{session: s}).put(ctx, method, rawURL, localPath, header, size)
	if err != nil {
		return nil, err
	}

	return &Result{Header: got}, nil
}

// Result is a completed call's body and headers.
type Result struct {
	Header http.Header
	Body   []byte
}

// SendJSON performs an authenticated call with a JSON body.
//
// Providers with a JSON RPC style — Dropbox among them — need nowhere else to
// put their arguments, so the body is marshalled here rather than by each
// adapter.
func (s *Session) SendJSON(
	ctx context.Context,
	method, rawURL string,
	payload any,
) (*Result, error) {
	if s == nil {
		return nil, domain.ErrCloudAuthRequired
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument, "encode the request")
	}

	header := http.Header{}
	header.Set("Content-Type", "application/json")

	return (&transport{session: s}).call(ctx, method, rawURL, bytes.NewReader(raw), header)
}

// GetWithHeaders streams an authenticated download into destPath, adding the
// headers an API needs alongside the token.
func (s *Session) GetWithHeaders(
	ctx context.Context,
	rawURL, destPath string,
	header http.Header,
) error {
	if s == nil {
		return domain.ErrCloudAuthRequired
	}

	return (&transport{session: s}).getWithHeaders(ctx, rawURL, destPath, header)
}

// PutRange streams a local file into an open upload session.
//
// The session URL is the provider's, not ours: it already carries the grant for
// that one upload, which is precisely why it is never logged and never put in a
// share link.
func (s *Session) PutRange(
	ctx context.Context,
	uploadURL, localPath string,
	total int64,
	onBody func([]byte),
) error {
	if s == nil {
		return domain.ErrCloudAuthRequired
	}

	return (&transport{session: s}).putRange(ctx, uploadURL, localPath, total, onBody, 0)
}
