package commands

import (
	"slices"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

// SourceLinkCommands answers what a mod screen asks about every mod: where it
// comes from and how to get it again. Some sources serve files only to a signed-in
// client, so the link itself is the interface: readable, refreshable, openable.
type SourceLinkCommands struct {
	*base
}

// NewSourceLinkCommands builds the source-link command set.
func NewSourceLinkCommands(deps Deps) *SourceLinkCommands {
	return &SourceLinkCommands{base: newBase(deps)}
}

func newSourceLinkCommands(b *base) *SourceLinkCommands {
	return &SourceLinkCommands{base: b}
}

// ModSourceLink returns the stored links for one mod, or nil when it has none: a
// locally installed mod has no page, and an empty object renders better than an error.
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

// ModSourceLinks returns the links of every installed mod in one call: a mod
// screen renders a table, and fetching per row would be a request storm.
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

// OpenModSource opens a mod's page in the browser. target names the link to open
// ("auto" lets the manager choose); the fallback is returned with the error so a
// frontend can still say what to do by hand.
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

// linkTargets is the order a user reads the choices in: page, download, update
// page. It is a package variable so LinkTargets hands out a clone.
var linkTargets = []string{"auto", "source", "download", "update"}

// LinkTargets lists the link kinds a frontend may offer.
func (c *SourceLinkCommands) LinkTargets() []string {
	return slices.Clone(linkTargets)
}

// WriteModManifest writes the manifest that describes an installed mod from
// everything the database knows. It is the manual-update path: when the source
// cannot be fetched, the manifest tells the next install what the archive is.
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
