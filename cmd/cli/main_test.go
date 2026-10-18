package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/cmd/cli/commands"
	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installBuild swaps the container factory for the duration of one test.
func installBuild(
	t *testing.T,
	fn func(ctx context.Context, opts di.Options) (*di.Container, error),
) {
	t.Helper()

	original := build
	build = fn

	t.Cleanup(func() { build = original })
}

// capture runs one invocation of the entry point and returns what it wrote.
func capture(t *testing.T, args []string, in string) (int, string, string) {
	t.Helper()

	var out, errOut bytes.Buffer

	code := run(context.Background(), args, strings.NewReader(in), &out, &errOut)

	return code, out.String(), errOut.String()
}

// isolate points every per-user environment variable at a fresh directory.
func isolate(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	for _, name := range []string{
		"HOME", "USERPROFILE",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"APPDATA", "LOCALAPPDATA",
	} {
		t.Setenv(name, home)
	}

	return home
}

func TestRunReportsAVersionWithoutBuildingAnything(t *testing.T) {
	// The version query must answer even when nothing else could: a user with a
	// corrupt settings file still needs to be able to say which build they have.
	isolate(t)

	built := 0

	installBuild(t, func(ctx context.Context, opts di.Options) (*di.Container, error) {
		built++
		return nil, errors.New("the container should not have been built")
	})

	code, out, errOut := capture(t, []string{"version"}, "")
	assert.Equal(t, commands.ExitOK, code, "stderr: %s", errOut)

	assert.Contains(t, out, "dmm", "stdout = %q, want it to name the program", out)

	assert.Equal(t, 0, built, "the container was built %d times for a version query", built)
}

func TestRunPassesThePathFlagsThroughToTheFactory(t *testing.T) {
	// The path flags are the whole contract between the shell and the manager's
	// wiring; losing one would send every user to the same installation.
	isolate(t)
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	cache := filepath.Join(root, "cache")
	storage := filepath.Join(root, "data", "mods")

	var seen di.Options

	installBuild(t, func(ctx context.Context, opts di.Options) (*di.Container, error) {
		seen = opts
		return di.NewContainer(ctx, opts)
	})

	code, _, errOut := capture(t, []string{
		"--config", config, "--cache", cache, "--storage", storage, "cache", "info",
	}, "")
	require.Equal(t, commands.ExitOK, code, "stderr: %s", errOut)

	assert.Equal(
		t,
		config,
		seen.ConfigPath,
		"--config reached the factory as %q, want %q",
		seen.ConfigPath,
		config,
	)

	assert.Equal(
		t,
		cache,
		seen.CachePath,
		"--cache reached the factory as %q, want %q",
		seen.CachePath,
		cache,
	)

	assert.Equal(
		t,
		storage,
		seen.StoragePath,
		"--storage reached the factory as %q, want %q",
		seen.StoragePath,
		storage,
	)
}

func TestRunTurnsAFactoryFailureIntoAnExitCode(t *testing.T) {
	// A refusal to start has to reach the shell as a non-zero code, and the reason
	// has to be on stderr: a bare "1" tells a person nothing.
	isolate(t)
	installBuild(t, func(ctx context.Context, opts di.Options) (*di.Container, error) {
		return nil, appErrors.New(appErrors.ErrCodeConfigIO, "the settings file is unreadable")
	})

	code, out, errOut := capture(t, []string{"cache", "info"}, "")
	assert.Equal(t, commands.ExitError, code, "exit = %d, want %d", code, commands.ExitError)

	assert.Contains(
		t,
		errOut,
		"the settings file is unreadable",
		"stderr = %q, want it to carry the reason",
		errOut,
	)

	assert.NotContains(t, out, "unreadable", "stdout = %q, want the reason to stay off stdout", out)
}

func TestRunReturnsTheCancellationExitCode(t *testing.T) {
	// Ctrl-C is not a failure: an interrupted operation leaves no partial state.
	// A cancelled context stands in for main's signal context.
	isolate(t)
	installBuild(t, func(ctx context.Context, opts di.Options) (*di.Container, error) {
		return nil, appErrors.New(appErrors.ErrCodeOperationCanceled, "the operation was canceled")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out, errOut bytes.Buffer

	code := run(ctx, []string{"cache", "info"}, strings.NewReader(""), &out, &errOut)
	assert.Equal(t, commands.ExitCanceled, code, "stderr: %s", errOut.String())
}

func TestRunBuildsOneContainerPerInvocation(t *testing.T) {
	// Two invocations must not share a container: the first one closed it, and a
	// second command handed the same handle would be talking to a released database.
	isolate(t)

	built := 0

	installBuild(t, func(ctx context.Context, opts di.Options) (*di.Container, error) {
		built++
		return di.NewContainer(ctx, opts)
	})

	root := t.TempDir()
	for _, config := range []string{
		filepath.Join(root, "config.json"),
		filepath.Join(root, "other.json"),
	} {
		code, _, errOut := capture(t, []string{"--config", config, "cache", "info"}, "")
		require.Equal(t, commands.ExitOK, code, "stderr: %s", errOut)
	}

	assert.Equal(t, 2, built, "the factory was called %d times for two invocations, want 2", built)
}

func TestRunHandsStandardInputToTheCommand(t *testing.T) {
	// An API key is passed on standard input so it never appears in the process
	// list; if the entry point did not pass its reader through, that path would break.
	isolate(t)
	installBuild(t, func(ctx context.Context, opts di.Options) (*di.Container, error) {
		return di.NewContainer(ctx, opts)
	})

	var keys []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("apikey"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"mods":[]}}`))
	}))
	defer server.Close()

	config := filepath.Join(t.TempDir(), "config.json")
	writeSettings(t, config, map[string]any{
		"sources": map[string]any{
			// The base URL is the only knob this test needs: pointing it at the
			// test server keeps the request off the network.
			"nexus": map[string]any{
				"enabled": true,
				"options": map[string]any{"api_base": server.URL},
			},
		},
	})
	code, _, errOut := capture(t, []string{
		"--config", config, "source", "auth", "nexus", "--api-key-stdin",
	}, "read-from-stdin\n")

	require.Equal(t, commands.ExitOK, code, "stderr: %s", errOut)
	// The key the server saw is the proof: asserting on a complaint instead would
	// only prove that something was read, not that it reached the wire.
	require.Len(t, keys, 1, "the source was asked %d times, want once", len(keys))

	assert.Equal(
		t,
		"read-from-stdin",
		keys[0],
		"the source received the key %q, want %q",
		keys[0],
		"read-from-stdin",
	)
	// A key that is only held in memory is unauthenticated again on the next
	// command, so the command that read it has to put it somewhere.
	stored := settingValue(
		t,
		config,
		"sources",
		"nexus",
		"api_key",
	)
	assert.Equal(
		t,
		"read-from-stdin",
		stored,
		"the settings file holds %q, want the key to have been saved",
		stored,
	)
}

// writeSettings writes a settings file holding the given object as its body.
func writeSettings(t *testing.T, path string, body map[string]any) {
	t.Helper()

	encoded, err := json.MarshalIndent(body, "", "  ")
	require.NoError(t, err, "encode the settings")

	require.NoError(t, os.WriteFile(path, encoded, 0o600), "write the settings file")
}

// settingValue reads one dotted path out of a settings file.
func settingValue(t *testing.T, path string, steps ...string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read the settings file")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "decode the settings file")

	current := any(decoded)
	for _, step := range steps {
		asMap, ok := current.(map[string]any)
		require.True(t, ok, "the settings file has no object at %q", step)

		current = asMap[step]
	}

	text, _ := current.(string)

	return text
}

func TestRunSendsHelpToStandardOutputWithoutBuildingAnything(t *testing.T) {
	// Help is what a person runs first, so it must not need a working settings
	// file, and it belongs on stdout because a shell may be reading it.
	isolate(t)

	built := 0

	installBuild(t, func(ctx context.Context, opts di.Options) (*di.Container, error) {
		built++
		return nil, errors.New("the container should not have been built")
	})

	code, out, errOut := capture(t, []string{"--help"}, "")
	assert.Equal(t, commands.ExitOK, code, "stderr: %s", errOut)

	assert.Contains(t, out, "Available Commands", "stdout = %q, want the command list", out)

	assert.Equal(t, 0, built, "the container was built %d times for a help request", built)
}
