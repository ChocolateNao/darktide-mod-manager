// Package hooks wires the concrete hook runners into a services.HookExecutor.
//
// The runners live in sub-packages so that each one owns its concerns (the
// JavaScript VM, the process-spawning shell runner, the in-binary Go registry)
// and can be replaced without touching the executor. This package is the only
// place that knows the full set: a new runner type is added by registering it
// here and in domain.HookType.Valid.
package hooks

import (
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/golang"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/javascript"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/shell"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// errNilExecutor is returned when a caller hands RegisterAll no executor. Wiring
// the runners is mandatory, so a nil container is a programming error.
var errNilExecutor = appErrors.New(appErrors.ErrCodeInvalidArgument,
	"cannot register hook runners on a nil executor")

// RegisterAll installs the built-in runners on an executor.
//
// Registration failures are returned, not swallowed: a missing runner means a
// mod's hook silently does nothing, which is worse than a startup error. A nil
// executor is rejected rather than dereferenced.
func RegisterAll(exec *services.HookExecutor) error {
	if exec == nil {
		return errNilExecutor
	}

	runners := []services.HookRunner{
		javascript.NewRunner(),
		shell.NewRunner(),
		golang.NewRunner(),
	}
	for _, r := range runners {
		if err := exec.Register(r); err != nil {
			return err
		}
	}

	return nil
}

// Types lists the script types RegisterAll installs runners for, in a stable
// order. Tests assert against this so that forgetting a runner is a test
// failure rather than a runtime surprise.
func Types() []domain.HookType {
	return []domain.HookType{
		domain.HookTypeInline,
		domain.HookTypeFile,
		domain.HookTypeCommand,
		domain.HookTypeGo,
	}
}
