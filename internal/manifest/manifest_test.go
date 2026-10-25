package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750), "create parent for %s", path)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600), "write %s", path)
}

func TestReadPrefersModToml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mod.toml"), "[mod]\nid = \"winner\"\n")
	writeFile(t, filepath.Join(dir, "manifest.toml"), "[mod]\nid = \"loser\"\n")

	m, err := Read(dir)
	require.NoError(t, err, "Read")

	assert.Equal(t, "winner", m.Mod.ID, "%s should win", Names[0])
}

func TestReadWithoutOne(t *testing.T) {
	m, err := Read(t.TempDir())
	require.NoError(t, err, "a mod without a manifest is legal, got %v", err)

	if m == nil {
		require.FailNow(t, "Read must return a usable empty manifest, not nil")
	}

	assert.Equal(t, "", m.Mod.ID, "expected an empty manifest, got %+v", m.Mod)
}

func TestReadMalformedNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, Names[0]), "[mod\nid = ")

	_, err := Read(dir)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod),
		"want INVALID_MOD, got %v", err)
	assert.Contains(t, err.Error(), Names[0], "the error should name the offending file")
}

func TestReadRejectsAnUnknownHookLanguage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, Names[0]),
		"[[hooks.pre_enable]]\ntype = \"powershell\"\ncontent = \"x\"\n")

	_, err := Read(dir)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod),
		"want INVALID_MOD, got %v", err)
}

func TestHasRecognisesEveryNameAndOnlyFiles(t *testing.T) {
	root := t.TempDir()

	plain := filepath.Join(root, "plain")
	writeFile(t, filepath.Join(plain, "readme.txt"), "x")
	assert.False(t, Has(plain), "a folder with only a readme is not a mod folder")

	for _, name := range Names {
		dir := filepath.Join(root, "with"+name)
		writeFile(t, filepath.Join(dir, name), "[mod]\n")
		assert.True(t, Has(dir), "%s should be recognised", name)
		assert.True(t, IsName(name), "%s should be a manifest name", name)
	}

	// A *directory* named mod.toml is not a manifest.
	odd := filepath.Join(root, "odd")
	writeFile(t, filepath.Join(odd, Names[0], "inner.txt"), "x")
	assert.False(t, Has(odd), "a directory called %s must not count", Names[0])

	assert.False(t, IsName("mod.json"), "IsName is about the format, not about any file")
}

func TestHooksCoverEveryPhase(t *testing.T) {
	var body strings.Builder

	for _, phase := range domain.HookPhases() {
		name := strings.ReplaceAll(string(phase), "-", "_")
		body.WriteString("[[hooks." + name + "]]\ntype = \"lua\"\ncontent = \"print('" +
			name + "')\"\ntimeout = 7\n\n")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, Names[0]), body.String())

	m, err := Read(dir)
	require.NoError(t, err, "Read")

	hooks, err := m.DomainHooks()
	require.NoError(t, err, "DomainHooks")

	assert.Equal(t, len(domain.HookPhases()), hooks.Count(),
		"one hook per phase, so a new phase cannot be added without a manifest mapping")

	for _, phase := range domain.HookPhases() {
		scripts := hooks.For(phase)
		require.Len(t, scripts, 1, "phase %s", phase)

		assert.Equal(t, 7, scripts[0].Timeout, "phase %s script = %+v", phase, scripts[0])
		assert.Equal(t, domain.LangLua, scripts[0].Language, "phase %s script = %+v", phase, scripts[0])
	}
}

func TestDomainHooksDropsBlankScripts(t *testing.T) {
	// An empty content line must not make the mod impossible to enable.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, Names[0]),
		"[[hooks.pre_enable]]\ntype = \"lua\"\ncontent = \"\"\n\n"+
			"[[hooks.pre_enable]]\ntype = \"javascript\"\ncontent = \"  \"\n\n"+
			"[[hooks.pre_enable]]\ntype = \"lua\"\ncontent = \"ok\"\n")

	m, err := Read(dir)
	require.NoError(t, err, "Read")

	hooks, err := m.DomainHooks()
	require.NoError(t, err, "DomainHooks")

	require.Len(t, hooks.PreEnable, 1, "the blank scripts should be dropped")
	assert.Equal(t, "ok", hooks.PreEnable[0].Content, "wrong script survived")
}

// An unknown or blank type is a hard manifest error: guessing a language would
// run the wrong interpreter against untrusted content.
func TestScriptRejectsUnknownLanguage(t *testing.T) {
	for _, typ := range []string{"", "  ", "powershell", "bogus", "inline", "file", "command", "go"} {
		_, err := HookEntry{Type: typ, Content: "x"}.Script()
		assert.Error(t, err, "type %q must be rejected", typ)
	}
}

func TestScriptAcceptsKnownLanguages(t *testing.T) {
	lua, err := HookEntry{
		Type:       "lua",
		Content:    "x",
		WorkingDir: " sub ",
		Env:        []string{"A=1", " "},
	}.Script()
	require.NoError(t, err, "lua")

	assert.Equal(t, domain.LangLua, lua.Language, "a valid entry must survive: %+v", lua)
	assert.Equal(t, "sub", lua.WorkingDir, "a valid entry must survive: %+v", lua)
	assert.Len(t, lua.Env, 1, "a valid entry must survive: %+v", lua)

	js, err := HookEntry{Type: "javascript", Content: "x"}.Script()
	require.NoError(t, err, "javascript")
	assert.Equal(t, domain.LangJS, js.Language, "language")
}

func TestTrimmedList(t *testing.T) {
	got := TrimmedList([]string{" a ", "", "  ", "b", "\t"})
	assert.Equal(t, "a,b", strings.Join(got, ","), "TrimmedList")

	assert.Nil(t, TrimmedList(nil), "a nil list stays nil so it encodes as an absent field")
	assert.Nil(t, TrimmedList([]string{" ", ""}), "an all-blank list collapses to nil")
}

func TestInitWritesAManifestTheManagerCanRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new_mod")

	path, err := Init(dir)
	require.NoError(t, err, "Init: %v", err)
	assert.Equal(t, filepath.Join(dir, Names[0]), path, "Init should write the preferred name")

	m, err := Read(dir)
	require.NoError(t, err, "the starter manifest must parse: %v", err)
	assert.Equal(t, "", m.Mod.ID, "a blank manifest claims nothing")

	hooks, err := m.DomainHooks()
	require.NoError(t, err, "the commented-out hooks must not parse as scripts")
	assert.Equal(t, 0, hooks.Count(), "a blank manifest has no hooks")

	// Parsing is the assertion: a starter file that no longer parses fails here rather
	// than at the author's first install.
}

func TestInitRefusesToOverwriteAWrittenManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, Names[0]), "[mod]\nid = \"mine\"\n")

	_, err := Init(dir)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidMod),
		"want INVALID_MOD, got %v", err)

	m, err := Read(dir)
	require.NoError(t, err, "Read")
	assert.Equal(t, "mine", m.Mod.ID, "the existing manifest must survive untouched")
}

func TestInitNeedsAFolder(t *testing.T) {
	_, err := Init("   ")
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"want INVALID_ARGUMENT, got %v", err)
}

func TestInitRefusesWhenThePathIsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mod")
	writeFile(t, file, "not a folder")

	_, err := Init(file)
	assert.Error(t, err, "a file is not a mod folder")
}
