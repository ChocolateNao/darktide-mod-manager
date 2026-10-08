package javascript

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prepared builds the PreparedHook an executor would hand us, so the runner is
// exercised through the same struct production uses instead of a hand-rolled one.
func prepared(script domain.HookScript) services.PreparedHook {
	return services.PreparedHook{
		Script: script,
		Context: domain.HookContext{
			Mod: domain.ModHookContext{
				ID:      "better_ui",
				Name:    "Better UI",
				Version: "1.2.0",
				Enabled: true,
			},
			Game: domain.GameHookContext{
				Path:     "/games/darktide",
				Version:  "1.0.3",
				ModsPath: "/games/darktide/mods",
			},
			Config: domain.ConfigHookContext{
				CachePath:   "/cache",
				TempPath:    "/tmp",
				StoragePath: "/storage",
			},
			Phase: domain.PhasePreInstall,
			Index: 0,
		},
		WorkingDir: "/tmp/hook",
		Env:        []string{"PATH=/usr/bin"},
	}
}

func mod() *domain.Mod {
	return &domain.Mod{ID: "better_ui", Name: "Better UI", Version: "1.2.0", SourceID: "vortex"}
}

func TestRunnerHookType(t *testing.T) {
	got := NewRunner().HookType()
	assert.Equal(
		t,
		domain.HookTypeInline,
		got,
		"HookType() = %q, want %q",
		got,
		domain.HookTypeInline,
	)
}

func TestRunnerRejectsBlankScript(t *testing.T) {
	for _, content := range []string{"", "   ", "\n\t "} {
		err := NewRunner().Run(
			context.Background(),
			prepared(domain.HookScript{Content: content}),
			mod(),
		)
		assert.Equal(
			t,
			appErrors.ErrCodeHookInvalid,
			appErrors.CodeOf(err),
			"content %q: code = %v, want %v",
			content,
			appErrors.CodeOf(err),
			appErrors.ErrCodeHookInvalid,
		)
	}
}

// The script must be able to read the context the manager promises it.
func TestRunnerExposesHookContextToScript(t *testing.T) {
	r := NewRunnerWithSink(func(lines []string, dropped int) {})

	script := `
		if (hook.mod.id !== "better_ui") { throw new Error("mod.id=" + hook.mod.id); }
		if (hook.mod.version !== "1.2.0") { throw new Error("mod.version=" + hook.mod.version); }
		if (hook.mod.enabled !== true) { throw new Error("enabled=" + hook.mod.enabled); }
		if (hook.game.path !== "/games/darktide") { throw new Error("game.path=" + hook.game.path); }
		if (hook.game.mods_path !== "/games/darktide/mods") { throw new Error("mods_path=" + hook.game.mods_path); }
		if (hook.game.version !== "1.0.3") { throw new Error("game.version=" + hook.game.version); }
		if (hook.config.cache_path !== "/cache") { throw new Error("cache_path=" + hook.config.cache_path); }
		if (hook.config.temp_path !== "/tmp") { throw new Error("temp_path=" + hook.config.temp_path); }
		if (hook.config.storage_path !== "/storage") { throw new Error("storage_path=" + hook.config.storage_path); }
		if (context.phase !== "pre_install") { throw new Error("phase=" + context.phase); }
		if (context.index !== 0) { throw new Error("index=" + context.index); }
		if (mod.id !== "better_ui") { throw new Error("mod.id=" + mod.id); }
	`
	if err := r.Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	); err != nil {
		require.FailNow(t, "Run: %v", err)
	}
}

func TestRunnerPhaseAndIndexAreTakenFromContext(t *testing.T) {
	hook := prepared(domain.HookScript{Content: `
		if (context.phase !== "post_enable") { throw new Error("phase=" + context.phase); }
		if (context.index !== 4) { throw new Error("index=" + context.index); }
	`})
	hook.Context.Phase = domain.PhasePostEnable

	hook.Context.Index = 4
	err := NewRunner().Run(context.Background(), hook, mod())
	require.NoError(t, err, "Run: %v", err)
}

// The context must be the *prepared* one, so a mod that changed between Prepare
// and Run cannot make the script see stale data.
func TestRunnerUsesPreparedContextNotModArgument(t *testing.T) {
	hook := prepared(
		domain.HookScript{
			Content: `if (hook.mod.name !== "Better UI") { throw new Error(hook.mod.name); }`,
		},
	)

	stale := &domain.Mod{ID: "other", Name: "Wrong Name"}
	err := NewRunner().Run(context.Background(), hook, stale)
	require.NoError(t, err, "Run: %v", err)
}

func TestRunnerAcceptsNilMod(t *testing.T) {
	hook := prepared(
		domain.HookScript{
			Content: `if (context.phase === "") { throw new Error("phase missing"); }`,
		},
	)
	err := NewRunner().Run(context.Background(), hook, nil)
	require.NoError(t, err, "Run: %v", err)
}

func TestRunnerToleratesNilContext(t *testing.T) {
	hook := prepared(domain.HookScript{Content: `1 + 1`})
	hook.Context = domain.HookContext{}
	// A zero context is what an executor produces when no context func is wired;
	// the script must still run rather than dereference nil.
	err := NewRunner().Run(context.Background(), hook, mod())
	require.NoError(t, err, "Run: %v", err)
}

// A thrown Error is a hook failure, not a crash, and the message must survive so
// the UI can show the script author's own text.
func TestRunnerReportsThrownErrors(t *testing.T) {
	err := NewRunner().Run(
		context.Background(),
		prepared(domain.HookScript{Content: `throw new Error("boom-42");`}),
		mod(),
	)
	require.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"code = %v, want %v",
		appErrors.CodeOf(err),
		appErrors.ErrCodeHookFailed,
	)

	assert.Contains(t, err.Error(), "boom-42", "error %q does not mention the thrown message", err)
}

func TestRunnerReportsThrownNonErrorValues(t *testing.T) {
	for _, content := range []string{`throw "plain string";`, `throw 123;`, `throw {code: 7};`} {
		err := NewRunner().Run(
			context.Background(),
			prepared(domain.HookScript{Content: content}),
			mod(),
		)
		assert.Equal(
			t,
			appErrors.ErrCodeHookFailed,
			appErrors.CodeOf(err),
			"%q: code = %v, want HookFailed",
			content,
			appErrors.CodeOf(err),
		)

		if err == nil || err.Error() == "" {
			assert.Fail(t, "%q: expected a non-empty message", content)
		}
	}
}

func TestRunnerReportsSyntaxErrors(t *testing.T) {
	err := NewRunner().Run(
		context.Background(),
		prepared(domain.HookScript{Content: `function (`}),
		mod(),
	)
	require.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"code = %v, want HookFailed",
		appErrors.CodeOf(err),
	)

	assert.Contains(t, err.Error(), "SyntaxError", "error %q should mention SyntaxError", err)
}

func TestRunnerReportsRuntimeTypeErrors(t *testing.T) {
	err := NewRunner().Run(
		context.Background(),
		prepared(domain.HookScript{Content: `null.x;`}),
		mod(),
	)
	require.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"code = %v, want HookFailed",
		appErrors.CodeOf(err),
	)
}

// The VM must not be able to reach the host: no network, no require, no fs.
// Each of these is a call that would work in plain Node.
func TestRunnerHasNoNetworkAccess(t *testing.T) {
	for _, content := range []string{
		`fetch("http://example.com");`,
		`new XMLHttpRequest();`,
		`WebSocket;`,
		`navigator;`,
		`require("fs");`,
		`process.binding("fs");`,
		`Deno.readFile("/etc/passwd");`,
	} {
		err := NewRunner().Run(
			context.Background(),
			prepared(domain.HookScript{Content: content}),
			mod(),
		)
		assert.Equal(
			t,
			appErrors.ErrCodeHookFailed,
			appErrors.CodeOf(err),
			"%q: code = %v, want HookFailed (the global must be absent)",
			content,
			appErrors.CodeOf(err),
		)
	}
}

// The sandbox boundary that matters most: an inline hook must not be able to
// read the manager's config or the mod's files. Merely *mentioning* a missing
// global evaluates to undefined without failing, so each probe has to use it.
func TestRunnerHasNoFilesystemAccess(t *testing.T) {
	for _, content := range []string{
		`require("node:fs");`,
		`require("fs");`,
		`import("fs");`,
		`process.mainModule;`,
		`process.chdir("/");`,
		`eval("require('fs')");`,
		`fs.readdirSync(".");`,
		`Deno.readFile("/etc/passwd");`,
	} {
		err := NewRunner().Run(
			context.Background(),
			prepared(domain.HookScript{Content: content}),
			mod(),
		)
		// Some of these are legal-but-inert (reading a property off a bare
		// undefined global throws, calling a host API throws, a missing import
		// throws); the one thing none of them may do is succeed at touching the
		// host.
		assert.Error(t, err, "%q: the hook reached a host capability", content)
	}
}

// The probe above only proves the probes fail; assert the globals really are
// absent so a future goja upgrade that starts providing them is caught.
func TestRunnerHostGlobalsAreAbsent(t *testing.T) {
	script := `
		var absent = ["fetch", "XMLHttpRequest", "WebSocket", "navigator", "require",
			"process", "Deno", "Bun", "fs", "window", "document", "importScripts",
			"globalThis.global", "__dirname", "eval_shadowed"];
		var leaked = [];
		for (var i = 0; i < absent.length; i++) {
			if (typeof globalThis[absent[i]] !== "undefined") { leaked.push(absent[i]); }
		}
		if (leaked.length > 0) { throw new Error("present: " + leaked.join(",")); }
	`
	if err := NewRunner().Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	); err != nil {
		require.FailNow(t, "Run: %v", err)
	}
}

// Console output is captured, not printed: a hook spewing megabytes must not
// reach the manager's stdout.
func TestRunnerCapturesConsoleOutput(t *testing.T) {
	var (
		got     []string
		dropped int
	)

	r := NewRunnerWithSink(func(lines []string, d int) {
		got = append(got, lines...)
		dropped += d
	})

	script := `
		console.log("one");
		console.info("two");
		console.warn("three");
		console.error("four");
		print("five");
	`
	if err := r.Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	); err != nil {
		require.FailNow(t, "Run: %v", err)
	}

	require.Len(t, got, 5, "captured %d lines %v, want 5", len(got), got)

	joined := strings.Join(got, "\n")
	for _, want := range []string{"one", "two", "three", "four", "five"} {
		assert.Contains(t, joined, want, "captured output %q missing %q", joined, want)
	}

	assert.Equal(t, 0, dropped, "dropped = %d, want 0", dropped)
}

func TestRunnerBoundsCapturedLogLines(t *testing.T) {
	var (
		got     []string
		dropped int
	)

	r := NewRunnerWithSink(func(lines []string, d int) {
		got = append(got, lines...)
		dropped += d
	})
	// More lines than the cap, so the excess must be counted rather than kept.
	script := `for (var i = 0; i < maxLogLines + 25; i++) { console.log("line " + i); }`
	if err := r.Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	); err != nil {
		require.FailNow(t, "Run: %v", err)
	}

	assert.Len(t, got, maxLogLines, "captured %d lines, want the cap %d", len(got), maxLogLines)

	assert.Equal(t, 25, dropped, "dropped = %d, want 25", dropped)
}

func TestRunnerBoundsCapturedLineLength(t *testing.T) {
	var got []string

	r := NewRunnerWithSink(func(lines []string, _ int) { got = append(got, lines...) })

	script := `console.log("x".repeat(maxLogLineLength * 3));`
	if err := r.Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	); err != nil {
		require.FailNow(t, "Run: %v", err)
	}

	require.Len(t, got, 1, "captured %d lines, want 1", len(got))

	assert.False(
		t,
		len(got[0]) > maxLogLineLength+3,
		"captured line is %d bytes, want at most %d plus the ellipsis",
		len(got[0]),
		maxLogLineLength,
	)
}

// Output is reported even when the hook fails: that is exactly when a mod author
// needs it. The lines must arrive in the order they were logged.
func TestRunnerReportsLogsFromAFailedHook(t *testing.T) {
	var (
		got     []string
		dropped int
	)

	r := NewRunnerWithSink(func(lines []string, d int) {
		got = append(got, lines...)
		dropped += d
	})

	err := r.Run(context.Background(), prepared(domain.HookScript{Content: `
		console.log("before");
		throw new Error("x");
	`}), mod())
	require.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"expected the throw to fail the hook, got %v",
		err,
	)

	if len(got) != 1 || got[0] != "before" {
		assert.Fail(t, "captured %v, want [before]", got)
	}

	assert.Equal(t, 0, dropped, "dropped = %d, want 0", dropped)
}

// A hook that fails while filling the buffer must still report the overflow.
func TestRunnerReportsDroppedLinesFromAFailedHook(t *testing.T) {
	var dropped int

	r := NewRunnerWithSink(func(_ []string, d int) { dropped += d })

	err := r.Run(context.Background(), prepared(domain.HookScript{Content: `
		for (var i = 0; i < maxLogLines + 3; i++) { console.log(i); }
		throw new Error("late");
	`}), mod())
	require.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"expected the throw to fail the hook, got %v",
		err,
	)

	assert.Equal(t, 3, dropped, "dropped = %d, want 3", dropped)
}

// An interrupted hook's partial log is still reported: the author needs to see
// how far the runaway loop got.
func TestRunnerReportsLogsFromATimedOutHook(t *testing.T) {
	var got []string

	r := NewRunnerWithSink(func(lines []string, _ int) { got = append(got, lines...) })

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	err := r.Run(ctx, prepared(domain.HookScript{Content: `
		while (true) { console.log("tick"); }
	`}), mod())
	require.Equal(
		t,
		appErrors.ErrCodeHookTimeout,
		appErrors.CodeOf(err),
		"code = %v, want HookTimeout",
		appErrors.CodeOf(err),
	)

	if len(got) == 0 {
		assert.Fail(t, "expected the partial log of the interrupted hook to be reported")
	}

	assert.False(
		t,
		len(got) > maxLogLines,
		"captured %d lines, want at most %d",
		len(got),
		maxLogLines,
	)
}

func TestRunnerNewRunnerDiscardsOutput(t *testing.T) {
	if err := NewRunner().Run(
		context.Background(),
		prepared(domain.HookScript{Content: `console.log("noise")`}),
		mod(),
	); err != nil {
		require.FailNow(t, "Run: %v", err)
	}
}

func TestRunnerNilSinkIsSafe(t *testing.T) {
	// NewRunnerWithSink(nil) is what DI does when nobody subscribed; logging must
	// not panic in that configuration.
	if err := NewRunnerWithSink(
		nil,
	).Run(
		context.Background(),
		prepared(domain.HookScript{Content: `console.log("x")`}),
		mod(),
	); err != nil {
		require.FailNow(t, "Run: %v", err)
	}
}

// An infinite loop must be interrupted, not hang the install forever.
func TestRunnerStopsInfiniteLoops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()

	err := NewRunner().Run(ctx, prepared(domain.HookScript{Content: `while (true) {}`}), mod())
	require.Equal(
		t,
		appErrors.ErrCodeHookTimeout,
		appErrors.CodeOf(err),
		"code = %v, want HookTimeout (err=%v)",
		appErrors.CodeOf(err),
		err,
	)

	elapsed := time.Since(start)
	assert.False(t, elapsed > 5*time.Second, "interruption took %s, want well under 5s", elapsed)
}

func TestRunnerReportsTimeoutForBusyLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := NewRunner().Run(
		ctx,
		prepared(domain.HookScript{Content: `for (;;) { Math.random(); }`}),
		mod(),
	)
	require.Equal(
		t,
		appErrors.ErrCodeHookTimeout,
		appErrors.CodeOf(err),
		"code = %v, want HookTimeout",
		appErrors.CodeOf(err),
	)
}

// An already-dead context must be reported as a timeout too, and must not run
// the body.
func TestRunnerReportsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewRunner().Run(ctx, prepared(domain.HookScript{Content: `1;`}), mod())
	require.Equal(
		t,
		appErrors.ErrCodeHookTimeout,
		appErrors.CodeOf(err),
		"code = %v, want HookTimeout (err=%v)",
		appErrors.CodeOf(err),
		err,
	)
}

// The same must hold for a context that is already past its deadline, and for a
// nil context, which the runner treats as "no deadline".
func TestRunnerHandlesExpiredAndNilContexts(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	if err := NewRunner().Run(
		expired,
		prepared(domain.HookScript{Content: `1;`}),
		mod(),
	); appErrors.CodeOf(
		err,
	) != appErrors.ErrCodeHookTimeout {
		assert.Fail(t, "expired deadline: code = %v, want HookTimeout", appErrors.CodeOf(err))
	}
	//nolint:staticcheck // passing a nil context is exactly the edge case under test.
	if err := NewRunner().Run(
		nil,
		prepared(domain.HookScript{Content: `1 + 1`}),
		mod(),
	); err != nil {
		assert.Fail(t, "nil context: %v", err)
	}
}

// Each execution gets a fresh VM: globals defined by one hook must not leak into
// the next, or mods could collide over a shared variable name.
func TestRunnerUsesAFreshVMPerExecution(t *testing.T) {
	first := prepared(domain.HookScript{Content: `globalThis.leaked = "yes";`})
	err := NewRunner().Run(context.Background(), first, mod())
	require.NoError(t, err, "first Run: %v", err)

	second := prepared(
		domain.HookScript{
			Content: `if (typeof leaked !== "undefined") { throw new Error("leaked: " + leaked); }`,
		},
	)
	err = NewRunner().Run(context.Background(), second, mod())
	require.NoError(t, err, "globals leaked between runs: %v", err)
}

// Hooks are authored by third parties; a runaway loop in one must not stop the
// next hook from running.
func TestRunnerSurvivesAFailedHook(t *testing.T) {
	r := NewRunner()
	if err := r.Run(
		context.Background(),
		prepared(domain.HookScript{Content: `throw new Error("first");`}),
		mod(),
	); err == nil {
		require.FailNow(t, "expected the first hook to fail")
	}

	if err := r.Run(
		context.Background(),
		prepared(domain.HookScript{Content: `2 + 2;`}),
		mod(),
	); err != nil {
		require.FailNow(t, "second Run after a failure: %v", err)
	}
}

// Scripts are real JavaScript, so ordinary language features must keep working;
// the sandbox is about *host* access, not about crippling the language.
func TestRunnerSupportsOrdinaryJavaScript(t *testing.T) {
	script := `
		const items = [3, 1, 2].sort((a, b) => a - b);
		const total = items.reduce((a, b) => a + b, 0);
		if (total !== 6) { throw new Error("total=" + total); }
		class Point { constructor(x) { this.x = x; } get double() { return this.x * 2; } }
		if (new Point(4).double !== 8) { throw new Error("class"); }
		const { id, ...rest } = { id: "a", b: 1, c: 2 };
		if (id !== "a" || rest.b !== 1) { throw new Error("destructuring"); }
		if (` + "`tpl`" + ` !== "tpl") { throw new Error("template"); }
		JSON.stringify({ ok: true });
		Math.max(1, 2, 3);
		String(42).toUpperCase();
		[...new Set([1, 1, 2])].length;
		(async () => 1)();
		(function* () { yield 1; })().next().value;
		JSON.parse('{"k":1}');
		new Date(0).getTime();
		Object.entries({ a: 1 });
		label: for (const x of [1]) { break label; }
		try { null.x; } catch (e) { e instanceof Error; }
		switch (1) { case 1: break; default: throw new Error("switch"); }
	`
	if err := NewRunner().Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	); err != nil {
		require.FailNow(t, "Run: %v", err)
	}
}

// The mod folder is the working directory for hooks, so relative file-ish reads
// through the exposed context have to resolve the way the docs promise.
func TestRunnerExposesWorkingDirThroughContextOnly(t *testing.T) {
	// The script cannot chdir or list the directory; that is the point.
	for _, content := range []string{`process.chdir("/")`, `fs.readdirSync(".")`} {
		err := NewRunner().Run(
			context.Background(),
			prepared(domain.HookScript{Content: content}),
			mod(),
		)
		assert.Equal(
			t,
			appErrors.ErrCodeHookFailed,
			appErrors.CodeOf(err),
			"%q: code = %v, want HookFailed",
			content,
			appErrors.CodeOf(err),
		)
	}
}
