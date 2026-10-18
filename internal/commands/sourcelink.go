package commands

import (
	"slices"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

// SourceLinkCommands answers the question a mod screen has to ask about every
// mod: "where does this thing come from, and how do I get it again?".
//
// Not every source can be downloaded from programmatically — Nexus hands files
// only to a signed-in client, and some sources change a mod's version without
// changing anything the manager can fetch. For those the link is the interface,
// so it has to be readable, refreshable and openable.
type SourceLinkCommands struct {
	*base
}

// NewSourceLinkCommands builds the source-link command set.
func NewSourceLinkCommands(deps Deps) *SourceLinkCommands {
	return &SourceLinkCommands{base: newBase(deps)}
}

// newSourceLinkCommands builds the set over a shared scope.
func newSourceLinkCommands(b *base) *SourceLinkCommands {
	return &SourceLinkCommands{base: b}
}

// ModSourceLink returns the stored links for one mod, or nil when the mod has
// none: a mod installed from a local folder genuinely has no page on the web,
// and an empty object renders better in a frontend than an error.
func (c *SourceLinkCommands) ModSourceLink(modID string) (*api.ModSourceLink, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	link, err := client.ModSourceLink(ctx, strings.TrimSpace(modID))
	if err != nil {
		return nil, c.fail(err, "read the source link of %q", modID)
	}

	return link, nil
}

// ModSourceLinks returns the links of every installed mod in one call. A mod
// screen renders a table; fetching per row would be a request storm.
func (c *SourceLinkCommands) ModSourceLinks() (map[string]api.ModSourceLink, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	links, err := client.ModSourceLinks(ctx)
	if err != nil {
		return nil, c.fail(err, "read mod source links")
	}

	return links, nil
}

// OpenModSource opens a mod's page in the browser.
//
// target names which link to open — the mod page, the download, or the update
// page — and "auto" lets the manager choose. The fallback is returned alongside
// the error so a frontend can still say what to do by hand.
func (c *SourceLinkCommands) OpenModSource(modID, target string) (*api.ModLinkFallback, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	fallback, err := client.OpenModSource(ctx, strings.TrimSpace(modID), strings.TrimSpace(target))
	if err != nil {
		return fallback, c.fail(err, "open the %s link of %q", targetOrAuto(target), modID)
	}

	return fallback, nil
}

// linkTargets is the order a user reads the choices in: the mod's page first,
// then its download, then the page to check for a newer version. It is a package
// variable so the frontend receives a copy rather than something it can rewrite.
var linkTargets = []string{"auto", "source", "download", "update"}

// LinkTargets lists the link kinds a frontend may offer.
func (c *SourceLinkCommands) LinkTargets() []string {
	return slices.Clone(linkTargets)
}

// WriteModManifest writes the manifest that describes an installed mod, using
// everything the database knows about it.
//
// This is the manual-update path: a mod whose source cannot be fetched
// programmatically is upgraded by its author publishing a new archive, and the
// manifest is how the next install of that archive knows what it is.
func (c *SourceLinkCommands) WriteModManifest(modID string) (string, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return "", err
	}
	defer cancel()

	path, err := client.WriteModManifest(ctx, strings.TrimSpace(modID))
	if err != nil {
		return "", c.fail(err, "write the manifest of %q", modID)
	}

	return path, nil
}

// targetOrAuto keeps a log message readable when the caller did not choose.
func targetOrAuto(target string) string {
	target = strings.TrimSpace(target)
	if target == "" || target == "auto" {
		return "source"
	}

	return target
}
