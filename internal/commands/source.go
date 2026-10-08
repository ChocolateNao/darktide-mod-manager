package commands

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

// SourceCommands is the mod sources as a frontend sees them: which are
// available, what they can do, how to sign in, and what they are offering.
type SourceCommands struct {
	// base is embedded rather than held as a named field so the lifecycle
	// methods reach a set directly: a caller holding a single set needs to be
	// able to shut it down without knowing the shape of the aggregate.
	*base
}

// NewSourceCommands builds the source command set.
func NewSourceCommands(deps Deps) *SourceCommands {
	return &SourceCommands{base: newBase(deps)}
}

func newSourceCommands(b *base) *SourceCommands {
	return &SourceCommands{base: b}
}

// ListSources describes every available source.
//
// Unlike the methods that install or sign in, this one takes no context of its
// own and cannot fail in a way a frontend should show as an error: it degrades to
// a source with Enabled false and an empty name, because a settings screen with a
// blank entry and a reason beats one that refuses to draw. The method still
// reports the not-initialised and shutting-down cases, which are programming
// errors rather than data.
func (c *SourceCommands) ListSources() ([]api.ModSourceInfo, error) {
	client, _, _, err := c.ready()
	if err != nil {
		return nil, err
	}

	infos := client.ListSources(c.Context())
	if infos == nil {
		// api documents a nil result as "no sources at all". Over the bridge nil
		// becomes null, and a frontend iterating the result would have to guard
		// against a shape the service never returns otherwise.
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

// SearchSource searches one source.
//
// A source that cannot search is refused rather than answered with an empty
// list: an empty list is a real answer the user would believe, and "this source
// does not support searching" is the true one.
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

// AuthenticateSource signs a source in.
//
// The credentials arrive as a map because every source asks for a different set
// — a token, a username and a key — and a per-source command signature would put
// that knowledge in the frontend. A nil map is passed through rather than
// replaced by an empty one, because "no credentials" and "credentials with no
// usable entries" are the same request to every source and the source decides.
//
// A successful call stores the credentials: the key has to outlive the process or
// the user signs in again on every launch.
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

// SetSourceAPIKey stores an API key without asking the source whether it works.
//
// The frontend uses it for the offline case: a user who pasted a key before
// connecting, or who would rather not wait for a round trip while typing.
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
// source. It is what a "sign out" button calls; nothing about the key comes back
// to the frontend, because nothing about the key ever left the backend.
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
// itself, for the badge in the sources list.
//
// It reports success for an unknown source rather than failing: the flag is
// bookkeeping for display, and a source that has been unregistered since the
// screen was drawn has nothing left to badge.
func (c *SourceCommands) SetSourceAuthenticated(sourceID string, authenticated bool) error {
	if _, _, _, err := c.ready(); err != nil {
		return err
	}

	client := c.client
	client.SetSourceAuthenticated(strings.TrimSpace(sourceID), authenticated)

	return nil
}
