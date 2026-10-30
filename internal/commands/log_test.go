package commands

import (
	"testing"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestLogLevelRoundTrips(t *testing.T) {
	w := newWorkspace(t)
	c := w.container(t)
	app := New(Deps{Client: c.Client, Logging: c.Logging})

	before, err := app.Logs.GetLogSettings()
	require.NoError(t, err, "read settings: %v", err)

	require.NoError(t, app.Logs.SetLogLevel("debug"))

	after, err := app.Logs.GetLogSettings()
	require.NoError(t, err, "read settings: %v", err)

	require.Equal(t, "debug", after.Level, "level = %q, want debug", after.Level)
	// The runtime switch must leave the destinations alone: only a config
	// write moves them.
	require.Equal(t, before.Directory, after.Directory, "directory changed")
	require.Equal(t, before.File, after.File, "file changed")
}

func TestSetLogLevelRejectsAnUnknownName(t *testing.T) {
	w := newWorkspace(t)
	c := w.container(t)
	app := New(Deps{Client: c.Client, Logging: c.Logging})

	err := app.Logs.SetLogLevel("loud")

	require.Error(t, err, "SetLogLevel accepted an unknown level")
	require.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"err = %v, want an invalid-argument app error", err)
}
