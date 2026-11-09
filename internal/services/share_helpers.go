package services

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ChocolateNao/darktide-mod-manager/internal/storage"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/rs/zerolog"
)

// ModInstaller is the slice of ModService the sharing path needs: install a mod
// from its source, or unpack a verified archive that arrived over a share.
type ModInstaller interface {
	Install(ctx context.Context, modID, sourceID string, opts InstallOptions) (*domain.Mod, error)
	InstallArchiveFromShare(
		ctx context.Context,
		mod *domain.Mod,
		archivePath string,
		opts InstallOptions,
	) (*domain.Mod, error)
}

// ShareGameLocator is the slice of GameLocator the sharing path needs: which
// game is active. Narrowing it keeps sharing testable without a filesystem-backed
// configuration.
type ShareGameLocator interface {
	Active() (domain.GameDefinition, domain.GameSettings, error)
}

// shareInstallOptions maps a share's install preference onto the installer's.
// AutoInstall is a property of the share, so it becomes the installer's explicit
// enable flag, not the installer's own auto-detection.
func shareInstallOptions(opts domain.InstallOptions) InstallOptions {
	if !opts.AutoInstall {
		return InstallOptions{}
	}

	enabled := true

	return InstallOptions{Enable: &enabled}
}

// collectModFiles lists a mod's payload files with their hashes. A file that
// cannot be hashed is logged and skipped, not failing the share: one unreadable
// file should not cost the user the other nineteen.
func collectModFiles(logger zerolog.Logger, mod domain.Mod) []domain.ShareFile {
	if mod.LocalPath == "" {
		return nil
	}

	entries, err := storage.CollectTree(mod.LocalPath)
	if err != nil {
		logger.Warn().
			Str("op", "share-pack").
			Str("mod", mod.Name).
			Str("path", mod.LocalPath).
			Err(err).
			Msg("failed to read mod directory")

		return nil
	}

	files := make([]domain.ShareFile, 0, len(entries))

	for _, entry := range entries {
		hash, err := fileHash(entry.Source)
		if err != nil {
			logger.Warn().
				Str("op", "share-pack").
				Str("mod", mod.Name).
				Str("path", entry.Path).
				Err(err).
				Msg("failed to hash file")

			continue
		}

		files = append(files, domain.ShareFile{
			Path:   entry.Path,
			Source: entry.Source,
			Size:   entry.Size,
			Hash:   hash,
		})
	}

	return files
}

// fileHash returns the lowercase hex SHA-256 of a file's contents.
func fileHash(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
