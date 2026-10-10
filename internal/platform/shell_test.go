package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lookPathSet builds a LookPath that only finds the named programs.
func lookPathSet(names ...string) LookPath {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}

	return func(file string) (string, error) {
		if set[file] {
			return "/usr/bin/" + file, nil
		}

		return "", errors.New(errors.ErrCodeRunnerNotFound, "%s not found", file)
	}
}

func TestGetShell(t *testing.T) {
	cases := map[string]struct {
		platform domain.Platform
		found    []string
		want     Shell
	}{
		"linux with bash": {
			domain.PlatformLinux,
			[]string{"bash"},
			Shell{"bash", []string{"-c"}},
		},
		"linux without bash": {domain.PlatformLinux, nil, Shell{"sh", []string{"-c"}}},
		"darwin with bash": {
			domain.PlatformDarwin,
			[]string{"bash"},
			Shell{"bash", []string{"-c"}},
		},
		"freebsd with bash": {
			domain.PlatformFreeBSD,
			[]string{"bash"},
			Shell{"bash", []string{"-c"}},
		},
		"windows with pwsh": {
			domain.PlatformWindows,
			[]string{"pwsh"},
			Shell{"pwsh", []string{"-NoProfile", "-Command"}},
		},
		"windows without pwsh": {
			domain.PlatformWindows,
			nil,
			Shell{"powershell", []string{"-NoProfile", "-Command"}},
		},
		"windows with powershell only": {
			domain.PlatformWindows,
			[]string{"powershell"},
			Shell{"powershell", []string{"-NoProfile", "-Command"}},
		},
		"unknown platform": {
			domain.Platform("solaris"),
			[]string{"bash"},
			Shell{"bash", []string{"-c"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := GetShell(tc.platform, lookPathSet(tc.found...))
			assert.True(t, got.Equal(tc.want), "GetShell = %+v, want %+v", got, tc.want)
		})
	}
}

func TestGetShellNilLookPath(t *testing.T) {
	got := GetShell(domain.PlatformLinux, nil)
	assert.False(t, got.Name != "sh" && got.Name != "bash", "GetShell with nil LookPath = %+v", got)

	assert.True(t, got.Valid(), "shell reported invalid")
}

func TestShellHelpers(t *testing.T) {
	s := Shell{Name: "sh", Args: []string{"-c"}}
	got := s.String()
	assert.Equal(t, "sh -c", got, "String() = %q", got)

	cmd := s.Command("echo hi")
	assert.False(
		t,
		cmd.Path == "" || len(cmd.Args) != 3 || cmd.Args[2] != "echo hi",
		"Command = %v",
		cmd.Args,
	)
	// The receiver's Args slice must not be mutated by Command.
	again := s.Command("echo again")
	assert.Equal(t, "-c", s.Args[0], "Args mutated: %v", s.Args)

	assert.Equal(t, "echo again", again.Args[2], "second Command = %v", again.Args)

	bare := Shell{Name: "sh"}
	got = bare.String()
	assert.Equal(t, "sh", got, "String() with no args = %q", got)

	if (Shell{}).Valid() {
		assert.Fail(t, "empty shell reported valid")
	}

	if (Shell{Name: " "}).Valid() {
		assert.Fail(t, "whitespace-only name reported valid")
	}
}

func TestResolveScriptRunnerWindows(t *testing.T) {
	cases := map[string]struct {
		path  string
		found []string
		want  Shell
	}{
		"powershell script": {
			"C:\\hooks\\setup.ps1",
			[]string{"pwsh"},
			Shell{"pwsh", []string{"-NoProfile", "-File"}},
		},
		"ps1 without pwsh": {
			"C:\\hooks\\setup.ps1",
			nil,
			Shell{"powershell", []string{"-NoProfile", "-File"}},
		},
		"batch file": {
			"C:\\hooks\\setup.bat",
			nil,
			Shell{"cmd", []string{"/C"}},
		},
		"cmd file": {
			"C:\\hooks\\setup.cmd",
			nil,
			Shell{"cmd", []string{"/C"}},
		},
		"uppercase ps1": {
			"C:\\hooks\\SETUP.PS1",
			[]string{"pwsh"},
			Shell{"pwsh", []string{"-NoProfile", "-File"}},
		},
		"no extension": {
			"C:\\hooks\\setup",
			nil,
			Shell{"powershell", []string{"-NoProfile", "-Command"}},
		},
		"unknown extension": {
			"C:\\hooks\\setup.py",
			nil,
			Shell{"powershell", []string{"-NoProfile", "-Command"}},
		},
		"with pwsh available for unknown ext": {
			"C:\\hooks\\setup",
			[]string{"pwsh"},
			Shell{"pwsh", []string{"-NoProfile", "-Command"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveScriptRunner(
				domain.PlatformWindows,
				tc.path,
				lookPathSet(tc.found...),
			)
			require.NoError(t, err, "ResolveScriptRunner")

			assert.True(t, got.Equal(tc.want), "ResolveScriptRunner = %+v, want %+v", got, tc.want)
		})
	}
}

func TestResolveScriptRunnerUnix(t *testing.T) {
	dir := t.TempDir()

	shScript := filepath.Join(dir, "hook.sh")
	err := os.WriteFile(shScript, []byte("#!/bin/sh\necho hi\n"), 0o755)
	require.NoError(t, err)

	got, err := ResolveScriptRunner(domain.PlatformLinux, shScript, lookPathSet())
	require.NoError(t, err, "ResolveScriptRunner")

	if want := (Shell{Name: "/bin/sh"}); !got.Equal(want) {
		assert.Fail(t, "ResolveScriptRunner = %+v, want %+v", got, want)
	}

	envScript := filepath.Join(dir, "hook.py")
	err = os.WriteFile(envScript, []byte("#!/usr/bin/env python3\nprint(1)\n"), 0o755)
	require.NoError(t, err)

	got, err = ResolveScriptRunner(domain.PlatformLinux, envScript, lookPathSet())
	require.NoError(t, err, "ResolveScriptRunner")

	assert.Equal(t, "python3", got.Name, "interpreter = %q, want python3", got.Name)

	// A script with no shebang falls back to the platform shell.
	plain := filepath.Join(dir, "plain")
	err = os.WriteFile(plain, []byte("echo hi\n"), 0o644)
	require.NoError(t, err)

	got, err = ResolveScriptRunner(domain.PlatformLinux, plain, lookPathSet())
	require.NoError(t, err, "ResolveScriptRunner")

	assert.False(
		t,
		got.Name != "sh" || len(got.Args) != 1 || got.Args[0] != "-c",
		"fallback = %+v, want sh -c",
		got,
	)

	// An empty file has no shebang either.
	empty := filepath.Join(dir, "empty")
	err = os.WriteFile(empty, nil, 0o644)
	require.NoError(t, err)

	_, err = ResolveScriptRunner(domain.PlatformLinux, empty, lookPathSet())
	assert.NoError(t, err, "ResolveScriptRunner on an empty file: %v", err)
}

func TestResolveScriptRunnerErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := ResolveScriptRunner(domain.PlatformLinux, "", nil)
	assert.Error(t, err, "ResolveScriptRunner with an empty path succeeded, want error")

	_, err = ResolveScriptRunner(domain.PlatformLinux, "   ", nil)
	assert.Error(t, err, "ResolveScriptRunner with a blank path succeeded, want error")

	_, err = ResolveScriptRunner(domain.PlatformLinux, filepath.Join(dir, "missing"), nil)
	assert.Error(t, err, "ResolveScriptRunner on a missing file succeeded, want error")

	// A broken shebang must fail loudly instead of running the script with the
	// wrong interpreter.
	bad := filepath.Join(dir, "bad")
	err = os.WriteFile(bad, []byte("#!\n"), 0o755)
	require.NoError(t, err)

	_, err = ResolveScriptRunner(domain.PlatformLinux, bad, nil)
	assert.Error(t, err, "ResolveScriptRunner with an empty shebang succeeded, want error")
}

func TestReadShebang(t *testing.T) {
	dir := t.TempDir()

	cases := map[string]struct {
		content string
		want    string
	}{
		"plain":            {"#!/bin/bash\necho hi\n", "#!/bin/bash"},
		"crlf":             {"#!/bin/bash\r\necho hi\r\n", "#!/bin/bash"},
		"second line":      {"\n#!/bin/bash\n", ""},
		"no trailing nl":   {"#!/usr/bin/env python3", "#!/usr/bin/env python3"},
		"absent":           {"echo hi\n", ""},
		"comment first":    {"# a comment\n#!/bin/sh\n", ""},
		"empty":            {"", ""},
		"interpreter args": {"#!/bin/sh -e -x\n", "#!/bin/sh -e -x"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "script-"+strings.ReplaceAll(name, " ", "-"))
			err := os.WriteFile(path, []byte(tc.content), 0o755)
			require.NoError(t, err)

			got, err := ReadShebang(path)
			require.NoError(t, err, "ReadShebang")

			assert.Equal(t, tc.want, got, "ReadShebang = %q, want %q", got, tc.want)
		})
	}

	_, err := ReadShebang("")
	assert.Error(t, err, "ReadShebang(\"\") succeeded, want error")

	_, err = ReadShebang(filepath.Join(dir, "missing"))
	assert.Error(t, err, "ReadShebang on a missing file succeeded, want error")
}

func TestReadShebangOnDirectory(t *testing.T) {
	// A directory can be opened but not read as a shebang; either way the
	// function must not claim a shebang exists.
	got, err := ReadShebang(t.TempDir())
	if err != nil {
		return
	}

	assert.Equal(t, "", got, "ReadShebang on a directory = %q, want empty", got)
}

func TestReadShebangLongFirstLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge")

	content := "#!" + strings.Repeat("a", maxShebangLine*2) + "\n"
	err := os.WriteFile(path, []byte(content), 0o755)
	require.NoError(t, err)

	got, err := ReadShebang(path)
	require.NoError(t, err, "ReadShebang")

	assert.False(
		t,
		len(got) > maxShebangLine,
		"shebang length = %d, want at most %d",
		len(got),
		maxShebangLine,
	)
}

func TestParseShebang(t *testing.T) {
	cases := map[string]struct {
		line string
		want Shell
	}{
		"absolute":      {"#!/bin/bash", Shell{"/bin/bash", nil}},
		"absolute+args": {"#!/bin/bash -e -x", Shell{"/bin/bash", []string{"-e", "-x"}}},
		"env":           {"#!/usr/bin/env python3", Shell{"python3", nil}},
		"env with args": {"#!/usr/bin/env node --harmony", Shell{"node", []string{"--harmony"}}},
		"env with -S":   {"#!/usr/bin/env -S python3 -u", Shell{"python3", []string{"-u"}}},
		"env with -i":   {"#!/usr/bin/env -i python3", Shell{"python3", nil}},
		"env named":     {"#!/bin/env ruby", Shell{"ruby", nil}},
		"extra spaces":  {"#!   /bin/sh   -x  ", Shell{"/bin/sh", []string{"-x"}}},
		"with shebang":  {"#!/usr/bin/perl -w", Shell{"/usr/bin/perl", []string{"-w"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseShebang(tc.line)
			require.NoError(t, err, "ParseShebang")

			assert.Equal(t, tc.want.Name, got.Name, "Name = %q, want %q", got.Name, tc.want.Name)

			assert.Equal(
				t,
				strings.Join(tc.want.Args, " "),
				strings.Join(got.Args, " "),
				"Args = %v, want %v",
				got.Args,
				tc.want.Args,
			)
		})
	}
}

func TestParseShebangRejects(t *testing.T) {
	cases := map[string]string{
		"empty":                   "",
		"whitespace":              "   ",
		"bare hash bang":          "#",
		"hash only":               "#!",
		"no shebang":              "/bin/bash",
		"env only":                "#!/usr/bin/env",
		"env flags only":          "#!/usr/bin/env -S",
		"env flag no interpreter": "#!/usr/bin/env -i",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseShebang(line)
			require.Error(t, err, "ParseShebang(%q) = %+v, want error", line, got)

			assert.True(
				t,
				errors.IsAppError(err, errors.ErrCodeRunnerNotFound),
				"error code = %q, want %q",
				errors.CodeOf(err),
				errors.ErrCodeRunnerNotFound,
			)
		})
	}
}
