// Package javascript runs `javascript` hook scripts in a Goja virtual machine.
//
// The sandbox is what makes a hook safe to run: the VM is created fresh for
// every execution, is given no network primitives, no filesystem primitives and
// no `require`, and is interrupted as soon as the executor's context expires so
// a `while (true) {}` cannot outlive its timeout. Everything a script may touch
// goes through the shared host package, identically to the Lua binding.
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

// LogSink receives the console output of a hook script. It is called once per
// execution, after the script ends, with the lines in order.
//
// Output is reported whether the hook succeeded or failed: a failing hook is
// exactly when a mod author needs the log to work out why.
type LogSink func(lines []string, dropped int)

// Runner executes JavaScript hook scripts.
type Runner struct {
	sink      LogSink
	providers host.Providers
}

// NewRunner returns the JavaScript runner with its console output discarded.
// Mods that log a lot are still bounded by host.MaxLogLines, but nothing is
// retained.
func NewRunner(providers host.Providers) *Runner { return &Runner{providers: providers} }

// NewRunnerWithSink returns a runner that forwards console output to sink.
func NewRunnerWithSink(sink LogSink, providers host.Providers) *Runner {
	return &Runner{providers: providers, sink: sink}
}

// Language identifies the scripts this runner handles.
func (r *Runner) Language() domain.ScriptLanguage { return domain.LangJS }

// Run evaluates the script in a fresh VM.
//
// The context is honoured through an interrupt watcher rather than by checking
// between statements: Goja only observes an interrupt at a bytecode boundary,
// so a tight loop needs one to be interrupted at all.
func (r *Runner) Run(ctx context.Context, hook services.PreparedHook, mod *domain.Mod) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		// Wrapped rather than returned raw so a caller driving the runner directly
		// gets the same typed error the executor would produce for a timed-out
		// hook, instead of a bare context.Canceled it cannot classify.
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

	// Interrupt exactly once per execution and always clear it afterwards: a
	// leftover interrupt would poison the VM, and clearing it from this
	// goroutine would make Goja panic.
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
// broken binding is an infrastructure fault, not something the script did.
func wrap(err error, format string, args ...any) error {
	return appErrors.Wrap(err, appErrors.ErrCodeHookFailed, format, args...)
}

// exceptionMessage unwraps a thrown JavaScript value into a readable message.
// A hook that throws a string is common; reporting "[object Object]" for it
// would be useless to the mod author.
func exceptionMessage(exc *goja.Exception) string {
	if v := exc.Value(); v != nil {
		if s := v.String(); s != "" && s != "null" && s != "undefined" {
			return s
		}
	}

	return exc.Error()
}
