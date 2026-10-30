package platform

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

const trashInfoLayout = "2006-01-02T15:04:05"

// TrashAvailable reports whether this host has a trash location the manager can
// move a folder into. Windows uses the recycle bin through PowerShell.
func TrashAvailable() bool {
	p := domain.CurrentPlatform()
	if p.IsWindows() {
		return windowsPowerShellAvailable(exec.LookPath)
	}

	home, err := HomeDir(p, OSEnv())

	return err == nil && strings.TrimSpace(home) != ""
}

// MoveToTrash moves path into the user's trash so it can be restored by the
// desktop's file manager. The folder must exist.
func MoveToTrash(path string) error {
	p := domain.CurrentPlatform()
	if p.IsWindows() {
		return moveToRecycleBin(path, exec.LookPath)
	}

	home, err := HomeDir(p, OSEnv())
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeTrashUnavailable, "locate the home directory")
	}

	filesDir, infoDir := trashDirs(p, home, OSEnv())

	return moveToTrash(path, filesDir, infoDir, time.Now())
}

// windowsPowerShellAvailable reports whether a PowerShell the recycle-bin API
// supports can be started. Windows ships Windows PowerShell, so this is normally
// true; a stripped image can still lack both.
func windowsPowerShellAvailable(look LookPath) bool {
	if look == nil {
		look = exec.LookPath
	}

	if _, err := look("pwsh"); err == nil {
		return true
	}

	_, err := look("powershell")

	return err == nil
}

// moveToRecycleBin uses the .NET file API because Windows exposes no command-line
// equivalent that keeps the item restorable from Explorer.
func moveToRecycleBin(path string, look LookPath) error {
	if !windowsPowerShellAvailable(look) {
		return errors.New(errors.ErrCodeTrashUnavailable,
			"PowerShell is required to move items to the recycle bin")
	}

	info, err := os.Stat(path)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeTrashUnavailable, "trash %q", path)
	}

	out, err := GetShell(domain.PlatformWindows, look).
		Command(windowsTrashScript(path, info.IsDir())).
		CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}

		return errors.Wrap(err, errors.ErrCodeTrashUnavailable,
			"move %q to the recycle bin: %s", path, message)
	}

	return nil
}

// windowsTrashScript renders the PowerShell that sends one path to the recycle
// bin. Single quotes make PowerShell treat the path literally; doubling an inner
// quote is its escape.
func windowsTrashScript(path string, isDir bool) string {
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	method := "DeleteFile"
	if isDir {
		method = "DeleteDirectory"
	}

	return "Add-Type -AssemblyName Microsoft.VisualBasic; " +
		"[Microsoft.VisualBasic.FileIO.FileSystem]::" + method +
		"(" + quoted + ", 'OnlyErrorDialogs', 'SendToRecycleBin')"
}

// trashDirs returns the trash payload and metadata folders for a platform.
// macOS keeps both in ~/.Trash; Linux follows the XDG trash specification.
func trashDirs(p domain.Platform, home string, getenv Env) (string, string) {
	if p.IsDarwin() {
		return filepath.Join(home, ".Trash"), ""
	}

	base := filepath.Join(xdgPath(getenv, "XDG_DATA_HOME", home, ".local", "share"), "Trash")

	return filepath.Join(base, "files"), filepath.Join(base, "info")
}

// moveToTrash is the testable core: it picks a free name, writes the metadata
// first so a crash cannot leave a file with no record, then moves the folder.
func moveToTrash(path, filesDir, infoDir string, now time.Time) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return errors.Wrap(err, errors.ErrCodeTrashUnavailable, "resolve %q", path)
	}

	if _, err := os.Stat(abs); err != nil {
		return errors.Wrap(err, errors.ErrCodeTrashUnavailable, "trash %q", path)
	}

	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return errors.Wrap(err, errors.ErrCodeTrashUnavailable, "create %q", filesDir)
	}

	name, dest := freeTrashName(filesDir, filepath.Base(abs))

	var infoPath string

	if infoDir != "" {
		if err := os.MkdirAll(infoDir, 0o700); err != nil {
			return errors.Wrap(err, errors.ErrCodeTrashUnavailable, "create %q", infoDir)
		}

		infoPath = filepath.Join(infoDir, name+".trashinfo")
		info := fmt.Sprintf(
			"[Trash Info]\nPath=%s\nDeletionDate=%s\n",
			(&url.URL{Path: abs}).EscapedPath(),
			now.Format(trashInfoLayout),
		)
		if err := AtomicWriteFile(infoPath, []byte(info), 0o600); err != nil {
			return errors.Wrap(err, errors.ErrCodeTrashUnavailable, "write trash info")
		}
	}

	if err := movePath(abs, dest); err != nil {
		if infoPath != "" {
			_ = os.Remove(infoPath)
		}

		return errors.Wrap(err, errors.ErrCodeTrashUnavailable, "move %q to trash", path)
	}

	return nil
}

// freeTrashName finds an unused name. The suffix is what lets two mods with the
// same folder name coexist in the trash.
func freeTrashName(dir, base string) (string, string) {
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s.%d", base, i)
		}

		dest := filepath.Join(dir, name)
		if !Exists(RealFS(), dest) {
			return name, dest
		}
	}
}

// movePath renames when it can and copies across filesystems when it cannot,
// which is the normal case for a game installed on a second drive.
func movePath(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		return err
	}

	return os.RemoveAll(src)
}
