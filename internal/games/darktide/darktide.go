// Package darktide is the Warhammer 40,000: Darktide game definition. The
// facts that used to be scattered through pkg/domain and internal/platform
// as constants live here, behind domain.GameDefinition.
package darktide

import "github.com/ChocolateNao/darktide-mod-manager/pkg/domain"

// The identifiers this game answers to. ID is the registry key used in
// paths, the database and the configuration.
const (
	// ID is the registry key for Darktide.
	ID = "darktide"
	// SteamAppID is Darktide's Steam application id; it also names the
	// Proton compatdata directory on Linux.
	SteamAppID = "1361210"
	// BinaryName is the executable used to validate an installation.
	BinaryName = "Darktide.exe"
	// ModsDirName is the default mods folder inside an installation.
	ModsDirName = "mods"
	// LoadOrderName is the file written into the mods folder to pin the
	// order the game loads mods in.
	LoadOrderName = "mod_load_order.txt"
)

// Definition implements domain.GameDefinition for Darktide. It is stateless:
// every method returns fresh slices so a caller cannot mutate what the
// registry handed to everyone else.
type Definition struct{}

// New returns the Darktide game definition.
func New() domain.GameDefinition { return Definition{} }

// ID implements domain.GameDefinition.
func (Definition) ID() string { return ID }

// DisplayName implements domain.GameDefinition.
func (Definition) DisplayName() string { return "Warhammer 40,000: Darktide" }

// Description implements domain.GameDefinition.
func (Definition) Description() string {
	return "A first-person co-op shooter set in the Warhammer 40,000 universe."
}

// SteamAppID implements domain.GameDefinition.
func (Definition) SteamAppID() string { return SteamAppID }

// InstallDirNames implements domain.GameDefinition.
func (Definition) InstallDirNames() []string { return []string{"Darktide"} }

// BinaryNames implements domain.GameDefinition.
func (Definition) BinaryNames() []string { return []string{BinaryName} }

// VersionMarkers implements domain.GameDefinition.
func (Definition) VersionMarkers() []string {
	return []string{BinaryName, "Darktide", "version.txt"}
}

// ModsPathCandidates implements domain.GameDefinition.
func (Definition) ModsPathCandidates() [][]string {
	return [][]string{
		{"data", ModsDirName},
		{ModsDirName},
		{"b3data", "temp", ModsDirName},
	}
}

// ModsDirName implements domain.GameDefinition.
func (Definition) ModsDirName() string { return ModsDirName }

// ModInstallMethod implements domain.GameDefinition. Mods are symlinked into
// the game folder by default, with copies as the fallback on file systems
// that cannot create symlinks.
func (Definition) ModInstallMethod() string { return domain.InstallMethodSymlink }

// ManifestNames implements domain.GameDefinition. These files describe a mod
// payload and are never deployed into the game.
func (Definition) ManifestNames() []string {
	return []string{"mod.toml", "manifest.toml", "metadata.toml"}
}

// LoadOrderFile implements domain.GameDefinition.
func (Definition) LoadOrderFile() string { return LoadOrderName }

// InstallModes implements domain.GameDefinition.
func (Definition) InstallModes() []domain.ModInstallMode {
	return []domain.ModInstallMode{
		domain.InstallModeModsFolder,
		domain.InstallModeGameRoot,
	}
}

// SourceDefaults implements domain.GameDefinition. Shared sources read these
// when the user's config has no explicit option; a configured option always
// wins.
func (Definition) SourceDefaults() map[string]map[string]string {
	return map[string]map[string]string{
		"nexus":  {"game_domain": "warhammer40kdarktide"},
		"github": {"topic": "darktide-mod"},
	}
}
