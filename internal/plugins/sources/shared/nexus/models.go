package nexus

import (
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// The Nexus Mods v3 API is shaped around three nested resources: a mod, the mod
// files on it (each an update chain), and the versions inside each file. The
// manager installs one *file* at a version, so those three are the vocabulary
// here.
//
// Two conventions in the payloads are worth stating once, because they shape
// every struct below:
//
//   - Almost every endpoint nests its payload under "data". The four dependency
//     endpoints and the download-repacked response do not. Decoding a response
//     into the wrong shape yields a zero value rather than an error, and a zero
//     value here reads as "this mod has no files" or "no dependencies", which is
//     a wrong answer rather than a missing one. Every envelope below therefore
//     names the wrapper it actually has.
//   - Every id in v3 is a *string*, including the ones that are plainly numbers.
//     They are kept as strings rather than parsed, because the API is free to
//     widen them and a mod id that no longer parses is a mod id that cannot be
//     installed.

// modScoped is what GET /games/{game_domain}/mods/{game_scoped_id} returns.
//
// The whole of it is four fields, and three of them are ids. Notably there is
// no summary, no author, no category and no timestamp: v3's single-mod endpoint
// is deliberately thin, and a caller wanting more has to go through the batch
// endpoint with the composite id. Author in particular is unavailable here, so
// an installed Nexus mod reports no author unless the caller already knows one
// from the trending feed.
type modScoped struct {
	ID           string `json:"id"`
	GameScopedID string `json:"game_scoped_id"`
	GameID       string `json:"game_id"`
	Name         string `json:"name"`
}

// fileAggregate is a mod file with the counts the API aggregates across its
// versions (ModFileWithAggregates).
//
// IsActive is the API's own statement that the file holds at least one version
// in an installable category, which is exactly the question "is there anything to
// install here", so it is preferred over re-deriving that from the categories.
type fileAggregate struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	LastFileUploadedAt string `json:"last_file_uploaded_at"`
	VersionsCount      int    `json:"versions_count"`
	ArchivedCount      int    `json:"archived_count"`
	RemovedCount       int    `json:"removed_count"`
	IsActive           bool   `json:"is_active"`
}

// fileVersion is one version of a mod file (ModFileVersion).
//
// Position is declared as a decimal *string* rather than a number, so it is kept
// verbatim and ordered through positionValue. IsPrimary is not in the API's
// required list and defaults to false when absent.
type fileVersion struct {
	ID           string `json:"id"`
	Position     string `json:"position"`
	GameScopedID string `json:"game_scoped_id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Category     string `json:"category"`
	UploadedAt   string `json:"uploaded_at"`
	IsPrimary    bool   `json:"is_primary"`
}

// trendingMod is one entry of the public trending feed (TrendingMod).
//
// It carries no id at all — only a name, an author, a summary and the canonical
// URL of the mod page. The game-scoped id is recovered from that URL by
// scopedIDFromPageURL, which is the only way the feed can be turned into
// something installable.
type trendingMod struct {
	Name       string `json:"name"`
	Author     string `json:"author"`
	Summary    string `json:"summary"`
	PictureURL string `json:"picture_url"`
	ModPageURL string `json:"mod_page_url"`
}

// minimalMod is the trimmed mod the dependency endpoints embed inside a
// dependency target (MinimalMod). It is the one place a dependency's owning mod
// is named.
type minimalMod struct {
	ID           string      `json:"id"`
	GameScopedID string      `json:"game_scoped_id"`
	Name         string      `json:"name"`
	Game         minimalGame `json:"game"`
}

// minimalGame identifies the game a mod belongs to (MinimalGame).
type minimalGame struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DomainName string `json:"domain_name"`
}

// fileWithMod is a dependency's target: the mod file plus the mod that owns it
// (ModFileWithMod). Together with the mod's game-scoped id this is enough to
// build the dependency's manager id.
type fileWithMod struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Mod  minimalMod `json:"mod"`
}

// dlcTarget is one DLC satisfying a DLC dependency definition (ModFileVersionDlcTarget).
type dlcTarget struct {
	ID    string `json:"id"`
	DLCID string `json:"dlc_id"`
	Name  string `json:"name"`
}

// The envelopes. See the file comment for why each of these names its wrapper.

// filesEnvelope is GET /mods/{id}/files.
type filesEnvelope struct {
	Data struct {
		ModFiles []fileAggregate `json:"mod_files"`
	} `json:"data"`
}

// fileEnvelope is GET /mod-files/{id}.
//
// The single-file endpoint wraps its payload in "data" just like the list ones
// do. Decoding the body into a bare fileAggregate instead yields a zero value —
// which, because a zero file has an empty id, reads as "this file does not
// exist" for every file that does. The wrapper is named for the same reason the
// others are.
type fileEnvelope struct {
	Data fileAggregate `json:"data"`
}

// versionsEnvelope is GET /mod-files/{id}/versions.
type versionsEnvelope struct {
	Data struct {
		Versions []fileVersion `json:"versions"`
	} `json:"data"`
}

// trendingEnvelope is GET /games/{game_domain}/trending-mods.
type trendingEnvelope struct {
	Data struct {
		Mods []trendingMod `json:"mods"`
	} `json:"data"`
}

// modEnvelope is GET /games/{game_domain}/mods/{game_scoped_id}.
type modEnvelope struct {
	Data modScoped `json:"data"`
}

// versionEnvelope is GET /mod-file-versions/{id}.
//
// The single-version endpoint wraps its payload in "data" like the other single
// reads do. It is read by the keyed download path, which needs the version's
// game-scoped id: that id is the legacy file number the v1 download endpoint
// addresses a file by, and it appears nowhere else in the manager's record of
// a mod.
type versionEnvelope struct {
	Data fileVersion `json:"data"`
}

// versionByScopedIDEnvelope is
// GET /games/{game_domain}/mod-file-versions/{game_scoped_id}.
//
// It is the one version read that also names its mod file. The endpoint
// addresses a version by the legacy file number a website link carries, and
// answers with the v3 mod file id, which is what a mod's identity is built
// from; the version read by id does not name its file at all, so a link carrying
// the legacy number and nothing else cannot be turned into the pair the manager
// identifies a mod by without this third shape.
type versionByScopedIDEnvelope struct {
	Data struct {
		File struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"file"`
		fileVersion
	} `json:"data"`
}

// downloadLinkResponse is
// GET /v1/games/{domain}/mods/{mod}/files/{file}/download_link.
//
// The body is a JSON array of mirrors rather than a single URL, and the field
// names are the legacy ones: name, short_name, URI. The URI is a signed
// content-host address that carries its own expiry and needs no API key, so it
// is fetched with no credentials attached.
type downloadLinkResponse []downloadLinkMirror

// downloadLinkMirror is one content host offering a file.
type downloadLinkMirror struct {
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	URI       string `json:"URI"`
}

// dependencyResponse is GET /mod-file-versions/{id}/dependencies.
//
// There is deliberately no "data" wrapper here, unlike almost every other
// endpoint. The min and max version bounds inside each range are not modelled:
// the manager only needs to know *which* mod file is required, and the bounds
// cannot make a dependency disappear, so leaving them out cannot hide one.
type dependencyResponse struct {
	DependencyDefinitions    []dependencyDefinition    `json:"dependency_definitions"`
	DLCDependencyDefinitions []dlcDependencyDefinition `json:"dlc_dependency_definitions"`
}

type dependencyDefinition struct {
	ID     string            `json:"id"`
	Ranges []dependencyRange `json:"ranges"`
}

type dependencyRange struct {
	ID            string      `json:"id"`
	TargetModFile fileWithMod `json:"target_mod_file"`
}

type dlcDependencyDefinition struct {
	ID         string      `json:"id"`
	DLCTargets []dlcTarget `json:"dlc_targets"`
}

// downloadResponse is POST /mod-file-versions/{id}/download-repacked.
//
// Like the dependency endpoints this response has no "data" wrapper: the body is
// the schema. The URL it carries is short-lived (ExpiresAt) and points at a
// content host, not at the API, so it must not be requested with the API key
// attached.
type downloadResponse struct {
	DownloadURL string `json:"download_url"`
	ExpiresAt   string `json:"expires_at"`
}

// modsBatchRequest is the body of POST /mods/batch. It is used to check a
// credential: the endpoint requires an api key, and an id that does not exist
// simply contributes no row, so a key that works is answered with a 200 and an
// empty list rather than with a 404 that could also mean "hidden key".
type modsBatchRequest struct {
	ModIDs []string `json:"mod_ids"`
}

// modContext carries what a mod's identity and presentation need. The file and
// version responses carry no mod name at all, so the name has to come from the
// mod fetch and be threaded through the conversions rather than rediscovered.
type modContext struct {
	scopedID     string // game-scoped id: the number in a mod page URL and an nxm link
	uid          string // composite Mod.id
	gameID       string
	gameDomain   string
	name         string
	summary      string
	author       string
	thumbnailURL string
}

// pageURL renders the canonical address of a mod page, which is also the form an
// nxm link is built from. It is published as the mod's release_url so a shared
// link and a browsable address agree.
func (c modContext) pageURL() string {
	if c.scopedID == "" || c.gameDomain == "" {
		return ""
	}

	return "https://www.nexusmods.com/" + c.gameDomain + "/mods/" + c.scopedID
}

// locationURL is the stable, non-fetchable locator published as the mod's
// download_url.
//
// The cache keys an archive by hashing a download location plus a version, so
// something location-shaped has to exist before anything is downloaded. In v3
// there is no such URL: the real one is minted per download by a POST that has
// to happen first. This string therefore names the endpoint that mints it rather
// than pretending to be the archive's address, and it changes exactly when the
// version does, which is what makes the cache key correct.
func (c modContext) locationURL(versionID string) string {
	if strings.TrimSpace(versionID) == "" {
		return ""
	}

	return DefaultAPIBase + "/mod-file-versions/" + versionID + "/download-repacked"
}

// versionCategory is what the manager makes of a version's category.
//
// The distinction that matters is not "installable or not" but "the API said no"
// versus "the API said something this build has never heard of". Collapsing the
// two is what let a site change break every mod at once.
type versionCategory int

const (
	// categoryInstallable is a category the API documents as holding a payload a
	// user could install: main, update, optional or miscellaneous.
	categoryInstallable versionCategory = iota

	// categorySuperseded is old_version — a release kept for history. Installing
	// it in place of the current one is the mistake the category check exists to
	// prevent, so it is refused rather than merely ranked lower.
	categorySuperseded

	// categoryUnknown is anything else, including an empty category: a value
	// Nexus has introduced since this was written, or a version whose category
	// came back blank.
	//
	// Unknown is not treated as a refusal. A filter that rejects everything it
	// does not recognise makes the manager unusable the first time the site adds
	// a category, and it does so silently — the mod simply "has no downloadable
	// version" while a perfectly good grant sits in the user's hand. Offering the
	// version instead lets the download fail loudly and specifically if it really
	// is not installable, which is the better of the two failures.
	categoryUnknown
)

// classifyCategory sorts a category reported by the API into the three cases the
// manager acts on.
func classifyCategory(category string) versionCategory {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "main", "update", "optional", "miscellaneous":
		return categoryInstallable
	case "old_version":
		return categorySuperseded
	default:
		return categoryUnknown
	}
}

// describeCategories renders the categories present in a version list, for an
// error message.
//
// A message that says only "no downloadable version" leaves the reader with
// nothing to act on: the fix is either in this build or in a link, and which one
// is visible only in the categories. Duplicates are collapsed and the order is
// fixed so the same list always produces the same sentence.
func describeCategories(versions []fileVersion) string {
	seen := map[string]int{}
	order := make([]string, 0, 4)

	for _, candidate := range versions {
		name := strings.ToLower(strings.TrimSpace(candidate.Category))
		if name == "" {
			name = "(blank)"
		}

		if _, ok := seen[name]; ok {
			continue
		}

		seen[name] = 1

		order = append(order, name)
	}

	if len(order) == 0 {
		return "none — the file published no versions at all"
	}

	slices.Sort(order)

	return strings.Join(order, ", ")
}

// positionValue parses the decimal position the API publishes.
//
// A position that will not parse reports false, and callers treat that as "no
// opinion" rather than as zero, so a file whose positions all look unusual
// falls back to upload order instead of collapsing onto the first entry.
//
// The non-finite values are refused as well. strconv.ParseFloat accepts "NaN",
// "Inf" and "-Inf", and every comparison against NaN is false, so a NaN
// position would make pickVersion keep whichever candidate it saw first — the
// opposite of what "newest wins" means, and silently so.
func positionValue(raw string) (float64, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}

	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}

	return value, true
}

// pickVersion chooses the version a mod should be reported at.
//
// The newest version wins, not the primary one. A primary version is the default
// download for a mod page and can be years old, and the manager's update check
// compares the version it is told about against the one it has installed; if
// this returned the primary, an update would never be found. The primary flag is
// still recorded in SourceData.
//
// Versions in a category that the API documents as superseded are skipped before
// the comparison, because a file's newest version is sometimes a removed or
// archived entry and offering that would produce an install that fails. A
// category this build does not recognise is not skipped — see categoryUnknown —
// so that a change on Nexus's side cannot empty the candidate set.
func pickVersion(versions []fileVersion) (fileVersion, bool) {
	best := fileVersion{}
	found := false
	bestPos, bestHasPos := 0.0, false
	bestAt := time.Time{}

	for _, candidate := range versions {
		if classifyCategory(candidate.Category) == categorySuperseded {
			continue
		}

		if !found {
			best, found = candidate, true
			bestPos, bestHasPos = positionValue(candidate.Position)
			bestAt = parseStamp(candidate.UploadedAt)

			continue
		}

		pos, hasPos := positionValue(candidate.Position)
		switch {
		case hasPos && bestHasPos:
			if pos > bestPos {
				best, bestPos = candidate, pos
				bestAt = parseStamp(candidate.UploadedAt)
			}
		case hasPos != bestHasPos:
			// A version with a usable position beats one without: the position is
			// the API's own ordering, whereas upload time is a second opinion that
			// can be wrong when an old file was re-uploaded.
			if hasPos {
				best, bestPos, bestHasPos = candidate, pos, true
				bestAt = parseStamp(candidate.UploadedAt)
			}
		default:
			if parseStamp(candidate.UploadedAt).After(bestAt) {
				best = candidate
				bestAt = parseStamp(candidate.UploadedAt)
			}
		}
	}

	return best, found
}

// parseStamp reads one of the API's timestamps, reporting the zero time when it
// cannot be read. A missing timestamp must not stop a mod being offered: the
// upload date is presentation, the version id is identity.
func parseStamp(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}

	return parsed
}

// unversioned is the version recorded for a file version that carries no version
// string. The manager needs something non-empty to store, and 0.0.0 is the
// honest rendering of "unversioned" because it sorts below every real version.
const unversioned = "0.0.0"

// modFor converts a mod context plus one file and its chosen version into a
// domain.Mod.
//
// The mod's identity is the (game-scoped mod id, mod file id) pair rather than
// either half. The file alone would not survive a shared link, which carries
// both; the mod alone would collapse every file of one mod into a single entry,
// so a mod with a main file and an optional file would install both or neither.
// The pair is also exactly what an nxm link carries, so a shared link and a
// stored id are the same string.
func modFor(ctx modContext, file fileAggregate, version fileVersion) *domain.Mod {
	scoped := ctx.scopedID
	if scoped == "" {
		// The version knows which mod page it belongs to, which is the next best
		// answer when the mod fetch did not supply one.
		scoped = strings.TrimSpace(version.GameScopedID)
	}

	id := ModID(scoped, file.ID)
	if id == "" {
		return nil
	}

	versionID := strings.TrimSpace(version.ID)
	if versionID == "" {
		// Without a version id nothing can be downloaded later, so a file with no
		// usable version is not an offerable mod.
		return nil
	}

	mod := &domain.Mod{
		ID:          id,
		Name:        firstText(ctx.name, version.Name, file.Name),
		Version:     firstText(version.Version, unversioned),
		Author:      strings.TrimSpace(ctx.author),
		Description: strings.TrimSpace(ctx.summary),
		SourceID:    SourceID,
	}

	mod.Tags = append(mod.Tags, SourceID)
	if category := strings.TrimSpace(version.Category); category != "" {
		mod.Tags = append(mod.Tags, strings.ToLower(category))
	}

	mod.SourceData = map[string]any{
		"mod_id":       ctx.scopedID,
		"file_id":      file.ID,
		"version_id":   versionID,
		"mod_uid":      ctx.uid,
		"game_id":      firstText(ctx.gameID, version.GameScopedID),
		"game_domain":  ctx.gameDomain,
		"position":     strings.TrimSpace(version.Position),
		"uploaded_at":  strings.TrimSpace(version.UploadedAt),
		"category":     strings.TrimSpace(version.Category),
		"is_primary":   version.IsPrimary,
		"download_url": ctx.locationURL(versionID),
	}
	if file.Name != "" {
		mod.SourceData["file_name"] = file.Name
	}

	if file.VersionsCount > 0 {
		mod.SourceData["versions_count"] = file.VersionsCount
	}

	if file.LastFileUploadedAt != "" {
		mod.SourceData["last_file_uploaded_at"] = file.LastFileUploadedAt
	}

	if page := ctx.pageURL(); page != "" {
		mod.SourceData["release_url"] = page
	}

	if ctx.thumbnailURL != "" {
		mod.SourceData["thumbnail_url"] = ctx.thumbnailURL
	}

	if page := ctx.pageURL(); page != "" {
		mod.SourceURL = page
		mod.SourceData["source_url"] = page
	}
	// Nexus download links require authentication for many cases; store release URL as
	// update/source reference
	if du := ctx.locationURL(versionID); du != "" {
		mod.DownloadURL = ""
		mod.SourceData["download_url_candidate"] = du
	}

	return mod
}

// scopedIDFromPageURL recovers a game's mod id from a canonical mod page URL.
//
// The trending feed is the only public discovery endpoint and it carries no id,
// so this is the only way to turn one of its entries into something installable.
// The URL is the one the API itself published, so parsing it is reading the
// answer rather than guessing it — but a URL of an unexpected shape yields
// nothing, because an entry that cannot be resolved must not be guessed at.
func scopedIDFromPageURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false
	}

	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i, segment := range segments {
		if !strings.EqualFold(segment, "mods") || i+1 >= len(segments) {
			continue
		}

		candidate := strings.TrimSpace(segments[i+1])
		if isDigits(candidate) {
			return candidate, true
		}
	}

	return "", false
}

// isDigits reports whether the string is one or more plain decimal digits.
//
// The API's ids are strings, so the check that an id looks like an id is this
// one. A sign, padding or any other character is refused: ids this plugin mints
// never contain them, and quietly accepting one would let a crafted id through.
func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

// ModID renders the (game-scoped mod id, mod file id) pair as a manager mod id.
// Both halves must be present: an id missing either half could not be resolved
// back into a Nexus request, and inventing one would point at an arbitrary mod.
func ModID(scopedID, fileID string) string {
	scoped := strings.TrimSpace(scopedID)

	file := strings.TrimSpace(fileID)
	if !isDigits(scoped) || !isDigits(file) {
		return ""
	}

	return idPrefix + scoped + idSeparator + file
}

// ParseModID reverses ModID, reporting the game-scoped mod id and the file id.
//
// The id is exported because the CLI parses launcher links into the same pair
// and the two encodings have to agree: a link that resolved to a different id
// than the one shareLink prints would install something else.
func ParseModID(id string) (scopedID, fileID string, ok bool) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" || !strings.HasPrefix(trimmed, idPrefix) {
		return "", "", false
	}

	rest := strings.TrimPrefix(trimmed, idPrefix)

	scoped, file, found := strings.Cut(rest, idSeparator)
	if !found {
		return "", "", false
	}

	scoped, file = strings.TrimSpace(scoped), strings.TrimSpace(file)
	if !isDigits(scoped) || !isDigits(file) {
		return "", "", false
	}

	return scoped, file, true
}

// dependencyMods turns a dependency response into manager mod ids.
//
// Ranges inside one definition are alternatives and separate definitions are
// separate requirements, while domain.Mod.Dependencies is one flat list, so
// every candidate across every range is reported. That is a superset of what the
// API requires: the manager's dependency check treats the list as "all of these
// must be installed", so listing the alternatives of one requirement as several
// requirements would report a conflict the author did not declare. Deduplicating
// matters because two requirements commonly name the same file.
//
// A dependency on another game is skipped, and one whose owning mod has no id to
// resolve is skipped: an id that cannot be built would install the wrong mod.
func dependencyMods(resp dependencyResponse, gameDomain string) []string {
	seen := map[string]bool{}

	var out []string

	for _, definition := range resp.DependencyDefinitions {
		for _, rng := range definition.Ranges {
			target := rng.TargetModFile
			if gameDomain != "" && target.Mod.Game.DomainName != "" &&
				!strings.EqualFold(target.Mod.Game.DomainName, gameDomain) {
				continue
			}

			id := ModID(target.Mod.GameScopedID, target.ID)
			if id == "" || seen[id] {
				continue
			}

			seen[id] = true
			out = append(out, id)
		}
	}

	return out
}

// dlcNames lists the DLCs a version requires. DLCs are not mods, so they have no
// manager id and are reported as names for the user to read.
func dlcNames(resp dependencyResponse) []string {
	var out []string

	for _, definition := range resp.DLCDependencyDefinitions {
		for _, target := range definition.DLCTargets {
			if name := strings.TrimSpace(target.Name); name != "" {
				out = append(out, name)
			}
		}
	}

	return out
}

// firstText returns the first non-blank value, trimmed.
func firstText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}

	return ""
}
