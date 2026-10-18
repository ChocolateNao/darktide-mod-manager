package domain

import "strings"

// ModLinkTarget names which of a mod's stored links a caller wants.
//
// A mod can have several and they are not interchangeable: the page is where a
// human decides, the download is the file, the update link is the newest version.
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

// ParseModLinkTarget reads a target from text, accepting both the hyphenated CLI
// spelling and the underscored JSON one. An unrecognised value is refused rather
// than defaulted, because opening a different page than the one asked for is worse
// than an error. Empty text means ModLinkAuto.
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
// The values are the words a share's SourceLinks carry, so the advice given after
// refusing a peer-to-peer transfer and the advice given when a source stops being
// scriptable are the same sentence.
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
// It describes the next step rather than repeating the URLs.
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
// The order matches P2PSourceLinkForMod: a direct download beats a page to visit.
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
// An empty return is meaningful rather than an error: a caller that gets the mod's
// page instead would think it had been handed a download URL.
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
// This is the "keep the link" half of the contract; LinkFor below is the "or
// reconstruct it" half, and both read the same three stored fields.
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
// The fallback covers mods installed before the manager kept links: a Nexus mod
// has a page and no download, and asking for the download should produce the page.
// It is not a silent substitution — callers that care read SourceLink().URLFor.
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
// ModLinkAuto has already tried everything by now, so it uses the reverse
// preference: when the caller could not say what it wanted, a page that lets a
// human choose beats a download that hands over a version they may not want.
func fallbackLinkOrder(target ModLinkTarget) []ModLinkTarget {
	if target == ModLinkAuto {
		return []ModLinkTarget{ModLinkSource, ModLinkUpdate, ModLinkDownload}
	}

	return []ModLinkTarget{target, ModLinkAuto}
}
