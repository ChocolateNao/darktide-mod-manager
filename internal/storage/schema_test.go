package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openMemory opens a private in-memory database with the schema applied, which
// is what most tests in this package want.
func openMemory(t *testing.T) *sql.DB {
	t.Helper()

	db, err := Open(context.Background(), ":memory:")
	require.NoError(t, err, "open in-memory database")

	t.Cleanup(func() { _ = db.Close() })

	return db
}

func tableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	require.NoError(t, err, "list tables")

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var name string

		err := rows.Scan(&name)
		require.NoError(t, err, "scan table name: %v", err)

		out = append(out, name)
	}

	err = rows.Err()
	require.NoError(t, err, "iterate tables: %v", err)

	return out
}

func TestOpenAppliesSchema(t *testing.T) {
	db := openMemory(t)

	for _, want := range []string{"config", "load_order", "mod_files", "mods"} {
		found := false

		for _, name := range tableNames(t, db) {
			if name == want {
				found = true
				break
			}
		}

		assert.True(t, found, "table %q is missing; got %v", want, tableNames(t, db))
	}

	state, err := InspectSchema(context.Background(), db)
	require.NoError(t, err, "inspect schema")

	assert.True(
		t,
		state.IsCurrent(),
		"schema state = %+v, want version %d installed",
		state,
		CurrentSchemaVersion,
	)

	assert.Equal(
		t,
		CurrentSchemaVersion,
		state.Version,
		"schema version = %d, want %d",
		state.Version,
		CurrentSchemaVersion,
	)
}

func TestOpenEnforcesForeignKeys(t *testing.T) {
	db := openMemory(t)

	var enabled int

	err := db.QueryRow("PRAGMA foreign_keys").Scan(&enabled)
	require.NoError(t, err, "read foreign_keys: %v", err)

	assert.Equal(
		t,
		1,
		enabled,
		"foreign_keys = %d, want 1 (cascades must fire on every pooled connection)",
		enabled,
	)

	_, err = db.Exec(
		`INSERT INTO mod_files (mod_id, file_path, stored_path, size_bytes, checksum) VALUES ('ghost', 'a', 'b', 0, '')`,
	)
	assert.Error(
		t,
		err,
		"inserting a file for a mod that does not exist succeeded, want a foreign key error",
	)
}

func TestInMemoryDatabaseSurvivesSeparateStatements(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	// An in-memory database lives inside one connection; a second pooled one
	// would see a brand-new empty database, hence the write-then-read below.
	_, err := db.ExecContext(ctx, "INSERT INTO config(key, value) VALUES('k', 'v')")
	require.NoError(t, err, "insert: %v", err)

	var value string

	err = db.QueryRowContext(ctx, "SELECT value FROM config WHERE key = 'k'").Scan(&value)
	require.NoError(t, err, "select: %v", err)

	assert.Equal(t, "v", value, "value = %q, want %q", value, "v")
}

func TestApplySchemaIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openMemory(t)

	for i := 0; i < 3; i++ {
		err := ApplySchema(ctx, db)
		require.NoError(t, err, "ApplySchema call %d: %v", i+1, err)
	}

	state, err := InspectSchema(ctx, db)
	require.NoError(t, err, "inspect schema")

	assert.Equal(
		t,
		CurrentSchemaVersion,
		state.Version,
		"version = %d after repeated ApplySchema, want %d",
		state.Version,
		CurrentSchemaVersion,
	)

	_, err = db.Exec("INSERT INTO config(key, value) VALUES('k', 'v')")
	require.NoError(t, err, "insert after re-applying schema: %v", err)
}

// A fresh database carries the game_id column and the index game-scoped
// queries filter on.
func TestOpenCreatesGameIDColumnAndIndex(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	var count int

	err := db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM pragma_table_info('mods') WHERE name = 'game_id'",
	).Scan(&count)
	require.NoError(t, err, "probe game_id column")
	require.Equal(t, 1, count, "mods.game_id must exist on a fresh database")

	var defs string

	err = db.QueryRowContext(
		ctx,
		"SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_mods_game'",
	).Scan(&defs)
	require.NoError(t, err, "probe idx_mods_game: %v", err)
	assert.Contains(t, defs, "mods(game_id)", "index must cover game_id")
}

// A pre-multi-game mods table lacks game_id and its index; ApplySchema upgrades
// it in place without bumping the schema version.
func TestApplySchemaUpgradesStaleModsTable(t *testing.T) {
	ctx := context.Background()
	db := openMemory(t)

	// Roll the database back to the pre-multi-game shape: no game_id column
	// and no index over it.
	_, err := db.ExecContext(ctx, "DROP INDEX idx_mods_game")
	require.NoError(t, err, "drop game index: %v", err)

	_, err = db.ExecContext(ctx, "ALTER TABLE mods DROP COLUMN game_id")
	require.NoError(t, err, "drop game_id column: %v", err)

	_, err = db.ExecContext(ctx, "ALTER TABLE mods DROP COLUMN source_url")
	require.NoError(t, err, "drop source_url column: %v", err)

	_, err = db.ExecContext(ctx, "ALTER TABLE mods DROP COLUMN download_url")
	require.NoError(t, err, "drop download_url column: %v", err)

	_, err = db.ExecContext(ctx, "ALTER TABLE mods DROP COLUMN update_url")
	require.NoError(t, err, "drop update_url column: %v", err)

	_, err = db.ExecContext(
		ctx,
		`INSERT INTO mods (id, name, version, installed_at)
		 VALUES ('legacy', 'Legacy Mod', '1.0.0', '2024-01-01T00:00:00.000Z')`,
	)
	require.NoError(t, err, "insert pre-upgrade row: %v", err)

	require.NoError(t, ApplySchema(ctx, db), "upgrade stale database")

	var count int

	err = db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM pragma_table_info('mods') WHERE name = 'game_id'",
	).Scan(&count)
	require.NoError(t, err, "probe game_id column")
	require.Equal(t, 1, count, "game_id column must be added by the upgrade")

	var gameID string

	err = db.QueryRowContext(ctx, "SELECT game_id FROM mods WHERE id = 'legacy'").Scan(&gameID)
	require.NoError(t, err, "read upgraded row: %v", err)
	assert.Equal(t, "", gameID, "pre-upgrade rows get the empty default")

	// A pre-existing row has to survive the upgrade: a migration that loses data is
	// worse than one that cannot run.
	for _, column := range []string{"source_url", "download_url", "update_url"} {
		err = db.QueryRowContext(
			ctx,
			"SELECT COUNT(*) FROM pragma_table_info('mods') WHERE name = ?",
			column,
		).Scan(&count)
		require.NoError(t, err, "probe %s column", column)
		require.Equal(t, 1, count, "%s column must be added by the upgrade", column)
	}

	var url string

	err = db.QueryRowContext(ctx, "SELECT source_url FROM mods WHERE id = 'legacy'").Scan(&url)
	require.NoError(t, err, "read upgraded source_url: %v", err)
	assert.Empty(t, url, "pre-upgrade rows get the empty default")

	var index int

	err = db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_mods_game'",
	).Scan(&index)
	require.NoError(t, err, "probe index")
	assert.Equal(t, 1, index, "idx_mods_game must be created by the DDL pass")

	// Second apply is a no-op and the version stamp is unchanged.
	require.NoError(t, ApplySchema(ctx, db), "re-apply after upgrade")

	state, err := InspectSchema(ctx, db)
	require.NoError(t, err, "inspect schema")
	assert.Equal(
		t,
		CurrentSchemaVersion,
		state.Version,
		"version = %d, want %d (upgrade must not bump the schema version)",
		state.Version,
		CurrentSchemaVersion,
	)
}

func TestApplySchemaAcceptsAnyVersionStamp(t *testing.T) {
	ctx := context.Background()
	db := openMemory(t)

	_, err := db.Exec("PRAGMA user_version = 999")
	require.NoError(t, err, "set user_version: %v", err)

	err = ApplySchema(ctx, db)
	require.NoError(
		t,
		err,
		"ApplySchema on a database with a higher version stamp = %v, want no error",
		err,
	)
}

func TestApplySchemaRejectsNilHandle(t *testing.T) {
	err := ApplySchema(context.Background(), nil)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeConfigIO),
		"ApplySchema(nil) = %v, want CONFIG_IO",
		err,
	)
}

func TestApplySchemaHonoursCancellation(t *testing.T) {
	db := openMemory(t)

	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := ApplySchema(ctx, db)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"ApplySchema with a cancelled context = %v, want OPERATION_CANCELED",
			err,
		)
	})

	t.Run("nil context", func(t *testing.T) {
		//nolint:staticcheck // deliberately passing nil: the API accepts it.
		err := ApplySchema(nil, db)
		assert.NoError(t, err, "ApplySchema(nil ctx) = %v, want success", err)
	})
}

func TestInspectSchemaOnUninitialisedDatabase(t *testing.T) {
	raw, err := OpenDB(context.Background(), ":memory:")
	require.NoError(t, err, "open")

	defer func() { _ = raw.Close() }()

	state, err := InspectSchema(context.Background(), raw)
	require.NoError(t, err, "inspect")

	assert.False(t, state.Installed, "Installed = true on a database with no schema, want false")

	assert.Equal(t, 0, state.Version, "Version = %d on a fresh database, want 0", state.Version)

	assert.False(t, state.IsCurrent(), "IsCurrent() = true on a fresh database, want false")
}

func TestInspectSchemaRejectsNilHandle(t *testing.T) {
	_, err := InspectSchema(context.Background(), nil)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeConfigIO),
		"InspectSchema(nil) = %v, want CONFIG_IO",
		err,
	)
}

func TestOpenDBCreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "mods.db")

	db, err := Open(context.Background(), path)
	require.NoError(t, err, "open %q: %v", path)

	defer func() { _ = db.Close() }()

	_, err = os.Stat(path)
	assert.NoError(t, err, "database file was not created at %q: %v", path, err)
}

func TestOpenDBRejectsDirectoryAsPath(t *testing.T) {
	dir := t.TempDir()

	_, err := Open(context.Background(), dir)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeConfigIO),
		"open a directory as a database = %v, want CONFIG_IO",
		err,
	)
}

func TestOpenDBHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := OpenDB(ctx, filepath.Join(t.TempDir(), "mods.db"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"open with a cancelled context = %v, want OPERATION_CANCELED",
		err,
	)
}

func TestOpenDBInMemoryVariants(t *testing.T) {
	cases := map[string]string{
		"explicit":           ":memory:",
		"empty":              "",
		"blank":              "   ",
		"padded":             "  :memory:  ",
		"whitespace":         "\t\n",
		"uri form":           "file::memory:",
		"uri form with args": "file::memory:?cache=shared",
		"not memory":         " ",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			db, err := OpenDB(context.Background(), path)
			require.NoError(t, err, "open %q: %v", path)

			defer func() { _ = db.Close() }()

			_, err = db.Exec("CREATE TABLE probe (id INTEGER)")
			assert.NoError(t, err, "create table: %v", err)
		})
	}

	t.Run("anything else is a file", func(t *testing.T) {
		for _, path := range []string{`""`, "mods.db", "./mods.db", "file:mods.db", "memory:", "/tmp/x.db"} {
			assert.False(t, IsMemoryPath(path), "IsMemoryPath(%q) = true, want false", path)
		}

		for _, path := range []string{":memory:", " :memory: ", "", "  ", "file::memory:", "file::memory:?cache=shared"} {
			assert.True(t, IsMemoryPath(path), "IsMemoryPath(%q) = false, want true", path)
		}
	})
}

// A relative database path used to be rendered as the DSN authority, which
// SQLite rejects with a message that says nothing about the real mistake.
func TestOpenDBAcceptsRelativePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	db, err := OpenDB(context.Background(), "mods.db")
	require.NoError(t, err, "open relative path")

	defer func() { _ = db.Close() }()

	_, err = os.Stat(filepath.Join(dir, "mods.db"))
	assert.NoError(t, err, "database was not created relative to the working directory: %v", err)
}

// Two in-memory databases must not share state, and a file database must keep
// its contents across reopens.
func TestDatabasePersistence(t *testing.T) {
	ctx := context.Background()

	t.Run("in-memory databases are isolated", func(t *testing.T) {
		first, err := Open(ctx, ":memory:")
		require.NoError(t, err, "open first")

		defer func() { _ = first.Close() }()

		_, err = first.Exec("INSERT INTO config(key, value) VALUES('k', 'first')")
		require.NoError(t, err, "insert: %v", err)

		second, err := Open(ctx, ":memory:")
		require.NoError(t, err, "open second")

		defer func() { _ = second.Close() }()

		var n int

		err = second.QueryRow("SELECT COUNT(*) FROM config").Scan(&n)
		require.NoError(t, err, "count: %v", err)

		assert.Equal(t, 0, n, "second in-memory database saw %d config rows, want 0", n)
	})

	t.Run("file database survives reopen", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mods.db")

		first, err := Open(ctx, path)
		require.NoError(t, err, "open")

		_, err = first.Exec("INSERT INTO config(key, value) VALUES('k', 'kept')")
		require.NoError(t, err, "insert: %v", err)

		err = first.Close()
		require.NoError(t, err, "close: %v", err)

		second, err := Open(ctx, path)
		require.NoError(t, err, "reopen")

		defer func() { _ = second.Close() }()

		var value string

		err = second.QueryRow("SELECT value FROM config WHERE key = 'k'").Scan(&value)
		require.NoError(t, err, "select: %v", err)

		assert.Equal(t, "kept", value, "value = %q after reopen, want %q", value, "kept")
	})
}

func TestFileDatabaseUsesWAL(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "mods.db"))
	require.NoError(t, err, "open")

	defer func() { _ = db.Close() }()

	var mode string

	err = db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	require.NoError(t, err, "read journal_mode: %v", err)

	assert.True(t, strings.EqualFold(mode, "wal"), "journal_mode = %q, want wal", mode)
}

func TestSchemaDDLIsEmbedded(t *testing.T) {
	ddl := SchemaDDL()
	require.NotEqual(
		t,
		"",
		strings.TrimSpace(ddl),
		"SchemaDDL() is empty; the embed did not happen",
	)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS mods", "CREATE TABLE IF NOT EXISTS mod_files",
		"CREATE TABLE IF NOT EXISTS load_order", "CREATE TABLE IF NOT EXISTS config",
	} {
		assert.Contains(t, ddl, want, "SchemaDDL() does not contain %q", want)
	}
}

func TestSplitStatements(t *testing.T) {
	cases := map[string]struct {
		ddl  string
		want []string
	}{
		"empty": {
			ddl:  "",
			want: nil,
		},
		"whitespace only": {
			ddl:  "  \n\t\n",
			want: nil,
		},
		"only comments": {
			ddl:  "-- one\n-- two\n",
			want: nil,
		},
		"single statement": {
			ddl:  "CREATE TABLE a (id TEXT)",
			want: []string{"CREATE TABLE a (id TEXT)"},
		},
		"trailing semicolon": {
			ddl:  "SELECT 1;",
			want: []string{"SELECT 1"},
		},
		"blank statements between": {
			ddl:  "SELECT 1;\n\n;\nSELECT 2;",
			want: []string{"SELECT 1", "SELECT 2"},
		},
		// The regression this guards: a semicolon inside a comment used to cut
		// the comment in half and leave the tail as a bogus statement.
		"semicolon inside comment": {
			ddl:  "-- path is storage/sqlite_schema.sql; keep it here\nSELECT 1;",
			want: []string{"SELECT 1"},
		},
		"indented comment": {
			ddl:  "   -- indented\nSELECT 1;",
			want: []string{"SELECT 1"},
		},
		"multi line statement": {
			ddl:  "CREATE TABLE a (\n  id TEXT,\n  n INT\n);\nSELECT 1;",
			want: []string{"CREATE TABLE a (\n  id TEXT,\n  n INT\n)", "SELECT 1"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := splitStatements(tc.ddl)
			require.Len(
				t,
				got,
				len(tc.want),
				"splitStatements(%q) = %q, want %q",
				tc.ddl,
				got,
				tc.want,
			)

			for i := range got {
				assert.Equal(
					t,
					tc.want[i],
					got[i],
					"statement %d = %q, want %q",
					i,
					got[i],
					tc.want[i],
				)
			}
		})
	}
}

func TestFormatAndParseTime(t *testing.T) {
	if got := formatTime(time.Time{}); got != "" {
		assert.Fail(t, "formatTime(zero) = %q, want the empty string", got)
	}

	instant := time.Date(2024, 5, 17, 8, 30, 15, 123456789, time.UTC)

	formatted := formatTime(instant)
	assert.Equal(
		t,
		"2024-05-17T08:30:15.123Z",
		formatted,
		"formatTime = %q, want millisecond RFC3339",
		formatted,
	)

	parsed := parseTime(formatted)
	assert.True(
		t,
		parsed.Equal(instant.Truncate(time.Millisecond)),
		"parseTime round trip = %s, want %s",
		parsed,
		instant.Truncate(time.Millisecond),
	)

	got := parseTime("")
	assert.True(t, got.IsZero(), "parseTime(\"\") = %s, want the zero time", got)

	got = parseTime("   ")
	assert.True(t, got.IsZero(), "parseTime(blank) = %s, want the zero time", got)

	got = parseTime("not a timestamp")
	assert.True(t, got.IsZero(), "parseTime(garbage) = %s, want the zero time", got)

	// Layouts written by older builds must keep loading.
	legacy := map[string]bool{
		"2024-05-17T08:30:15Z":                    true,
		"2024-05-17T08:30:15.123456789Z":          true,
		"2024-05-17 08:30:15":                     true,
		"2024-05-17 08:30:15+00:00":               true,
		"2024-05-17 08:30:15.999999999 +0000 UTC": true,
	}
	for value := range legacy {
		assert.False(
			t,
			parseTime(value).IsZero(),
			"parseTime(%q) = zero time, want a parsed instant",
			value,
		)
	}

	nonUTC := time.Date(2024, 5, 17, 10, 30, 15, 0, time.FixedZone("CEST", 2*60*60))

	parsed = parseTime(formatTime(nonUTC))
	assert.True(
		t,
		parsed.Equal(nonUTC),
		"parseTime(formatTime(zone)) = %s, want %s (offsets must be normalised to UTC)",
		parsed,
		nonUTC,
	)
}

func TestDataSourceName(t *testing.T) {
	mem, err := dataSourceName(":memory:", true)
	require.NoError(t, err, "dataSourceName(memory): %v", err)

	assert.True(
		t,
		strings.HasPrefix(mem, ":memory:?"),
		"in-memory DSN = %q, want a :memory: DSN",
		mem,
	)

	path := filepath.Join(t.TempDir(), "mods.db")

	file, err := dataSourceName(path, false)
	require.NoError(t, err, "dataSourceName(file): %v", err)

	assert.True(t, strings.HasPrefix(file, "file://"), "file DSN = %q, want a file:// DSN", file)

	assert.Contains(t, file, "mods.db", "file DSN = %q, want it to mention the database file", file)

	pragmas, err := url.ParseQuery(strings.SplitN(file, "?", 2)[1])
	require.NoError(t, err, "parse DSN query %q: %v", file)

	got := pragmas["_pragma"]
	assert.False(
		t,
		len(got) != 3 || got[0] != "foreign_keys(1)" || got[1] != "busy_timeout(5000)" ||
			got[2] != "synchronous(NORMAL)",
		"file DSN pragmas = %q, want foreign_keys(1), busy_timeout(5000), synchronous(NORMAL)",
		got,
	)

	assert.Equal(t, "rwc", pragmas.Get("mode"), "file DSN mode = %q, want rwc", pragmas.Get("mode"))

	assert.NotContains(t, mem, "mode=rwc", "in-memory DSN = %q, want no rwc mode", mem)

	// A path with characters that would corrupt a naive DSN.
	odd := filepath.Join(t.TempDir(), "with space&sym?bol#hash.db")

	oddDSN, err := dataSourceName(odd, false)
	require.NoError(t, err, "dataSourceName(odd): %v", err)

	db, err := OpenDB(context.Background(), odd)
	require.NoError(t, err, "open database at %q (DSN %q): %v", odd, oddDSN, err)

	defer func() { _ = db.Close() }()

	_, err = os.Stat(odd)
	assert.NoError(t, err, "database was not created at the literal path %q: %v", odd, err)
}

func TestEnsureParentDir(t *testing.T) {
	t.Run("in-memory is a no-op", func(t *testing.T) {
		err := EnsureParentDir("", true)
		assert.NoError(t, err, "EnsureParentDir(\"\", true) = %v, want nil", err)
	})

	t.Run("creates nested directories", func(t *testing.T) {
		base := t.TempDir()

		target := filepath.Join(base, "x", "y", "mods.db")
		err := EnsureParentDir(target, false)
		require.NoError(t, err, "EnsureParentDir: %v", err)

		info, err := os.Stat(filepath.Dir(target))
		assert.False(t, err != nil || !info.IsDir(), "parent directory was not created: %v", err)
	})

	t.Run("existing directory is fine", func(t *testing.T) {
		base := t.TempDir()
		err := EnsureParentDir(filepath.Join(base, "mods.db"), false)
		assert.NoError(t, err, "EnsureParentDir over an existing directory = %v, want nil", err)
	})

	t.Run("file in the way", func(t *testing.T) {
		base := t.TempDir()

		notADir := filepath.Join(base, "file")
		err := os.WriteFile(notADir, []byte("x"), 0o600)
		require.NoError(t, err)

		err = EnsureParentDir(filepath.Join(notADir, "mods.db"), false)
		assert.Error(t, err, "EnsureParentDir with a file in the way succeeded, want an error")
	})

	t.Run("blank path", func(t *testing.T) {
		err := EnsureParentDir("   ", false)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeNoStoragePath),
			"EnsureParentDir(blank) = %v, want NO_STORAGE_PATH",
			err,
		)
	})

	t.Run("bare filename has no directory component", func(t *testing.T) {
		err := EnsureParentDir("mods.db", false)
		assert.NoError(t, err, "EnsureParentDir(\"mods.db\") = %v, want nil", err)
	})
}

func TestCanceledError(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		err := canceledError(context.Canceled)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"canceledError(context.Canceled) = %v, want OPERATION_CANCELED",
			err,
		)

		assert.True(
			t,
			errors.Is(err, context.Canceled),
			"canceledError(context.Canceled) lost the cause",
		)
	})

	t.Run("deadline", func(t *testing.T) {
		err := canceledError(context.DeadlineExceeded)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"canceledError(context.DeadlineExceeded) = %v, want OPERATION_CANCELED",
			err,
		)

		assert.True(
			t,
			errors.Is(err, context.DeadlineExceeded),
			"canceledError(context.DeadlineExceeded) lost the cause",
		)
	})

	t.Run("wrapped cancellation", func(t *testing.T) {
		wrapped := errors.Join(errors.New("query failed"), context.Canceled)

		err := canceledError(wrapped)
		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"canceledError(joined) = %v, want OPERATION_CANCELED",
			err,
		)
	})
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"single":          "single",
		"first\nsecond":   "first",
		"  first\nsecond": "first",
		"first\n\nsecond": "first",
		"trailing\n":      "trailing",
		"a\nb\nc":         "a",
		"  padded  ":      "  padded  ",
	}
	for input, want := range cases {
		got := firstLine(input)
		assert.Equal(t, want, got, "firstLine(%q) = %q, want %q", input, got, want)
	}
}

func TestOpenReturnsTypedErrorForUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}

	base := t.TempDir()

	locked := filepath.Join(base, "locked")
	err := os.Mkdir(locked, 0o500)
	require.NoError(t, err)

	_, err = Open(context.Background(), filepath.Join(locked, "mods.db"))
	assert.Error(t, err, "opening a database inside a read-only directory succeeded, want an error")
}
