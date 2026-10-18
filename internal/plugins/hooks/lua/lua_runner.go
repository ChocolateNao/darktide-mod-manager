// Package lua runs `lua` hook scripts in a gopher-lua state.
//
// The state is created fresh per execution with SkipOpenLibs: only base, string,
// table and math are opened, and require/module/dofile/loadfile/load are removed
// again, so a script reaches no subprocess, no arbitrary file read and no module
// loader. Cancellation rides on the state's context, which gopher-lua checks on
// every instruction, so `while true do end` still stops at its deadline.
// Everything a script may touch goes through the shared host package, as in the
// JavaScript binding.
package lua

import (
	"context"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	lua "github.com/yuin/gopher-lua"
)

// LogSink receives the console output of a hook script, once per execution and
// after the script ends. Output is reported whether the hook succeeded or failed:
// a failing hook is exactly when a mod author needs the log to work out why.
type LogSink func(lines []string, dropped int)

// Runner executes Lua hook scripts.
type Runner struct {
	sink      LogSink
	providers host.Providers
}

// NewRunner returns the Lua runner with its print output discarded.
func NewRunner(providers host.Providers) *Runner { return &Runner{providers: providers} }

// NewRunnerWithSink returns a runner that forwards print output to sink.
func NewRunnerWithSink(sink LogSink, providers host.Providers) *Runner {
	return &Runner{providers: providers, sink: sink}
}

// Language identifies the scripts this runner handles.
func (r *Runner) Language() domain.ScriptLanguage { return domain.LangLua }

// Run evaluates the script in a fresh state. gopher-lua has no instruction hook,
// so there is no separate step budget: the executor's deadline is enforced by
// the state's context, which the VM consults on every instruction.
func (r *Runner) Run(ctx context.Context, hook services.PreparedHook, mod *domain.Mod) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeHookTimeout,
			"lua hook was interrupted: %v", err)
	}

	source := hook.Script.Content
	if strings.TrimSpace(source) == "" {
		return appErrors.New(appErrors.ErrCodeHookInvalid, "lua hook has no script")
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

	st := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer st.Close()

	if err := openSandboxLibs(st); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"prepare the lua sandbox")
	}

	// raised keeps the Go error behind a permission or availability failure so the
	// run reports the original typed code (HOOK_DENIED, ...), not a generic failure.
	var raised error

	if err := bind(st, h, &raised); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeHookFailed,
			"expose the hook api to the script")
	}

	st.SetContext(ctx)

	runErr := st.DoString(source)

	lines, dropped := h.Logs()
	if r != nil && r.sink != nil {
		r.sink(lines, dropped)
	}

	if runErr == nil {
		return nil
	}

	if raised != nil {
		return raised
	}

	if ctx.Err() != nil {
		return appErrors.Wrap(ctx.Err(), appErrors.ErrCodeHookTimeout,
			"lua hook was interrupted: %v", ctx.Err())
	}

	return appErrors.Wrap(runErr, appErrors.ErrCodeHookFailed,
		"lua hook threw: %v", runErr)
}

// openSandboxLibs opens the only libraries a hook gets, then removes the base
// functions that could reach the filesystem or a module loader. What is left is
// pure computation: strings, tables, math, pcall/error.
func openSandboxLibs(st *lua.LState) error {
	loaders := []struct {
		fn   lua.LGFunction
		name string
	}{
		{fn: lua.OpenBase, name: lua.BaseLibName},
		{fn: lua.OpenString, name: lua.StringLibName},
		{fn: lua.OpenTable, name: lua.TabLibName},
		{fn: lua.OpenMath, name: lua.MathLibName},
	}

	for _, loader := range loaders {
		if err := st.CallByParam(
			lua.P{Fn: st.NewFunction(loader.fn), NRet: 0, Protect: true},
			lua.LString(loader.name),
		); err != nil {
			return err
		}
	}

	for _, name := range []string{"require", "module", "dofile", "loadfile", "load", "loadstring"} {
		st.SetGlobal(name, lua.LNil)
	}

	return nil
}
