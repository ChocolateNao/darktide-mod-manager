package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeConfig struct {
	values map[string]string
	sets   []string
}

func (c *fakeConfig) Get(key string) (string, error) {
	if c.values == nil {
		return "", appErrors.New(appErrors.ErrCodeNotFound, "missing")
	}

	v, ok := c.values[key]
	if !ok {
		return "", appErrors.New(appErrors.ErrCodeNotFound, "missing")
	}

	return v, nil
}

func (c *fakeConfig) Set(key, value string) error {
	c.sets = append(c.sets, key+"="+value)
	if c.values == nil {
		c.values = map[string]string{}
	}

	c.values[key] = value

	return nil
}

func (c *fakeConfig) Values() (map[string]string, error) {
	out := map[string]string{}
	for k, v := range c.values {
		out[k] = v
	}

	return out, nil
}

type fakeStorage struct{ listed int }

func (s *fakeStorage) List(context.Context) ([]StorageEntry, error) {
	s.listed++

	return []StorageEntry{{GameID: "darktide", ModID: "m", Version: "1.0", Path: "/p"}}, nil
}

func (s *fakeStorage) Path(context.Context, string, string, string) (string, error) {
	return "/p", nil
}

func TestPermissionGatesRejectUngrantedCapabilities(t *testing.T) {
	cfg := &fakeConfig{values: map[string]string{"a": "1"}}
	stor := &fakeStorage{}
	tmp := t.TempDir()

	all := Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{},
		Context: domain.HookContext{
			Mod:    domain.ModHookContext{ID: "m"},
			Config: domain.ConfigHookContext{TempPath: tmp},
		},
		Providers: Providers{Config: cfg, Storage: stor, TempDir: tmp},
	}
	h := New(all)

	_, err := h.ReadFile("x")
	assert.Equal(t, appErrors.ErrCodeHookDenied, appErrors.CodeOf(err), "fs_read must be denied")
	_, err = h.List("x")
	assert.Equal(
		t,
		appErrors.ErrCodeHookDenied,
		appErrors.CodeOf(err),
		"fs_read must be denied for list",
	)
	_, err = h.Exists("x")
	assert.Equal(
		t,
		appErrors.ErrCodeHookDenied,
		appErrors.CodeOf(err),
		"fs_read must be denied for exists",
	)
	_, err = h.Stat("x")
	assert.Equal(
		t,
		appErrors.ErrCodeHookDenied,
		appErrors.CodeOf(err),
		"fs_read must be denied for stat",
	)
	_, err = h.WriteTemp("x", "y")
	assert.Equal(t, appErrors.ErrCodeHookDenied, appErrors.CodeOf(err), "fs_write must be denied")
	_, err = h.ConfigGet("a")
	assert.Equal(
		t,
		appErrors.ErrCodeHookDenied,
		appErrors.CodeOf(err),
		"config_read must be denied",
	)
	_, err = h.ConfigGetAll()
	assert.Equal(
		t,
		appErrors.ErrCodeHookDenied,
		appErrors.CodeOf(err),
		"config_read must be denied for getAll",
	)
	err = h.ConfigSet("a", "2")
	assert.Equal(
		t,
		appErrors.ErrCodeHookDenied,
		appErrors.CodeOf(err),
		"config_write must be denied",
	)
	_, err = h.StorageList(context.Background())
	assert.Equal(
		t,
		appErrors.ErrCodeHookDenied,
		appErrors.CodeOf(err),
		"storage_read must be denied",
	)
	_, err = h.StoragePath(context.Background(), "g", "m", "1")
	assert.Equal(
		t,
		appErrors.ErrCodeHookDenied,
		appErrors.CodeOf(err),
		"storage_read must be denied for path",
	)
	_, err = h.EnvGet("PATH")
	assert.Equal(t, appErrors.ErrCodeHookDenied, appErrors.CodeOf(err), "env_read must be denied")
}

func TestLogAndPathsAreAlwaysAvailable(t *testing.T) {
	h := New(Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{},
		Context: domain.HookContext{
			Game: domain.GameHookContext{Path: "/game"},
			Mod:  domain.ModHookContext{ID: "m"},
		},
	})

	h.Log("info", "hello")
	lines, dropped := h.Logs()
	require.Equal(t, []string{"hello"}, lines, "log lines")
	assert.Equal(t, 0, dropped, "dropped")

	v, ok := h.Path("game")
	assert.True(t, ok, "paths.game must be available without a capability")
	assert.Equal(t, "/game", v, "paths.game")
}

func TestConfigSetRejectsPermissionsKeysEvenWithConfigWrite(t *testing.T) {
	cfg := &fakeConfig{values: map[string]string{}}
	h := New(Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{ConfigWrite: true},
		Providers:   Providers{Config: cfg},
	})

	for _, key := range []string{
		"hooks.permissions",
		"Hooks.Permissions.Default.FSRead",
		" hooks.permissions.mods.m.timeout_seconds ",
	} {
		err := h.ConfigSet(key, "true")
		assert.Equal(t, appErrors.ErrCodeHookDenied, appErrors.CodeOf(err),
			"key %q must be refused even with config_write", key)
	}

	// An ordinary key goes through.
	require.NoError(t, h.ConfigSet("storage.path", "/x"), "ordinary key")
	assert.Equal(t, []string{"storage.path=/x"}, cfg.sets, "sets recorded")
}

func TestWriteTempStaysInsideTempDir(t *testing.T) {
	tmp := t.TempDir()
	h := New(Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{FSWrite: true},
		Context:     domain.HookContext{Config: domain.ConfigHookContext{TempPath: tmp}},
		Providers:   Providers{TempDir: tmp},
	})

	_, err := h.WriteTemp("notes/hello.txt", "hi")
	require.NoError(t, err, "writeTemp")
	data, err := os.ReadFile(filepath.Join(tmp, "notes", "hello.txt"))
	require.NoError(t, err, "read back")
	assert.Equal(t, "hi", string(data), "content")

	_, err = h.WriteTemp("../../escape.txt", "x")
	require.Error(t, err, "escape must fail")
	assert.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"code = %v",
		appErrors.CodeOf(err),
	)
}

func TestReadFileRejectsEscapesAndMissingRoots(t *testing.T) {
	base := t.TempDir()
	modDir := filepath.Join(base, "mod")
	require.NoError(t, os.MkdirAll(modDir, 0o755), "mkdir")
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ok.txt"), []byte("yes"), 0o644), "write")

	h := New(Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{FSRead: true},
		Context:     domain.HookContext{Mod: domain.ModHookContext{Path: modDir}},
		BaseDir:     modDir,
	})

	data, err := h.ReadFile("ok.txt")
	require.NoError(t, err, "read relative inside the mod folder")
	assert.Equal(t, "yes", string(data), "content")

	_, err = h.ReadFile(filepath.Join(t.TempDir(), "elsewhere"))
	assert.Error(t, err, "a path outside every allowed root must fail")
	assert.Equal(
		t,
		appErrors.ErrCodeHookFailed,
		appErrors.CodeOf(err),
		"code = %v",
		appErrors.CodeOf(err),
	)
}

func TestEnvGetNeverTouchesTheProcessEnvironment(t *testing.T) {
	t.Setenv("DMM_HOST_LEAK", "secret")

	h := New(Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{EnvRead: true},
		Env:         []string{"OTHER=1"},
	})

	v, _ := h.EnvGet("DMM_HOST_LEAK")
	assert.Equal(t, "", v, "process env must not leak")
	v, _ = h.EnvGet("NOT_DECLARED")
	assert.Equal(t, "", v, "undeclared name is empty")

	h2 := New(Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{EnvRead: true},
		Env:         []string{"DMM_ALLOWED=yes"},
	})
	v2, _ := h2.EnvGet("DMM_ALLOWED")
	assert.Equal(t, "yes", v2, "declared env")
}

func TestNewIsTheOnlyConstructionAndParsesEnv(t *testing.T) {
	h := New(Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{EnvRead: true},
		Env:         []string{"TOKEN=a=b", "BROKEN", "EMPTY="},
	})

	v, _ := h.EnvGet("TOKEN")
	assert.Equal(t, "a=b", v, "later = stays in the value")
	v, _ = h.EnvGet("BROKEN")
	assert.Equal(t, "", v, "malformed entries are skipped")
	v, _ = h.EnvGet("EMPTY")
	assert.Equal(t, "", v, "empty values are dropped")
}

func TestTruncateAndLineCaps(t *testing.T) {
	var logged []string

	h := New(Options{
		Ctx:         context.Background(),
		Permissions: domain.ScriptPermissions{},
		Providers: Providers{
			Logger: func(_, msg string) { logged = append(logged, msg) },
		},
	})

	long := make([]byte, MaxLogLineLength+100)
	for i := range long {
		long[i] = 'x'
	}

	h.Log("info", string(long))

	lines, dropped := h.Logs()
	require.Len(t, lines, 1, "one line")
	assert.Equal(t, 0, dropped, "dropped")
	assert.LessOrEqual(t, len(lines[0]), MaxLogLineLength+len(logLineTruncation),
		"line must be truncated to the cap plus the ellipsis")

	for range MaxLogLines + 5 {
		h.Log("info", "x")
	}

	lines, dropped = h.Logs()
	assert.Len(t, lines, MaxLogLines, "line count capped")
	// One line was already buffered, so only MaxLogLines-1 of the next
	// MaxLogLines+5 calls fit: the excess is 6.
	assert.Equal(t, 6, dropped, "excess counted")

	// The sink is told about every call, including the ones past the buffer cap.
	assert.Len(t, logged, 1+MaxLogLines+5, "logger sees every call")
}
