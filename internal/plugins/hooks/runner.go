// Package hooks wires the concrete hook runners into a services.HookExecutor.
//
// The runners live in sub-packages so each owns its own concerns (the JavaScript
// VM, the Lua state). This is the only place that knows the full set: a new
// language is added by registering it here and in domain.ScriptLanguage.Valid.
// Both share the host package, so a script sees an identical surface in either.
package hooks

import (
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/javascript"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/lua"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// errNilExecutor is returned when a caller hands RegisterAll no executor. Wiring
// the runners is mandatory, so a nil container is a programming error.
var errNilExecutor = appErrors.New(appErrors.ErrCodeInvalidArgument,
	"cannot register hook runners on a nil executor")

// RegisterAll installs the built-in runners on an executor. providers are the
// injected capabilities both runners hand to their scripts; a runner never
// reaches for anything else. Registration failures are returned, not swallowed.
func RegisterAll(exec *services.HookExecutor, providers host.Providers) error {
	if exec == nil {
		return errNilExecutor
	}

	runners := []services.HookRunner{
		javascript.NewRunner(providers),
		lua.NewRunner(providers),
	}
	for _, r := range runners {
		if err := exec.Register(r); err != nil {
			return err
		}
	}

	return nil
}

// Types lists the script languages RegisterAll installs runners for, in a stable
// order. Tests assert against this so a forgotten runner is a test failure.
func Types() []domain.ScriptLanguage {
	return domain.ScriptLanguages()
}
