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

// The v3 API nests three resources: mod, mod file (an update chain), and the
// versions inside each file. The manager installs one *file* at a version, so
// those three are the vocabulary here.
//
// Two payload conventions shape every struct below: almost every endpoint nests
// its payload under "data" (the dependency and download-repacked responses do
// not, and a mismatch decodes to a zero value rather than an error), and ids are
// strings even when plainly numeric — kept unparsed so a widened id cannot make a
// mod uninstallable.

// modScoped is what GET /games/{game_domain}/mods/{game_scoped_id} returns: four
// fields, three of them ids.
//
// v3's single-mod endpoint is deliberately thin — no summary, author, category or
// timestamp — so a mod's author comes from the trending feed or from nowhere.
type modScoped struct {
	ID           string `json:"id"`
	GameScopedID string `json:"game_scoped_id"`
	GameID       string `json:"game_id"`
	Name         string `json:"name"`
}

// fileAggregate is a mod file with the counts the API aggregates across its
// versions (ModFileWithAggregates).
//
// IsActive is the API's own answer to "is there anything installable here", so it
// is preferred over re-deriving that from the categories.
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
// Position arrives as a decimal string, kept verbatim and ordered through
// positionValue. IsPrimary is absent more often than not and defaults to false.
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
// It carries no id at all — only the canonical mod page URL, which
// scopedIDFromPageURL parses to recover the game-scoped id. That is the only way
// a feed entry becomes installable.
type trendingMod struct {
	Name       string `json:"name"`
	Author     string `json:"author"`
	Summary    string `json:"summary"`
	PictureURL string `json:"picture_url"`
	ModPageURL string `json:"mod_page_url"`
}

// minimalMod is the trimmed mod the dependency endpoints embed in a target
// (MinimalMod) — the only place a dependency names its owning mod.
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

// fileWithMod is a dependency's target: the mod file plus its owning mod
// (ModFileWithMod), which together are enough to build a manager id.
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
// The "data" wrapper is load-bearing: decoding into a bare fileAggregate yields a
// zero value, and a zero file has an empty id, which reads as "no such file".
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
// Read by the keyed download path, which needs the version's game-scoped id: that
// is the legacy file number the v1 download endpoint addresses a file by, and it
// appears nowhere else in the manager's record of a mod.
type versionEnvelope struct {
	Data fileVersion `json:"data"`
}

// versionByScopedIDEnvelope is
// GET /games/{game_domain}/mod-file-versions/{game_scoped_id}.
//
// The one version read that also names its mod file: a link carries the legacy
// file number and nothing else, and the read-by-id endpoint never names the file,
// so this is the only shape that yields the pair a manager id is built from.
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
// GET /v1/games/{domain}/mods/{mod}/files/{file}/download_link answers with a
// JSON array of mirrors under the legacy name, short_name and URI fields. The URI
// is a signed content-host address carrying its own expiry, so it is fetched with
// no credentials attached.
type downloadLinkResponse []downloadLinkMirror

// downloadLinkMirror is one content host offering a file.
type downloadLinkMirror struct {
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	URI       string `json:"URI"`
}

// dependencyResponse is GET /mod-file-versions/{id}/dependencies.
//
// — deliberately without the "data" wrapper almost every other endpoint has.
// Per-range version bounds are unmodelled: the manager only needs *which* mod file
// is required, and bounds cannot make a dependency disappear.
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
// Like the dependency endpoints this response has no "data" wrapper. The URL is
// short-lived and points at a content host, not the API, so it must not be
// requested with the API key attached.
type downloadResponse struct {
	DownloadURL string `json:"download_url"`
	ExpiresAt   string `json:"expires_at"`
}

// modsBatchRequest is the body of POST /mods/batch, used to check a credential.
// An id that does not exist contributes no row, so a working key gets a 200 and an
// empty list rather than a 404 that could also mean "hidden key".
type modsBatchRequest struct {
	ModIDs []string `json:"mod_ids"`
}

// modContext carries what a mod's identity and presentation need. File and
// version responses name no mod, so the name comes from the mod fetch and is
// threaded through rather than rediscovered.
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

// pageURL renders the canonical mod page address, the same form an nxm link is
// built from, and is published as release_url so the two agree.
func (c modContext) pageURL() string {
	if c.scopedID == "" || c.gameDomain == "" {
		return ""
	}

	return "https://www.nexusmods.com/" + c.gameDomain + "/mods/" + c.scopedID
}

// locationURL is the stable, non-fetchable locator published as the mod's
// download_url.
//
// The cache hashes a download location plus a version, so something
// location-shaped must exist before anything is downloaded, and v3 has no such URL
// until a POST mints one. This names the minting endpoint rather than pretending
// to be the archive's address, and changes exactly when the version does.
func (c modContext) locationURL(versionID string) string {
	if strings.TrimSpace(versionID) == "" {
		return ""
	}

	return DefaultAPIBase + "/mod-file-versions/" + versionID + "/download-repacked"
}

// versionCategory is what the manager makes of a version's category.
//
// The distinction that matters is not "installable or not" but "the API said no"
// versus "the API said something this build has never heard of".
type versionCategory int

const (
	// categoryInstallable is a category the API documents as holding a payload a
	// user could install: main, update, optional or miscellaneous.
	categoryInstallable versionCategory = iota

	// categorySuperseded is old_version, a release kept for history. Installing it in
	// place of the current one is the mistake the category check exists to prevent.
	categorySuperseded

	// categoryUnknown covers anything unrecognised, including a blank or a category
	// introduced since this was written. It is not a refusal: rejecting everything
	// unrecognised would make the manager silently useless the first time the site adds
	// a category, whereas offering it fails loudly if it truly is not installable.
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
// "No downloadable version" alone leaves the reader nothing to act on — the fix
// is in this build or in the link, and only the categories say which. Duplicates
// collapse and order is fixed so one list always reads the same.
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
// An unparseable position reports false and callers read that as "no opinion"
// rather than zero, so a file with odd positions falls back to upload order.
//
// Non-finite values are refused too: ParseFloat accepts "NaN", every comparison
// against it is false, and pickVersion would then keep the first candidate seen —
// the opposite of "newest wins", silently.
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
// Newest wins, not primary: a primary version is a mod page's default download and
// can be years old, so returning it would mean the update check finds nothing.
// The flag is still recorded in SourceData.
//
// Superseded categories are skipped first — a file's newest version is sometimes a
// removed entry whose install would fail — but an unrecognised category is not
// skipped (see categoryUnknown) so a Nexus-side change cannot empty the set.
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
			// A usable position beats upload time: it is the API's own ordering,
			// whereas an old file re-uploaded makes a timestamp lie.
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
// cannot: a missing upload date must not stop a mod being offered, since the date
// is presentation and the version id is identity.
func parseStamp(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}

	return parsed
}

// unversioned is stored for a file version that carries no version string; 0.0.0
// is non-empty to persist and sorts below every real version.
const unversioned = "0.0.0"

// modFor converts a mod context plus one file and its chosen version into a
// domain.Mod.
//
// Identity is the (game-scoped mod id, mod file id) pair, not either half: the
// file alone would not survive a shared link, the mod alone would collapse all of
// a mod's files into one entry. The pair is what an nxm link carries.
func modFor(ctx modContext, file fileAggregate, version fileVersion) *domain.Mod {
	scoped := ctx.scopedID
	if scoped == "" {
		// Fall back to the mod page the version itself names.
		scoped = strings.TrimSpace(version.GameScopedID)
	}

	id := ModID(scoped, file.ID)
	if id == "" {
		return nil
	}

	versionID := strings.TrimSpace(version.ID)
	if versionID == "" {
		// Nothing can be downloaded later without a version id.
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
		"mod_id":               ctx.scopedID,
		"file_id":              file.ID,
		"version_id":           versionID,
		"mod_uid":              ctx.uid,
		"game_id":              firstText(ctx.gameID, version.GameScopedID),
		domain.SourceDomainKey: ctx.gameDomain,
		"position":             strings.TrimSpace(version.Position),
		"uploaded_at":          strings.TrimSpace(version.UploadedAt),
		"category":             strings.TrimSpace(version.Category),
		"is_primary":           version.IsPrimary,
		"download_url":         ctx.locationURL(versionID),
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
	// Nexus download links need auth, so the release URL stands in as update reference.
	if du := ctx.locationURL(versionID); du != "" {
		mod.DownloadURL = ""
		mod.SourceData["download_url_candidate"] = du
	}

	return mod
}

// scopedIDFromPageURL recovers a game's mod id from a canonical mod page URL.
//
// The trending feed is the only public discovery endpoint and carries no id, so
// this is the only way to make one of its entries installable. A URL of an
// unexpected shape yields nothing rather than a guess.
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

// isDigits reports whether the string is plain decimal digits — the check that an
// id looks like an id, now that ids arrive as strings. Anything else is refused:
// this plugin never mints such ids, and accepting one would let a crafted id
// through.
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
// Exported because the CLI parses launcher links into the same pair; the two
// encodings must agree or a link installs something other than shareLink prints.
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
// Ranges in one definition are alternatives and separate definitions are
// separate requirements, but domain.Mod.Dependencies is one flat list, so every
// candidate is reported. That over-reports: the check reads the list as "all of
// these", so splitting alternatives across entries would declare an undeclared
// conflict. Deduped because two requirements commonly name the same file.
//
// Cross-game dependencies, and ones with no unresolvable id, are skipped: an id
// that cannot be built would install the wrong mod.
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
