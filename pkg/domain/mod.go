// Package domain holds the pure data model of the mod manager. It has no
// dependencies outside the standard library and performs no I/O: everything in
// here is a type, a constant, or a pure function over those types. Services,
// storage and UI layers build on this package, never the other way around.
package domain

import (
	"maps"
	"slices"
	"time"
)

// ModInstallMode selects where a mod's payload is deployed.
type ModInstallMode string

const (
	// InstallModeModsFolder deploys the mod into the game's mods folder
	// (<game>/mods/<mod folder>). This is the default for regular mods.
	InstallModeModsFolder ModInstallMode = "mods_folder"
	// InstallModeGameRoot deploys the mod by merging its contents into the
	// game root folder. Used for mods that patch the game and already contain
	// the "mods_folder" defined by the game folder and other game directories in their archive
	// structure.
	InstallModeGameRoot ModInstallMode = "game_root"
)

// Valid reports whether the mode is one of the known modes.
func (m ModInstallMode) Valid() bool {
	return m == InstallModeModsFolder || m == InstallModeGameRoot
}

// String implements fmt.Stringer.
func (m ModInstallMode) String() string { return string(m) }

// Mod is an installed mod as tracked by the manager. Fields that describe where a
// mod came from (SourceID, SourceData) are separated from fields that describe
// where it lives on disk (LocalPath) so that the same struct can represent a
// remote listing and an installed mod.
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

// Clone returns a deep copy so that callers can mutate a mod without touching
// the copy held by the index. Slices and maps are duplicated; time values are
// values already.
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
	Type           ConflictType
	ModA           *Mod
	ModB           *Mod
	Details        string
	Severity       ConflictSeverity
	Recommendation string
}

// ID returns a stable identity for a report so a caller can hold on to it and
// refer to it later — "resolve conflict 3" becomes "resolve <id>".
//
// A ConflictReport has no natural key of its own: Details is prose, and Type
// alone is not unique. The identity is therefore derived from what actually
// distinguishes one report from another — its type and the pair of mods involved.
//
// The two mod ids are sorted so that a report found with ModA and ModB swapped
// produces the same ID: the order the detector happened to walk the mods in is
// not part of the conflict. An empty id is kept as an empty field rather than
// dropped, so "declared:" and "declared::" cannot collide.
func (r ConflictReport) ID() string {
	a, b := r.modIDs()
	return string(r.Type) + ":" + a + ":" + b
}

// modIDs returns the two mod ids in sorted order, tolerating a missing side.
//
// The missing side sorts *last*, not first: the empty string compares below every
// real id, so sorting naively would make a one-sided report read
// "load_order::better_ui" and, worse, give it the same id as any pair whose
// first id were the empty string.
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

// ModIDs returns the two mod ids involved in the report, in sorted order, and
// whether both ends are present. A single-sided report (one mod contradicts a
// version requirement rather than another mod) still reports what it has.
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

// ModSourceInfo is the serialisable description of a registered source. The
// live ModSource interface cannot cross the Wails boundary, so the UI receives
// this projection instead.
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
	// HasAPIKey reports that a credential is stored for this source. It is
	// deliberately a yes/no answer and never the key: a settings screen needs to
	// know whether to show an input box or a "key stored, replace it?" prompt,
	// and the value itself has no business in a JSON payload a UI renders.
	HasAPIKey bool `json:"has_api_key"`
}
