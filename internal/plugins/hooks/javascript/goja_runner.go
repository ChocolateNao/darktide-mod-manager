// Package javascript runs `inline` hook scripts in a Goja virtual machine.
//
// The sandbox is what makes an inline hook safe to run: the VM is created fresh
// for every execution, is given no network primitives, no filesystem primitives
// and no `require`, and is interrupted as soon as the executor's context expires
// so a `while (true) {}` cannot outlive its timeout.
package javascript

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/dop251/goja"
)

const (
	// maxLogLines bounds what a hook can make the manager retain. A script in a
	// loop that logs on every pass would otherwise fill memory through the only
	// channel it has.
	maxLogLines = 200
	// maxLogLineLength bounds a single captured log line.
	maxLogLineLength = 4096
)

// LogSink receives the console output of an inline hook. It is called once per
// execution, after the script ends, with the lines in order.
//
// Output is reported whether the hook succeeded or failed: a failing hook is
// exactly when a mod author needs the log to work out why.
type LogSink func(lines []string, dropped int)

// Runner executes inline JavaScript.
type Runner struct {
	sink LogSink
}

// NewRunner returns the inline JavaScript runner with its console output
// discarded. Mods that log a lot are still bounded by maxLogLines, but nothing
// is retained.
func NewRunner() *Runner { return &Runner{} }

// NewRunnerWithSink returns a runner that forwards console output to sink.
func NewRunnerWithSink(sink LogSink) *Runner { return &Runner{sink: sink} }

// HookType identifies the scripts this runner handles.
func (r *Runner) HookType() domain.HookType { return domain.HookTypeInline }

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
			"inline hook was interrupted: %v", err)
	}

	source := hook.Script.Content
	if strings.TrimSpace(source) == "" {
		return appErrors.New(appErrors.ErrCodeHookInvalid, "inline hook has no script")
	}

	vm := goja.New()

	logs, err := install(vm, hook)
	if err != nil {
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
		r.sink(logs.Lines(), logs.Dropped())
	}

	if runErr == nil {
		return nil
	}

	if ctx.Err() != nil {
		return appErrors.Wrap(ctx.Err(), appErrors.ErrCodeHookTimeout,
			"inline hook was interrupted: %v", ctx.Err())
	}

	if thrown, ok := errors.AsType[*goja.Exception](runErr); ok {
		return appErrors.Wrap(
			runErr,
			appErrors.ErrCodeHookFailed,
			"inline hook threw: %s",
			exceptionMessage(thrown),
		)
	}

	return appErrors.Wrap(runErr, appErrors.ErrCodeHookFailed, "inline hook failed: %v", runErr)
}

// install exposes the hook context and a console to the script. Nothing else is
// added: an inline hook gets no require, no fetch, no filesystem.
//
// The context is converted through JSON rather than handed over as a Go struct:
// Goja exposes a struct's *methods* to JavaScript and none of its fields, so a
// script would see `hook.mod` as undefined. Marshalling also makes the JSON tags
// on domain.HookContext the single definition of the script-facing shape, so the
// documentation cannot drift away from what a hook actually sees.
func install(vm *goja.Runtime, hook services.PreparedHook) (*logBuffer, error) {
	raw, err := json.Marshal(hook.Context)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeHookInvalid, "encode the hook context")
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeHookInvalid, "decode the hook context")
	}

	sets := []struct {
		value any
		name  string
	}{
		{decoded, "context"},
		// "hook" is the friendlier spelling used by the documentation; both name
		// the same object so a script can use whichever it knows.
		{decoded, "hook"},
		{string(hook.Context.Phase), "phase"},
		{decoded["mod"], "mod"},
		// The log caps are visible to the script on purpose: a mod author who
		// wants to batch their logging can ask how much of it will survive
		// instead of discovering it by losing output.
		{maxLogLines, "maxLogLines"},
		{maxLogLineLength, "maxLogLineLength"},
	}
	for _, s := range sets {
		if err := vm.Set(s.name, vm.ToValue(s.value)); err != nil {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeHookInvalid,
				"expose %s to the script",
				s.name,
			)
		}
	}

	logs := &logBuffer{}

	console := vm.NewObject()
	for _, name := range []string{"log", "warn", "error", "info"} {
		if err := console.Set(name, logs.record); err != nil {
			return nil, appErrors.Wrap(err, appErrors.ErrCodeHookInvalid, "expose console.%s", name)
		}
	}

	if err := vm.Set("console", console); err != nil {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeHookInvalid,
			"expose console to the script",
		)
	}

	if err := vm.Set("print", logs.record); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeHookInvalid, "expose print to the script")
	}

	return logs, nil
}

// logBuffer accumulates the console output of one inline hook.
type logBuffer struct {
	lines   []string
	dropped int
	mu      sync.Mutex
}

func (l *logBuffer) record(call goja.FunctionCall) goja.Value {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.lines) >= maxLogLines {
		l.dropped++
		return goja.Undefined()
	}

	parts := make([]string, 0, len(call.Arguments))
	for _, arg := range call.Arguments {
		parts = append(parts, arg.String())
	}

	line := strings.Join(parts, " ")
	if len(line) > maxLogLineLength {
		line = line[:maxLogLineLength] + "…"
	}

	l.lines = append(l.lines, line)

	return goja.Undefined()
}

// Lines returns a copy of the captured output.
func (l *logBuffer) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string{}, l.lines...)
}

// Dropped returns how many log lines were discarded after the buffer filled.
func (l *logBuffer) Dropped() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.dropped
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
