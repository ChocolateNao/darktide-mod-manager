package hooks_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
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
	err := hooks.RegisterAll(exec, host.Providers{})
	require.NoError(t, err, "RegisterAll: %v", err)

	return exec
}

func TestRegisterAllRejectsNilExecutor(t *testing.T) {
	err := hooks.RegisterAll(nil, host.Providers{})
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

func TestRegisterAllInstallsARunnerForEveryAdvertisedLanguage(t *testing.T) {
	// A language advertised by hooks.Types() but missing a runner would leave
	// every manifest asking for it unrunnable.
	exec := wired(t)
	for _, lang := range hooks.Types() {
		assert.True(t, exec.HasRunner(lang), "no runner registered for %q", lang)
	}
}

func TestRegisterAllCoversEveryValidScriptLanguage(t *testing.T) {
	// domain.ScriptLanguage is the source of truth for what a manifest may ask
	// for, so a new language added there without a runner must fail here.
	exec := wired(t)
	for _, lang := range domain.ScriptLanguages() {
		assert.True(t, exec.HasRunner(lang), "no runner registered for the valid language %q", lang)
	}
}

func TestTypesMatchesDomainScriptLanguages(t *testing.T) {
	// Types() is a documentation aid: if it drifts from the domain list, either a
	// language is advertised but has no runner or a runner is undocumented.
	got := hooks.Types()

	want := domain.ScriptLanguages()
	require.Len(t, got, len(want), "Types() = %v, want %v", got, want)

	for i := range want {
		assert.Equal(t, want[i], got[i], "Types()[%d] = %q, want %q", i, got[i], want[i])
	}
}

func TestTypesReturnsAFreshSlice(t *testing.T) {
	// A caller mutating the returned slice must not be able to change what the
	// package reports next time.
	first := hooks.Types()
	first[0] = domain.ScriptLanguage("tampered")

	if hooks.Types()[0] == "tampered" {
		assert.Fail(t, "Types() exposes its backing array")
	}
}

func TestRegisterAllIsIdempotent(t *testing.T) {
	// The DI container may be built more than once in a test process; wiring the
	// same runners twice must not fail.
	exec := services.NewHookExecutor(services.HookExecutorDeps{})
	for i := range 2 {
		err := hooks.RegisterAll(exec, host.Providers{})
		require.NoError(t, err, "RegisterAll call %d: %v", i+1, err)
	}

	for _, lang := range hooks.Types() {
		assert.True(t, exec.HasRunner(lang), "no runner registered for %q after two calls", lang)
	}
}

func TestRegisteredJavascriptHookRuns(t *testing.T) {
	exec := wired(t)

	hooksFor := []domain.HookScript{{Language: domain.LangJS, Content: `1 + 1`}}
	err := exec.Execute(context.Background(), hooksFor, nil)
	require.NoError(t, err, "javascript hook: %v", err)
}

func TestRegisteredLuaHookRuns(t *testing.T) {
	exec := wired(t)

	hooksFor := []domain.HookScript{{Language: domain.LangLua, Content: "local x = 1 + 1"}}
	err := exec.Execute(context.Background(), hooksFor, nil)
	require.NoError(t, err, "lua hook: %v", err)
}

func TestExecutorReportsUnknownScriptLanguage(t *testing.T) {
	exec := wired(t)

	err := exec.Execute(context.Background(),
		[]domain.HookScript{{Language: "powershell", Content: "Write-Host hi"}}, nil)
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
