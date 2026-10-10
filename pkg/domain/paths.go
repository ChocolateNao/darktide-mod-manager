package domain

import (
	"path/filepath"
	"strings"
)

// CleanPath normalises a filesystem path for comparison, storage and display:
// it collapses redundant separators, resolves "." and ".." segments, and converts
// backslashes to forward slashes so that Windows and Unix paths compare evenly in
// logs and in the mod database.
//
// Backslashes are always folded, even on Unix where they are legal filename
// characters. That is safe because the result is never used to open a file: file
// operations take the raw path, while this function feeds comparison, keying and
// display. The payoff is that a config written on Windows still matches after
// being moved to Linux.
//
// It deliberately does not convert case: a case-folded path cannot be used for
// file operations.
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

// PathsEqual reports whether two filesystem paths point at the same location
// under case-insensitive comparison. This is correct on Windows and macOS and
// deliberately lenient on Linux; use StrictPathsEqual when a case difference
// must be treated as a difference.
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

// ModStoragePath returns the directory that holds a mod's version folder:
// <storage>/<gameID>/<modID>/<version>. Every segment is sanitised so that a
// hostile id or version string cannot escape the storage root. The game
// segment keeps two games' mods of the same id apart when one manager
// installation manages both.
func ModStoragePath(storageRoot, gameID, modID, version string) string {
	return JoinPath(
		storageRoot,
		SanitizeSegment(gameID),
		SanitizeSegment(modID),
		SanitizeSegment(version),
	)
}

// ModVersionRoot is the per-mod parent of a payload folder: the directory
// that holds every installed version of one mod under a given root. Cleaning
// it up after the last version is removed is what keeps the mods folder from
// filling up with empty directories.
func ModVersionRoot(modsRoot, modID string) string {
	return JoinPath(modsRoot, SanitizeSegment(modID))
}

// SanitizeSegment reduces an arbitrary string to a single safe path segment.
// Path separators, drive letters, dot segments and characters that are illegal
// on Windows are removed; the empty result becomes "_" so callers always get a
// usable directory name.
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
