package platform

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// symlinkCapable reports whether this host can create symlinks, so tests that
// exercise the link path skip cleanly where it is unavailable.
func symlinkCapable(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "a"), filepath.Join(dir, "b")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// modFixture lays out a manager storage folder plus a game mods folder.
type modFixture struct {
	storage  string
	version  string
	gameMods string
}

func newModFixture(t *testing.T) modFixture {
	t.Helper()
	root := t.TempDir()

	storage := filepath.Join(root, "mods", "better_ui", "1.2.0")
	err := os.MkdirAll(filepath.Join(storage, "textures"), 0o755)
	require.NoError(t, err)

	writeFile(t, filepath.Join(storage, "mod.json"), `{"id":"better_ui"}`)
	writeFile(t, filepath.Join(storage, "mod.toml"), "id = \"better_ui\"\n")
	writeFile(t, filepath.Join(storage, "textures", "hud.dds"), "DDS")

	return modFixture{
		storage:  storage,
		version:  storage,
		gameMods: filepath.Join(root, "game", "mods"),
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o755)
	require.NoError(t, err)

	err = os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)
}

func TestDeployModeValid(t *testing.T) {
	cases := map[string]struct {
		mode DeployMode
		want bool
	}{
		"symlink": {DeploySymlink, true},
		"copy":    {DeployCopy, true},
		"empty":   {DeployMode(""), false},
		"unknown": {DeployMode("hardlink"), false},
		"case":    {DeployMode("SYMLINK"), false},
	}
	for name, tc := range cases {
		got := tc.mode.Valid()
		assert.Equal(t, tc.want, got, "%s: Valid() = %v, want %v", name, got, tc.want)
	}

	got := DeploySymlink.String()
	assert.Equal(t, "symlink", got, "String() = %q", got)
}

func TestDeployerMode(t *testing.T) {
	cases := map[string]struct {
		platform domain.Platform
		want     DeployMode
		probe    bool
	}{
		"linux always symlinks":        {domain.PlatformLinux, DeploySymlink, false},
		"linux probe irrelevant":       {domain.PlatformLinux, DeploySymlink, true},
		"darwin always symlinks":       {domain.PlatformDarwin, DeploySymlink, false},
		"windows with developer mode":  {domain.PlatformWindows, DeploySymlink, true},
		"windows without symlink priv": {domain.PlatformWindows, DeployCopy, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := NewDeployerWithProbe(tc.platform, func() bool { return tc.probe })
			got := d.Mode()
			assert.Equal(t, tc.want, got, "Mode() = %v, want %v", got, tc.want)

			gotPlatform := d.Platform()
			assert.Equal(
				t,
				tc.platform,
				gotPlatform,
				"Platform() = %v, want %v",
				gotPlatform,
				tc.platform,
			)
		})
	}
}

func TestNewDeployerNilProbeMeansCopyOnWindows(t *testing.T) {
	d := NewDeployerWithProbe(domain.PlatformWindows, nil)
	got := d.Mode()
	assert.Equal(t, DeployCopy, got, "Mode() with nil probe = %v, want %v", got, DeployCopy)

	assert.Equal(
		t,
		DeploySymlink,
		NewDeployerWithFS(domain.PlatformLinux, nil, nil).Mode(),
		"nil probe on linux should still symlink",
	)
}

func TestDeploySymlinks(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)
	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })

	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err, "Deploy: %v", err)

	// Every payload file and directory is exposed; the manifest is not.
	for _, rel := range []string{"mod.json", "textures"} {
		p := filepath.Join(f.gameMods, rel)

		info, err := os.Lstat(p)
		require.NoError(t, err, "%s not deployed: %v", rel)

		assert.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "%s is not a symlink", rel)
	}

	_, err = os.Lstat(filepath.Join(f.gameMods, "mod.toml"))
	assert.Error(t, err, "mod.toml was deployed to the game folder")
	// Content is reachable through the link.
	data, err := os.ReadFile(filepath.Join(f.gameMods, "mod.json"))
	require.NoError(t, err, "reading through the link")

	if string(data) != `{"id":"better_ui"}` {
		assert.Fail(t, "content through the link = %q", data)
	}
}

func TestDeployIsIdempotent(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)
	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })

	for i := 0; i < 3; i++ {
		err := d.Deploy(f.storage, f.gameMods)
		require.NoError(t, err, "Deploy attempt %d: %v", i, err)
	}
}

func TestDeployReplacesDanglingLink(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)
	// A leftover link pointing at a version that no longer exists.
	err := os.MkdirAll(f.gameMods, 0o755)
	require.NoError(t, err)

	stale := filepath.Join(f.gameMods, "mod.json")
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone", "mod.json"), stale); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })
	err = d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err, "Deploy over a dangling link: %v", err)

	if got := LinkStatus(OSFS{}, stale, ""); got != LinkOK {
		assert.Fail(t, "link state = %v, want %v", got, LinkOK)
	}
}

func TestDeployCopyMode(t *testing.T) {
	f := newModFixture(t)
	d := NewDeployerWithProbe(domain.PlatformWindows, func() bool { return false })

	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err, "Deploy: %v", err)

	info, err := os.Lstat(filepath.Join(f.gameMods, "mod.json"))
	require.NoError(t, err)

	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "copy mode created a symlink")

	if !IsDir(OSFS{}, filepath.Join(f.gameMods, "textures")) {
		assert.Fail(t, "nested directory not copied")
	}

	data, err := os.ReadFile(filepath.Join(f.gameMods, "textures", "hud.dds"))
	assert.False(t, err != nil || string(data) != "DDS", "nested file content = %q, %v", data, err)

	_, err = os.Lstat(filepath.Join(f.gameMods, "mod.toml"))
	assert.Error(t, err, "mod.toml was deployed")
	// Editing the deployed copy must not touch the manager's payload.
	err = os.WriteFile(filepath.Join(f.gameMods, "mod.json"), []byte("edited"), 0o644)
	require.NoError(t, err)

	original, err := os.ReadFile(filepath.Join(f.storage, "mod.json"))
	if err != nil || string(original) != `{"id":"better_ui"}` {
		assert.Fail(t, "storage payload was modified through the copy: %q, %v", original, err)
	}
}

func TestDeployValidation(t *testing.T) {
	f := newModFixture(t)
	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })

	err := d.Deploy("", f.gameMods)
	assert.Error(t, err, "Deploy with an empty source succeeded, want error")

	err = d.Deploy(f.storage, "")
	assert.Error(t, err, "Deploy with an empty destination succeeded, want error")

	err = d.DeployAs(f.storage, f.gameMods, DeployMode("bogus"))
	assert.Error(t, err, "DeployAs with an unknown mode succeeded, want error")

	err = d.Deploy(filepath.Join(f.storage, "missing"), f.gameMods)
	assert.Error(t, err, "Deploy from a missing source succeeded, want error")

	err = d.Deploy(filepath.Join(f.storage, "mod.json"), f.gameMods)
	assert.Error(t, err, "Deploy from a file succeeded, want error")

	// Deploying a mod folder onto itself would delete the payload.
	self := filepath.Join(f.storage, "self")
	err = d.Deploy(f.storage, self)
	assert.Error(t, err, "Deploy into a subdirectory of the source succeeded, want error")

	err = d.Deploy(f.storage, f.storage)
	assert.Error(t, err, "Deploy onto the source itself succeeded, want error")
}

func TestDeployEmptySource(t *testing.T) {
	f := newModFixture(t)

	empty := filepath.Join(t.TempDir(), "empty-mod")
	err := os.MkdirAll(empty, 0o755)
	require.NoError(t, err)

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })
	err = d.Deploy(empty, f.gameMods)
	require.NoError(t, err, "Deploy of an empty mod: %v", err)

	if !IsDir(OSFS{}, f.gameMods) {
		assert.Fail(t, "destination folder was not created")
	}

	entries, err := os.ReadDir(f.gameMods)
	require.NoError(t, err)

	assert.Empty(t, entries, "destination has %d entries, want 0", len(entries))
}

func TestDeployPreservesForeignEntries(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)
	foreign := filepath.Join(f.gameMods, "user_notes.txt")
	writeFile(t, foreign, "keep me")

	foreignLink := filepath.Join(f.gameMods, "user_link")
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), foreignLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })
	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err, "Deploy: %v", err)

	data, err := os.ReadFile(foreign)
	assert.False(
		t,
		err != nil || string(data) != "keep me",
		"foreign file was clobbered: %q, %v",
		data,
		err,
	)

	_, err = os.Lstat(foreignLink)
	assert.NoError(t, err, "foreign link was removed: %v", err)
}

func TestUndeployEntries(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })
	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err, "Deploy: %v", err)

	removed, err := d.UndeployEntries(
		f.gameMods,
		[]string{"mod.json", "textures", "does-not-exist"},
	)
	require.NoError(t, err, "UndeployEntries")

	assert.Equal(t, 2, removed, "removed = %d, want 2", removed)

	if IsDir(OSFS{}, filepath.Join(f.gameMods, "textures")) {
		assert.Fail(t, "textures still present")
	}
	// The manager's own payload must survive.
	if !IsFile(OSFS{}, filepath.Join(f.storage, "mod.json")) {
		assert.Fail(t, "storage payload was deleted")
	}
}

func TestUndeployEntriesRejectsTraversal(t *testing.T) {
	f := newModFixture(t)
	writeFile(t, filepath.Join(f.gameMods, "keep.txt"), "x")
	outside := filepath.Join(filepath.Dir(f.gameMods), "victim.txt")
	writeFile(t, outside, "x")

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })

	removed, err := d.UndeployEntries(f.gameMods, []string{
		"../victim.txt",
		"..",
		".",
		"",
		`sub\keep.txt`,
		"mod.toml",
	})
	require.NoError(t, err, "UndeployEntries")

	assert.Equal(t, 0, removed, "removed = %d, want 0", removed)

	if !IsFile(OSFS{}, outside) {
		assert.Fail(t, "a file outside the mods folder was removed")
	}

	if !IsFile(OSFS{}, filepath.Join(f.gameMods, "keep.txt")) {
		assert.Fail(t, "keep.txt was removed")
	}
}

func TestUndeployEntriesEmptyDestination(t *testing.T) {
	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })

	removed, err := d.UndeployEntries("", []string{"a"})
	assert.False(
		t,
		err != nil || removed != 0,
		"UndeployEntries(\"\") = %d, %v; want 0, nil",
		removed,
		err,
	)
}

func TestLinkStatus(t *testing.T) {
	symlinkCapable(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	writeFile(t, target, "payload")

	other := filepath.Join(dir, "other.txt")
	writeFile(t, other, "other")

	real := filepath.Join(dir, "real.txt")
	writeFile(t, real, "real")

	dangling := filepath.Join(dir, "dangling.txt")
	if err := os.Symlink(filepath.Join(dir, "gone.txt"), dangling); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cases := map[string]struct {
		path   string
		target string
		want   LinkState
	}{
		"missing":             {filepath.Join(dir, "nope.txt"), "", LinkMissing},
		"real file":           {real, "", LinkNotAManagedLink},
		"real file vs target": {real, target, LinkNotAManagedLink},
		"dangling":            {dangling, "", LinkBroken},
		"ok no target":        {link, "", LinkOK},
		"ok with target":      {link, target, LinkOK},
		"retargeted":          {link, other, LinkRetargeted},
		"empty path":          {"", "", LinkMissing},
	}
	for name, tc := range cases {
		if got := LinkStatus(OSFS{}, tc.path, tc.target); got != tc.want {
			assert.Fail(t, "%s: LinkStatus = %v, want %v", name, got, tc.want)
		}
	}

	got := LinkStatus(nil, link, "")
	assert.Equal(t, LinkOK, got, "LinkStatus with nil FS = %v, want %v", got, LinkOK)
}

func TestLinkStateNeedsRepair(t *testing.T) {
	repairable := map[LinkState]bool{
		LinkBroken: true, LinkRetargeted: true,
		LinkOK: false, LinkMissing: false, LinkNotAManagedLink: false,
	}
	for state, want := range repairable {
		got := state.NeedsRepair()
		assert.Equal(t, want, got, "%v.NeedsRepair() = %v, want %v", state, got, want)
	}
}

func TestVerifyFolder(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })
	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err)
	// Break one link and add one foreign file.
	err = os.Remove(filepath.Join(f.gameMods, "mod.json"))
	require.NoError(t, err)

	if err := os.Symlink(
		filepath.Join(f.storage, "gone.json"),
		filepath.Join(f.gameMods, "mod.json"),
	); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	writeFile(t, filepath.Join(f.gameMods, "user.txt"), "x")

	states, total, err := d.VerifyFolder(f.gameMods)
	require.NoError(t, err, "VerifyFolder")

	assert.Equal(t, 3, total, "total = %d, want 3 (mod.json, textures, user.txt)", total)

	got := states["mod.json"]
	assert.Equal(t, LinkBroken, got, "mod.json = %v, want %v", got, LinkBroken)

	got = states["textures"]
	assert.Equal(t, LinkOK, got, "textures = %v, want %v", got, LinkOK)

	got = states["user.txt"]
	assert.Equal(t, LinkNotAManagedLink, got, "user.txt = %v, want %v", got, LinkNotAManagedLink)

	_, ok := states["mod.toml"]
	assert.False(t, ok, "manifest reported in the folder listing")

	states, total, err = d.VerifyFolder("")
	assert.False(
		t,
		err != nil || total != 0 || len(states) != 0,
		"VerifyFolder(\"\") = %v, %d, %v",
		states,
		total,
		err,
	)

	missing := filepath.Join(t.TempDir(), "nope")
	states, total, err = d.VerifyFolder(missing)
	assert.False(
		t,
		err != nil || total != 0 || len(states) != 0,
		"VerifyFolder(missing) = %v, %d, %v; want empty, nil",
		states,
		total,
		err,
	)
}

func TestRepair(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })
	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err)

	// Nothing is wrong yet.
	fixed, err := d.Repair(f.storage, f.gameMods)
	require.NoError(t, err, "Repair on a healthy folder")

	assert.Len(t, fixed, 0, "fixed = %v, want none", fixed)

	// Break both a link and the target of another.
	err = os.Remove(filepath.Join(f.gameMods, "mod.json"))
	require.NoError(t, err)
	// A dangling link is repaired when the source still exists.
	if err := os.Symlink(
		filepath.Join(f.storage, "wrong.json"),
		filepath.Join(f.gameMods, "mod.json"),
	); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Retarget a good link to a stale version folder.
	oldDir := filepath.Join(filepath.Dir(f.storage), "1.0.0")
	err = os.MkdirAll(oldDir, 0o755)
	require.NoError(t, err)

	writeFile(t, filepath.Join(oldDir, "mod.json"), "{}")

	err = os.RemoveAll(filepath.Join(f.gameMods, "textures"))
	require.NoError(t, err)

	if err := os.Symlink(
		filepath.Join(oldDir, "textures"),
		filepath.Join(f.gameMods, "textures"),
	); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	fixed, err = d.Repair(f.storage, f.gameMods)
	require.NoError(t, err, "Repair")

	assert.Len(t, fixed, 2, "fixed = %v, want both entries", fixed)

	if got := LinkStatus(OSFS{}, filepath.Join(f.gameMods, "mod.json"), ""); got != LinkOK {
		assert.Fail(t, "mod.json after repair = %v, want %v", got, LinkOK)
	}

	if got := LinkStatus(OSFS{}, filepath.Join(f.gameMods, "textures"), ""); got != LinkOK {
		assert.Fail(t, "textures after repair = %v, want %v", got, LinkOK)
	}
}

func TestRepairLeavesDanglingLinkWhenSourceIsGone(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })
	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err)
	// Remove the payload but keep the link.
	err = os.Remove(filepath.Join(f.storage, "mod.json"))
	require.NoError(t, err)

	fixed, err := d.Repair(f.storage, f.gameMods)
	require.NoError(t, err, "Repair")

	assert.Len(
		t,
		fixed,
		0,
		"fixed = %v, want none: recreating the link would leave it dangling",
		fixed,
	)

	if got := LinkStatus(OSFS{}, filepath.Join(f.gameMods, "mod.json"), ""); got != LinkBroken {
		assert.Fail(t, "link state = %v, want %v", got, LinkBroken)
	}
}

func TestRepairMissingPaths(t *testing.T) {
	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })

	fixed, err := d.Repair("", "")
	assert.False(t, err != nil || len(fixed) != 0, "Repair(\"\", \"\") = %v, %v", fixed, err)

	dir := t.TempDir()
	fixed, err = d.Repair(filepath.Join(dir, "missing"), dir)
	assert.False(
		t,
		err != nil || len(fixed) != 0,
		"Repair with a missing source = %v, %v",
		fixed,
		err,
	)
}

func TestOwns(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)

	d := NewDeployerWithProbe(domain.PlatformLinux, func() bool { return true })
	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err)

	writeFile(t, filepath.Join(f.gameMods, "user.txt"), "x")

	assert.True(t, d.Owns(f.gameMods, "mod.json"), "Owns(deployed link) = false")

	assert.False(t, d.Owns(f.gameMods, "user.txt"), "Owns(foreign file) = true")

	assert.False(t, d.Owns(f.gameMods, "absent.txt"), "Owns(missing) = true")

	assert.False(
		t,
		d.Owns(f.gameMods, "") || d.Owns(f.gameMods, "../escape"),
		"Owns accepted an unsafe name",
	)
}

func TestDeployCopyRecreatesSymlinks(t *testing.T) {
	symlinkCapable(t)
	f := newModFixture(t)
	// A symlink inside the payload must stay a link in copy mode.
	if err := os.Symlink(
		filepath.Join(f.storage, "mod.json"),
		filepath.Join(f.storage, "alias.json"),
	); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	d := NewDeployerWithProbe(domain.PlatformWindows, func() bool { return false })
	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err, "Deploy: %v", err)

	info, err := os.Lstat(filepath.Join(f.gameMods, "alias.json"))
	require.NoError(t, err)

	assert.NotEqual(
		t,
		os.FileMode(0),
		info.Mode()&os.ModeSymlink,
		"copy mode dereferenced the symlink",
	)
}

func TestDeployOverExistingRealFile(t *testing.T) {
	f := newModFixture(t)
	writeFile(t, filepath.Join(f.gameMods, "mod.json"), "stale")

	d := NewDeployerWithProbe(domain.PlatformWindows, func() bool { return false })
	err := d.Deploy(f.storage, f.gameMods)
	require.NoError(t, err, "Deploy over an existing file: %v", err)

	data, err := os.ReadFile(filepath.Join(f.gameMods, "mod.json"))
	if err != nil || string(data) != `{"id":"better_ui"}` {
		assert.Fail(t, "content = %q, %v; want the payload", data, err)
	}
}

func TestCanCreateSymlinks(t *testing.T) {
	// The answer must at least not panic on the host running the tests.
	if domain.CurrentPlatform().IsWindows() {
		return
	}

	assert.True(t, CanCreateSymlinks(), "CanCreateSymlinks = false on a Unix host")
}

func TestIsManifestName(t *testing.T) {
	for _, name := range []string{"mod.toml", "MOD.TOML", "manifest.toml", "metadata.toml"} {
		assert.True(t, isManifestName(name), "isManifestName(%q) = false", name)
	}

	for _, name := range []string{"mod.json", "toml", "", "mod.toml.bak"} {
		assert.False(t, isManifestName(name), "isManifestName(%q) = true", name)
	}
}
