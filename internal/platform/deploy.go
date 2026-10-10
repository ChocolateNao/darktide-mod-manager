package platform

import (
	"os"
	"path/filepath"
	"strings"

	manifestpkg "github.com/ChocolateNao/darktide-mod-manager/internal/manifest"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// DeployMode selects how a mod folder is exposed to the game.
type DeployMode string

const (
	// DeploySymlink exposes each file as a symbolic link. Preferred because the
	// manager keeps sole ownership of the payload.
	DeploySymlink DeployMode = "symlink"
	// DeployCopy duplicates the payload into the game folder. Used where
	// symlinks are unavailable, which on Windows means without Developer Mode
	// or administrator rights.
	DeployCopy DeployMode = "copy"
)

// Valid reports whether the mode is one of the known modes.
func (m DeployMode) Valid() bool {
	return m == DeploySymlink || m == DeployCopy
}

// String implements fmt.Stringer.
func (m DeployMode) String() string { return string(m) }

// Deployer places a mod's storage folder into the game mods folder using
// symlinks when the platform allows it and copying otherwise.
type Deployer struct {
	fs        FS
	symlinkOK func() bool
	platform  domain.Platform
}

// NewDeployer returns a deployer for a platform, probing symlink support on the
// host.
func NewDeployer(p domain.Platform) *Deployer {
	return NewDeployerWithFS(p, CanCreateSymlinks, RealFS())
}

// NewDeployerWithProbe returns a deployer whose symlink capability comes from an
// injected probe, which lets tests exercise the copy fallback on any host.
func NewDeployerWithProbe(p domain.Platform, probe func() bool) *Deployer {
	return NewDeployerWithFS(p, probe, RealFS())
}

// NewDeployerWithFS returns a deployer with both the symlink probe and the
// filesystem injected.
func NewDeployerWithFS(p domain.Platform, probe func() bool, fsys FS) *Deployer {
	if probe == nil {
		probe = func() bool { return false }
	}

	if fsys == nil {
		fsys = RealFS()
	}

	return &Deployer{platform: p, symlinkOK: probe, fs: fsys}
}

// Platform reports the platform this deployer targets.
func (d *Deployer) Platform() domain.Platform { return d.platform }

// SupportsSymlinks reports the raw capability without a probe result.
func (d *Deployer) SupportsSymlinks() bool {
	if d.platform.IsWindows() {
		// Windows needs Developer Mode or elevation; the probe decides.
		return d.symlinkOK()
	}

	return true
}

// Mode reports the deployment strategy this platform defaults to: symlinks on
// Unix, and on Windows only when the probe confirms symlinks work.
func (d *Deployer) Mode() DeployMode {
	if d.SupportsSymlinks() {
		return DeploySymlink
	}

	return DeployCopy
}

// ModeFor answers the question a deployment actually asks: given the game's
// preferred install method, what happens here, now.
//
// A preference of symlink is honoured when the platform can create links and
// quietly degrades to copying when it cannot — a user asking for symlinks on a
// Windows box without Developer Mode is asking for their mods to work, and
// failing the install would not be honouring that. The reverse never happens:
// a request for direct copies is copied, because that is the whole point of
// asking. Anything unrecognised, including the empty string, falls back to the
// platform default.
func (d *Deployer) ModeFor(preferred string) DeployMode {
	switch strings.TrimSpace(preferred) {
	case domain.InstallMethodSymlink:
		if d.SupportsSymlinks() {
			return DeploySymlink
		}

		return DeployCopy
	case domain.InstallMethodDirect:
		return DeployCopy
	default:
		return d.Mode()
	}
}

// Deploy exposes the contents of srcDir at dstDir using the current mode. An
// empty srcDir is allowed and yields an empty dstDir, which is how a mod with no
// files is represented.
func (d *Deployer) Deploy(srcDir, dstDir string) error {
	return d.DeployAs(srcDir, dstDir, d.Mode())
}

// DeployAs exposes the contents of srcDir at dstDir using an explicit mode.
// Existing entries at dstDir are replaced, but entries the source no longer has
// are left alone: a stale file there belongs to another mod or to the user.
func (d *Deployer) DeployAs(srcDir, dstDir string, mode DeployMode) error {
	if srcDir == "" || dstDir == "" {
		return errors.New(
			errors.ErrCodeInvalidMod,
			"deploy needs a non-empty source and destination",
		)
	}

	if !mode.Valid() {
		return errors.New(errors.ErrCodeInvalidMod, "unknown deploy mode %q", mode)
	}

	srcInfo, err := os.Stat(srcDir)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeModNotFound, "stat mod folder %s", srcDir)
	}

	if !srcInfo.IsDir() {
		return errors.New(errors.ErrCodeInvalidMod, "%s is not a directory", srcDir)
	}
	// Deploying into the payload folder itself would replace the source entries
	// with links to themselves.
	if domain.PathWithin(srcDir, dstDir) {
		return errors.New(errors.ErrCodeInvalidMod,
			"refusing to deploy %s onto its own source %s", dstDir, srcDir)
	}

	if err := MkdirAll(dstDir); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeConfigIO, "read mod folder %s", srcDir)
	}

	for _, entry := range entries {
		name := entry.Name()
		if manifestpkg.IsName(name) {
			continue
		}

		src := filepath.Join(srcDir, name)

		dst := filepath.Join(dstDir, name)
		if mode == DeploySymlink {
			if err := d.linkEntry(src, dst); err != nil {
				return err
			}

			continue
		}

		if err := d.copyEntry(src, dst); err != nil {
			return err
		}
	}

	return nil
}

func (d *Deployer) linkEntry(src, dst string) error {
	// A dangling link from a previous install still has to go, or os.Symlink
	// fails with EEXIST.
	if err := removeEntry(dst); err != nil {
		return err
	}

	if err := os.Symlink(src, dst); err != nil {
		return errors.Wrap(err, errors.ErrCodeInstallFailed, "link %s to %s", dst, src)
	}

	return nil
}

func (d *Deployer) copyEntry(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeModNotFound, "stat %s", src)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return errors.Wrap(err, errors.ErrCodeConfigIO, "read link %s", src)
		}

		if err := removeEntry(dst); err != nil {
			return err
		}

		if err := os.Symlink(target, dst); err != nil {
			return errors.Wrap(err, errors.ErrCodeInstallFailed, "link %s to %s", dst, target)
		}

		return nil
	}

	if info.IsDir() {
		return CopyTree(src, dst)
	}

	return CopyFile(src, dst)
}

// UndeployEntries removes the named entries from dstDir and returns how many
// were actually removed. Names are validated so a mod can never delete a
// sibling directory, and missing entries are not an error.
//
// The caller passes the rel paths it recorded at install time, because in copy
// mode a deployed file is indistinguishable from a file the user put there.
func (d *Deployer) UndeployEntries(dstDir string, names []string) (int, error) {
	if dstDir == "" {
		return 0, nil
	}

	removed := 0

	for _, name := range names {
		if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			continue
		}

		if manifestpkg.IsName(name) {
			continue
		}

		path := filepath.Join(dstDir, name)
		if !ExistsNoFollow(d.fs, path) {
			continue
		}

		if err := removeEntry(path); err != nil {
			return removed, err
		}

		removed++
	}

	return removed, nil
}

// LinkState describes the condition of one entry in the game mods folder.
type LinkState string

const (
	// LinkMissing means nothing exists at the path.
	LinkMissing LinkState = "missing"
	// LinkNotAManagedLink means a real file or directory sits there, created by
	// the user or by the game. It is never removed automatically.
	LinkNotAManagedLink LinkState = "foreign"
	// LinkOK means the symlink resolves to the expected target.
	LinkOK LinkState = "ok"
	// LinkBroken means the symlink exists but its target does not.
	LinkBroken LinkState = "broken"
	// LinkRetargeted means the symlink points somewhere other than the
	// expected target, which happens after a storage path change.
	LinkRetargeted LinkState = "retargeted"
)

// NeedsRepair reports whether the state should be fixed by re-linking.
func (s LinkState) NeedsRepair() bool {
	return s == LinkBroken || s == LinkRetargeted
}

// LinkStatus inspects a single managed entry. wantTarget is the storage path the
// link is expected to point at; pass "" to skip the target comparison.
//
// The final readlink goes to the real filesystem on purpose: a target can only
// be read from an actual symlink, so an injected FS never reaches that branch.
func LinkStatus(fsys FS, path, wantTarget string) LinkState {
	if fsys == nil {
		fsys = OSFS{}
	}

	info, err := fsys.Lstat(path)
	if err != nil {
		return LinkMissing
	}

	if info.Mode()&os.ModeSymlink == 0 {
		return LinkNotAManagedLink
	}

	if _, err := fsys.Stat(path); err != nil {
		return LinkBroken
	}

	if wantTarget == "" {
		return LinkOK
	}

	target, err := os.Readlink(path)
	if err != nil {
		return LinkBroken
	}

	if domain.StrictPathsEqual(domain.CleanPath(target), domain.CleanPath(wantTarget)) {
		return LinkOK
	}

	return LinkRetargeted
}

// VerifyFolder reports the link state of every managed entry in dstDir along
// with the total entry count, so the CLI can print a symlink report.
func (d *Deployer) VerifyFolder(dstDir string) (map[string]LinkState, int, error) {
	states := map[string]LinkState{}
	if dstDir == "" {
		return states, 0, nil
	}

	entries, err := os.ReadDir(dstDir)
	if err != nil {
		if os.IsNotExist(err) {
			return states, 0, nil
		}

		return nil, 0, errors.Wrap(err, errors.ErrCodeConfigIO, "read game mods folder %s", dstDir)
	}

	total := 0

	for _, entry := range entries {
		if manifestpkg.IsName(entry.Name()) {
			continue
		}

		total++
		states[entry.Name()] = LinkStatus(d.fs, filepath.Join(dstDir, entry.Name()), "")
	}

	return states, total, nil
}

// Repair re-creates every broken or retargeted link in dstDir from srcDir and
// returns the names it fixed.
func (d *Deployer) Repair(srcDir, dstDir string) ([]string, error) {
	if srcDir == "" || dstDir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, errors.Wrap(err, errors.ErrCodeModNotFound, "read mod folder %s", srcDir)
	}

	var fixed []string

	for _, entry := range entries {
		name := entry.Name()
		if manifestpkg.IsName(name) {
			continue
		}

		src := filepath.Join(srcDir, name)
		dst := filepath.Join(dstDir, name)

		state := LinkStatus(d.fs, dst, src)
		if !state.NeedsRepair() {
			continue
		}

		if state == LinkBroken && !Exists(d.fs, src) {
			// The payload is gone too. Re-linking would just recreate a
			// dangling link, so leave it and let the uninstaller report it.
			continue
		}

		if err := d.linkEntry(src, dst); err != nil {
			return fixed, err
		}

		fixed = append(fixed, name)
	}

	return fixed, nil
}

// Owns reports whether the entry named name inside dstDir is a symlink this
// manager created. A foreign real file or directory is never owned.
func (d *Deployer) Owns(dstDir, name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return false
	}

	info, err := d.fs.Lstat(filepath.Join(dstDir, name))
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeSymlink != 0
}

// CanCreateSymlinks probes whether the current process may create symlinks.
// On Unix the answer is always yes; on Windows a temporary link inside the
// working directory is the only reliable check.
func CanCreateSymlinks() bool {
	if !domain.CurrentPlatform().IsWindows() {
		return true
	}

	dir, err := os.MkdirTemp("", "dmm-symlink-*")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		return false
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		return false
	}

	return os.Remove(link) == nil
}

// IsManifestName reports whether name is one of the manager metadata files that
// must never reach the game folder.
//
// It is exported because a service needs the same answer when it builds the file
// inventory it records in the database: the deployer skips manifests while
// linking, and the inventory has to agree with that or the manager will try to
// undeploy a file it never deployed.
//
// The comparison is on the base name only, so "pack/mod.toml" is recognised too.
func IsManifestName(name string) bool { return manifestpkg.IsName(filepath.Base(name)) }

// removeEntry deletes a file, symlink or directory, replacing whatever is
// already at the path.
func removeEntry(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return errors.Wrap(err, errors.ErrCodeUninstallFailed, "remove %s", path)
	}

	return nil
}
