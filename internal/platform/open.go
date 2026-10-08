package platform

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// openers maps a platform to the command that reveals a directory in the
// user's file manager. Each one is the documented, user-visible entry point for
// that desktop; shelling out to a terminal emulator would just add a window.
var openers = map[domain.Platform]struct {
	binary string
	args   []string
}{
	domain.PlatformWindows: {binary: "explorer"},
	domain.PlatformDarwin:  {binary: "open"},
	domain.PlatformLinux:   {binary: "xdg-open"},
}

// CommandRunner starts a helper process. It is injected so the callers that
// reveal a directory can be tested without launching anything.
type CommandRunner func(ctx context.Context, name string, args ...string) error

// execRunner is the production CommandRunner.
func execRunner(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// OpenDirectory reveals dir in the host's file manager.
//
// It is used by `dmm logs open` so a user who was told "the log is in ..." can
// get there in one keystroke instead of copying a path into a file dialog.
func OpenDirectory(ctx context.Context, p domain.Platform, dir string) error {
	return OpenDirectoryWith(ctx, p, dir, OSLookPath, execRunner)
}

// OSLookPath is the real LookPath.
func OSLookPath(file string) (string, error) { return exec.LookPath(file) }

// OpenDirectoryWith is OpenDirectory with the lookup and the process launch
// injected.
func OpenDirectoryWith(
	ctx context.Context,
	p domain.Platform,
	dir string,
	look LookPath,
	run CommandRunner,
) error {
	if look == nil {
		look = OSLookPath
	}

	if run == nil {
		run = execRunner
	}

	dir = domain.CleanPath(strings.TrimSpace(dir))
	if dir == "" {
		return errors.New(errors.ErrCodeInvalidArgument, "no directory to open")
	}

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return errors.Wrap(
			err,
			errors.ErrCodeInvalidArgument,
			"log directory %s does not exist",
			dir,
		)
	}

	opener, ok := openers[p]
	if !ok {
		return errors.New(errors.ErrCodeRunnerNotFound,
			"cannot open a file manager for platform %q", p)
	}

	if _, err := look(opener.binary); err != nil {
		return errors.Wrap(err, errors.ErrCodeRunnerNotFound,
			"%s is not installed: cannot open %s", opener.binary, dir)
	}

	if err := run(ctx, opener.binary, append(opener.args, dir)...); err != nil {
		return errors.Wrap(err, errors.ErrCodeRunnerNotFound,
			"could not open %s with %s", dir, opener.binary)
	}

	return nil
}
