package platform

import (
	"context"
	"net/url"
	"os"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// urlOpeners maps a platform to the command that hands a URL to whatever the
// user has registered for it.
//
// Every desktop has a documented "open this link" entry point, and using it
// rather than a browser by path matters twice over: the user's default browser
// is honoured, and a link whose scheme the manager has never heard of is
// rejected before a process is started rather than after.
var urlOpeners = map[domain.Platform]struct {
	binary string
	args   []string
}{
	domain.PlatformWindows: {binary: "rundll32", args: []string{"url.dll,FileProtocolHandler"}},
	domain.PlatformDarwin:  {binary: "open"},
	domain.PlatformLinux:   {binary: "xdg-open"},
}

// allowedURLSchemes are the schemes the manager is willing to hand to the
// desktop.
//
// This is a denylist by omission and deliberately short. A mod page is an
// http(s) link; a Steam Workshop page is a steam: link; a file on this machine
// is a file: link. Anything else would mean a source plugin, or a mod
// manifest, got to choose what the operating system executes, which is not a
// capability a mod page should have.
var allowedURLSchemes = map[string]bool{
	"http":   true,
	"https":  true,
	"steam":  true,
	"file":   true,
	"mailto": true,
}

// OpenURL hands rawURL to the host's default handler.
//
// It is what "open this mod's page" resolves to: the manager knows where a mod
// came from but never renders it, so the only way to follow a link is to ask
// the desktop to do it.
func OpenURL(ctx context.Context, p domain.Platform, rawURL string) error {
	return OpenURLWith(ctx, p, rawURL, OSLookPath, execRunner)
}

// OpenURLWith is OpenURL with the lookup and the process launch injected.
func OpenURLWith(
	ctx context.Context,
	p domain.Platform,
	rawURL string,
	look LookPath,
	run CommandRunner,
) error {
	if look == nil {
		look = OSLookPath
	}

	if run == nil {
		run = execRunner
	}

	target, err := ValidateURL(rawURL)
	if err != nil {
		return err
	}

	opener, ok := urlOpeners[p]
	if !ok {
		return errors.New(errors.ErrCodeRunnerNotFound,
			"cannot open a browser for platform %q", p)
	}

	if _, err := look(opener.binary); err != nil {
		return errors.Wrap(err, errors.ErrCodeRunnerNotFound,
			"%s is not installed: cannot open the link", opener.binary)
	}

	args := make([]string, 0, len(opener.args)+1)
	args = append(args, opener.args...)
	args = append(args, target)

	if err := run(ctx, opener.binary, args...); err != nil {
		return errors.Wrap(err, errors.ErrCodeRunnerNotFound,
			"could not open the link with %s", opener.binary)
	}

	return nil
}

// ValidateURL checks that rawURL is a link the manager is willing to open and
// returns its canonical form.
//
// It is exported because the same question is asked before a link reaches a
// frontend or a command line: "would this link open something?" should not be
// answered differently by the button that calls OpenURL and the command that
// prints it. file: links are additionally required to name an existing file,
// since a file link that points at nothing opens an error dialog the user did
// not ask for.
func ValidateURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New(errors.ErrCodeInvalidArgument, "no link to open")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", errors.Wrap(err, errors.ErrCodeInvalidArgument,
			"%q is not a link", rawURL)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if !allowedURLSchemes[scheme] {
		return "", errors.New(errors.ErrCodeInvalidArgument,
			"refusing to open a %q link; only %s are allowed",
			scheme, strings.Join(allowedSchemeList(), ", "))
	}

	if scheme == "file" {
		if _, err := os.Stat(parsed.Path); err != nil {
			return "", errors.Wrap(err, errors.ErrCodeNotFound,
				"the file behind %q is not there", rawURL)
		}
	}

	return parsed.String(), nil
}

// allowedSchemeList renders the allowlist for an error message, in a fixed
// order so the message does not change from run to run with map iteration.
func allowedSchemeList() []string {
	return []string{"http", "https", "steam", "file", "mailto"}
}

// CanOpenURL reports whether rawURL would be accepted, without opening it.
//
// The frontend uses it to decide whether to show an "open in browser" control at
// all: a mod from a source that published no page should not offer a button
// that can only fail.
func CanOpenURL(rawURL string) bool {
	_, err := ValidateURL(rawURL)

	return err == nil
}

// URLLauncher adapts OpenURL to domain.ActionOpener's OpenURL method.
//
// It exists so the action runner can offer "open this folder / this page"
// without importing this package, and so the action layer never gains the
// ability to launch something the platform layer has not approved.
type URLLauncher struct {
	Platform domain.Platform
}

// OpenDirectory reveals dir in the host's file manager.
func (l URLLauncher) OpenDirectory(ctx context.Context, dir string) error {
	return OpenDirectory(ctx, l.Platform, dir)
}

// OpenURL hands rawURL to the host's default handler.
func (l URLLauncher) OpenURL(ctx context.Context, rawURL string) error {
	return OpenURL(ctx, l.Platform, rawURL)
}
