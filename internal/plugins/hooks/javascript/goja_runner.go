// Package javascript runs `javascript` hook scripts in a Goja virtual machine.
//
// The VM is created fresh per execution, has no network, filesystem or require,
// and is interrupted when the executor's context expires, so a `while (true) {}`
// cannot outlive its timeout.
package javascript

import (
	"context"
	"errors"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/dop251/goja"
)

// LogSink receives a hook's console output once per execution, after the script
// ends. It is reported whether the hook succeeded or failed: a failing hook is
// exactly when the author needs the log.
type LogSink func(lines []string, dropped int)

type Runner struct {
	sink      LogSink
	providers host.Providers
}

// NewRunner returns the JavaScript runner with its console output discarded.
func NewRunner(providers host.Providers) *Runner { return &Runner{providers: providers} }

func NewRunnerWithSink(sink LogSink, providers host.Providers) *Runner {
	return &Runner{providers: providers, sink: sink}
}

func (r *Runner) Language() domain.ScriptLanguage { return domain.LangJS }

// Run evaluates the script in a fresh VM. The context is enforced with an
// interrupt watcher: Goja only observes an interrupt at a bytecode boundary, so
// a tight loop needs one.
func (r *Runner) Run(ctx context.Context, hook services.PreparedHook, mod *domain.Mod) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		// Wrapped so a caller driving the runner directly gets the same typed
		// error the executor produces for a timed-out hook.
		return appErrors.Wrap(err, appErrors.ErrCodeHookTimeout,
			"javascript hook was interrupted: %v", err)
	}

	source := hook.Script.Content
	if strings.TrimSpace(source) == "" {
		return appErrors.New(appErrors.ErrCodeHookInvalid, "javascript hook has no script")
	}

	var providers host.Providers
	if r != nil {
		providers = r.providers
	}

	h := host.New(host.Options{
		Ctx:         ctx,
		Providers:   providers,
		Permissions: hook.Permissions,
		Context:     hook.Context,
		Env:         hook.Env,
		BaseDir:     hook.WorkingDir,
	})

	vm := goja.New()

	if err := install(vm, h); err != nil {
		return err
	}

	// Interrupt exactly once per execution and clear it afterwards: a leftover
	// interrupt poisons the VM, and clearing it from another goroutine makes Goja
	// panic.
	done := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-done:
		}
	}()

	defer close(done)

	_, runErr := vm.RunString(source)
	vm.ClearInterrupt()

	if r != nil && r.sink != nil {
		lines, dropped := h.Logs()
		r.sink(lines, dropped)
	}

	if runErr == nil {
		return nil
	}

	if ctx.Err() != nil {
		return appErrors.Wrap(ctx.Err(), appErrors.ErrCodeHookTimeout,
			"javascript hook was interrupted: %v", ctx.Err())
	}

	if thrown, ok := errors.AsType[*goja.Exception](runErr); ok {
		return appErrors.Wrap(
			runErr,
			appErrors.ErrCodeHookFailed,
			"javascript hook threw: %s",
			exceptionMessage(thrown),
		)
	}

	return appErrors.Wrap(runErr, appErrors.ErrCodeHookFailed,
		"javascript hook failed: %v", runErr)
}

// wrap builds the error install reports when a binding cannot be exposed: a
// broken binding is an infrastructure fault.
func wrap(err error, format string, args ...any) error {
	return appErrors.Wrap(err, appErrors.ErrCodeHookFailed, format, args...)
}

// exceptionMessage unwraps a thrown JavaScript value into a readable message: a
// hook that throws a string is common, and "[object Object]" would be useless.
func exceptionMessage(exc *goja.Exception) string {
	if v := exc.Value(); v != nil {
		if s := v.String(); s != "" && s != "null" && s != "undefined" {
			return s
		}
	}

	return exc.Error()
}
