package commands

import (
	"bytes"
	"context"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/di"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (w *workspace) execute(t *testing.T, args ...string) (string, string, int) {
	t.Helper()

	var out, errOut bytes.Buffer

	env := Env{
		Out: &out,
		Err: &errOut,
		In:  bytes.NewReader(nil),
		NewContainer: func(ctx context.Context, opts di.Options) (*di.Container, error) {
			return di.NewContainer(ctx, opts)
		},
	}

	code := RunContext(context.Background(), env, w.args(args...))

	return out.String(), errOut.String(), code
}

// The share commands run against the real command tree and container. The upload
// path is not covered: it needs a provider.

func TestShareListWithNoShares(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	out, errOut, code := w.execute(t, "share", "list")

	assert.Equal(t, 0, code, "exit code")
	assert.Contains(t, out, "No shares found.", "output")
	assert.Contains(t, errOut, "cloud sharing is deprecated", "stderr")
}

func TestShareListWithGameFilter(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	out, errOut, code := w.execute(t, "share", "list", "--game", "darktide")

	assert.Equal(t, 0, code, "exit code")
	assert.Contains(t, out, "No shares found.", "output")
	assert.Contains(t, errOut, "cloud sharing is deprecated", "stderr")
	assert.NotContains(t, out, "deprecated", "the notice must not pollute stdout")
}

func TestShareCreateWithoutModsFails(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	out, errOut, code := w.execute(t, "share", "create")

	assert.Equal(t, 1, code, "exit code")
	assert.Empty(t, out, "output")
	assert.Contains(t, errOut, "no mods specified")
	assert.Contains(t, errOut, "--mods")
}

func TestShareCreateRejectsUnknownModBeforeUploading(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	out, errOut, code := w.execute(t, "share", "create", "--mods", "not-installed")

	assert.Equal(t, 1, code, "exit code")
	assert.Empty(t, out, "output")
	assert.Contains(t, errOut, "not-installed")
}

func TestShareShowUnknownID(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	out, errOut, code := w.execute(t, "share", "show", "nope")

	assert.Equal(t, 1, code, "exit code")
	assert.Empty(t, out, "output")
	assert.Contains(t, errOut, "nope")
}

func TestShareRevokeUnknownID(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	out, errOut, code := w.execute(t, "share", "revoke", "nope")

	assert.Equal(t, 1, code, "exit code")
	assert.Empty(t, out, "output")
	assert.Contains(t, errOut, "nope")
}

func TestShareUpdateUnknownID(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	out, errOut, code := w.execute(t, "share", "update", "nope")

	assert.Equal(t, 1, code, "exit code")
	assert.Empty(t, out, "output")
	assert.Contains(t, errOut, "not found")
}

func TestShareCommandsRequireAnArgument(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	for _, args := range [][]string{
		{"share", "show"},
		{"share", "revoke"},
		{"share", "update"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			_, errOut, code := w.execute(t, args...)

			assert.Equal(t, 1, code, "exit code")
			assert.Contains(t, errOut, "accepts 1 arg")
		})
	}
}

func TestShareHelpListsEverySubcommand(t *testing.T) {
	w := newWorkspace(t)
	w.makeGame(t)
	w.writeConfig(t)

	out, _, code := w.execute(t, "share", "--help")

	require.Equal(t, 0, code, "exit code")

	for _, name := range []string{"create", "list", "show", "revoke", "update"} {
		assert.Contains(t, out, name, "subcommand %q", name)
	}
}
