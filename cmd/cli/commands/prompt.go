package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"golang.org/x/term"
)

// This file is the CLI's one terminal interaction: reading a credential. A pipe
// and a terminal are different things to read, so the read sits behind a
// one-method interface.

// keyPrompter asks for a secret without echoing it. The prompt is passed in
// because the terminal prompter must be in raw state before writing.
type keyPrompter interface {
	// ReadKey writes prompt, then blocks until the line is submitted or abandoned
	// or ctx is cancelled; an error means nothing was typed and nothing stored.
	ReadKey(ctx context.Context, prompt string) (string, error)
}

type terminalPrompter struct {
	file *os.File
	err  io.Writer
}

// terminalPrompterFor returns a prompter when in is a terminal, nil otherwise:
// a pipe has to keep behaving like a pipe. The untyped nil is deliberate: a
// boxed nil *terminalPrompter panics at the call.
func terminalPrompterFor(in io.Reader, errw io.Writer) keyPrompter {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return nil
	}

	return &terminalPrompter{file: file, err: errw}
}

// readResult is one outcome of the terminal read: a line, or why there was none.
type readResult struct {
	err error
	key string
}

// errNoLine is how a cancelled prompt says so. The terminal reports end-of-file
// on an empty line as no error, and Ctrl-D there means "nothing was typed".
var errNoLine = errors.New("no line was typed")

// The bytes raw mode delivers in place of signals and line endings.
const (
	ctrlC   = 0x03
	ctrlD   = 0x04
	delete_ = 0x7f
)

// maxKeyLine bounds what a person can type into a hidden prompt: a key is a UUID
// or a fixed-length token, and a held-down key is an unbounded amount of memory.
const maxKeyLine = 512

// readHiddenLine reads one line with echo off. Raw mode because term.ReadPassword
// cannot be escaped from (Ctrl-D in canonical mode loops forever), Ctrl-C is a
// handled SIGINT, and raw mode keeps the secret off the screen on redraws.
func readHiddenLine(
	file *os.File,
	previous *term.State,
	prompt string,
	promptTo io.Writer,
) (string, error) {
	if _, err := term.MakeRaw(int(file.Fd())); err != nil {
		return "", err
	}
	defer term.Restore(int(file.Fd()), previous)
	// Written here, after raw mode is on, or a keystroke arriving while the caller
	// composed the prompt is echoed. It adds no line ending; the raw terminal eats it.
	if _, err := io.WriteString(promptTo, prompt); err != nil {
		return "", err
	}

	return readLine(file, maxKeyLine)
}

// readLine reads one line of raw terminal input, one byte at a time. A newline
// and Ctrl-D end the line, Ctrl-C ends the prompt, and backspace is handled here.
func readLine(file *os.File, limit int) (string, error) {
	var (
		line []byte
		one  [1]byte
	)
	for len(line) <= limit {
		// A read with no bytes and no error is end-of-file; anything that loops
		// on it never returns.
		got, err := file.Read(one[:])
		if got == 0 || err != nil {
			// Either way the line is over. A line typed without a final Enter is
			// still a line; an empty one is a cancel.
			if len(line) > 0 {
				return string(line), nil
			}

			if got == 0 && errors.Is(err, io.EOF) {
				return "", nil
			}

			return "", err
		}

		switch one[0] {
		case '\n', '\r':
			return string(line), nil
		case ctrlC:
			return "", errNoLine
		case ctrlD:
			// End of line on an empty line; promptForKey reports this as a cancellation.
			return string(line), nil
		case '\b', delete_:
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		default:
			line = append(line, one[0])
		}
	}

	return string(line), appErrors.New(appErrors.ErrCodeInvalidArgument,
		"that is longer than any API key; nothing was stored")
}

// ReadKey returns one line from the terminal without echoing it. Enter submits,
// Ctrl-D submits an empty line, Ctrl-C cancels. The read runs on its own goroutine
// with the terminal restored here on cancellation, or echo stays off.
func (t *terminalPrompter) ReadKey(ctx context.Context, prompt string) (string, error) {
	if !term.IsTerminal(int(t.file.Fd())) {
		return "", appErrors.Wrap(errNoLine, appErrors.ErrCodeOperationCanceled,
			"standard input stopped being a terminal; nothing was stored")
	}
	// The terminal's state is captured before the read changes it, so cancellation
	// can restore it without waiting for the still-blocked goroutine.
	descriptor := int(t.file.Fd())

	previous, err := term.GetState(descriptor)
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled,
			"the terminal would not report its state; nothing was stored")
	}
	// answered carries one result, so a read that is abandoned here does not
	// block forever on an unbuffered send once the person gives up and types.
	answered := make(chan readResult, 1)

	go func() {
		key, err := readHiddenLine(t.file, previous, prompt, t.err)
		answered <- readResult{key: key, err: err}
	}()

	var typed readResult
	select {
	case typed = <-answered:
	case <-ctx.Done():
		// Restore here: the abandoned goroutine may never return, and raw mode left
		// behind means no echo, Ctrl-C or Enter for the rest of the session.
		_ = term.Restore(descriptor, previous)

		return "", appErrors.Wrap(ctx.Err(), appErrors.ErrCodeOperationCanceled,
			"the prompt was cancelled; nothing was stored")
	}

	// The terminal consumed the newline without echoing it, so the cursor is
	// still on the prompt line and the next print would land in the middle of it.
	if _, writeErr := fmt.Fprintln(t.err); writeErr != nil && typed.err == nil {
		typed.err = writeErr
	}

	if typed.err != nil {
		if errors.Is(typed.err, errNoLine) {
			return "", appErrors.Wrap(typed.err, appErrors.ErrCodeOperationCanceled,
				"the prompt was cancelled; nothing was stored")
		}

		return "", appErrors.Wrap(typed.err, appErrors.ErrCodeOperationCanceled,
			"no key was read; nothing was stored")
	}

	return typed.key, nil
}

// promptForKey asks for a credential and returns what was typed. It writes to
// err because standard output is a contract with whatever reads it. An empty
// line and a cancelled prompt both mean "never mind", reported as a cancellation.
func promptForKey(
	ctx context.Context,
	errw io.Writer,
	ask keyPrompter,
	sourceID string,
) (string, error) {
	label := strings.TrimSpace(sourceID)
	if label == "" {
		label = "mod source"
	}
	// The question is composed here and written by the prompter, which is the only
	// party that knows when the terminal is ready not to echo what is typed.
	prompt := fmt.Sprintf(
		"%s API key (input hidden; press Enter to submit, Ctrl-C to cancel): ",
		label,
	)

	key, err := ask.ReadKey(ctx, prompt)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(key) == "" {
		return "", appErrors.New(appErrors.ErrCodeOperationCanceled,
			"no key entered; nothing was stored")
	}

	return strings.TrimSpace(key), nil
}

// prompter returns the prompter for this run's standard input, nil for a pipe.
func (s *session) prompter() keyPrompter {
	return terminalPrompterFor(s.env.In, s.env.Err)
}

// maxKeyFromStdin bounds what a pipe is read for. 4 KiB is far more than any real
// key, and `dmm ... < /dev/zero` would otherwise exhaust memory.
const maxKeyFromStdin = 1 << 12

// readKey takes the credential from standard input, asking for it on a terminal.
// A pipe is finite and read to the end; a terminal is asked for a line, since
// reading to end-of-file appears to hang and echoes the secret.
func (s *session) readKey(ctx context.Context, sourceID string) (string, error) {
	if ask := s.prompter(); ask != nil {
		return promptForKey(ctx, s.env.Err, ask, sourceID)
	}

	read, err := io.ReadAll(io.LimitReader(s.env.In, maxKeyFromStdin))
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"cannot read the key from standard input")
	}

	return strings.TrimSpace(string(read)), nil
}
