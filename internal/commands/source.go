package commands

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

// SourceCommands is the mod sources as a frontend sees them: which are
// available, what they can do, how to sign in, and what they are offering.
type SourceCommands struct {
	// Embedded so a caller holding one set can shut it down without the
	// aggregate.
	*base
}

func NewSourceCommands(deps Deps) *SourceCommands {
	return &SourceCommands{base: newBase(deps)}
}

func newSourceCommands(b *base) *SourceCommands {
	return &SourceCommands{base: b}
}

// ListSources describes every available source. It degrades to Enabled false with
// an empty name, because a blank entry beats a settings screen that refuses to
// draw. Not-initialised and shutting-down are still reported: those are
// programming errors.
func (c *SourceCommands) ListSources() ([]api.ModSourceInfo, error) {
	client, _, _, err := c.ready()
	if err != nil {
		return nil, err
	}

	infos := client.ListSources(c.Context())
	if infos == nil {
		// api documents nil as "no sources at all", but over the bridge nil is
		// null and a frontend iterating it guards a shape never otherwise returned.
		return []api.ModSourceInfo{}, nil
	}

	return infos, nil
}

// SourceInfo describes one source.
func (c *SourceCommands) SourceInfo(sourceID string) (api.ModSourceInfo, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return api.ModSourceInfo{}, err
	}
	defer cancel()

	info, err := client.SourceInfo(ctx, strings.TrimSpace(sourceID))
	if err != nil {
		return api.ModSourceInfo{}, c.fail(err, "describe source %q", sourceID)
	}

	return info, nil
}

// SearchSource searches one source. A source that cannot search is refused,
// because an empty list is a real answer the user would believe.
func (c *SourceCommands) SearchSource(sourceID, query string) ([]api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	mods, err := client.SearchSource(ctx, strings.TrimSpace(sourceID), strings.TrimSpace(query))
	if err != nil {
		return nil, c.fail(err, "search source %q for %q", sourceID, query)
	}

	return mods, nil
}

// SearchAll searches every source that supports it, and returns the union.
func (c *SourceCommands) SearchAll(query api.SearchQuery) ([]api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	mods, err := client.Search(ctx, query)
	if err != nil {
		return nil, c.fail(err, "search every source")
	}

	return mods, nil
}

// GetSourceMod fetches one mod's details from a source without installing it.
func (c *SourceCommands) GetSourceMod(sourceID, modID string) (*api.Mod, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	mod, err := client.GetSourceMod(ctx, strings.TrimSpace(sourceID), strings.TrimSpace(modID))
	if err != nil {
		return nil, c.fail(err, "read %s/%s", sourceID, modID)
	}

	return mod, nil
}

// InspectDroppedPath describes the mod a drop gesture landed on, because the drop
// overlay has to name what it is about to install and no other command can:
// Search needs a query and GetMod needs an id. The result carries the recognised
// flag so the overlay can refuse an unclaimed path in its own wording. Wails only
// binds one or two return values, hence the result struct.
func (c *SourceCommands) InspectDroppedPath(path string) (*api.DroppedPathResult, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	mod, recognised, err := client.InspectDroppedPath(ctx, strings.TrimSpace(path))
	if err != nil {
		return nil, c.fail(err, "inspect dropped path %q", path)
	}

	return &api.DroppedPathResult{Mod: mod, Recognised: recognised}, nil
}

// AuthenticateSource signs a source in. The credentials arrive as a map because
// every source asks for a different set, and a per-source signature would put that
// knowledge in the frontend. A nil map passes through: "no credentials" and
// "credentials with no usable entries" are the same request, and the source
// decides. A successful call stores the credentials, since the key has to outlive
// the process.
func (c *SourceCommands) AuthenticateSource(sourceID string, creds map[string]string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	id := strings.TrimSpace(sourceID)
	if err := client.AuthenticateSource(ctx, id, creds); err != nil {
		return c.fail(err, "authenticate source %q", sourceID)
	}

	return nil
}

// SetSourceAPIKey signs a source in with a single key. The source is asked before
// the key is stored, so a key the provider refuses never reaches the settings file
// and a key it accepts works immediately: the registry caches built sources for
// the process's life.
func (c *SourceCommands) SetSourceAPIKey(sourceID, apiKey string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.SetSourceAPIKey(ctx, strings.TrimSpace(sourceID), apiKey); err != nil {
		return c.fail(err, "store API key for source %q", sourceID)
	}

	return nil
}

// ClearSourceAPIKey forgets a stored key, in the settings file and in the live
// source. It is what a "sign out" button calls; nothing about the key reaches the
// frontend.
func (c *SourceCommands) ClearSourceAPIKey(sourceID string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.ClearSourceAPIKey(ctx, strings.TrimSpace(sourceID)); err != nil {
		return c.fail(err, "forget the API key for source %q", sourceID)
	}

	return nil
}

// TestSourceConnection checks a stored credential without changing it.
func (c *SourceCommands) TestSourceConnection(sourceID string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	id := strings.TrimSpace(sourceID)
	if err := client.TestSourceConnection(ctx, id); err != nil {
		return c.fail(err, "test source %q", sourceID)
	}

	return nil
}

// SetSourceAuthenticated records the result of a sign-in the frontend performed
// itself, for the badge in the sources list. An unknown source reports success:
// the flag is bookkeeping for display, and a source unregistered since the screen
// was drawn has nothing to badge.
func (c *SourceCommands) SetSourceAuthenticated(sourceID string, authenticated bool) error {
	if _, _, _, err := c.ready(); err != nil {
		return err
	}

	client := c.client
	client.SetSourceAuthenticated(strings.TrimSpace(sourceID), authenticated)

	return nil
}
