package services

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// stubInstaller records installs without running any of the real machinery. It
// backs the share service tests so an install seam can be asserted on.
type stubInstaller struct {
	installErr error
	failFor    map[string]error
	installed  []domain.Mod
	archives   []string
}

func (i *stubInstaller) Install(
	_ context.Context,
	modID, sourceID string,
	_ InstallOptions,
) (*domain.Mod, error) {
	if i.installErr != nil {
		return nil, i.installErr
	}

	if err, ok := i.failFor[modID]; ok {
		return nil, err
	}

	for _, m := range i.installed {
		if m.ID == modID {
			out := m
			out.ID = modID
			out.SourceID = sourceID

			return &out, nil
		}
	}

	return &domain.Mod{ID: modID, SourceID: sourceID}, nil
}

// InstallArchiveFromShare records the archive it was handed instead of placing
// anything. The orchestrator's job is to get a verified archive to this seam.
func (i *stubInstaller) InstallArchiveFromShare(
	_ context.Context,
	mod *domain.Mod,
	archivePath string,
	_ InstallOptions,
) (*domain.Mod, error) {
	i.archives = append(i.archives, archivePath)

	if i.installErr != nil {
		return nil, i.installErr
	}

	if err, ok := i.failFor[mod.ID]; ok {
		return nil, err
	}

	return mod.Clone(), nil
}

// stubLocator resolves the active game without touching the filesystem.
type stubLocator struct {
	id    string
	found bool
}

func (l *stubLocator) Active() (domain.GameDefinition, domain.GameSettings, error) {
	if !l.found {
		return nil, domain.GameSettings{}, appErrors.ErrGameNotFound
	}

	return stubID(l.id), domain.GameSettings{}, nil
}

// stubID returns a GameDefinition whose ID is id.
func stubID(id string) domain.GameDefinition {
	return stubDef{id: id}
}

type stubDef struct{ id string }

func (d stubDef) ID() string                                   { return d.id }
func (d stubDef) DisplayName() string                          { return d.id }
func (d stubDef) Description() string                          { return "" }
func (d stubDef) SteamAppID() string                           { return "" }
func (d stubDef) InstallDirNames() []string                    { return nil }
func (d stubDef) BinaryNames() []string                        { return nil }
func (d stubDef) VersionMarkers() []string                     { return nil }
func (d stubDef) ModsPathCandidates() [][]string               { return nil }
func (d stubDef) ModsDirName() string                          { return "" }
func (d stubDef) ModInstallMethod() string                     { return "" }
func (d stubDef) ManifestNames() []string                      { return nil }
func (d stubDef) LoadOrderFile() string                        { return "" }
func (d stubDef) InstallModes() []domain.ModInstallMode        { return nil }
func (d stubDef) SourceDefaults() map[string]map[string]string { return nil }

func (d stubDef) GameActions() []domain.GameAction { return nil }
