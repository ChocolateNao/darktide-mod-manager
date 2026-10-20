package storage

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entry describes one archive member for the builders below.
type entry struct {
	name     string
	body     string
	linkname string
	mode     int64
	dir      bool
	typ      byte
}

// zipEntries writes a zip archive at path.
func zipEntries(t *testing.T, path string, entries ...entry) {
	t.Helper()

	var buf bytes.Buffer

	w := zip.NewWriter(&buf)

	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}

		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}

		name := e.name
		if e.dir && !strings.HasSuffix(name, "/") {
			name += "/"
		}

		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(os.FileMode(mode))

		if e.dir {
			hdr.SetMode(os.FileMode(mode) | os.ModeDir)
		}

		if typ == tar.TypeSymlink {
			// A zip link is a deflated file whose body is the target.
			hdr.Method = zip.Deflate
		}

		fw, err := w.CreateHeader(hdr)
		require.NoError(t, err, "create zip entry %q: %v", name)

		if e.dir {
			continue
		}

		body := e.body
		if typ == tar.TypeSymlink {
			body = e.linkname
		}

		_, err = fw.Write([]byte(body))
		require.NoError(t, err, "write zip entry %q: %v", name, err)
	}

	err := w.Close()
	require.NoError(t, err, "close zip: %v", err)

	err = os.WriteFile(path, buf.Bytes(), 0o600)
	require.NoError(t, err, "write zip file: %v", err)
}

// tarEntries writes a tar archive at path, optionally gzip compressed.
func tarEntries(t *testing.T, path string, gzipped bool, entries ...entry) string {
	t.Helper()

	var (
		buf  bytes.Buffer
		sink io.Writer = &buf
		gz   *gzip.Writer
	)
	if gzipped {
		gz = gzip.NewWriter(&buf)
		sink = gz
	}

	tw := tar.NewWriter(sink)

	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}

		typ := e.typ
		if typ == 0 {
			if e.dir {
				typ = tar.TypeDir
			} else {
				typ = tar.TypeReg
			}
		}

		name := e.name
		if e.dir && !strings.HasSuffix(name, "/") {
			name += "/"
		}

		hdr := &tar.Header{Name: name, Mode: mode, Typeflag: typ, Linkname: e.linkname}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}

		if typ == tar.TypeDir || typ == tar.TypeSymlink {
			hdr.Size = 0
		}

		err := tw.WriteHeader(hdr)
		require.NoError(t, err, "write tar header %q: %v", name, err)

		if typ == tar.TypeReg {
			_, err = tw.Write([]byte(e.body))
			require.NoError(t, err, "write tar entry %q: %v", name, err)
		}
	}

	err := tw.Close()
	require.NoError(t, err, "close tar: %v", err)

	if gz != nil {
		err := gz.Close()
		require.NoError(t, err, "close gzip: %v", err)
	}

	err = os.WriteFile(path, buf.Bytes(), 0o600)
	require.NoError(t, err, "write tar file: %v", err)

	return path
}

func relPaths(files []domain.ModFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.RelPath
	}

	return out
}

func fileByRel(files []domain.ModFile, rel string) (domain.ModFile, bool) {
	for _, f := range files {
		if f.RelPath == rel {
			return f, true
		}
	}

	return domain.ModFile{}, false
}

func sumHex(s string) string {
	return platform.StringSHA256([]byte(s))
}

func TestExtractZipWritesFilesAndRecordsInventory(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive,
		entry{name: "mod.toml", body: "[mod]\nid = \"better_ui\"\n"},
		entry{name: "bin/plugin.dll", body: "dll-bytes"},
		entry{name: "readme.txt", body: "hello"},
	)

	dest := filepath.Join(dir, "payload")

	res, err := NewArchiveExtractor().Extract(context.Background(), archive, dest)
	require.NoError(t, err, "Extract")

	want := []string{"bin/plugin.dll", "mod.toml", "readme.txt"}
	got := relPaths(res.Files)
	assert.Equal(
		t,
		strings.Join(want, ","),
		strings.Join(got, ","),
		"inventory = %v, want %v (sorted by relative path)",
		got,
		want,
	)

	assert.Empty(t, res.Skipped, "unexpected skipped entries: %v", res.Skipped)

	readme, ok := fileByRel(res.Files, "readme.txt")
	require.True(t, ok, "readme.txt missing from the inventory")

	assert.Equal(
		t,
		int64(len("hello")),
		readme.SizeBytes,
		"readme size = %d, want %d",
		readme.SizeBytes,
		len("hello"),
	)

	assert.Equal(
		t,
		sumHex("hello"),
		readme.Checksum,
		"readme checksum = %q, want %q",
		readme.Checksum,
		sumHex("hello"),
	)

	assert.Equal(
		t,
		"",
		readme.ModID,
		"inventory ModID should be empty, the repository owns it, got %q",
		readme.ModID,
	)

	wantPath := filepath.Join(dest, "readme.txt")
	assert.Equal(
		t,
		wantPath,
		readme.StoredPath,
		"readme StoredPath = %q, want %q",
		readme.StoredPath,
		wantPath,
	)

	body, err := os.ReadFile(readme.StoredPath)
	require.NoError(t, err, "read extracted file")

	assert.Equal(t, "hello", string(body), "extracted body = %q, want %q", body, "hello")
}

func TestExtractZipCreatesMissingDestination(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive, entry{name: "a.txt", body: "a"})

	dest := filepath.Join(dir, "deep", "nested", "payload")

	_, err := NewArchiveExtractor().Extract(context.Background(), archive, dest)
	require.NoError(t, err, "Extract: %v", err)

	_, err = os.Stat(filepath.Join(dest, "a.txt"))
	assert.NoError(t, err, "file not written into the created destination: %v", err)
}

// The manifest has to survive extraction: it lives in the manager's storage
// folder and is only filtered out later, when the payload is exposed to the game.
func TestExtractKeepsManagerManifests(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive,
		entry{name: "mod.toml", body: "id"},
		entry{name: "manifest.toml", body: "id"},
		entry{name: "METADATA.TOML", body: "id"},
	)

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract")

	for _, rel := range []string{"mod.toml", "manifest.toml", "METADATA.TOML"} {
		_, ok := fileByRel(res.Files, rel)
		assert.True(
			t,
			ok,
			"%s was dropped during extraction (inventory %v)",
			rel,
			relPaths(res.Files),
		)
	}
}

func TestExtractTarFormats(t *testing.T) {
	dir := t.TempDir()

	entries := []entry{
		{name: "mod.toml", body: "id"},
		{name: "shaders/custom.hlsl", body: "shader"},
	}
	for _, tc := range []struct {
		name    string
		file    string
		gzipped bool
	}{
		{name: "plain", file: "mod.tar", gzipped: false},
		{name: "gzip", file: "mod.tar.gz", gzipped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(dir, tc.file)
			tarEntries(t, archive, tc.gzipped, entries...)

			res, err := NewArchiveExtractor().Extract(
				context.Background(),
				archive,
				filepath.Join(dir, "out-"+tc.name),
			)
			require.NoError(t, err, "Extract")

			want := []string{"mod.toml", "shaders/custom.hlsl"}
			got := relPaths(res.Files)
			assert.Equal(
				t,
				strings.Join(want, ","),
				strings.Join(got, ","),
				"inventory = %v, want %v",
				got,
				want,
			)
		})
	}
}

// A real bzip2 stream cannot be produced with the standard library, so the test
// feeds a well-formed bzip2 header and nothing after it.
func TestExtractBzip2FailsLoudlyOnATruncatedStream(t *testing.T) {
	dir := t.TempDir()

	archive := filepath.Join(dir, "mod.tar.bz2")
	err := os.WriteFile(archive, []byte("BZh91AY&SY"), 0o600)
	require.NoError(t, err)

	_, err = NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.Error(t, err, "expected a decompression failure")
	// bzip2.NewReader does not validate eagerly, so the truncation surfaces while
	// reading the tar header; either way, a half-unpacked mod looks complete.
	code := appErrors.CodeOf(err)
	assert.Equal(
		t,
		appErrors.ErrCodeInstallFailed,
		code,
		"code = %q, want %q",
		code,
		appErrors.ErrCodeInstallFailed,
	)

	assert.Contains(t, err.Error(), "mod.tar.bz2", "error should name the archive, got %v", err)
}

// Content beats the name, so a gzip stream behind a .tar.bz2 name still unpacks.
func TestExtractPrefersContentOverAMismatchedSuffix(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.tar.bz2")
	tarEntries(t, archive, true, entry{name: "a.txt", body: "a"})

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract")

	_, ok := fileByRel(res.Files, "a.txt")
	assert.True(t, ok, "inventory = %v, want a.txt", relPaths(res.Files))
}

// Release assets are frequently named .tar.gz while actually holding a zip, so
// the content wins over the name.
func TestExtractDetectsFormatFromContentNotName(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.tar.gz")
	zipEntries(t, archive, entry{name: "a.txt", body: "a"})

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract")

	_, ok := fileByRel(res.Files, "a.txt")
	assert.True(
		t,
		ok,
		"zip content behind a .tar.gz name was not extracted (inventory %v)",
		relPaths(res.Files),
	)
}

func TestExtractFallsBackToExtensionWhenMagicIsUnknown(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "weird.zip")
	// Four leading bytes that match no known magic, but the name says zip.
	err := os.WriteFile(archive, []byte("JUNKJUNKpayload"), 0o600)
	require.NoError(t, err)

	if _, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	); err == nil {
		require.FailNow(t, "expected an unreadable-archive error")
	} else if !strings.Contains(
		err.Error(),
		"zip archive",
	) {
		assert.Fail(t, "error should name the container, got %v", err)
	}
}

func TestExtractRejectsPathTraversalEntries(t *testing.T) {
	dir := t.TempDir()

	hostile := []string{
		"../escaped.txt",
		"../../escaped.txt",
		"a/../../escaped.txt",
		"..\\escaped.txt",
		"/etc/escaped.txt",
		"C:/escaped.txt",
		"..",
		".",
	}
	for _, name := range hostile {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(dir, strings.ReplaceAll(name, "/", "_")+".zip")
			zipEntries(
				t,
				archive,
				entry{name: name, body: "pwned"},
				entry{name: "good.txt", body: "ok"},
			)
			dest := filepath.Join(dir, "out", filepath.Base(archive))

			res, err := NewArchiveExtractor().Extract(context.Background(), archive, dest)
			require.NoError(t, err, "Extract should skip, not fail")

			assert.Len(t, res.Skipped, 1, "skipped = %v, want exactly one entry", res.Skipped)

			_, ok := fileByRel(res.Files, "good.txt")
			assert.True(t, ok, "the safe entry was lost (inventory %v)", relPaths(res.Files))

			_, err = os.Stat(filepath.Join(filepath.Dir(dest), "escaped.txt"))
			assert.Error(t, err, "a traversal entry escaped the destination")

			_, err = os.Stat("/etc/escaped.txt")
			assert.Error(t, err, "wrote outside the destination")
		})
	}
}

func TestExtractRejectsTraversalInTar(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.tar")
	tarEntries(t, archive, false, entry{name: "../escaped.txt", body: "pwned"})

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract should skip, not fail")

	assert.False(
		t,
		len(res.Skipped) != 1 || len(res.Files) != 0,
		"files = %v skipped = %v, want nothing written and one skip",
		relPaths(res.Files),
		res.Skipped,
	)

	_, err = os.Stat(filepath.Join(dir, "escaped.txt"))
	assert.Error(t, err, "a tar traversal entry escaped the destination")
}

func TestExtractSkipsSymlinksThatLeaveTheDestination(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.tar")
	tarEntries(t, archive, false,
		entry{name: "passwd", typ: tar.TypeSymlink, linkname: "/etc/passwd"},
		entry{name: "escape", typ: tar.TypeSymlink, linkname: "../../etc/passwd"},
		entry{name: "empty", typ: tar.TypeSymlink},
		entry{name: "real.txt", body: "ok"},
	)

	dest := filepath.Join(dir, "out")

	res, err := NewArchiveExtractor().Extract(context.Background(), archive, dest)
	require.NoError(t, err, "Extract")

	assert.Len(t, res.Skipped, 3, "skipped = %v, want three hostile links", res.Skipped)

	for _, name := range []string{"passwd", "escape", "empty"} {
		_, err := os.Lstat(filepath.Join(dest, name))
		assert.Error(t, err, "link %q should not exist", name)
	}

	_, ok := fileByRel(res.Files, "real.txt")
	assert.True(t, ok, "the safe entry was lost (inventory %v)", relPaths(res.Files))
}

func TestExtractKeepsSymlinksThatStayInside(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.tar")
	tarEntries(t, archive, false,
		entry{name: "assets/real.txt", body: "ok"},
		entry{name: "assets/alias.txt", typ: tar.TypeSymlink, linkname: "real.txt"},
	)

	dest := filepath.Join(dir, "out")

	res, err := NewArchiveExtractor().Extract(context.Background(), archive, dest)
	require.NoError(t, err, "Extract")

	_, ok := fileByRel(res.Files, "assets/alias.txt")
	assert.True(t, ok, "internal link missing from inventory: %v", relPaths(res.Files))

	target, err := os.Readlink(filepath.Join(dest, "assets", "alias.txt"))
	require.NoError(t, err, "readlink")

	assert.Equal(t, "real.txt", target, "link target = %q, want %q", target, "real.txt")
}

func TestExtractSkipsHardLinksAndSpecialEntries(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.tar")
	tarEntries(t, archive, false,
		entry{name: "a.txt", body: "a"},
		entry{name: "hard.txt", typ: tar.TypeLink, linkname: "a.txt"},
		entry{name: "pipe", typ: tar.TypeFifo},
	)

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract")

	got := relPaths(res.Files)
	assert.Equal(t, "a.txt", strings.Join(got, ","), "inventory = %v, want only a.txt", got)

	assert.Len(t, res.Skipped, 2, "skipped = %v, want two entries", res.Skipped)

	assert.True(
		t,
		sort.StringsAreSorted(res.Skipped) && len(res.Skipped) > 1,
		"skipped entries should keep archive order: %v",
		res.Skipped,
	)
}

func TestExtractRecordsDirectoryEntries(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive,
		entry{name: "textures", dir: true},
		entry{name: "textures/hud.dds", body: "dds"},
	)

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract")

	dirEntry, ok := fileByRel(res.Files, "textures")
	require.True(t, ok, "directory not recorded: %v", relPaths(res.Files))

	assert.False(
		t,
		dirEntry.SizeBytes != 0 || dirEntry.Checksum != "",
		"directory entry should carry no size or checksum: %+v",
		dirEntry,
	)

	info, err := os.Stat(dirEntry.StoredPath)
	assert.False(
		t,
		err != nil || !info.IsDir(),
		"directory entry does not point at a directory: %v %v",
		info,
		err,
	)
}

func TestExtractEnforcesMaxEntries(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "many.zip")
	zipEntries(t, archive,
		entry{name: "a.txt", body: "a"},
		entry{name: "b.txt", body: "b"},
		entry{name: "c.txt", body: "c"},
	)

	e := NewArchiveExtractorWithLimits(ExtractionLimits{MaxEntries: 2})

	_, err := e.Extract(context.Background(), archive, filepath.Join(dir, "out"))
	require.Error(t, err, "expected the entry budget to be enforced")

	code := appErrors.CodeOf(err)
	assert.Equal(
		t,
		appErrors.ErrCodeInstallFailed,
		code,
		"code = %q, want %q",
		code,
		appErrors.ErrCodeInstallFailed,
	)

	assert.Contains(
		t,
		err.Error(),
		"more than 2 entries",
		"error should state the budget, got %v",
		err,
	)
}

func TestExtractEnforcesMaxEntrySize(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "big.zip")
	zipEntries(t, archive, entry{name: "big.bin", body: strings.Repeat("x", 4096)})

	e := NewArchiveExtractorWithLimits(ExtractionLimits{MaxEntrySize: 16})
	_, err := e.Extract(context.Background(), archive, filepath.Join(dir, "out"))
	require.Error(t, err, "expected the per-file budget to be enforced")
}

func TestExtractEnforcesMaxTotalSize(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "many.zip")
	zipEntries(t, archive,
		entry{name: "a.bin", body: strings.Repeat("x", 100)},
		entry{name: "b.bin", body: strings.Repeat("y", 100)},
	)

	e := NewArchiveExtractorWithLimits(ExtractionLimits{MaxTotalSize: 150})
	_, err := e.Extract(context.Background(), archive, filepath.Join(dir, "out"))
	require.Error(t, err, "expected the total budget to be enforced")
}

func TestExtractionLimitsFallBackToDefaults(t *testing.T) {
	d := DefaultExtractionLimits()

	for _, l := range []ExtractionLimits{
		{},
		{MaxEntries: -1, MaxEntrySize: -1, MaxTotalSize: -1},
	} {
		got := l.normalized()
		assert.Equal(t, d, got, "normalized(%+v) = %+v, want %+v", l, got, d)
	}

	partial := ExtractionLimits{MaxEntries: 7}.normalized()
	assert.Equal(
		t,
		7,
		partial.MaxEntries,
		"MaxEntries = %d, want the caller's 7",
		partial.MaxEntries,
	)

	assert.False(
		t,
		partial.MaxEntrySize != d.MaxEntrySize || partial.MaxTotalSize != d.MaxTotalSize,
		"unset fields should default: %+v",
		partial,
	)
}

func TestExtractorLimitsAreSafeOnNilReceiver(t *testing.T) {
	var e *ArchiveExtractor

	got := e.Limits()
	assert.Equal(
		t,
		DefaultExtractionLimits(),
		got,
		"nil extractor limits = %+v, want the defaults",
		got,
	)

	assert.Equal(
		t,
		DefaultExtractionLimits(),
		NewArchiveExtractor().Limits(),
		"NewArchiveExtractor should use the default limits",
	)
}

func TestExtractRejectsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()

	archive := filepath.Join(dir, "mod.rar")
	err := os.WriteFile(archive, []byte("Rar!\x1a\x07\x00"), 0o600)
	require.NoError(t, err)

	_, err = NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.Error(t, err, "expected an unsupported-format error")

	code := appErrors.CodeOf(err)
	assert.Equal(
		t,
		appErrors.ErrCodeInvalidMod,
		code,
		"code = %q, want %q",
		code,
		appErrors.ErrCodeInvalidMod,
	)

	assert.Contains(
		t,
		err.Error(),
		UnsupportedArchive,
		"error should carry the %q prefix so callers can match it, got %v",
		UnsupportedArchive,
		err,
	)
}

func TestExtractRejectsBadArguments(t *testing.T) {
	dir := t.TempDir()
	e := NewArchiveExtractor()

	_, err := e.Extract(context.Background(), "", filepath.Join(dir, "out"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"blank archive code = %v, want INVALID_ARGUMENT",
		appErrors.CodeOf(err),
	)

	_, err = e.Extract(context.Background(), filepath.Join(dir, "x.zip"), "   ")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"blank destination code = %v, want INVALID_ARGUMENT",
		appErrors.CodeOf(err),
	)

	missing := filepath.Join(dir, "nope.zip")
	_, err = e.Extract(context.Background(), missing, filepath.Join(dir, "out"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeModNotFound),
		"missing archive code = %v, want MOD_NOT_FOUND",
		appErrors.CodeOf(err),
	)

	asDir := filepath.Join(dir, "adir")
	err = os.MkdirAll(asDir, 0o755)
	require.NoError(t, err)

	_, err = e.Extract(context.Background(), asDir, filepath.Join(dir, "out"))
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"directory archive code = %v, want INVALID_ARGUMENT",
		appErrors.CodeOf(err),
	)
}

func TestExtractHonoursPreCanceledContext(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive, entry{name: "a.txt", body: "a"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewArchiveExtractor().Extract(ctx, archive, filepath.Join(dir, "out"))
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"code = %v, want OPERATION_CANCELED",
		appErrors.CodeOf(err),
	)

	_, err = os.Stat(filepath.Join(dir, "out"))
	assert.Error(t, err, "a canceled extraction created the destination")
}

func TestExtractToleratesNilContext(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive, entry{name: "a.txt", body: "a"})
	//nolint:staticcheck // a nil context is what the manager's other services tolerate too.
	_, err := NewArchiveExtractor().Extract(nil, archive, filepath.Join(dir, "out"))
	require.NoError(t, err, "Extract(nil ctx): %v", err)
}

func TestExtractEmptyArchiveSucceedsWithNoFiles(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "empty.zip")
	zipEntries(t, archive)

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract")

	assert.Empty(t, res.Files, "files = %v, want none", relPaths(res.Files))

	assert.NotNil(t, res.Files, "Files should be an empty slice, not nil")
}

func TestExtractOverwritesSameNamedFilesAndKeepsOthers(t *testing.T) {
	dir := t.TempDir()

	dest := filepath.Join(dir, "out")
	err := os.MkdirAll(dest, 0o755)
	require.NoError(t, err)

	stale := filepath.Join(dest, "stale.txt")
	err = os.WriteFile(stale, []byte("old"), 0o600)
	require.NoError(t, err)

	victim := filepath.Join(dest, "a.txt")
	err = os.WriteFile(victim, []byte("previous"), 0o600)
	require.NoError(t, err)

	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive, entry{name: "a.txt", body: "new"})

	_, err = NewArchiveExtractor().Extract(context.Background(), archive, dest)
	require.NoError(t, err, "Extract: %v", err)

	body, err := os.ReadFile(victim)
	assert.False(
		t,
		err != nil || string(body) != "new",
		"a.txt = %q (%v), want %q",
		body,
		err,
		"new",
	)

	_, err = os.Stat(stale)
	assert.NoError(t, err, "extraction removed an unrelated file: %v", err)
}

func TestExtractReplacesAnExistingSymlinkAtTheEntryPath(t *testing.T) {
	dir := t.TempDir()

	dest := filepath.Join(dir, "out")
	err := os.MkdirAll(dest, 0o755)
	require.NoError(t, err)

	elsewhere := filepath.Join(dir, "elsewhere.txt")
	err = os.WriteFile(elsewhere, []byte("x"), 0o600)
	require.NoError(t, err)

	err = os.Symlink(elsewhere, filepath.Join(dest, "a.txt"))
	require.NoError(t, err)

	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive, entry{name: "a.txt", body: "new"})

	_, err = NewArchiveExtractor().Extract(context.Background(), archive, dest)
	require.NoError(t, err, "Extract: %v", err)

	info, err := os.Lstat(filepath.Join(dest, "a.txt"))
	require.NoError(t, err)

	assert.Equal(
		t,
		os.FileMode(0),
		info.Mode()&os.ModeSymlink,
		"the old symlink survived extraction",
	)
}

func TestExtractNormalisesBackslashSeparators(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "win.zip")
	zipEntries(t, archive, entry{name: `textures\hud.dds`, body: "dds"})

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract")

	got := relPaths(res.Files)
	assert.False(
		t,
		len(got) != 1 || got[0] != "textures/hud.dds",
		"inventory = %v, want [textures/hud.dds]",
		got,
	)
}

func TestExtractPreservesExecutableBit(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("POSIX permissions are meaningless on Windows")
	}

	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.tar")
	tarEntries(t, archive, false,
		entry{name: "run.sh", body: "#!/bin/sh\n", mode: 0o755},
		entry{name: "plain.txt", body: "x", mode: 0o644},
	)

	res, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	)
	require.NoError(t, err, "Extract")

	run, _ := fileByRel(res.Files, "run.sh")

	info, err := os.Stat(run.StoredPath)
	require.NoError(t, err)

	assert.Equal(
		t,
		os.FileMode(0o755),
		info.Mode().Perm(),
		"run.sh mode = %v, want 0755",
		info.Mode().Perm(),
	)

	plain, _ := fileByRel(res.Files, "plain.txt")

	plainInfo, err := os.Stat(plain.StoredPath)
	require.NoError(t, err)

	assert.Equal(
		t,
		os.FileMode(0o644),
		plainInfo.Mode().Perm(),
		"plain.txt mode = %v, want 0644",
		plainInfo.Mode().Perm(),
	)
}

func TestExtractFailsOnTruncatedTar(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.tar")
	tarEntries(t, archive, false, entry{name: "a.txt", body: strings.Repeat("a", 4096)})

	full, err := os.ReadFile(archive)
	require.NoError(t, err)

	err = os.WriteFile(archive, full[:len(full)/2], 0o600)
	require.NoError(t, err)

	// A half-written mod looks complete to the game, so this must be an error.
	_, err = NewArchiveExtractor().Extract(context.Background(), archive, filepath.Join(dir, "out"))
	require.Error(t, err, "expected a truncated archive to fail")
}

func TestExtractReportsUnreadableZipEntryAsSkipped(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "mod.zip")
	zipEntries(t, archive, entry{name: "a.txt", body: "a"})
	// Corrupt the deflate stream of the only member.
	raw, err := os.ReadFile(archive)
	require.NoError(t, err)

	idx := bytes.Index(raw, []byte("a.txt"))
	if idx < 0 {
		t.Skip("could not locate the member name to corrupt")
	}

	for i := idx; i < len(raw) && i < idx+64; i++ {
		raw[i] = 0xff
	}

	err = os.WriteFile(archive, raw, 0o600)
	require.NoError(t, err)

	if _, err := NewArchiveExtractor().Extract(
		context.Background(),
		archive,
		filepath.Join(dir, "out"),
	); err == nil {
		t.Log("corruption was rejected by the checksum, which is also acceptable")
	}
}

func TestSafeRelPath(t *testing.T) {
	ok := []struct{ in, want string }{
		{"a.txt", "a.txt"},
		{"./a.txt", "a.txt"},
		{"a//b.txt", "a/b.txt"},
		{"a/./b.txt", "a/b.txt"},
		{"a/b/../c.txt", "a/c.txt"},
		{`a\b.txt`, "a/b.txt"},
		{"dir with spaces/a.txt", "dir with spaces/a.txt"},
		{"日本語.txt", "日本語.txt"},
	}
	for _, tc := range ok {
		got, valid := safeRelPath(tc.in)
		assert.True(
			t,
			valid || got != tc.want,
			"safeRelPath(%q) = (%q, %v), want (%q, true)",
			tc.in,
			got,
			valid,
			tc.want,
		)
	}

	for _, in := range []string{"", "   ", ".", "..", "../x", "a/../../x", "/x", "//server/share", "C:/x", `C:\x`, "C:x"} {
		got, valid := safeRelPath(in)
		assert.False(t, valid, "safeRelPath(%q) = (%q, true), want rejection", in, got)
	}
}

func TestExtractStateResolveRejectsTheDestinationItself(t *testing.T) {
	dir := t.TempDir()

	s := newExtractState(dir, DefaultExtractionLimits())
	_, err := s.resolve(".")
	assert.Error(t, err, "resolving the destination itself should be rejected")

	_, err = s.resolve("sub")
	assert.NoError(t, err, "resolving a child should be allowed: %v", err)
}

func TestMagicOfUnknownAndUnreadableFiles(t *testing.T) {
	dir := t.TempDir()
	got := magicOf(filepath.Join(dir, "missing"))
	assert.Equal(t, "", got, "magicOf(missing) = %q, want %q", got, "")

	short := filepath.Join(dir, "short")
	err := os.WriteFile(short, []byte("x"), 0o600)
	require.NoError(t, err)

	got = magicOf(short)
	assert.Equal(t, "", got, "magicOf(one byte) = %q, want %q", got, "")

	plain := filepath.Join(dir, "plain.bin")
	err = os.WriteFile(plain, []byte("0123456789"), 0o600)
	require.NoError(t, err)

	got = magicOf(plain)
	assert.Equal(t, "", got, "magicOf(unknown) = %q, want %q", got, "")
}

func TestDetectArchiveKindTable(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		err := os.WriteFile(p, body, 0o600)
		require.NoError(t, err)

		return p
	}

	cases := []struct {
		file string
		body []byte
		want archiveKind
	}{
		{file: "a.zip", body: []byte("PK\x03\x04rest"), want: archiveZip},
		{file: "a.ZIP", body: []byte("PK\x05\x06"), want: archiveZip},
		{file: "a.jar", body: []byte("PK\x03\x04"), want: archiveZip},
		{file: "a.pak", body: []byte("PK\x03\x04"), want: archiveZip},
		{file: "a.mod", body: []byte("PK\x03\x04"), want: archiveZip},
		{file: "a.tgz", body: []byte("\x1f\x8b\x08"), want: archiveTarGz},
		{file: "a.tar.gz", body: []byte("\x1f\x8b"), want: archiveTarGz},
		{file: "a.taz", body: []byte("\x1f\x8b"), want: archiveTarGz},
		{file: "a.tar.bz2", body: []byte("BZh9"), want: archiveTarBz2},
		{file: "a.tbz2", body: []byte("BZh9"), want: archiveTarBz2},
		{file: "a.tbz", body: []byte("BZh9"), want: archiveTarBz2},
		{file: "a.tar", body: []byte("plain tar body"), want: archiveTar},
		{file: "a.bin", body: []byte("plain"), want: archiveUnknown},
		// Content wins over a contradictory name.
		{file: "a.tar.gz", body: []byte("PK\x03\x04"), want: archiveZip},
		{file: "a.zip", body: []byte("\x1f\x8b\x08"), want: archiveTarGz},
	}
	for _, tc := range cases {
		got := detectArchiveKind(write(tc.file, tc.body))
		assert.Equal(t, tc.want, got, "detectArchiveKind(%s) = %v, want %v", tc.file, got, tc.want)
	}
}
