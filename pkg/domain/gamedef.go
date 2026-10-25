package domain

import "strings"

// GameDefinition describes one supported game: what the manager needs to find and
// validate its installation, locate its mods folder, and give each source its
// per-game defaults. The rest of the program depends on the interface, never on a
// concrete game.
type GameDefinition interface {
	// ID is the registry key, used in paths, the database and the config.
	ID() string
	// DisplayName is the human-readable title shown in the UI and CLI.
	DisplayName() string
	// Description is a one-line summary shown next to the display name.
	Description() string
	// SteamAppID is the Steam application id ("" when the game is not on
	// Steam). It also identifies the Proton compatdata directory on Linux.
	SteamAppID() string
	// InstallDirNames are the folder names probed under launcher roots.
	InstallDirNames() []string
	// BinaryNames are the executables used to validate an installation path.
	BinaryNames() []string
	// VersionMarkers are files probed, in order, for the installed version.
	VersionMarkers() []string
	// ModsPathCandidates are relative layouts tried, in order, to locate the
	// mods folder inside an installation.
	ModsPathCandidates() [][]string
	// ModsDirName is the default mods folder name used when no candidate
	// layout resolves.
	ModsDirName() string
	// ModInstallMethod is the deployment default for this game: either
	// InstallMethodSymlink or InstallMethodDirect.
	ModInstallMethod() string
	// ManifestNames are the payload manifests recognised by the local source
	// and the deployer; they are never deployed into the game.
	ManifestNames() []string
	// LoadOrderFile is the file written into the mods folder to pin load
	// order; "" when the game has no such file.
	LoadOrderFile() string
	// InstallModes lists the ModInstallMode values this game supports.
	InstallModes() []ModInstallMode
	// SourceDefaults gives per-source option defaults, keyed by source id then
	// option name. The user's source config always wins over these.
	SourceDefaults() map[string]map[string]string
	// GameActions returns code-defined special actions for this game.
	GameActions() []GameAction
}

// Installation methods a GameDefinition may declare.
const (
	// InstallMethodSymlink deploys mods as symlinks into the game folder,
	// falling back to copies where symlinks cannot be created.
	InstallMethodSymlink = "symlink"
	// InstallMethodDirect copies mod payloads straight into the game folder.
	InstallMethodDirect = "direct"
)

// ValidInstallMethod reports whether m is one of the known methods.
func ValidInstallMethod(m string) bool {
	return m == InstallMethodSymlink || m == InstallMethodDirect
}

// GameSettings is one game's slice of the configuration: where its
// installation is, which launcher to prefer when detecting it, where its mods
// live, how mods reach the game folder, and which icon represents it in the
// rail. The map key in Config.Games is the game's registry id.
type GameSettings struct {
	// Icon names a built-in rail icon or holds a data URL for a custom image.
	// Here, not on GameDefinition because it is one user's choice on one machine.
	Icon               string     `json:"icon,omitempty"`
	Path               string     `json:"path,omitempty"`
	Source             GameSource `json:"source,omitempty"`
	ModsFolderOverride string     `json:"mods_folder_override,omitempty"`
	// InstallMethod overrides how mods reach the game folder: symlinks or a
	// direct copy. Empty means "whatever the game definition prefers".
	InstallMethod string `json:"install_method,omitempty"`
	AutoDetect    bool   `json:"auto_detect"`
}

// InstallMethodOrDefault reports the install method to prefer: the configured
// override when it is one of the known methods, otherwise the definition's own.
//
// An unrecognised stored value falls through, not failing, so a config
// written by a newer version still deploys.
func (s GameSettings) InstallMethodOrDefault(def GameDefinition) string {
	if ValidInstallMethod(s.InstallMethod) {
		return s.InstallMethod
	}

	if def == nil {
		return ""
	}

	method := strings.TrimSpace(def.ModInstallMethod())
	if ValidInstallMethod(method) {
		return method
	}

	return ""
}

// SourceDomainKey is the SourceDefaults key naming the sub-domain a game occupies on
// a hosted source. It lives here so a game, a source plugin and the code that maps a
// link back to a game all spell it the same way.
const SourceDomainKey = "game_domain"

// SourceDefault reads one per-source default out of a game definition. A nil
// definition or a missing entry yields "", which callers read as "fall back to the
// source's own default".
func SourceDefault(def GameDefinition, sourceID, key string) string {
	if def == nil {
		return ""
	}

	return strings.TrimSpace(def.SourceDefaults()[sourceID][key])
}

// DefaultGameSettings is the settings block used when a game has no entry in
// the configuration yet: no path pinned, detection on, every launcher allowed.
func DefaultGameSettings() GameSettings {
	return GameSettings{AutoDetect: true}
}
