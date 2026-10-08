-- Schema for the darktide-mod-manager SQLite database.
--
-- The DDL lives next to the code that embeds it (see schema.go) so the binary
-- never depends on files being present at run time.
--
-- Every statement is idempotent: applying the schema to a populated database is
-- a no-op, which is what lets the repository call it on every open.

PRAGMA foreign_keys = ON;

-- Schema bookkeeping. PRAGMA user_version is the canonical version marker; the
-- table exists so a future migration can describe what it did.
CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

-- Arbitrary key/value app state (last used source, window geometry, ...).
CREATE TABLE IF NOT EXISTS config (
    key        TEXT PRIMARY KEY NOT NULL,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Installed mods. `local_path` points at the manager-owned folder
-- (<storage>/mods/<id>/<version>); the game's mods folder only ever holds
-- symlinks (or copies) pointing into it.
CREATE TABLE IF NOT EXISTS mods (
    id             TEXT PRIMARY KEY NOT NULL,
    name           TEXT NOT NULL DEFAULT '',
    version        TEXT NOT NULL DEFAULT '',
    author         TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    source_id      TEXT NOT NULL DEFAULT '',
    game_version   TEXT NOT NULL DEFAULT '',
    dependencies   TEXT NOT NULL DEFAULT '[]',  -- JSON array of mod ids
    conflicts_with TEXT NOT NULL DEFAULT '[]',  -- JSON array of mod ids
    tags           TEXT NOT NULL DEFAULT '[]',  -- JSON array of strings
    source_data    TEXT NOT NULL DEFAULT '{}',  -- JSON object, source specific
    hooks          TEXT NOT NULL DEFAULT '{}',  -- JSON object, domain.ModHooks
    local_path     TEXT NOT NULL DEFAULT '',
    hash           TEXT NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1,  -- 0/1, any non-zero is true
    installed_at   TEXT NOT NULL,
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    priority       INTEGER NOT NULL DEFAULT 0,
    -- Install mode: "mods_folder" (default) or "game_root".
    install_mode   TEXT NOT NULL DEFAULT 'mods_folder',
    -- Mod folder name in the game's mods folder. Used for load order file.
    mod_folder_name TEXT NOT NULL DEFAULT ''
);

-- One row per file belonging to a mod. CASCADE means deleting the mod row is
-- enough to forget the file inventory; the files themselves live on disk under
-- the mod folder and are removed separately.
CREATE TABLE IF NOT EXISTS mod_files (
    mod_id      TEXT NOT NULL,
    file_path   TEXT NOT NULL,  -- relative path inside the mod, e.g. textures/hud.dds
    stored_path TEXT NOT NULL,  -- absolute path inside manager storage
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    checksum    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (mod_id, file_path),
    FOREIGN KEY (mod_id) REFERENCES mods(id) ON DELETE CASCADE
);

-- Explicit load order. A mod with no row keeps the implicit order it has in the
-- mods table (priority, then id).
CREATE TABLE IF NOT EXISTS load_order (
    mod_id   TEXT PRIMARY KEY NOT NULL,
    position INTEGER NOT NULL,
    FOREIGN KEY (mod_id) REFERENCES mods(id) ON DELETE CASCADE
);

-- Download cache bookkeeping. The archive itself lives on disk under
-- <cache root>/blobs/<xx>/<hash>; this table is only the metadata needed to
-- deduplicate downloads, decide what is safe to prune, and detect a blob that
-- was deleted behind our back.
--
-- `refs` is a reference count: one increment per installed mod holding the
-- archive. A prune may only remove entries whose refs is zero, which is what
-- makes "uninstall with --purge" safe to implement as "release, then prune".
CREATE TABLE IF NOT EXISTS cache_entries (
    hash        TEXT PRIMARY KEY NOT NULL,
    path        TEXT NOT NULL,  -- absolute path of the blob on disk
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    refs        INTEGER NOT NULL DEFAULT 0,
    last_access TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_cache_last_access ON cache_entries(last_access);
CREATE INDEX IF NOT EXISTS idx_cache_refs        ON cache_entries(refs);

CREATE INDEX IF NOT EXISTS idx_mods_enabled    ON mods(enabled);
CREATE INDEX IF NOT EXISTS idx_mods_source     ON mods(source_id);
CREATE INDEX IF NOT EXISTS idx_mods_priority   ON mods(priority, id);
CREATE INDEX IF NOT EXISTS idx_mod_files_mod   ON mod_files(mod_id);
CREATE INDEX IF NOT EXISTS idx_load_order_pos  ON load_order(position);