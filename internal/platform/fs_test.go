package platform

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memFile is one entry in a fake filesystem tree.
type memFile struct {
	modTime time.Time
	mode    fs.FileMode
}

// fakeFS is an in-memory FS for tests that need existence checks without a disk.
type fakeFS struct {
	entries map[string]memFile
}

func newFakeFS() *fakeFS { return &fakeFS{entries: map[string]memFile{}} }

func (f *fakeFS) addDir(path string) {
	f.entries[filepath.Clean(path)] = memFile{mode: fs.ModeDir | 0o755}
}

func (f *fakeFS) addFile(path string) {
	f.entries[filepath.Clean(path)] = memFile{mode: 0o644, modTime: time.Now()}
}

func (f *fakeFS) addSymlink(path string) {
	f.entries[filepath.Clean(path)] = memFile{mode: fs.ModeSymlink | 0o777}
}

func (f *fakeFS) Stat(path string) (fs.FileInfo, error) {
	entry, ok := f.entries[filepath.Clean(path)]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}

	if entry.mode&fs.ModeSymlink != 0 {
		// A symlink resolves to whatever the target points at; tests model a
		// resolvable link by also registering the target path.
		for candidate, e := range f.entries {
			if candidate != filepath.Clean(path) && !e.mode.IsDir() {
				return &memInfo{
					name:    filepath.Base(candidate),
					mode:    e.mode,
					modTime: e.modTime,
				}, nil
			}
		}
	}

	return &memInfo{name: filepath.Base(path), mode: entry.mode, modTime: entry.modTime}, nil
}

func (f *fakeFS) Lstat(path string) (fs.FileInfo, error) { return f.Stat(path) }

func (f *fakeFS) ReadDir(string) ([]fs.DirEntry, error) {
	return nil, &fs.PathError{Op: "readdir", Path: "", Err: fs.ErrNotExist}
}

type memInfo struct {
	modTime time.Time
	name    string
	mode    fs.FileMode
}

func (i *memInfo) Name() string       { return i.name }
func (i *memInfo) Size() int64        { return 0 }
func (i *memInfo) Mode() fs.FileMode  { return i.mode }
func (i *memInfo) ModTime() time.Time { return i.modTime }
func (i *memInfo) IsDir() bool        { return i.mode.IsDir() }
func (i *memInfo) Sys() any           { return nil }

func TestPredicatesWithNilFilesystem(t *testing.T) {
	// Every predicate must tolerate a nil FS and an empty path instead of
	// panicking, since the service layer passes user input straight through.
	assert.False(
		t,
		Exists(nil, "") || ExistsNoFollow(nil, "") || IsDir(nil, "") || IsFile(nil, ""),
		"predicates reported true for an empty path",
	)

	missing := filepath.Join(t.TempDir(), "definitely-missing")
	_, err := os.Stat(missing)
	require.Error(t, err, "fixture already exists: %s", missing)

	assert.False(t, Exists(nil, missing), "Exists(%q) = true, want false", missing)
}

func TestPredicatesWithFakeFilesystem(t *testing.T) {
	f := newFakeFS()
	f.addDir("/game/mods")
	f.addFile("/game/Darktide.exe")
	f.addSymlink("/game/mods/better_ui")

	assert.True(t, IsDir(f, "/game/mods"), "IsDir(/game/mods) = false")

	assert.True(t, IsFile(f, "/game/Darktide.exe"), "IsFile(/game/Darktide.exe) = false")

	assert.False(t, IsDir(f, "/game/Darktide.exe"), "IsDir on a file = true")

	assert.False(t, IsFile(f, "/game/mods"), "IsFile on a dir = true")

	assert.True(t, Exists(f, "/game/mods"), "Exists(/game/mods) = false")

	assert.False(t, Exists(f, "/game/nope"), "Exists on a missing path = true")

	assert.True(t, ExistsNoFollow(f, "/game/mods/better_ui"), "ExistsNoFollow on a symlink = false")
}

func TestFileSize(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "payload.bin")
	err := os.WriteFile(file, []byte("0123456789"), 0o644)
	require.NoError(t, err)

	size, err := FileSize(file)
	require.NoError(t, err, "FileSize")

	assert.Equal(t, int64(10), size, "FileSize = %d, want 10", size)

	_, err = FileSize(dir)
	assert.Error(t, err, "FileSize on a directory succeeded, want error")

	_, err = FileSize(filepath.Join(dir, "missing"))
	assert.Error(t, err, "FileSize on a missing file succeeded, want error")

	_, err = FileSize("")
	assert.Error(t, err, "FileSize on an empty path succeeded, want error")
}

func TestHashing(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")

	c := filepath.Join(dir, "c.txt")
	for path, content := range map[string]string{a: "same", b: "same", c: "other"} {
		err := os.WriteFile(path, []byte(content), 0o644)
		require.NoError(t, err)
	}

	hashA, err := FileSHA256(a)
	require.NoError(t, err, "FileSHA256")

	hashB, err := FileSHA256(b)
	require.NoError(t, err, "FileSHA256")

	hashC, err := FileSHA256(c)
	require.NoError(t, err, "FileSHA256")

	assert.Equal(t, hashB, hashA, "identical content hashed differently: %q vs %q", hashA, hashB)

	assert.NotEqual(t, hashC, hashA, "different content hashed identically: %q", hashA)

	assert.Len(t, hashA, 64, "digest length = %d, want 64", len(hashA))
	// StringSHA256 must agree with FileSHA256 for the same bytes.
	got := StringSHA256([]byte("same"))
	assert.Equal(t, hashA, got, "StringSHA256 = %q, want %q", got, hashA)

	_, err = FileSHA256(filepath.Join(dir, "missing"))
	assert.Error(t, err, "FileSHA256 on a missing file succeeded, want error")

	_, err = FileSHA256("")
	assert.Error(t, err, "FileSHA256 on an empty path succeeded, want error")

	_, err = FileSHA256(dir)
	assert.Error(t, err, "FileSHA256 on a directory succeeded, want error")
}

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "deep", "config.json")

	if err := AtomicWriteFile(target, []byte(`{"a":1}`), 0o600); err != nil {
		require.FailNow(t, "AtomicWriteFile: %v", err)
	}

	got, err := os.ReadFile(target)
	require.NoError(t, err, "ReadFile")

	if string(got) != `{"a":1}` {
		assert.Fail(t, "content = %q, want %q", got, `{"a":1}`)
	}

	info, err := os.Stat(target)
	require.NoError(t, err)

	runtime := info.Mode().Perm()
	assert.Equal(t, fs.FileMode(0o600), runtime, "mode = %v, want 0600", runtime)

	// Overwriting must replace the file without leaving temporaries behind.
	if err := AtomicWriteFile(target, []byte(`{"a":2}`), 0o644); err != nil {
		require.FailNow(t, "AtomicWriteFile overwrite: %v", err)
	}

	got, _ = os.ReadFile(target)
	if string(got) != `{"a":2}` {
		assert.Fail(t, "content after overwrite = %q", got)
	}

	assertNoTempFiles(t, filepath.Dir(target))

	// A zero mode falls back to a sane default instead of creating an
	// unreadable file.
	err = AtomicWriteFile(target, []byte("x"), 0)
	require.NoError(t, err, "AtomicWriteFile with zero mode: %v", err)

	info, _ = os.Stat(target)
	assert.Equal(
		t,
		os.FileMode(0o644),
		info.Mode().Perm(),
		"default mode = %v, want 0644",
		info.Mode().Perm(),
	)

	err = AtomicWriteFile("", []byte("x"), 0o644)
	assert.Error(t, err, "AtomicWriteFile with an empty path succeeded, want error")
}

func TestAtomicWriteFileFailureLeavesNoTemporary(t *testing.T) {
	dir := t.TempDir()
	// A directory in place of the target makes the rename fail.
	target := filepath.Join(dir, "target")
	err := os.MkdirAll(filepath.Join(target, "child"), 0o755)
	require.NoError(t, err)

	err = AtomicWriteFile(target, []byte("x"), 0o644)
	require.Error(t, err, "AtomicWriteFile onto a directory succeeded, want error")

	assertNoTempFiles(t, dir)
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, e := range entries {
		assert.False(
			t,
			strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), ".tmp"),
			"temporary file left behind: %s",
			e.Name(),
		)
	}
}

func TestReadFileLimit(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "data.bin")
	err := os.WriteFile(file, []byte("0123456789"), 0o644)
	require.NoError(t, err)

	data, err := ReadFileLimit(file, 10)
	require.NoError(t, err, "ReadFileLimit at exact size")

	assert.Len(t, data, 10, "len = %d, want 10", len(data))

	_, err = ReadFileLimit(file, 9)
	assert.Error(t, err, "ReadFileLimit below file size succeeded, want error")

	_, err = ReadFileLimit(file, 0)
	assert.Error(t, err, "ReadFileLimit with a zero limit succeeded, want error")

	_, err = ReadFileLimit(file, -1)
	assert.Error(t, err, "ReadFileLimit with a negative limit succeeded, want error")

	_, err = ReadFileLimit(filepath.Join(dir, "missing"), 10)
	assert.Error(t, err, "ReadFileLimit on a missing file succeeded, want error")

	_, err = ReadFileLimit("", 10)
	assert.Error(t, err, "ReadFileLimit with an empty path succeeded, want error")

	// An exactly-empty file is legal.
	empty := filepath.Join(dir, "empty.bin")
	err = os.WriteFile(empty, nil, 0o644)
	require.NoError(t, err)

	data, err = ReadFileLimit(empty, 1)
	assert.False(
		t,
		err != nil || len(data) != 0,
		"ReadFileLimit(empty) = %q, %v; want empty, nil",
		data,
		err,
	)
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "out", "dst.txt")

	err := os.WriteFile(src, []byte("payload"), 0o640)
	require.NoError(t, err)

	err = CopyFile(src, dst)
	require.NoError(t, err, "CopyFile: %v", err)

	data, err := os.ReadFile(dst)
	require.NoError(t, err)

	assert.Equal(t, "payload", string(data), "content = %q", data)

	srcInfo, _ := os.Stat(src)

	dstInfo, _ := os.Stat(dst)
	assert.Equal(
		t,
		dstInfo.Mode().Perm(),
		srcInfo.Mode().Perm(),
		"mode = %v, want %v",
		dstInfo.Mode().Perm(),
		srcInfo.Mode().Perm(),
	)

	// Overwriting an existing file works.
	err = os.WriteFile(src, []byte("newer"), 0o640)
	require.NoError(t, err)

	err = CopyFile(src, dst)
	require.NoError(t, err, "CopyFile overwrite: %v", err)

	data, _ = os.ReadFile(dst)
	assert.Equal(t, "newer", string(data), "content after overwrite = %q", data)

	// A directory destination is an error, not a silent nest.
	err = CopyFile(src, dir)
	assert.Error(t, err, "CopyFile onto a directory succeeded, want error")

	err = CopyFile(dir, dst)
	assert.Error(t, err, "CopyFile from a directory succeeded, want error")

	err = CopyFile(filepath.Join(dir, "missing"), dst)
	assert.Error(t, err, "CopyFile from a missing file succeeded, want error")

	err = CopyFile("", dst)
	assert.Error(t, err, "CopyFile with an empty source succeeded, want error")

	err = CopyFile(src, "")
	assert.Error(t, err, "CopyFile with an empty destination succeeded, want error")

	assertNoTempFiles(t, filepath.Dir(dst))
}

func TestCopyTree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	err := os.MkdirAll(filepath.Join(src, "textures", "hud"), 0o755)
	require.NoError(t, err)

	if err := os.WriteFile(filepath.Join(src, "mod.json"), []byte("{}"), 0o644); err != nil {
		require.FailNow(t, err.Error())
	}

	err = os.WriteFile(filepath.Join(src, "textures", "hud", "a.dds"), []byte("A"), 0o644)
	require.NoError(t, err)
	// A link inside the tree must be recreated, not followed.
	if err := os.Symlink(
		filepath.Join(src, "mod.json"),
		filepath.Join(src, "alias.json"),
	); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err = CopyTree(src, dst)
	require.NoError(t, err, "CopyTree: %v", err)

	for _, rel := range []string{"mod.json", "textures/hud/a.dds"} {
		_, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel)))
		assert.NoError(t, err, "copied tree missing %s: %v", rel, err)
	}

	linkInfo, err := os.Lstat(filepath.Join(dst, "alias.json"))
	require.NoError(t, err)

	assert.NotEqual(
		t,
		os.FileMode(0),
		linkInfo.Mode()&os.ModeSymlink,
		"symlink was dereferenced instead of recreated",
	)

	// An empty source directory is legal and yields an empty destination.
	emptySrc := filepath.Join(dir, "empty")
	err = os.MkdirAll(emptySrc, 0o755)
	require.NoError(t, err)

	emptyDst := filepath.Join(dir, "empty-dst")
	err = CopyTree(emptySrc, emptyDst)
	require.NoError(t, err, "CopyTree of an empty dir: %v", err)

	entries, err := os.ReadDir(emptyDst)
	assert.False(
		t,
		err != nil || len(entries) != 0,
		"empty destination has %d entries, err %v",
		len(entries),
		err,
	)

	err = CopyTree(filepath.Join(dir, "missing"), dst)
	assert.Error(t, err, "CopyTree from a missing source succeeded, want error")

	err = CopyTree(filepath.Join(dir, "src", "mod.json"), dst)
	assert.Error(t, err, "CopyTree from a file succeeded, want error")

	err = CopyTree("", dst)
	assert.Error(t, err, "CopyTree with an empty source succeeded, want error")

	err = CopyTree(src, "")
	assert.Error(t, err, "CopyTree with an empty destination succeeded, want error")
}

func TestScanTree(t *testing.T) {
	root := t.TempDir()

	stats, err := ScanTree(root)
	require.NoError(t, err, "ScanTree on empty dir")

	assert.False(
		t,
		stats.FileCount != 0 || stats.TotalSize != 0,
		"empty dir stats = %+v, want zero",
		stats,
	)

	assert.True(
		t,
		stats.Oldest.IsZero() || !stats.Newest.IsZero(),
		"empty dir timestamps = %v/%v, want zero",
		stats.Oldest,
		stats.Newest,
	)

	old := time.Now().Add(-48 * time.Hour)

	files := map[string]struct {
		modTime time.Time
		content string
	}{
		"a.txt":      {time.Now(), "aaa"},
		"sub/b.txt":  {old, "bbbb"},
		"sub/c/d.md": {time.Now().Add(-time.Hour), "dd"},
	}
	for rel, f := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		err := os.MkdirAll(filepath.Dir(p), 0o755)
		require.NoError(t, err)

		err = os.WriteFile(p, []byte(f.content), 0o644)
		require.NoError(t, err)

		err = os.Chtimes(p, f.modTime, f.modTime)
		require.NoError(t, err)
	}

	stats, err = ScanTree(root)
	require.NoError(t, err, "ScanTree")

	assert.Equal(t, 3, stats.FileCount, "FileCount = %d, want 3", stats.FileCount)

	want := int64(3 + 4 + 2)
	assert.Equal(t, want, stats.TotalSize, "TotalSize = %d, want %d", stats.TotalSize, want)

	require.False(t, stats.Oldest.IsZero() || stats.Newest.IsZero(), "timestamps not populated")

	assert.False(
		t,
		stats.Oldest.Before(old.Add(-time.Minute)) || stats.Oldest.After(old.Add(time.Minute)),
		"Oldest = %v, want about %v",
		stats.Oldest,
		old,
	)

	assert.True(t, stats.Newest.After(old), "Newest = %v, want after %v", stats.Newest, old)

	info := ToCacheInfo(root, stats)
	assert.Equal(
		t,
		filepath.ToSlash(root),
		info.Path,
		"CacheInfo.Path = %q, want %q",
		info.Path,
		filepath.ToSlash(root),
	)

	assert.False(
		t,
		info.FileCount != stats.FileCount || info.TotalSize != stats.TotalSize,
		"CacheInfo = %+v, does not mirror stats %+v",
		info,
		stats,
	)
}

func TestScanTreeMissingAndFileRoot(t *testing.T) {
	dir := t.TempDir()

	stats, err := ScanTree(filepath.Join(dir, "missing"))
	require.NoError(t, err, "ScanTree on a missing root")

	assert.Equal(t, 0, stats.FileCount, "missing root stats = %+v, want zero", stats)

	stats, err = ScanTree("")
	assert.False(
		t,
		err != nil || stats.FileCount != 0,
		"ScanTree(\"\") = %+v, %v; want zero, nil",
		stats,
		err,
	)

	file := filepath.Join(dir, "single.txt")
	err = os.WriteFile(file, []byte("12345"), 0o644)
	require.NoError(t, err)

	stats, err = ScanTree(file)
	require.NoError(t, err, "ScanTree on a file")

	assert.False(
		t,
		stats.FileCount != 1 || stats.TotalSize != 5,
		"file root stats = %+v, want 1 file of 5 bytes",
		stats,
	)
}

func TestScanTreeCountsLinksWithoutFollowingThem(t *testing.T) {
	root := t.TempDir()

	outside := t.TempDir()
	err := os.WriteFile(filepath.Join(outside, "huge.dds"), make([]byte, 1024), 0o644)
	require.NoError(t, err)

	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	stats, err := ScanTree(root)
	require.NoError(t, err, "ScanTree")
	// The link itself is one entry; the 1024 bytes behind it must not be
	// counted, or the cache size would report data it does not own.
	assert.Equal(t, 1, stats.FileCount, "FileCount = %d, want 1", stats.FileCount)

	assert.False(
		t,
		stats.TotalSize >= 1024,
		"TotalSize = %d: the walk followed a link out of the root",
		stats.TotalSize,
	)
}

func TestMkdirAll(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "a", "b", "c")
	err := MkdirAll(target)
	require.NoError(t, err, "MkdirAll: %v", err)

	if !IsDir(OSFS{}, target) {
		assert.Fail(t, "directory not created")
	}
	// Idempotent.
	err = MkdirAll(target)
	require.NoError(t, err, "MkdirAll twice: %v", err)

	err = MkdirAll("")
	assert.Error(t, err, "MkdirAll(\"\") succeeded, want error")
	// A path whose parent is a regular file cannot be created.
	file := filepath.Join(dir, "file")
	err = os.WriteFile(file, []byte("x"), 0o644)
	require.NoError(t, err)

	err = MkdirAll(filepath.Join(file, "child"))
	assert.Error(t, err, "MkdirAll under a regular file succeeded, want error")
}

func TestRemoveEmptyDir(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty")
	err := os.MkdirAll(empty, 0o755)
	require.NoError(t, err)

	err = RemoveEmptyDir(empty)
	require.NoError(t, err, "RemoveEmptyDir: %v", err)

	if IsDir(OSFS{}, empty) {
		assert.Fail(t, "empty directory still exists")
	}

	full := filepath.Join(dir, "full")
	err = os.MkdirAll(full, 0o755)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(full, "keep.txt"), []byte("x"), 0o644)
	require.NoError(t, err)

	err = RemoveEmptyDir(full)
	require.NoError(t, err, "RemoveEmptyDir on a non-empty dir: %v", err)

	if !IsDir(OSFS{}, full) {
		assert.Fail(t, "non-empty directory was removed")
	}

	err = RemoveEmptyDir(filepath.Join(dir, "missing"))
	assert.Error(t, err, "RemoveEmptyDir on a missing dir succeeded, want error")
}
