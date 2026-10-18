package local

// This file holds the parts of the local source that are about a mod *folder*
// rather than about where it came from: the id a folder gets without a manifest,
// the string rules discovery needs, and the tree copy that installs a payload.

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// fallbackID derives a mod id from a folder name.
func fallbackID(dir string) string {
	return SanitizeID(filepath.Base(filepath.Clean(dir)))
}

// firstNonBlank returns the first non-blank value trimmed, or "".
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}

	return ""
}

// modLabel renders a mod for an error message without dereferencing nil.
func modLabel(mod *domain.Mod) string {
	if mod == nil || strings.TrimSpace(mod.ID) == "" {
		return "<unknown>"
	}

	return mod.ID
}

// looksLikeVersion reports whether s is a dotted numeric version such as "1.2.0".
func looksLikeVersion(s string) bool {
	if s == "" {
		return false
	}

	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}

		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}

	return true
}

// copyTree copies a folder tree, refusing to follow a symlink out of it. A local mod
// folder is user-owned, so links inside it are expected; one whose target escapes the
// tree would let a "copy" read arbitrary files and index them as mod content.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read %q", path)
		}

		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return appErrors.Wrap(
				relErr,
				appErrors.ErrCodeConfigIO,
				"resolve %q under %q",
				path,
				src,
			)
		}

		target := filepath.Join(dst, rel)

		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, 0o750); err != nil {
				return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create %q", target)
			}

			return nil
		case d.Type()&fs.ModeSymlink != 0:
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "resolve link %q", path)
			}

			within, err := filepath.Rel(src, resolved)
			if err != nil || within == ".." ||
				strings.HasPrefix(within, ".."+string(filepath.Separator)) {
				return appErrors.New(appErrors.ErrCodeInvalidMod,
					"link %q points outside the mod folder and was not copied", rel)
			}

			return copyFile(resolved, target)
		case !d.Type().IsRegular():
			// Sockets, devices and FIFOs cannot appear in a mod archive anyway.
			return nil
		default:
			return copyFile(path, target)
		}
	})
}

// copyFile copies one file, preserving its permission bits. The destination must
// not exist: the caller has already removed any pre-existing entry.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "open %q", src)
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "stat %q", src)
	}

	if info.IsDir() {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "%q is a directory", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create %q", filepath.Dir(dst))
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create %q", dst)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "copy %q", src)
	}

	if err := out.Close(); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "close %q", dst)
	}

	return nil
}
