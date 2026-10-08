package api

import (
	"context"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// AuthCredentials are the values a caller supplies to authenticate against a
// source: a username, an API key, a token. It is an alias so the same value can
// be handed to a domain.ModSource the caller implements themselves.
type AuthCredentials = domain.AuthCredentials

// sourceRegistry returns the source registry, or the error to report.
func (c *Client) sourceRegistry() (*services.SourceRegistry, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.Sources == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a source registry")
	}

	return c.deps.Sources, nil
}

// ListSources describes every registered source.
//
// It cannot fail: describing a source is reading its capabilities, and a source
// that cannot be built is still described — with Enabled false — because a source
// silently missing from the list is the one failure mode a user cannot diagnose.
// A closed client returns nil.
func (c *Client) ListSources(ctx context.Context) []ModSourceInfo {
	registry, err := c.sourceRegistry()
	if err != nil {
		return nil
	}

	infos := registry.Infos()
	// One read for the whole list: the answer is the same for every row, and a
	// settings screen asking for the list should not read the file once per
	// source.
	cfg := c.storedConfig()
	for i := range infos {
		infos[i] = withStoredKey(infos[i], cfg)
	}

	return infos
}

// SourceInfo describes one source. An unknown id returns ErrCodeSourceNotFound.
func (c *Client) SourceInfo(ctx context.Context, sourceID string) (ModSourceInfo, error) {
	registry, err := c.sourceRegistry()
	if err != nil {
		return ModSourceInfo{}, err
	}

	id := strings.TrimSpace(sourceID)
	if id == "" {
		return ModSourceInfo{}, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"source id must not be blank")
	}
	// Has, not Get: describing a source must not build it, so a source that is
	// switched off or misconfigured is still described — and a source that does
	// not exist is reported as missing rather than as the same empty
	// description. Info answers from the registry without building anything, so
	// a broken source still gets described rather than failing here; the registry
	// reports a source it could not build as disabled, which is exactly what a
	// caller wants to see.
	if !registry.Has(id) {
		return ModSourceInfo{}, appErrors.New(appErrors.ErrCodeSourceNotFound,
			"no mod source with id %q is registered", id)
	}

	return withStoredKey(registry.Info(id), c.storedConfig()), nil
}

// storedConfig reads the current configuration, or returns nil when it cannot be
// read.
//
// It exists for one fact. The registry judges "is a key stored?" against the
// configuration it was built with — the composition root merges the command line
// over the file into that value and hands it over on purpose — so a key stored
// through this API is invisible to it until the next launch. A user who signs in
// and is then told the source has no key has been told something false about
// their own machine. The store is the thing that changed, so the store is what is
// asked.
func (c *Client) storedConfig() *domain.Config {
	if c == nil || c.deps.Config == nil {
		return nil
	}

	cfg, err := c.deps.Config.Load()
	if err != nil {
		return nil
	}

	return cfg
}

// withStoredKey replaces the registry's answer with the one the configuration
// file gives. A configuration that could not be read leaves the registry's answer
// in place: it is a guess, but it is the only one there is.
func withStoredKey(info ModSourceInfo, cfg *domain.Config) ModSourceInfo {
	if cfg == nil {
		return info
	}

	block, ok := cfg.SourceConfigFor(info.ID)
	info.HasAPIKey = ok && strings.TrimSpace(block.APIKey) != ""

	return info
}

// SearchSource searches one source by name.
//
// Only the free-text part of a SearchQuery is exposed here; a caller that needs
// tags, paging or an explicit sort uses Search and passes a SearchQuery. Searching
// a source that does not support search reports ErrCodeSourceUnavailable, which
// is more useful than an empty list: the difference between "this source has
// nothing matching" and "this source cannot be searched" is the difference
// between a user trying another word and a user filing a bug.
func (c *Client) SearchSource(ctx context.Context, sourceID, query string) ([]Mod, error) {
	registry, err := c.sourceRegistry()
	if err != nil {
		return nil, err
	}

	source, err := registry.Get(strings.TrimSpace(sourceID))
	if err != nil {
		return nil, err
	}

	if !source.SupportsSearch() {
		return nil, appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"source %q cannot be searched", source.ID())
	}

	mods, err := source.Search(ctx, SearchQuery{Query: strings.TrimSpace(query)})
	if err != nil {
		return nil, err
	}

	return mods, nil
}

// Search runs a full query against every searchable source at once.
//
// A source that fails does not fail the search: the remaining results are
// returned and only an error is reported when every source failed, because
// returning an error alongside a partial result invites the caller to discard
// the results that are good. Use ListSources and SearchSource when a caller needs
// to know which provider answered.
func (c *Client) Search(ctx context.Context, query SearchQuery) ([]Mod, error) {
	registry, err := c.sourceRegistry()
	if err != nil {
		return nil, err
	}

	return registry.SearchAll(ctx, query)
}

// GetSourceMod fetches one mod's metadata from its source, without installing
// it. It is how a UI shows a mod's description and dependencies before asking
// the user to install.
func (c *Client) GetSourceMod(ctx context.Context, sourceID, modID string) (*Mod, error) {
	registry, err := c.sourceRegistry()
	if err != nil {
		return nil, err
	}

	source, err := registry.Get(strings.TrimSpace(sourceID))
	if err != nil {
		return nil, err
	}

	return source.GetMod(ctx, strings.TrimSpace(modID))
}

// AuthenticateSource supplies credentials to one source and, once the source has
// confirmed them, stores them in the application configuration.
//
// The accepted keys are the source's own: Nexus wants a username and an api_key,
// Vortex and GitHub accept a token under api_key or token. A source that needs
// none reports ErrCodeAuthRequired — there is nothing to enter, and asking is how
// a user learns the source needs no credentials.
//
// Persistence is part of the call on purpose. The registry's record of a
// successful sign-in lives in memory and dies with the process, so a caller that
// authenticated and stopped there would have to repeat the sign-in on every
// launch; the API key in the configuration file is what the sources rebuild
// themselves from. A credential the source rejects is therefore never written.
//
// The registry records the outcome, so Authenticated on the returned source info
// flips to true only after the source itself confirms the credential.
func (c *Client) AuthenticateSource(
	ctx context.Context,
	sourceID string,
	creds map[string]string,
) error {
	registry, err := c.sourceRegistry()
	if err != nil {
		return err
	}

	id := strings.TrimSpace(sourceID)
	if err := registry.Authenticate(ctx, id, AuthCredentials(creds)); err != nil {
		return err
	}

	return c.persistCredentials(id, creds)
}

// SetSourceAPIKey stores an API key for a source without asking the source about
// it. It is SetSourceCredentials for the common case; a source that also needs a
// username is covered by that function directly.
func (c *Client) SetSourceAPIKey(ctx context.Context, sourceID, apiKey string) error {
	return c.SetSourceCredentials(ctx, sourceID, map[string]string{"api_key": apiKey})
}

// SetSourceCredentials stores credentials for a source without asking the source
// about them.
//
// Verification is a network round trip to the provider, and it is the wrong thing
// to insist on in two situations: a machine with no connection yet, and a key the
// provider would refuse for a reason the provider cannot explain. Use
// AuthenticateSource when the key is new, TestSourceConnection to find out whether
// a stored one works, and this when the answer is "store it either way".
func (c *Client) SetSourceCredentials(
	ctx context.Context,
	sourceID string,
	creds map[string]string,
) error {
	return c.persistCredentials(sourceID, creds)
}

// ClearSourceAPIKey forgets a stored key.
//
// It clears the configuration *and* the live source, because the registry caches
// built sources for the life of the process: a key removed from the file alone
// would keep working until the next launch, which is the opposite of what a user
// who clicked "forget" is asking for.
func (c *Client) ClearSourceAPIKey(ctx context.Context, sourceID string) error {
	if c == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "no client")
	}

	if err := c.ready(); err != nil {
		return err
	}

	id := strings.TrimSpace(sourceID)

	store := c.deps.Config
	if store == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a configuration store")
	}

	cfg, err := store.Load()
	if err != nil {
		return err
	}

	if cfg == nil {
		return appErrors.New(appErrors.ErrCodeConfigInvalid,
			"configuration store returned no configuration")
	}

	block, ok := cfg.SourceConfigFor(id)
	if !ok || strings.TrimSpace(block.APIKey) == "" {
		return appErrors.New(appErrors.ErrCodeNotFound,
			"source %q has no stored API key", id)
	}

	updated := cfg.Clone()
	sourceBlock, _ := updated.SourceConfigFor(id)
	sourceBlock.APIKey = ""
	updated.SetSource(id, sourceBlock)

	if _, err := store.SaveIfChanged(updated); err != nil {
		return err
	}
	// The registry knows best whether this source can drop the credential it is
	// holding, so its refusal is the error the caller gets — after the file is
	// already clean, which is the half we can guarantee.
	if c.deps.Sources != nil {
		return c.deps.Sources.ForgetCredentials(id)
	}

	return nil
}

// credentialField maps one credential name onto the configuration field that
// holds it.
//
// api_key is the field every source reads back (see each plugin's constructor),
// and the aliases are the names the sources themselves accept, so a caller that
// used the provider's own spelling still ends up in the right place. A username is
// an option because no source has a dedicated field for it, and anything else is
// stored as an option too rather than dropped: a source may need a field this API
// has never heard of, and silently keeping half of a sign-in is worse than
// keeping all of it somewhere the source can read.
func credentialField(name string) (string, bool) {
	switch normalizeKey(name) {
	case "apikey", "key", "token":
		return "api_key", true
	case "username", "user":
		return "options.username", true
	}

	return "", false
}

// persistCredentials writes credentials into the stored configuration in one
// read-modify-write cycle.
//
// The whole set goes in at once because half a sign-in is not a state worth
// storing: a token written while its username was dropped looks configured on the
// next launch and fails on first use, which is a harder bug to diagnose than no
// configuration at all.
func (c *Client) persistCredentials(sourceID string, creds map[string]string) error {
	if c == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "no client")
	}

	if err := c.ready(); err != nil {
		return err
	}

	id := strings.TrimSpace(sourceID)

	store := c.deps.Config
	if store == nil {
		// A client built straight from services has nothing to persist into.
		// Failing loudly beats reporting a sign-in that will not survive a
		// restart.
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a configuration store")
	}

	writes := map[string]string{}

	for name, value := range creds {
		trimmedName := strings.TrimSpace(name)

		trimmedValue := strings.TrimSpace(value)
		if trimmedName == "" || trimmedValue == "" {
			// Sources already reject a blank credential before they get here; this
			// only keeps an empty option out of the configuration file.
			continue
		}

		field, ok := credentialField(trimmedName)
		if !ok {
			// A dot would read as a nested source field and land somewhere the
			// source never looks.
			if strings.Contains(trimmedName, ".") {
				return appErrors.New(appErrors.ErrCodeInvalidArgument,
					"credential name %q must not contain a dot", trimmedName)
			}

			field = "options." + normalizeKey(trimmedName)
		}

		writes[field] = trimmedValue
	}

	if len(writes) == 0 {
		return appErrors.New(appErrors.ErrCodeInvalidArgument,
			"no usable credentials for source %q", id)
	}

	cfg, err := store.Load()
	if err != nil {
		return err
	}

	if cfg == nil {
		return appErrors.New(appErrors.ErrCodeConfigInvalid,
			"configuration store returned no configuration")
	}

	updated := cfg.Clone()
	for field, value := range writes {
		if err := SetConfigValue(updated, "sources."+id+"."+field, value); err != nil {
			return err
		}
	}

	_, err = store.SaveIfChanged(updated)

	return err
}

// TestSourceConnection checks a source's stored credentials, reporting
// ErrCodeAuthFailed when the source rejects them and nil when the source needs
// none. It does not prompt, and it does not cache anything new beyond what the
// source itself records.
func (c *Client) TestSourceConnection(ctx context.Context, sourceID string) error {
	registry, err := c.sourceRegistry()
	if err != nil {
		return err
	}

	return registry.ValidateCredentials(ctx, strings.TrimSpace(sourceID))
}

// SetSourceAuthenticated overrides the registry's record of whether a source is
// authenticated. It exists for a source whose credential is managed elsewhere —
// a token issued out of band, for instance — and for tests. Passing a value the
// source disagrees with is a display error, not a functional one, which is why
// this returns nothing.
func (c *Client) SetSourceAuthenticated(sourceID string, authenticated bool) {
	registry, err := c.sourceRegistry()
	if err != nil {
		return
	}

	registry.SetAuthenticated(sourceID, authenticated)
}
