package platform

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Shell is an interpreter invocation: the program plus the fixed leading
// arguments that make it read a script or a command string.
type Shell struct {
	// Name is the executable to run, without arguments.
	Name string
	// Args are the arguments that always precede the script or command.
	Args []string
}

// Command renders the invocation for a script or inline command.
func (s Shell) Command(script string) *exec.Cmd {
	return exec.Command(s.Name, append(append([]string{}, s.Args...), script)...)
}

// CommandContext binds the invocation to ctx, so cancelling the context kills
// the process instead of leaving it running. Callers that only need to inspect
// the invocation should use Command.
func (s Shell) CommandContext(ctx context.Context, script string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}

	return exec.CommandContext(ctx, s.Name, append(append([]string{}, s.Args...), script)...)
}

// String implements fmt.Stringer.
func (s Shell) String() string {
	if len(s.Args) == 0 {
		return s.Name
	}

	return s.Name + " " + strings.Join(s.Args, " ")
}

// Valid reports whether the shell names a program.
func (s Shell) Valid() bool { return strings.TrimSpace(s.Name) != "" }

// Equal compares two shells. A nil Args slice and an empty one are equal, since
// both mean "no fixed arguments".
func (s Shell) Equal(other Shell) bool {
	if s.Name != other.Name || len(s.Args) != len(other.Args) {
		return false
	}

	for i := range s.Args {
		if s.Args[i] != other.Args[i] {
			return false
		}
	}

	return true
}

// LookPath resolves an executable on PATH. os/exec.LookPath satisfies it.
type LookPath func(file string) (string, error)

// GetShell returns the shell used to run inline hook commands for a platform.
// Windows prefers PowerShell 7 when present, because it behaves like the POSIX
// shells the hook documentation is written against.
func GetShell(p domain.Platform, look LookPath) Shell {
	if look == nil {
		look = exec.LookPath
	}

	if p.IsWindows() {
		if _, err := look("pwsh"); err == nil {
			return Shell{Name: "pwsh", Args: []string{"-NoProfile", "-Command"}}
		}

		return Shell{Name: "powershell", Args: []string{"-NoProfile", "-Command"}}
	}

	if _, err := look("bash"); err == nil {
		return Shell{Name: "bash", Args: []string{"-c"}}
	}

	return Shell{Name: "sh", Args: []string{"-c"}}
}

// ResolveScriptRunner picks the interpreter for a script file. On Windows the
// extension decides; elsewhere a shebang decides, and a script without one falls
// back to the platform shell.
func ResolveScriptRunner(p domain.Platform, scriptPath string, look LookPath) (Shell, error) {
	if strings.TrimSpace(scriptPath) == "" {
		return Shell{}, errors.New(
			errors.ErrCodeRunnerNotFound,
			"cannot resolve a runner for an empty script path",
		)
	}

	if p.IsWindows() {
		switch strings.ToLower(filepath.Ext(scriptPath)) {
		case ".ps1":
			return windowsScriptRunner(look), nil
		case ".bat", ".cmd":
			return Shell{Name: "cmd", Args: []string{"/C"}}, nil
		default:
			// No extension (or an unknown one): run it through the platform
			// shell, which for Windows means PowerShell.
			return GetShell(p, look), nil
		}
	}

	shebang, err := ReadShebang(scriptPath)
	if err != nil {
		return Shell{}, err
	}

	if shebang == "" {
		return GetShell(p, look), nil
	}

	shell, err := ParseShebang(shebang)
	if err != nil {
		return Shell{}, err
	}

	return shell, nil
}

// windowsScriptRunner picks the PowerShell flavour used to run a .ps1 file,
// matching the preference order used for inline commands.
func windowsScriptRunner(look LookPath) Shell {
	if look == nil {
		look = exec.LookPath
	}

	name := "pwsh"
	if _, err := look("pwsh"); err != nil {
		name = "powershell"
	}

	return Shell{Name: name, Args: []string{"-NoProfile", "-File"}}
}

// maxShebangLine bounds how much of a file is read while hunting for a shebang,
// so pointing at a large binary cannot exhaust memory. 4 KiB comfortably covers
// every real interpreter line.
const maxShebangLine = 4096

// ReadShebang returns the interpreter line of a script, or "" when the file has
// none. A file that cannot be read is an error.
func ReadShebang(scriptPath string) (string, error) {
	if strings.TrimSpace(scriptPath) == "" {
		return "", errors.New(errors.ErrCodeHookInvalid, "cannot read a shebang from an empty path")
	}

	f, err := os.Open(scriptPath)
	if err != nil {
		return "", errors.Wrap(err, errors.ErrCodeHookInvalid, "open hook script %s", scriptPath)
	}
	defer f.Close()

	buf := make([]byte, maxShebangLine)

	n, err := f.Read(buf)
	if n == 0 {
		if err != nil && err != io.EOF {
			return "", errors.Wrap(
				err,
				errors.ErrCodeHookInvalid,
				"read hook script %s",
				scriptPath,
			)
		}

		return "", nil
	}

	line := strings.TrimRight(string(buf[:n]), "\x00")
	if idx := strings.IndexAny(line, "\n\r"); idx >= 0 {
		line = line[:idx]
	}

	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#!") {
		return "", nil
	}

	return line, nil
}

// ParseShebang turns an interpreter line into a Shell invocation. Both
// "/bin/bash -e" and "/usr/bin/env bash" forms are understood, as is env's "-S"
// split form. A malformed line is an error: not a silent fallback,
// because running a script with the wrong interpreter produces baffling failures.
func ParseShebang(line string) (Shell, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return Shell{}, errors.New(errors.ErrCodeRunnerNotFound, "empty shebang")
	}

	if !strings.HasPrefix(trimmed, "#!") {
		return Shell{}, errors.New(errors.ErrCodeRunnerNotFound,
			"shebang line %q does not start with #!", trimmed)
	}

	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(trimmed, "#!")))
	if len(fields) == 0 {
		return Shell{}, errors.New(
			errors.ErrCodeRunnerNotFound,
			"shebang line %q names no interpreter",
			trimmed,
		)
	}

	program := fields[0]
	args := append([]string{}, fields[1:]...)

	if filepath.Base(program) != "env" {
		if program == "" {
			return Shell{}, errors.New(
				errors.ErrCodeRunnerNotFound,
				"shebang line %q names no interpreter",
				trimmed,
			)
		}

		return Shell{Name: program, Args: args}, nil
	}

	// env form: "-S" splits one word and passes the rest to the interpreter;
	// without it the first non-flag word is the interpreter.
	rest := args
	for len(rest) > 0 && isFlag(rest[0]) {
		switch {
		case rest[0] == "-S" || rest[0] == "--split-string":
			if len(rest) < 2 {
				return Shell{}, errors.New(errors.ErrCodeRunnerNotFound,
					"shebang line %q uses env -S without an interpreter", trimmed)
			}

			interp := strings.Fields(rest[1])
			if len(interp) == 0 {
				return Shell{}, errors.New(errors.ErrCodeRunnerNotFound,
					"shebang line %q uses env -S without an interpreter", trimmed)
			}

			return Shell{
				Name: interp[0],
				Args: append(append([]string{}, interp[1:]...), rest[2:]...),
			}, nil
		case envFlagsWithValue[rest[0]]:
			// These env options consume the following word, so skipping only the
			// flag would make the variable name look like the interpreter.
			if len(rest) < 2 {
				return Shell{}, errors.New(errors.ErrCodeRunnerNotFound,
					"shebang line %q uses env %s without a value", trimmed, rest[0])
			}

			rest = rest[2:]
		default:
			rest = rest[1:]
		}
	}

	if len(rest) == 0 || rest[0] == "" {
		return Shell{}, errors.New(errors.ErrCodeRunnerNotFound,
			"shebang line %q uses env without naming an interpreter", trimmed)
	}

	return Shell{Name: rest[0], Args: rest[1:]}, nil
}

// envFlagsWithValue are the env options that take a separate argument.
var envFlagsWithValue = map[string]bool{
	"-u": true, "--unset": true,
	"-C": true, "--chdir": true,
	"-a": true, "--argv0": true,
}

// isFlag reports whether a shebang word is an option: not a program name.
// A bare "-" is stdin, which is not a flag.
func isFlag(word string) bool {
	return len(word) > 1 && strings.HasPrefix(word, "-")
}
