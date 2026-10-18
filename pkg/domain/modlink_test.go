package commands

import (
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShareLinkBuildsEachLaunchersOwnForm(t *testing.T) {
	// The mod stores number a file separately from a mod, and the file id is
	// what the manager installs. A link carrying only the mod id would resolve to
	// a different file, so both halves have to be there and in the launcher's
	// own order.
	tests := []struct {
		name string
		want string
		note string
		mod  api.Mod
	}{
		{
			name: "nexus",
			mod: api.Mod{
				ID: "nexus-900-7001", SourceID: "nexus",
				SourceData: map[string]any{"mod_id": 900, "file_id": 7001},
			},
			want: "nxm link warhammer40kdarktide/900/7001",
			note: "the plain form is what people paste into chat; a launcher link is a long URL",
		},
		{
			name: "github uses the release page",
			mod: api.Mod{
				ID: "owner.pack", SourceID: "github",
				SourceData: map[string]any{
					"release_url": "https://github.com/owner/pack/releases/tag/v1.2.0",
				},
			},
			want: "https://github.com/owner/pack/releases/tag/v1.2.0",
			note: "a GitHub release is already a link; wrapping it in another scheme would break it",
		},
		{
			name: "the source id is matched without regard to case or padding",
			mod: api.Mod{
				ID: "nexus-900-7001", SourceID: "  NEXUS  ",
				SourceData: map[string]any{"mod_id": 900, "file_id": 7001},
			},
			want: "nxm link warhammer40kdarktide/900/7001",
			note: "a source id is normalised everywhere else, so it must be here too",
		},
		{
			name: "a whole float is accepted",
			mod: api.Mod{
				ID: "nexus-900-7001", SourceID: "nexus",
				SourceData: map[string]any{"mod_id": 900.0, "file_id": 7001.0},
			},
			want: "nxm link warhammer40kdarktide/900/7001",
			note: "JSON has one number type, so a mod that has been through the database carries every id as a float64",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := shareLink(test.mod)
			require.NoError(t, err, "shareLink")

			assert.Equal(t, test.want, got, "shareLink = %q, want %q", got, test.want)
		})
	}
}

func TestShareLinkRefusesWhatCannotBeShared(t *testing.T) {
	tests := []struct {
		name string
		want string
		note string
		mod  api.Mod
	}{
		{
			name: "a local mod has no link",
			mod:  api.Mod{ID: "betterui", SourceID: "local"},
			want: "local",
			note: "a local mod only exists on this machine, and printing a link for it would be a lie",
		},
		{
			name: "a nexus mod missing its file id",
			mod: api.Mod{
				ID:         "nexus-900-7001",
				SourceID:   "nexus",
				SourceData: map[string]any{"mod_id": 900},
			},
			want: "nexus",
			note: "half the pair cannot address a file, so the link would name a mod, not this file",
		},
		{
			name: "a nexus mod with no source data at all",
			mod:  api.Mod{ID: "nexus-900-7001", SourceID: "nexus"},
			want: "nexus",
			note: "a nil map is what a mod built by hand looks like, and it must not panic",
		},
		{
			name: "a github mod with no release url",
			mod:  api.Mod{ID: "owner.pack", SourceID: "github"},
			want: "github",
			note: "the release page is the whole link, so there is nothing to fall back to",
		},
		{
			name: "a github mod with a blank release url",
			mod: api.Mod{
				ID:         "owner.pack",
				SourceID:   "github",
				SourceData: map[string]any{"release_url": "   "},
			},
			want: "github",
			note: "a value that is only whitespace is no value at all",
		},
		{
			name: "a source with no name at all",
			mod:  api.Mod{ID: "mystery"},
			want: "-",
			note: "the message has to stay readable when there is nothing to name",
		},
		{
			name: "an id written as a string",
			mod: api.Mod{
				ID: "nexus-900-7001", SourceID: "nexus",
				SourceData: map[string]any{"mod_id": "900", "file_id": "7001"},
			},
			want: "nexus",
			note: "a string here would be a plugin that disagreed with the JSON type; guessing at it is how a link ends up naming a different mod",
		},
		{
			name: "a fractional id",
			mod: api.Mod{
				ID: "nexus-900-7001", SourceID: "nexus",
				SourceData: map[string]any{"mod_id": 900.0, "file_id": 7001.5},
			},
			want: "nexus",
			note: "truncating 7001.5 would produce a link to file 7001, which is a real and different mod",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := shareLink(test.mod)
			require.Error(t, err, "shareLink returned %q, want a refusal", got)

			assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
				"the refusal is not INVALID_ARGUMENT, so a script cannot branch on it: %v",
				err,
			)
			// The message has to name the source, because the fix is to get the
			// mod from a source that can be shared.
			assert.Contains(
				t,
				err.Error(),
				test.want,
				"the message does not name %q: %v",
				test.want,
				err,
			)

			assert.Contains(
				t,
				err.Error(),
				"nexus and github",
				"the message does not say what is shareable: %v",
				err,
			)
		})
	}
}

func TestParseModLinkAcceptsBothSpellings(t *testing.T) {
	tests := []struct {
		name       string
		link       string
		wantSource string
		wantMod    string
		note       string
	}{
		{
			name:       "the nexus scheme",
			link:       "nxm://warhammer40kdarktide/900/7001",
			wantSource: "nexus",
			wantMod:    "nexus-900-7001",
			note:       "what the launcher puts on the clipboard",
		},
		{
			name:       "the nexus plain form",
			link:       "nxm link warhammer40kdarktide/900/7001",
			wantSource: "nexus",
			wantMod:    "nexus-900-7001",
			note:       "what people paste into chat",
		},
		{
			name:       "the nexus plain form in the wrong case",
			link:       "NXM LINK WaRhAmMer40kdarktide/900/7001",
			wantSource: "nexus",
			wantMod:    "nexus-900-7001",
			note:       "a launcher has no say over how somebody typed it",
		},
		{
			name:       "surrounding whitespace",
			link:       "  nxm link warhammer40kdarktide/900/7001\n",
			wantSource: "nexus",
			wantMod:    "nexus-900-7001",
			note:       "a link pasted from a document carries a trailing newline",
		},
		{
			name:       "a trailing slash",
			link:       "nxm://warhammer40kdarktide/900/7001/",
			wantSource: "nexus",
			wantMod:    "nexus-900-7001",
		},
		{
			name:       "a query string",
			link:       "nxm://warhammer40kdarktide/900/7001?key=PERSONAL",
			wantSource: "nexus",
			wantMod:    "nexus-900-7001",
			note:       "a query string is not an error; what it means is read separately below",
		},
		{
			name:       "the game name in the wrong case",
			link:       "nxm link WaRhAmMeR40kdarktide/900/7001",
			wantSource: "nexus",
			wantMod:    "nexus-900-7001",
		},
		{
			name:       "a nexus link with extra path segments",
			link:       "nxm://warhammer40kdarktide/mods/900/7001",
			wantSource: "nexus",
			wantMod:    "nexus-900-7001",
			note:       "only the last two numbers are the ids, and the manager installs the file id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, mod, _, err := parseModLink(test.link)
			require.NoError(t, err, "parseModLink(%q)", test.link)

			assert.Equal(t, test.wantSource, source, "parseModLink(%q) = %q, %q; want %q, %q",
				test.link, source, mod, test.wantSource, test.wantMod)
			assert.Equal(t, test.wantMod, mod, "parseModLink(%q) = %q, %q; want %q, %q",
				test.link, source, mod, test.wantSource, test.wantMod)
		})
	}
}

func TestParseNexusLinkReadsTheDownloadAuthorization(t *testing.T) {
	// A keyed link is the website authorizing one specific download, which is
	// the only way a free account can fetch an archive: the v3 mint is
	// premium-only. The three parts travel in the query string, so a link either
	// carries all of them or carries no authorization at all.
	for _, test := range []struct {
		name      string
		link      string
		wantKey   string
		wantExp   string
		wantUser  string
		wantGrant bool
	}{
		{
			name:      "a keyed link",
			link:      "nxm://warhammer40kdarktide/mods/1419/files/8847?key=SllmtdT3ImkvA7QVgMoi-w&expires=1791538669&user_id=189193053",
			wantKey:   "SllmtdT3ImkvA7QVgMoi-w",
			wantExp:   "1791538669",
			wantUser:  "189193053",
			wantGrant: true,
		},
		{
			name:      "the plain form can be keyed too",
			link:      "nxm link warhammer40kdarktide/1419/8847?key=k&expires=1&user_id=2",
			wantKey:   "k",
			wantExp:   "1",
			wantUser:  "2",
			wantGrant: true,
		},
		{
			name:      "a plain link carries no authorization",
			link:      "nxm://warhammer40kdarktide/mods/1419/files/8847",
			wantGrant: false,
		},
		{
			name:      "an unrelated query string is not an authorization",
			link:      "nxm://warhammer40kdarktide/mods/1419/files/8847?view=1",
			wantGrant: false,
		},
		{
			name:      "a partial authorization is not a grant",
			link:      "nxm://warhammer40kdarktide/mods/1419/files/8847?key=abc&expires=1791538669",
			wantGrant: false,
		},
		{
			name:      "a blank part is not a part",
			link:      "nxm://warhammer40kdarktide/mods/1419/files/8847?key=&expires=1&user_id=2",
			wantGrant: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, grant, err := parseNexusLink(test.link)
			require.NoError(t, err, "parseNexusLink(%q)", test.link)

			require.Equal(
				t,
				test.wantGrant,
				grant.authorized(),
				"grant.authorized() = %v, want %v",
				grant.authorized(),
				test.wantGrant,
			)

			if !test.wantGrant {
				return
			}

			assert.Equal(
				t,
				test.wantKey,
				grant.key,
				"grant.key = %q, want %q",
				grant.key,
				test.wantKey,
			)

			assert.Equal(
				t,
				test.wantExp,
				grant.expires,
				"grant.expires = %q, want %q",
				grant.expires,
				test.wantExp,
			)

			assert.Equal(
				t,
				test.wantUser,
				grant.userID,
				"grant.userID = %q, want %q",
				grant.userID,
				test.wantUser,
			)
		})
	}
}

func TestNXMGrantSourceDataIsNilUnlessTheGrantIsComplete(t *testing.T) {
	// A partial grant rendered as SourceData would reach the source as an
	// authorization, and the endpoint answers 403 to one — a 403 that blames the
	// account rather than the link.
	got := (nxmGrant{}).sourceData()
	assert.Nil(t, got, "an empty grant rendered %v, want nil", got)

	got = (nxmGrant{key: "k", expires: "1"}).sourceData()
	assert.Nil(t, got, "a partial grant rendered SourceData, want nil")

	got = nxmGrant{key: "k", expires: "1", userID: "2"}.sourceData()

	want := map[string]any{"nxm_key": "k", "nxm_expires": "1", "nxm_user_id": "2"}
	for key, value := range want {
		assert.Equal(t, value, got[key], "sourceData[%q] = %v, want %v", key, got[key], value)
	}
}

func TestParseModLinkCarriesAKeyedGrantThrough(t *testing.T) {
	// The grant has to survive the trip from the parser to the caller intact:
	// it is the whole reason a keyed link is worth pasting.
	source, mod, grant, err := parseModLink(
		"nxm://warhammer40kdarktide/mods/1419/files/8847?key=k&expires=1&user_id=2")
	require.NoError(t, err, "parseModLink")

	require.Equal(
		t,
		"nexus",
		source,
		"parseModLink = %q/%q, want nexus/nexus-1419-8847",
		source,
		mod,
	)
	require.Equal(
		t,
		"nexus-1419-8847",
		mod,
		"parseModLink = %q/%q, want nexus/nexus-1419-8847",
		source,
		mod,
	)

	data := grant.sourceData()
	require.Equal(t, "k", data["nxm_key"], "the grant reached the caller as %v", data)
	require.Equal(t, "1", data["nxm_expires"], "the grant reached the caller as %v", data)
	require.Equal(t, "2", data["nxm_user_id"], "the grant reached the caller as %v", data)
	// A plain link yields nothing rather than an empty map, so the caller's
	// install options are unchanged by a link that carried no authorization.
	_, _, plain, err := parseModLink("nxm://warhammer40kdarktide/mods/1419/files/8847")
	require.NoError(t, err, "parseModLink")

	assert.Nil(t, plain.sourceData(), "a plain link rendered %v, want nil", plain.sourceData())
}

func TestParseModLinkRefusesWhatItCannotInstall(t *testing.T) {
	tests := []struct {
		name      string
		link      string
		want      string
		note      string
		showsForm bool
	}{
		{
			name: "nothing at all",
			link: "",
			want: "no link given",
		},
		{
			name: "only whitespace",
			link: "   \n",
			want: "no link given",
		},
		{
			name:      "a web address",
			link:      "https://www.nexusmods.com/darktide/mods/7001",
			want:      "is a web address",
			showsForm: true,
			note:      "the page has the numbers, but a mod id is not a page address and guessing is worse than saying so",
		},
		{
			name:      "a plain web address",
			link:      "http://example.invalid",
			want:      "is a web address",
			showsForm: true,
		},
		{
			name:      "an id with no scheme",
			link:      "900/7001",
			want:      "is not a mod link",
			showsForm: true,
		},
		{
			name:      "a nexus link missing the file id",
			link:      "nxm link darktide/900",
			want:      "not a complete Nexus link",
			showsForm: true,
		},
		{
			name:      "a nexus link with no numbers",
			link:      "nxm link darktide",
			want:      "not a complete Nexus link",
			showsForm: true,
		},
		{
			name:      "a nexus link with a non-numeric mod id",
			link:      "nxm link warhammer40kdarktide/beta/7001",
			want:      "no numeric mod id",
			showsForm: true,
			note:      "the mod id is not used, but a link that does not have one came from a different scheme",
		},
		{
			name:      "a nexus link with a non-numeric file id",
			link:      "nxm link warhammer40kdarktide/900/latest",
			want:      "no numeric file id",
			showsForm: true,
		},
		{
			name:      "a nexus link with a zero file id",
			link:      "nxm link warhammer40kdarktide/900/0",
			want:      "no numeric file id",
			showsForm: true,
			note:      "Nexus numbers its files from one, so zero is not a file",
		},
		{
			name:      "a nexus link for another game",
			link:      "nxm link fallout4/900/7001",
			want:      "this manager only handles",
			showsForm: false,
			note:      "installing it would be wrong rather than merely useless",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, mod, _, err := parseModLink(test.link)
			require.Error(t, err, "parseModLink(%q) accepted it as %q/%q", test.link, source, mod)

			assert.True(
				t,
				appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
				"the refusal is not INVALID_ARGUMENT: %v",
				err,
			)

			assert.Contains(
				t,
				err.Error(),
				test.want,
				"the message %q does not say %q",
				err.Error(),
				test.want,
			)
			// A malformed link has to name what it expected, or the user has
			// nothing to correct the link with.
			if test.showsForm {
				assert.Contains(
					t,
					err.Error(),
					"nxm",
					"the message does not show the expected form: %v",
					err,
				)
			}
		})
	}
}

func TestParseModLinkRoundTripsWhatShareLinkBuilds(t *testing.T) {
	// The two halves of the share/import pair are only useful together if one
	// accepts exactly what the other produces. A Nexus mod is keyed by the
	// (mod, file) pair, so a link has to carry enough for the source to
	// rebuild exactly the id it published — a link that carried only one half
	// would install a different file than the one the sender meant.
	mod := api.Mod{
		ID:         "nexus-900-7001",
		SourceID:   "nexus",
		SourceData: map[string]any{"mod_id": 900, "file_id": 7001},
	}

	link, err := shareLink(mod)
	require.NoError(t, err, "shareLink(%s)", mod.SourceID)

	source, got, _, err := parseModLink(link)
	require.NoError(t, err, "parseModLink(%q)", link)

	assert.Equal(
		t,
		mod.SourceID,
		source,
		"the link %q resolved to source %q, want %q",
		link,
		source,
		mod.SourceID,
	)

	assert.Equal(t, mod.ID, got, "the link %q resolved to mod %q, want %q", link, got, mod.ID)
}

func TestPositiveInt(t *testing.T) {
	tests := []struct {
		in    string
		note  string
		wantI int64
		ok    bool
	}{
		{in: "1", ok: true, wantI: 1},
		{in: "7001", ok: true, wantI: 7001},
		{
			in:    "  42  ",
			ok:    true,
			wantI: 42,
			note:  "a segment picked out of a pasted link carries whitespace",
		},
		{
			in:    "+7",
			ok:    true,
			wantI: 7,
			note:  "strconv accepts a sign; a launcher does not print one, and a link that did was not made by a launcher",
		},
		{in: "", ok: false},
		{in: "   ", ok: false},
		{in: "0", ok: false, note: "both stores number from one, so zero is not an id"},
		{in: "-5", ok: false},
		{in: "1.5", ok: false},
		{in: "1e3", ok: false},
		{in: "beta", ok: false},
		{in: "900/7001", ok: false},
		{in: "0x10", ok: false, note: "base 10 is the only base a link uses"},
	}
	for _, test := range tests {
		got, ok := positiveInt(test.in)
		assert.Equal(t, test.ok, ok, "positiveInt(%q) ok = %v, want %v", test.in, ok, test.ok)

		if !ok {
			continue
		}

		assert.Equal(t, test.wantI, got, "positiveInt(%q) = %d, want %d", test.in, got, test.wantI)
	}
}
