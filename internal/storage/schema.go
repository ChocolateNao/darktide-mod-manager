// Package storage owns every byte the manager persists: the configuration
// file, the SQLite database that records installed mods, and the download
// cache. Nothing here knows about the game, the network or the UI — services
// depend on the interfaces declared in this package, and the concrete
// implementations below them are the only things that touch the disk or a
// database handle.
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
	"strings"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	// Pure-Go SQLite: registers the "sqlite" database/sql driver. No CGO, so
	// the manager cross-compiles for every platform Wails targets.
	_ "modernc.org/sqlite"
)

// DriverName is the database/sql driver registered by modernc.org/sqlite. It is
// a pure-Go build: the manager ships as a single binary with no CGO toolchain
// requirement.
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

// InspectSchema reports the schema version of an already-open database. A
// database with no user_version set is reported as not installed, which is the
// signal callers use to decide whether to run ApplySchema.
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

// ApplySchema creates any missing tables and indexes and stamps the schema
// version. It is idempotent, and it refuses to touch a database written by a
// newer build: downgrading would silently leave the newer tables in place and
// the older code would trip over them.
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

	// Upgrades run before the idempotent DDL: the DDL's CREATE INDEX on
	// game_id would fail against a mods table written by an older build that
	// does not have the column yet. Cheap PRAGMA probe, guarded so a fresh
	// database (no mods table) skips it.
	if err := addModsGameIDColumn(ctx, tx); err != nil {
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

// addModsGameIDColumn upgrades a mods table created by a pre-multi-game build.
//
// The upgrade is a single guarded ALTER rather than a versioned migration: the
// column carries DEFAULT ” so the statement cannot fail on populated tables,
// and the probe (a PRAGMA table-info call) makes it a no-op on every database
// that already has the column — which is every database this build creates.
// The schema version is deliberately NOT bumped for this.
func addModsGameIDColumn(ctx context.Context, tx *sql.Tx) error {
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
		"SELECT COUNT(*) FROM pragma_table_info('mods') WHERE name = 'game_id'",
	).Scan(&hasColumn); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "probe mods.game_id column")
	}

	if hasColumn > 0 {
		return nil
	}

	if _, err := tx.ExecContext(
		ctx,
		"ALTER TABLE mods ADD COLUMN game_id TEXT NOT NULL DEFAULT ''",
	); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "add mods.game_id column")
	}

	return nil
}

// OpenDB opens (creating if necessary) the manager database at path.
//
// The empty path and the special value ":memory:" both yield a private in-memory
// database. An in-memory database lives inside a single connection, so those
// handles get a pool of exactly one connection with no idle or lifetime
// eviction; otherwise a pooled second connection would open a brand-new empty
// database and every read would come back empty.
//
// On-disk databases get WAL journalling (concurrent readers alongside one
// writer), a busy timeout so a competing writer waits instead of failing
// immediately, and enforced foreign keys.
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
		// journal_mode returns a row, and a database that cannot switch to WAL
		// (a network share, a read-only mount) still works in its default mode,
		// so a failure here is reported but not fatal.
		var mode string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
			_ = db.Close()
			return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "enable WAL for %q", path)
		}
	}

	return db, nil
}

// EnsureParentDir creates the directory that will hold an on-disk database.
// SQLite refuses to create a file inside a directory that does not exist, and
// the manager's data directory may legitimately be missing on first run. An
// existing parent, an existing directory at the parent path, and an in-memory
// database are all fine.
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

// Open initializes the database at path and applies the schema. It is the
// single call the rest of the program needs.
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

// IsMemoryPath reports whether path names a private in-memory database rather
// than a file: the empty string, ":memory:", and the URI spelling "file::memory:".
// Callers in production always pass a real path — the empty case exists for
// tests and for "--dry-run" flows that must not touch the disk.
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

// dataSourceName builds the driver DSN, encoding the pragmas that have to be
// set per connection.
func dataSourceName(path string, inMemory bool) (string, error) {
	name := MemoryPath

	if !inMemory {
		// The path is made absolute first. url.URL with a relative Path renders
		// the first segment as the URI *authority*, which SQLite then rejects
		// with "invalid uri authority" — so a relative path would fail in a way
		// that looks nothing like the mistake it is.
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"resolve database path %q",
				path,
			)
		}

		u := url.URL{Scheme: "file", Path: absolute}
		name = u.String()
	}

	q := url.Values{}
	// Every transaction starts as BEGIN IMMEDIATE rather than a deferred BEGIN.
	// A deferred transaction that reads before it writes can hold a shared lock
	// and then need to upgrade to a write lock, and SQLite resolves that race by
	// failing one of the two with SQLITE_BUSY immediately — a busy timeout does
	// not help, because the loser is not waiting on a busy database but on its
	// own un-upgradable transaction. Claiming the write lock up front makes
	// concurrent writers queue on the busy timeout instead.
	q.Add("_txlock", "immediate")
	// Per-connection pragmas: foreign_keys is a connection setting, not a
	// database one, so putting it in the DSN is the only way to guarantee every
	// pooled connection enforces cascades.
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")

	if !inMemory {
		q.Add("mode", "rwc")
	}

	return name + "?" + q.Encode(), nil
}

// splitStatements breaks the embedded DDL into individual statements.
//
// Comment lines are removed *before* the split: a semicolon inside a comment
// (".../sqlite_schema.sql; keeping it inside...") would otherwise cut a
// comment in half and leave the tail as a statement. The embedded file has no
// semicolons inside string literals, so splitting after stripping is enough —
// and it keeps the driver from having to accept multi-statement Execute.
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

// formatTime renders t for storage. The zero time is rendered as the empty
// string rather than as year 1, so a caller scanning it into a string can tell
// "never set" from "set to a real instant".
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(timeLayout)
}

// parseTime reverses formatTime, accepting the RFC3339 variants SQLite and Go
// may produce. An unparseable or empty value yields the zero time, which is how
// a row written by an older build with a different layout degrades instead of
// failing the whole query.
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
