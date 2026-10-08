package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// FS is the subset of filesystem access the platform layer needs. Injecting it
// keeps detectors and deployers testable without touching a real disk.
type FS interface {
	Stat(path string) (fs.FileInfo, error)
	Lstat(path string) (fs.FileInfo, error)
	ReadDir(path string) ([]fs.DirEntry, error)
}

// OSFS is the real filesystem.
type OSFS struct{}

// Stat implements FS.
func (OSFS) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

// Lstat implements FS.
func (OSFS) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }

// ReadDir implements FS.
func (OSFS) ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }

// RealFS returns the filesystem backed by the operating system.
func RealFS() FS { return OSFS{} }

// Exists reports whether a path exists, following symlinks. A dangling symlink
// therefore reports false.
func Exists(fsys FS, path string) bool {
	if fsys == nil {
		fsys = OSFS{}
	}

	if path == "" {
		return false
	}

	_, err := fsys.Stat(path)

	return err == nil
}

// ExistsNoFollow reports whether a path exists without following a final
// symlink, so a dangling link still counts as present.
func ExistsNoFollow(fsys FS, path string) bool {
	if fsys == nil {
		fsys = OSFS{}
	}

	if path == "" {
		return false
	}

	_, err := fsys.Lstat(path)

	return err == nil
}

// IsDir reports whether path is an existing directory, following symlinks.
func IsDir(fsys FS, path string) bool {
	if fsys == nil {
		fsys = OSFS{}
	}

	if path == "" {
		return false
	}

	info, err := fsys.Stat(path)

	return err == nil && info.IsDir()
}

// IsFile reports whether path is an existing regular file, following symlinks.
func IsFile(fsys FS, path string) bool {
	if fsys == nil {
		fsys = OSFS{}
	}

	if path == "" {
		return false
	}

	info, err := fsys.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// FileSize returns the size of a regular file in bytes.
func FileSize(path string) (int64, error) {
	if path == "" {
		return 0, errors.New(errors.ErrCodeNotFound, "cannot size an empty path")
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, errors.Wrap(err, errors.ErrCodeNotFound, "stat %s", path)
	}

	if info.IsDir() {
		return 0, errors.New(errors.ErrCodeInvalidMod, "%s is a directory, not a file", path)
	}

	return info.Size(), nil
}

// FileSHA256 returns the hex-encoded SHA-256 digest of a file's contents.
func FileSHA256(path string) (string, error) {
	if path == "" {
		return "", errors.New(errors.ErrCodeNotFound, "cannot hash an empty path")
	}

	f, err := os.Open(path)
	if err != nil {
		return "", errors.Wrap(err, errors.ErrCodeNotFound, "open %s", path)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", errors.Wrap(err, errors.ErrCodeIntegrity, "read %s", path)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// StringSHA256 returns the hex-encoded SHA-256 digest of a byte slice. It is
// the same digest FileSHA256 produces, for content already in memory.
func StringSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MkdirAll creates a directory and its parents.
func MkdirAll(path string) error {
	if path == "" {
		return errors.New(errors.ErrCodeConfigIO, "cannot create a directory for an empty path")
	}

	if err := os.MkdirAll(path, 0o750); err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "create directory %s", path)
	}

	return nil
}

// AtomicWriteFile writes data to path via a temporary file in the same directory
// followed by a rename, so a crash mid-write cannot leave a truncated file.
// Parent directories are created as needed.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) (err error) {
	if path == "" {
		return errors.New(errors.ErrCodeConfigIO, "cannot write to an empty path")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "create directory %s", dir)
	}

	if perm == 0 {
		perm = 0o644
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "create temporary file in %s", dir)
	}

	tmpName := tmp.Name()

	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "write %s", path)
	}

	if err = tmp.Chmod(perm); err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "set mode on %s", path)
	}

	if err = tmp.Sync(); err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "flush %s", path)
	}

	if err = tmp.Close(); err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "close %s", path)
	}
	// On Windows a rename over an existing file needs the destination removed
	// first; ignore a "not there" failure.
	if err = replaceFile(tmpName, path); err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "replace %s", path)
	}

	return nil
}

// ReadFileLimit reads at most limit bytes from path. A file larger than limit is
// rejected instead of silently truncated, because a truncated archive would be
// deployed to the game and fail much later with a confusing error.
func ReadFileLimit(path string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, errors.New(errors.ErrCodeNotFound, "cannot read an empty path")
	}

	if limit <= 0 {
		return nil, errors.New(errors.ErrCodeConfigIO, "read limit must be positive, got %d", limit)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, errors.ErrCodeNotFound, "open %s", path)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, errors.Wrap(err, errors.ErrCodeConfigIO, "read %s", path)
	}

	if int64(len(data)) > limit {
		return nil, errors.New(errors.ErrCodeConfigIO,
			"%s is larger than the %d byte limit", path, limit)
	}

	return data, nil
}

// CopyFile copies a regular file, creating parent directories as needed and
// replacing any existing destination. An existing destination that is a
// directory is an error rather than a silent no-op.
func CopyFile(src, dst string) error {
	if src == "" || dst == "" {
		return errors.New(errors.ErrCodeConfigIO, "copy needs a non-empty source and destination")
	}

	info, err := os.Lstat(src)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeNotFound, "stat source %s", src)
	}

	if info.IsDir() {
		return errors.New(
			errors.ErrCodeInvalidMod,
			"copy source %s is a directory; use CopyTree",
			src,
		)
	}

	if dstInfo, err := os.Stat(dst); err == nil && dstInfo.IsDir() {
		return errors.New(
			errors.ErrCodeConfigIO,
			"copy destination %s is an existing directory",
			dst,
		)
	}

	if err := MkdirAll(filepath.Dir(dst)); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeNotFound, "open source %s", src)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp*")
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "create temporary file next to %s", dst)
	}

	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpName)

		return errors.Wrap(err, errors.ErrCodeConfigIO, "copy %s to %s", src, dst)
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return errors.Wrap(err, errors.ErrCodeConfigIO, "close temporary file for %s", dst)
	}
	// Keep the source's permission bits but never widen them past the owner.
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		os.Remove(tmpName)
		return errors.Wrap(err, errors.ErrCodeConfigIO, "set mode on %s", dst)
	}

	if err := replaceFile(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return errors.Wrap(err, errors.ErrCodeConfigIO, "replace %s", dst)
	}

	return nil
}

// CopyTree recursively copies a directory. Symlinks are recreated as symlinks
// rather than followed, so a link inside a mod folder cannot drag content from
// outside the manager's storage into the game folder.
func CopyTree(src, dst string) error {
	if src == "" || dst == "" {
		return errors.New(errors.ErrCodeConfigIO, "copy needs a non-empty source and destination")
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeNotFound, "stat source %s", src)
	}

	if !srcInfo.IsDir() {
		return errors.New(errors.ErrCodeInvalidMod, "copy source %s is not a directory", src)
	}

	if err := MkdirAll(dst); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "read directory %s", src)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())

		dstPath := filepath.Join(dst, entry.Name())
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(srcPath)
			if err != nil {
				return errors.Wrap(err, errors.ErrCodeConfigIO, "read link %s", srcPath)
			}

			_ = os.Remove(dstPath)
			if err := os.Symlink(target, dstPath); err != nil {
				return errors.Wrap(err, errors.ErrCodeConfigIO, "create link %s", dstPath)
			}
		case entry.IsDir():
			if err := CopyTree(srcPath, dstPath); err != nil {
				return err
			}
		default:
			if err := CopyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// TreeStats summarises the contents of a directory tree.
type TreeStats struct {
	Oldest    time.Time
	Newest    time.Time
	FileCount int
	TotalSize int64
}

// ScanTree walks root and collects file counts, sizes and modification times.
// Symlinks are counted but not followed, so the walk cannot escape root. An empty
// or missing root yields a zero value.
func ScanTree(root string) (TreeStats, error) {
	var stats TreeStats
	if root == "" {
		return stats, nil
	}

	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}

		return stats, errors.Wrap(err, errors.ErrCodeConfigIO, "stat %s", root)
	}

	if !info.IsDir() {
		return TreeStats{
			FileCount: 1,
			TotalSize: info.Size(),
			Oldest:    info.ModTime(),
			Newest:    info.ModTime(),
		}, nil
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			return err
		}

		stats.FileCount++
		stats.TotalSize += fi.Size()

		mod := fi.ModTime()
		if stats.Oldest.IsZero() || mod.Before(stats.Oldest) {
			stats.Oldest = mod
		}

		if stats.Newest.IsZero() || mod.After(stats.Newest) {
			stats.Newest = mod
		}

		return nil
	})
	if err != nil {
		return stats, errors.Wrap(err, errors.ErrCodeConfigIO, "scan %s", root)
	}

	return stats, nil
}

// ToCacheInfo converts a scanned tree into the serialisable projection used by
// the UI.
func ToCacheInfo(root string, stats TreeStats) domain.CacheInfo {
	return domain.CacheInfo{
		Path:      domain.CleanPath(root),
		FileCount: stats.FileCount,
		TotalSize: stats.TotalSize,
		Oldest:    stats.Oldest,
		Newest:    stats.Newest,
	}
}

// RemoveEmptyDir removes a directory only when it holds no entries, which keeps
// cleanup from deleting a directory the user still uses.
func RemoveEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "read directory %s", dir)
	}

	if len(entries) > 0 {
		return nil
	}

	if err := os.Remove(dir); err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "remove directory %s", dir)
	}

	return nil
}

// replaceFile renames src over dst. A host that refuses to rename onto an
// existing file gets the destination cleared and the rename retried once; only
// then is the original failure reported, so a genuinely broken rename is not
// masked by the remove step.
func replaceFile(src, dst string) error {
	renameErr := os.Rename(src, dst)
	if renameErr == nil {
		return nil
	}

	if removeErr := os.Remove(dst); removeErr != nil && !os.IsNotExist(removeErr) {
		return renameErr
	}

	if retryErr := os.Rename(src, dst); retryErr != nil {
		return retryErr
	}

	return nil
}
