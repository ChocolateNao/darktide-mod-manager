package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedTime is the instant the test clock reports, at millisecond precision
// because that is what the timestamp columns store.
var fixedTime = time.Date(2024, 3, 4, 5, 6, 7, 891000000, time.UTC)

// newRepo returns a repository over a private in-memory database whose clock is
// pinned to fixedTime.
func newRepo(t *testing.T) (ModRepository, *sql.DB) {
	t.Helper()
	db := openMemory(t)
	repo := &sqliteModRepository{db: db, now: func() time.Time { return fixedTime }}

	t.Cleanup(func() { _ = repo.Close() })

	return repo, db
}

// fullMod returns a mod with every field populated, for round-trip tests.
func fullMod() *domain.Mod {
	return &domain.Mod{
		ID:          "better_ui",
		GameID:      "darktide",
		Name:        "Better UI",
		Version:     "1.4.2",
		Author:      "someone",
		Description: "a nicer hud",
		SourceID:    "vortex",
		SourceData: map[string]any{
			"downloads": float64(4321),
			"nested":    map[string]any{"k": "v"},
		},
		GameVersion:   ">=1.0.0",
		Dependencies:  []string{"lib_a", "lib_b"},
		ConflictsWith: []string{"worse_ui"},
		Tags:          []string{"hud", "ui"},
		Enabled:       true,
		InstalledAt:   fixedTime.Add(-72 * time.Hour),
		UpdatedAt:     fixedTime,
		LocalPath:     "/storage/mods/better_ui/1.4.2",
		Hash:          "sha256-abc",
		Priority:      50,
		Hooks: domain.ModHooks{
			PostInstall: []domain.HookScript{{
				Language:   domain.LangJS,
				Content:    "console.log('hi')",
				Timeout:    10,
				WorkingDir: "/tmp",
				Env:        []string{"A=1", "B=2"},
			}},
			PreUninstall: []domain.HookScript{
				{Language: domain.LangLua, Content: "print('bye')", Timeout: 5},
			},
		},
	}
}

func assertModEqual(t *testing.T, want, got *domain.Mod) {
	t.Helper()

	require.NotNil(t, got, "expected a mod, got nil (wanted %+v)", want)

	simple := []struct {
		want  any
		got   any
		field string
	}{
		{want.ID, got.ID, "ID"},
		{want.GameID, got.GameID, "GameID"},
		{want.Name, got.Name, "Name"},
		{want.Version, got.Version, "Version"},
		{want.Author, got.Author, "Author"},
		{want.Description, got.Description, "Description"},
		{want.SourceID, got.SourceID, "SourceID"},
		{want.GameVersion, got.GameVersion, "GameVersion"},
		{want.LocalPath, got.LocalPath, "LocalPath"},
		{want.Hash, got.Hash, "Hash"},
		{want.Priority, got.Priority, "Priority"},
		{want.Enabled, got.Enabled, "Enabled"},
	}
	for _, c := range simple {
		assert.Equal(t, c.got, c.want, "%s: expected %v, got %v", c.field, c.want, c.got)
	}

	for _, c := range []struct {
		field string
		want  []string
		got   []string
	}{
		{"Dependencies", want.Dependencies, got.Dependencies},
		{"ConflictsWith", want.ConflictsWith, got.ConflictsWith},
		{"Tags", want.Tags, got.Tags},
	} {
		assert.Equal(
			t,
			strings.Join(c.got, "\x00"),
			strings.Join(c.want, "\x00"),
			"%s: expected %v, got %v",
			c.field,
			c.want,
			c.got,
		)
	}

	assert.True(
		t,
		want.InstalledAt.Equal(got.InstalledAt),
		"InstalledAt: expected %s, got %s",
		want.InstalledAt,
		got.InstalledAt,
	)

	assert.True(
		t,
		want.UpdatedAt.Equal(got.UpdatedAt),
		"UpdatedAt: expected %s, got %s",
		want.UpdatedAt,
		got.UpdatedAt,
	)

	if len(want.SourceData) != len(got.SourceData) {
		assert.Fail(t, "SourceData: expected %v, got %v", want.SourceData, got.SourceData)
	} else {
		for k, v := range want.SourceData {
			gotV, ok := got.SourceData[k]
			if !ok {
				assert.Fail(t, "SourceData is missing key %q", k)
				continue
			}

			assert.Equal(
				t,
				fmt.Sprint(gotV),
				fmt.Sprint(v),
				"SourceData[%q]: expected %v, got %v",
				k,
				v,
				gotV,
			)
		}
	}

	if got.Hooks.Count() != want.Hooks.Count() {
		assert.Fail(t, "Hooks: expected %d scripts, got %d", want.Hooks.Count(), got.Hooks.Count())
		return
	}

	postInstall := got.Hooks.For(domain.PhasePostInstall)
	require.Len(t, postInstall, 1, "expected one post-install hook, got %d", len(postInstall))

	assert.False(
		t,
		postInstall[0].Content != "console.log('hi')" || postInstall[0].Timeout != 10 || postInstall[0].WorkingDir != "/tmp" ||
			strings.Join(postInstall[0].Env, ",") != "A=1,B=2",
		"post-install hook did not round-trip: %+v",
		postInstall[0],
	)

	pre := got.Hooks.For(domain.PhasePreUninstall)
	assert.False(
		t,
		len(pre) != 1 || pre[0].Content != "print('bye')",
		"pre-uninstall hook did not round-trip: %+v",
		pre,
	)

	assert.Equal(
		t,
		domain.LangJS,
		got.Hooks.For(domain.PhasePostInstall)[0].Language,
		"expected the javascript hook language to survive",
	)
}

func modIDs(mods []domain.Mod) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, m.ID)
	}

	return out
}

func TestModRepositoryRoundTrip(t *testing.T) {
	repo, _ := newRepo(t)
	want := fullMod()

	err := repo.Save(context.Background(), want)
	require.NoError(t, err, "save mod: %v", err)

	got, err := repo.GetByID(context.Background(), want.ID)
	require.NoError(t, err, "load mod")

	assertModEqual(t, want, got)

	n, err := repo.Count(context.Background())
	assert.False(t, err != nil || n != 1, "Count() = %d, %v; want 1, nil", n, err)
}

func TestModRepositoryRoundTripsMinimalMod(t *testing.T) {
	repo, _ := newRepo(t)
	want := &domain.Mod{ID: "tiny", Name: "Tiny"}

	err := repo.Save(context.Background(), want)
	require.NoError(t, err, "save mod: %v", err)

	got, err := repo.GetByID(context.Background(), "tiny")
	require.NoError(t, err, "load mod")
	// The columns store "[]" and "{}", so an absent collection reads back as an
	// empty one. That is deliberate: empty is what callers range over, and the
	// mod's JSON is identical either way because the fields are omitempty.
	assert.False(
		t,
		len(got.Dependencies) != 0 || len(got.ConflictsWith) != 0 || len(got.Tags) != 0,
		"expected empty lists, got %v / %v / %v",
		got.Dependencies,
		got.ConflictsWith,
		got.Tags,
	)

	assert.NotNil(
		t,
		got.Dependencies,
		"expected an empty slice rather than nil, so callers need no nil check",
	)

	assert.Empty(t, got.SourceData, "expected an empty source map, got %v", got.SourceData)

	assert.NotNil(t, got.SourceData, "expected an empty map rather than nil")

	assert.True(t, got.Hooks.Empty(), "expected an absent hook set to stay empty")

	assert.True(t, got.Enabled == false, "expected the zero mod to be disabled")
}

func TestModRepositorySaveRejects(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	cases := map[string]struct {
		mod *domain.Mod
	}{
		"nil mod":         {nil},
		"empty id":        {&domain.Mod{ID: ""}},
		"blank id":        {&domain.Mod{ID: "   "}},
		"newline id":      {&domain.Mod{ID: "\n"}},
		"missing name ok": {&domain.Mod{ID: "ok", Name: ""}},
		"unserialisable source data": {&domain.Mod{ID: "bad", SourceData: map[string]any{
			"ch": make(chan int),
		}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := repo.Save(ctx, tc.mod)

			want := appErrors.ErrCodeInvalidMod
			if name == "missing name ok" {
				want = ""
			}

			if want == "" {
				require.NoError(t, err, "save should be accepted")

				return
			}

			requireCode(t, err, want, "save "+name)
		})
	}
}

func TestModRepositorySaveTrimsID(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	if err := repo.Save(ctx, &domain.Mod{ID: "  spaced  "}); err != nil {
		require.FailNow(t, "save mod: %v", err)
	}

	got := modIDs(mustList(t, repo))
	assert.False(
		t,
		len(got) != 1 || got[0] != "spaced",
		"expected the id to be trimmed, got %v",
		got,
	)

	_, err := repo.GetByID(ctx, " spaced ")
	assert.NoError(t, err, "expected a padded lookup to find the trimmed row: %v", err)
}

func TestModRepositorySaveStampsTimestamps(t *testing.T) {
	ctx := context.Background()

	t.Run("zero timestamps use the clock", func(t *testing.T) {
		repo, _ := newRepo(t)
		if err := repo.Save(ctx, &domain.Mod{ID: "a"}); err != nil {
			require.FailNow(t, "save: %v", err)
		}

		got, err := repo.GetByID(ctx, "a")
		require.NoError(t, err, "load")

		assert.True(
			t,
			got.UpdatedAt.Equal(fixedTime),
			"expected UpdatedAt %s, got %s",
			fixedTime,
			got.UpdatedAt,
		)

		assert.True(
			t,
			got.InstalledAt.Equal(fixedTime),
			"expected InstalledAt to mirror UpdatedAt, got %s",
			got.InstalledAt,
		)
	})

	t.Run("zero installed_at mirrors updated_at", func(t *testing.T) {
		repo, _ := newRepo(t)

		mod := &domain.Mod{ID: "b", UpdatedAt: fixedTime.Add(-time.Hour)}
		err := repo.Save(ctx, mod)
		require.NoError(t, err, "save: %v", err)

		got, err := repo.GetByID(ctx, "b")
		require.NoError(t, err, "load")

		assert.True(
			t,
			got.InstalledAt.Equal(mod.UpdatedAt),
			"expected InstalledAt %s, got %s",
			mod.UpdatedAt,
			got.InstalledAt,
		)
	})

	t.Run("upsert keeps the original install time", func(t *testing.T) {
		repo, _ := newRepo(t)

		first := &domain.Mod{ID: "c", InstalledAt: fixedTime.Add(-time.Hour * 24)}
		err := repo.Save(ctx, first)
		require.NoError(t, err, "first save: %v", err)
		// An update that says nothing about the install time must not invent a
		// new one: that would reset the age shown in the UI.
		if err := repo.Save(ctx, &domain.Mod{ID: "c", Name: "renamed"}); err != nil {
			require.FailNow(t, "second save: %v", err)
		}

		got, err := repo.GetByID(ctx, "c")
		require.NoError(t, err, "load")

		assert.True(
			t,
			got.InstalledAt.Equal(first.InstalledAt),
			"expected InstalledAt %s, got %s",
			first.InstalledAt,
			got.InstalledAt,
		)

		assert.Equal(t, "renamed", got.Name, "expected the rename to apply, got %q", got.Name)

		assert.True(
			t,
			got.UpdatedAt.Equal(fixedTime),
			"expected UpdatedAt to move to %s, got %s",
			fixedTime,
			got.UpdatedAt,
		)
	})

	t.Run("explicit install time wins", func(t *testing.T) {
		repo, _ := newRepo(t)
		if err := repo.Save(
			ctx,
			&domain.Mod{ID: "d", InstalledAt: fixedTime.Add(-time.Hour)},
		); err != nil {
			require.FailNow(t, "save: %v", err)
		}

		explicit := fixedTime.Add(-time.Hour * 2)
		if err := repo.Save(ctx, &domain.Mod{ID: "d", InstalledAt: explicit}); err != nil {
			require.FailNow(t, "second save: %v", err)
		}

		got, err := repo.GetByID(ctx, "d")
		require.NoError(t, err, "load")

		assert.True(
			t,
			got.InstalledAt.Equal(explicit),
			"expected InstalledAt %s, got %s",
			explicit,
			got.InstalledAt,
		)
	})
}

func mustList(t *testing.T, repo ModRepository) []domain.Mod {
	t.Helper()

	mods, err := repo.List(context.Background())
	require.NoError(t, err, "list mods")

	return mods
}

func seedMods(t *testing.T, repo ModRepository, mods ...*domain.Mod) {
	t.Helper()

	for _, m := range mods {
		err := repo.Save(context.Background(), m)
		require.NoError(t, err, "seed mod %q: %v", m.ID, err)
	}
}

func TestModRepositoryListOrder(t *testing.T) {
	repo, _ := newRepo(t)
	seedMods(t, repo,
		&domain.Mod{ID: "zebra_enabled_low", Enabled: true, Priority: 1},
		&domain.Mod{ID: "alpha_disabled_high", Enabled: false, Priority: 100},
		&domain.Mod{ID: "beta_enabled_high", Enabled: true, Priority: 100},
		&domain.Mod{ID: "gamma_enabled_low", Enabled: true, Priority: 1},
	)

	// Equal priorities are broken by id, so the order never depends on which mod
	// happened to be inserted first.
	want := []string{
		"beta_enabled_high",
		"gamma_enabled_low",
		"zebra_enabled_low",
		"alpha_disabled_high",
	}
	got := modIDs(mustList(t, repo))
	assert.Equal(
		t,
		strings.Join(want, ","),
		strings.Join(got, ","),
		"List order: expected %v, got %v",
		want,
		got,
	)

	enabled, err := repo.ListEnabled(context.Background())
	require.NoError(t, err, "list enabled")

	wantEnabled := []string{"beta_enabled_high", "gamma_enabled_low", "zebra_enabled_low"}
	got = modIDs(enabled)
	assert.Equal(
		t,
		strings.Join(wantEnabled, ","),
		strings.Join(got, ","),
		"ListEnabled order: expected %v, got %v",
		wantEnabled,
		got,
	)
}

func TestModRepositoryListBySource(t *testing.T) {
	repo, _ := newRepo(t)
	seedMods(t, repo,
		&domain.Mod{ID: "m1", SourceID: "vortex"},
		&domain.Mod{ID: "m2", SourceID: ""},
		&domain.Mod{ID: "m3", SourceID: "nexus"},
		&domain.Mod{ID: "m0", SourceID: "vortex"},
	)

	cases := map[string]string{
		"vortex": "m0,m1",
		"nexus":  "m3",
		"manual": "",
	}
	for source, want := range cases {
		mods, err := repo.ListBySource(context.Background(), source)
		require.NoError(t, err, "list by source %q: %v", source)

		got := strings.Join(modIDs(mods), ",")
		assert.Equal(t, want, got, "source %q: expected %q, got %q", source, want, got)
	}
}

func TestModRepositoryListOrdered(t *testing.T) {
	repo, _ := newRepo(t)
	seedMods(
		t,
		repo,
		&domain.Mod{
			ID:          "b_name",
			Name:        "alpha",
			Priority:    1,
			InstalledAt: fixedTime,
			UpdatedAt:   fixedTime,
		},
		&domain.Mod{
			ID:          "a_name",
			Name:        "zulu",
			Priority:    3,
			InstalledAt: fixedTime.Add(time.Hour),
			UpdatedAt:   fixedTime.Add(-time.Hour),
		},
		&domain.Mod{
			ID:          "c_name",
			Name:        "mike",
			Priority:    2,
			InstalledAt: fixedTime.Add(-time.Hour),
			UpdatedAt:   fixedTime.Add(time.Hour),
		},
	)

	cases := map[string]string{
		SortID:           "a_name,b_name,c_name",
		SortName:         "b_name,c_name,a_name",
		SortPriority:     "a_name,c_name,b_name",
		SortLoadPriority: "a_name,c_name,b_name",
		SortInstalledAt:  "c_name,b_name,a_name",
		SortUpdatedAt:    "a_name,b_name,c_name",
		"  " + SortName:  "b_name,c_name,a_name",
		"":               "a_name,b_name,c_name",
	}
	for key, want := range cases {
		mods, err := repo.ListOrdered(context.Background(), key)
		require.NoError(t, err, "list ordered by %q: %v", key)

		got := strings.Join(modIDs(mods), ",")
		assert.Equal(t, want, got, "sort %q: expected %q, got %q", key, want, got)
	}
}

func TestModRepositoryListOrderedRejectsUnknownKey(t *testing.T) {
	repo, _ := newRepo(t)
	seedMods(t, repo, &domain.Mod{ID: "a"})

	for _, key := range []string{
		"id; DROP TABLE mods",
		"id DESC",
		"1",
		"ID",
		"unknown",
		"id, name",
	} {
		mods, err := repo.ListOrdered(context.Background(), key)
		requireCode(t, err, appErrors.ErrCodeInvalidArgument, "sort by "+key)

		assert.Nil(
			t,
			mods,
			"sort %q: expected no results alongside the error, got %d mods",
			key,
			len(mods),
		)
	}
	// The table must still be there.
	n, err := repo.Count(context.Background())
	assert.False(
		t,
		err != nil || n != 1,
		"expected the mods table to survive, count=%d err=%v",
		n,
		err,
	)
}

func TestModRepositoryEmptyResultsAreEmptySlices(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	mods, err := repo.List(ctx)
	require.NoError(t, err, "list")

	assert.NotNil(t, mods, "expected an empty slice from List, got nil")

	data, err := json.Marshal(mods)
	assert.False(
		t,
		err != nil || string(data) != "[]",
		"expected List to marshal to [], got %s (err %v)",
		data,
		err,
	)

	ordered, err := repo.ListOrdered(ctx, SortID)
	assert.False(
		t,
		err != nil || ordered == nil,
		"expected an empty slice from ListOrdered, got %v, %v",
		ordered,
		err,
	)

	enabled, err := repo.ListEnabled(ctx)
	assert.False(
		t,
		err != nil || enabled == nil,
		"expected an empty slice from ListEnabled, got %v, %v",
		enabled,
		err,
	)

	bySource, err := repo.ListBySource(ctx, "nope")
	assert.False(
		t,
		err != nil || bySource == nil,
		"expected an empty slice from ListBySource, got %v, %v",
		bySource,
		err,
	)

	files, err := repo.GetFilesByMod(ctx, "ghost")
	assert.False(
		t,
		err != nil || files == nil,
		"expected an empty slice from GetFilesByMod, got %v, %v",
		files,
		err,
	)

	order, err := repo.LoadOrder(ctx)
	assert.False(
		t,
		err != nil || order == nil,
		"expected an empty slice from LoadOrder, got %v, %v",
		order,
		err,
	)
}

func TestModRepositoryGetByID(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "known"})

	_, err := repo.GetByID(ctx, "ghost")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
		"expected MOD_NOT_FOUND for an unknown mod, got %v",
		err,
	)

	for _, id := range []string{"", "   "} {
		_, err := repo.GetByID(ctx, id)
		requireCode(t, err, appErrors.ErrCodeInvalidArgument, "get by id "+id)
	}

	_, err = repo.GetByID(ctx, "known")
	assert.NoError(t, err, "expected the known mod to load: %v", err)
}

func TestModRepositoryUpdateEnabledAndPriority(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "m", InstalledAt: fixedTime.Add(-time.Hour)})

	err := repo.UpdateEnabled(ctx, "m", true)
	require.NoError(t, err, "enable: %v", err)

	got, err := repo.GetByID(ctx, "m")
	require.NoError(t, err, "load")

	assert.True(t, got.Enabled, "expected the mod to be enabled")

	assert.True(
		t,
		got.UpdatedAt.Equal(fixedTime),
		"expected the update to refresh UpdatedAt, got %s",
		got.UpdatedAt,
	)

	err = repo.UpdatePriority(ctx, "m", -7)
	require.NoError(t, err, "set priority: %v", err)

	got, err = repo.GetByID(ctx, "m")
	require.NoError(t, err, "load")

	assert.Equal(t, -7, got.Priority, "expected priority -7, got %d", got.Priority)

	err = repo.UpdateEnabled(ctx, "ghost", true)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
		"expected MOD_NOT_FOUND, got %v",
		err,
	)

	err = repo.UpdatePriority(ctx, "ghost", 1)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
		"expected MOD_NOT_FOUND, got %v",
		err,
	)

	err = repo.UpdateEnabled(ctx, "  ", true)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"expected INVALID_ARGUMENT for a blank id, got %v",
		err,
	)

	err = repo.UpdatePriority(ctx, "  ", 1)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"expected INVALID_ARGUMENT for a blank id, got %v",
		err,
	)
}

func TestModRepositoryUpdateEnabledIsIdempotent(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "m", Enabled: true})

	err := repo.UpdateEnabled(ctx, "m", true)
	assert.NoError(t, err, "re-enabling an enabled mod should be accepted: %v", err)
}

func TestModRepositoryFiles(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "m"})

	files := []domain.ModFile{
		{
			ModID:      "m",
			RelPath:    "b/second.txt",
			StoredPath: "/s/b/second.txt",
			SizeBytes:  20,
			Checksum:   "bbb",
		},
		{
			ModID:      "m",
			RelPath:    "first.txt",
			StoredPath: "/s/first.txt",
			SizeBytes:  10,
			Checksum:   "aaa",
		},
	}
	err := repo.SaveFiles(ctx, "m", files)
	require.NoError(t, err, "save files: %v", err)

	got, err := repo.GetFilesByMod(ctx, "m")
	require.NoError(t, err, "get files")

	require.Len(t, got, 2, "expected 2 files, got %d", len(got))
	// Lexicographic order: "b/second.txt" sorts before "first.txt".
	assert.False(
		t,
		got[0].RelPath != "b/second.txt" || got[1].RelPath != "first.txt",
		"expected files ordered by relative path, got %q then %q",
		got[0].RelPath,
		got[1].RelPath,
	)

	assert.False(
		t,
		got[1].ModID != "m" || got[1].StoredPath != "/s/first.txt" || got[1].SizeBytes != 10 ||
			got[1].Checksum != "aaa",
		"first.txt did not round-trip: %+v",
		got[1],
	)

	// A single-file save replaces the whole inventory: the entry with the same
	// relative path is updated, and nothing else is left behind.
	if err := repo.SaveFile(
		ctx,
		&domain.ModFile{ModID: "m", RelPath: "first.txt", SizeBytes: 11},
	); err != nil {
		require.FailNow(t, "save one file: %v", err)
	}

	got, err = repo.GetFilesByMod(ctx, "m")
	require.NoError(t, err, "get files")

	require.False(
		t,
		len(got) != 1 || got[0].SizeBytes != 11,
		"expected the inventory to be replaced by one entry, got %+v",
		got,
	)

	// An empty inventory clears it.
	err = repo.SaveFiles(ctx, "m", nil)
	require.NoError(t, err, "clear files: %v", err)

	got, err = repo.GetFilesByMod(ctx, "m")
	assert.False(
		t,
		err != nil || len(got) != 0,
		"expected the inventory to be empty, got %+v (%v)",
		got,
		err,
	)
}

func TestModRepositorySaveFilesRejects(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "m"})

	cases := []struct {
		name  string
		modID string
		files []domain.ModFile
	}{
		{"unknown mod", "ghost", []domain.ModFile{{RelPath: "a.txt"}}},
		{"blank mod id", "  ", []domain.ModFile{{RelPath: "a.txt"}}},
		{"blank relative path", "m", []domain.ModFile{{RelPath: "  "}}},
		{"blank path in the middle", "m", []domain.ModFile{{RelPath: "a.txt"}, {RelPath: ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := appErrors.ErrCodeModNotFound

			switch tc.name {
			case "blank mod id":
				want = appErrors.ErrCodeInvalidArgument
			case "blank relative path", "blank path in the middle":
				want = appErrors.ErrCodeInvalidMod
			}

			err := repo.SaveFiles(ctx, tc.modID, tc.files)
			requireCode(t, err, want, tc.name)
		})
	}

	requireCode(t, repo.SaveFile(ctx, nil), appErrors.ErrCodeInvalidMod, "save nil file")
}

func TestModRepositorySaveFilesIsAtomic(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "m"})

	if err := repo.SaveFiles(
		ctx,
		"m",
		[]domain.ModFile{{RelPath: "old.txt", SizeBytes: 1}},
	); err != nil {
		require.FailNow(t, "seed files: %v", err)
	}

	// A batch that fails half way must not delete the previous inventory.
	err := repo.SaveFiles(ctx, "m", []domain.ModFile{{RelPath: "new.txt"}, {RelPath: " "}})
	requireCode(t, err, appErrors.ErrCodeInvalidMod, "partial batch")

	got, err := repo.GetFilesByMod(ctx, "m")
	require.NoError(t, err, "get files")

	assert.False(
		t,
		len(got) != 1 || got[0].RelPath != "old.txt",
		"expected the previous inventory to survive a failed batch, got %+v",
		got,
	)
}

func TestModRepositoryDeleteFile(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "m"})

	if err := repo.SaveFiles(ctx, "m", []domain.ModFile{
		{RelPath: "a.txt"}, {RelPath: "b.txt"},
	}); err != nil {
		require.FailNow(t, "seed files: %v", err)
	}

	err := repo.DeleteFile(ctx, "m", "a.txt")
	require.NoError(t, err, "delete file: %v", err)

	got, err := repo.GetFilesByMod(ctx, "m")
	require.NoError(t, err, "get files")

	assert.False(
		t,
		len(got) != 1 || got[0].RelPath != "b.txt",
		"expected only b.txt to remain, got %+v",
		got,
	)

	// Deleting something that was never there converges on the wanted state.
	err = repo.DeleteFile(ctx, "m", "ghost.txt")
	assert.NoError(t, err, "expected deleting an unknown file to succeed, got %v", err)

	err = repo.DeleteFile(ctx, "ghost", "a.txt")
	assert.NoError(t, err, "expected deleting a file of an unknown mod to succeed, got %v", err)

	for _, args := range [][2]string{{"", "a.txt"}, {"m", ""}, {" ", " "}} {
		err := repo.DeleteFile(ctx, args[0], args[1])
		requireCode(
			t,
			err,
			appErrors.ErrCodeInvalidArgument,
			fmt.Sprintf("delete %q %q", args[0], args[1]),
		)
	}
}

func TestModRepositoryLoadOrder(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "a"}, &domain.Mod{ID: "b"}, &domain.Mod{ID: "c"})

	if err := repo.SetLoadOrder(ctx, []string{"c", "a"}); err != nil {
		require.FailNow(t, "set load order: %v", err)
	}

	order, err := repo.LoadOrder(ctx)
	require.NoError(t, err, "load order")

	assert.Equal(t, "c,a", strings.Join(order, ","), "expected c,a, got %v", order)

	// Replacing the order is a full replacement, not an append.
	if err := repo.SetLoadOrder(ctx, []string{"b"}); err != nil {
		require.FailNow(t, "replace load order: %v", err)
	}

	order, err = repo.LoadOrder(ctx)
	assert.False(
		t,
		err != nil || strings.Join(order, ",") != "b",
		"expected b, got %v (%v)",
		order,
		err,
	)

	// An empty order clears it, and blanks are ignored.
	if err := repo.SetLoadOrder(ctx, []string{"", "  ", "a", ""}); err != nil {
		require.FailNow(t, "set order with blanks: %v", err)
	}

	order, err = repo.LoadOrder(ctx)
	assert.False(
		t,
		err != nil || strings.Join(order, ",") != "a",
		"expected a, got %v (%v)",
		order,
		err,
	)

	err = repo.SetLoadOrder(ctx, nil)
	require.NoError(t, err, "clear load order: %v", err)

	order, err = repo.LoadOrder(ctx)
	assert.False(
		t,
		err != nil || len(order) != 0,
		"expected an empty order, got %v (%v)",
		order,
		err,
	)
}

func TestModRepositoryLoadOrderRejectsInvalidInput(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "a"}, &domain.Mod{ID: "b"})

	requireCode(
		t,
		repo.SetLoadOrder(ctx, []string{"a", "a"}),
		appErrors.ErrCodeDuplicate,
		"duplicate id",
	)
	requireCode(
		t,
		repo.SetLoadOrder(ctx, []string{"a", "  ", "a"}),
		appErrors.ErrCodeDuplicate,
		"duplicate id around a blank",
	)
	requireCode(
		t,
		repo.SetLoadOrder(ctx, []string{"ghost"}),
		appErrors.ErrCodeModNotFound,
		"unknown mod",
	)

	// None of those attempts may leave anything behind.
	order, err := repo.LoadOrder(ctx)
	require.NoError(t, err, "load order")

	assert.Len(t, order, 0, "expected a rejected order not to be stored, got %v", order)

	// A partially-valid order is rejected as a whole: a load order that silently
	// drops a mod would reorder the game without asking.
	if err := repo.SetLoadOrder(ctx, []string{"a", "b"}); err != nil {
		require.FailNow(t, "seed load order: %v", err)
	}

	requireCode(
		t,
		repo.SetLoadOrder(ctx, []string{"b", "ghost"}),
		appErrors.ErrCodeModNotFound,
		"half-valid order",
	)

	order, err = repo.LoadOrder(ctx)
	assert.False(
		t,
		err != nil || strings.Join(order, ",") != "a,b",
		"expected the previous order to survive, got %v (%v)",
		order,
		err,
	)
}

func TestModRepositoryGameFilters(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo,
		&domain.Mod{ID: "a1", GameID: "darktide", Enabled: true},
		&domain.Mod{ID: "a2", GameID: "darktide"},
		&domain.Mod{ID: "b1", GameID: "sietch", Enabled: true},
	)

	got, err := repo.ListByGame(ctx, "darktide")
	require.NoError(t, err, "list darktide mods")
	assert.Equal(t, "a1,a2", strings.Join(modIDs(got), ","),
		"canonical order (enabled first) within one game")

	got, err = repo.ListByGame(ctx, "sietch")
	require.NoError(t, err, "list sietch mods")
	assert.Equal(t, "b1", strings.Join(modIDs(got), ","), "only sietch's mods")

	got, err = repo.ListByGame(ctx, "")
	require.NoError(t, err, "list legacy mods")
	assert.Empty(t, got, "no legacy (empty game_id) rows expected, got %v", modIDs(got))

	got, err = repo.ListEnabledByGame(ctx, "darktide")
	require.NoError(t, err, "list enabled darktide mods")
	assert.Equal(t, "a1", strings.Join(modIDs(got), ","),
		"enabled filter combined with the game filter")

	n, err := repo.CountByGame(ctx, "darktide")
	require.NoError(t, err, "count darktide mods")
	assert.Equal(t, 2, n, "darktide count")

	n, err = repo.CountByGame(ctx, "sietch")
	require.NoError(t, err, "count sietch mods")
	assert.Equal(t, 1, n, "sietch count")

	n, err = repo.CountByGame(ctx, "")
	require.NoError(t, err, "count legacy mods")
	assert.Equal(t, 0, n, "legacy count")

	total, err := repo.Count(ctx)
	require.NoError(t, err, "count all mods")
	assert.Equal(t, 3, total, "unscoped count still spans every game")
}

func TestModRepositoryLoadOrderIsPerGame(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo,
		&domain.Mod{ID: "a1", GameID: "darktide"},
		&domain.Mod{ID: "a2", GameID: "darktide"},
		&domain.Mod{ID: "b1", GameID: "sietch"},
		&domain.Mod{ID: "b2", GameID: "sietch"},
	)

	require.NoError(t, repo.SetLoadOrderForGame(ctx, "darktide", []string{"a2", "a1"}),
		"set darktide order")
	require.NoError(t, repo.SetLoadOrderForGame(ctx, "sietch", []string{"b1", "b2"}),
		"set sietch order")

	order, err := repo.LoadOrderByGame(ctx, "darktide")
	require.NoError(t, err, "load darktide order")
	assert.Equal(t, "a2,a1", strings.Join(order, ","), "darktide order")

	order, err = repo.LoadOrderByGame(ctx, "sietch")
	require.NoError(t, err, "load sietch order")
	assert.Equal(t, "b1,b2", strings.Join(order, ","), "sietch order")

	// Replacing one game's order must not touch the other's rows.
	require.NoError(t, repo.SetLoadOrderForGame(ctx, "darktide", []string{"a1", "a2"}),
		"replace darktide order")

	order, err = repo.LoadOrderByGame(ctx, "sietch")
	require.NoError(t, err, "reload sietch order")
	assert.Equal(t, "b1,b2", strings.Join(order, ","),
		"sietch order must survive a darktide replacement")

	// A mod belonging to another game is rejected...
	requireCode(t, repo.SetLoadOrderForGame(ctx, "darktide", []string{"b1"}),
		appErrors.ErrCodeModNotFound, "cross-game id")

	// ...and the rejected call left the previous order in place.
	order, err = repo.LoadOrderByGame(ctx, "darktide")
	require.NoError(t, err, "reload darktide order")
	assert.Equal(t, "a1,a2", strings.Join(order, ","),
		"darktide order must be unchanged after a rejected write")

	requireCode(t, repo.SetLoadOrderForGame(ctx, "darktide", []string{"a1", "a1"}),
		appErrors.ErrCodeDuplicate, "duplicate id")

	// The unscoped view still sees every row in the table.
	all, err := repo.LoadOrder(ctx)
	require.NoError(t, err, "load all order rows")
	assert.Len(t, all, 4, "expected both games' rows, got %v", all)
}

func TestModRepositoryDeleteCascades(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "keep"}, &domain.Mod{ID: "drop"})

	if err := repo.SaveFiles(ctx, "drop", []domain.ModFile{{RelPath: "a.txt"}}); err != nil {
		require.FailNow(t, "seed files: %v", err)
	}

	if err := repo.SetLoadOrder(ctx, []string{"keep", "drop"}); err != nil {
		require.FailNow(t, "seed load order: %v", err)
	}

	err := repo.Delete(ctx, "drop")
	require.NoError(t, err, "delete: %v", err)

	_, err = repo.GetByID(ctx, "drop")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
		"expected the mod to be gone, got %v",
		err,
	)

	remaining, err := repo.GetFilesByMod(ctx, "drop")
	assert.False(
		t,
		err != nil || len(remaining) != 0,
		"expected the files to be gone, got %v (%v)",
		remaining,
		err,
	)

	var files, loadRows int

	err = db.QueryRow("SELECT COUNT(*) FROM mod_files").Scan(&files)
	require.NoError(t, err, "count file rows: %v", err)

	err = db.QueryRow("SELECT COUNT(*) FROM load_order").Scan(&loadRows)
	require.NoError(t, err, "count load order rows: %v", err)

	assert.False(
		t,
		files != 0 || loadRows != 1,
		"expected 0 file rows and 1 load order row, got %d and %d",
		files,
		loadRows,
	)

	requireCode(t, repo.Delete(ctx, "ghost"), appErrors.ErrCodeModNotFound, "delete an unknown mod")
	requireCode(t, repo.Delete(ctx, " "), appErrors.ErrCodeInvalidArgument, "delete a blank id")
}

func TestModRepositoryDeleteRemovesLoadOrderEntry(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "a"}, &domain.Mod{ID: "b"})

	if err := repo.SetLoadOrder(ctx, []string{"a", "b"}); err != nil {
		require.FailNow(t, "seed load order: %v", err)
	}

	err := repo.Delete(ctx, "a")
	require.NoError(t, err, "delete: %v", err)

	order, err := repo.LoadOrder(ctx)
	require.NoError(t, err, "load order")

	assert.Equal(
		t,
		"b",
		strings.Join(order, ","),
		"expected the deleted mod's entry to go, got %v",
		order,
	)
	// The remaining mod can still be reordered afterwards.
	if err := repo.SetLoadOrder(ctx, []string{"b"}); err != nil {
		assert.Fail(t, "expected the surviving entry to stay reorderable: %v", err)
	}
}

func TestModRepositoryCorruptJSONColumnFailsTheRead(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedMods(t, repo, &domain.Mod{ID: "m", Dependencies: []string{"a"}})

	for _, column := range []string{"dependencies", "conflicts_with", "tags", "source_data", "hooks"} {
		if _, err := db.Exec(
			"UPDATE mods SET "+column+" = ? WHERE id = ?",
			"{not json",
			"m",
		); err != nil {
			require.FailNow(t, "corrupt %s: %v", column, err)
		}

		_, err := repo.GetByID(ctx, "m")
		requireCode(t, err, appErrors.ErrCodeCacheCorrupt, "read with a corrupt "+column)

		mods, err := repo.List(ctx)
		requireCode(t, err, appErrors.ErrCodeCacheCorrupt, "list with a corrupt "+column)

		assert.Nil(t, mods, "expected no results alongside the error, got %v", mods)
	}
}

func TestModRepositoryLegacyEmptyColumns(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()

	seedMods(
		t,
		repo,
		&domain.Mod{ID: "m", Dependencies: []string{"a"}, SourceData: map[string]any{"k": "v"}},
	)

	// A row written before the JSON columns existed has to read back as an
	// ordinary mod with no metadata, not as a corrupt one.
	_, err := db.Exec(
		`UPDATE mods SET dependencies = '', conflicts_with = 'null', tags = '', source_data = 'null', hooks = 'null' WHERE id = ?`,
		"m",
	)
	require.NoError(t, err, "blank the columns: %v", err)

	got, err := repo.GetByID(ctx, "m")
	require.NoError(t, err, "expected legacy columns to be tolerated")

	assert.False(
		t,
		got.Dependencies != nil || got.SourceData != nil || !got.Hooks.Empty(),
		"expected empty metadata, got %+v",
		got,
	)
}

func TestModRepositoryHonoursCancellation(t *testing.T) {
	repo, _ := newRepo(t)
	seedMods(t, repo, &domain.Mod{ID: "m"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.List(ctx)
	requireCode(t, err, appErrors.ErrCodeOperationCanceled, "list")
	requireCode(t, repo.Save(ctx, &domain.Mod{ID: "x"}), appErrors.ErrCodeOperationCanceled, "save")
	_, err = repo.GetByID(ctx, "m")
	requireCode(t, err, appErrors.ErrCodeOperationCanceled, "get by id")
	requireCode(
		t,
		repo.UpdateEnabled(ctx, "m", true),
		appErrors.ErrCodeOperationCanceled,
		"update enabled",
	)
	requireCode(t, repo.SaveFiles(ctx, "m", nil), appErrors.ErrCodeOperationCanceled, "save files")
	_, err = repo.GetFilesByMod(ctx, "m")
	requireCode(t, err, appErrors.ErrCodeOperationCanceled, "get files")
	requireCode(
		t,
		repo.DeleteFile(ctx, "m", "a.txt"),
		appErrors.ErrCodeOperationCanceled,
		"delete file",
	)
	requireCode(
		t,
		repo.SetLoadOrder(ctx, []string{"m"}),
		appErrors.ErrCodeOperationCanceled,
		"set load order",
	)
	_, err = repo.LoadOrder(ctx)
	requireCode(t, err, appErrors.ErrCodeOperationCanceled, "load order")
	requireCode(t, repo.Delete(ctx, "m"), appErrors.ErrCodeOperationCanceled, "delete")
	_, err = repo.Count(ctx)
	requireCode(t, err, appErrors.ErrCodeOperationCanceled, "count")

	// Nothing above may have changed the stored data.
	_, err = repo.List(context.Background())
	assert.NoError(t, err, "expected the repository to still work afterwards: %v", err)
}

func TestModRepositoryAcceptsNilContext(t *testing.T) {
	repo, _ := newRepo(t)
	//nolint:staticcheck // the nil context is exactly what this guards against.
	if err := repo.Save(nil, &domain.Mod{ID: "m"}); err != nil {
		require.FailNow(t, "save with a nil context: %v", err)
	}

	//nolint:staticcheck // the nil context is exactly what this guards against.
	_, err := repo.GetByID(nil, "m")
	require.NoError(t, err, "get with a nil context: %v", err)

	//nolint:staticcheck // the nil context is exactly what this guards against.
	_, err = repo.List(nil)
	require.NoError(t, err, "list with a nil context: %v", err)
}

func TestModRepositoryNilHandle(t *testing.T) {
	var repo *sqliteModRepository

	requireCode(
		t,
		repo.Save(context.Background(), &domain.Mod{ID: "m"}),
		appErrors.ErrCodeConfigIO,
		"save",
	)
	_, err := repo.GetByID(context.Background(), "m")
	requireCode(t, err, appErrors.ErrCodeConfigIO, "get by id")
	_, err = repo.List(context.Background())
	requireCode(t, err, appErrors.ErrCodeConfigIO, "list")
	_, err = repo.ListEnabled(context.Background())
	requireCode(t, err, appErrors.ErrCodeConfigIO, "list enabled")
	_, err = repo.ListBySource(context.Background(), "s")
	requireCode(t, err, appErrors.ErrCodeConfigIO, "list by source")
	_, err = repo.ListOrdered(context.Background(), SortID)
	requireCode(t, err, appErrors.ErrCodeConfigIO, "list ordered")
	requireCode(
		t,
		repo.UpdateEnabled(context.Background(), "m", true),
		appErrors.ErrCodeConfigIO,
		"update enabled",
	)
	requireCode(
		t,
		repo.UpdatePriority(context.Background(), "m", 1),
		appErrors.ErrCodeConfigIO,
		"update priority",
	)
	requireCode(
		t,
		repo.SaveFile(context.Background(), &domain.ModFile{}),
		appErrors.ErrCodeConfigIO,
		"save file",
	)
	requireCode(
		t,
		repo.SaveFiles(context.Background(), "m", nil),
		appErrors.ErrCodeConfigIO,
		"save files",
	)
	_, err = repo.GetFilesByMod(context.Background(), "m")
	requireCode(t, err, appErrors.ErrCodeConfigIO, "get files")
	requireCode(
		t,
		repo.DeleteFile(context.Background(), "m", "a"),
		appErrors.ErrCodeConfigIO,
		"delete file",
	)
	requireCode(
		t,
		repo.SetLoadOrder(context.Background(), nil),
		appErrors.ErrCodeConfigIO,
		"set load order",
	)
	_, err = repo.LoadOrder(context.Background())
	requireCode(t, err, appErrors.ErrCodeConfigIO, "load order")
	requireCode(t, repo.Delete(context.Background(), "m"), appErrors.ErrCodeConfigIO, "delete")
	_, err = repo.Count(context.Background())
	requireCode(t, err, appErrors.ErrCodeConfigIO, "count")

	err = repo.Close()
	assert.NoError(t, err, "closing a nil repository should be a no-op, got %v", err)
}

func TestNewSQLiteModRepository(t *testing.T) {
	t.Run("in memory", func(t *testing.T) {
		repo, err := NewSQLiteModRepository(context.Background(), ":memory:")
		require.NoError(t, err, "open in-memory repository")

		defer func() { _ = repo.Close() }()

		if err := repo.Save(context.Background(), &domain.Mod{ID: "m"}); err != nil {
			require.FailNow(t, "save: %v", err)
		}

		n, err := repo.Count(context.Background())
		assert.False(t, err != nil || n != 1, "count = %d, %v", n, err)
	})

	t.Run("empty path", func(t *testing.T) {
		repo, err := NewSQLiteModRepository(context.Background(), "")
		require.NoError(t, err, "open with an empty path")

		defer func() { _ = repo.Close() }()
	})

	t.Run("file", func(t *testing.T) {
		path := t.TempDir() + "/nested/mods.db"

		//nolint:staticcheck // deliberately passing nil: the API accepts it.
		repo, err := NewSQLiteModRepository(nil, path)
		require.NoError(t, err, "open file repository")

		if err := repo.Save(context.Background(), &domain.Mod{ID: "m"}); err != nil {
			require.FailNow(t, "save: %v", err)
		}

		err = repo.Close()
		require.NoError(t, err, "close: %v", err)
	})

	t.Run("unusable path", func(t *testing.T) {
		_, err := NewSQLiteModRepository(context.Background(), t.TempDir())
		require.Error(t, err, "expected an error for a directory path")
	})

	t.Run("injected clock", func(t *testing.T) {
		repo, err := NewSQLiteModRepositoryWithClock(
			context.Background(),
			":memory:",
			func() time.Time { return fixedTime },
		)
		require.NoError(t, err, "open")

		defer func() { _ = repo.Close() }()

		if err := repo.Save(context.Background(), &domain.Mod{ID: "m"}); err != nil {
			require.FailNow(t, "save: %v", err)
		}

		got, err := repo.GetByID(context.Background(), "m")
		require.NoError(t, err, "load")

		assert.True(
			t,
			got.UpdatedAt.Equal(fixedTime),
			"expected the injected clock's time, got %s",
			got.UpdatedAt,
		)
	})

	t.Run("nil clock falls back to now", func(t *testing.T) {
		repo, err := NewSQLiteModRepositoryWithClock(context.Background(), ":memory:", nil)
		require.NoError(t, err, "open")

		defer func() { _ = repo.Close() }()

		before := time.Now().Add(-time.Minute)

		if err := repo.Save(context.Background(), &domain.Mod{ID: "m"}); err != nil {
			require.FailNow(t, "save: %v", err)
		}

		got, err := repo.GetByID(context.Background(), "m")
		require.NoError(t, err, "load")

		assert.False(
			t,
			got.UpdatedAt.Before(before),
			"expected a real timestamp, got %s",
			got.UpdatedAt,
		)
	})

	t.Run("nil database handle", func(t *testing.T) {
		_, err := NewSQLiteModRepositoryFromDB(nil)
		requireCode(t, err, appErrors.ErrCodeInvalidArgument, "wrap a nil handle")
	})
}

func TestModRepositoryCloseIsIdempotent(t *testing.T) {
	repo, err := NewSQLiteModRepository(context.Background(), ":memory:")
	require.NoError(t, err, "open")

	err = repo.Close()
	require.NoError(t, err, "first close: %v", err)

	err = repo.Close()
	require.NoError(t, err, "second close: %v", err)
	// Using a closed repository is a programming error, not a crash.
	_, err = repo.List(context.Background())
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeConfigIO),
		"expected CONFIG_IO from a closed repository, got %v",
		err,
	)
}

func TestModRepositoryIsSafeForConcurrentUse(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			id := fmt.Sprintf("mod_%d", n)
			if err := repo.Save(ctx, &domain.Mod{ID: id, Priority: n}); err != nil {
				assert.Fail(t, "concurrent save: %v", err)
			}

			_, err := repo.List(ctx)
			assert.NoError(t, err, "concurrent list: %v", err)

			err = repo.UpdateEnabled(ctx, id, n%2 == 0)
			assert.NoError(t, err, "concurrent update: %v", err)
		}(i)
	}

	wg.Wait()

	n, err := repo.Count(ctx)
	assert.False(t, err != nil || n != 8, "expected 8 mods, got %d (%v)", n, err)
}

func TestSortKeys(t *testing.T) {
	keys := SortKeys()
	require.NotEmpty(t, keys, "expected at least one sort key")

	seen := map[string]bool{}

	for _, k := range keys {
		assert.NotEqual(t, "", k, "expected no empty sort key")

		assert.False(t, seen[k], "sort key %q appears twice", k)

		seen[k] = true
	}
	// Every advertised key must actually be accepted.
	for _, k := range keys {
		_, err := sortColumn(k)
		assert.NoError(t, err, "advertised sort key %q is rejected: %v", k, err)
	}
	// Mutating the returned slice must not affect the next caller.
	keys[0] = "clobbered"

	assert.NotEqual(t, "clobbered", SortKeys()[0], "expected SortKeys to return a fresh slice")
}

func TestSortColumn(t *testing.T) {
	cases := map[string]string{
		SortID:           "id ASC",
		SortName:         "name ASC, id ASC",
		SortPriority:     "priority DESC, id ASC",
		SortLoadPriority: "priority DESC, id ASC",
		SortInstalledAt:  "installed_at ASC, id ASC",
		SortUpdatedAt:    "updated_at ASC, id ASC",
		"":               "id ASC",
		"   ":            "id ASC",
		" name ":         "name ASC, id ASC",
	}
	for key, want := range cases {
		got, err := sortColumn(key)
		if err != nil {
			assert.Fail(t, "sortColumn(%q) failed: %v", key, err)
			continue
		}

		assert.Equal(t, want, got, "sortColumn(%q) = %q, want %q", key, got, want)
	}

	_, err := sortColumn("1=1")
	assert.Error(t, err, "expected an injection attempt to be rejected")
}

func TestEncodeDecodeStringList(t *testing.T) {
	cases := []struct {
		want  string
		input []string
	}{
		{"[]", nil},
		{"[]", []string{}},
		{`["a"]`, []string{"a"}},
		{`["a","b"]`, []string{"a", "b"}},
		{`["we\"ird"]`, []string{`we"ird`}},
	}
	for _, tc := range cases {
		got, err := encodeStringList(tc.input)
		if err != nil {
			assert.Fail(t, "encodeStringList(%v): %v", tc.input, err)
			continue
		}

		assert.Equal(t, tc.want, got, "encodeStringList(%v) = %s, want %s", tc.input, got, tc.want)
	}

	decodeCases := map[string][]string{
		"[]":        {},
		"null":      nil,
		"":          nil,
		"   ":       nil,
		`["a","b"]`: {"a", "b"},
	}
	for input, want := range decodeCases {
		got, err := decodeStringList(input)
		if err != nil {
			assert.Fail(t, "decodeStringList(%q): %v", input, err)
			continue
		}

		if len(got) != len(want) {
			assert.Fail(t, "decodeStringList(%q) = %v, want %v", input, got, want)
			continue
		}

		for i := range want {
			assert.Equal(
				t,
				want[i],
				got[i],
				"decodeStringList(%q)[%d] = %q, want %q",
				input,
				i,
				got[i],
				want[i],
			)
		}
	}

	for _, input := range []string{`{"a":1}`, `"a"`, `[1,2]`, `[`, `["a"`} {
		_, err := decodeStringList(input)
		assert.Error(t, err, "expected decodeStringList(%q) to fail", input)
	}
}

func TestEncodeDecodeJSONObject(t *testing.T) {
	if got, err := encodeJSONObject(nil); err != nil || got != "{}" {
		assert.Fail(t, "encodeJSONObject(nil) = %q, %v; want {}", got, err)
	}

	if got, err := encodeJSONObject(map[string]any{"a": 1}); err != nil || got != `{"a":1}` {
		assert.Fail(t, "encodeJSONObject = %q, %v", got, err)
	}

	if _, err := encodeJSONObject(map[string]any{"ch": make(chan int)}); err == nil {
		assert.Fail(t, "expected an unserialisable map to fail")
	}

	for _, input := range []string{"", "null", "   "} {
		got, err := decodeJSONObject(input)
		assert.NoError(t, err, "decodeJSONObject(%q): %v", input, err)

		assert.Nil(t, got, "decodeJSONObject(%q) = %v, want nil", input, got)
	}

	got, err := decodeJSONObject(`{"a":1,"b":[1,2]}`)
	require.NoError(t, err, "decodeJSONObject")

	assert.Len(t, got, 2, "expected 2 keys, got %v", got)

	_, err = decodeJSONObject(`[1,2]`)
	assert.Error(t, err, "expected a JSON array to be rejected")

	if _, err := decodeJSONObject(`{"a":`); err == nil {
		assert.Fail(t, "expected truncated JSON to be rejected")
	}
}

func TestEncodeDecodeHooks(t *testing.T) {
	empty, err := encodeHooks(domain.ModHooks{})
	require.NoError(t, err, "encode empty hooks")

	if empty != "{}" {
		assert.Fail(t, "expected an empty hook set to encode as {}, got %s", empty)
	}

	decoded, err := decodeHooks(empty)
	assert.False(
		t,
		err != nil || !decoded.Empty(),
		"expected empty hooks back, got %+v (%v)",
		decoded,
		err,
	)

	full := domain.ModHooks{
		PostEnable: []domain.HookScript{
			{Language: domain.LangLua, Content: "print('run')", Timeout: 3, Env: []string{"K=V"}},
		},
	}

	encoded, err := encodeHooks(full)
	require.NoError(t, err, "encode hooks")

	roundTripped, err := decodeHooks(encoded)
	require.NoError(t, err, "decode hooks")

	require.Equal(t, 1, roundTripped.Count(), "expected one hook, got %d", roundTripped.Count())

	got := roundTripped.For(domain.PhasePostEnable)[0]
	assert.False(
		t,
		got.Content != "print('run')" || got.Timeout != 3 || got.Language != domain.LangLua ||
			len(got.Env) != 1,
		"hook did not round-trip: %+v",
		got,
	)

	// An explicitly empty phase list means the same thing as an absent one.
	emptyPhase, err := decodeHooks(`{"post_enable":[]}`)
	require.NoError(t, err, "decode empty phase")

	phase := emptyPhase.For(domain.PhasePostEnable)
	assert.Nil(t, phase, "expected an empty phase list to read back as nil, got %v", phase)

	// Unknown keys are dropped rather than fatal, so a newer manifest still
	// loads on an older build.
	withUnknown, err := decodeHooks(`{"future_phase":[{"type":"inline","content":"x"}]}`)
	require.NoError(t, err, "decode with an unknown phase")

	assert.True(
		t,
		withUnknown.Empty(),
		"expected unknown phases to be ignored, got %+v",
		withUnknown,
	)

	for _, input := range []string{"[1]", `{"post_enable":"nope"}`, "{"} {
		_, err := decodeHooks(input)
		assert.Error(t, err, "expected decodeHooks(%q) to fail", input)
	}
}

func TestIsForeignKeyViolation(t *testing.T) {
	cases := map[string]bool{
		"": false,
		"constraint failed: FOREIGN KEY constraint failed": true,
		"constraint failed: foreign key constraint failed": true,
		"UNIQUE constraint failed: mods.id":                false,
		"no such table: mods":                              false,
	}
	for msg, want := range cases {
		var err error
		if msg != "" {
			err = fmt.Errorf("%s", msg)
		}

		got := isForeignKeyViolation(err)
		assert.Equal(t, want, got, "isForeignKeyViolation(%q) = %v, want %v", msg, got, want)
	}
}

func TestBoolToInt(t *testing.T) {
	assert.False(
		t,
		boolToInt(true) != 1 || boolToInt(false) != 0,
		"boolToInt produced %d / %d",
		boolToInt(true),
		boolToInt(false),
	)
}
