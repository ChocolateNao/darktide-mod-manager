package domain

import (
	"path/filepath"
	"strings"
)

// CleanPath normalises a path for comparison, storage and display: redundant
// separators collapse, "." and ".." resolve, and backslashes become forward
// slashes so a Windows path still matches after the config moves to Linux.
//
// Backslashes are folded even on Unix, where they are legal filename characters.
// That is safe because nothing here opens a file: file operations take the raw
// path, while this feeds comparison, keying and display.
//
// Case is never folded, because a case-folded path cannot be used to open one.
func CleanPath(p string) string {
	if p == "" {
		return ""
	}
	// filepath.Clean does not treat "\" as a separator on every host, so fold
	// separators ourselves before cleaning.
	normalized := strings.ReplaceAll(p, `\`, "/")
	// filepath.Clean turns "" into "."; a bare volume name ("C:/") must keep its
	// root, which FromSlash/Clean preserve on Windows.
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(normalized)))
}

// JoinPath joins path elements and normalises the result.
func JoinPath(elems ...string) string {
	parts := make([]string, 0, len(elems))
	for _, e := range elems {
		if e != "" {
			parts = append(parts, e)
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return CleanPath(filepath.Join(parts...))
}

// EqualFold compares two strings case-insensitively using Unicode simple case
// folding, which is the right semantic for tag and source-ID comparisons.
func EqualFold(a, b string) bool {
	return strings.EqualFold(a, b)
}

// PathsEqual reports whether two paths point at the same location under
// case-insensitive comparison: correct on Windows and macOS, deliberately lenient
// on Linux. Use StrictPathsEqual when a case difference is a difference.
func PathsEqual(a, b string) bool {
	return strings.EqualFold(CleanPath(a), CleanPath(b))
}

// StrictPathsEqual compares paths with case sensitivity, matching Linux
// semantics.
func StrictPathsEqual(a, b string) bool {
	return CleanPath(a) == CleanPath(b)
}

// PathWithin reports whether child is parent or lives below it. It compares on
// separator boundaries, so "/a/bc" is not considered to be inside "/a/b". A
// relative child is never inside an absolute parent.
func PathWithin(parent, child string) bool {
	p, c := CleanPath(parent), CleanPath(child)
	if p == "" || c == "" {
		return false
	}

	if strings.EqualFold(p, c) {
		return true
	}

	prefix := p
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	if len(c) <= len(prefix) {
		return false
	}

	if !strings.HasPrefix(strings.ToLower(c), strings.ToLower(prefix)) {
		return false
	}
	// Guard against root-only parents like "/" where prefix becomes "//".
	return strings.HasPrefix(c, "/") == strings.HasPrefix(p, "/")
}

// ModStoragePath returns the directory holding a mod's version folder:
// <storage>/<gameID>/<modID>/<version>. Every segment is sanitised so a hostile id
// cannot escape the root; the game segment keeps two games' same-id mods apart.
func ModStoragePath(storageRoot, gameID, modID, version string) string {
	return JoinPath(
		storageRoot,
		SanitizeSegment(gameID),
		SanitizeSegment(modID),
		SanitizeSegment(version),
	)
}

// ModVersionRoot is the per-mod parent of a payload folder, holding every
// installed version of one mod. Removing it with the last version keeps the mods
// folder from filling with empty directories.
func ModVersionRoot(modsRoot, modID string) string {
	return JoinPath(modsRoot, SanitizeSegment(modID))
}

// SanitizeSegment reduces an arbitrary string to a single safe path segment:
// separators, drive letters, dot segments and characters illegal on Windows are
// removed, and an empty result becomes "_".
func SanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}

	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			// Drop separators, colons, wildcards and anything else.
		}
	}

	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "_"
	}

	return out
}
