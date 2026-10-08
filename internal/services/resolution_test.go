package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseConflictResolution covers the parser that turns the value back into a
// resolution. It exists because the value crosses two boundaries as a string —
// JSON over the Wails bridge, a flag argument on the command line — and a caller
// typing a hand-written flag is the most likely place for a hyphen to have been
// typed as an underscore.
func TestParseConflictResolution(t *testing.T) {
	accepted := []struct {
		text string
		want ConflictResolution
	}{
		{"keep-both", ResolutionKeepBoth},
		{"disable-mod-a", ResolutionDisableModA},
		{"disable-mod-b", ResolutionDisableModB},
		{"uninstall-mod-a", ResolutionUninstallModA},
		{"uninstall-mod-b", ResolutionUninstallModB},
		// The spellings String produces, plus the shapes a hand-typed argument or
		// a JSON payload arrives in.
		{"  keep-both  ", ResolutionKeepBoth},
		{"KEEP-BOTH", ResolutionKeepBoth},
		{"Keep_Both", ResolutionKeepBoth},
		{"disable_mod_b", ResolutionDisableModB},
		{"\tUninstall-Mod-A\n", ResolutionUninstallModA},
	}
	for _, tc := range accepted {
		t.Run(tc.text, func(t *testing.T) {
			got, ok := ParseConflictResolution(tc.text)
			require.True(t, ok, "ParseConflictResolution(%q) refused it", tc.text)

			assert.Equal(
				t,
				tc.want,
				got,
				"ParseConflictResolution(%q) = %v, want %v",
				tc.text,
				got,
				tc.want,
			)
		})
	}

	refused := []struct {
		text string
	}{
		{""},
		{"   "},
		{"unset"},     // the zero value is refused downstream, so it is refused here too
		{"keep both"}, // a space is not a hyphen
		{"keepboth"},
		{"disable-mod"}, // a resolution needs both sides
		{"disable-mod-c"},
		{"disable-everything"},
		{"keep-both!"},
	}
	for _, tc := range refused {
		t.Run("refuse "+tc.text, func(t *testing.T) {
			got, ok := ParseConflictResolution(tc.text)
			assert.False(t, ok, "ParseConflictResolution(%q) = %v, want a refusal", tc.text, got)

			assert.Equal(
				t,
				ResolutionUnset,
				got,
				"a refused value came back as %v, want ResolutionUnset",
				got,
			)
		})
	}
}

// TestParseConflictResolutionRoundTrips: every resolution the type can hold has to
// survive the trip through its own text, or a caller that renders a report and
// reads the value back cannot act on it.
func TestParseConflictResolutionRoundTrips(t *testing.T) {
	all := []ConflictResolution{
		ResolutionKeepBoth,
		ResolutionDisableModA,
		ResolutionDisableModB,
		ResolutionUninstallModA,
		ResolutionUninstallModB,
	}
	for _, want := range all {
		got, ok := ParseConflictResolution(want.String())
		if !ok {
			assert.True(
				t,
				ok,
				"ParseConflictResolution(%q) refused the type's own spelling",
				want.String(),
			)

			continue
		}

		assert.Equal(t, want, got, "%q round-tripped to %v", want.String(), got)
	}
}

// TestResolveConflictRefusesAnUnparsableResolution pins the whole path: a
// resolution that never reaches the service, so nothing is changed and the caller
// is told what it could have said instead.
func TestResolveConflictRefusesAnUnparsableResolution(t *testing.T) {
	svc := &ModService{}
	{
		err := svc.ResolveConflict(t.Context(), "file_overlap:a:b", ResolutionUnset)
		require.Error(t, err, "ResolveConflict accepted the zero resolution")
	}
}

// TestConflictResolutionStringIsStable: the CLI prints these and the GUI sends
// them back, so changing one is a breaking change and it should be pinned.
func TestConflictResolutionStringIsStable(t *testing.T) {
	want := map[ConflictResolution]string{
		ResolutionUnset:         "unset",
		ResolutionKeepBoth:      "keep-both",
		ResolutionDisableModA:   "disable-mod-a",
		ResolutionDisableModB:   "disable-mod-b",
		ResolutionUninstallModA: "uninstall-mod-a",
		ResolutionUninstallModB: "uninstall-mod-b",
		ConflictResolution(99):  "unset",
	}
	for value, text := range want {
		{
			got := value.String()
			assert.Equal(
				t,
				text,
				got,
				"ConflictResolution(%d).String() = %q, want %q",
				int(value),
				got,
				text,
			)
		}
	}
}
