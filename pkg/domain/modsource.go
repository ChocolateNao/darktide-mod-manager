package domain

import "strings"

// ModLinkTarget names which of a mod's stored links a caller wants.
//
// It exists because a mod can have several and they are not interchangeable: the
// page is where a human decides, the direct download is the file, and the update
// link is the newest version rather than the one installed. A source that
// requires the user's own credentials — Nexus Mods — has a page and no download
// at all, and "open the page" is the only correct answer there.
type ModLinkTarget string

const (
	// ModLinkAuto picks the best link available, preferring the one that gets
	// the bytes over the one that shows the page.
	ModLinkAuto ModLinkTarget = "auto"
	// ModLinkSource is the mod's page on the web.
	ModLinkSource ModLinkTarget = "source"
	// ModLinkDownload is a direct download, when the source offers one.
	ModLinkDownload ModLinkTarget = "download"
	// ModLinkUpdate points at the newest version rather than the installed one.
	ModLinkUpdate ModLinkTarget = "update"
)

// String implements fmt.Stringer.
func (t ModLinkTarget) String() string { return string(t) }

// Valid reports whether the target is one of the known kinds.
func (t ModLinkTarget) Valid() bool {
	switch t {
	case ModLinkAuto, ModLinkSource, ModLinkDownload, ModLinkUpdate:
		return true
	default:
		return false
	}
}

// ParseModLinkTarget reads a target from text, accepting the hyphenated
// spelling the CLI uses as well as the underscored one the JSON form uses.
//
// An unrecognised value is refused rather than defaulted: silently opening a
// different page than the one asked for is worse than an error message, because
// the user believes they are looking at the file the mod was installed from.
// Empty text means ModLinkAuto.
func ParseModLinkTarget(text string) (ModLinkTarget, bool) {
	target := ModLinkTarget(strings.ToLower(strings.TrimSpace(text)))
	if target == "" {
		return ModLinkAuto, true
	}

	target = ModLinkTarget(strings.ReplaceAll(string(target), "-", "_"))
	if !target.Valid() {
		return "", false
	}

	return target, true
}

// ModLinkFallback describes how a mod can be obtained without this manager.
//
// The values are the same words a share's SourceLinks carry, so the advice a
// recipient is given after refusing a peer-to-peer transfer and the advice a
// user is given when a source stops being scriptable are the same sentence.
type ModLinkFallback string

const (
	// ModFallbackNone means there is nowhere to go: the mod was installed from a
	// local folder and never had a web origin.
	ModFallbackNone ModLinkFallback = P2PSourceFallbackNone
	// ModFallbackDownload means a direct file can be fetched.
	ModFallbackDownload ModLinkFallback = P2PSourceFallbackDownload
	// ModFallbackBrowse means a page has to be opened and the right file chosen
	// by hand.
	ModFallbackBrowse ModLinkFallback = P2PSourceFallbackBrowse
	// ModFallbackUpdate means only a "newest version" link is known, so the user
	// has to find the version themselves.
	ModFallbackUpdate ModLinkFallback = P2PSourceFallbackUpdate
)

// Describe returns the sentence a CLI prints and a UI shows next to the link.
// It describes the next step rather than repeating the URLs, because the point
// is to tell a user what they still have to do by hand.
func (f ModLinkFallback) Describe() string {
	switch f {
	case ModFallbackDownload:
		return "download the archive directly from this link"
	case ModFallbackBrowse:
		return "open the mod's page and download the version by hand"
	case ModFallbackUpdate:
		return "open the newest release and pick the version to install"
	case ModFallbackNone:
		return "this mod has no web location recorded"
	default:
		return ""
	}
}

// FallbackKind classifies what the stored links make possible.
//
// The order is the same one P2PSourceLinkForMod uses: a direct download beats a
// page to visit, because "fetch this exact file" is more precise than "go and
// find something suitable".
func (l ModSourceLink) FallbackKind() ModLinkFallback {
	switch {
	case l.DownloadURL != "":
		return ModFallbackDownload
	case l.SourceURL != "":
		return ModFallbackBrowse
	case l.UpdateURL != "":
		return ModFallbackUpdate
	default:
		return ModFallbackNone
	}
}

// URLFor returns the link for a target, or an empty string when the source has
// none of that kind.
//
// An empty return is meaningful rather than an error: Nexus genuinely has no
// unauthenticated download URL, and a caller asking for one needs to be told
// "there is not one" rather than handed the mod's page as a substitute that
// looks like it would have worked.
func (l ModSourceLink) URLFor(target ModLinkTarget) string {
	switch target {
	case ModLinkSource:
		return l.SourceURL
	case ModLinkDownload:
		return l.DownloadURL
	case ModLinkUpdate:
		return l.UpdateURL
	case ModLinkAuto:
		for _, candidate := range []string{l.DownloadURL, l.SourceURL, l.UpdateURL} {
			if candidate != "" {
				return candidate
			}
		}

		return ""
	default:
		return ""
	}
}

// SourceLink projects an installed mod onto its web locations.
//
// This is the "keep the link" half of the contract; LinkFor below is the "or be
// able to reconstruct it" half, and both read the same three stored fields so
// there is one answer to "where does this mod live".
func (m *Mod) SourceLink() ModSourceLink {
	if m == nil {
		return ModSourceLink{}
	}

	link := ModSourceLink{
		ModID:       m.ID,
		SourceID:    m.SourceID,
		SourceURL:   m.SourceURL,
		DownloadURL: m.DownloadURL,
		UpdateURL:   m.UpdateURL,
	}

	link.Fallback = string(link.FallbackKind())

	return link
}

// LinkFor returns the mod's web location for a target, falling back to the
// other links when the requested kind was never recorded.
//
// The fallback is what makes the feature useful for mods installed before the
// manager kept links: a mod from Nexus has a page and no download, and asking for
// its download link should still produce the page rather than nothing. It is not
// a silent substitution, though — callers that care which kind they got read
// SourceLink().URLFor and see the empty string.
func (m *Mod) LinkFor(target ModLinkTarget) string {
	if m == nil {
		return ""
	}

	link := m.SourceLink()
	if url := link.URLFor(target); url != "" {
		return url
	}

	for _, candidate := range fallbackLinkOrder(target) {
		if url := link.URLFor(candidate); url != "" {
			return url
		}
	}

	return ""
}

// fallbackLinkOrder lists the targets to try after the requested one came up
// empty.
//
// ModLinkAuto has already tried everything by the time it reaches here, so it
// falls back to the reverse preference: when the caller could not say what it
// wanted, a page that lets a human choose beats a download that silently hands
// them a version they may not want.
func fallbackLinkOrder(target ModLinkTarget) []ModLinkTarget {
	if target == ModLinkAuto {
		return []ModLinkTarget{ModLinkSource, ModLinkUpdate, ModLinkDownload}
	}

	return []ModLinkTarget{target, ModLinkAuto}
}
