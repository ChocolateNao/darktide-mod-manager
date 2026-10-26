package lua

import (
	"context"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestRunnerLanguage(t *testing.T) {
	got := NewRunner(host.Providers{}).Language()
	assert.Equal(t, domain.LangLua, got, "Language() = %q, want lua", got)
}

func TestRunnerRejectsBlankScript(t *testing.T) {
	for _, content := range []string{"", "   ", "\n\t "} {
		err := NewRunner(host.Providers{}).Run(
			context.Background(),
			prepared(domain.HookScript{Content: content}),
			mod(),
		)
		assert.Equal(t, appErrors.ErrCodeHookInvalid, appErrors.CodeOf(err),
			"content %q: code = %v, want HookInvalid", content, appErrors.CodeOf(err))
	}
}

func TestRunnerExposesHookContextToScript(t *testing.T) {
	script := `
		local c = dmm.context
		if c.mod.id ~= "better_ui" then error("mod.id=" .. tostring(c.mod.id)) end
		if c.mod.version ~= "1.2.0" then error("version") end
		if c.game.path ~= "/games/darktide" then error("game.path") end
		if c.phase ~= "pre_install" then error("phase=" .. tostring(c.phase)) end
		if c.index ~= 0 then error("index") end
	`
	err := NewRunner(host.Providers{}).Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	)
	require.NoError(t, err, "Run: %v", err)
}

// The dangerous globals must not exist at all.
func TestRunnerSandboxHidesDangerousGlobals(t *testing.T) {
	script := `
		local banned = {"require", "module", "dofile", "loadfile", "load", "loadstring",
			"os", "io", "package", "debug"}
		local present = {}
		for _, name in ipairs(banned) do
			if _G[name] ~= nil then present[#present + 1] = name end
		end
		if #present > 0 then error("present: " .. table.concat(present, ",")) end
	`
	err := NewRunner(host.Providers{}).Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	)
	require.NoError(t, err, "Run: %v", err)
}

func TestRunnerReportsLuaErrors(t *testing.T) {
	err := NewRunner(host.Providers{}).Run(
		context.Background(),
		prepared(domain.HookScript{Content: `error("boom-42")`}),
		mod(),
	)
	require.Equal(t, appErrors.ErrCodeHookFailed, appErrors.CodeOf(err),
		"code = %v, want HookFailed", appErrors.CodeOf(err))
	assert.Contains(t, err.Error(), "boom-42", "error %q should mention the message", err)
}

func TestRunnerCapturesPrintOutput(t *testing.T) {
	var got []string

	r := NewRunnerWithSink(func(lines []string, _ int) { got = append(got, lines...) },
		host.Providers{})

	err := r.Run(context.Background(),
		prepared(domain.HookScript{Content: `print("one"); print("two")`}), mod())
	require.NoError(t, err, "Run: %v", err)

	require.Len(t, got, 2, "captured %v", got)
	assert.Equal(t, "one", got[0], "first line")
	assert.Equal(t, "two", got[1], "second line")
}

// A runaway loop must abort via context, not hang the install.
func TestRunnerStopsInfiniteLoops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()

	err := NewRunner(host.Providers{}).Run(ctx,
		prepared(domain.HookScript{Content: `while true do end`}), mod())
	require.Equal(t, appErrors.ErrCodeHookTimeout, appErrors.CodeOf(err),
		"code = %v, want HookTimeout (err=%v)", appErrors.CodeOf(err), err)

	elapsed := time.Since(start)
	assert.Less(t, elapsed, 5*time.Second, "interruption took %s", elapsed)
}

func TestRunnerReportsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewRunner(host.Providers{}).Run(ctx,
		prepared(domain.HookScript{Content: `local x = 1`}), mod())
	require.Equal(t, appErrors.ErrCodeHookTimeout, appErrors.CodeOf(err),
		"code = %v, want HookTimeout", appErrors.CodeOf(err))
}

func TestRunnerSurvivesAFailedHook(t *testing.T) {
	r := NewRunner(host.Providers{})
	if err := r.Run(context.Background(),
		prepared(domain.HookScript{Content: `error("first")`}), mod()); err == nil {
		require.FailNow(t, "expected the first hook to fail")
	}

	if err := r.Run(context.Background(),
		prepared(domain.HookScript{Content: `local x = 2 + 2`}), mod()); err != nil {
		require.FailNow(t, "second Run after a failure: %v", err)
	}
}

func TestRunnerSupportsOrdinaryLua(t *testing.T) {
	script := `
		local t = {3, 1, 2}
		table.sort(t)
		if t[1] ~= 1 or t[3] ~= 3 then error("sort") end
		local s = string.upper("abc")
		if s ~= "ABC" then error("string") end
		if math.max(1, 2) ~= 2 then error("math") end
		local ok, msg = pcall(function() error("caught") end)
		if ok then error("pcall should fail") end
		if not string.find(tostring(msg), "caught") then error("pcall lost the message") end
	`
	err := NewRunner(host.Providers{}).Run(
		context.Background(),
		prepared(domain.HookScript{Content: script}),
		mod(),
	)
	require.NoError(t, err, "Run: %v", err)
}

func TestCheckRejectsASyntaxError(t *testing.T) {
	err := NewRunner(host.Providers{}).Check(domain.HookScript{
		Language: domain.LangLua,
		Content:  "if",
	})

	require.Error(t, err, "an unparseable script passed Check")
	assert.Equal(t, appErrors.ErrCodeHookInvalid, appErrors.CodeOf(err))
}

func TestCheckAcceptsAValidScript(t *testing.T) {
	err := NewRunner(host.Providers{}).Check(domain.HookScript{
		Language: domain.LangLua,
		Content:  "print('hi')",
	})

	require.NoError(t, err, "Check: %v", err)
}
