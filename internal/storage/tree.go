package storage

// This file owns the walk over a mod folder. Three callers need the same answer
// — what files does this mod actually contain, and where are they — and they
// need it to be the *same* answer: the local source when it copies a folder into
// the download staging area, the cache when it turns whatever a source handed it
// into one blob, and the share service when it packs a mod to send. A walker per
// caller is how mods with subfolders end up installed, cached and shared with
// different contents.

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// TreeEntry is one file found by CollectTree.
type TreeEntry struct {
	// Path is the file's location relative to the root, slash-separated. It is
	// the name the file gets inside an archive and inside the installed mod, so
	// it must not depend on the machine the walk ran on.
	Path string
	// Source is where the bytes are read from.
	Source string
	// Size is the file's size in bytes.
	Size int64
	// Mode is the file's permission bits, kept so a caller that reproduces the
	// layout on disk can decide what to do with them.
	Mode fs.FileMode
}

// PackEntry describes this file to PackArchive.
func (e TreeEntry) PackEntry() PackEntry {
	return PackEntry{Source: e.Source, Name: e.Path}
}

// CollectTree walks root and returns every regular file beneath it, sorted by
// path so that two runs over the same folder produce the same archive.
//
// Names are relative and slash-separated rather than absolute: an archive that
// carried the sharer's home directory in every entry name would leak the layout
// of their machine to the recipient and would not extract anywhere.
//
// Symlinks are followed only when they resolve inside the tree. A mod folder is
// user-owned, so links inside it are expected; a link pointing out of it would
// turn "share this folder" into "read whatever it points at", which is the same
// reason the local source refuses to copy one. Such a link is refused rather
// than quietly skipped, because a mod that silently loses a file is the failure
// mode this walk exists to remove.
//
// Entries that are neither files nor directories — sockets, devices, FIFOs — are
// skipped: they have no meaning inside a mod archive.
func CollectTree(root string) ([]TreeEntry, error) {
	if strings.TrimSpace(root) == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "no folder to collect")
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read %q", root)
	}

	if !info.IsDir() {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "%q is not a folder", root)
	}

	var entries []TreeEntry

	walkErr := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read %q", current)
		}

		relative, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return appErrors.Wrap(relErr, appErrors.ErrCodeConfigIO,
				"resolve %q under %q", current, root)
		}

		if entry.IsDir() {
			return nil
		}

		source := current

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return appErrors.Wrap(resolveErr, appErrors.ErrCodeConfigIO,
					"resolve link %q", current)
			}

			if escapes(root, resolved) {
				return appErrors.New(appErrors.ErrCodeInvalidMod,
					"link %q points outside the mod folder and was not collected", relative)
			}

			source = resolved
		case !entry.Type().IsRegular():
			return nil
		}

		details, statErr := os.Stat(source)
		if statErr != nil {
			return appErrors.Wrap(statErr, appErrors.ErrCodeConfigIO, "stat %q", source)
		}

		entries = append(entries, TreeEntry{
			Path:   path.Join(filepath.ToSlash(relative)),
			Source: source,
			Size:   details.Size(),
			Mode:   details.Mode().Perm(),
		})

		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	// WalkDir already walks lexically, so the result is sorted; the sort is here
	// so that the guarantee is stated rather than inferred, because the archive's
	// bytes — and therefore its hash — depend on it.
	slices.SortFunc(entries, func(a, b TreeEntry) int { return strings.Compare(a.Path, b.Path) })

	return entries, nil
}

// escapes reports whether resolved lies outside root. A link that leaves the
// folder is refused; a link that stays inside it is ordinary mod content.
func escapes(root, resolved string) bool {
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return true
	}

	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// PackDirectory zips a mod folder to destPath.
//
// This is how a folder-shaped source becomes installable: the archive extractor
// reads zips, so anything a source hands the cache as a directory has to become
// one before the rest of the pipeline can see it.
func PackDirectory(ctx context.Context, root, destPath string) (*PackResult, error) {
	entries, err := CollectTree(root)
	if err != nil {
		return nil, err
	}

	packed := make([]PackEntry, 0, len(entries))
	for _, entry := range entries {
		packed = append(packed, entry.PackEntry())
	}

	return PackArchive(ctx, destPath, packed)
}
