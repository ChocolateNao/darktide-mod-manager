package storage

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// PackEntry is one file to place into a share archive. Name is the path the file
// will carry inside the archive, written as given, so two mods each shipping one
// are kept apart by the archive they sit in rather than by renaming contents.
type PackEntry struct {
	Source string
	Name   string
}

// PackFile is one entry after packing. Checksum is computed over the bytes as they
// were written, so a recipient re-hashing the extracted file with the same
// algorithm lands on the same value.
type PackFile struct {
	Name     string
	Checksum string
	Size     int64
}

// PackResult reports what a packed archive holds.
type PackResult struct {
	Files []PackFile
	// TotalSize is the sum of the entry sizes, which is what a preview should
	// show: the compressed archive on the provider can be far smaller, and quoting
	// that would understate what a recipient is about to download.
	TotalSize int64
}

// PackArchive writes entries to destPath as a single zip archive and returns the inventory.
//
// The reported hash is of the archive file rather than the entries, so the recipient verifies
// the download in one pass; entry checksums are recorded because the mod index expects one
// per file. The archive is written to a temporary file and moved into place, because a
// partial one at destPath would be indistinguishable from a finished one.
func PackArchive(ctx context.Context, destPath string, entries []PackEntry) (*PackResult, error) {
	if destPath == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "no destination path given")
	}

	if len(entries) == 0 {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "no files to pack")
	}

	parent := filepath.Dir(destPath)
	if err := platform.MkdirAll(parent); err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp(parent, ".pack-*.zip")
	if err != nil {
		return nil, fmt.Errorf("create a staging archive in %s: %w", parent, err)
	}

	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	result, err := writeZip(ctx, tmp, entries)
	if err != nil {
		_ = tmp.Close()

		return nil, err
	}

	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close the packed archive %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		return nil, fmt.Errorf("move the packed archive into place: %w", err)
	}

	return result, nil
}

// writeZip streams entries into an open writer and reports the inventory.
func writeZip(ctx context.Context, w io.Writer, entries []PackEntry) (*PackResult, error) {
	zw := zip.NewWriter(w)

	result := &PackResult{}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if entry.Name == "" {
			// An empty entry name is a path separator in the archive and would
			// collide with anything above it.
			return nil, appErrors.New(
				appErrors.ErrCodeInvalidArgument,
				"a packed file needs a name",
			)
		}

		size, checksum, err := addZipEntry(ctx, zw, entry)
		if err != nil {
			_ = zw.Close()

			return nil, err
		}

		result.Files = append(result.Files, PackFile{
			Name:     entry.Name,
			Checksum: checksum,
			Size:     size,
		})

		result.TotalSize += size
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finish the packed archive: %w", err)
	}

	return result, nil
}

// addZipEntry copies one file into the archive and reports its size and hash.
func addZipEntry(ctx context.Context, zw *zip.Writer, entry PackEntry) (int64, string, error) {
	src, err := os.Open(filepath.Clean(entry.Source))
	if err != nil {
		return 0, "", fmt.Errorf("open %s to pack: %w", entry.Source, err)
	}

	defer func() { _ = src.Close() }()

	header := &zip.FileHeader{
		Name:   entry.Name,
		Method: zip.Deflate,
	}
	// Stored mods are never meant to be executed from inside the archive, so the mode
	// is fixed rather than copied: a source mod that happens to be executable
	// should not install as one.
	header.SetMode(0o644)

	dst, err := zw.CreateHeader(header)
	if err != nil {
		return 0, "", fmt.Errorf("add %s to the archive: %w", entry.Name, err)
	}

	hasher := sha256.New()

	size, err := io.Copy(io.MultiWriter(dst, hasher), src)
	if err != nil {
		return 0, "", fmt.Errorf("write %s into the archive: %w", entry.Name, err)
	}

	if err := ctx.Err(); err != nil {
		return 0, "", err
	}

	return size, fmt.Sprintf("%x", hasher.Sum(nil)), nil
}
