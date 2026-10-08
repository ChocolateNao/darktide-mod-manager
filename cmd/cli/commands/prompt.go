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

// This file is the CLI's one interaction with a person who is at a terminal
// rather than running a script. There is exactly one such interaction — reading
// a credential — and it is kept apart from the command that needs it for two
// reasons.
//
// The first is that the difference between a pipe and a terminal changes what
// the command has to do. A pipe ends on its own; a terminal does not, so a
// command that reads one to end-of-file appears to hang, echoes the secret onto
// the screen while the person types it, and offers no way out short of killing
// the process. Telling the two apart is the whole job of this file.
//
// The second is testability. A real prompt needs a real terminal, which a test
// cannot portably have, so the read is behind an interface with one method. The
// logic around it — when to prompt, what to do with an empty line, which error a
// cancelled prompt produces — is then ordinary testable code, and the only part
// left unproven is the ioctl.

// keyPrompter asks for a secret and returns it, without echoing it.
//
// One method, and the narrowest one that does the job. It is an interface so the
// flow can be tested; it is not a seam for the commands, which never construct
// one.
//
// The prompt is passed in rather than written by the caller because the terminal
// prompter has to be in its raw state before it writes anything: a person typing
// while the prompt is still being composed has their keystrokes echoed by the
// terminal, which for a secret means printed on the screen. Only the prompter
// knows when that moment is.
type keyPrompter interface {
	// ReadKey writes prompt, then blocks until the person submits or abandons a
	// line, or until ctx is cancelled, and returns what was typed. An error means
	// nothing usable was typed and nothing was stored.
	ReadKey(ctx context.Context, prompt string) (string, error)
}

// terminalPrompter reads a line from a terminal with echo turned off.
type terminalPrompter struct {
	file *os.File
	err  io.Writer
}

// terminalPrompterFor returns a prompter for in when in is a terminal, and nil
// when it is not.
//
// A nil result is the ordinary answer, not a failure, and the caller has to cope
// with it. A pipe has to keep behaving like a pipe: a command that prompted
// anyway would consume the rest of what the script was feeding it, and a script
// cannot answer a question it never sees.
//
// It returns the interface rather than the concrete type, and returns an untyped
// nil in the non-terminal case, because that is the only version of this
// function whose nil is nil. A *terminalPrompter that happens to be nil, boxed
// into the interface, is an interface that is not nil, and every call through it
// then dereferences nothing. The distinction is invisible at the return
// statement and fatal at the call, so it is made once here instead of being left
// to each caller to get right.
//
// The reader is narrowed to an *os.File because that is the only thing a
// descriptor can be got from. Everything else — a test's strings.Reader, an
// embedder's buffer — is therefore non-interactive by construction, which is the
// right answer rather than a limitation.
func terminalPrompterFor(in io.Reader, errw io.Writer) keyPrompter {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return nil
	}

	return &terminalPrompter{file: file, err: errw}
}

// readResult is one outcome of the terminal read: either a line, or the reason
// there was not one.
type readResult struct {
	err error
	key string
}

// errNoLine is how a cancelled prompt says so. It is a value rather than
// io.EOF because the terminal reports end-of-file on an empty line as no error
// at all, and Ctrl-D on an empty line means "nothing was typed" rather than "the
// terminal closed".
var errNoLine = errors.New("no line was typed")

// The bytes raw mode delivers in place of the keys a terminal would otherwise
// turn into signals or line endings.
const (
	ctrlC   = 0x03
	ctrlD   = 0x04
	delete_ = 0x7f
)

// maxKeyLine bounds what a person can type into a hidden prompt. A key is a
// UUID or a fixed-length token, so this is far more than any real one, and it
// exists because the line is accumulated in memory and a held-down key is an
// unbounded amount of it.
const maxKeyLine = 512

// readHiddenLine reads one line from a terminal with echo off.
//
// The line is assembled here rather than by term.ReadPassword, and the terminal
// is put into raw mode to do it, because both of the ways out of a prompt that
// somebody reaches for do not work through that function.
//
// Ctrl-D: in canonical mode an empty line followed by Ctrl-D makes the terminal
// deliver a zero-byte read, and x/term's line reader treats "no bytes and no
// error" as keep waiting. It loops on it, so the prompt never returns and never
// responds to the keyboard again. Raw mode delivers Ctrl-D as the byte 0x04,
// which is an ordinary case here.
//
// Ctrl-C: the entry point installs a handler for SIGINT so that a long download
// can be cancelled, and a handled signal is no longer a default action — the
// interrupted read is resumed by the runtime. Raw mode turns ISIG off, so Ctrl-C
// arrives as the byte 0x03 instead and never becomes a signal to lose.
//
// Raw mode is also what keeps the secret off the screen: with echo off alone, a
// terminal that redraws its line on a resize can write it out, and the byte at a
// time read below is what lets backspace be handled without the terminal doing
// the drawing.
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
	// The prompt is written here, after raw mode is on rather than before it,
	// and written by this function rather than by the caller because a keystroke
	// arriving while the caller was still composing the prompt would be echoed by
	// the terminal, which for a secret means printed on the screen.
	//
	// It carries no line ending of its own. The one the person types to submit the
	// key is consumed by the raw terminal along with the rest of the line and is
	// written back by ReadKey once the terminal is restored, so adding one here
	// would move the answer up a line for no reason.
	if _, err := io.WriteString(promptTo, prompt); err != nil {
		return "", err
	}

	return readLine(file, maxKeyLine)
}

// readLine reads one line of raw terminal input, one byte at a time.
//
// One byte at a time is what makes the interesting cases visible rather than
// something the caller has to guess: a newline and Ctrl-D both end the line, and
// Ctrl-C ends the whole prompt. Backspace is handled here because raw mode does
// not do it, and without it a mistyped character could only be removed by
// cancelling and starting again.
func readLine(file *os.File, limit int) (string, error) {
	var (
		line []byte
		one  [1]byte
	)
	for len(line) <= limit {
		// A read that returns no bytes and no error is end-of-file rather than
		// more input, which is what a terminal reports for a closed connection.
		// Anything that loops on such a read never returns at all.
		got, err := file.Read(one[:])
		if got == 0 || err != nil {
			// Either way the line is over: a terminal that has been closed reports
			// no bytes and no error, and one that was closed with a line half typed
			// reports end-of-file with it. A line typed without a final Enter is
			// still a line, and an empty one is reported as no line at all, which
			// promptForKey then treats as a cancellation.
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
			// End of line on an empty line, which promptForKey reports as a
			// cancellation: nothing was typed, so there is nothing to store.
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

// ReadKey returns one line from the terminal without echoing it.
//
// All three ways out of a password prompt are ordinary ones: Enter submits,
// Ctrl-D submits an empty line, and Ctrl-C cancels. The third takes the work
// rather than assuming the read will notice it, because it will not.
//
// Reading blocks, and the entry point installs a handler for SIGINT so that a
// long download can be cancelled. Once a signal is handled it is not a default
// action any more, so the interrupted read is resumed by the runtime and the
// prompt sits there apparently ignoring the keyboard — the symptom that made an
// interactive credential check look broken in the first place. So the read
// happens on its own goroutine and the cancellation is awaited here, and the
// terminal is put back before returning.
//
// The terminal is restored explicitly rather than left to the goroutine, because
// that goroutine is still blocked and may never run its own restore. Leaving
// echo off would silently swallow everything the person typed for the rest of the
// session, which is a worse failure than the one being fixed.
func (t *terminalPrompter) ReadKey(ctx context.Context, prompt string) (string, error) {
	if !term.IsTerminal(int(t.file.Fd())) {
		return "", appErrors.Wrap(errNoLine, appErrors.ErrCodeOperationCanceled,
			"standard input stopped being a terminal; nothing was stored")
	}
	// The terminal's own state is captured before the read changes it, so the
	// cancellation path can put it back without waiting for the goroutine that is
	// still blocked inside it.
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
		// Put the terminal back here rather than waiting for the goroutine to
		// finish a read nobody is going to type into. What it left behind is raw
		// mode, so leaving it would take the line discipline with it: no echo,
		// no Ctrl-C, and no Enter either. The state is restored rather than reset
		// so a terminal that had been in some other mode is not silently changed.
		_ = term.Restore(descriptor, previous)

		return "", appErrors.Wrap(ctx.Err(), appErrors.ErrCodeOperationCanceled,
			"the prompt was cancelled; nothing was stored")
	}

	// The terminal consumed the newline without echoing it, so the cursor is
	// still sitting on the prompt line. Without this the next thing the program
	// prints lands in the middle of it and the shell's own prompt follows a
	// half-written line.
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

// promptForKey asks for a credential and returns what was typed.
//
// It writes to err rather than to out, and that is not a stylistic choice.
// Standard output is a contract this program keeps with whatever is reading it,
// and a prompt printed there lands in the middle of a table or a JSON document
// for anyone who piped one. Standard error is where the person at the terminal is
// already looking, and nothing in this package parses it.
//
// Pressing Enter is the confirmation: nothing is stored, and nothing is sent to
// the source, until a line has been completed. An empty line and a cancelled
// prompt both mean "never mind", and both are reported as a cancellation rather
// than as a missing argument — the exit code then distinguishes "I stopped" from
// "this failed", which is the difference a wrapping script cares about.
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

// prompter returns the prompter for this run's standard input, or nil when
// standard input is not a terminal.
func (s *session) prompter() keyPrompter {
	return terminalPrompterFor(s.env.In, s.env.Err)
}

// maxKeyFromStdin bounds what a pipe is read for. A Nexus key is a UUID and an
// API key from any of the other hosts is a fixed-length token, so 4 KiB is
// several orders of magnitude more than any real one. The bound exists because
// this reads a pipe rather than a file: an unbounded read of `dmm ... < /dev/zero`
// ends by exhausting memory instead of by complaining.
const maxKeyFromStdin = 1 << 12

// readKey takes the credential from standard input, asking for it on a terminal
// when standard input is one.
//
// The two cases are genuinely different operations rather than the same one
// reached differently. A pipe is finite and ends on its own, so it is read to the
// end and whatever is in it is the key. A terminal is neither finite nor
// self-terminating, so it is asked for a line: read to end-of-file, the command
// appears to hang, the secret is echoed as it is typed, and the only exit is a
// process signal — which is exactly the behaviour that makes an interactive
// `--api-key-stdin` look broken.
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
