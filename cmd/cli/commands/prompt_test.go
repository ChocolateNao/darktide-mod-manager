package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePrompter answers a prompt without a terminal, which is the only reason
// keyPrompter is an interface at all: term.ReadPassword needs a real descriptor,
// and the behaviour around it does not.
type fakePrompter struct {
	err     error
	answer  string
	prompts []string
	asked   int
	waits   bool
}

// ReadKey waits on ctx before answering, so a cancelled run is a cancellation
// rather than a value that happened to be ready.
func (f *fakePrompter) ReadKey(ctx context.Context, prompt string) (string, error) {
	f.asked++

	f.prompts = append(f.prompts, prompt)
	if f.waits {
		<-ctx.Done()

		return "", appErrors.Wrap(ctx.Err(), appErrors.ErrCodeOperationCanceled,
			"the prompt was cancelled; nothing was stored")
	}

	return f.answer, f.err
}

// The question has to name the source, or somebody reading it does not know
// which of several credentials is being asked for.
func TestPromptForKeyNamesTheSourceAndSaysHowToFinish(t *testing.T) {
	ask := &fakePrompter{answer: "  secret-key  "}

	key, err := promptForKey(t.Context(), io.Discard, ask, "nexus")
	require.NoError(t, err, "promptForKey")

	assert.Equal(t, "secret-key", key, "key = %q, want it trimmed", key)

	assert.Equal(t, 1, ask.asked, "asked %d times, want exactly one line read", ask.asked)

	prompt := ask.askedPrompt(t)
	assert.Contains(t, prompt, "nexus API key", "prompt = %q, want it to name the source", prompt)
	// Submitting is pressing Enter, so the prompt has to say so rather than leave
	// the person guessing whether more input is wanted.
	assert.Contains(
		t,
		prompt,
		"Enter",
		"prompt = %q, want it to say how to submit and how to stop",
		prompt,
	)
	assert.Contains(
		t,
		prompt,
		"Ctrl-C",
		"prompt = %q, want it to say how to submit and how to stop",
		prompt,
	)
}

func TestPromptForKeyTakesEnterAsTheConfirmation(t *testing.T) {
	ask := &fakePrompter{answer: "abc"}

	key, err := promptForKey(t.Context(), io.Discard, ask, "")
	require.NoError(t, err, "promptForKey = %q, %v, want the typed key and no error", key, err)
	assert.Equal(t, "abc", key, "promptForKey = %q, %v, want the typed key and no error", key, err)

	assert.Contains(
		t,
		ask.askedPrompt(t),
		"API key",
		"want a generic label when the source was not named",
	)
}

// askedPrompt returns the question the fake was asked, so a test can read it
// without having to model a terminal's output.
func (f *fakePrompter) askedPrompt(t *testing.T) string {
	t.Helper()

	require.Len(t, f.prompts, 1, "asked %d questions, want exactly one", len(f.prompts))

	return f.prompts[0]
}

// A cancelled prompt must not be reportable as a missing argument: the exit code
// is what tells "I stopped" apart from "this failed", and a script wrapping this
// branches on exactly that.
func TestPromptForKeyReportsAnAbandonedLineAsCancellation(t *testing.T) {
	for _, answer := range []string{"", "   ", "\t\n"} {
		key, err := promptForKey(t.Context(), io.Discard, &fakePrompter{answer: answer}, "nexus")
		assert.Equal(t, "", key, "promptForKey(%q) returned a key %q", answer, key)

		assert.True(
			t,
			appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
			"promptForKey(%q) = %v, want ErrCodeOperationCanceled",
			answer,
			err,
		)

		assert.Contains(t, appErrors.Message(err), "nothing was stored",
			"complaint = %q, want it to say that nothing was stored",
			appErrors.Message(err),
		)
	}
}

// An interrupted read — Ctrl-C is a signal, so the read itself fails — must be
// reported the same way an empty line is: nobody entered a key, and nothing was
// sent to the source before they stopped.
func TestPromptForKeyPropagatesAnInterruptedRead(t *testing.T) {
	interrupted := errors.New("read: interrupted by signal")

	key, err := promptForKey(t.Context(), io.Discard, &fakePrompter{err: interrupted}, "nexus")
	assert.Equal(t, "", key, "an interrupted read returned a key %q", key)
	// promptForKey passes the read's own error on rather than wrapping it again:
	// the prompter that reports OperationCanceled is the one that knows a
	// terminal was involved, and a second wrapper would bury its complaint.
	assert.True(
		t,
		errors.Is(err, interrupted),
		"error = %v, want the read's own error, unchanged",
		err,
	)
}

// A prompter that honours ctx is the whole contract; the terminal one below is
// what makes it necessary.
func TestPromptForKeyStopsWhenTheRunIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	ask := &fakePrompter{answer: "too-late", waits: true}
	go func() {
		for ask.asked == 0 {
			time.Sleep(time.Millisecond)
		}

		cancel()
	}()

	key, err := promptForKey(ctx, io.Discard, ask, "nexus")
	assert.Equal(t, "", key, "a cancelled prompt returned a key %q", key)

	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeOperationCanceled),
		"error = %v, want ErrCodeOperationCanceled",
		err,
	)

	assert.True(
		t,
		errors.Is(err, context.Canceled),
		"error = %v, want the cancellation kept as the cause",
		err,
	)
}

// The line reader is exercised through a pipe rather than a terminal: what it
// does with the bytes it is given does not depend on where they came from, and a
// test cannot portably have a terminal. The mode the terminal is put into is the
// part that cannot be covered here.
func TestReadLineUnderstandsTheKeysAPersonPresses(t *testing.T) {
	for _, tc := range []struct {
		err   error
		name  string
		typed string
		want  string
	}{
		{name: "Enter submits", typed: "abc\n", want: "abc"},
		{name: "a carriage return submits too", typed: "abc\r", want: "abc"},
		{name: "backspace erases", typed: "abcde\x7f", want: "abcd"},
		{name: "control-H erases", typed: "abcde\b", want: "abcd"},
		{name: "backspace on an empty line is nothing", typed: "\b\bab\n", want: "ab"},
		{name: "Ctrl-D on an empty line submits nothing", typed: "\x04", want: ""},
		{name: "Ctrl-D after a line submits it", typed: "abc\x04", want: "abc"},
		{name: "Ctrl-C cancels", typed: "abc\x03", err: errNoLine},
		{name: "Ctrl-C on an empty line cancels", typed: "\x03", err: errNoLine},
		{
			name:  "a line is only what came before the newline",
			typed: "abc\nignored", want: "abc",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err, "create a pipe")
			// The bytes are written before the read so the test does not depend on
			// a pipe's buffer being big enough to take them all at once.
			go func() {
				defer writer.Close()

				_, _ = io.WriteString(writer, tc.typed)
			}()

			got, err := readLine(reader, maxKeyLine)
			_ = reader.Close()

			if tc.err != nil {
				require.True(
					t,
					errors.Is(err, tc.err),
					"readLine(%q) error = %v, want %v",
					tc.typed,
					err,
					tc.err,
				)

				return
			}

			require.NoError(t, err, "readLine(%q)", tc.typed)

			assert.Equal(t, tc.want, got, "readLine(%q) = %q, want %q", tc.typed, got, tc.want)
		})
	}
}

// A line is accumulated in memory, so a stuck key cannot be allowed to grow it
// without limit. The bound is far above any real key.
func TestReadLineRefusesALineLongerThanAnyKey(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err, "create a pipe")

	go func() {
		defer writer.Close()

		_, _ = io.WriteString(writer, strings.Repeat("k", maxKeyLine+10)+"\n")
	}()

	_, err = readLine(
		reader,
		maxKeyLine,
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	), "error = %v, want ErrCodeInvalidArgument", err)

	_ = reader.Close()
}

// A closed terminal delivers no bytes and no error, which a reader that waits
// for either would wait on forever. The person closed their terminal, so the
// prompt has to come back and say so.
func TestReadLineReturnsWhenTheTerminalSaysNothingMore(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err, "create a pipe")

	_ = writer.Close()
	got, err := readLine(reader, maxKeyLine)
	_ = reader.Close()

	assert.NoError(t, err, "readLine on a closed pipe: %v", err)

	assert.Equal(t, "", got, "readLine = %q, want nothing typed", got)
}

// terminalPrompterFor is nil unless standard input is a terminal. A nil result
// must mean "not interactive" rather than a value to dereference.
// terminalPrompterFor returns the interface for this reason, and this is the test
// that keeps it that way: a *terminalPrompter that is nil, boxed into the
// interface, is an interface that is not nil, and every call through it
// dereferences nothing.
func TestTerminalPrompterForIsNilUnlessStandardInputIsATerminal(t *testing.T) {
	// A strings.Reader is what a test and an embedder both supply; neither has a
	// descriptor, so neither is interactive.
	ask := terminalPrompterFor(strings.NewReader("key"), &bytes.Buffer{})
	assert.Nil(t, ask, "a reader that is not a file was taken for a terminal")

	ask = terminalPrompterFor(&bytes.Buffer{}, &bytes.Buffer{})
	assert.Nil(t, ask, "a buffer was taken for a terminal")

	ask = terminalPrompterFor(nil, &bytes.Buffer{})
	assert.Nil(t, ask, "a nil reader was taken for a terminal")
	// A regular file has a descriptor but no grid and no line discipline, which is
	// the case a descriptor check alone would get wrong: prompting on one would
	// read a file as though a person were typing into it.
	file, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	require.NoError(t, err, "create a file")

	defer file.Close()

	ask = terminalPrompterFor(file, &bytes.Buffer{})
	assert.Nil(t, ask, "a regular file was taken for a terminal")
}

// A pipe is finite and ends on its own, so it is read to the end and whatever is
// in it is the key. That behaviour has to survive alongside the prompt, because
// this is the path every script uses.
func TestReadKeyReadsAPipeToTheEnd(t *testing.T) {
	var out, errOut bytes.Buffer

	s := &session{env: Env{In: strings.NewReader("  piped-key\n"), Out: &out, Err: &errOut}}

	key, err := s.readKey(t.Context(), "nexus")
	require.NoError(t, err, "readKey")

	assert.Equal(t, "piped-key", key, "key = %q, want the piped value trimmed", key)

	assert.Equal(
		t,
		0,
		out.Len(),
		"a pipe was prompted for: out=%q err=%q",
		out.String(),
		errOut.String(),
	)
	assert.Equal(
		t,
		0,
		errOut.Len(),
		"a pipe was prompted for: out=%q err=%q",
		out.String(),
		errOut.String(),
	)
}

// An unbounded read of `dmm ... < /dev/zero` ends by exhausting memory rather
// than by complaining, so the read is bounded. The bound is far above any real
// key, which is why hitting it is worth an error rather than a truncation.
func TestReadKeyBoundsWhatAPipeCanHandIt(t *testing.T) {
	flooded := strings.Repeat("k", maxKeyFromStdin+1024)

	key, err := (&session{env: Env{In: strings.NewReader(flooded)}}).readKey(t.Context(), "nexus")
	require.NoError(t, err, "readKey")

	assert.Equal(
		t,
		maxKeyFromStdin,
		len(key),
		"read %d bytes, want the read capped at %d",
		len(key),
		maxKeyFromStdin,
	)
}

func TestReadKeyReportsAFailedPipe(t *testing.T) {
	s := &session{env: Env{In: &brokenReader{}}}
	_, err := s.readKey(
		t.Context(),
		"nexus",
	)
	assert.True(t, appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	), "error = %v, want ErrCodeInvalidArgument", err)
}

// brokenReader fails on the first read, standing in for a pipe that closes with
// an error rather than at end of file.
type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("pipe closed") }

// An explicit choice is never second-guessed by a prompt. Somebody who named a
// source of the key has said where it is; quietly reading a different one would
// make the flag's name a lie, and a prompt they have to dismiss instead of
// answer is not an answer.
func TestResolveAPIKeyPrefersAnExplicitChoiceOverTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		apiKey   string
		fromPipe string
		env      map[string]string
		want     string
	}{
		{
			name: "the flag wins", apiKey: "from-flag",
			env: map[string]string{"DMM_NEXUS_API_KEY": "from-env"}, want: "from-flag",
		},
		{
			name: "stdin beats the environment", fromPipe: "from-pipe",
			env: map[string]string{"DMM_NEXUS_API_KEY": "from-env"}, want: "from-pipe",
		},
		{
			name: "the derived environment variable is the last resort",
			env:  map[string]string{"DMM_NEXUS_API_KEY": "from-env"}, want: "from-env",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer

			s := &session{env: Env{In: strings.NewReader(tc.fromPipe), Err: &errOut}}
			for name, value := range tc.env {
				t.Setenv(name, value)
			}

			key, err := s.resolveAPIKey(t.Context(), "nexus", tc.apiKey, tc.fromPipe != "", "")
			require.NoError(t, err, "resolveAPIKey")

			assert.Equal(t, tc.want, key, "key = %q, want %q", key, tc.want)
		})
	}
}

func TestResolveAPIKeyRefusesContradictoryFlags(t *testing.T) {
	var errOut bytes.Buffer

	s := &session{env: Env{In: strings.NewReader("from-pipe"), Err: &errOut}}

	_, err := s.resolveAPIKey(t.Context(), "nexus", "from-flag", true, "")
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want ErrCodeInvalidArgument",
		err,
	)
}

// A named environment variable that is empty is a mistake to report, not a gap
// to fill: the flag said where the key is, so reading anywhere else would be a
// different key than the one that was asked for.
func TestResolveAPIKeyReportsANamedVariableThatIsEmpty(t *testing.T) {
	t.Setenv("DMM_TEST_EMPTY_KEY", "")

	s := &session{env: Env{In: strings.NewReader("")}}

	_, err := s.resolveAPIKey(t.Context(), "nexus", "", false, "DMM_TEST_EMPTY_KEY")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want ErrCodeInvalidArgument",
		err,
	)

	assert.Contains(
		t,
		appErrors.Message(err),
		"DMM_TEST_EMPTY_KEY",
		"complaint = %q, want it to name the variable",
		appErrors.Message(err),
	)
}

// Without a terminal and without a flag there is nothing to read, and the message
// has to name the environment variable to set: that is the one instruction a
// script can act on.
func TestResolveAPIKeyNamesTheVariableWhenThereIsNothingToRead(t *testing.T) {
	t.Setenv("DMM_NEXUS_API_KEY", "")

	s := &session{env: Env{In: strings.NewReader("")}}

	_, err := s.resolveAPIKey(t.Context(), "nexus", "", false, "")
	require.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want ErrCodeInvalidArgument",
		err,
	)

	assert.Contains(
		t,
		appErrors.Message(err),
		"DMM_NEXUS_API_KEY",
		"complaint = %q, want it to name DMM_NEXUS_API_KEY",
		appErrors.Message(err),
	)
}

// The derived name is what the help and the error messages both point at, so it
// has to match the source id the user typed.
func TestDefaultKeyEnvVarNamesTheSource(t *testing.T) {
	for source, want := range map[string]string{
		"nexus":  "DMM_NEXUS_API_KEY",
		"vortex": "DMM_VORTEX_API_KEY",
		"my-src": "DMM_MY_SRC_API_KEY",
	} {
		got := defaultKeyEnvVar(source)
		assert.Equal(t, want, got, "defaultKeyEnvVar(%q) = %q, want %q", source, got, want)
	}
}
