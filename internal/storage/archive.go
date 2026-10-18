package storage

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// ExtractionLimits bounds what a single archive may write to disk. Mod archives
// come from the network, so a decompression bomb is a real input, not a
// hypothetical one: without a ceiling one crafted entry can fill a user's disk.
type ExtractionLimits struct {
	// MaxEntries caps the number of archive members. A mod needing more is malformed.
	MaxEntries int
	// MaxEntrySize caps one extracted file.
	MaxEntrySize int64
	// MaxTotalSize caps the sum of all extracted files.
	MaxTotalSize int64
}

// DefaultExtractionLimits are generous enough for a real mod pack (Darktide's own
// data files run to hundreds of megabytes) while still bounding a bomb.
func DefaultExtractionLimits() ExtractionLimits {
	return ExtractionLimits{
		MaxEntries:   50_000,
		MaxEntrySize: 4 << 30,  // 4 GiB
		MaxTotalSize: 16 << 30, // 16 GiB
	}
}

// normalized returns a usable copy, replacing every unset or nonsensical field
// with its default: a zero ExtractionLimits must not mean "extract nothing" and a
// negative one must not mean "allow everything".
func (l ExtractionLimits) normalized() ExtractionLimits {
	d := DefaultExtractionLimits()
	if l.MaxEntries <= 0 {
		l.MaxEntries = d.MaxEntries
	}

	if l.MaxEntrySize <= 0 {
		l.MaxEntrySize = d.MaxEntrySize
	}

	if l.MaxTotalSize <= 0 {
		l.MaxTotalSize = d.MaxTotalSize
	}

	return l
}

// ExtractionResult reports what an extraction produced.
type ExtractionResult struct {
	// Files is the written inventory, sorted by relative path.
	Files []domain.ModFile
	// Skipped names entries the extractor deliberately did not write, each
	// followed by the reason in parentheses, so a user whose mod came out short
	// can see why.
	Skipped []string
}

// ArchiveExtractor unpacks a downloaded archive into a mod folder and reports the file
// inventory so the repository can record it. Extraction tolerates junk but not escapes: an
// entry landing outside the destination is skipped and counted, because honouring a stray
// "../" would let a mod write anywhere on the user's disk.
type ArchiveExtractor struct {
	limits ExtractionLimits
}

// NewArchiveExtractor returns an extractor with the default limits.
func NewArchiveExtractor() *ArchiveExtractor {
	return &ArchiveExtractor{limits: DefaultExtractionLimits()}
}

// NewArchiveExtractorWithLimits returns an extractor with caller-chosen limits.
// Unset fields fall back to the defaults.
func NewArchiveExtractorWithLimits(l ExtractionLimits) *ArchiveExtractor {
	return &ArchiveExtractor{limits: l.normalized()}
}

// Limits reports the limits in force.
func (e *ArchiveExtractor) Limits() ExtractionLimits {
	if e == nil {
		return DefaultExtractionLimits()
	}

	return e.limits.normalized()
}

// archiveKind names the container format.
type archiveKind int

const (
	archiveUnknown archiveKind = iota
	archiveZip
	archiveTar
	archiveTarGz
	archiveTarBz2
)

// UnsupportedArchive is the message prefix used when the container format is not
// one the manager unpacks, so callers can recognise the failure without depending
// on a sentinel value.
const UnsupportedArchive = "unsupported archive format"

// Extract unpacks archivePath into destDir, creating it if missing and leaving anything it
// does not overwrite in place. Manager manifests such as mod.toml are written like any other
// file and filtered out later, when the payload is exposed to the game.
func (e *ArchiveExtractor) Extract(
	ctx context.Context,
	archivePath, destDir string,
) (ExtractionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return ExtractionResult{}, canceledError(err)
	}

	archivePath = strings.TrimSpace(archivePath)

	destDir = strings.TrimSpace(destDir)
	if archivePath == "" || destDir == "" {
		return ExtractionResult{}, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"extract needs both an archive path and a destination")
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		return ExtractionResult{}, appErrors.Wrap(
			err,
			appErrors.ErrCodeModNotFound,
			"stat archive %q",
			archivePath,
		)
	}

	if info.IsDir() {
		return ExtractionResult{}, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"archive %q is a directory", archivePath)
	}

	state := newExtractState(destDir, e.Limits())

	kind := detectArchiveKind(archivePath)
	switch kind {
	case archiveZip:
		err = state.zip(ctx, archivePath)
	case archiveTar:
		err = state.tar(ctx, archivePath, nil)
	case archiveTarGz:
		err = state.tar(ctx, archivePath, gzipDecompress)
	case archiveTarBz2:
		err = state.tar(ctx, archivePath, bzip2Decompress)
	default:
		return ExtractionResult{}, appErrors.New(appErrors.ErrCodeInvalidMod,
			"%s: %q (.zip, .tar, .tar.gz and .tar.bz2 are supported)",
			UnsupportedArchive, filepath.Base(archivePath))
	}

	if err != nil {
		return ExtractionResult{}, err
	}

	return state.result(), nil
}

// detectArchiveKind picks the reader for a path. Magic bytes are checked before
// the extension, because mislabelled archives are common and content is the more
// reliable signal; only unknown leading bytes fall back to the name.
func detectArchiveKind(archivePath string) archiveKind {
	switch magicOf(archivePath) {
	case magicZip:
		return archiveZip
	case magicGzip:
		return archiveTarGz
	case magicBzip2:
		return archiveTarBz2
	}

	name := strings.ToLower(filepath.Base(archivePath))
	switch {
	case hasAnySuffix(name, ".zip", ".jar", ".mod", ".pak"):
		return archiveZip
	case hasAnySuffix(name, ".tgz", ".tar.gz", ".taz"):
		return archiveTarGz
	case hasAnySuffix(name, ".tar.bz2", ".tbz2", ".tbz"):
		return archiveTarBz2
	case hasAnySuffix(name, ".tar"):
		return archiveTar
	}

	return archiveUnknown
}

func hasAnySuffix(name string, suffixes ...string) bool {
	for _, s := range suffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}

	return false
}

const (
	magicZip   = "zip"
	magicGzip  = "gzip"
	magicBzip2 = "bzip2"
)

// magicOf sniffs the first bytes of a file, reporting "" when the file cannot be
// read or matches nothing known.
func magicOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	head := make([]byte, 4)

	n, err := io.ReadFull(f, head)
	if err != nil && n < 2 {
		return ""
	}

	head = head[:n]
	switch {
	// "PK\x03\x04" for a normal entry, "PK\x05\x06" for an empty archive,
	// "PK\x07\x08" for a spanned one.
	case len(head) >= 4 && head[0] == 'P' && head[1] == 'K' &&
		(head[2] == 0x03 || head[2] == 0x05 || head[2] == 0x07):
		return magicZip
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return magicGzip
	case len(head) >= 3 && head[0] == 'B' && head[1] == 'Z' && head[2] == 'h':
		return magicBzip2
	}

	return ""
}

// extractState accumulates one extraction run.
type extractState struct {
	byPath  map[string]domain.ModFile
	dest    string
	skipped []string
	limits  ExtractionLimits
	entries int
	total   int64
}

func newExtractState(dest string, limits ExtractionLimits) *extractState {
	return &extractState{dest: dest, limits: limits, byPath: map[string]domain.ModFile{}}
}

func (s *extractState) result() ExtractionResult {
	out := make([]domain.ModFile, 0, len(s.byPath))
	for _, f := range s.byPath {
		out = append(out, f)
	}
	// Sorted so the recorded inventory is stable: reinstalling the same archive
	// must not look like a change.
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })

	files := make([]domain.ModFile, len(out))
	for i := range out {
		files[i] = out[i]
		files[i].ModID = ""
	}

	return ExtractionResult{Files: files, Skipped: s.skipped}
}

// safeRelPath normalises an archive entry name and reports whether it is safe to
// write. Everything that could escape destDir is rejected: absolute paths, drive
// letters, UNC prefixes, and any ".." that climbs above the root.
func safeRelPath(name string) (string, bool) {
	// Archives built on Windows may use backslashes, and treating them as
	// separators means "..\..\evil.dll" is caught rather than becoming one file.
	slashed := strings.ReplaceAll(name, "\\", "/")
	if strings.TrimSpace(slashed) == "" {
		return "", false
	}

	if strings.HasPrefix(slashed, "/") {
		return "", false
	}
	// filepath.VolumeName only recognises drive letters on Windows, so the colon
	// is checked directly to keep "C:/windows/system32/x.dll" out on every host.
	if hasDrivePrefix(slashed) || strings.HasPrefix(slashed, "//") {
		return "", false
	}

	cleaned := path.Clean(slashed)
	if cleaned == "." || cleaned == ".." || cleaned == "/" || strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	return cleaned, true
}

// hasDrivePrefix reports whether name starts with a Windows drive or device
// specifier, e.g. "C:/x" or "C:x".
func hasDrivePrefix(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}

	c := name[0]

	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// resolve maps a safe relative path onto an absolute destination path and
// re-checks containment. The re-check is the actual security boundary: it holds
// even if safeRelPath misses a case, because domain.PathWithin is boundary aware
// and the root itself is never writable.
func (s *extractState) resolve(rel string) (string, error) {
	if rel == "" || path.Clean(strings.ReplaceAll(rel, "\\", "/")) == "." {
		return "", appErrors.New(appErrors.ErrCodeInstallFailed,
			"archive entry %q does not name a file inside the mod folder", rel)
	}

	destPath := filepath.Join(s.dest, filepath.FromSlash(rel))
	if !domain.PathWithin(s.dest, destPath) {
		return "", appErrors.New(appErrors.ErrCodeInstallFailed,
			"archive entry %q escapes the destination", rel)
	}

	return destPath, nil
}

func (s *extractState) skip(name, reason string) {
	s.skipped = append(s.skipped, name+" ("+reason+")")
}

// admit reserves room for one more entry. Entries are counted before they are
// written so an archive with a million zero-byte members cannot run the loop away,
// and the context is re-checked per member so cancelling a large install takes
// effect promptly.
func (s *extractState) admit(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return canceledError(err)
	}

	if s.entries >= s.limits.MaxEntries {
		return appErrors.New(appErrors.ErrCodeInstallFailed,
			"archive has more than %d entries", s.limits.MaxEntries)
	}

	s.entries++

	return nil
}

// writeFile copies one entry out and records its checksum and size. The recorded
// size is what was actually written, never the header's claim: a header claiming
// "10 bytes" can expand to gigabytes of NUL, and the repository would then record
// a size the game could not verify.
func (s *extractState) writeFile(
	ctx context.Context,
	rel string,
	r io.Reader,
	mode os.FileMode,
) error {
	destPath, err := s.resolve(rel)
	if err != nil {
		return err
	}

	if err := platform.MkdirAll(filepath.Dir(destPath)); err != nil {
		return err
	}
	// O_TRUNC follows a symlink already sitting at this path, which would let a
	// hostile archive overwrite whatever the link points at; removing first makes
	// the entry land on a path we just created.
	if err := os.RemoveAll(destPath); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInstallFailed, "replace %q", destPath)
	}

	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInstallFailed, "create %q", destPath)
	}

	defer func() { _ = f.Close() }()

	// One byte past the limit is enough to notice the overflow without letting the
	// bomb through.
	hasher := sha256.New()

	written, err := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(r, s.limits.MaxEntrySize+1))
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInstallFailed, "write %q", destPath)
	}

	if written > s.limits.MaxEntrySize {
		return appErrors.New(appErrors.ErrCodeInstallFailed,
			"entry %q exceeds the %d byte per-file limit", rel, s.limits.MaxEntrySize)
	}

	if s.total+written > s.limits.MaxTotalSize {
		return appErrors.New(appErrors.ErrCodeInstallFailed,
			"archive exceeds the %d byte total limit", s.limits.MaxTotalSize)
	}

	s.total += written
	s.byPath[rel] = domain.ModFile{
		RelPath:    rel,
		StoredPath: destPath,
		SizeBytes:  written,
		Checksum:   hex.EncodeToString(hasher.Sum(nil)),
	}

	return ctx.Err()
}

// trySymlink re-creates a link entry only when its resolved target stays inside the
// destination: a link to /etc/passwd is the archive equivalent of "../", and unlike a plain
// file it survives a later move of the mod folder. A refusal is reported rather than fatal.
func (s *extractState) trySymlink(rel, target string) (ok bool, reason string, err error) {
	destPath, err := s.resolve(rel)
	if err != nil {
		return false, "", err
	}

	if strings.TrimSpace(target) == "" {
		return false, "link has no target", nil
	}
	// Absolute targets are rejected outright; a relative one resolves against the
	// directory the link itself sits in, which is how tar semantics work.
	if path.IsAbs(strings.ReplaceAll(target, "\\", "/")) {
		return false, "link points outside the archive", nil
	}

	linkedTo := filepath.Join(filepath.Dir(destPath), filepath.FromSlash(target))
	if !domain.PathWithin(s.dest, linkedTo) {
		return false, "link resolves outside the mod folder", nil
	}

	if err := platform.MkdirAll(filepath.Dir(destPath)); err != nil {
		return false, "", err
	}
	// A leftover link or file at the same name blocks os.Symlink.
	if err := os.RemoveAll(destPath); err != nil {
		return false, "", appErrors.Wrap(
			err,
			appErrors.ErrCodeInstallFailed,
			"replace %q",
			destPath,
		)
	}

	if err := os.Symlink(target, destPath); err != nil {
		return false, "", appErrors.Wrap(err, appErrors.ErrCodeInstallFailed, "link %q", destPath)
	}

	s.byPath[rel] = domain.ModFile{RelPath: rel, StoredPath: destPath}

	return true, "", nil
}

// dirEntry records a directory so the inventory reflects empty folders the mod
// declared. Directories contribute no checksum and count towards no byte budget.
func (s *extractState) dirEntry(rel string) error {
	destPath, err := s.resolve(rel)
	if err != nil {
		return err
	}

	if err := platform.MkdirAll(destPath); err != nil {
		return err
	}

	s.byPath[rel] = domain.ModFile{RelPath: rel, StoredPath: destPath}

	return nil
}

// zip unpacks a zip container.
func (s *extractState) zip(ctx context.Context, archivePath string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "open zip archive %q", archivePath)
	}
	defer func() { _ = r.Close() }()

	for _, entry := range r.File {
		if err := s.admit(ctx, entry.Name); err != nil {
			return err
		}

		rel, ok := safeRelPath(entry.Name)
		if !ok {
			s.skip(entry.Name, "path escapes the mod folder")
			continue
		}

		if entry.FileInfo().IsDir() {
			if err := s.dirEntry(rel); err != nil {
				return err
			}

			continue
		}
		// Zip's own unix mode is often absent, but when present it is the best
		// available signal and costs nothing to honour.
		mode := os.FileMode(0o644)
		if entry.Mode().Perm()&0o100 != 0 {
			mode = 0o755
		}

		rc, err := entry.Open()
		if err != nil {
			// A single unreadable member is skipped; the rest of the mod is still
			// worth installing and the user is told what is missing.
			s.skip(entry.Name, "entry could not be read")
			continue
		}

		err = s.writeFile(ctx, rel, rc, mode)
		_ = rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}

// gzipDecompress and bzip2Decompress adapt the two standard-library readers to
// the one decompress signature state.tar uses; a corrupt stream fails here, before
// anything is written.
func gzipDecompress(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }

func bzip2Decompress(r io.Reader) (io.Reader, error) { return bzip2.NewReader(r), nil }

// tar unpacks a (possibly compressed) tar container. decompress is nil for plain
// tar; otherwise it wraps the raw stream, e.g. gzipDecompress.
func (s *extractState) tar(
	ctx context.Context,
	archivePath string,
	decompress func(io.Reader) (io.Reader, error),
) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeModNotFound, "open tar archive %q", archivePath)
	}
	defer func() { _ = f.Close() }()

	var src io.Reader = f
	if decompress != nil {
		decompressed, err := decompress(f)
		if err != nil {
			return appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "decompress %q", archivePath)
		}

		if closer, ok := decompressed.(io.Closer); ok {
			defer func() { _ = closer.Close() }()
		}

		src = decompressed
	}

	reader := tar.NewReader(src)

	for {
		if err := ctx.Err(); err != nil {
			return canceledError(err)
		}

		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}

		if err != nil {
			// A truncated archive leaves a half-written mod folder. Failing is the
			// only safe answer: a partial mod looks complete to the game.
			return appErrors.Wrap(
				err,
				appErrors.ErrCodeInstallFailed,
				"read tar archive %q",
				archivePath,
			)
		}

		if err := s.admit(ctx, header.Name); err != nil {
			return err
		}

		rel, ok := safeRelPath(header.Name)
		if !ok {
			s.skip(header.Name, "path escapes the mod folder")
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := s.dirEntry(rel); err != nil {
				return err
			}
		case tar.TypeSymlink:
			ok, reason, err := s.trySymlink(rel, header.Linkname)
			if err != nil {
				return err
			}

			if !ok {
				s.skip(header.Name, reason)
			}
		case tar.TypeLink:
			// Hard links duplicate an entry that is already in the archive;
			// reproducing them needs link-order bookkeeping for no benefit.
			s.skip(header.Name, "hard links are not extracted")
		case tar.TypeReg:
			mode := os.FileMode(0o644)
			if header.Mode&0o100 != 0 {
				mode = 0o755
			}

			if err := s.writeFile(ctx, rel, reader, mode); err != nil {
				return err
			}
		default:
			// Devices, FIFOs and sockets have no business in a mod archive.
			s.skip(header.Name, "unsupported entry type")
		}
	}
}
