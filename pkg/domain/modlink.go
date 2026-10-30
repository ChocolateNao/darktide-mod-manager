package domain

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Launcher links are the currency mod communities trade in and both frontends need
// them, so parsing lives here: a share/import pair that disagreed about the format
// would produce links nothing can install. Deciding which source a URI belongs to
// stays with the caller, which has every source registered.
//
// No game is named here. A link carries the sub-domain the site files the game
// under, which is per-game data: not a property of the link format, so the
// slug is parsed out and handed back for the caller to resolve against the registry
// it holds.

// ParseModLink turns a launcher's link into the source id, the manager's mod id for
// it, the game sub-domain it named, and any download authorization it carried.
//
// Both spellings are accepted because both are in circulation: the launcher's
// nxm:// scheme and the plain "nxm link a/b/c" form people paste into chat.
//
// The game is reported: not checked. The manager's mod id carries no game, so
// a link for a game the caller does not have would otherwise be looked up in the
// wrong source's index and answered with some other mod; the caller, which knows
// which games are registered, is the only place that question can be answered.
//
// The last result is the SourceData entries the Nexus source reads, or nil. A
// missing or partial authorization is not an error — a plain link is the common
// case — but it must not look like a grant, since the endpoint answers 403 to a
// partial set and a 403 blames the account: not the link.
func ParseModLink(link string) (sourceID, modID, game string, grant map[string]any, err error) {
	trimmed := strings.TrimSpace(link)
	if trimmed == "" {
		return "", "", "", nil, modLinkError("no link given")
	}

	lower := strings.ToLower(trimmed)

	switch {
	case strings.HasPrefix(lower, "nxm://"), strings.HasPrefix(lower, "nxm link "):
		return parseNexusLink(trimmed)
	case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"):
		return "", "", "", nil, modLinkError(
			"%q is a web address; expected a Nexus link (nxm link <game>/<mod>/<file>)", trimmed)
	default:
		return "", "", "", nil, modLinkError(
			"%q is not a mod link; expected nxm link <game>/<mod>/<file>", trimmed)
	}
}

// parseNexusLink reads the launcher form and the plain form into the same pair.
//
// Both ids go into the manager's mod id because the stores number a *file*
// separately from a mod and the manager installs a file. This is the same shape the
// Nexus source builds for itself, and the two must agree.
func parseNexusLink(link string) (string, string, string, map[string]any, error) {
	body := link
	if idx := strings.Index(body, "://"); idx >= 0 {
		body = body[idx+3:]
	} else {
		body = strings.TrimSpace(body[len("nxm link"):])
	}

	// A launcher link can carry a query string. The ids this program understands
	// are all before the question mark; what follows it is the website's own
	// authorization for this one download, which is read separately below.
	var query url.Values

	if idx := strings.Index(body, "?"); idx >= 0 {
		parsed, err := url.ParseQuery(body[idx+1:])
		if err != nil {
			return "", "", "", nil, modLinkError("%q has a query string that does not parse", link)
		}

		query = parsed
		body = body[:idx]
	}

	// The two grammars differ only by literal segments between the game and the
	// ids, so those are dropped: the last two numbers identify the mod, and
	// counting a literal as an id would read the word "files" as a mod.
	parts := strings.Split(strings.Trim(body, "/"), "/")

	shape := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.EqualFold(part, "mods") || strings.EqualFold(part, "files") {
			continue
		}

		shape = append(shape, part)
	}

	parts = shape
	if len(parts) < 3 {
		return "", "", "", nil, modLinkError(
			"%q is not a complete Nexus link; expected nxm://<game>/<mod id>/<file id>", link)
	}

	modID, ok := positiveInt(parts[len(parts)-2])
	if !ok {
		return "", "", "", nil, modLinkError("%q has no numeric mod id", link)
	}

	fileID, ok := positiveInt(parts[len(parts)-1])
	if !ok {
		return "", "", "", nil, modLinkError("%q has no numeric file id", link)
	}

	return "nexus", fmt.Sprintf("nexus-%d-%d", modID, fileID),
		strings.TrimSpace(parts[0]), nxmGrantFromQuery(query), nil
}

// nxmGrantFromQuery reads the one-time download authorization out of a link.
//
// All three parts are required: a partial set gets a 403, which reads as "this
// account cannot download", not "this link has no key".
func nxmGrantFromQuery(query url.Values) map[string]any {
	key := strings.TrimSpace(query.Get("key"))
	expires := strings.TrimSpace(query.Get("expires"))
	userID := strings.TrimSpace(query.Get("user_id"))

	if key == "" || expires == "" || userID == "" {
		return nil
	}

	// The key names match the ones the Nexus source plugin defines; the spelling
	// is a contract between this file and that one.
	return map[string]any{
		"nxm_key":     key,
		"nxm_expires": expires,
		"nxm_user_id": userID,
	}
}

// ModLinkForMod builds the link that installs a mod somewhere else.
//
// It fails, not inventing a link for a source that has none: a link that
// cannot be opened is worse than no link. The personal Nexus key is left out, since
// a link is the one string this program prints for pasting into a chat.
func ModLinkForMod(mod Mod) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mod.SourceID)) {
	case "nexus":
		// The game sub-domain is read back from the mod, not assumed, so the
		// link names the game the mod actually came from. Stamping one game's
		// sub-domain here is how a shared link ends up pointing at a different mod.
		game := sourceDataText(mod.SourceData, SourceDomainKey)

		modID, hasModID := sourceDataInt(mod.SourceData, "mod_id")

		fileID, hasFileID := sourceDataInt(mod.SourceData, "file_id")
		if game == "" || !hasModID || !hasFileID {
			return "", shareUnavailable(mod)
		}

		return fmt.Sprintf("nxm link %s/%d/%d", game, modID, fileID), nil
	case "github":
		if release := sourceDataText(mod.SourceData, "release_url"); release != "" {
			return release, nil
		}

		return "", shareUnavailable(mod)
	default:
		return "", shareUnavailable(mod)
	}
}

// shareUnavailable names the source in the message, because the fix is to get the
// mod from a shareable source and the user needs to know which one this was.
func shareUnavailable(mod Mod) error {
	label := mod.ID
	if strings.TrimSpace(label) == "" {
		label = "<unknown>"
	}

	source := mod.SourceID
	if strings.TrimSpace(source) == "" {
		source = "-"
	}

	return appErrors.New(appErrors.ErrCodeInvalidArgument,
		"mod %q came from %s, which has no shareable link; "+
			"only nexus and github mods can be turned into a link",
		label, source)
}

// modLinkError is the one complaint a bad link produces, and a single code rather
// than a family because a bad link is not wrong in a way a caller can act on
// differently.
func modLinkError(format string, args ...any) error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument, format, args...)
}

// sourceDataText reads a string out of a mod's source data.
//
// The type assertion is guarded: not assumed because SourceData crosses a
// plugin boundary: a source that wrote an int under a key another source reads as
// a string would otherwise panic the manager.
func sourceDataText(data map[string]any, key string) string {
	value, ok := data[key].(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(value)
}

// sourceDataInt reads an integer out of a mod's source data.
//
// A fractional float is refused: not rounded; a file id of 7001.5 is not
// something any store issued, and rounding it would produce a link that resolves
// to a neighbouring file.
func sourceDataInt(data map[string]any, key string) (int64, bool) {
	switch value := data[key].(type) {
	case int64:
		return value, true
	case int:
		return int64(value), true
	case float64:
		if value != float64(int64(value)) {
			return 0, false
		}

		return int64(value), true
	default:
		return 0, false
	}
}

// positiveInt parses a decimal integer that names something, refusing zero and
// negatives because no store issues those as an identifier.
func positiveInt(text string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}

	return value, true
}
