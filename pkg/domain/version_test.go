package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"v1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"V1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"  1.2.3  ", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"1.2", Version{Major: 1, Minor: 2}, true},
		{"1", Version{Major: 1}, true},
		{"0.0.0", Version{}, true},
		{"1.2.3-beta.1", Version{Major: 1, Minor: 2, Patch: 3, PreRelease: "beta.1"}, true},
		{"1.2.3+build5", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"1.2.3-rc1+sha.abc", Version{Major: 1, Minor: 2, Patch: 3, PreRelease: "rc1"}, true},
		{"10.20.30", Version{Major: 10, Minor: 20, Patch: 30}, true},
	}
	for _, tc := range cases {
		got, ok := ParseVersion(tc.in)
		if ok != tc.ok || got != tc.want {
			assert.Fail(t, "ParseVersion(%q) = %+v,%v want %+v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseVersionRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "abc", "1.2.3.4", "1..3", "1.x", "-1.0.0", "-beta", "1.2.-3", "v", "+build"} {
		if got, ok := ParseVersion(in); ok {
			assert.Fail(t, "ParseVersion(%q) = %+v, want not ok", in, got)
		}
	}
}

func TestVersionString(t *testing.T) {
	v := Version{Major: 1, Minor: 2, Patch: 3, PreRelease: "beta"}
	assert.Equal(t, "1.2.3-beta", v.String(), "String")

	assert.Equal(t, "0.0.0", (Version{}).String(), "zero String")

	assert.True(t, (Version{}).IsZero(), "zero version should report IsZero")

	assert.False(t, (Version{Major: 1}).IsZero(), "1.0.0 should not be zero")

	assert.False(t, (Version{PreRelease: "rc"}).IsZero(), "pre-release makes a version non-zero")
}

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "10.0.0", -1},
		{"1.0.10", "1.0.9", 1},
		// A pre-release sorts below its own release.
		{"1.0.0-beta", "1.0.0", -1},
		{"1.0.0", "1.0.0-beta", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-alpha", "1.0.0-alpha", 0},
		{"1.2", "1.2.0", 0},
		{"v1.0.0", "1.0.0", 0},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, CompareVersions(tc.a, tc.b), "CompareVersions(%q,%q)", tc.a, tc.b)
	}
}

func TestCompareVersionsWithUnparseable(t *testing.T) {
	assert.Equal(t, -1, CompareVersions("garbage", "1.0.0"), "unparseable should sort below")

	assert.Equal(t, 1, CompareVersions("1.0.0", "garbage"), "parseable should sort above")

	assert.Equal(
		t,
		0,
		CompareVersions("garbage", "nonsense"),
		"two unparseable should compare equal",
	)
}

func TestParseVersionConstraints(t *testing.T) {
	cases := []struct {
		in   string
		want []VersionConstraint
		ok   bool
	}{
		{"", nil, true},
		{"  ", nil, true},
		{"*", nil, true},
		{
			"1.2.3",
			[]VersionConstraint{{Op: "==", Version: Version{Major: 1, Minor: 2, Patch: 3}}},
			true,
		},
		{">=1.0.0", []VersionConstraint{{Op: ">=", Version: Version{Major: 1}}}, true},
		{"<2.0", []VersionConstraint{{Op: "<", Version: Version{Major: 2}}}, true},
		{"=1.0.0", []VersionConstraint{{Op: "==", Version: Version{Major: 1}}}, true},
		{"!=1.0.0", []VersionConstraint{{Op: "!=", Version: Version{Major: 1}}}, true},
		{">=1.0.0,<2.0.0", []VersionConstraint{
			{Op: ">=", Version: Version{Major: 1}},
			{Op: "<", Version: Version{Major: 2}},
		}, true},
		{" >= 1.0.0 , < 2.0.0 ", []VersionConstraint{
			{Op: ">=", Version: Version{Major: 1}},
			{Op: "<", Version: Version{Major: 2}},
		}, true},
	}
	for _, tc := range cases {
		got, ok := ParseVersionConstraints(tc.in)
		if ok != tc.ok {
			assert.Fail(t, "ParseVersionConstraints(%q) ok = %v want %v", tc.in, ok, tc.ok)
			continue
		}

		if len(got) != len(tc.want) {
			assert.Fail(t, "ParseVersionConstraints(%q) = %+v want %+v", tc.in, got, tc.want)
			continue
		}

		for i := range got {
			if got[i].Op != tc.want[i].Op || got[i].Version != tc.want[i].Version {
				assert.Fail(t, "term %d of %q = %+v want %+v", i, tc.in, got[i], tc.want[i])
			}
		}
	}
}

func TestParseVersionConstraintsRejectsGarbage(t *testing.T) {
	for _, in := range []string{">=", ">=", ",", ">=1.0.0,", "1.0.0,,2.0.0", ">=abc", ">=1.0.0 || <2.0.0", ">=>=1.0.0", "1.0.0||", "||"} {
		if got, ok := ParseVersionConstraints(in); ok {
			assert.Fail(t, "ParseVersionConstraints(%q) = %+v, want not ok", in, got)
		}
	}
}

func TestSatisfiesExpression(t *testing.T) {
	cases := []struct {
		version, expr string
		want          bool
	}{
		{"1.0.0", "", true},
		{"1.0.0", "*", true},
		{"1.2.3", "1.2.3", true},
		{"1.2.4", "1.2.3", false},
		{"1.2.3", ">=1.0.0", true},
		{"0.9.9", ">=1.0.0", false},
		{"2.0.0", ">=1.0.0,<3.0.0", true},
		{"3.0.0", ">=1.0.0,<3.0.0", false},
		{"1.0.0", "<=1.0.0", true},
		{"1.0.0", ">1.0.0", false},
		{"1.0.0", "!=2.0.0", true},
		{"2.0.0", "!=2.0.0", false},
		{"1.0.0-beta", ">=1.0.0", false},
		// Unparseable version: an open constraint accepts it, a specific one
		// cannot be satisfied.
		{"garbage", "", true},
		{"garbage", "*", true},
		{"garbage", ">=1.0.0", false},
		// Malformed expression never matches.
		{"1.0.0", ">=", false},
		{"1.0.0", "||", false},
	}
	for _, tc := range cases {
		assert.Equal(
			t,
			tc.want,
			SatisfiesExpression(tc.version, tc.expr),
			"SatisfiesExpression(%q, %q)",
			tc.version,
			tc.expr,
		)
	}
}

func TestSatisfiesWithParsedConstraints(t *testing.T) {
	constraints, ok := ParseVersionConstraints(">=1.0.0")
	require.True(t, ok, "parse failed")

	assert.True(t, Satisfies("1.5.0", constraints), "1.5.0 should satisfy >=1.0.0")

	assert.False(
		t,
		Satisfies("garbage", constraints),
		"unparseable version cannot satisfy a specific constraint",
	)

	assert.True(t, Satisfies("garbage", nil), "an empty constraint list accepts anything")
}
