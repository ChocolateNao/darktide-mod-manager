// Package services holds the mod manager's use cases.
//
// Each service owns one slice of behaviour — mod lifecycle, source discovery,
// game detection, download caching, hook execution — and depends on the narrow
// interfaces declared here. Concretions are wired in internal/di, and the
// narrowness keeps a test double's method count low.
package services

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// ConfigStore is the slice of storage.ConfigStore the services need, narrow
// enough that a test double can keep it honest.
type ConfigStore interface {
	// Path is the file the configuration is read from and written to.
	Path() string
	// Load returns the stored configuration.
	Load() (*domain.Config, error)
	// Save replaces the stored configuration.
	Save(cfg *domain.Config) error
	// SaveIfChanged writes only when the canonical encoding differs from what is
	// on disk, and reports whether it wrote.
	SaveIfChanged(cfg *domain.Config) (bool, error)
}

// InstallationDetector probes the machine for Darktide installations; tests
// substitute a fake with a fixed set of candidates.
type InstallationDetector interface {
	// FindInstallations probes every known candidate location.
	FindInstallations(ctx context.Context) ([]domain.GameInstallation, error)
	// ValidateInstallation checks one user-supplied path.
	ValidateInstallation(path string) (*domain.GameInstallation, error)
	// GetModsPath resolves the mods folder for an installation, honouring a
	// configured override.
	GetModsPath(install *domain.GameInstallation, override string) (string, error)
}

// DetectorFactory builds a detector for one registered game plus one game's
// configuration snapshot.
//
// A function, not a stored detector, because the configuration is re-read
// on every lookup: a user who repoints the game in the UI must not need a restart
// for detection to agree with the config file.
type DetectorFactory func(
	game domain.GameDefinition,
	cfg domain.GameSettings,
) InstallationDetector
