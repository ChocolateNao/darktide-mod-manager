package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	manifestpkg "github.com/ChocolateNao/darktide-mod-manager/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runManifestInit runs `dmm manifest init` the way a person would, and fails
// the test if anything asks for the container: scaffolding a mod needs no
// database, no game and no configured source, and a command that started
// building one would be a step that can fail for no reason.
func runManifestInit(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var out, errOut strings.Builder

	env := Env{
		Out: &out, Err: &errOut,
		NewContainer: func(context.Context, di.Options) (*di.Container, error) {
			require.FailNow(t, "manifest init must not need the container")

			return nil, nil
		},
	}

	code := RunContext(context.Background(), env, append([]string{"manifest", "init"}, args...))

	return code, out.String(), errOut.String()
}

func TestManifestInitWritesAManifestTheManagerCanRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "better_ui")

	code, out, errOut := runManifestInit(t, dir)
	require.Equal(t, ExitOK, code, "manifest init failed: %s%s", out, errOut)
	assert.Contains(t, out, filepath.Join(dir, manifestpkg.Names[0]),
		"the user is told which file was written")

	m, err := manifestpkg.Read(dir)
	require.NoError(t, err, "the file the command wrote must be readable: %v", err)
	assert.Equal(t, "", m.Mod.ID, "a blank manifest claims nothing")
}

func TestManifestInitRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, manifestpkg.Names[0])

	require.NoError(t, os.WriteFile(path, []byte("[mod]\nid = \"mine\"\n"), 0o600))

	code, _, errOut := runManifestInit(t, dir)
	assert.Equal(t, ExitError, code, "a second init must not succeed")
	assert.Contains(t, errOut, "already has a manifest", "the refusal explains itself")

	body, err := os.ReadFile(path)
	require.NoError(t, err, "read back")
	assert.Contains(t, string(body), `id = "mine"`, "the existing manifest must survive")
}

func TestManifestInitReportsJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "better_ui")

	code, out, _ := runManifestInit(t, dir, "--json")
	require.Equal(t, ExitOK, code, "manifest init --json failed")

	var payload struct {
		Path     string `json:"path"`
		Template string `json:"template"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload), "output was not JSON: %s", out)
	assert.Equal(t, filepath.Join(dir, manifestpkg.Names[0]), payload.Path, "path")
	assert.Equal(t, manifestpkg.Template, payload.Template, "template")
}

func TestManifestInitNeedsAFolder(t *testing.T) {
	code, _, errOut := runManifestInit(t)
	assert.Equal(t, ExitError, code, "init with no path must fail")
	assert.Contains(t, errOut, "error:", "the failure is reported: %q", errOut)
}
