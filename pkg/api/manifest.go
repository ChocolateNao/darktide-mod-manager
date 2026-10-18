package api

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/internal/manifest"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ModSourceLink is the set of web locations a mod can be fetched from, as the
// frontend sees it. Aliased rather than re-exported field by field so the type
// a UI binds to is literally the type the domain uses.
type ModSourceLink = domain.ModSourceLink

// ModLinkTarget names which of a mod's links to act on.
type ModLinkTarget = domain.ModLinkTarget

// ModLinkFallback describes what a user must do by hand because the manager
// cannot fetch the mod itself.
type ModLinkFallback = domain.ModLinkFallback

// URLOpener opens a web address in the user's browser. Declared as its own
// interface because "open this link" is all the source-link and manifest surfaces
// do, and a caller supplying a stub can assert on the URL rather than a real
// browser.
type URLOpener interface {
	OpenURL(ctx context.Context, rawURL string) error
}

// opener returns the URL opener, or the error to report. Opening a link is a desktop
// concern, so a client wired without an opener — the CLI in a container, a test — says
// so rather than silently doing nothing.
func (c *Client) opener() (URLOpener, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}

	if c.deps.URLs == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"client was built without a URL opener")
	}

	return c.deps.URLs, nil
}

// ModSourceLink returns every web location an installed mod can be fetched from, and
// what a user must do by hand. The URLs are kept rather than rebuilt from
// source-specific data because "where did this come from" has to outlive the plugin
// that knew it.
func (c *Client) ModSourceLink(ctx context.Context, modID string) (*domain.ModSourceLink, error) {
	mod, err := c.GetMod(ctx, modID)
	if err != nil {
		return nil, err
	}

	link := mod.SourceLink()

	return &link, nil
}

// ModSourceLinks returns the web locations of every installed mod, keyed by mod
// id. It is one call because a share, a settings screen and an update check all
// need the whole set and none of them want to loop.
func (c *Client) ModSourceLinks(ctx context.Context) (map[string]domain.ModSourceLink, error) {
	mods, err := c.ListMods(ctx)
	if err != nil {
		return nil, err
	}

	links := make(map[string]domain.ModSourceLink, len(mods))
	for i := range mods {
		links[mods[i].ID] = mods[i].SourceLink()
	}

	return links, nil
}

// OpenModSource opens an installed mod's web location in the user's browser. target
// selects which link and takes the same words the CLI takes: "source", "download",
// "update" or "auto". A mod with no link of that kind is an error, not a silent
// no-op; a mod with no links at all returns the fallback advice.
func (c *Client) OpenModSource(
	ctx context.Context,
	modID, target string,
) (*domain.ModLinkFallback, error) {
	kind, ok := domain.ParseModLinkTarget(target)
	if !ok {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"unknown link target %q: use source, download, update or auto", target)
	}

	mod, err := c.GetMod(ctx, modID)
	if err != nil {
		return nil, err
	}

	link := mod.SourceLink()
	fallback := link.FallbackKind()

	url := link.URLFor(kind)
	if url == "" {
		url = mod.LinkFor(kind)
	}

	if url == "" {
		return &fallback, appErrors.New(appErrors.ErrCodeNotFound,
			"%s has no %s link recorded", mod.ID, kind)
	}

	launcher, err := c.opener()
	if err != nil {
		return &fallback, err
	}

	if err := launcher.OpenURL(ctx, url); err != nil {
		return &fallback, err
	}

	return &fallback, nil
}

// WriteModManifest rewrites a mod's manifest from what the manager knows about it and
// returns the path written. A mod installed from a source the manager can no longer
// reach still documents its origin, requirements and hooks. It overwrites, because
// refreshing a record is the point; a hand-written manifest for a mod the manager
// has never seen is untouched.
func (c *Client) WriteModManifest(ctx context.Context, modID string) (string, error) {
	mod, err := c.GetMod(ctx, modID)
	if err != nil {
		return "", err
	}

	if mod.LocalPath == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidMod,
			"%s has no folder on disk to write a manifest into", mod.ID)
	}

	return manifest.Write(mod.LocalPath, manifest.FromMod(mod))
}
