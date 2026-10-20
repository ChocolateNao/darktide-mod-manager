package domain

import (
	"strconv"
	"strings"
)

// Version is a dotted numeric version. Manifest strings are semver-ish, so
// parsing is lenient: missing components default to zero and a pre-release suffix
// is kept but ordered below its release.
type Version struct {
	PreRelease string
	Major      int
	Minor      int
	Patch      int
}

func (v Version) String() string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(v.Major))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(v.Minor))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(v.Patch))

	if v.PreRelease != "" {
		b.WriteByte('-')
		b.WriteString(v.PreRelease)
	}

	return b.String()
}

func (v Version) IsZero() bool {
	return v.Major == 0 && v.Minor == 0 && v.Patch == 0 && v.PreRelease == ""
}

// Compare returns -1, 0 or 1 for the ordering of v and other. A pre-release sorts
// before its release, matching semver.
func (v Version) Compare(other Version) int {
	if c := compareInt(v.Major, other.Major); c != 0 {
		return c
	}

	if c := compareInt(v.Minor, other.Minor); c != 0 {
		return c
	}

	if c := compareInt(v.Patch, other.Patch); c != 0 {
		return c
	}

	return comparePreRelease(v.PreRelease, other.PreRelease)
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func comparePreRelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		// A release outranks any pre-release.
		return 1
	case b == "":
		return -1
	case a < b:
		return -1
	default:
		return 1
	}
}

// ParseVersion parses a version string, ignoring a leading "v" and surrounding
// whitespace. Unparseable input yields the zero version and ok=false, so a
// malformed manifest field is "unknown".
func ParseVersion(s string) (Version, bool) {
	v := Version{}

	s = strings.TrimSpace(s)
	if s == "" {
		return v, false
	}

	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")

	// Split off build metadata and the pre-release, which semver says must not
	// affect precedence. A leading '-' means no numeric part, which is invalid.
	if strings.HasPrefix(s, "-") {
		return v, false
	}

	if idx := strings.IndexByte(s, '+'); idx >= 0 {
		s = s[:idx]
	}

	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		v.PreRelease = s[idx+1:]
		s = s[:idx]
	}

	if s == "" {
		return v, false
	}

	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return v, false
	}

	nums := make([]int, 3)

	for i, p := range parts {
		if p == "" {
			return v, false
		}

		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}

		nums[i] = n
	}

	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]

	return v, true
}

// CompareVersions parses both strings and compares them. Unparseable versions sort
// below parseable ones, and two unparseable versions compare equal.
func CompareVersions(a, b string) int {
	va, okA := ParseVersion(a)

	vb, okB := ParseVersion(b)
	switch {
	case okA && okB:
		return va.Compare(vb)
	case okA:
		return 1
	case okB:
		return -1
	default:
		return 0
	}
}

type VersionConstraint struct {
	Op      string
	Version Version
}

// VersionConstraintPrefixes lists the accepted operators, longest first so that
// parsing does not mistake ">=" for ">".
var VersionConstraintPrefixes = []string{">=", "<=", "==", "!=", ">", "<", "="}

var emptyConstraint = []VersionConstraint{}

// ParseVersionConstraints parses a constraint expression. Accepted forms:
//
//	"" or "*"        → matches any version
//	"1.2.3"          → exact match (equivalent to "==1.2.3")
//	">=1.0.0,<2.0.0" → conjunction, all terms must hold
//
// Whitespace is ignored. "||" is rejected, since it would silently widen what a
// mod claims to support. ok is false if any component is malformed.
func ParseVersionConstraints(s string) ([]VersionConstraint, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return emptyConstraint, true
	}

	if strings.Contains(s, "||") {
		return nil, false
	}

	var out []VersionConstraint

	for _, term := range strings.Split(s, ",") {
		c, ok := parseConstraintTerm(term)
		if !ok {
			return nil, false
		}

		out = append(out, c)
	}

	if len(out) == 0 {
		return nil, false
	}

	return out, true
}

func parseConstraintTerm(term string) (VersionConstraint, bool) {
	term = strings.TrimSpace(term)
	if term == "" {
		return VersionConstraint{}, false
	}

	for _, prefix := range VersionConstraintPrefixes {
		if len(term) > len(prefix) && strings.HasPrefix(term, prefix) {
			v, ok := ParseVersion(term[len(prefix):])
			if !ok {
				return VersionConstraint{}, false
			}

			op := prefix
			if op == "=" {
				op = "=="
			}

			return VersionConstraint{Op: op, Version: v}, true
		}
	}

	v, ok := ParseVersion(term)
	if !ok {
		return VersionConstraint{}, false
	}

	return VersionConstraint{Op: "==", Version: v}, true
}

// Satisfies reports whether version meets every constraint. An empty list accepts
// any version, including one that cannot be parsed.
func Satisfies(version string, constraints []VersionConstraint) bool {
	if len(constraints) == 0 {
		return true
	}

	v, ok := ParseVersion(version)
	if !ok {
		return false
	}

	for _, c := range constraints {
		if !c.matches(v) {
			return false
		}
	}

	return true
}

// SatisfiesExpression parses expr and reports whether version matches. A malformed
// expression returns false, so a broken manifest field cannot enable a mod.
func SatisfiesExpression(version, expr string) bool {
	constraints, ok := ParseVersionConstraints(expr)
	if !ok {
		return false
	}

	v, parsed := ParseVersion(version)
	if !parsed {
		// An empty constraint list matches everything, including unknown versions.
		return len(constraints) == 0
	}

	for _, c := range constraints {
		if !c.matches(v) {
			return false
		}
	}

	return true
}

func (c VersionConstraint) matches(v Version) bool {
	cmp := v.Compare(c.Version)
	switch c.Op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "==":
		return cmp == 0
	case "!=":
		return cmp != 0
	default:
		return false
	}
}
