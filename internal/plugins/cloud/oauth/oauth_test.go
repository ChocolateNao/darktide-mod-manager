package oauth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenServer is a stand-in for a provider's token endpoint that answers with
// whatever the test wants it to say.
type tokenServer struct {
	server   *httptest.Server
	token    string
	refresh  string
	body     string
	requests []string
	expires  int64
	status   int
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()

	ts := &tokenServer{
		token:   "access-token",
		refresh: "refresh-token",
		expires: 3600,
		status:  http.StatusOK,
	}

	ts.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		ts.requests = append(ts.requests, r.Form.Get("grant_type"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(ts.status)

		if ts.body != "" {
			_, _ = w.Write([]byte(ts.body))

			return
		}

		_, _ = w.Write([]byte(`{"access_token":"` + ts.token +
			`","refresh_token":"` + ts.refresh + `","expires_in":` + itoa(ts.expires) + `}`))
	}))

	t.Cleanup(ts.server.Close)

	return ts
}

func (ts *tokenServer) session() *Session {
	return New(Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Endpoints:    Endpoints{TokenURL: ts.server.URL + "/token"},
	})
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}

	var digits []byte

	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}

	return string(digits)
}

func TestAuthenticateStoresTheToken(t *testing.T) {
	s := newTokenServer(t).session()

	require.NoError(t, s.Authenticate(map[string]string{
		"access_token": "tok",
		"expires_at":   "2999-01-01T00:00:00Z",
	}))

	got, err := s.Access(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok", got)
}

func TestAuthenticateRequiresAToken(t *testing.T) {
	s := newTokenServer(t).session()

	assert.Error(t, s.Authenticate(map[string]string{"refresh_token": "r"}))

	// With nothing stored, a later call has to fail rather than send an empty
	// bearer header and let the provider be the one to say no.
	_, err := s.Access(context.Background())
	assert.Error(t, err)
}

func TestAccessRefreshesAnExpiredToken(t *testing.T) {
	ts := newTokenServer(t)
	s := ts.session()

	require.NoError(t, s.Authenticate(map[string]string{
		"access_token":  "old",
		"refresh_token": "refresh-token",
		// An expiry in the past forces the refresh path.
		"expires_at": "2000-01-01T00:00:00Z",
	}))

	got, err := s.Access(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "access-token", got, "the refreshed token replaces the expired one")
	require.Equal(t, []string{"refresh_token"}, ts.requests,
		"a refresh must spend the refresh token, not re-authenticate")
}

func TestAccessFailsWithoutARefreshToken(t *testing.T) {
	s := newTokenServer(t).session()

	require.NoError(t, s.Authenticate(map[string]string{
		"access_token": "old",
		"expires_at":   "2000-01-01T00:00:00Z",
	}))

	_, err := s.Access(context.Background())
	require.Error(t, err)

	var authErr *AuthError
	assert.True(t, errors.As(err, &authErr), "the reason must be an AuthError")
}

func TestRefreshKeepsARefreshTokenTheProviderOmitted(t *testing.T) {
	ts := newTokenServer(t)
	// A refresh response that omits the refresh token is the provider keeping the
	// one it was already sent, not revoking it.
	ts.body = `{"access_token":"next","expires_in":3600}`

	s := ts.session()

	require.NoError(t, s.Authenticate(map[string]string{
		"access_token":  "old",
		"refresh_token": "keep-me",
		"expires_at":    "2000-01-01T00:00:00Z",
	}))

	_, err := s.Access(context.Background())
	require.NoError(t, err)

	// Spending the same token twice has to work: the session still holds it.
	require.NoError(t, s.Authenticate(map[string]string{
		"access_token":  "old",
		"refresh_token": "keep-me",
		"expires_at":    "2000-01-01T00:00:00Z",
	}))

	_, err = s.Access(context.Background())
	require.NoError(t, err)
}

func TestARefusedTokenCarriesNoCredential(t *testing.T) {
	ts := newTokenServer(t)
	ts.status = http.StatusBadRequest
	// A provider's error document can echo the request back, and the request
	// held the client secret.
	ts.body = `{"error":"invalid_grant","error_description":"code rejected client_secret=client-secret"}`

	s := ts.session()

	err := s.Authorize(context.Background(), "a-code", "")
	require.Error(t, err)

	assert.NotContains(t, err.Error(), "client-secret",
		"a refusal must not quote the credential that was sent")
	assert.Contains(t, err.Error(), "invalid_grant")
}

func TestForgetDropsTheToken(t *testing.T) {
	s := newTokenServer(t).session()

	require.NoError(t, s.Authenticate(map[string]string{
		"access_token": "tok",
		"expires_at":   "2999-01-01T00:00:00Z",
	}))

	s.Forget()

	_, err := s.Access(context.Background())
	assert.Error(t, err)
}

func TestConsentURLCarriesNoSecret(t *testing.T) {
	s := New(Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Endpoints:    Endpoints{ConsentURL: "https://example.com/consent"},
	})

	got := s.ConsentURL("http://127.0.0.1:8080/cb", "state-value", "scope-a")

	// This URL lands in a browser address bar and in browser history, so the
	// client secret has no business in it.
	assert.Contains(t, got, "client_id=client-id")
	assert.Contains(t, got, "state=state-value")
	assert.Contains(t, got, "scope=scope-a")
	assert.NotContains(t, got, "client-secret")
}

func TestConsentURLIsEmptyWithoutAnEndpoint(t *testing.T) {
	s := New(Config{ClientID: "id"})

	assert.Empty(t, s.ConsentURL("", "", ""))
}

func TestNilSessionIsSafe(t *testing.T) {
	var s *Session

	assert.Error(t, s.Authenticate(map[string]string{"access_token": "x"}))
	assert.Error(t, s.ValidateCredentials(context.Background()))
	assert.Error(t, s.Authorize(context.Background(), "code", ""))
	s.Forget()

	_, err := s.Access(context.Background())
	assert.Error(t, err)

	assert.Empty(t, s.ConsentURL("", "", ""))
}

func TestMissingErrorsAreRecognised(t *testing.T) {
	// 404 from a provider is "there is no such object", which is what a revoke
	// wants to hear.
	err := &AuthError{Reason: reasonFrom(http.StatusNotFound, []byte("not_found"))}

	assert.True(t, IsAuthError(err))
	assert.False(t, IsMissing(err))
}

func TestValueOfTrimsAndAliases(t *testing.T) {
	creds := map[string]string{"access_token": "  spaced  ", "token": "unused"}

	assert.Equal(t, "spaced", valueOf(creds, "access_token", "token"))
	assert.Equal(t, "unused", valueOf(creds, "absent", "token"))
	assert.Empty(t, valueOf(creds, "absent"))
}

func TestExpiryFromToleratesAWorkingTime(t *testing.T) {
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	got := expiryFrom(map[string]string{"expires_at": at.Format(time.RFC3339)})
	assert.Equal(t, at, got)

	// A value that does not parse must not pin the token in the future, or a
	// dead token would be treated as live until that date.
	assert.True(t, expiryFrom(map[string]string{"expires_at": "nonsense"}).IsZero())
}

func TestDomainSentinelChains(t *testing.T) {
	var s *Session

	err := s.Authenticate(nil)
	assert.ErrorIs(t, err, domain.ErrCloudAuthRequired)
}

func TestBasicAuthIsBase64Encoded(t *testing.T) {
	// The value is base64(user:pass), not the plain credential.
	raw := "client-id:client-secret"
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(raw)),
		basicAuth("client-id", "client-secret"))
}

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestReasonFromFallsBackToTheStatus(t *testing.T) {
	// A provider that answers with nothing readable still has to produce a
	// reason, and the status line is the honest one.
	assert.Contains(t, reasonFrom(http.StatusInternalServerError, nil), "500")
	assert.NotEmpty(t, reasonFrom(http.StatusBadRequest, nil))
}
