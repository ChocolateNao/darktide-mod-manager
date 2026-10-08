package hooks_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wired builds an executor with the built-in runners installed.
func wired(t *testing.T) *services.HookExecutor {
	t.Helper()

	exec := services.NewHookExecutor(services.HookExecutorDeps{})
	err := hooks.RegisterAll(exec)
	require.NoError(t, err, "RegisterAll: %v", err)

	return exec
}

func TestRegisterAllRejectsNilExecutor(t *testing.T) {
	err := hooks.RegisterAll(nil)
	code := appErrors.CodeOf(err)
	require.Equal(
		t,
		appErrors.ErrCodeInvalidArgument,
		code,
		"code = %q, want %q",
		code,
		appErrors.ErrCodeInvalidArgument,
	)

	if err == nil || !strings.Contains(err.Error(), "nil executor") {
		assert.Fail(t, "error should explain the problem: %v", err)
	}
}

func TestRegisterAllInstallsARunnerForEveryAdvertisedType(t *testing.T) {
	// The `file` type is the regression this guards: the shell runner advertises
	// it through MultiHookTyper, and nothing else does, so a change on either
	// side would otherwise leave every file hook unrunnable.
	exec := wired(t)
	for _, typ := range hooks.Types() {
		assert.True(t, exec.HasRunner(typ), "no runner registered for %q", typ)
	}
}

func TestRegisterAllCoversEveryValidHookType(t *testing.T) {
	// domain.HookType is the source of truth for what a manifest may ask for, so
	// a new type added there without a runner must fail here.
	exec := wired(t)
	for _, typ := range domain.HookTypes() {
		assert.True(t, exec.HasRunner(typ), "no runner registered for the valid hook type %q", typ)
	}
}

func TestTypesMatchesDomainHookTypes(t *testing.T) {
	// Types() is a documentation aid; if it drifts from the domain list, either a
	// type is advertised but has no runner or a runner exists but is undocumented.
	got := hooks.Types()

	want := domain.HookTypes()
	require.Len(t, got, len(want), "Types() = %v, want %v", got, want)

	for i := range want {
		assert.Equal(t, want[i], got[i], "Types()[%d] = %q, want %q", i, got[i], want[i])
	}
}

func TestTypesReturnsAFreshSlice(t *testing.T) {
	// A caller mutating the returned slice must not be able to change what the
	// package reports next time.
	first := hooks.Types()
	first[0] = "tampered"

	if hooks.Types()[0] == "tampered" {
		assert.Fail(t, "Types() exposes its backing array")
	}
}

func TestRegisterAllIsIdempotent(t *testing.T) {
	// The DI container may be built more than once in a test process; wiring the
	// same runners twice must not fail.
	exec := services.NewHookExecutor(services.HookExecutorDeps{})
	for i := range 2 {
		err := hooks.RegisterAll(exec)
		require.NoError(t, err, "RegisterAll call %d: %v", i+1, err)
	}

	for _, typ := range hooks.Types() {
		assert.True(t, exec.HasRunner(typ), "no runner registered for %q after two calls", typ)
	}
}

func TestRegisteredInlineHookRuns(t *testing.T) {
	exec := wired(t)

	hooksFor := []domain.HookScript{{Type: domain.HookTypeInline, Content: `1 + 1`}}
	err := exec.Execute(context.Background(), hooksFor, nil)
	require.NoError(t, err, "inline hook: %v", err)
}

func TestRegisteredCommandHookRuns(t *testing.T) {
	exec := wired(t)

	hooksFor := []domain.HookScript{{Type: domain.HookTypeCommand, Content: "exit 0"}}
	err := exec.Execute(context.Background(), hooksFor, nil)
	require.NoError(t, err, "command hook: %v", err)
}

func TestRegisteredFileHookRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a POSIX shebang script is not runnable on Windows")
	}

	dir := t.TempDir()

	script := filepath.Join(dir, "run.sh")
	err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	require.NoError(t, err, "write script: %v", err)

	mod := &domain.Mod{ID: "better_ui", LocalPath: dir}
	exec := wired(t)
	// This is the end-to-end path a manifest takes: the executor resolves the
	// script inside the mod folder, hands the runner a PreparedHook, and the
	// shell runner spawns it.
	err = exec.Execute(context.Background(),
		[]domain.HookScript{{Type: domain.HookTypeFile, Content: "run.sh"}}, mod)
	require.NoError(t, err, "file hook")
}

func TestRegisteredGoHookFailsLoudlyWhenNothingIsCompiledIn(t *testing.T) {
	// No `go` hook ships by default. The manifest must get a clear error rather
	// than a silent no-op, which is the whole point of the registry.
	exec := wired(t)

	err := exec.Execute(context.Background(),
		[]domain.HookScript{{Type: domain.HookTypeGo, Content: "write-install-marker"}}, nil)
	code := appErrors.CodeOf(err)
	require.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		code,
		"code = %q, want %q (err=%v)",
		code,
		appErrors.ErrCodeHookFailed,
		err,
	)
}

func TestExecutorReportsUnknownScriptType(t *testing.T) {
	exec := wired(t)

	err := exec.Execute(context.Background(),
		[]domain.HookScript{{Type: domain.HookType("powershell"), Content: "Write-Host hi"}}, nil)
	code := appErrors.CodeOf(err)
	assert.Equal(
		t,
		appErrors.ErrCodeHookInvalid,
		code,
		"code = %q, want %q (err=%v)",
		code,
		appErrors.ErrCodeHookInvalid,
		err,
	)
}
