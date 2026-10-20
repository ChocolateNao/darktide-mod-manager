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

// ListSources describes every registered source. It cannot fail: a source that cannot
// be built is still described, with Enabled false, because a source silently missing
// from the list is the one failure mode a user cannot diagnose. A closed client
// returns nil.
func (c *Client) ListSources(ctx context.Context) []ModSourceInfo {
	registry, err := c.sourceRegistry()
	if err != nil {
		return nil
	}

	infos := registry.Infos()
	// One read for the whole list: the answer is the same for every row, and a
	// settings screen asking for the list should not read the file once per source.
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
	// Has, not Get: describing a source must not build it, so a switched-off or
	// misconfigured source is still described, and a missing one is reported as missing
	// rather than as the same empty description.
	if !registry.Has(id) {
		return ModSourceInfo{}, appErrors.New(appErrors.ErrCodeSourceNotFound,
			"no mod source with id %q is registered", id)
	}

	return withStoredKey(registry.Info(id), c.storedConfig()), nil
}

// storedConfig reads the current configuration, or nil when it cannot be read. It
// exists for one fact: the registry judges "is a key stored?" against the configuration
// it was built with — the composition root merges the command line over the file into
// that value on purpose — so a key stored through this API is invisible to it until the
// next launch, and a user who signs in and is then told the source has no key has been
// told something false. The store is what changed, so the store is what is asked.
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

// SearchSource searches one source by name. Only the free-text part of a SearchQuery is
// exposed here; a caller needing tags, paging or an explicit sort uses Search. A
// source that cannot be searched reports ErrCodeSourceUnavailable, which beats an
// empty list: "nothing matches" and "cannot be searched" are the difference between a
// user trying another word and a user filing a bug.
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

// DroppedMod is what a source recognises at a filesystem path, projected for a
// drop-and-confirm overlay. A projection rather than a domain.Mod because the overlay
// must say what it is about to install before an install exists; LocalPath and
// SourceData are withheld because nothing about the payload is needed to confirm it,
// and the install re-reads the path itself.
type DroppedMod struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Author   string `json:"author,omitempty"`
	Path     string `json:"path"`
}

// InspectDroppedPath asks the registered sources what mod sits at path.
//
// The recognised flag is separate from the error so the caller can refuse an
// unrecognised drop with a reason of its own wording, while a genuine failure
// still arrives as a failure.
func (c *Client) InspectDroppedPath(ctx context.Context, path string) (*DroppedMod, bool, error) {
	registry, err := c.sourceRegistry()
	if err != nil {
		return nil, false, err
	}

	mod, recognised, err := registry.InspectPath(ctx, strings.TrimSpace(path))
	if err != nil {
		return nil, false, err
	}

	if !recognised {
		return nil, false, nil
	}

	return &DroppedMod{
		SourceID: mod.SourceID,
		ModID:    mod.ID,
		Name:     mod.Name,
		Version:  mod.Version,
		Author:   mod.Author,
		Path:     path,
	}, true, nil
}

// Search runs a full query against every searchable source at once. A source that
// fails does not fail the search: the remaining results are returned and an error is
// reported only when every source failed, because an error alongside a partial result
// invites the caller to discard the results that are good. Use ListSources and
// SearchSource when a caller needs to know which provider answered.
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

// AuthenticateSource supplies credentials to one source and, once the source confirms
// them, stores them in the application configuration. The accepted keys are the
// source's own: Nexus wants a username and an api_key, Vortex and GitHub accept a
// token under api_key or token. A source that needs none reports ErrCodeAuthRequired —
// there is nothing to enter, and asking is how a user learns the source needs no
// credentials.
//
// Persistence is part of the call on purpose: the registry's record of a sign-in
// dies with the process, so stopping after authenticating would repeat the sign-in
// every launch. A credential the source rejects is never written, and the
// registry records the outcome, so Authenticated flips only once the source itself
// confirms.
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

// SetSourceAPIKey signs a source in with a single API key. The source is asked
// before the key is stored, which is what makes the stored key usable straight
// away: the registry caches built sources for the life of the process, so a key
// that only reached the settings file left the running source signing nothing and
// every download failed on AUTH_REQUIRED until the next launch. Validating first
// also means a key the provider refuses is never written at all.
//
// SetSourceCredentials is the escape hatch for a key entered before the machine
// can reach the provider, where there is nothing to validate against.
func (c *Client) SetSourceAPIKey(ctx context.Context, sourceID, apiKey string) error {
	return c.AuthenticateSource(ctx, sourceID, map[string]string{"api_key": apiKey})
}

// SetSourceCredentials stores credentials for a source without asking the source about
// them. Verification is a network round trip, and it is the wrong thing to insist on in
// two situations: a machine with no connection yet, and a key the provider would refuse
// for a reason the provider cannot explain. Use AuthenticateSource — or SetSourceAPIKey,
// which is it for a bare key — when the key is new, TestSourceConnection to find out
// whether a stored one works, and this when the answer is "store it either way".
//
// Storing alone only reaches the settings file. The live source keeps whatever it
// was built with until the next launch, so anything that has to work now must go
// through AuthenticateSource.
func (c *Client) SetSourceCredentials(
	ctx context.Context,
	sourceID string,
	creds map[string]string,
) error {
	return c.persistCredentials(sourceID, creds)
}

// ClearSourceAPIKey forgets a stored key. It clears the configuration *and* the live
// source, because the registry caches built sources for the life of the process: a key
// removed from the file alone would keep working until the next launch, the opposite of
// what a user who clicked "forget" is asking for.
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
	// holding, so its refusal is the error the caller gets — after the file is already
	// clean, the half we can guarantee.
	if c.deps.Sources != nil {
		return c.deps.Sources.ForgetCredentials(id)
	}

	return nil
}

// credentialField maps one credential name onto the configuration field that holds it.
// api_key is the field every source reads back (see each plugin's constructor) and the
// aliases are the names sources accept, so a caller using the provider's own spelling
// still lands in the right place. A username is an option because no source has a
// dedicated field, and anything else is stored as an option rather than dropped: a
// source may need a field this API has never heard of, and keeping half a sign-in is
// worse than keeping all of it somewhere the source can read.
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
// read-modify-write cycle. The whole set goes in at once because half a sign-in is
// not a state worth storing: a token written while its username was dropped looks
// configured on the next launch and fails on first use, a harder bug to diagnose
// than no configuration at all.
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
		// Sources already reject a blank credential before it gets here; this only
		// keeps an empty option out of the configuration file.
		if trimmedName == "" || trimmedValue == "" {
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
// authenticated. It exists for a source whose credential is managed elsewhere — a token
// issued out of band — and for tests. A value the source disagrees with is a display
// error, not a functional one, which is why this returns nothing.
func (c *Client) SetSourceAuthenticated(sourceID string, authenticated bool) {
	registry, err := c.sourceRegistry()
	if err != nil {
		return
	}

	registry.SetAuthenticated(sourceID, authenticated)
}
