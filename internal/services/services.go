// Package services holds the mod manager's use cases.
//
// Each service owns one slice of behaviour — mod lifecycle, source
// discovery, game detection, download caching, hook execution — and depends on
// the small interfaces declared here rather than on concretions. Concretions
// (SQLite, the filesystem, HTTP sources, Goja) are wired in internal/di and
// injected through the constructors, which is what lets the whole layer be
// exercised against fakes.
//
// The interfaces are deliberately narrower than the implementations they stand
// in for. storage.ModRepository, for instance, exposes twenty methods, while
// ModService needs fewer than half of them; the narrower interface below means a
// test double has fewer methods to keep honest.
package services

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// ConfigStore is the slice of storage.ConfigStore the services need. Depending
// on this rather than the concrete store keeps the services testable and lets a
// future implementation serve configuration from the database instead of a file.
type ConfigStore interface {
	// Path is the file the configuration is read from and written to.
	Path() string
	// Load returns the stored configuration. A missing file yields defaults
	// rather than an error, so first run needs no special case.
	Load() (*domain.Config, error)
	// Save replaces the stored configuration.
	Save(cfg *domain.Config) error
	// SaveIfChanged writes only when the canonical encoding differs from what is
	// already on disk, and reports whether it wrote. Services call this on paths
	// that run on a timer so an unchanged configuration is not rewritten.
	SaveIfChanged(cfg *domain.Config) (bool, error)
}

// InstallationDetector probes the machine for Darktide installations. The
// platform package provides the real implementation; tests substitute a fake
// with a fixed set of candidates.
type InstallationDetector interface {
	// FindInstallations probes every known candidate location.
	FindInstallations(ctx context.Context) ([]domain.GameInstallation, error)
	// ValidateInstallation checks one user-supplied path.
	ValidateInstallation(path string) (*domain.GameInstallation, error)
	// GetModsPath resolves the mods folder for an installation, honouring a
	// configured override.
	GetModsPath(install *domain.GameInstallation, override string) (string, error)
}

// DetectorFactory builds a detector for one configuration snapshot.
//
// It is a function rather than a stored detector because the configured game
// path has to be folded in as an extra candidate, and the configuration is
// re-read on every lookup: a user who repoints the game in the UI must not need
// the process restarted for detection to agree with the config file.
type DetectorFactory func(cfg domain.GameConfig) InstallationDetector
