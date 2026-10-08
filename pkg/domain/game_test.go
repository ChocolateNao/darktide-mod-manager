package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseGameSource(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  GameSource
		valid bool
	}{
		{"steam", GameSourceSteam, true},
		{"  Steam  ", GameSourceSteam, true},
		{"EPIC", GameSourceEpic, true},
		{"crossover", GameSourceCrossOver, true},
		{"parallels", GameSourceParallels, true},
		{"proton", GameSourceProton, true},
		// "auto" is a filter meaning "do not narrow detection", not a launcher.
		{"auto", "", false},
		// "manual" is a recorded source but is not something a user filters by.
		{"manual", "", false},
		{"", "", false},
		{"   ", "", false},
		{"not-a-launcher", "", false},
		{"stea", "", false},
	} {
		got, ok := ParseGameSource(tc.in)
		if ok != tc.valid {
			assert.Fail(t, "ParseGameSource(%q) valid = %v, want %v", tc.in, ok, tc.valid)
			continue
		}

		if ok && got != tc.want {
			assert.Fail(t, "ParseGameSource(%q) = %q, want %q", tc.in, got, tc.want)
		}

		if !ok && got != "" {
			assert.Fail(t, "ParseGameSource(%q) = %q on failure, want the empty source", tc.in, got)
		}
	}
}

func TestParseGameSourceAcceptsEveryKnownLauncher(t *testing.T) {
	// Guards against a new GameSource constant being added without being
	// reachable through ParseGameSource.
	for _, known := range knownGameSources {
		if known == GameSourceManual {
			continue
		}

		got, ok := ParseGameSource(string(known))
		if !ok {
			assert.Fail(t, "ParseGameSource(%q) rejected a known launcher", known)
			continue
		}

		assert.Equal(t, known, got, "ParseGameSource(%q)", known)
	}
}
