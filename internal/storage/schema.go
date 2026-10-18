// Package storage owns every byte the manager persists: the configuration file, the
// SQLite database recording installed mods, and the download cache. Nothing here knows
// about the game, the network or the UI; services depend on this package's interfaces.
package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	// Pure-Go SQLite: registers the "sqlite" database/sql driver. No CGO, so
	// the manager cross-compiles for every platform Wails targets.
	_ "modernc.org/sqlite"
)

// DriverName is the database/sql driver registered by modernc.org/sqlite: a pure-Go
// build, so the manager ships as one binary with no CGO toolchain requirement.
const DriverName = "sqlite"

// CurrentSchemaVersion is the value written to PRAGMA user_version after
// ApplySchema runs.
const CurrentSchemaVersion = 1

// timeLayout is the storage format for every timestamp column. It is RFC3339
// with millisecond precision and a literal Z, which time.RFC3339 parses and
// which sorts correctly as text.
const timeLayout = "2006-01-02T15:04:05.000Z"

//go:embed sqlite_schema.sql
var schemaSQL string

// SchemaDDL returns the embedded DDL. It is exposed for tests and for
// operators who want to inspect the schema a given build was compiled with.
func SchemaDDL() string { return schemaSQL }

// dbVersion reads PRAGMA user_version, tolerating a database that has never
// been initialised (SQLite reports 0 there).
func dbVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read schema version")
	}

	return v, nil
}

// SchemaState describes the schema currently stored in a database.
type SchemaState struct {
	Version   int
	Installed bool
}

// IsCurrent reports whether the database carries the schema this build expects.
func (s SchemaState) IsCurrent() bool { return s.Installed && s.Version == CurrentSchemaVersion }

// InspectSchema reports the schema version of an already-open database. No user_version
// set means not installed, which is the signal callers use to decide on ApplySchema.
func InspectSchema(ctx context.Context, db *sql.DB) (SchemaState, error) {
	if db == nil {
		return SchemaState{}, appErrors.New(appErrors.ErrCodeConfigIO, "nil database handle")
	}

	version, err := dbVersion(ctx, db)
	if err != nil {
		return SchemaState{}, err
	}

	return SchemaState{Version: version, Installed: version > 0}, nil
}

// ApplySchema creates any missing tables and indexes and stamps the schema version. It
// is idempotent, and it refuses to touch a database written by a newer build: the older
// code would trip over the newer tables it cannot see.
func ApplySchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "nil database handle")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "begin schema transaction")
	}

	defer func() { _ = tx.Rollback() }()

	// Upgrades run before the DDL: CREATE INDEX on game_id would fail against a
	// mods table written by a pre-multi-game build.
	if err := addModsGameIDColumn(ctx, tx); err != nil {
		return err
	}

	if err := addModsSourceLinkColumns(ctx, tx); err != nil {
		return err
	}

	for _, stmt := range splitStatements(schemaSQL) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"apply schema statement %q",
				firstLine(stmt),
			)
		}
	}

	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_meta(key, value) VALUES('version', ?) "+
		"ON CONFLICT(key) DO UPDATE SET value = excluded.value", itoa(CurrentSchemaVersion)); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "record schema version")
	}
	// PRAGMA statements cannot be parameterised and, inside a transaction,
	// SQLite silently ignores them; the version has to be set afterwards.
	if _, err := tx.ExecContext(
		ctx,
		fmt.Sprintf("PRAGMA user_version = %d", CurrentSchemaVersion),
	); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "stamp schema version")
	}

	if err := tx.Commit(); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "commit schema transaction")
	}

	return nil
}

// addModsGameIDColumn upgrades a mods table created by a pre-multi-game build. A single
// guarded ALTER rather than a versioned migration: the column carries a DEFAULT so it
// cannot fail on populated tables, and the probe makes it a no-op on every database
// this build creates, so the version is never bumped.
func addModsGameIDColumn(ctx context.Context, tx *sql.Tx) error {
	return addModsColumn(ctx, tx, "game_id",
		"ALTER TABLE mods ADD COLUMN game_id TEXT NOT NULL DEFAULT ''")
}

// addModsColumn adds one column to the mods table if it is not there already. Shared by
// every mods-table upgrade so the probing logic has exactly one copy; a second copy is
// how a migration ends up guarded in one place and not in another.
func addModsColumn(ctx context.Context, tx *sql.Tx, name, ddl string) error {
	var table int

	if err := tx.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'mods'",
	).Scan(&table); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "probe mods table")
	}

	if table == 0 {
		return nil
	}

	var hasColumn int

	if err := tx.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM pragma_table_info('mods') WHERE name = ?",
		name,
	).Scan(&hasColumn); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "probe mods.%s column", name)
	}

	if hasColumn > 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "add mods.%s column", name)
	}

	return nil
}

// addModsSourceLinkColumns upgrades a mods table written before the manager remembered where a
// mod came from. Without these columns a mod from a source that cannot be re-queried becomes
// unreachable once the database moves machine: a share recipient never sees the manifest in
// the mod folder. Three guarded ALTERs rather than a versioned migration, as in
// addModsGameIDColumn.
func addModsSourceLinkColumns(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{
			name: "source_url",
			ddl:  "ALTER TABLE mods ADD COLUMN source_url TEXT NOT NULL DEFAULT ''",
		},
		{
			name: "download_url",
			ddl:  "ALTER TABLE mods ADD COLUMN download_url TEXT NOT NULL DEFAULT ''",
		},
		{
			name: "update_url",
			ddl:  "ALTER TABLE mods ADD COLUMN update_url TEXT NOT NULL DEFAULT ''",
		},
	}

	for _, column := range columns {
		if err := addModsColumn(ctx, tx, column.name, column.ddl); err != nil {
			return err
		}
	}

	return nil
}

// OpenDB opens (creating if necessary) the manager database at path.
//
// An empty path or ":memory:" yields a database that lives inside one connection, so those
// handles get a pool of exactly one: a pooled second connection would open a brand-new empty
// database and every read would come back empty. On-disk databases get WAL journalling
// (concurrent readers alongside one writer), a busy timeout so a competing writer waits
// rather than fails, and enforced foreign keys.
func OpenDB(ctx context.Context, path string) (*sql.DB, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, canceledError(err)
	}

	inMemory := IsMemoryPath(path)
	if err := EnsureParentDir(path, inMemory); err != nil {
		return nil, err
	}

	dsn, err := dataSourceName(path, inMemory)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "open database %q", path)
	}

	if inMemory {
		// One connection, kept alive forever: the whole database is that
		// connection's state.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxIdleTime(0)
		db.SetConnMaxLifetime(0)
	} else {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(4)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "connect to database %q", path)
	}

	if !inMemory {
		// A database that cannot switch to WAL (a network share, a read-only
		// mount) still works in its default mode, so a failure is not fatal.
		var mode string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
			_ = db.Close()
			return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "enable WAL for %q", path)
		}
	}

	return db, nil
}

// EnsureParentDir creates the directory that will hold an on-disk database: SQLite
// refuses to create a file inside a missing directory, and the data directory may be
// absent on first run. An existing parent, an existing directory at the parent path, and
// an in-memory database are all fine.
func EnsureParentDir(path string, inMemory bool) error {
	if inMemory {
		return nil
	}

	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return appErrors.New(appErrors.ErrCodeNoStoragePath, "empty database path")
	}

	dir := filepath.Dir(trimmed)
	if strings.TrimSpace(dir) == "" {
		return nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create database directory %q", dir)
	}

	return nil
}

// canceledError maps a context cancellation onto the typed cancellation code
// callers check for.
func canceledError(err error) error {
	if errors.Is(err, context.Canceled) {
		return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled, "operation canceled")
	}

	return appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled, "operation stopped: %v", err)
}

// Open initializes the database at path and applies the schema.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := OpenDB(ctx, path)
	if err != nil {
		return nil, err
	}

	if err := ApplySchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// MemoryPath is the DSN fragment that asks for a private in-memory database.
const MemoryPath = ":memory:"

// IsMemoryPath reports whether path names a private in-memory database rather than a
// file: the empty string, ":memory:", and the URI spelling "file::memory:". Production
// always passes a real path; the empty case is for tests and "--dry-run" flows.
func IsMemoryPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || trimmed == MemoryPath {
		return true
	}
	// SQLite also spells an in-memory database as a URI. The query string is
	// ignored: cache=shared and friends do not change where the data lives.
	if i := strings.IndexAny(trimmed, "?"); i >= 0 {
		trimmed = trimmed[:i]
	}

	return trimmed == "file:"+MemoryPath
}

// fileURI renders an absolute filesystem path as the SQLite file: URI.
//
// A drive letter is a URI authority by every definition, so a raw Windows path renders as
// `file://C:%5Cstate%5Cdmm.db` and SQLite answers "invalid uri authority" — invisible on Unix,
// fatal there. The accepted form is `file:///C:/state/dmm.db`, which is why the caller passes an
// absolute path. Backslashes are converted because a SQLite DSN is parsed as a URI.
var windowsDrive = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func fileURI(absolute string) string {
	// Not filepath.ToSlash: that is a no-op on a build that does not recognise backslash
	// as a separator, which is the build under test.
	slashed := strings.ReplaceAll(absolute, `\`, "/")

	// The drive is matched explicitly rather than with filepath.VolumeName, which
	// only knows volumes of the platform it was compiled for.
	if windowsDrive.MatchString(absolute) {
		// `C:\state` -> `/C:/state`. The leading slash is what keeps the
		// authority empty; without it the drive letter becomes the host.
		slashed = "/" + slashed
	}

	u := url.URL{Scheme: "file", Path: slashed}

	return u.String()
}

// dataSourceName builds the driver DSN, encoding the pragmas that have to be
// set per connection.
func dataSourceName(path string, inMemory bool) (string, error) {
	name := MemoryPath

	if !inMemory {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"resolve database path %q",
				path,
			)
		}

		name = fileURI(absolute)
	}

	q := url.Values{}
	// BEGIN IMMEDIATE up front: a deferred transaction that reads then writes
	// fails one of two racers with SQLITE_BUSY, which no busy timeout cures.
	q.Add("_txlock", "immediate")
	// foreign_keys is a per-connection setting, so it can only be guaranteed
	// for every pooled connection by putting it in the DSN.
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")

	if !inMemory {
		q.Add("mode", "rwc")
	}

	return name + "?" + q.Encode(), nil
}

// splitStatements breaks the embedded DDL into individual statements. Comment lines are removed
// *before* the split: a semicolon inside one would otherwise leave its tail as a statement.
// The embedded file has no semicolons inside string literals, so stripping first is enough.
func splitStatements(ddl string) []string {
	var code strings.Builder

	for _, line := range strings.Split(ddl, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}

		code.WriteString(line)
		code.WriteByte('\n')
	}

	var out []string

	for _, raw := range strings.Split(code.String(), ";") {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}

		out = append(out, stmt)
	}

	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}

	return s
}

func itoa(v int) string { return fmt.Sprintf("%d", v) }

// formatTime renders t for storage. The zero time becomes the empty string rather than
// year 1, so a caller scanning it into a string can tell "never set" from a real instant.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(timeLayout)
}

// parseTime reverses formatTime, accepting the RFC3339 variants SQLite and Go may
// produce. An unparseable value yields the zero time, so a row written by an older build
// with a different layout degrades instead of failing the whole query.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}

	layouts := []string{
		timeLayout,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}

	return time.Time{}
}
