package darktide

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

func TestLaunchCommandFollowsTheInstallation(t *testing.T) {
	tests := []struct {
		name     string
		actx     domain.ActionContext
		wantPath string
		wantArgs []string
	}{
		{
			name: "native steam on linux",
			actx: domain.ActionContext{
				Platform:    domain.PlatformLinux,
				InstallPath: "/home/u/.local/share/Steam/steamapps/common/Darktide",
			},
			wantPath: "steam",
			wantArgs: []string{"steam://rungameid/" + SteamAppID},
		},
		{
			name: "flatpak steam on linux",
			actx: domain.ActionContext{
				Platform:    domain.PlatformLinux,
				InstallPath: "/home/u/.var/app/com.valvesoftware.Steam/data/Steam/steamapps/common/Darktide",
			},
			wantPath: "flatpak",
			wantArgs: []string{
				"run", "com.valvesoftware.Steam", "steam://rungameid/" + SteamAppID,
			},
		},
		{
			name: "snap steam on linux",
			actx: domain.ActionContext{
				Platform:    domain.PlatformLinux,
				InstallPath: "/home/u/snap/steam/common/.local/share/Steam/steamapps/common/Darktide",
			},
			wantPath: "snap",
			wantArgs: []string{"run", "steam", "steam://rungameid/" + SteamAppID},
		},
		{
			name: "steam on windows",
			actx: domain.ActionContext{
				Platform:    domain.PlatformWindows,
				InstallPath: `C:\Program Files (x86)\Steam\steamapps\common\Darktide`,
			},
			wantPath: "rundll32",
			wantArgs: []string{
				"url.dll,FileProtocolHandler", "steam://rungameid/" + SteamAppID,
			},
		},
		{
			name: "microsoft store on windows",
			actx: domain.ActionContext{
				Platform:    domain.PlatformWindows,
				InstallPath: `C:\XboxGames\Warhammer 40,000 Darktide\Content`,
			},
			wantPath: filepath.Join(`C:\XboxGames\Warhammer 40,000 Darktide\Content`, BinaryName),
			wantArgs: nil,
		},
		{
			name:     "steam on macos",
			actx:     domain.ActionContext{Platform: domain.PlatformDarwin},
			wantPath: "open",
			wantArgs: []string{"steam://rungameid/" + SteamAppID},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := launchCommandFor(test.actx)

			require.True(t, ok)
			assert.Equal(t, test.wantPath, command.Path)
			assert.Equal(t, test.wantArgs, command.Args)
		})
	}
}

func TestLaunchCommandRejectsAnUnknownPlatform(t *testing.T) {
	_, ok := launchCommandFor(domain.ActionContext{Platform: "plan9"})

	assert.False(t, ok)
}

func launchContext(start func(context.Context, domain.ActionCommand) error) domain.ActionContext {
	return domain.ActionContext{
		Game:            Definition{},
		Platform:        domain.PlatformLinux,
		InstallPath:     "/home/u/.local/share/Steam/steamapps/common/Darktide",
		Action:          domain.ActionID(ID + ":" + actionLaunchGame),
		CommandsAllowed: true,
		StartCommand:    start,
	}
}

func TestLaunchGameStartsTheDetectedCommand(t *testing.T) {
	var started []domain.ActionCommand

	result, err := launchGame(context.Background(), launchContext(
		func(_ context.Context, command domain.ActionCommand) error {
			started = append(started, command)

			return nil
		},
	))
	require.NoError(t, err, "launch")
	require.NotNil(t, result, "result")
	assert.True(t, result.Success, "launch reported failure: %+v", result)
	assert.Equal(t, "actions.darktide.launchGame.start", result.MessageKey)

	require.Len(t, started, 1, "started %d commands", len(started))
	assert.Equal(t, "steam", started[0].Path)
	assert.Equal(t, []string{"steam://rungameid/" + SteamAppID}, started[0].Args)
}

func TestLaunchGameRefusesWhenCommandsAreDisabled(t *testing.T) {
	called := false
	actx := launchContext(func(context.Context, domain.ActionCommand) error {
		called = true

		return nil
	})
	actx.CommandsAllowed = false

	result, err := launchGame(context.Background(), actx)
	require.NoError(t, err, "launch")
	require.NotNil(t, result, "result")
	assert.False(t, result.Success, "launch ignored the permission")
	assert.Equal(t, "actions.commandsDisabled", result.MessageKey)
	assert.False(t, called, "a command ran despite the permission")
}

func TestLaunchGameReportsAStartFailure(t *testing.T) {
	_, err := launchGame(context.Background(), launchContext(
		func(context.Context, domain.ActionCommand) error {
			return errors.New("steam is missing")
		},
	))
	require.Error(t, err, "a failure to start the game reported success")
	assert.Contains(t, err.Error(), "steam is missing")
}

func writePatcher(t *testing.T, dir, name string) string {
	t.Helper()

	tools := filepath.Join(dir, "tools")
	require.NoError(t, os.MkdirAll(tools, 0o755), "create tools folder")

	path := filepath.Join(tools, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755), "write %s", name)

	return path
}

func noWine(string) (string, error) { return "", errors.New("wine is not installed") }

func TestDtkitPatchCommandPrefersTheNativeBinary(t *testing.T) {
	game := t.TempDir()
	native := writePatcher(t, game, "dtkit-patch")
	writePatcher(t, game, "dtkit-patch.exe")

	command, ok := dtkitPatchCommand(
		domain.ActionContext{InstallPath: game}, "--patch", noWine)
	require.True(t, ok, "native patcher was not found")
	assert.Equal(t, native, command.Path)
	assert.Equal(t, []string{"--patch", "bundle"}, command.Args)
}

func TestDtkitPatchCommandFallsBackToWine(t *testing.T) {
	game := t.TempDir()
	exe := writePatcher(t, game, "dtkit-patch.exe")

	lookPath := func(name string) (string, error) {
		if name == "wine" {
			return "/usr/bin/wine", nil
		}

		return "", errors.New("missing")
	}

	command, ok := dtkitPatchCommand(
		domain.ActionContext{InstallPath: game}, "--unpatch", lookPath)
	require.True(t, ok, "wine fallback was not found")
	assert.Equal(t, "/usr/bin/wine", command.Path)
	assert.Equal(t, []string{exe, "--unpatch", "bundle"}, command.Args)
}

func TestDtkitPatchCommandReportsAMissingPatcher(t *testing.T) {
	_, ok := dtkitPatchCommand(
		domain.ActionContext{InstallPath: t.TempDir()}, "--patch", noWine)

	assert.False(t, ok, "found a patcher that is not there")
}

func modLoadingContext(
	game string,
	mode string,
	run domain.ActionCommandRunner,
) domain.ActionContext {
	return domain.ActionContext{
		Game:            Definition{},
		Platform:        domain.PlatformLinux,
		InstallPath:     game,
		Action:          domain.ActionID(ID + ":" + actionSetModLoading),
		CommandsAllowed: true,
		RunCommand:      run,
		Args:            map[string]any{"mode": mode},
	}
}

func TestSetModLoadingRunsTheNativePatcher(t *testing.T) {
	game := t.TempDir()
	writePatcher(t, game, "dtkit-patch")

	var ran []domain.ActionCommand

	result, err := setModLoading(context.Background(), modLoadingContext(
		game, "enable",
		func(_ context.Context, command domain.ActionCommand) (domain.ActionCommandResult, error) {
			ran = append(ran, command)

			return domain.ActionCommandResult{Output: "successfully patched"}, nil
		},
	))
	require.NoError(t, err, "set mod loading")
	require.NotNil(t, result, "result")
	assert.True(t, result.Success, "set mod loading failed: %+v", result)
	assert.Equal(t, "actions.darktide.setModLoading.enabled", result.MessageKey)
	assert.Equal(t, "successfully patched", result.Output)

	require.Len(t, ran, 1, "ran %d commands", len(ran))
	assert.Equal(t, []string{"--patch", "bundle"}, ran[0].Args)
}

func TestSetModLoadingRunsTheUnpatch(t *testing.T) {
	game := t.TempDir()
	writePatcher(t, game, "dtkit-patch")

	var ran domain.ActionCommand

	result, err := setModLoading(context.Background(), modLoadingContext(
		game, "disable",
		func(_ context.Context, command domain.ActionCommand) (domain.ActionCommandResult, error) {
			ran = command

			return domain.ActionCommandResult{Output: "successfully unpatched"}, nil
		},
	))
	require.NoError(t, err, "set mod loading")
	require.NotNil(t, result, "result")
	assert.True(t, result.Success, "set mod loading failed: %+v", result)
	assert.Equal(t, "actions.darktide.setModLoading.disabled", result.MessageKey)
	assert.Equal(t, []string{"--unpatch", "bundle"}, ran.Args)
}

func TestSetModLoadingRefusesWhenCommandsAreDisabled(t *testing.T) {
	called := false
	actx := modLoadingContext(t.TempDir(), "enable",
		func(context.Context, domain.ActionCommand) (domain.ActionCommandResult, error) {
			called = true

			return domain.ActionCommandResult{}, nil
		})
	actx.CommandsAllowed = false

	result, err := setModLoading(context.Background(), actx)
	require.NoError(t, err, "set mod loading")
	require.NotNil(t, result, "result")
	assert.False(t, result.Success, "set mod loading ignored the permission")
	assert.Equal(t, "actions.commandsDisabled", result.MessageKey)
	assert.False(t, called, "a command ran despite the permission")
}

func TestSetModLoadingReportsAFailedPatch(t *testing.T) {
	game := t.TempDir()
	writePatcher(t, game, "dtkit-patch")

	result, err := setModLoading(context.Background(), modLoadingContext(
		game, "enable",
		func(context.Context, domain.ActionCommand) (domain.ActionCommandResult, error) {
			return domain.ActionCommandResult{Output: "not a Darktide bundle", ExitCode: 1},
				errors.New("exit status 1")
		},
	))
	require.NoError(t, err, "set mod loading returned a Go error")
	require.NotNil(t, result, "result")
	assert.False(t, result.Success, "failed patch reported success")
	assert.Equal(t, "actions.darktide.setModLoading.failed", result.MessageKey)
	assert.Equal(t, "not a Darktide bundle", result.Output)
}

func TestSetModLoadingReportsAMissingPatcher(t *testing.T) {
	result, err := setModLoading(context.Background(), modLoadingContext(
		t.TempDir(), "enable",
		func(context.Context, domain.ActionCommand) (domain.ActionCommandResult, error) {
			t.Fatal("ran a command without a patcher")

			return domain.ActionCommandResult{}, nil
		},
	))
	require.NoError(t, err, "set mod loading")
	require.NotNil(t, result, "result")
	assert.False(t, result.Success, "missing patcher reported success")
	assert.Equal(t, "actions.darktide.setModLoading.missingTool", result.MessageKey)
}
