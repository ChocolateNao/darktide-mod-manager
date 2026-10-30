package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// DefaultPathRequest is the start argument that makes a chooser report a reset
// and skip the dialog. "" already means "use the platform default" in every path
// field of domain.Config, so returning it for both a reset and a cancel would
// leave a form unable to tell "clear this field" from "never mind", and clearing
// a configured path is not recoverable. The sentinel carries a NUL byte, which no
// filesystem accepts in a path.
const DefaultPathRequest = "\x00"

// PathCommands is the frontend's route to the native filesystem choosers, the
// only surface in the package that touches the window runtime: a chooser is an
// interaction with the shell.
type PathCommands struct {
	*base
}

func NewPathCommands(deps Deps) *PathCommands {
	return &PathCommands{base: newBase(deps)}
}

func newPathCommands(b *base) *PathCommands {
	return &PathCommands{base: b}
}

// ChooseDirectory opens the platform's folder picker and returns what the user
// picked. The three results are distinct on purpose, because a form reacts
// differently to each: a path replaces the field, cleared means the field goes
// back to the platform default, and ("", false, nil) means the user dismissed the
// dialog and the field stays as it was. start is validated because the runtime
// fails outright on a DefaultDirectory that does not exist.
func (p *PathCommands) ChooseDirectory(start string) (string, bool, error) {
	if p == nil {
		return "", false, errUninitialised()
	}

	if start == DefaultPathRequest {
		return "", true, nil
	}

	ctx, cancel, err := p.windowContext()
	if err != nil {
		return "", false, err
	}
	defer cancel()

	chosen, err := wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:            "Choose a folder",
		DefaultDirectory: usableDir(start),
	})
	if err != nil {
		return "", false, p.fail(err, "choose a folder")
	}

	// An empty result is a dismissal on every platform Wails supports, told apart
	// from the reset above.
	return strings.TrimSpace(chosen), false, nil
}

// ChooseFile opens the platform's file picker and returns what the user picked.
// patterns are the filter entries in the Wails form ("*.toml"), so the caller
// controls what the picker shows. A dismissal is reported the same way
// ChooseDirectory reports one.
func (p *PathCommands) ChooseFile(start string, patterns []string) (string, error) {
	if p == nil {
		return "", errUninitialised()
	}

	ctx, cancel, err := p.windowContext()
	if err != nil {
		return "", err
	}
	defer cancel()

	filters := make([]wailsruntime.FileFilter, 0, len(patterns))

	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		filters = append(filters, wailsruntime.FileFilter{DisplayName: pattern, Pattern: pattern})
	}

	chosen, err := wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:            "Choose a file",
		DefaultDirectory: usableDir(start),
		Filters:          filters,
	})
	if err != nil {
		return "", p.fail(err, "choose a file")
	}

	return strings.TrimSpace(chosen), nil
}

// RevealDirectory opens the OS file manager on a folder the manager knows about.
// It is a sibling of ChooseDirectory: the game and log folders need "show me
// this", which a picker cannot answer. The opener is the platform's file manager,
// since Wails routes `runtime.BrowserOpenURL` through a URL validator that rejects
// the `file` scheme — so a `file://` URL never opens anything.
func (p *PathCommands) RevealDirectory(path string) error {
	if p == nil {
		return errUninitialised()
	}

	target := strings.TrimSpace(path)
	if target == "" {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "a folder is required")
	}

	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return appErrors.New(appErrors.ErrCodeNotFound, "folder %q does not exist", target)
	}

	ctx, cancel, err := p.windowContext()
	if err != nil {
		return err
	}
	defer cancel()

	if err := platform.OpenDirectory(ctx, domain.CurrentPlatform(), target); err != nil {
		return p.fail(err, "reveal folder %q", target)
	}

	return nil
}

// windowContext returns the context the Wails runtime functions need: the shared
// scope, because the runtime looks up the frontend in the context's values and
// only the window's context carries it. A chooser that inherited the 30-minute
// command timeout could be cancelled out from under the user.
func (p *PathCommands) windowContext() (ctx context.Context, cancel func(), err error) {
	if p.Stopped() {
		return nil, nil, errShutDown()
	}

	return p.Context(), func() {}, nil
}

// usableDir narrows a suggested start directory to one the runtime will accept.
// The runtime refuses a DefaultDirectory that does not exist, so a path from a
// hand-edited config or an unmounted drive falls back to the platform default.
func usableDir(start string) string {
	start = strings.TrimSpace(start)
	if start == "" || start == DefaultPathRequest {
		return ""
	}

	// A path can name a file — a user picks a config file and reuses it as the
	// start for a folder — so prefer the parent.
	if info, err := os.Stat(start); err != nil || !info.IsDir() {
		if parent := filepath.Dir(start); parent != start && dirExists(parent) {
			return parent
		}

		return ""
	}

	return start
}

func dirExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}
