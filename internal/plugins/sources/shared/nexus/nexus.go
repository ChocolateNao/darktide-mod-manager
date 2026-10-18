// Package nexus implements the Nexus Mods source against API v3
// (https://api.nexusmods.com/v3).
//
// # Authentication
//
// v3 accepts an `apikey` header or a bearer JWT and requires only one of them:
// there is no login, refresh or expiry, so a personal API key from the account
// page is the whole protocol. Only the trending feed, the DLC list and the Vortex
// extension list are public, so a key is needed to install and not to browse —
// which is what RequiresAPIKey and SupportsAuth report. The legacy v1 API
// addressed games by numeric id and minted a bearer token from a username, so
// only the key carries over.
//
// # There is no search endpoint
//
// The only discovery feed is GET /games/{game_domain}/trending-mods: public,
// unfiltered, unpaged, the top five by endorsements, entries carrying no id at
// all — only name, author, summary and page URL. Search applies the caller's
// query to those five locally, so an empty result means "nothing trending
// matched", not "no such mod". Nothing better is on offer to a third-party
// client. Expanding one entry costs several requests, since a feed entry names no
// files: one for the mod's own id, one for its files, one per file for versions.
//
// # Downloads
//
// A download is two steps: POST /mod-file-versions/{id}/download-repacked mints a
// short-lived download_url, which is then fetched. That URL points at a content
// host rather than the API and is fetched with no credentials: it is already
// signed, and sending the account key to a third-party host would hand it to
// anyone who could read the request.
//
// # There is no checksum
//
// v3 publishes no digest, size or ETag for any downloadable object, so there is
// nothing to verify an archive against and Verify accepts everything. The manager
// still records a SHA-256 of what it installs, so a later copy can be compared;
// what is missing is a platform value to check the first copy against.
//
// # Dependencies
//
// The dependency endpoints carry no "data" wrapper. Ranges within one definition
// are alternatives and separate definitions are separate requirements; both are
// flattened into the manager's single list, which over-reports alternatives — see
// dependencyMods. Reading them is best-effort: a list the manager cannot read is
// recorded in SourceData rather than failing an otherwise installable mod, which
// keeps "no dependencies" and "dependencies unknown" distinguishable.
//
// # Games
//
// v3 addresses games by their site domain, so every game Nexus serves works
// through the game_domain option. Every request goes through the httpx helpers,
// which is where status codes become typed errors, so this package only deals
// with payloads.
package nexus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/httpx"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/sourceutil"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// SourceID is the id this plugin registers under. It matches the key used in
// the configuration file.
const SourceID = "nexus"

// DefaultAPIBase is Nexus's v3 endpoint. Tests point this at a local server.
const DefaultAPIBase = "https://api.nexusmods.com/v3"

// DefaultGameDomain is this source's fallback for the sub-domain a game lives
// on; a game's own domain.SourceDomainKey entry wins when it supplies one. v3 does
// not currently serve this domain.
const DefaultGameDomain = "warhammer40kdarktide"

// DefaultTimeout bounds one HTTP exchange.
const DefaultTimeout = httpx.DefaultTimeout

const (
	// idPrefix marks a manager mod id as a Nexus one and idSeparator divides the
	// two numbers in it. A dash, not a dot: both halves are digits, so a dotted
	// pair would be indistinguishable from the ids the github source mints.
	idPrefix    = "nexus-"
	idSeparator = "-"

	optionAPIBase    = "api_base"
	optionGameDomain = domain.SourceDomainKey
	optionTimeout    = "timeout_seconds"

	// credentialProbeID is the id sent to check a credential, deliberately one that
	// does not exist: POST /mods/batch requires a key and an unknown id contributes
	// no row, so a working key gets an empty 200 and a rejected one a 401. Probing
	// a real mod would make "no such mod" and "bad key" indistinguishable.
	credentialProbeID = "0"

	// applicationName is the Application-Name header on the download request; the
	// API records it, which is what makes an abusive client identifiable.
	applicationName = "darktide-mod-manager"

	// SourceData keys for the one-time download authorization a keyed nxm link
	// carries. The CLI writes them and Download reads them back, so the spellings
	// are a contract between the two.
	sourceDataNXKey     = "nxm_key"
	sourceDataNXExpires = "nxm_expires"
	sourceDataNXUserID  = "nxm_user_id"

	// sourceDataFileScopedID is the legacy game-scoped file number of the resolved
	// version, which is how the v1 download endpoint is addressed — resolving the
	// mod already read the version carrying it, so keeping it here removes a
	// request from the one place a user waits on a timed one-time key.
	sourceDataFileScopedID = "file_scoped_id"
)

// Source is the Nexus Mods source. The zero value is not usable: use New. The
// client is a pointer because httpx clients are immutable copies shared by every
// call.
type Source struct {
	client     *httpx.Client
	apiBase    string
	gameDomain string
	apiKey     string
	mu         sync.Mutex
}

var (
	_ domain.ModSource           = (*Source)(nil)
	_ domain.CredentialForgetter = (*Source)(nil)
	_ services.Downloader        = (*Source)(nil)
)

// Option customises a Source.
type Option func(*Source)

// WithAPIBase points the source at a different Nexus deployment. A trailing
// slash is trimmed so paths concatenate cleanly.
func WithAPIBase(base string) Option {
	return func(s *Source) {
		if trimmed := strings.TrimRight(strings.TrimSpace(base), "/"); trimmed != "" {
			s.apiBase = trimmed
		}
	}
}

// WithGameDomain sets the game's site domain, the slug in mod page URLs. Anything
// but letters, digits, dashes and underscores is ignored: the value goes into a
// request path, where a slash would turn one request into a different one.
func WithGameDomain(domain string) Option {
	return func(s *Source) {
		if trimmed := strings.TrimSpace(domain); validGameDomain(trimmed) {
			s.gameDomain = trimmed
		}
	}
}

// WithTimeout replaces the HTTP timeout.
func WithTimeout(d time.Duration) Option {
	return func(s *Source) {
		if d > 0 && s.client != nil {
			s.client = s.client.WithTimeout(d)
		}
	}
}

// init registers the factory, discarding the error on purpose: a duplicate id
// means another package already claimed this name, not worth crashing over at
// program start.
func init() {
	_ = services.RegisterSourceFactory(
		SourceID,
		func(ctx services.SourceBuildContext) (domain.ModSource, error) {
			// Applied as an option, so the game default lands between the
			// source's own fallback and the config block New applies last.
			var opts []Option

			if gameDomain := domain.SourceDefault(
				ctx.Game,
				SourceID,
				optionGameDomain,
			); gameDomain != "" {
				opts = append(opts, WithGameDomain(gameDomain))
			}

			return New(ctx.Config, opts...)
		},
	)
}

// New builds a Nexus source from configuration.
//
// Options are applied first and the configuration block second, so a key in both
// resolves to the configuration. That is the opposite of what most libraries do,
// deliberately: the config file is the user's own statement about this source and
// it outranks whatever the wiring layer passed in. A v1 configuration is honoured
// where it can be — its API key is the personal key v3 wants — while game_id is
// ignored, since v3 addresses games by domain.
func New(cfg domain.SourceConfig, opts ...Option) (*Source, error) {
	s := &Source{
		client:     httpx.New(DefaultTimeout),
		apiBase:    DefaultAPIBase,
		gameDomain: DefaultGameDomain,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if raw := strings.TrimSpace(sourceutil.StringOf(cfg.Options[optionAPIBase])); raw != "" {
		s.apiBase = strings.TrimRight(raw, "/")
	}

	if raw := strings.TrimSpace(
		sourceutil.StringOf(cfg.Options[optionGameDomain]),
	); validGameDomain(
		raw,
	) {
		s.gameDomain = raw
	}

	if raw := strings.TrimSpace(sourceutil.StringOf(cfg.Options[optionTimeout])); raw != "" {
		if seconds, ok := positiveInt(raw); ok {
			s.client = s.client.WithTimeout(time.Duration(seconds) * time.Second)
		}
	}

	if s.client == nil {
		s.client = httpx.New(DefaultTimeout)
	}

	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		s.apiKey = key
	}

	if key := strings.TrimSpace(
		sourceutil.StringOf(cfg.Options["api_key"]),
	); key != "" &&
		s.apiKey == "" {
		s.apiKey = key
	}

	if !validGameDomain(s.gameDomain) {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%q is not a Nexus game domain; expected the slug from a mod page URL, "+
				"such as skyrimspecialedition", s.gameDomain)
	}

	if !validAPIBase(s.apiBase) {
		// The transport rejects a non-http base per request; reporting it here
		// tells a user who mistyped what they mistyped.
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%q is not a Nexus API base; expected an http or https URL such as %s",
			s.apiBase, DefaultAPIBase)
	}

	return s, nil
}

// validAPIBase reports whether a base is something the transport will accept.
// The transport rejects anything that is not http or https by refusing the
// request rather than by reporting a bad configuration, so the same rule is
// applied at construction to move the diagnosis to where the mistake was made.
func validAPIBase(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	default:
		return false
	}
}

// validGameDomain reports whether a value is safe to interpolate into a request
// path. Anything but letters, digits, dashes and underscores is refused rather
// than escaped: a domain with a slash or a space is a configuration mistake, and
// percent-encoding it would build a request nobody intended.
func validGameDomain(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}

	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_':
		default:
			return false
		}
	}

	return true
}

// ID implements domain.ModSource.
func (s *Source) ID() string { return SourceID }

// Name implements domain.ModSource.
func (s *Source) Name() string { return "Nexus Mods" }

// Description implements domain.ModSource.
func (s *Source) Description() string {
	return "Nexus Mods catalogue; only the trending feed is public, everything else needs an API key"
}

// Version implements domain.ModSource.
func (s *Source) Version() string { return pluginVersion }

// SupportsSearch reports that the trending feed needs no credentials.
func (s *Source) SupportsSearch() bool { return true }

// SupportsAuth reports that a user can supply a Nexus API key.
func (s *Source) SupportsAuth() bool { return true }

// RequiresAPIKey reports that a key is needed to install anything. Only the
// trending feed and the DLC list are public, so a keyless source can show five
// trending mods and install none; reporting otherwise would present a source that
// looks ready and cannot do its job.
func (s *Source) RequiresAPIKey() bool { return true }

// Authenticate stores an API key and checks that Nexus accepts it. v3 has no
// login step, so the key is the credential: there is nothing to exchange or cache.
// Checking it here rather than at first use tells a user who pasted the wrong key
// while they are still looking at the form. A blank credential is reported as
// ErrCodeAuthRequired without a request.
func (s *Source) Authenticate(ctx context.Context, creds domain.AuthCredentials) error {
	ctx = sourceutil.CtxOrBackground(ctx)

	key := strings.TrimSpace(
		sourceutil.CredValue(creds, "api_key", "apikey", "apiKey", "key", "token"),
	)
	if key == "" {
		return appErrors.New(appErrors.ErrCodeAuthRequired,
			"Nexus needs an API key; create one at nexusmods.com/settings/api-keys")
	}

	s.mu.Lock()
	s.apiKey = key
	s.mu.Unlock()

	return s.ValidateCredentials(ctx)
}

// ValidateCredentials checks the stored key against Nexus. No key is not an
// error: the trending feed is public, so a keyless user is browsing, not broken.
// A key that is present and rejected is a failure, and the credential is
// discarded so the next attempt starts from what the user typed.
func (s *Source) ValidateCredentials(ctx context.Context) error {
	ctx = sourceutil.CtxOrBackground(ctx)

	if s.apiKeyIfAny() == "" {
		return nil
	}

	if err := s.checkCredential(ctx); err != nil {
		// Only a refusal discards the key: a timeout or a 500 would make
		// somebody retype a key that was fine.
		if appErrors.IsAppError(err, appErrors.ErrCodeAuthFailed) {
			s.forgetCredential()
		}

		return err
	}

	return nil
}

// checkCredential asks Nexus to resolve an id that does not exist. The call is
// authenticated, so a rejected key fails before the id is considered.
func (s *Source) checkCredential(ctx context.Context) error {
	header, err := s.authHeader()
	if err != nil {
		return err
	}
	// httpx owns JSON encoding, so handing it bytes would double-encode the struct and
	// draw a 422. Neither answer's body adds to the status, so it is not decoded.
	if _, err := s.client.Do(ctx, httpx.Request{
		Method: http.MethodPost,
		URL:    s.apiBase + "/mods/batch",
		Body:   modsBatchRequest{ModIDs: []string{credentialProbeID}},
		Header: header,
		Kind:   httpx.KindAuth,
	}); err != nil {
		// The request carries a key, so a 401 means refused rather than missing —
		// the same translation classify applies to the reads.
		return classify(err)
	}

	return nil
}

// GetMod fetches one mod by its manager id. The version list decides whether the
// mod exists at all, so it is read first and an id with nothing installable is
// answered without asking about the mod.
func (s *Source) GetMod(ctx context.Context, modID string) (*domain.Mod, error) {
	ctx = sourceutil.CtxOrBackground(ctx)

	scopedID, fileID, ok := ParseModID(modID)
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%q is not a Nexus mod id; expected %s<mod id>-<file id>",
			modID, idPrefix)
	}

	if err := ctx.Err(); err != nil {
		return nil, canceledError(err)
	}
	// A pasted link numbers its file the legacy way, so the file half goes through
	// modFileForID; that is a no-op for an id the manager produced itself.
	file, named, err := s.modFileForID(ctx, scopedID, fileID)
	if err != nil {
		return nil, err
	}

	version, ok := named, named.ID != ""

	// A legacy link number names one version, so re-listing could install one
	// its key was never issued for; a manager-made id takes the newest instead.
	if !ok {
		versions, listErr := s.modFileVersions(ctx, file.ID)
		if listErr != nil {
			return nil, listErr
		}

		version, ok = pickVersion(versions)
		if !ok {
			return nil, appErrors.New(appErrors.ErrCodeModNotFound,
				"Nexus mod %s file %s has no downloadable version (categories seen: %s)",
				scopedID, fileID, describeCategories(versions))
		}
	}

	info, err := s.modContext(ctx, scopedID)
	if err != nil {
		return nil, err
	}

	mod := modFor(info, file, version)
	if mod == nil {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound,
			"Nexus mod %s file %s could not be identified", scopedID, fileID)
	}

	// Kept so a keyed download need not fetch the version a second time.
	if scoped := strings.TrimSpace(version.GameScopedID); scoped != "" {
		mod.SourceData[sourceDataFileScopedID] = scoped
	}

	s.attachDependencies(ctx, mod)

	return mod, nil
}

// Search returns the trending feed, filtered locally. A failing provider is
// reported rather than swallowed: unlike the registry's fan-out, a direct Search
// on one provider has nothing to fall back to, and an empty result would read as
// "this mod does not exist".
func (s *Source) Search(ctx context.Context, query domain.SearchQuery) ([]domain.Mod, error) {
	ctx = sourceutil.CtxOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return nil, canceledError(err)
	}

	q := query.Normalized()

	var feed trendingEnvelope
	if err := s.client.GetJSON(ctx,
		s.apiBase+"/games/"+s.gameDomain+"/trending-mods",
		&feed, s.unauthHeader(), httpx.KindSearch); err != nil {
		return nil, err
	}
	// Expanding an entry needs a credential, so the first failure is kept: an
	// empty list would read as "nothing trending matched", a statement about Nexus
	// rather than about the user.
	var firstErr error

	identified := 0

	mods := make([]domain.Mod, 0, len(feed.Data.Mods))
	for _, entry := range feed.Data.Mods {
		if err := ctx.Err(); err != nil {
			return nil, canceledError(err)
		}

		scopedID, ok := scopedIDFromPageURL(entry.ModPageURL)
		if !ok {
			// Skipped rather than counted as a failure: nothing was asked of the
			// API, and a guessed id could be a different mod entirely.
			continue
		}

		identified++

		expanded, err := s.trendingMod(ctx, scopedID, entry)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}

			continue
		}

		mods = append(mods, expanded...)
	}
	// One unreachable mod must not empty the feed, but an empty feed caused
	// entirely by one cause is that cause.
	if firstErr != nil && identified > 0 && len(mods) == 0 {
		return nil, firstErr
	}

	return sourceutil.SelectMods(mods, q), nil
}

// trendingMod expands one feed entry into the mods its files offer.
func (s *Source) trendingMod(
	ctx context.Context,
	scopedID string,
	entry trendingMod,
) ([]domain.Mod, error) {
	info, err := s.modContext(ctx, scopedID)
	if err != nil {
		return nil, err
	}
	// The feed's name wins: a query naming an entry as it appears there has
	// to match the name the user just searched for.
	info.name = firstText(entry.Name, info.name)
	info.author = strings.TrimSpace(entry.Author)
	info.summary = firstText(entry.Summary, info.summary)
	info.thumbnailURL = strings.TrimSpace(entry.PictureURL)

	files, err := s.modFiles(ctx, info.uid)
	if err != nil {
		return nil, err
	}

	var out []domain.Mod

	for _, file := range files {
		if !file.IsActive {
			continue
		}

		versions, err := s.modFileVersions(ctx, file.ID)
		if err != nil {
			// One unusable file among several is not a failure of the mod.
			continue
		}

		version, ok := pickVersion(versions)
		if !ok {
			continue
		}

		if mod := modFor(info, file, version); mod != nil {
			out = append(out, *mod)
		}
	}

	return out, nil
}

// modContext fetches the mod a game-scoped id names. A 404 is answered as
// "no such mod" rather than the transport's not-found, which a caller showing a
// mod page has no use for.
func (s *Source) modContext(ctx context.Context, scopedID string) (modContext, error) {
	var envelope modEnvelope
	if err := s.authedJSON(
		ctx,
		"/games/"+s.gameDomain+"/mods/"+scopedID,
		&envelope,
		httpx.KindMeta,
	); err != nil {
		return modContext{}, err
	}

	return modContext{
		// The requested id wins over the one in the response: a disagreement is a
		// redirect or a mistake, and neither may rename the mod.
		scopedID:   firstText(scopedID, envelope.Data.GameScopedID),
		uid:        strings.TrimSpace(envelope.Data.ID),
		gameID:     strings.TrimSpace(envelope.Data.GameID),
		gameDomain: s.gameDomain,
		name:       strings.TrimSpace(envelope.Data.Name),
	}, nil
}

// modFiles lists a mod's files, which are its update chains.
func (s *Source) modFiles(ctx context.Context, uid string) ([]fileAggregate, error) {
	if uid == "" {
		// No composite id means no route to the files, and a caller that cannot
		// enumerate them should not be offered the mod.
		return nil, nil
	}

	var envelope filesEnvelope
	if err := s.authedJSON(ctx, "/mods/"+uid+"/files", &envelope, httpx.KindMeta); err != nil {
		return nil, err
	}

	return envelope.Data.ModFiles, nil
}

// modFile fetches one mod file's aggregates.
func (s *Source) modFile(ctx context.Context, fileID string) (fileAggregate, error) {
	var envelope fileEnvelope
	if err := s.authedJSON(ctx, "/mod-files/"+fileID, &envelope, httpx.KindMeta); err != nil {
		return fileAggregate{}, err
	}

	file := envelope.Data
	if strings.TrimSpace(file.ID) == "" {
		// An empty 200 means the shape did not match, not that the file is
		// absent: the API answers a missing file with 404.
		return file, appErrors.New(appErrors.ErrCodeModNotFound,
			"Nexus answered for file %s with no file in the response", fileID)
	}

	return file, nil
}

// modFileVersions lists every version of a mod file.
func (s *Source) modFileVersions(ctx context.Context, fileID string) ([]fileVersion, error) {
	var envelope versionsEnvelope
	if err := s.authedJSON(
		ctx,
		"/mod-files/"+fileID+"/versions",
		&envelope,
		httpx.KindMeta,
	); err != nil {
		return nil, err
	}

	return envelope.Data.Versions, nil
}

// modFileVersion fetches one version of a mod file.
func (s *Source) modFileVersion(ctx context.Context, versionID string) (fileVersion, error) {
	var envelope versionEnvelope
	if err := s.authedJSON(
		ctx,
		"/mod-file-versions/"+versionID,
		&envelope,
		httpx.KindMeta,
	); err != nil {
		return fileVersion{}, err
	}

	return envelope.Data, nil
}

// modFileVersionByScopedID reads one version by the legacy file number a website
// link carries, and reports the v3 mod file id that version belongs to. A version
// read by id names no file and a file read by id names no version, so this is the
// only read that supplies both at once.
func (s *Source) modFileVersionByScopedID(
	ctx context.Context,
	scopedID, fileScopedID string,
) (fileVersion, string, error) {
	var envelope versionByScopedIDEnvelope

	path := "/games/" + s.gameDomain + "/mod-file-versions/" + fileScopedID
	if err := s.authedJSON(ctx, path, &envelope, httpx.KindMeta); err != nil {
		return fileVersion{}, "", err
	}

	return envelope.Data.fileVersion, strings.TrimSpace(envelope.Data.File.ID), nil
}

// modFileForID reads the mod file a mod id's file half refers to, accepting
// either numbering. A pasted link's number is a version's game-scoped id and the
// file endpoint wants the v3 file id, so one request against the version endpoint
// bridges them; an id that is already v3 is returned untouched and costs nothing.
//
// The second result is the version that number named, set only on that path: it
// is what lets GetMod install the version the site authorised instead of
// re-listing the file and picking another.
func (s *Source) modFileForID(
	ctx context.Context,
	scopedID, fileID string,
) (fileAggregate, fileVersion, error) {
	file, err := s.modFile(ctx, fileID)
	if err == nil {
		return file, fileVersion{}, nil
	}

	if !appErrors.IsAppError(err, appErrors.ErrCodeModNotFound) {
		return fileAggregate{}, fileVersion{}, err
	}

	named, v3FileID, err := s.modFileVersionByScopedID(ctx, scopedID, fileID)
	if err != nil {
		return fileAggregate{}, fileVersion{}, err
	}

	if v3FileID == "" {
		return fileAggregate{}, fileVersion{}, appErrors.New(appErrors.ErrCodeModNotFound,
			"Nexus mod %s file %s is not a file this manager can install", scopedID, fileID)
	}

	file, err = s.modFile(ctx, v3FileID)
	if err != nil {
		return fileAggregate{}, fileVersion{}, err
	}

	return file, named, nil
}

// attachDependencies records a version's requirements on the mod, recording a
// failure rather than returning it: the mod is installable either way, and only
// "requirements could not be read" is a reason to distrust the conflict report.
func (s *Source) attachDependencies(ctx context.Context, mod *domain.Mod) {
	versionID := strings.TrimSpace(sourceutil.StringOf(mod.SourceData["version_id"]))
	if versionID == "" {
		return
	}

	var resp dependencyResponse

	err := s.authedJSON(ctx, "/mod-file-versions/"+versionID+"/dependencies", &resp, httpx.KindMeta)
	if err != nil {
		mod.SourceData["dependency_error"] = appErrors.Message(err)
		return
	}

	mod.Dependencies = dependencyMods(resp, s.gameDomain)
	if dlcs := dlcNames(resp); len(dlcs) > 0 {
		mod.SourceData["dlc_dependencies"] = dlcs
	}
}

// Download fetches the archive behind a mod into destPath.
//
// The version comes from the mod's SourceData. A mod built by hand — one that
// never went through Search or GetMod — has no version id, so the newest
// installable one is used, while a pinned mod keeps the id it was given.
func (s *Source) Download(ctx context.Context, mod *domain.Mod, destPath string) (string, error) {
	ctx = sourceutil.CtxOrBackground(ctx)

	if mod == nil {
		return "", appErrors.New(appErrors.ErrCodeInvalidMod, "no mod to download")
	}

	if strings.TrimSpace(destPath) == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "no download destination")
	}

	_, fileID, ok := ParseModID(mod.ID)
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"mod %q does not carry a Nexus mod and file id", mod.ID)
	}

	versionID, err := s.versionIDFor(ctx, mod, fileID)
	if err != nil {
		return "", err
	}

	if err := sourceutil.EnsureDir(filepath.Dir(destPath)); err != nil {
		return "", err
	}

	url, err := s.downloadURL(ctx, versionID, mod)
	if err != nil {
		return "", err
	}
	// No header: the URL is signed and the key belongs to the account, so
	// sending it would put a live credential on whatever the URL points at.
	if _, err := s.client.Download(ctx, url, destPath, nil, ""); err != nil {
		return "", err
	}

	return destPath, nil
}

// versionIDFor resolves which version of a file to download.
func (s *Source) versionIDFor(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	if known := strings.TrimSpace(sourceutil.StringOf(mod.SourceData["version_id"])); known != "" {
		return known, nil
	}

	versions, err := s.modFileVersions(ctx, fileID)
	if err != nil {
		return "", err
	}

	version, ok := pickVersion(versions)
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeModNotFound,
			"Nexus file %s has no downloadable version (categories seen: %s)",
			fileID, describeCategories(versions))
	}

	return strings.TrimSpace(version.ID), nil
}

// downloadURL mints the short-lived URL for one version's archive. A mod
// carrying a keyed link's authorization goes through the legacy endpoint that
// accepts it, because the v3 endpoint is premium-only and answers a free account
// with 403.
func (s *Source) downloadURL(
	ctx context.Context,
	versionID string,
	mod *domain.Mod,
) (string, error) {
	if _, _, _, ok := nxmGrant(mod); ok {
		url, err := s.downloadURLForKeyed(ctx, mod, versionID)
		if err != nil {
			// The grant survives: a status rather than a URL means the key was
			// not spent, and burning it here would need a new one from the website.
			return "", err
		}
		// Spent the moment the endpoint mints, so not left in the record.
		forgetNXMGrant(mod)

		return url, nil
	}

	return s.downloadURLRepacked(ctx, versionID)
}

// downloadURLRepacked mints the short-lived URL for one version's repacked
// archive. This is a POST with no body: GetJSON would send a GET and draw a 404
// or 405 for an endpoint that exists, reading as "not downloadable".
func (s *Source) downloadURLRepacked(ctx context.Context, versionID string) (string, error) {
	header, err := s.authHeader()
	if err != nil {
		return "", err
	}
	// The API records this against the download.
	header.Set("Application-Name", applicationName+"/"+pluginVersion)

	resp, err := s.client.Do(ctx, httpx.Request{
		Method: http.MethodPost,
		URL:    s.apiBase + "/mod-file-versions/" + versionID + "/download-repacked",
		Header: header,
		Kind:   httpx.KindDownload,
	})
	if err != nil {
		return "", err
	}
	// No "data" wrapper here: the body is the schema.
	var payload downloadResponse
	if err := decodeJSON(resp.Body, &payload); err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeDownloadFailed,
			"Nexus download link for version %s was not JSON", versionID)
	}

	url := strings.TrimSpace(payload.DownloadURL)
	if url == "" {
		return "", appErrors.New(appErrors.ErrCodeDownloadFailed,
			"Nexus returned no download link for version %s", versionID)
	}

	return url, nil
}

// downloadURLForKeyed mints a content-host URL with the one-time authorization
// a keyed nxm link carried. It is a GET taking the three parameters the website
// put in the link, and it answers with mirrors rather than one URL; the first is
// used, since they are the same file and one has no basis for preferring a host.
func (s *Source) downloadURLForKeyed(
	ctx context.Context,
	mod *domain.Mod,
	versionID string,
) (string, error) {
	grantKey, expires, userID, ok := nxmGrant(mod)
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeDownloadFailed,
			"mod %q carries no usable Nexus download authorization", sourceutil.ModLabel(mod))
	}

	scopedID, linkFileID, ok := ParseModID(mod.ID)
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"mod %q does not carry a Nexus mod and file id", mod.ID)
	}

	query := url.Values{}
	query.Set("key", grantKey)
	query.Set("expires", expires)
	query.Set("user_id", userID)

	// The legacy endpoint's expected number is not consistent across the API's
	// two eras, so the pasted number is asked for first and the resolved one only
	// after a refusal. Only a refusal advances the loop, since a rejected key or a
	// 500 answers the same for every number — and a one-time grant could be spent.
	tried := make([]string, 0, 2)
	authorizedEmpty := false

	candidates := []string{strings.TrimSpace(linkFileID)}

	for attempt := 0; attempt < len(candidates); attempt++ {
		fileID := candidates[attempt]
		if fileID == "" || slices.Contains(tried, fileID) {
			continue
		}

		tried = append(tried, fileID)

		uri, authorized, err := s.mintKeyedURL(ctx, scopedID, fileID, query, mod)
		if err == nil {
			return uri, nil
		}

		if !appErrors.IsAppError(err, appErrors.ErrCodeModNotFound) {
			return "", err
		}

		authorizedEmpty = authorizedEmpty || authorized

		if attempt == 0 {
			// The extra read is only worth its request once the pasted number has
			// been refused.
			if legacy := s.legacyFileIDFor(ctx, mod, versionID); legacy != "" {
				candidates = append(candidates, legacy)
			}
		}
	}

	if authorizedEmpty {
		return "", appErrors.New(appErrors.ErrCodeDownloadFailed,
			"Nexus authorized the download of mod %s under file id %s but offered no mirror",
			sourceutil.ModLabel(mod), quoteAll(tried))
	}

	return "", appErrors.New(appErrors.ErrCodeModNotFound,
		"Nexus authorized no download for %s under file id %s",
		sourceutil.ModLabel(mod), quoteAll(tried))
}

// mintKeyedURL spends a grant against one file id and returns the first mirror
// the API offers. The second result reports whether the server authorized the
// request at all, which is what tells a refused number from an unserved download.
func (s *Source) mintKeyedURL(
	ctx context.Context,
	scopedID, fileID string,
	query url.Values,
	mod *domain.Mod,
) (uri string, authorized bool, err error) {
	var mirrors downloadLinkResponse

	path := "/games/" + s.gameDomain + "/mods/" + scopedID +
		"/files/" + fileID + "/download_link"

	if err := s.authedV1JSON(ctx, path, query, &mirrors, httpx.KindDownload); err != nil {
		return "", false, err
	}

	for _, mirror := range mirrors {
		if uri := strings.TrimSpace(mirror.URI); uri != "" {
			return uri, true, nil
		}
	}

	return "", true, appErrors.New(appErrors.ErrCodeModNotFound,
		"Nexus authorized the download of mod %s under file id %s but offered no mirror",
		sourceutil.ModLabel(mod), fileID)
}

// legacyFileIDFor recovers the game-scoped id of the version a keyed download
// has to address. It is the second guess, not the first: a pasted link's own
// number is what the website used, and the two disagree in the wild.
func (s *Source) legacyFileIDFor(ctx context.Context, mod *domain.Mod, versionID string) string {
	if scoped := strings.TrimSpace(
		sourceutil.StringOf(mod.SourceData[sourceDataFileScopedID]),
	); scoped != "" {
		return scoped
	}

	version, err := s.modFileVersion(ctx, versionID)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(version.GameScopedID)
}

// quoteAll renders a list of ids for an error message, so the next failure says
// which numbers were asked for without a source dive.
func quoteAll(ids []string) string {
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			quoted = append(quoted, strconv.Quote(id))
		}
	}

	if len(quoted) == 0 {
		return "(none)"
	}

	return strings.Join(quoted, " or ")
}

// nxmGrant reads the one-time download authorization a keyed nxm link left in a
// mod's SourceData. A free account cannot mint a download URL, so the website
// hands a registered nxm:// handler a key, expires and user_id for the legacy
// download_link endpoint to exchange. All three are required, and a link
// carrying only some is reported as no key rather than as a grant that would
// blame the account with a 403.
func nxmGrant(mod *domain.Mod) (key, expires, userID string, ok bool) {
	if mod == nil || mod.SourceData == nil {
		return "", "", "", false
	}

	key = strings.TrimSpace(sourceutil.StringOf(mod.SourceData[sourceDataNXKey]))
	expires = strings.TrimSpace(sourceutil.StringOf(mod.SourceData[sourceDataNXExpires]))

	userID = strings.TrimSpace(sourceutil.StringOf(mod.SourceData[sourceDataNXUserID]))
	if key == "" || expires == "" || userID == "" {
		return "", "", "", false
	}

	return key, expires, userID, true
}

// forgetNXMGrant drops a spent authorization: the index is the one place a mod's
// data outlives the download it was for.
func forgetNXMGrant(mod *domain.Mod) {
	if mod == nil || mod.SourceData == nil {
		return
	}

	delete(mod.SourceData, sourceDataNXKey)
	delete(mod.SourceData, sourceDataNXExpires)
	delete(mod.SourceData, sourceDataNXUserID)
}

// Verify accepts every downloaded file. v3 publishes no digest, size or checksum,
// so there is no expected value to compare against and any check here would be a
// no-op or a false rejection. The manager records a SHA-256 of what it installs,
// so a later copy can be compared against that.
func (s *Source) Verify(ctx context.Context, mod *domain.Mod, path string) error {
	return nil
}

// unauthHeader returns the headers for a request that needs no credentials.
func (s *Source) unauthHeader() http.Header {
	header := http.Header{}
	header.Set("Accept", "application/json")

	return header
}

// authHeader returns the headers for an authenticated request. A missing key is
// refused locally rather than sent, so the caller is told what is missing.
func (s *Source) authHeader() (http.Header, error) {
	key := s.apiKeyIfAny()
	if key == "" {
		// Names the source and the fact only: the frontend turns the code into a
		// dialog, so naming a CLI command it cannot run reads as a broken dialog.
		return nil, appErrors.New(appErrors.ErrCodeAuthRequired,
			"Nexus Mods requires an API key for this operation")
	}

	header := s.unauthHeader()
	// Lower case and without a prefix: "x-api-key" is simply unauthenticated.
	header.Set("apikey", key)

	return header, nil
}

// authedJSON performs one authenticated GET against the v3 base and decodes the
// body into out. Every authenticated v3 read goes through here, so the credential
// policy lives in one place. It is a helper rather than an inline header argument
// because Go will not spread a two-value call into a call with other arguments.
func (s *Source) authedJSON(ctx context.Context, path string, out any, kind httpx.Kind) error {
	return s.authedJSONAt(ctx, s.apiBase, path, nil, out, kind)
}

// authedV1JSON is authedJSON against the legacy base, for the one read that has
// no v3 equivalent: the keyed download_link.
func (s *Source) authedV1JSON(
	ctx context.Context,
	path string,
	query url.Values,
	out any,
	kind httpx.Kind,
) error {
	return s.authedJSONAt(ctx, s.v1Base(), path, query, out, kind)
}

func (s *Source) authedJSONAt(
	ctx context.Context,
	base, path string,
	query url.Values,
	out any,
	kind httpx.Kind,
) error {
	header, err := s.authHeader()
	if err != nil {
		return err
	}

	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	err = s.client.GetJSON(ctx, target, out, header, kind)
	if err == nil {
		return nil
	}
	// Holding on would make every later call fail with the same message while
	// the user has already been told to paste a new one.
	if appErrors.IsAppError(err, appErrors.ErrCodeAuthRequired) {
		s.forgetCredential()
	}

	return classify(err)
}

// v1Base returns the legacy API base, which serves the one endpoint v3 has no
// equivalent for: the keyed download_link a free account needs. The bases share a
// host and differ only in the version segment; a base whose last segment is not a
// version is returned unchanged, because a wrong host is worse than no fallback.
func (s *Source) v1Base() string {
	trimmed := strings.TrimRight(s.apiBase, "/")

	cut := strings.LastIndex(trimmed, "/")
	if cut < 0 {
		return trimmed
	}

	if !isVersionSegment(trimmed[cut+1:]) {
		return trimmed
	}

	return trimmed[:cut] + "/v1"
}

// isVersionSegment reports whether a URL's last path segment names an API
// version, which is the shape every Nexus base has: "v3", "v2", "v1".
func isVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}

	return isDigits(segment[1:])
}

// classify turns the transport's status mapping into one the manager can present.
//
// A 404 on any of these reads means the mod or file is not there, which the
// registry and the CLI both know how to present. A 401 means the key was refused
// rather than missing, and the message says so: the chain below still reads
// AUTH_REQUIRED, and without the sentence the two contradict each other.
func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case appErrors.IsAppError(err, appErrors.ErrCodeNotFound):
		return appErrors.Wrap(err, appErrors.ErrCodeModNotFound,
			"Nexus has no such mod or mod file")
	case appErrors.IsAppError(err, appErrors.ErrCodeAuthRequired):
		return appErrors.Wrap(err, appErrors.ErrCodeAuthFailed,
			"Nexus rejected the API key (the service answers a refused key with the "+
				"same 401 it uses for a missing one, so the status alone cannot tell them apart)")
	default:
		return err
	}
}

// apiKeyIfAny returns the stored key.
func (s *Source) apiKeyIfAny() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.apiKey
}

// forgetCredential discards a key Nexus refused, so the next attempt starts from
// what the user typed rather than from what was rejected.
func (s *Source) forgetCredential() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.apiKey = ""
}

// ForgetCredentials drops the key the manager is holding, without contacting
// Nexus. The settings file has already been changed by the time this is called.
func (s *Source) ForgetCredentials() { s.forgetCredential() }

// HasCredentials reports whether an API key is configured, without contacting
// Nexus.
func (s *Source) HasCredentials() bool { return s.apiKeyIfAny() != "" }

// Authenticated reports whether a credential is stored. v3 has no token to
// expire, so this means "a key is configured", not "a key proven good"; use
// ValidateCredentials for that.
func (s *Source) Authenticated() bool { return s.HasCredentials() }

// pluginVersion is reported by Version. A constant rather than a build stamp, so
// a build of the manager and a build of the plugin always agree.
const pluginVersion = "1.0.0"

// decodeJSON unmarshals a response body. The endpoints that answer without a
// "data" envelope are decoded through here, and a body that is not the expected
// JSON means the API answered something else — a proxy error page, an object
// that changed shape — which is more useful as a decode failure than as empty.
func decodeJSON(body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigInvalid, "decode Nexus response")
	}

	return nil
}

// canceledError reports a context that ended before the work did.
func canceledError(err error) error {
	return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled,
		"Nexus request canceled")
}

// positiveInt parses a decimal integer, reporting whether it was well formed.
// Anything with a sign, padding or a fractional part is refused, because no
// number this package publishes or reads has one.
func positiveInt(text string) (int, bool) {
	trimmed := strings.TrimSpace(text)
	if !isDigits(trimmed) {
		return 0, false
	}

	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}

	return value, true
}
