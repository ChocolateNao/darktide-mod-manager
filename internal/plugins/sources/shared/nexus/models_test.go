package nexus

import (
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests here exercise the conversions on their own, without a server; the transport
// tests in nexus_test.go cover the shapes they are fed.

// nexusContext is a fully populated mod context. Using one literal rather than a
// zero value everywhere keeps the tests about the field under test rather than
// about which other fields happened to be empty.
func nexusContext() modContext {
	return modContext{
		scopedID:     "900",
		uid:          "1234567890123",
		gameID:       "3834",
		gameDomain:   "darktide",
		name:         "Better UI",
		summary:      "UI tweaks",
		author:       "modder",
		thumbnailURL: "https://images.invalid/thumb.png",
	}
}

// nexusFile is one active mod file.
func nexusFile() fileAggregate {
	return fileAggregate{
		ID:                 "7001",
		Name:               "Better UI Main",
		IsActive:           true,
		LastFileUploadedAt: "2026-02-01T10:00:00Z",
		VersionsCount:      3,
		ArchivedCount:      1,
		RemovedCount:       0,
	}
}

// nexusVersion is one installable version of that file.
func nexusVersion() fileVersion {
	return fileVersion{
		ID:           "8001",
		Position:     "2.0",
		GameScopedID: "900",
		Name:         "Better UI 1.2.0",
		Version:      "1.2.0",
		Category:     "main",
		UploadedAt:   "2026-02-01T09:30:00Z",
		IsPrimary:    true,
	}
}

func TestModIDNeedsBothHalves(t *testing.T) {
	cases := []struct {
		scoped, file string
		want         string
	}{
		{"900", "7001", "nexus-900-7001"},
		{" 900 ", " 7001 ", "nexus-900-7001"},
		{"1", "1", "nexus-1-1"},
		{"", "7001", ""},
		{"900", "", ""},
		{"", "", ""},
		{"   ", "7001", ""},
		{"900", "   ", ""},
		{"90a", "7001", ""},
		{"900", "700a", ""},
		{"-900", "7001", ""},
		{"900", "+7001", ""},
		{"9.00", "7001", ""},
		{"900", "7001 ", "nexus-900-7001"},
	}
	for _, tc := range cases {
		got := ModID(tc.scoped, tc.file)
		assert.Equal(
			t,
			tc.want,
			got,
			"ModID(%q, %q) = %q, want %q",
			tc.scoped,
			tc.file,
			got,
			tc.want,
		)
	}
}

func TestParseModIDReversesModID(t *testing.T) {
	cases := []struct {
		id     string
		scoped string
		file   string
		ok     bool
	}{
		{"nexus-900-7001", "900", "7001", true},
		{"  nexus-900-7001  ", "900", "7001", true},
		{"nexus-1-1", "1", "1", true},
		{"", "", "", false},
		{"   ", "", "", false},
		{"nexus-", "", "", false},
		{"nexus-900", "", "", false},
		{"nexus-900-", "", "", false},
		{"nexus--7001", "", "", false},
		// A trailing segment is refused rather than truncated: ModID can never produce
		// "nexus-900-7001-5", so accepting one would be a guess about what was meant.
		{"nexus-900-7001-5", "", "", false},
		{"nexus-900-7001.5", "", "", false},
		{"nexus-900a-7001", "", "", false},
		{"nexus-900-7001a", "", "", false},
		{"github-owner-repo", "", "", false},
		{"nexus 900 7001", "", "", false},
		{"-900-7001", "", "", false},
	}
	for _, tc := range cases {
		scoped, file, ok := ParseModID(tc.id)
		if ok != tc.ok {
			assert.Fail(t, "ParseModID(%q) ok = %v, want %v", tc.id, ok, tc.ok)
			continue
		}

		if ok && (scoped != tc.scoped || file != tc.file) {
			assert.Fail(t,
				"ParseModID(%q) = %q, %q; want %q, %q",
				tc.id,
				scoped,
				file,
				tc.scoped,
				tc.file,
			)
		}
	}
}

func TestModIDAndParseModIDAreInverses(t *testing.T) {
	// The pair is what a shared link carries and what the manager stores, so the
	// two directions have to agree for every id the plugin can produce.
	for _, pair := range [][2]string{{"900", "7001"}, {"1", "2"}, {"1234567", "7654321"}} {
		id := ModID(pair[0], pair[1])

		scoped, file, ok := ParseModID(id)
		require.True(t, ok, "ParseModID(%q) refused an id this package built", id)

		if scoped != pair[0] || file != pair[1] {
			assert.Fail(t, "round trip of %v gave %q, %q", pair, scoped, file)
		}
	}
}

func TestModIDSurvivesSanitizeSegment(t *testing.T) {
	// A mod id has to stay usable as one path segment, because the payload
	// folder is built from it.
	for _, pair := range [][2]string{{"900", "7001"}, {"1234567", "7654321"}} {
		id := ModID(pair[0], pair[1])
		got := domain.SanitizeSegment(id)
		assert.Equal(t, id, got, "SanitizeSegment(%q) = %q; the id must survive unchanged", id, got)
	}
}

func TestIsDigitsRefusesAnythingButPlainDigits(t *testing.T) {
	good := []string{"0", "7", "42", "900", "007", "123456789012345678901234567890"}
	for _, s := range good {
		assert.True(t, isDigits(s), "isDigits(%q) = false, want true", s)
	}

	bad := []string{"", " ", "-1", "+1", " 1", "1 ", "1.0", "0x10", "abc", "1a", "nexus-1", "١٢٣"}
	for _, s := range bad {
		assert.False(t, isDigits(s), "isDigits(%q) = true, want false", s)
	}
}

func TestScopedIDFromPageURL(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"https://www.nexusmods.com/darktide/mods/900", "900", true},
		{"http://www.nexusmods.com/darktide/mods/900", "900", true},
		{"https://nexusmods.com/skyrimspecialedition/mods/12604", "12604", true},
		{"https://www.nexusmods.com/darktide/mods/900/files", "900", true},
		{"https://www.nexusmods.com/darktide/mods/900#files", "900", true},
		{"https://www.nexusmods.com/darktide/mods/900/", "900", true},
		{"https://www.nexusmods.com/Darktide/MODS/900", "900", true},
		{"  https://www.nexusmods.com/darktide/mods/900  ", "900", true},
		{"https://www.nexusmods.com/darktide/mods/0", "0", true},
		{"", "", false},
		{"   ", "", false},
		{"https://www.nexusmods.com/darktide", "", false},
		{"https://www.nexusmods.com/darktide/mods", "", false},
		{"https://www.nexusmods.com/darktide/mods/", "", false},
		{"https://www.nexusmods.com/darktide/mods/abc", "", false},
		{"https://www.nexusmods.com/darktide/mods/9a0", "", false},
		{"not a url at all", "", false},
		// The API publishes the canonical URL, and a query string on one is
		// normal; url.Parse keeps it out of the path, so the id is still read.
		{"https://www.nexusmods.com/darktide/mods/900?tab=files", "900", true},
	}
	for _, tc := range cases {
		got, ok := scopedIDFromPageURL(tc.raw)
		if ok != tc.ok || (ok && got != tc.want) {
			assert.Fail(t,
				"scopedIDFromPageURL(%q) = %q, %v; want %q, %v",
				tc.raw,
				got,
				ok,
				tc.want,
				tc.ok,
			)
		}
	}
}

func TestPageURLNeedsBothHalves(t *testing.T) {
	full := nexusContext()
	got, want := full.pageURL(), "https://www.nexusmods.com/darktide/mods/900"
	assert.Equal(t, want, got, "pageURL() = %q, want %q", got, want)

	noScoped := nexusContext()

	noScoped.scopedID = ""
	got = noScoped.pageURL()
	assert.Equal(
		t,
		"",
		got,
		"pageURL() with no mod id = %q, want empty: a page URL naming no mod points at nothing",
		got,
	)

	noDomain := nexusContext()

	noDomain.gameDomain = ""
	got = noDomain.pageURL()
	assert.Equal(t, "", got, "pageURL() with no game = %q, want empty", got)
}

func TestLocationURLNamesTheMintingEndpoint(t *testing.T) {
	// The cache hashes this string, so it has to change exactly when the version
	// does and must not be something fetchable: v3 mints a real URL per download.
	c := nexusContext()
	got := c.locationURL("8001")

	want := "https://api.nexusmods.com/v3/mod-file-versions/8001/download-repacked"
	assert.Equal(t, want, got, "locationURL = %q, want %q", got, want)

	if other := c.locationURL("8002"); other == got {
		assert.Fail(
			t,
			"locationURL is the same for two versions, so both would share one cache key",
		)
	}

	assert.True(
		t,
		strings.HasPrefix(got, "http"),
		"locationURL names the endpoint that mints a fetchable URL",
	)

	for _, blank := range []string{"", "   ", "\t"} {
		got := c.locationURL(blank)
		assert.Equal(
			t,
			"",
			got,
			"locationURL(%q) = %q, want empty: a padded version id would mint a cache key that differs from the trimmed one",
			blank,
			got,
		)
	}
}

func TestClassifyCategory(t *testing.T) {
	for _, category := range []string{
		"main", "update", "optional", "miscellaneous",
		"MAIN", " Main ", "\tupdate\n",
	} {
		assert.Equal(
			t,
			categoryInstallable,
			classifyCategory(category),
			"classifyCategory(%q) should be installable",
			category,
		)
	}

	// old_version is the one refused category: a superseded release, and a file's
	// newest version is often one. The spelling is the API's exact "old_version".
	for _, category := range []string{"old_version", " OLD_VERSION ", "\told_version\n"} {
		assert.Equal(
			t,
			categorySuperseded,
			classifyCategory(category),
			"classifyCategory(%q) should be superseded",
			category,
		)
	}

	// Anything else is unknown rather than refused: treating an unfamiliar category as
	// "not installable" empties the candidate set the first time Nexus adds one.
	for _, category := range []string{
		"", "  ", "archived", "removed", "unknown",
		"mainfile", "optionals", "main file", "mod", "soundtrack",
	} {
		assert.Equal(
			t,
			categoryUnknown,
			classifyCategory(category),
			"classifyCategory(%q) should be unknown",
			category,
		)
	}
}

// The categories named in an error are the reader's only clue about whether the
// fix belongs in this build or in the link they pasted.
func TestDescribeCategories(t *testing.T) {
	assert.Equal(
		t,
		"none — the file published no versions at all",
		describeCategories(nil),
	)

	assert.Equal(
		t,
		"(blank)",
		describeCategories([]fileVersion{{Category: ""}, {Category: "  "}}),
	)

	assert.Equal(
		t,
		"(blank), main, old_version",
		describeCategories([]fileVersion{
			{Category: "main"},
			{Category: "MAIN"},
			{Category: "old_version"},
			{Category: ""},
		}),
		"duplicates collapse case-insensitively and the blank category is named",
	)

	assert.Equal(
		t,
		"archived, main",
		describeCategories([]fileVersion{
			{Category: "main"},
			{Category: "archived"},
		}),
		"the order is fixed so the same list always prints the same sentence",
	)
}

func TestPositionValueReadsTheDecimalString(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
		ok   bool
	}{
		{"2", 2, true},
		{"2.0", 2, true},
		{" 3.5 ", 3.5, true},
		{"0", 0, true},
		{"-1", -1, true},
		{"1e2", 100, true},
		{"", 0, false},
		{"   ", 0, false},
		{"latest", 0, false},
		// strconv.ParseFloat accepts these, and every comparison against NaN is false,
		// so accepting one would make pickVersion keep whichever version it saw first.
		{"NaN", 0, false},
		{"nan", 0, false},
		{"Inf", 0, false},
		{"-Inf", 0, false},
		{"+Inf", 0, false},
		{"infinity", 0, false},
		{"2,0", 0, false},
	}
	for _, tc := range cases {
		got, ok := positionValue(tc.raw)
		if ok != tc.ok || (ok && got != tc.want) {
			assert.Fail(
				t,
				"positionValue(%q) = %v, %v; want %v, %v",
				tc.raw,
				got,
				ok,
				tc.want,
				tc.ok,
			)
		}
	}
}

func TestPickVersionPrefersTheNewestOverThePrimary(t *testing.T) {
	// A primary version is the mod page's default download and can be years old.
	// Returning it would make the manager's update check find nothing ever.
	primary := nexusVersion()
	primary.ID = "8001"
	primary.Position = "1.0"
	primary.Version = "1.0.0"
	primary.UploadedAt = "2020-01-01T00:00:00Z"

	newest := nexusVersion()
	newest.ID = "8009"
	newest.Position = "9.0"
	newest.Version = "9.0.0"
	newest.IsPrimary = false

	got, ok := pickVersion([]fileVersion{primary, newest})
	if !ok {
		require.FailNow(t, "pickVersion found nothing in a list with two installable versions")
	}

	assert.Equal(
		t,
		"8009",
		got.ID,
		"pickVersion chose %q (version %s), want the newest 8009",
		got.ID,
		got.Version,
	)
}

// old_version is a release the author has superseded. Installing it in place of
// the current one is the mistake the category check exists to prevent, so it is
// the one category that is refused outright.
func TestPickVersionSkipsSupersededVersions(t *testing.T) {
	for _, category := range []string{"old_version", " OLD_VERSION "} {
		versions := []fileVersion{
			{ID: "8001", Position: "1.0", Category: "main", Version: "1.0.0"},
			{ID: "8002", Position: "2.0", Category: category, Version: "2.0.0"},
		}

		got, ok := pickVersion(versions)
		require.True(t, ok, "pickVersion with a %q newest version found nothing", category)

		assert.Equal(
			t,
			"8001",
			got.ID,
			"pickVersion chose %q (%q) over the installable 8001",
			got.ID,
			category,
		)
	}
}

// Every version superseded means there is nothing to install, and that has to be
// reported rather than papered over with an unknown category.
func TestPickVersionFindsNothingInASupersededOnlyList(t *testing.T) {
	got, ok := pickVersion([]fileVersion{
		{ID: "8001", Position: "1.0", Category: "old_version"},
		{ID: "8002", Position: "2.0", Category: "old_version"},
	})

	assert.False(t, ok, "pickVersion chose %q from a list with nothing installable", got.ID)
}

// A category this build has never heard of is offered rather than skipped: refusing
// everything unrecognised emptied the candidate set the first time Nexus introduced a
// category, and the mod reported no downloadable version while a grant sat unused.
func TestPickVersionOffersAnUnrecognisedCategory(t *testing.T) {
	for _, category := range []string{"archived", "removed", "unknown", "", "mainfile", "soundtrack"} {
		versions := []fileVersion{
			{ID: "8001", Position: "1.0", Category: "main", Version: "1.0.0"},
			{ID: "8002", Position: "2.0", Category: category, Version: "2.0.0"},
		}

		got, ok := pickVersion(versions)
		require.True(t, ok, "pickVersion with a %q newest version found nothing", category)

		assert.Equal(
			t,
			"8002",
			got.ID,
			"pickVersion chose %q, want the newer 8002 (%q): an unfamiliar category "+
				"is not a reason to refuse a version",
			got.ID,
			category,
		)
	}
}

func TestPickVersionPrefersAPositionOverAnUploadTime(t *testing.T) {
	// A re-uploaded old file has the newest timestamp and the oldest position; the
	// API's own ordering wins because upload time is a second opinion that can be wrong.
	reUploaded := fileVersion{
		ID: "8002", Position: "1.0", Category: "main",
		UploadedAt: "2026-09-09T00:00:00Z",
	}
	positioned := fileVersion{
		ID: "8003", Position: "2.0", Category: "main",
		UploadedAt: "2020-01-01T00:00:00Z",
	}

	got, ok := pickVersion([]fileVersion{reUploaded, positioned})
	if !ok || got.ID != "8003" {
		assert.Fail(t,
			"pickVersion = %q, %v; want 8003: a real position outranks a re-upload date",
			got.ID,
			ok,
		)
	}
	// The order of the list must not matter.
	got, ok = pickVersion([]fileVersion{positioned, reUploaded})
	if !ok || got.ID != "8003" {
		assert.Fail(t, "pickVersion = %q, %v; want 8003 regardless of list order", got.ID, ok)
	}
}

func TestPickVersionFallsBackToUploadOrderWhenNoPositionParses(t *testing.T) {
	older := fileVersion{
		ID: "8001", Position: "latest", Category: "main",
		UploadedAt: "2024-01-01T00:00:00Z",
	}
	newer := fileVersion{
		ID: "8002", Position: "newest", Category: "main",
		UploadedAt: "2026-01-01T00:00:00Z",
	}

	got, ok := pickVersion([]fileVersion{older, newer})
	if !ok || got.ID != "8002" {
		assert.Fail(t,
			"pickVersion = %q, %v; want 8002: with no usable position the upload date decides",
			got.ID,
			ok,
		)
	}
}

func TestPickVersionOnDegenerateInput(t *testing.T) {
	if got, ok := pickVersion(nil); ok || got.ID != "" {
		assert.Fail(t, "pickVersion(nil) = %q, %v; want the zero version and false", got.ID, ok)
	}

	if got, ok := pickVersion([]fileVersion{}); ok || got.ID != "" {
		assert.Fail(t, "pickVersion(empty) = %q, %v; want the zero version and false", got.ID, ok)
	}

	// Only superseded versions are refused, so "unusable" means the category that
	// says so; unrecognised categories are still offered.
	onlySuperseded := []fileVersion{
		{ID: "8001", Category: "old_version"},
		{ID: "8002", Category: "old_version"},
	}
	got, ok := pickVersion(onlySuperseded)
	assert.False(
		t,
		ok,
		"pickVersion over superseded versions = %q; nothing here can be installed",
		got.ID,
	)
	// Equal positions must resolve to one of the two, not to neither and not to a
	// merge of them.
	a := fileVersion{ID: "8001", Position: "1.0", Category: "main", Version: "1.0.0"}
	b := fileVersion{ID: "8002", Position: "1.0", Category: "main", Version: "1.0.0"}

	got, ok = pickVersion([]fileVersion{a, b})
	if !ok || (got.ID != "8001" && got.ID != "8002") {
		assert.Fail(t,
			"pickVersion over tied positions = %q, %v; want one of the two versions",
			got.ID,
			ok,
		)
	}
}

func TestParseStamp(t *testing.T) {
	parsed := parseStamp("2026-02-01T09:30:00Z")
	if parsed.IsZero() {
		require.FailNow(t, "parseStamp of a valid stamp returned the zero time")
	}

	want := time.Date(2026, 2, 1, 9, 30, 0, 0, time.UTC)
	assert.True(t, parsed.Equal(want), "parseStamp = %v, want %v", parsed, want)

	for _, raw := range []string{"", "   ", "2026-02-01", "01/02/2026", "yesterday", "0"} {
		got := parseStamp(raw)
		assert.True(t, got.IsZero(), "parseStamp(%q) = %v, want the zero time", raw, got)
	}
	// An offset timestamp is still a timestamp.
	if got := parseStamp("2026-02-01T10:30:00+01:00"); got.IsZero() {
		assert.Fail(t, "parseStamp refused a valid RFC3339 timestamp with an offset")
	}
}

func TestModForCarriesBothIdHalves(t *testing.T) {
	mod := modFor(nexusContext(), nexusFile(), nexusVersion())
	if mod == nil {
		require.FailNow(t, "modFor returned nil for a complete context, file and version")
	}

	assert.Equal(
		t,
		"nexus-900-7001",
		mod.ID,
		"mod.ID = %q, want nexus-900-7001: both the mod id and the file id are part of the identity",
		mod.ID,
	)

	assert.Equal(t, SourceID, mod.SourceID, "mod.SourceID = %q, want %q", mod.SourceID, SourceID)

	assert.Equal(t, "1.2.0", mod.Version, "mod.Version = %q, want 1.2.0", mod.Version)

	assert.Equal(t, "Better UI", mod.Name, "mod.Name = %q, want the context's name", mod.Name)

	if mod.Author != "modder" || mod.Description != "UI tweaks" {
		assert.Fail(t,
			"mod.Author/Description = %q/%q, want modder/UI tweaks",
			mod.Author,
			mod.Description,
		)
	}

	if !mod.HasTag(SourceID) || !mod.HasTag("main") {
		assert.Fail(t, "mod.Tags = %v, want it to contain %q and main", mod.Tags, SourceID)
	}
}

func TestModForNamesFallBack(t *testing.T) {
	// The file and version responses carry no mod name, so the fallbacks are what
	// keep a mod identifiable when the mod fetch was thin.
	version := nexusVersion()
	version.Name = "Better UI 1.2.0"

	mod := modFor(modContext{scopedID: "900"}, nexusFile(), version)
	if mod == nil {
		require.FailNow(t, "modFor returned nil without a name in the context")
	}

	assert.Equal(t, "Better UI 1.2.0", mod.Name, "mod.Name = %q, want the version's name", mod.Name)

	version.Name = ""
	file := nexusFile()
	file.Name = "Better UI Main"

	mod = modFor(modContext{scopedID: "900"}, file, version)
	if mod == nil || mod.Name != "Better UI Main" {
		assert.Fail(t, "mod.Name = %q, want the file's name as a last resort", mod.Name)
	}
}

func TestModForRecordsWhatALaterInstallNeeds(t *testing.T) {
	// Each is read back later: version_id by Download, download_url by the cache key,
	// mod_id and file_id by a shared link.
	mod := modFor(nexusContext(), nexusFile(), nexusVersion())
	if mod == nil {
		require.FailNow(t, "modFor returned nil")
	}

	want := map[string]any{
		"mod_id":                "900",
		"file_id":               "7001",
		"version_id":            "8001",
		"mod_uid":               "1234567890123",
		"game_id":               "3834",
		domain.SourceDomainKey:  "darktide",
		"position":              "2.0",
		"uploaded_at":           "2026-02-01T09:30:00Z",
		"category":              "main",
		"is_primary":            true,
		"file_name":             "Better UI Main",
		"versions_count":        3,
		"last_file_uploaded_at": "2026-02-01T10:00:00Z",
		"release_url":           "https://www.nexusmods.com/darktide/mods/900",
		"thumbnail_url":         "https://images.invalid/thumb.png",
	}
	for key, wantValue := range want {
		got, ok := mod.SourceData[key]
		if !ok {
			assert.Fail(t, "SourceData is missing %q, which something reads back later", key)
			continue
		}

		assert.Equal(t, wantValue, got, "SourceData[%q] = %#v, want %#v", key, got, wantValue)
	}

	gotURL, wantURL := mod.SourceData["download_url"], "https://api.nexusmods.com/v3/mod-file-versions/8001/download-repacked"
	assert.Equal(t, wantURL, gotURL, "SourceData[download_url] = %#v, want %#v", gotURL, wantURL)

	_, ok := mod.SourceData["dependency_error"]
	assert.False(
		t,
		ok,
		"SourceData carries dependency_error for a mod whose dependencies were never asked for",
	)
}

func TestModForOmitsWhatItDoesNotKnow(t *testing.T) {
	// A zero value published as if it were known is worse than an absent field:
	// a file with no versions is not a file with zero versions.
	ctx := nexusContext()
	ctx.thumbnailURL = ""
	file := fileAggregate{ID: "7001", Name: "Better UI Main", IsActive: true}

	mod := modFor(ctx, file, nexusVersion())
	if mod == nil {
		require.FailNow(t, "modFor returned nil")
	}

	for _, key := range []string{"thumbnail_url", "versions_count", "last_file_uploaded_at"} {
		_, ok := mod.SourceData[key]
		assert.False(t, ok, "SourceData[%q] is present but the API said nothing about it", key)
	}
	// A page URL is always derivable from the two halves, so it stays.
	if _, ok := mod.SourceData["release_url"]; !ok {
		assert.Fail(t, "SourceData[release_url] is missing even though the mod and game are known")
	}
}

func TestModForRefusesWhatCannotBeInstalledLater(t *testing.T) {
	// No version id means no download is possible, so the mod is not offered at
	// all rather than offered and then failing on click.
	version := nexusVersion()

	version.ID = "   "
	if mod := modFor(nexusContext(), nexusFile(), version); mod != nil {
		assert.Fail(t, "modFor with a blank version id = %q, want nil", mod.ID)
	}

	version.ID = ""
	if mod := modFor(nexusContext(), nexusFile(), version); mod != nil {
		assert.Fail(t, "modFor with no version id = %q, want nil", mod.ID)
	}
	// Neither half of the id means nothing can be resolved later, so no mod is
	// offered at all.
	bare := nexusVersion()

	bare.GameScopedID = "   "
	if mod := modFor(modContext{}, nexusFile(), bare); mod != nil {
		assert.Fail(t,
			"modFor with no mod id anywhere = %q, want nil: nothing could be resolved later",
			mod.ID,
		)
	}
	// The version does know which mod page it belongs to, so it can supply the
	// half when the mod fetch came back thin.
	named := nexusVersion()
	named.GameScopedID = "912"

	mod := modFor(modContext{}, nexusFile(), named)
	if mod == nil {
		require.FailNow(t, "modFor refused a context with no mod id when the version named one")
	}

	assert.Equal(
		t,
		"nexus-912-7001",
		mod.ID,
		"mod.ID = %q, want nexus-912-7001 from the version's own game-scoped id",
		mod.ID,
	)
}

func TestModForRecordsAnUnversionedFileAsZero(t *testing.T) {
	// The manager stores a version string, and 0.0.0 sorts below every real
	// version, so an update check on this mod still finds the next release.
	version := nexusVersion()
	version.Version = "   "

	mod := modFor(nexusContext(), nexusFile(), version)
	if mod == nil {
		require.FailNow(t, "modFor returned nil for a version with no version string")
	}

	assert.Equal(t, unversioned, mod.Version, "mod.Version = %q, want %q", mod.Version, unversioned)
}

func TestModForLowerCasesTheCategoryTag(t *testing.T) {
	version := nexusVersion()
	version.Category = " Miscellaneous "

	mod := modFor(nexusContext(), nexusFile(), version)
	if mod == nil {
		require.FailNow(t, "modFor returned nil")
	}

	assert.True(
		t,
		mod.HasTag("miscellaneous"),
		"mod.Tags = %v, want it to contain the lower-cased category",
		mod.Tags,
	)

	got := mod.SourceData["category"]
	assert.Equal(
		t,
		"Miscellaneous",
		got,
		"SourceData[category] = %#v, want the category as the API spelled it",
		got,
	)
}

func dependencyResponseFixture() dependencyResponse {
	otherGame := minimalGame{
		ID:         "115",
		Name:       "Skyrim Special Edition",
		DomainName: "skyrimspecialedition",
	}

	return dependencyResponse{
		DependencyDefinitions: []dependencyDefinition{
			{ID: "d1", Ranges: []dependencyRange{
				{ID: "r1", TargetModFile: fileWithMod{
					ID: "8001", Name: "Dependency Main",
					Mod: minimalMod{GameScopedID: "950", Game: minimalGame{DomainName: "darktide"}},
				}},
			}},
			{ID: "d2", Ranges: []dependencyRange{
				// Two alternatives of one requirement.
				{ID: "r2", TargetModFile: fileWithMod{
					ID:  "8101",
					Mod: minimalMod{GameScopedID: "951", Game: minimalGame{DomainName: "darktide"}},
				}},
				{ID: "r3", TargetModFile: fileWithMod{
					ID:  "8102",
					Mod: minimalMod{GameScopedID: "952", Game: minimalGame{DomainName: "darktide"}},
				}},
			}},
			{ID: "d3", Ranges: []dependencyRange{
				// The same file named by a second requirement.
				{ID: "r4", TargetModFile: fileWithMod{
					ID:  "8001",
					Mod: minimalMod{GameScopedID: "950", Game: minimalGame{DomainName: "darktide"}},
				}},
				// A dependency on another game.
				{ID: "r5", TargetModFile: fileWithMod{
					ID: "8201", Mod: minimalMod{GameScopedID: "10", Game: otherGame},
				}},
				// A dependency whose mod has no id to resolve.
				{ID: "r6", TargetModFile: fileWithMod{
					ID:  "8301",
					Mod: minimalMod{GameScopedID: "", Game: minimalGame{DomainName: "darktide"}},
				}},
			}},
		},
		DLCDependencyDefinitions: []dlcDependencyDefinition{
			{ID: "l1", DLCTargets: []dlcTarget{
				{ID: "1", DLCID: "10", Name: "Some DLC"},
				{ID: "2", DLCID: "11", Name: "  "},
			}},
		},
	}
}

func TestDependencyModsFlattensAlternatives(t *testing.T) {
	// The API treats those ranges as alternatives while Dependencies is a flat "all of
	// these" list, so a superset can hide a file that is installed.
	got := dependencyMods(dependencyResponseFixture(), "darktide")

	want := []string{"nexus-950-8001", "nexus-951-8101", "nexus-952-8102"}
	require.Len(t, got, len(want), "dependencyMods = %v, want %v", got, want)

	for i := range want {
		assert.Equal(t, want[i], got[i], "dependencyMods[%d] = %q, want %q", i, got[i], want[i])
	}
}

func TestDependencyModsSkipsWhatCannotBeResolved(t *testing.T) {
	got := dependencyMods(dependencyResponseFixture(), "darktide")
	for _, id := range got {
		if id == "nexus-8201-8201" {
			assert.Fail(t, "dependencyMods kept a dependency on another game")
		}

		if strings.HasSuffix(id, "-8301") {
			assert.Fail(t, "dependencyMods kept a dependency whose mod has no id to resolve")
		}
	}
	// The same file named by two requirements appears once.
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}

	for id, count := range seen {
		assert.False(t, count > 1, "dependencyMods listed %q %d times", id, count)
	}
}

func TestDependencyModsOnEmptyInput(t *testing.T) {
	if got := dependencyMods(dependencyResponse{}, "darktide"); got != nil {
		assert.Fail(t,
			"dependencyMods(empty) = %v, want nil: a mod that needs nothing should say nothing",
			got,
		)
	}
	// A blank game domain disables the cross-game filter, which is what a caller
	// that has not chosen a game wants.
	onlyOther := dependencyResponse{DependencyDefinitions: []dependencyDefinition{
		{ID: "d1", Ranges: []dependencyRange{{ID: "r1", TargetModFile: fileWithMod{
			ID: "8201",
			Mod: minimalMod{
				GameScopedID: "10",
				Game:         minimalGame{DomainName: "skyrimspecialedition"},
			},
		}}}},
	}}
	got := dependencyMods(onlyOther, "")
	assert.Len(
		t,
		got,
		1,
		"dependencyMods with no configured game = %v, want the one dependency kept",
		got,
	)
	// The game comparison is case-insensitive because a slug is lower case on the
	// site but a caller may have capitalised it.
	got = dependencyMods(onlyOther, "SKYRIMSPECIALEDITION")
	assert.Len(
		t,
		got,
		1,
		"dependencyMods with a differently-cased game = %v, want the dependency kept",
		got,
	)

	got = dependencyMods(onlyOther, "darktide")
	assert.Empty(t, got, "dependencyMods for another game = %v, want none kept", got)
}

func TestDLCNames(t *testing.T) {
	got := dlcNames(dependencyResponseFixture())
	if len(got) != 1 || got[0] != "Some DLC" {
		assert.Fail(t,
			"dlcNames = %v, want [Some DLC]: a blank name is not a DLC the user can act on",
			got,
		)
	}

	if got := dlcNames(dependencyResponse{}); got != nil {
		assert.Fail(t, "dlcNames(empty) = %v, want nil", got)
	}
}

func TestFirstText(t *testing.T) {
	got := firstText("", "   ", "  chosen  ", "later")
	assert.Equal(t, "chosen", got, "firstText = %q, want the trimmed first non-blank value", got)

	got = firstText()
	assert.Equal(t, "", got, "firstText() = %q, want empty", got)

	got = firstText("", "  ")
	assert.Equal(t, "", got, "firstText of only blanks = %q, want empty", got)
}
