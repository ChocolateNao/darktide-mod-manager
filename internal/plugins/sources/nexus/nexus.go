// Package nexus implements the Nexus Mods source.
//
// # API version
//
// This speaks Nexus API v3 (https://api.nexusmods.com/v3), the version the
// published OpenAPI specification describes. The v1 API is still reachable and
// is what earlier revisions of this plugin used, but the specification calls it
// legacy and the two are not interchangeable: v1 addresses games by a numeric id
// and mints a bearer token from a username, v3 addresses games by their URL
// domain and takes an API key on every request. Only the configured game_domain
// and API key carry over.
//
// # Authentication
//
// v3 has exactly two security schemes, an `apikey` header and a bearer JWT, and
// the requirement is an OR of the two. There is no login, no token endpoint, no
// refresh and no expiry — a personal API key from the account settings page is
// sent as a header and that is the whole protocol. A username is therefore not
// part of anything, and none is asked for.
//
// Only three endpoints are public: the trending feed, the game's DLC list and
// the Vortex extension list. Everything needed to install a mod — its details,
// its files, their versions, its dependencies, the download link — requires the
// key. So the key is required to install, and not required to see the trending
// feed, which is what RequiresAPIKey and SupportsAuth respectively report.
//
// # There is no search endpoint
//
// The specification contains no search operation: the string "search" does not
// appear anywhere in it. The only discovery feed is GET
// /games/{game_domain}/trending-mods, which is public and returns the top five
// trending mods ranked by endorsements, unfiltered and unpaged, and whose
// entries carry no id at all — only a name, an author, a summary and the
// canonical mod page URL, from which the game-scoped id is recovered.
//
// Search therefore returns the trending feed with the caller's query applied to
// it locally. A query that matches nothing in those five mods returns an empty
// result, and that empty result means "nothing trending matched", not "no such
// mod exists on Nexus". There is no way to do better against this API; browsing
// the whole catalogue is not something it offers to a third-party client.
//
// Turning a trending entry into something installable costs several requests,
// because the feed's entries carry no file list: one to learn the mod's own id
// and name, one for its files, and one for each file's versions.
//
// # Downloads
//
// A download is two steps. POST /mod-file-versions/{id}/download-repacked
// returns a short-lived download_url and its expiry, and that URL is then
// fetched. It points at a content host rather than at the API, so it is
// requested with no credentials: the URL is already signed, and sending the
// account's API key to a third-party host would hand the credential to anyone
// who could read the request.
//
// # There is no checksum
//
// v3 publishes no digest, size or ETag for any downloadable object. There is
// nothing to verify a downloaded archive against, so Verify accepts everything
// and says why. The manager still records a SHA-256 of the archive it installs
// (see the mod service), so a local copy can be compared later; what is missing
// is a value from the platform to compare the first copy against.
//
// # Dependencies
//
// The dependency endpoints carry no "data" wrapper, unlike almost every other
// response. Ranges within one definition are alternatives and separate
// definitions are separate requirements, which is flattened here into the
// manager's single dependency list; the flattening over-reports alternatives as
// separate requirements and the reason for it is documented on dependencyMods.
//
// Reading a version's dependencies costs one request and its failure must not
// fail the mod: a dependency list the manager cannot read is not a reason to
// refuse an otherwise installable mod, so the error is recorded in the mod's
// SourceData instead of being swallowed, which keeps "no dependencies" and
// "dependencies unknown" distinguishable.
//
// # Darktide
//
// v3 addresses games by their site domain. Every other game
// Nexus does serve works through the game_domain option.
//
// Every request goes through the httpx helpers, which is where status codes
// become typed errors, so this package only deals with payloads.
package nexus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/httpx"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/sourceutil"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// SourceID is the id this plugin registers under. It matches the key used in
// the configuration file.
const SourceID = "nexus"

// DefaultAPIBase is Nexus's v3 endpoint. Tests point this at a local server.
const DefaultAPIBase = "https://api.nexusmods.com/v3"

// DefaultGameDomain is the sub-domain a game lives on. Nexus is one service
// hosting many games and nearly every endpoint is game-scoped. See the package
// comment: v3 does not currently serve this domain.
const DefaultGameDomain = "warhammer40kdarktide"

// DefaultTimeout bounds one HTTP exchange.
const DefaultTimeout = httpx.DefaultTimeout

const (
	// idPrefix marks a manager mod id as a Nexus one, and idSeparator divides the
	// two numbers inside it. A dash is used rather than a dot because both halves
	// are digits and a dot-separated pair would be indistinguishable from the
	// dotted ids the github source mints.
	idPrefix    = "nexus-"
	idSeparator = "-"

	optionAPIBase    = "api_base"
	optionGameDomain = "game_domain"
	optionTimeout    = "timeout_seconds"

	// credentialProbeID is the id sent to check a credential. It is deliberately
	// one that does not exist: POST /mods/batch requires a key, and an unknown id
	// contributes no row, so a working key is answered with an empty 200 and a
	// rejected one with a 401. Probing a real mod instead would make "no such mod"
	// and "bad key" indistinguishable.
	credentialProbeID = "0"

	// applicationName is sent as the Application-Name header on the download
	// request, which the API records against the download. Saying who is asking
	// is the courteous thing to do with someone's infrastructure, and it is what
	// makes an abusive client identifiable to the platform's operators.
	applicationName = "darktide-mod-manager"

	// SourceData keys for the one-time download authorization a keyed nxm link
	// carries. The CLI writes them when it parses a link and this package reads
	// them back in Download; the two spellings are a contract, so each side
	// names the other in a comment.
	sourceDataNXKey     = "nxm_key"
	sourceDataNXExpires = "nxm_expires"
	sourceDataNXUserID  = "nxm_user_id"
)

// Source is the Nexus Mods source.
//
// The zero value is not usable: use New. client is a pointer because httpx
// clients are immutable copies and a source shares one across every call.
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

// WithGameDomain sets the game's site domain, the slug that appears in mod page
// URLs. A domain containing anything but letters, digits, dashes and underscores
// is ignored: the value is interpolated into a request path, and a slash in it
// would turn one request into a different one.
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

// init registers the factory. The error is discarded on purpose: a duplicate id
// means another package already claimed this name, which is not worth crashing
// over at program start.
func init() {
	_ = services.RegisterSourceFactory(
		SourceID,
		func(cfg domain.SourceConfig) (domain.ModSource, error) {
			return New(cfg)
		},
	)
}

// New builds a Nexus source from configuration.
//
// Explicit options are applied first and the configuration block second, so a
// key present in both resolves to the configuration block. That is the opposite
// of what most libraries do and it is deliberate: the config file is the user's
// own statement about how they want this source to behave, and it should win
// over whatever the wiring layer happened to pass in.
//
// A configuration written for the v1 API is still honoured where it can be: its
// API key is the same personal key v3 wants. Its game_id is ignored, because v3
// addresses games by domain and the numeric id has no meaning here.
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
		// The transport refuses a base that is not http or https anyway, but it
		// refuses it once per request. Reporting it here means a user who mistyped
		// the base is told what they mistyped instead of watching every call fail.
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%q is not a Nexus API base; expected an http or https URL such as %s",
			s.apiBase, DefaultAPIBase)
	}

	return s, nil
}

// validAPIBase reports whether a base is something the transport will accept.
//
// The transport rejects anything that is not http or https, and it does so by
// refusing the request rather than by reporting a bad configuration. Applying the
// same rule at construction moves the diagnosis to where the mistake was made.
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
// path. Nexus game domains are URL slugs: letters, digits, dashes and
// underscores. Anything else is refused rather than escaped, because a domain
// containing a slash or a space is a configuration mistake, and quietly
// percent-encoding it would produce a request nobody intended.
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

// RequiresAPIKey reports that a key is needed to install anything.
//
// Only the trending feed and the DLC list are public, so a source with no key
// can show five trending mods and install none of them. Reporting otherwise would
// let the manager present a source that looks ready and cannot do its job.
func (s *Source) RequiresAPIKey() bool { return true }

// Authenticate stores an API key and checks that Nexus accepts it.
//
// v3 has no login step, so there is nothing to exchange and nothing to cache: the
// key is the credential. Checking it here rather than at first use means a user
// who pasted the wrong key is told while they are still looking at the form.
// A blank credential is reported as ErrCodeAuthRequired without a request.
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

// ValidateCredentials checks the stored key against Nexus.
//
// No key is not an error: the trending feed is public, so a user with no
// credential is browsing rather than broken. A key that is present and rejected
// is a failure, and the credential is discarded so the next attempt starts from
// what the user typed rather than from what was rejected.
func (s *Source) ValidateCredentials(ctx context.Context) error {
	ctx = sourceutil.CtxOrBackground(ctx)

	if s.apiKeyIfAny() == "" {
		return nil
	}

	if err := s.checkCredential(ctx); err != nil {
		// A refused key is discarded so the next attempt starts from what the
		// user typed. A failure that was *not* a refusal — a timeout, a 500, a
		// DNS failure — leaves the key alone: throwing away a credential because
		// the network hiccuped would make somebody retype a key that was fine.
		if appErrors.IsAppError(err, appErrors.ErrCodeAuthFailed) {
			s.forgetCredential()
		}

		return err
	}

	return nil
}

// checkCredential asks Nexus to resolve an id that does not exist. The call is
// authenticated, so a rejected key fails before the id is ever considered.
func (s *Source) checkCredential(ctx context.Context) error {
	header, err := s.authHeader()
	if err != nil {
		return err
	}
	// The payload is handed over as a value and not as bytes. httpx owns the JSON
	// encoding for every source, and a caller that encodes first gets it encoded a
	// second time: the endpoint then receives a JSON string where it requires an
	// object and answers 422, and the problem document names a request shape this
	// code never wrote. httpx refuses a []byte body for exactly that reason, so
	// passing the struct is not merely tidier — it is the only accepted form.
	//
	// The body of a good answer is an empty mod list and the body of a bad one is
	// a problem document; neither says anything the status did not already say,
	// so the response is not decoded.
	if _, err := s.client.Do(ctx, httpx.Request{
		Method: http.MethodPost,
		URL:    s.apiBase + "/mods/batch",
		Body:   modsBatchRequest{ModIDs: []string{credentialProbeID}},
		Header: header,
		Kind:   httpx.KindAuth,
	}); err != nil {
		// A 401 from here means the key was refused rather than missing, because the
		// request carries one. classify does that translation for the reads too, so
		// a rejected credential is reported the same way whichever call noticed it.
		return classify(err)
	}

	return nil
}

// GetMod fetches one mod by its manager id.
//
// Three requests: the mod's own record for its name, the file's versions for
// something to install, and the chosen version's dependencies. They are made in
// that order because the version list decides whether there is a mod at all, so
// an id whose file has no installable version is answered without ever asking
// about the mod.
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
	// The file half of an id is read through modFileForID rather than modFile
	// because a pasted link numbers a file the legacy way, and only the v3 id is
	// what the file endpoint understands. The resolution is a no-op for an id the
	// manager produced itself, which is the common case.
	file, err := s.modFileForID(ctx, scopedID, fileID)
	if err != nil {
		return nil, err
	}

	versions, err := s.modFileVersions(ctx, file.ID)
	if err != nil {
		return nil, err
	}

	version, ok := pickVersion(versions)
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound,
			"Nexus mod %s file %s has no downloadable version", scopedID, fileID)
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

	s.attachDependencies(ctx, mod)

	return mod, nil
}

// Search returns the trending feed, filtered locally.
//
// See the package comment: v3 has no search operation, so a query is applied to
// the five trending mods the public feed returns. A failing provider is reported
// rather than swallowed — unlike the registry's multi-source fan-out, a direct
// Search on one provider has nothing to fall back to, and an empty result would
// read as "this mod does not exist".
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
	// Every entry has to be expanded before it can be listed, and that needs a
	// credential, so the outcome depends on more than the feed request. The first
	// failure is kept so that "you have no key" can be reported as that rather
	// than as an empty result: an empty list reads as "nothing trending matched",
	// which is a statement about Nexus and not about the user's configuration.
	var firstErr error

	identified := 0

	mods := make([]domain.Mod, 0, len(feed.Data.Mods))
	for _, entry := range feed.Data.Mods {
		if err := ctx.Err(); err != nil {
			return nil, canceledError(err)
		}

		scopedID, ok := scopedIDFromPageURL(entry.ModPageURL)
		if !ok {
			// A feed entry with no recoverable id cannot be installed, and a
			// guessed one could be a different mod entirely. It is skipped rather
			// than counted as a failure, because nothing was asked of the API.
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
	// Reporting the failure only when nothing was reached at all is the same rule
	// the registry's fan-out uses: one unreachable mod must not empty the feed,
	// but an empty feed caused entirely by one cause is that cause.
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
	// The feed knows things the mod endpoint does not, and they are about the
	// same mod. The name is the one exception and it is taken from the feed even
	// when the mod record has one: Search's only job is to filter this feed, and
	// a query naming an entry as it appears on the trending page has to match it.
	// A rename on the mod page then shows the name the feed is using, which is
	// the name the user just searched for.
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
			// A single unusable file is not a failure of the mod: it is one
			// broken update chain among possibly several, and the file list
			// itself answered fine.
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

// modContext fetches the mod a game-scoped id names.
//
// A 404 is answered as "no such mod" rather than as the transport's not-found,
// because a caller showing a mod page has no use for a code it does not know.
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
		// The requested id wins over the one in the response. The caller asked for
		// a specific mod page, and a response disagreeing with its own request is
		// either a redirect or a mistake — and in neither case may it silently
		// rename the mod into another mod's identity.
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
		// Without the composite id there is no route to the files. Answering with
		// nothing keeps a caller that cannot enumerate files from offering a mod
		// it could not install.
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
		// A 200 with nothing in it means the shape did not match, not that the
		// file is absent: the API answers a missing file with 404. Saying "no such
		// file" here would send the user looking for a mod they already installed.
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
// link carries, and reports the v3 mod file id that version belongs to.
//
// The version read by id does not name its mod file, and the file read by id
// does not name its version, so a link carrying the legacy number and nothing
// else cannot be turned into the pair the manager identifies a mod by without
// this third shape. It is the only read that supplies both at once.
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
// either numbering.
//
// A link pasted from the website numbers a file the legacy way, and that number
// is not the v3 mod file id: the two differ for the same file, and only the v3
// id is what the file endpoint understands. The legacy number is a version's
// game-scoped id, and one request against the version endpoint turns it into
// the v3 id. An id that already is a v3 id is returned untouched, so a mod the
// manager itself produced — and every mod it already stores — costs nothing.
func (s *Source) modFileForID(ctx context.Context, scopedID, fileID string) (fileAggregate, error) {
	file, err := s.modFile(ctx, fileID)
	if err == nil {
		return file, nil
	}

	if !appErrors.IsAppError(err, appErrors.ErrCodeModNotFound) {
		return fileAggregate{}, err
	}

	_, v3FileID, err := s.modFileVersionByScopedID(ctx, scopedID, fileID)
	if err != nil {
		return fileAggregate{}, err
	}

	if v3FileID == "" {
		return fileAggregate{}, appErrors.New(appErrors.ErrCodeModNotFound,
			"Nexus mod %s file %s is not a file this manager can install", scopedID, fileID)
	}

	return s.modFile(ctx, v3FileID)
}

// attachDependencies records a version's requirements on the mod.
//
// A failure is recorded rather than returned. The mod is installable either way,
// and the difference between "this mod needs nothing" and "this mod's
// requirements could not be read" is worth keeping, because only the second is a
// reason to distrust the manager's conflict report.
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
// The API does not serve the bytes: a POST mints a short-lived URL and that URL
// is then fetched. The second request carries no credentials, because it goes to
// a content host and is already signed.
//
// The version to fetch comes from the mod's SourceData. A mod built by hand — a
// caller that never went through Search or GetMod — has no version id, so the
// file's versions are listed and the newest installable one is used; a mod
// pinned to an older version keeps the id it was given, which is what makes an
// "install version 1.2.0" request install 1.2.0.
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
	// No header. The URL is signed, and the API key belongs to the account rather
	// than to the content host; sending it on would put a live credential on
	// whatever the URL points at.
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
			"Nexus file %s has no downloadable version", fileID)
	}

	return strings.TrimSpace(version.ID), nil
}

// downloadURL mints the short-lived URL for one version's archive.
//
// A mod whose record carries a keyed link's authorization is fetched through the
// legacy endpoint that accepts it, because the v3 endpoint it would otherwise
// use is premium-only and answers a free account with 403. Which of the two a
// mod gets is decided by the mod rather than by the caller: the authorization is
// spent here, and a caller that has no business knowing which endpoint was hit
// should not have to ask.
func (s *Source) downloadURL(
	ctx context.Context,
	versionID string,
	mod *domain.Mod,
) (string, error) {
	if _, _, _, ok := nxmGrant(mod); ok {
		url, err := s.downloadURLForKeyed(ctx, mod, versionID)
		if err != nil {
			// The grant survives a refusal. The endpoint answered with a status
			// rather than minting a URL, which means the one-time key was not
			// spent — and a key burned on a failure that might have succeeded on
			// a retry is a key the user has to go back to the website to replace.
			return "", err
		}
		// The grant is spent the moment the endpoint mints, so it is dropped
		// here rather than left in the record as a dead secret.
		forgetNXMGrant(mod)

		return url, nil
	}

	return s.downloadURLRepacked(ctx, versionID)
}

// downloadURLRepacked mints the short-lived URL for one version's repacked
// archive.
//
// This is a POST with no body, not a GET. GetJSON would send a GET and the API
// would answer 404 or 405 for an endpoint that exists, which is exactly the kind
// of failure that reads as "this version is not downloadable" rather than "the
// client called the wrong verb".
func (s *Source) downloadURLRepacked(ctx context.Context, versionID string) (string, error) {
	header, err := s.authHeader()
	if err != nil {
		return "", err
	}
	// The API records this against the download, so it says who is asking.
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
	// No "data" wrapper on this response: the body is the schema.
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
// a keyed nxm link carried.
//
// The endpoint is GET rather than POST, and it takes the three query parameters
// the website put in the link. It answers with a list of mirrors rather than one
// URL, and the first is used: they are the same file on different content hosts,
// and a manager has no basis for preferring one over another.
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

	scopedID, _, ok := ParseModID(mod.ID)
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"mod %q does not carry a Nexus mod and file id", mod.ID)
	}
	// The legacy endpoint numbers a file the legacy way, and the id in the link
	// is the version's game-scoped id rather than the v3 mod-file id the manager
	// stores. One read recovers it; guessing would name a different file.
	version, err := s.modFileVersion(ctx, versionID)
	if err != nil {
		return "", err
	}

	fileScopedID := strings.TrimSpace(version.GameScopedID)
	if fileScopedID == "" {
		return "", appErrors.New(appErrors.ErrCodeDownloadFailed,
			"Nexus version %s published no legacy file id to authorize", versionID)
	}

	var mirrors downloadLinkResponse

	path := "/games/" + s.gameDomain + "/mods/" + scopedID +
		"/files/" + fileScopedID + "/download_link"
	query := url.Values{}
	query.Set("key", grantKey)
	query.Set("expires", expires)
	query.Set("user_id", userID)

	if err := s.authedV1JSON(ctx, path, query, &mirrors, httpx.KindDownload); err != nil {
		return "", err
	}

	for _, mirror := range mirrors {
		if uri := strings.TrimSpace(mirror.URI); uri != "" {
			return uri, nil
		}
	}

	return "", appErrors.New(appErrors.ErrCodeDownloadFailed,
		"Nexus authorized the download of mod %s but offered no mirror", sourceutil.ModLabel(mod))
}

// nxmGrant reads the one-time download authorization a keyed nxm link left in a
// mod's SourceData.
//
// A free account cannot mint a download URL: POST download-repacked answers 403
// to a key without premium. The website's own answer is to hand a registered
// nxm:// handler a link carrying key, expires and user_id, and those three
// together are what the legacy download_link endpoint needs in exchange for a
// signed URL. All three are required — the endpoint answers 403 to a partial
// set — so a link carrying only some of them is not a grant, and reporting it as
// one would trade a clear "this link has no key" for a 403 that blames the
// account.
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

// forgetNXMGrant drops a spent authorization.
//
// The endpoint spends the key when it answers, so a copy left in the mod's
// record would be a dead secret sitting in the index — and the index is the one
// place a mod's data outlives the download it was for.
func forgetNXMGrant(mod *domain.Mod) {
	if mod == nil || mod.SourceData == nil {
		return
	}

	delete(mod.SourceData, sourceDataNXKey)
	delete(mod.SourceData, sourceDataNXExpires)
	delete(mod.SourceData, sourceDataNXUserID)
}

// Verify accepts every downloaded file.
//
// v3 publishes no digest, size or checksum for any downloadable object, so there
// is no expected value to compare against and pretending otherwise would either
// be a no-op dressed as a check or a false rejection. The manager records a
// SHA-256 of the archive it installs, so a copy on disk can be compared against
// that later; what is unavailable is a value from the platform to check the
// first copy against.
func (s *Source) Verify(ctx context.Context, mod *domain.Mod, path string) error {
	return nil
}

// unauthHeader returns the headers for a request that needs no credentials.
func (s *Source) unauthHeader() http.Header {
	header := http.Header{}
	header.Set("Accept", "application/json")

	return header
}

// authHeader returns the headers for an authenticated request.
//
// A missing key is refused locally rather than sent, so an unauthenticated
// caller is told what is missing instead of collecting a 401 from every endpoint
// it tries.
func (s *Source) authHeader() (http.Header, error) {
	key := s.apiKeyIfAny()
	if key == "" {
		return nil, appErrors.New(appErrors.ErrCodeAuthRequired,
			"Nexus needs an API key; run: dmm source auth nexus --api-key-stdin")
	}

	header := s.unauthHeader()
	// The header name is "apikey", lower case and without a prefix. That is what
	// the specification's ApiKeyAuth scheme declares, and a request using
	// "x-api-key" is simply unauthenticated.
	header.Set("apikey", key)

	return header, nil
}

// authedJSON performs one authenticated GET against the v3 base and decodes the
// body into out.
//
// Every authenticated v3 read goes through here, so the credential policy lives in
// one place: a source with no key is told so locally, once, rather than
// collecting a 401 from every endpoint it tries. It is also a helper rather than
// an inline header argument because Go will not spread a two-value call into a
// call that already carries other arguments.
func (s *Source) authedJSON(ctx context.Context, path string, out any, kind httpx.Kind) error {
	return s.authedJSONAt(ctx, s.apiBase, path, nil, out, kind)
}

// authedV1JSON is authedJSON against the legacy base.
//
// It exists for the one read that has no v3 equivalent. Everything the plugin
// reads is v3; the keyed download_link is v1 or nothing.
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
	// A rejected key is worth forgetting immediately. Holding on to it would make
	// every later call fail with the same message while the user has already been
	// told to paste a new one.
	if appErrors.IsAppError(err, appErrors.ErrCodeAuthRequired) {
		s.forgetCredential()
	}

	return classify(err)
}

// v1Base returns the legacy API base, which serves the one endpoint v3 has no
// equivalent for: the keyed download_link a free account needs.
//
// The two bases share a host and differ only in the version segment, so the v1
// base is the configured base with its trailing version segment replaced. A base
// whose last segment is not a version is returned unchanged rather than guessed
// at: a wrong host is worse than no fallback.
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
// Two translations matter here. A 404 on any of these reads means the mod or the
// file asked for is not there, and a caller has no use for the transport's
// not-found — the registry and the CLI both know how to present "no such mod".
// And a 401 means the key was refused; a key was supplied, so it is wrong rather
// than missing, and reporting it as missing sends the user to a field they had
// already filled in.
//
// The explanation is spelled out in the message because the error chain below
// still says AUTH_REQUIRED: the service answers a refused key with the same 401
// it uses for a missing one, and without the sentence that reads as a
// contradiction rather than as a decision.
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
// Nexus. The settings file has already been changed by the time this is called;
// this is the half that stops the running process from sending a key the user
// has just deleted.
func (s *Source) ForgetCredentials() { s.forgetCredential() }

// HasCredentials reports whether an API key is configured, without contacting
// Nexus.
func (s *Source) HasCredentials() bool { return s.apiKeyIfAny() != "" }

// Authenticated reports whether a credential is stored. v3 has no token to
// expire, so this means "a key is configured", not "a key was proven good" — use
// ValidateCredentials for that. It deliberately performs no request.
func (s *Source) Authenticated() bool { return s.HasCredentials() }

// pluginVersion is reported by Version. It is a constant rather than a build
// stamp so a build of the manager and a build of the plugin always agree.
const pluginVersion = "1.0.0"

// decodeJSON unmarshals a response body.
//
// The endpoints that answer without a "data" envelope are the ones whose bodies
// are decoded through here, and a body that is not the JSON the operation
// described means the API answered something else — a proxy error page, or an
// object that changed shape — and reporting that as a decode failure is more
// useful than reporting it as an empty result.
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
