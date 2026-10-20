// Package domain holds the pure data model of the mod manager: types,
// constants and pure functions, with no I/O and no dependency outside the
// standard library. Every other layer builds on this package, not the reverse.
package domain

import (
	"maps"
	"slices"
	"time"
)

// ModInstallMode selects where a mod's payload is deployed.
type ModInstallMode string

const (
	// InstallModeModsFolder deploys the mod into <game>/mods/<mod folder>,
	// the default for regular mods.
	InstallModeModsFolder ModInstallMode = "mods_folder"
	// InstallModeGameRoot merges the mod into the game root. Used for mods that
	// patch the game and already carry the game's directory structure.
	InstallModeGameRoot ModInstallMode = "game_root"
)

// Valid reports whether the mode is one of the known modes.
func (m ModInstallMode) Valid() bool {
	return m == InstallModeModsFolder || m == InstallModeGameRoot
}

// String implements fmt.Stringer.
func (m ModInstallMode) String() string { return string(m) }

// Mod is an installed mod as tracked by the manager. Where it came from
// (SourceID, SourceData) is kept apart from where it lives (LocalPath) so the same
// struct can describe both a remote listing and an installed mod.
type Mod struct {
	UpdatedAt     time.Time      `json:"updated_at"`
	InstalledAt   time.Time      `json:"installed_at"`
	SourceData    map[string]any `json:"source_data,omitempty"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	SourceID      string         `json:"source_id"`
	GameID        string         `json:"game_id,omitempty"`
	Author        string         `json:"author"`
	GameVersion   string         `json:"game_version,omitempty"`
	ModFolderName string         `json:"mod_folder_name,omitempty"`
	InstallMode   ModInstallMode `json:"install_mode,omitempty"`
	Description   string         `json:"description"`
	Hash          string         `json:"hash,omitempty"`
	Version       string         `json:"version"`
	LocalPath     string         `json:"local_path"`
	UpdateURL     string         `json:"update_url,omitempty"`
	DownloadURL   string         `json:"download_url,omitempty"`
	SourceURL     string         `json:"source_url,omitempty"`
	Hooks         ModHooks       `json:"hooks"`
	Dependencies  []string       `json:"dependencies,omitempty"`
	ConflictsWith []string       `json:"conflicts_with,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	Priority      int            `json:"priority"`
	Enabled       bool           `json:"enabled"`
}

// ModFile is one file belonging to an installed mod. StoredPath is absolute;
// RelPath is relative to the mod directory and is the identity of the file
// within the mod, used for conflict detection between mods.
type ModFile struct {
	ModID      string `json:"mod_id"`
	RelPath    string `json:"file_path"`
	StoredPath string `json:"stored_path"`
	Checksum   string `json:"checksum"`
	SizeBytes  int64  `json:"size_bytes"`
}

// Clone returns a deep copy so a caller can mutate a mod without touching the copy
// held by the index.
func (m *Mod) Clone() *Mod {
	if m == nil {
		return nil
	}

	out := *m
	out.Dependencies = cloneStrings(m.Dependencies)
	out.ConflictsWith = cloneStrings(m.ConflictsWith)

	out.Tags = cloneStrings(m.Tags)
	if m.SourceData != nil {
		out.SourceData = make(map[string]any, len(m.SourceData))
		maps.Copy(out.SourceData, m.SourceData)
	}

	out.Hooks = m.Hooks.Clone()

	return &out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}

	out := make([]string, len(in))
	copy(out, in)

	return out
}

// HasDependency reports whether the mod declares a dependency on depID. The
// comparison is exact: callers that need loose matching should normalise first.
func (m *Mod) HasDependency(depID string) bool {
	for _, d := range m.Dependencies {
		if d == depID {
			return true
		}
	}

	return false
}

// ConflictsWithMod reports whether the mod declares a conflict with otherID.
func (m *Mod) ConflictsWithMod(otherID string) bool {
	return slices.Contains(m.ConflictsWith, otherID)
}

// HasTag reports whether the mod carries tag, compared case-insensitively since
// tags come from remote sources with inconsistent casing.
func (m *Mod) HasTag(tag string) bool {
	for _, t := range m.Tags {
		if EqualFold(t, tag) {
			return true
		}
	}

	return false
}

// ModUpdateInfo describes an available update for an installed mod.
type ModUpdateInfo struct {
	ModID         string `json:"mod_id"`
	LocalVersion  string `json:"local_version"`
	RemoteVersion string `json:"remote_version"`
	SourceID      string `json:"source_id"`
	ReleaseNotes  string `json:"release_notes,omitempty"`
	DownloadURL   string `json:"download_url,omitempty"`
	ChangelogURL  string `json:"changelog_url,omitempty"`
	Available     bool   `json:"available"`
}

// ConflictType enumerates why two installed mods are incompatible.
type ConflictType string

const (
	ConflictFileOverlap             ConflictType = "file_overlap"
	ConflictDuplicateID             ConflictType = "duplicate_id"
	ConflictLoadOrder               ConflictType = "load_order"
	ConflictVersionMismatch         ConflictType = "version_mismatch"
	ConflictDeclaredIncompatibility ConflictType = "declared"
)

// ConflictSeverity grades how badly a conflict affects the game.
type ConflictSeverity string

const (
	SeverityWarning ConflictSeverity = "warning"
	SeverityError   ConflictSeverity = "error"
)

// ConflictReport is a single detected conflict between two mods.
type ConflictReport struct {
	// ID is the identity the resolution path takes. A field rather than a
	// method because a method does not serialise, and a bridge caller has
	// nothing but the JSON to refer back to.
	ID             string           `json:"id"`
	Type           ConflictType     `json:"type"`
	ModA           *Mod             `json:"mod_a,omitempty"`
	ModB           *Mod             `json:"mod_b,omitempty"`
	Details        string           `json:"details"`
	Severity       ConflictSeverity `json:"severity"`
	Recommendation string           `json:"recommendation"`
}

// ID returns a stable identity for a report so a caller can refer to it later —
// "resolve conflict 3" becomes "resolve <id>".
//
// A report has no natural key: Details is prose and Type is not unique. The ids
// are sorted so a report found with ModA and ModB swapped gets the same identity,
// and an empty id is kept as an empty field so "declared:" and "declared::" cannot
// collide.
func (r ConflictReport) Identity() string {
	a, b := r.modIDs()
	return string(r.Type) + ":" + a + ":" + b
}

// modIDs returns the two mod ids in sorted order, tolerating a missing side.
//
// The missing side sorts last: the empty string compares below every real id, so
// sorting naively would give a one-sided report the same id as a pair whose first
// id is empty.
func (r ConflictReport) modIDs() (string, string) {
	a, b := "", ""
	if r.ModA != nil {
		a = r.ModA.ID
	}

	if r.ModB != nil {
		b = r.ModB.ID
	}

	if a == "" || (b != "" && b < a) {
		a, b = b, a
	}

	return a, b
}

// ModIDs returns the two mod ids in sorted order and whether both ends are
// present, so a single-sided report still reports what it has.
func (r ConflictReport) ModIDs() (first, second string, both bool) {
	a, b := r.modIDs()
	return a, b, a != "" && b != ""
}

// CacheInfo summarises the download cache for status UIs.
type CacheInfo struct {
	Oldest    time.Time `json:"oldest"`
	Newest    time.Time `json:"newest"`
	Path      string    `json:"path"`
	FileCount int       `json:"file_count"`
	TotalSize int64     `json:"total_size"`
}

// ModSourceInfo is the serialisable description of a registered source: the live
// ModSource interface cannot cross the Wails boundary, so the UI gets this.
type ModSourceInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Version        string `json:"version"`
	SupportsSearch bool   `json:"supports_search"`
	SupportsAuth   bool   `json:"supports_auth"`
	RequiresAPIKey bool   `json:"requires_api_key"`
	Enabled        bool   `json:"enabled"`
	Authenticated  bool   `json:"authenticated"`
	// HasAPIKey reports only that a credential is stored, never the key: a UI
	// needs to choose an input box, and the value has no business in JSON.
	HasAPIKey bool `json:"has_api_key"`
}
