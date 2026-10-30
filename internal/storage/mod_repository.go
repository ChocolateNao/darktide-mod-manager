package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Sort keys accepted by ListOrdered. Anything else is rejected, never
// interpolated, so a caller cannot smuggle SQL through a sort parameter.
const (
	SortPriority     = "priority"
	SortName         = "name"
	SortInstalledAt  = "installed_at"
	SortUpdatedAt    = "updated_at"
	SortID           = "id"
	SortLoadPriority = "load_priority"
)

// ModRepository is the persistence contract for installed mods. Services depend on
// this interface, never on SQLite, so the lifecycle can be tested against a fake.
type ModRepository interface {
	// Save inserts or updates a mod. The ID is the primary key.
	Save(ctx context.Context, m *domain.Mod) error
	// GetByID returns the stored mod, or ErrModNotFound.
	GetByID(ctx context.Context, id string) (*domain.Mod, error)
	// List returns every mod, in the repository's canonical order.
	List(ctx context.Context) ([]domain.Mod, error)
	// ListOrdered returns every mod sorted by one of the Sort* keys, ties broken by
	// id. SortPriority and SortLoadPriority sort descending, because a higher priority
	// loads earlier; the rest sort ascending. An unknown key is rejected.
	ListOrdered(ctx context.Context, sortBy string) ([]domain.Mod, error)
	// ListEnabled returns the enabled mods only.
	ListEnabled(ctx context.Context) ([]domain.Mod, error)
	// ListBySource returns the mods installed from one source.
	ListBySource(ctx context.Context, sourceID string) ([]domain.Mod, error)
	// ListByGame returns the mods belonging to one game in the repository's
	// canonical order. The empty game id is a valid key: it selects the rows written
	// before multi-game support, which the composition root maps onto the default game.
	ListByGame(ctx context.Context, gameID string) ([]domain.Mod, error)
	// ListEnabledByGame returns the enabled mods of one game.
	ListEnabledByGame(ctx context.Context, gameID string) ([]domain.Mod, error)
	// UpdateEnabled flips the enabled flag, or ErrModNotFound.
	UpdateEnabled(ctx context.Context, id string, enabled bool) error
	// UpdatePriority sets the load priority, or ErrModNotFound.
	UpdatePriority(ctx context.Context, id string, priority int) error

	// SaveFile records (or replaces) one file of a mod.
	SaveFile(ctx context.Context, mf *domain.ModFile) error
	// SaveFiles records a whole file inventory in one transaction.
	SaveFiles(ctx context.Context, modID string, files []domain.ModFile) error
	// GetFilesByMod lists a mod's files ordered by relative path.
	GetFilesByMod(ctx context.Context, modID string) ([]domain.ModFile, error)
	// DeleteFile forgets one file of a mod.
	DeleteFile(ctx context.Context, modID, relPath string) error

	// SetLoadOrder stores an explicit load order. Unknown ids are an error: a
	// load order that names a mod which is not installed cannot be honoured.
	SetLoadOrder(ctx context.Context, ids []string) error
	// LoadOrder returns the explicit load order, or an empty slice when none
	// was ever set.
	LoadOrder(ctx context.Context) ([]string, error)
	// SetLoadOrderForGame replaces the explicit load order of ONE game and
	// leaves every other game's rows untouched. Each id must be installed and
	// belong to gameID; anything else is ErrModNotFound.
	SetLoadOrderForGame(ctx context.Context, gameID string, ids []string) error
	// LoadOrderByGame returns one game's explicit load order in position
	// order, or an empty slice when that game has none.
	LoadOrderByGame(ctx context.Context, gameID string) ([]string, error)

	// Delete removes a mod and, by cascade, its files and load-order row.
	Delete(ctx context.Context, id string) error
	// Count returns the number of stored mods.
	Count(ctx context.Context) (int, error)
	// CountByGame returns the number of mods belonging to one game.
	CountByGame(ctx context.Context, gameID string) (int, error)
	// Close releases the database handle.
	Close() error
}

// sqliteModRepository is the SQLite-backed ModRepository. now is injected rather
// than calling time.Now directly so tests can pin the timestamps Save stamps on.
type sqliteModRepository struct {
	closeErr  error
	db        *sql.DB
	now       func() time.Time
	closeOnce sync.Once
}

var _ ModRepository = (*sqliteModRepository)(nil)

// NewSQLiteModRepository opens (creating if needed) the database at path, applies
// the schema and returns a repository over it. An empty path or ":memory:" yields a
// private in-memory database, which the tests and the `--dry-run` CLI paths use;
// any other path has its parent directory created first.
func NewSQLiteModRepository(ctx context.Context, path string) (ModRepository, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	db, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}

	return &sqliteModRepository{db: db, now: time.Now}, nil
}

// NewSQLiteModRepositoryWithClock is NewSQLiteModRepository with an injected
// clock for the timestamps written by Save and the Update* methods.
func NewSQLiteModRepositoryWithClock(
	ctx context.Context,
	path string,
	now func() time.Time,
) (ModRepository, error) {
	if now == nil {
		now = time.Now
	}

	db, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}

	return &sqliteModRepository{db: db, now: now}, nil
}

// NewSQLiteModRepositoryFromDB wraps an already-open handle. The caller keeps ownership
// of the handle, so Close on the returned repository is a no-op.
func NewSQLiteModRepositoryFromDB(db *sql.DB) (ModRepository, error) {
	if db == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "nil database handle")
	}

	return &sqliteModRepository{db: db, now: time.Now}, nil
}

// clock returns the current time, defaulting to time.Now on a repository built
// as a zero value so a hand-assembled struct cannot panic.
func (r *sqliteModRepository) clock() time.Time {
	if r == nil || r.now == nil {
		return time.Now()
	}

	return r.now()
}

// DB exposes the underlying handle for diagnostics and migrations.
func (r *sqliteModRepository) DB() *sql.DB { return r.db }

// Close closes the handle exactly once; repeated calls return the first result.
func (r *sqliteModRepository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}

	r.closeOnce.Do(func() { r.closeErr = r.db.Close() })

	return r.closeErr
}

// modColumns is the canonical column list; every read uses it so the scanner
// and the SELECT can never drift apart.
const modColumns = `id, game_id, name, version, author, description, source_id, game_version,
	dependencies, conflicts_with, tags, source_data, hooks, local_path, hash,
	enabled, installed_at, updated_at, priority, install_mode, mod_folder_name,
	source_url, download_url, update_url, issues`

// Save upserts a mod. A zero UpdatedAt becomes "now"; a zero InstalledAt takes that value on
// insert but is left alone on update, so an update that says nothing about the install time
// cannot reset the age shown in the UI. Nil Dependencies, ConflictsWith, Tags or SourceData
// come back from GetByID as an empty collection, never nil.
func (r *sqliteModRepository) Save(ctx context.Context, m *domain.Mod) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	if m == nil {
		return appErrors.New(appErrors.ErrCodeInvalidMod, "cannot save a nil mod")
	}

	id := strings.TrimSpace(m.ID)
	if id == "" {
		return appErrors.New(appErrors.ErrCodeInvalidMod, "mod id is required")
	}

	now := r.clock()

	updatedAt := m.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}

	installedAt := m.InstalledAt
	if installedAt.IsZero() {
		if stored, ok := r.existingInstalledAt(ctx, id); ok {
			installedAt = stored
		} else {
			installedAt = updatedAt
		}
	}

	deps, err := encodeStringList(m.Dependencies)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "encode dependencies of %q", id)
	}

	conflicts, err := encodeStringList(m.ConflictsWith)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "encode conflicts of %q", id)
	}

	tags, err := encodeStringList(m.Tags)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "encode tags of %q", id)
	}

	issues, err := encodeStringList(m.Issues)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "encode issues of %q", id)
	}

	sourceData, err := encodeJSONObject(m.SourceData)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "encode source data of %q", id)
	}

	hooks, err := encodeHooks(m.Hooks)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "encode hooks of %q: %v", id, err)
	}

	const query = `
INSERT INTO mods (id, game_id, name, version, author, description, source_id, game_version,
	dependencies, conflicts_with, tags, source_data, hooks, local_path, hash,
	enabled, installed_at, updated_at, priority, install_mode, mod_folder_name,
	source_url, download_url, update_url, issues)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	game_id        = excluded.game_id,
	name           = excluded.name,
	version        = excluded.version,
	author         = excluded.author,
	description    = excluded.description,
	source_id      = excluded.source_id,
	game_version   = excluded.game_version,
	dependencies   = excluded.dependencies,
	conflicts_with = excluded.conflicts_with,
	tags           = excluded.tags,
	source_data    = excluded.source_data,
	hooks          = excluded.hooks,
	local_path     = excluded.local_path,
	hash           = excluded.hash,
	enabled        = excluded.enabled,
	installed_at   = excluded.installed_at,
	updated_at     = excluded.updated_at,
	priority       = excluded.priority,
	install_mode   = excluded.install_mode,
	mod_folder_name = excluded.mod_folder_name,
	source_url     = excluded.source_url,
	download_url   = excluded.download_url,
	update_url     = excluded.update_url,
	issues         = excluded.issues`

	_, err = r.db.ExecContext(ctx, query,
		id, m.GameID, m.Name, m.Version, m.Author, m.Description, m.SourceID, m.GameVersion,
		deps, conflicts, tags, sourceData, hooks, m.LocalPath, m.Hash,
		boolToInt(m.Enabled), formatTime(installedAt), formatTime(updatedAt), m.Priority,
		m.InstallMode, m.ModFolderName, m.SourceURL, m.DownloadURL, m.UpdateURL, issues)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "save mod %q: %v", id, err)
	}

	return nil
}

// existingInstalledAt reads the install time already recorded for id. It is only
// consulted when the incoming mod carries none, so a partially-populated mod cannot
// reset the install time of a row that is already there.
func (r *sqliteModRepository) existingInstalledAt(
	ctx context.Context,
	id string,
) (time.Time, bool) {
	var stored string
	if err := r.db.QueryRowContext(
		ctx,
		"SELECT installed_at FROM mods WHERE id = ?",
		id,
	).Scan(
		&stored,
	); err != nil {
		return time.Time{}, false
	}

	t := parseTime(stored)
	if t.IsZero() {
		return time.Time{}, false
	}

	return t, true
}

// GetByID loads one mod.
func (r *sqliteModRepository) GetByID(ctx context.Context, id string) (*domain.Mod, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return nil, canceledError(err)
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	row := r.db.QueryRowContext(ctx, "SELECT "+modColumns+" FROM mods WHERE id = ?", id)

	m, err := scanMod(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound, "mod %q is not installed", id)
	}

	if err != nil {
		return nil, wrapKeepingCode(err, appErrors.ErrCodeConfigIO, "load mod %q", id)
	}

	return m, nil
}

// List returns every mod in canonical order: enabled first, then descending priority
// (a higher priority loads earlier), then id, so the order never depends on
// insertion order.
func (r *sqliteModRepository) List(ctx context.Context) ([]domain.Mod, error) {
	return r.query(
		ctx,
		"SELECT "+modColumns+" FROM mods ORDER BY enabled DESC, priority DESC, id ASC",
	)
}

// ListOrdered returns every mod sorted by one of the Sort* keys.
func (r *sqliteModRepository) ListOrdered(
	ctx context.Context,
	sortBy string,
) ([]domain.Mod, error) {
	clause, err := sortColumn(sortBy)
	if err != nil {
		return nil, err
	}

	return r.query(ctx, "SELECT "+modColumns+" FROM mods ORDER BY "+clause)
}

// ListEnabled returns the enabled mods in canonical order.
func (r *sqliteModRepository) ListEnabled(ctx context.Context) ([]domain.Mod, error) {
	return r.query(
		ctx,
		"SELECT "+modColumns+" FROM mods WHERE enabled != 0 ORDER BY priority DESC, id ASC",
	)
}

// ListBySource returns the mods installed from one source, in id order. An empty
// source id matches mods whose source_id is empty, which is exactly the set of
// manually installed mods.
func (r *sqliteModRepository) ListBySource(
	ctx context.Context,
	sourceID string,
) ([]domain.Mod, error) {
	return r.query(
		ctx,
		"SELECT "+modColumns+" FROM mods WHERE source_id = ? ORDER BY id ASC",
		sourceID,
	)
}

// ListByGame returns one game's mods in the same canonical order as List.
func (r *sqliteModRepository) ListByGame(
	ctx context.Context,
	gameID string,
) ([]domain.Mod, error) {
	return r.query(
		ctx,
		"SELECT "+modColumns+" FROM mods WHERE game_id = ? "+
			"ORDER BY enabled DESC, priority DESC, id ASC",
		gameID,
	)
}

// ListEnabledByGame returns one game's enabled mods in the same order as
// ListEnabled.
func (r *sqliteModRepository) ListEnabledByGame(
	ctx context.Context,
	gameID string,
) ([]domain.Mod, error) {
	return r.query(
		ctx,
		"SELECT "+modColumns+" FROM mods WHERE game_id = ? AND enabled != 0 "+
			"ORDER BY priority DESC, id ASC",
		gameID,
	)
}

// query runs a mod SELECT and scans the result set.
func (r *sqliteModRepository) query(
	ctx context.Context,
	query string,
	args ...any,
) ([]domain.Mod, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return nil, canceledError(err)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "query mods: %v", err)
	}

	defer func() { _ = rows.Close() }()

	// An empty result is an empty slice, never nil: it is marshalled straight to JSON,
	// where null and [] mean different things to the frontend.
	out := []domain.Mod{}

	for rows.Next() {
		m, err := scanMod(rows)
		if err != nil {
			return nil, wrapKeepingCode(err, appErrors.ErrCodeConfigIO, "read mods")
		}

		out = append(out, *m)
	}

	if err := rows.Err(); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read mods: %v", err)
	}

	return out, nil
}

// UpdateEnabled flips the enabled flag of one mod.
func (r *sqliteModRepository) UpdateEnabled(ctx context.Context, id string, enabled bool) error {
	return r.updateOne(ctx, id, "UPDATE mods SET enabled = ?, updated_at = ? WHERE id = ?",
		boolToInt(enabled), formatTime(r.clock()), strings.TrimSpace(id))
}

// UpdatePriority sets the load priority of one mod.
func (r *sqliteModRepository) UpdatePriority(ctx context.Context, id string, priority int) error {
	return r.updateOne(ctx, id, "UPDATE mods SET priority = ?, updated_at = ? WHERE id = ?",
		priority, formatTime(r.clock()), strings.TrimSpace(id))
}

// updateOne runs a single-row UPDATE and turns "no rows affected" into
// ErrModNotFound, so callers can tell a missing mod from a no-op change.
func (r *sqliteModRepository) updateOne(ctx context.Context, id, query string, args ...any) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "update mod %q: %v", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		// Drivers that cannot report this are allowed to succeed.
		return nil
	}

	if affected == 0 {
		return appErrors.New(appErrors.ErrCodeModNotFound, "mod %q is not installed", id)
	}

	return nil
}

// SaveFile records one file of a mod, replacing any previous row with the same
// relative path.
func (r *sqliteModRepository) SaveFile(ctx context.Context, mf *domain.ModFile) error {
	if mf == nil {
		return appErrors.New(appErrors.ErrCodeInvalidMod, "cannot save a nil mod file")
	}

	return r.SaveFiles(ctx, mf.ModID, []domain.ModFile{*mf})
}

// SaveFiles replaces a mod's whole file inventory inside one transaction: an empty
// slice clears the inventory, and the foreign key rejects an unknown mod id, which
// surfaces as ErrModNotFound.
func (r *sqliteModRepository) SaveFiles(
	ctx context.Context,
	modID string,
	files []domain.ModFile,
) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "begin file inventory transaction")
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM mod_files WHERE mod_id = ?", modID); err != nil {
		return appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"clear file inventory of %q: %v",
			modID,
			err,
		)
	}

	const query = `INSERT INTO mod_files (mod_id, file_path, stored_path, size_bytes, checksum)
		VALUES (?, ?, ?, ?, ?)`

	for _, f := range files {
		rel := strings.TrimSpace(f.RelPath)
		if rel == "" {
			return appErrors.New(
				appErrors.ErrCodeInvalidMod,
				"file %q of mod %q has no relative path",
				f.StoredPath,
				modID,
			)
		}

		if _, err := tx.ExecContext(
			ctx,
			query,
			modID,
			rel,
			f.StoredPath,
			f.SizeBytes,
			f.Checksum,
		); err != nil {
			if isForeignKeyViolation(err) {
				return appErrors.New(appErrors.ErrCodeModNotFound, "mod %q is not installed", modID)
			}

			return appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"record file %q of %q: %v",
				rel,
				modID,
				err,
			)
		}
	}

	if err := tx.Commit(); err != nil {
		if isForeignKeyViolation(err) {
			return appErrors.New(appErrors.ErrCodeModNotFound, "mod %q is not installed", modID)
		}

		return appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"commit file inventory of %q: %v",
			modID,
			err,
		)
	}

	return nil
}

// GetFilesByMod lists a mod's files ordered by relative path.
func (r *sqliteModRepository) GetFilesByMod(
	ctx context.Context,
	modID string,
) ([]domain.ModFile, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return nil, canceledError(err)
	}

	modID = strings.TrimSpace(modID)
	if modID == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT mod_id, file_path, stored_path, size_bytes, checksum
		 FROM mod_files WHERE mod_id = ? ORDER BY file_path ASC`, modID)
	if err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"query files of %q: %v",
			modID,
			err,
		)
	}

	defer func() { _ = rows.Close() }()

	out := []domain.ModFile{}

	for rows.Next() {
		var (
			f    domain.ModFile
			mod  string
			rel  string
			size int64
		)
		if err := rows.Scan(&mod, &rel, &f.StoredPath, &size, &f.Checksum); err != nil {
			return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "scan file row: %v", err)
		}

		f.ModID = mod
		f.RelPath = rel
		f.SizeBytes = size
		out = append(out, f)
	}

	if err := rows.Err(); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"read files of %q: %v",
			modID,
			err,
		)
	}

	return out, nil
}

// DeleteFile forgets one file. Removing a file that was never recorded is not an
// error: it converges on the requested state.
func (r *sqliteModRepository) DeleteFile(ctx context.Context, modID, relPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	modID = strings.TrimSpace(modID)

	relPath = strings.TrimSpace(relPath)
	if modID == "" || relPath == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id and file path are required")
	}

	if _, err := r.db.ExecContext(
		ctx,
		"DELETE FROM mod_files WHERE mod_id = ? AND file_path = ?",
		modID,
		relPath,
	); err != nil {
		return appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"delete file %q of %q: %v",
			relPath,
			modID,
			err,
		)
	}

	return nil
}

// SetLoadOrder stores an explicit load order, replacing any previous one. Every id
// must name an installed mod: rejecting the whole order beats a partially-applied
// one, which would silently reorder the game.
func (r *sqliteModRepository) SetLoadOrder(ctx context.Context, ids []string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	cleaned, err := cleanLoadIDs(ids)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "begin load order transaction")
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range cleaned {
		var exists int

		err := tx.QueryRowContext(ctx, "SELECT 1 FROM mods WHERE id = ?", id).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return appErrors.New(
				appErrors.ErrCodeModNotFound,
				"load order names mod %q, which is not installed",
				id,
			)
		}

		if err != nil {
			return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "check mod %q: %v", id, err)
		}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM load_order"); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "clear load order: %v", err)
	}

	for pos, id := range cleaned {
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO load_order (mod_id, position) VALUES (?, ?)",
			id,
			pos,
		); err != nil {
			if isForeignKeyViolation(err) {
				return appErrors.New(
					appErrors.ErrCodeModNotFound,
					"load order names mod %q, which is not installed",
					id,
				)
			}

			return appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"record load order for %q: %v",
				id,
				err,
			)
		}
	}

	if err := tx.Commit(); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "commit load order: %v", err)
	}

	return nil
}

// LoadOrder returns the explicit load order. An unset order is an empty slice.
func (r *sqliteModRepository) LoadOrder(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return nil, canceledError(err)
	}

	rows, err := r.db.QueryContext(
		ctx,
		"SELECT mod_id FROM load_order ORDER BY position ASC, mod_id ASC",
	)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "query load order: %v", err)
	}

	defer func() { _ = rows.Close() }()

	out := []string{}

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"scan load order row: %v",
				err,
			)
		}

		out = append(out, id)
	}

	if err := rows.Err(); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read load order: %v", err)
	}

	return out, nil
}

// cleanLoadIDs trims a requested load order, drops empty entries and rejects duplicates,
// so both setters agree on what a valid request looks like.
func cleanLoadIDs(ids []string) ([]string, error) {
	cleaned := make([]string, 0, len(ids))

	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}

		if seen[id] {
			return nil, appErrors.New(
				appErrors.ErrCodeDuplicate,
				"load order lists mod %q twice",
				id,
			)
		}

		seen[id] = true
		cleaned = append(cleaned, id)
	}

	return cleaned, nil
}

// SetLoadOrderForGame replaces the explicit load order of ONE game. The delete is
// scoped through the mods table, so other games' rows — which the unscoped
// SetLoadOrder would wipe — survive untouched.
func (r *sqliteModRepository) SetLoadOrderForGame(
	ctx context.Context,
	gameID string,
	ids []string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	cleaned, err := cleanLoadIDs(ids)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "begin load order transaction")
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range cleaned {
		var exists int

		err := tx.QueryRowContext(
			ctx,
			"SELECT 1 FROM mods WHERE id = ? AND game_id = ?",
			id,
			gameID,
		).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return appErrors.New(
				appErrors.ErrCodeModNotFound,
				"load order names mod %q, which is not installed for game %q",
				id,
				gameID,
			)
		}

		if err != nil {
			return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "check mod %q: %v", id, err)
		}
	}

	_, err = tx.ExecContext(
		ctx,
		"DELETE FROM load_order WHERE mod_id IN (SELECT id FROM mods WHERE game_id = ?)",
		gameID,
	)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO,
			"clear load order of game %q: %v", gameID, err)
	}

	for pos, id := range cleaned {
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO load_order (mod_id, position) VALUES (?, ?)",
			id,
			pos,
		); err != nil {
			if isForeignKeyViolation(err) {
				return appErrors.New(
					appErrors.ErrCodeModNotFound,
					"load order names mod %q, which is not installed",
					id,
				)
			}

			return appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"record load order for %q: %v",
				id,
				err,
			)
		}
	}

	if err := tx.Commit(); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "commit load order: %v", err)
	}

	return nil
}

// LoadOrderByGame returns one game's explicit load order. The join through
// mods is what keeps another game's positions out of the result.
func (r *sqliteModRepository) LoadOrderByGame(
	ctx context.Context,
	gameID string,
) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return nil, canceledError(err)
	}

	rows, err := r.db.QueryContext(
		ctx,
		`SELECT lo.mod_id FROM load_order lo
		 JOIN mods m ON m.id = lo.mod_id
		 WHERE m.game_id = ?
		 ORDER BY lo.position ASC, lo.mod_id ASC`,
		gameID,
	)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO,
			"query load order of game %q: %v", gameID, err)
	}

	defer func() { _ = rows.Close() }()

	out := []string{}

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigIO,
				"scan load order row: %v",
				err,
			)
		}

		out = append(out, id)
	}

	if err := rows.Err(); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read load order: %v", err)
	}

	return out, nil
}

// Delete removes a mod. Its files and its load-order row go with it, by
// cascade; the files on disk are the caller's business.
func (r *sqliteModRepository) Delete(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "mod id is required")
	}

	res, err := r.db.ExecContext(ctx, "DELETE FROM mods WHERE id = ?", id)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "delete mod %q: %v", id, err)
	}

	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return appErrors.New(appErrors.ErrCodeModNotFound, "mod %q is not installed", id)
	}

	return nil
}

// Count returns the number of stored mods.
func (r *sqliteModRepository) Count(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return 0, appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return 0, canceledError(err)
	}

	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mods").Scan(&n); err != nil {
		return 0, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "count mods: %v", err)
	}

	return n, nil
}

// CountByGame returns the number of mods belonging to one game.
func (r *sqliteModRepository) CountByGame(ctx context.Context, gameID string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if r == nil || r.db == nil {
		return 0, appErrors.New(appErrors.ErrCodeConfigIO, "repository is not open")
	}

	if err := ctx.Err(); err != nil {
		return 0, canceledError(err)
	}

	var n int

	err := r.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM mods WHERE game_id = ?",
		gameID,
	).Scan(&n)
	if err != nil {
		return 0, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "count mods of game %q: %v",
			gameID, err)
	}

	return n, nil
}

// wrapKeepingCode attaches a message to err without losing a code it already carries.
// A corrupt JSON column has to surface as CACHE_CORRUPT — the code the GUI uses to
// offer an index rebuild — so a generic I/O code must not be layered on a specific one.
func wrapKeepingCode(err error, fallback appErrors.Code, format string, args ...any) error {
	if err == nil {
		return nil
	}

	if code := appErrors.CodeOf(err); code != appErrors.CodeUnknown {
		return appErrors.Wrap(err, code, format, args...)
	}

	return appErrors.Wrap(err, fallback, format, args...)
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanMod reads one mods row into a domain.Mod. A corrupt JSON column fails the whole
// read, not being silently dropped: a mod that installs without its
// dependencies and hooks looks like a bug in the game instead of a bug in the file.
func scanMod(row rowScanner) (*domain.Mod, error) {
	var (
		m                                                    domain.Mod
		deps, conflicts, tags, issues, sourceData, hooksJSON string
		installedAt, updatedAt                               string
		enabled                                              int64
	)

	err := row.Scan(
		&m.ID, &m.GameID, &m.Name, &m.Version, &m.Author, &m.Description, &m.SourceID,
		&m.GameVersion,
		&deps, &conflicts, &tags, &sourceData, &hooksJSON, &m.LocalPath, &m.Hash,
		&enabled, &installedAt, &updatedAt, &m.Priority, &m.InstallMode, &m.ModFolderName,
		&m.SourceURL, &m.DownloadURL, &m.UpdateURL, &issues)
	if err != nil {
		return nil, err
	}

	if m.Dependencies, err = decodeStringList(deps); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"decode dependencies of %q: %v",
			m.ID,
			err,
		)
	}

	if m.ConflictsWith, err = decodeStringList(conflicts); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"decode conflicts of %q: %v",
			m.ID,
			err,
		)
	}

	if m.Tags, err = decodeStringList(tags); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"decode tags of %q: %v",
			m.ID,
			err,
		)
	}

	if m.Issues, err = decodeStringList(issues); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"decode issues of %q: %v",
			m.ID,
			err,
		)
	}

	if m.SourceData, err = decodeJSONObject(sourceData); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"decode source data of %q: %v",
			m.ID,
			err,
		)
	}

	if m.Hooks, err = decodeHooks(hooksJSON); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeCacheCorrupt,
			"decode hooks of %q: %v",
			m.ID,
			err,
		)
	}

	m.Enabled = enabled != 0
	m.InstalledAt = parseTime(installedAt)
	m.UpdatedAt = parseTime(updatedAt)

	return &m, nil
}

// sortColumn maps a Sort* key onto an ORDER BY clause, rejecting anything else so the
// key can never be interpolated unchecked. The id tiebreaker is part of the clause:
// without it, equal names or priorities come back in whatever order the query engine
// prefers, and the load order the player sees would change between runs.
func sortColumn(sortBy string) (string, error) {
	var column string

	descending := false

	switch strings.TrimSpace(sortBy) {
	case SortPriority, SortLoadPriority:
		column, descending = "priority", true
	case SortName:
		column = "name"
	case SortInstalledAt:
		column = "installed_at"
	case SortUpdatedAt:
		column = "updated_at"
	case SortID, "":
		column = "id"
	default:
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"unknown sort key %q (expected one of %s)", sortBy, strings.Join(SortKeys(), ", "))
	}

	if descending {
		return column + " DESC, id ASC", nil
	}

	if column == "id" {
		// The tiebreaker would be the sort key itself.
		return "id ASC", nil
	}

	return column + " ASC, id ASC", nil
}

// SortKeys lists the accepted sort keys, in a stable order, for error messages
// and for the CLI's --sort flag help.
func SortKeys() []string {
	return []string{
		SortID,
		SortName,
		SortPriority,
		SortLoadPriority,
		SortInstalledAt,
		SortUpdatedAt,
	}
}

// encodeStringList renders a string slice as a JSON array. A nil slice becomes "[]",
// not "null", so a column read back later is unambiguous.
func encodeStringList(values []string) (string, error) {
	if values == nil {
		return "[]", nil
	}

	out := make([]string, 0, len(values))
	out = append(out, values...)

	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// decodeStringList reverses encodeStringList. Both "null" and "" decode to a
// nil slice, matching what an absent list means in the domain.
func decodeStringList(s string) ([]string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var out []string
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, err
	}

	if out == nil {
		return nil, nil
	}

	return out, nil
}

// encodeJSONObject renders a map as JSON, treating nil as an empty object.
func encodeJSONObject(value map[string]any) (string, error) {
	if value == nil {
		return "{}", nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// decodeJSONObject reverses encodeJSONObject. A JSON `null` decodes to a nil map.
// Only SourceData uses it; hooks have their own codec.
func decodeJSONObject(s string) (map[string]any, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	out := map[string]any{}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, err
	}

	return out, nil
}

// encodeHooks renders a ModHooks struct. The struct already carries one JSON-tagged
// field per phase, so this is a straight marshal and an empty hook set becomes "{}".
func encodeHooks(h domain.ModHooks) (string, error) {
	data, err := json.Marshal(h)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// decodeHooks reverses encodeHooks. An empty or null column yields no hooks (a row written
// before hooks existed); an empty phase list normalises to nil so the round trip stays
// canonical; an unknown phase name is ignored so a mod from a newer build still loads.
// Anything else malformed fails the read.
func decodeHooks(s string) (domain.ModHooks, error) {
	var hooks domain.ModHooks

	trimmed := strings.TrimSpace(s)
	if trimmed == "" || trimmed == "null" {
		return hooks, nil
	}

	if err := json.Unmarshal([]byte(trimmed), &hooks); err != nil {
		return hooks, err
	}

	for _, phase := range domain.HookPhases() {
		if len(hooks.For(phase)) == 0 {
			hooks.Set(phase, nil)
		}
	}

	return hooks, nil
}

// boolToInt maps the domain's boolean onto SQLite's integer.
func boolToInt(v bool) int64 {
	if v {
		return 1
	}

	return 0
}

// isForeignKeyViolation reports whether err came from the foreign key constraint.
// modernc's driver renders the message, not exporting a typed error, so the
// check is on text; the alternative — probing the schema — costs a query per insert.
func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())

	return strings.Contains(msg, "foreign key") || strings.Contains(msg, "constraint failed: fk")
}
