package darktide

import (
	"context"
	"errors"
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
