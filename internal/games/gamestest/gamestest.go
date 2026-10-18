// Package gamestest provides a GameDefinition stand-in for packages that need a
// game but must not depend on a concrete one.
package gamestest

import "github.com/ChocolateNao/darktide-mod-manager/pkg/domain"

// Def is a minimal GameDefinition. Only ID and SourceDefaults are configurable;
// everything else returns the smallest value that satisfies the interface.
type Def struct {
	Defaults map[string]map[string]string
	IDValue  string
}

// ID implements domain.GameDefinition.
func (d Def) ID() string {
	if d.IDValue == "" {
		return "test-game"
	}

	return d.IDValue
}

// DisplayName implements domain.GameDefinition.
func (Def) DisplayName() string { return "Test Game" }

// Description implements domain.GameDefinition.
func (Def) Description() string { return "a test double" }

// SteamAppID implements domain.GameDefinition.
func (Def) SteamAppID() string { return "" }

// InstallDirNames implements domain.GameDefinition.
func (Def) InstallDirNames() []string { return []string{"TestGame"} }

// BinaryNames implements domain.GameDefinition.
func (Def) BinaryNames() []string { return []string{"test.exe"} }

// VersionMarkers implements domain.GameDefinition.
func (Def) VersionMarkers() []string { return []string{"test.exe"} }

// ModsPathCandidates implements domain.GameDefinition.
func (Def) ModsPathCandidates() [][]string { return [][]string{{"mods"}} }

// ModsDirName implements domain.GameDefinition.
func (Def) ModsDirName() string { return "mods" }

// ModInstallMethod implements domain.GameDefinition.
func (Def) ModInstallMethod() string { return domain.InstallMethodDirect }

// ManifestNames implements domain.GameDefinition.
func (Def) ManifestNames() []string { return []string{"mod.toml"} }

// LoadOrderFile implements domain.GameDefinition.
func (Def) LoadOrderFile() string { return "" }

// InstallModes implements domain.GameDefinition.
func (Def) InstallModes() []domain.ModInstallMode {
	return []domain.ModInstallMode{domain.InstallModeModsFolder}
}

// SourceDefaults implements domain.GameDefinition.
func (d Def) SourceDefaults() map[string]map[string]string {
	if d.Defaults == nil {
		return map[string]map[string]string{}
	}

	return d.Defaults
}

// GameActions implements domain.GameDefinition.
func (Def) GameActions() []domain.GameAction {
	return nil
}
