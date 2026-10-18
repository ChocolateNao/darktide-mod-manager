// Package github indexes GitHub releases as a Darktide mod source.
//
// Darktide mods reach users through GitHub far more often than through any
// central registry, so this source turns those releases into domain.Mod records
// the rest of the manager cannot tell apart from anything served by Nexus or
// Vortex.
//
// Configured repositories are the index; with none configured, discovery falls
// back to a repository search for the mod topic, capped so a broad query cannot
// turn into hundreds of API calls. Transport goes through httpx, which owns
// status interpretation, so this file only maps GitHub's JSON onto the domain.
package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/httpx"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

const (
	// SourceID is the registry key for this source.
	SourceID = "github"

	// DefaultAPIBase is the public GitHub REST API root.
	DefaultAPIBase = "https://api.github.com"

	// DefaultTimeout bounds a single HTTP exchange.
	DefaultTimeout = httpx.DefaultTimeout

	pluginVersion = "1.0.0"

	// defaultMaxDiscover caps how many discovered repositories are read. GitHub
	// allows 60 anonymous requests an hour, so an unbounded sweep would exhaust
	// the budget and then report a rate-limit error for the rest of the session.
	defaultMaxDiscover = 25

	// defaultReleaseLimit caps how many releases of one repository are indexed.
	defaultReleaseLimit = 20

	// maxPageSize is the largest releases-per-page GitHub honours.
	maxPageSize = 100

	// SearchTopic is this source's own fallback for repository discovery. It only
	// applies when the active game supplies no "topic" default and the
	// configuration block does not set one either.
	SearchTopic = "darktide-mod"
)

// Source implements domain.ModSource over GitHub releases.
type Source struct {
	client  *httpx.Client
	apiBase string
	token   string
	topic   string
	repos   []string
	maxDisc int
	relLmit int
	mu      sync.Mutex
}

var (
	_ domain.ModSource           = (*Source)(nil)
	_ domain.CredentialForgetter = (*Source)(nil)
	_ services.Downloader        = (*Source)(nil)
)

// Option customises a Source. Options let tests point the API base at an httptest
// server and a user pin a repository list rather than rely on discovery.
type Option func(*Source)

// WithAPIBase overrides the API root. A blank value is ignored, because an empty
// base would make every request fail in a way that looks like a network fault.
func WithAPIBase(base string) Option {
	return func(s *Source) {
		if trimmed := strings.TrimSpace(base); trimmed != "" {
			s.apiBase = strings.TrimRight(trimmed, "/")
		}
	}
}

// WithToken sets the personal access token used for API calls. GitHub works
// anonymously, so this is optional; supplying one raises the rate limit and
// unlocks private repositories.
func WithToken(token string) Option {
	return func(s *Source) { s.token = strings.TrimSpace(token) }
}

// WithRepositories sets the repositories this source indexes. Entries must look
// like "owner/repo"; malformed entries are dropped rather than carried into a
// URL that 404s for a reason the user cannot act on.
func WithRepositories(repos ...string) Option {
	return func(s *Source) {
		for _, r := range repos {
			if validRepoRef(r) {
				s.repos = append(s.repos, strings.TrimSpace(r))
			}
		}
	}
}

// WithDiscoveryLimit caps how many discovered repositories are followed.
func WithDiscoveryLimit(n int) Option {
	return func(s *Source) {
		if n > 0 {
			s.maxDisc = n
		}
	}
}

// WithReleaseLimit caps how many releases of one repository are indexed.
func WithReleaseLimit(n int) Option {
	return func(s *Source) {
		if n > 0 {
			s.relLmit = n
		}
	}
}

// WithTimeout overrides the per-request HTTP timeout. A non-positive value is
// ignored, so a zero in the config file means "unset" rather than "no timeout".
func WithTimeout(d time.Duration) Option {
	return func(s *Source) {
		if d > 0 {
			s.client = s.client.WithTimeout(d)
		}
	}
}

// WithTopic overrides the repository-discovery topic.
func WithTopic(topic string) Option {
	return func(s *Source) {
		if trimmed := strings.TrimSpace(topic); trimmed != "" {
			s.topic = trimmed
		}
	}
}

func init() {
	// Registration is best-effort: a duplicate id means something else already
	// registered this source, which is not a failure worth crashing over.
	_ = services.RegisterSourceFactory(
		SourceID,
		func(ctx services.SourceBuildContext) (domain.ModSource, error) {
			// The game default sits between the source's own fallback (New's struct
			// literal) and the user's block, which New applies last: config > game.
			var opts []Option

			if topic := domain.SourceDefault(ctx.Game, SourceID, "topic"); topic != "" {
				opts = append(opts, WithTopic(topic))
			}

			return New(ctx.Config, opts...)
		},
	)
}

// New builds a Source from the user's config block.
func New(cfg domain.SourceConfig, opts ...Option) (*Source, error) {
	s := &Source{
		client:  httpx.New(DefaultTimeout),
		apiBase: DefaultAPIBase,
		maxDisc: defaultMaxDiscover,
		relLmit: defaultReleaseLimit,
		topic:   SearchTopic,
	}
	for _, opt := range opts {
		opt(s)
	}

	if s.client == nil {
		s.client = httpx.New(DefaultTimeout)
	}

	if strings.TrimSpace(cfg.APIKey) != "" {
		s.token = strings.TrimSpace(cfg.APIKey)
	}

	if base, ok := cfg.Options["api_base"]; ok {
		if trimmed := strings.TrimSpace(fmt.Sprint(base)); trimmed != "" {
			s.apiBase = strings.TrimRight(trimmed, "/")
		}
	}

	if raw, ok := cfg.Options["repositories"]; ok {
		for _, r := range optionList(raw) {
			if validRepoRef(r) {
				s.repos = append(s.repos, strings.TrimSpace(r))
			}
		}
	}

	if raw, ok := cfg.Options["timeout_seconds"]; ok {
		if secs, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(raw))); err == nil && secs > 0 {
			s.client = s.client.WithTimeout(time.Duration(secs) * time.Second)
		}
	}

	if raw, ok := cfg.Options["topic"]; ok {
		if trimmed := strings.TrimSpace(fmt.Sprint(raw)); trimmed != "" {
			s.topic = trimmed
		}
	}

	if raw, ok := cfg.Options["max_discover"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(raw))); err == nil && n > 0 {
			s.maxDisc = n
		}
	}

	if raw, ok := cfg.Options["max_releases"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(raw))); err == nil && n > 0 {
			s.relLmit = n
		}
	}

	return s, nil
}

// ID implements domain.ModSource.
func (s *Source) ID() string { return SourceID }

// Name implements domain.ModSource.
func (s *Source) Name() string { return "GitHub Releases" }

// Description implements domain.ModSource.
func (s *Source) Description() string {
	return "Installs Darktide mods published as GitHub releases."
}

// Version implements domain.ModSource.
func (s *Source) Version() string { return pluginVersion }

// SupportsSearch implements domain.ModSource.
func (s *Source) SupportsSearch() bool { return true }

// SupportsAuth implements domain.ModSource. A token is optional, but a user who
// wants one must be able to supply it.
func (s *Source) SupportsAuth() bool { return true }

// RequiresAPIKey implements domain.ModSource. GitHub is usable anonymously, so
// a key is never required; supplying one only raises the rate limit.
func (s *Source) RequiresAPIKey() bool { return false }

// Search implements domain.ModSource.
func (s *Source) Search(ctx context.Context, q domain.SearchQuery) ([]domain.Mod, error) {
	ctx = ctxOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeOperationCanceled,
			"github search canceled",
		)
	}

	q = q.Normalized()

	if len(s.repos) > 0 {
		return s.searchConfigured(ctx, q)
	}

	return s.searchDiscovered(ctx, q)
}

// searchConfigured walks the configured repository list. A repository that
// fails is recorded and skipped: one broken repository must not hide the rest.
func (s *Source) searchConfigured(ctx context.Context, q domain.SearchQuery) ([]domain.Mod, error) {
	var (
		all      []domain.Mod
		failures []error
	)

	for _, ref := range s.repos {
		if err := ctx.Err(); err != nil {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeOperationCanceled,
				"github search canceled",
			)
		}

		releases, err := s.releases(ctx, ref, s.relLmit)
		if err != nil {
			failures = append(failures, err)
			continue
		}

		all = append(all, modsFor(ref, releases)...)
	}

	if len(all) == 0 && len(failures) > 0 {
		return nil, appErrors.New(appErrors.ErrCodeSearchFailed,
			"no GitHub releases could be read: %s", summarize(failures))
	}

	return selectMods(all, q), nil
}

// searchDiscovered asks GitHub for repositories carrying the mod topic and then
// reads each one's releases.
func (s *Source) searchDiscovered(ctx context.Context, q domain.SearchQuery) ([]domain.Mod, error) {
	refs, err := s.discover(ctx, q.Query)
	if err != nil {
		return nil, err
	}

	if len(refs) == 0 {
		return []domain.Mod{}, nil
	}

	var (
		all      []domain.Mod
		failures []error
	)

	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeOperationCanceled,
				"github search canceled",
			)
		}

		releases, err := s.releases(ctx, ref, s.relLmit)
		if err != nil {
			failures = append(failures, err)
			continue
		}

		all = append(all, modsFor(ref, releases)...)
	}

	if len(all) == 0 && len(failures) == len(refs) && len(refs) > 0 {
		return nil, appErrors.New(appErrors.ErrCodeSearchFailed,
			"every discovered GitHub repository failed: %s", summarize(failures))
	}

	return selectMods(all, q), nil
}

// discover searches for repositories carrying the mod topic.
func (s *Source) discover(ctx context.Context, query string) ([]string, error) {
	params := url.Values{}
	params.Add("q", searchQuery(query, s.topic))
	params.Add("per_page", strconv.Itoa(maxPageSize))

	var payload struct {
		Items []struct {
			FullName string `json:"full_name"`
		} `json:"items"`
	}

	target := s.apiBase + "/search/repositories?" + params.Encode()
	if err := s.client.GetJSON(ctx, target, &payload, s.header(), httpx.KindSearch); err != nil {
		return nil, err
	}

	refs := make([]string, 0, len(payload.Items))
	for _, it := range payload.Items {
		if !validRepoRef(it.FullName) {
			continue
		}

		refs = append(refs, strings.TrimSpace(it.FullName))
		if len(refs) >= s.maxDisc {
			break
		}
	}

	return refs, nil
}

// releases reads up to limit releases of owner/repo.
func (s *Source) releases(ctx context.Context, ref string, limit int) ([]release, error) {
	if !validRepoRef(ref) {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"malformed repository reference %q",
			ref,
		)
	}

	if limit <= 0 {
		limit = s.relLmit
	}

	perPage := limit
	if perPage > maxPageSize {
		perPage = maxPageSize
	}

	target := s.apiBase + "/repos/" + ref + "/releases?per_page=" + strconv.Itoa(perPage)

	var out []release
	if err := s.client.GetJSON(ctx, target, &out, s.header(), httpx.KindMeta); err != nil {
		return nil, err
	}

	kept := make([]release, 0, len(out))
	for _, r := range out {
		if len(kept) >= limit {
			break
		}
		// A draft release is not published yet; indexing it would offer a mod
		// whose download 404s.
		if r.Draft {
			continue
		}

		kept = append(kept, r)
	}

	return kept, nil
}

// GetMod implements domain.ModSource. It returns the most recent usable
// release; the caller that wants an older version uses Search, which lists them
// all.
func (s *Source) GetMod(ctx context.Context, modID string) (*domain.Mod, error) {
	ref, err := repoFromModID(modID)
	if err != nil {
		return nil, err
	}

	releases, err := s.releases(ctxOrBackground(ctx), ref, 1)
	if err != nil {
		// A repository GitHub does not know about and one with nothing published are the
		// same answer here; NOT_FOUND would leak a transport code past a registry that
		// only knows MOD_NOT_FOUND.
		if appErrors.IsAppError(err, appErrors.ErrCodeNotFound) {
			return nil, appErrors.New(appErrors.ErrCodeModNotFound,
				"GitHub repository %q does not exist or has no published releases", ref)
		}

		return nil, err
	}

	if len(releases) == 0 {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound,
			"GitHub repository %q has no published releases", ref)
	}

	m, ok := modFor(ref, releases[0])
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound,
			"GitHub repository %q has no release with a usable version", ref)
	}

	return &m, nil
}

// Download implements services.Downloader and domain.ModSource. It resolves the
// current release, picks its payload asset and streams it into destPath,
// verifying the published digest when GitHub offers one.
func (s *Source) Download(ctx context.Context, mod *domain.Mod, destPath string) (string, error) {
	ctx = ctxOrBackground(ctx)

	if mod == nil {
		return "", appErrors.New(appErrors.ErrCodeInvalidMod, "nil mod")
	}

	destPath = strings.TrimSpace(destPath)
	if destPath == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "empty download destination")
	}

	ref, err := repoFromModID(mod.ID)
	if err != nil {
		return "", err
	}

	releases, err := s.releases(ctx, ref, 1)
	if err != nil {
		return "", err
	}

	if len(releases) == 0 {
		return "", appErrors.New(appErrors.ErrCodeDownloadFailed,
			"GitHub repository %q has no published releases", ref)
	}

	asset, ok := payloadAsset(releases[0].Assets)
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeDownloadFailed,
			"release %s of %s has no installable archive asset", releases[0].TagName, ref)
	}

	if err := ensureDir(filepath.Dir(destPath)); err != nil {
		return "", err
	}

	expect := ""

	if mod.SourceData != nil {
		if v, ok := mod.SourceData["sha256"].(string); ok {
			expect = v
		}
	}

	if _, err := s.client.Download(
		ctx,
		asset.BrowserDownloadURL,
		destPath,
		s.header(),
		expect,
	); err != nil {
		return "", err
	}

	return destPath, nil
}

// Verify implements services.Downloader. GitHub publishes a digest only for
// recent uploads, so an absent digest is not a failure; a present one that does
// not match is.
func (s *Source) Verify(ctx context.Context, mod *domain.Mod, path string) error {
	if mod == nil || mod.SourceData == nil {
		return nil
	}

	expect := strings.TrimSpace(stringOf(mod.SourceData["sha256"]))

	expect = strings.TrimPrefix(expect, "sha256:")
	if expect == "" {
		return nil
	}

	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}

	if !strings.EqualFold(sum, expect) {
		return appErrors.New(appErrors.ErrCodeIntegrity,
			"download of %s does not match the published digest", mod.ID)
	}

	return nil
}

// Authenticate implements domain.ModSource. A GitHub token lives in the config,
// so there is nothing to exchange; validating it is the useful part.
func (s *Source) Authenticate(ctx context.Context, creds domain.AuthCredentials) error {
	token := credValue(creds, "token", "api_key", "apiKey")
	if token == "" {
		return appErrors.New(appErrors.ErrCodeAuthRequired,
			"no GitHub token supplied; anonymous access remains available")
	}

	s.mu.Lock()
	s.token = token
	s.mu.Unlock()

	return s.ValidateCredentials(ctx)
}

// ForgetCredentials drops the token the manager is holding, so a key deleted from
// the settings file stops being sent by this process.
func (s *Source) ForgetCredentials() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.token = ""
}

// tokenIfAny returns the held token, or "" when none is configured.
func (s *Source) tokenIfAny() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.token
}

// ValidateCredentials implements domain.ModSource. Anonymous access is valid, so
// a missing token is not an error; a supplied one is checked against /user.
func (s *Source) ValidateCredentials(ctx context.Context) error {
	if s.tokenIfAny() == "" {
		return nil
	}

	var u struct {
		Login string `json:"login"`
	}
	if err := s.client.GetJSON(
		ctxOrBackground(ctx),
		s.apiBase+"/user",
		&u,
		s.header(),
		httpx.KindAuth,
	); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeAuthFailed, "GitHub token was rejected")
	}

	if strings.TrimSpace(u.Login) == "" {
		return appErrors.New(
			appErrors.ErrCodeAuthFailed,
			"GitHub returned no account for the token",
		)
	}

	return nil
}

// header builds the request headers, including Authorization when a token is
// configured. GitHub accepts both "Bearer <token>" and "token <token>"; Bearer
// is what the current API documents.
func (s *Source) header() http.Header {
	h := http.Header{}
	if token := s.tokenIfAny(); token != "" {
		h.Set("Authorization", "Bearer "+token)
	}

	return h
}

// Repositories returns a copy of the configured repository list.
func (s *Source) Repositories() []string {
	out := make([]string, len(s.repos))
	copy(out, s.repos)

	return out
}
