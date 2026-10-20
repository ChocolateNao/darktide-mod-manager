package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePrompter struct {
	err     error
	answer  string
	prompts []string
	asked   atomic.Int64
	waits   bool
}

// ReadKey waits on ctx before answering, so a cancelled run is a cancellation.
func (f *fakePrompter) ReadKey(ctx context.Context, prompt string) (string, error) {
	f.asked.Add(1)

	f.prompts = append(f.prompts, prompt)
	if f.waits {
		<-ctx.Done()

		return "", appErrors.Wrap(ctx.Err(), appErrors.ErrCodeOperationCanceled,
			"the prompt was cancelled; nothing was stored")
	}

	return f.answer, f.err
}

func TestPromptForKeyNamesTheSourceAndSaysHowToFinish(t *testing.T) {
	ask := &fakePrompter{answer: "  secret-key  "}

	key, err := promptForKey(t.Context(), io.Discard, ask, "nexus")
	require.NoError(t, err, "promptForKey")

	assert.Equal(t, "secret-key", key, "key = %q, want it trimmed", key)

	assert.Equal(t, int64(1), ask.asked.Load(), "asked %d times, want exactly one line read",
		ask.asked.Load())

	prompt := ask.askedPrompt(t)
	assert.Contains(t, prompt, "nexus API key", "prompt = %q, want it to name the source", prompt)
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

func (f *fakePrompter) askedPrompt(t *testing.T) string {
	t.Helper()

	require.Len(t, f.prompts, 1, "asked %d questions, want exactly one", len(f.prompts))

	return f.prompts[0]
}

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

func TestPromptForKeyPropagatesAnInterruptedRead(t *testing.T) {
	interrupted := errors.New("read: interrupted by signal")

	key, err := promptForKey(t.Context(), io.Discard, &fakePrompter{err: interrupted}, "nexus")
	assert.Equal(t, "", key, "an interrupted read returned a key %q", key)
	// promptForKey passes the read's own error on; the terminal prompter reports
	// OperationCanceled.
	assert.True(
		t,
		errors.Is(err, interrupted),
		"error = %v, want the read's own error, unchanged",
		err,
	)
}

func TestPromptForKeyStopsWhenTheRunIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	ask := &fakePrompter{answer: "too-late", waits: true}
	go func() {
		for ask.asked.Load() == 0 {
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

// The line reader is exercised through a pipe: a test cannot portably have a
// terminal.
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

// A closed terminal delivers no bytes and no error; a reader that waits for
// either would wait on forever.
func TestReadLineReturnsWhenTheTerminalSaysNothingMore(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err, "create a pipe")

	_ = writer.Close()
	got, err := readLine(reader, maxKeyLine)
	_ = reader.Close()

	assert.NoError(t, err, "readLine on a closed pipe: %v", err)

	assert.Equal(t, "", got, "readLine = %q, want nothing typed", got)
}

// A nil *terminalPrompter boxed into the interface is not nil, and every call
// through it dereferences nothing.
func TestTerminalPrompterForIsNilUnlessStandardInputIsATerminal(t *testing.T) {
	ask := terminalPrompterFor(strings.NewReader("key"), &bytes.Buffer{})
	assert.Nil(t, ask, "a reader that is not a file was taken for a terminal")

	ask = terminalPrompterFor(&bytes.Buffer{}, &bytes.Buffer{})
	assert.Nil(t, ask, "a buffer was taken for a terminal")

	ask = terminalPrompterFor(nil, &bytes.Buffer{})
	assert.Nil(t, ask, "a nil reader was taken for a terminal")
	// A regular file has a descriptor but no grid, the case a descriptor check
	// alone would get wrong.
	file, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	require.NoError(t, err, "create a file")

	defer file.Close()

	ask = terminalPrompterFor(file, &bytes.Buffer{})
	assert.Nil(t, ask, "a regular file was taken for a terminal")
}

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

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("pipe closed") }

// An explicit choice is never second-guessed by a prompt: somebody who named a
// source of the key has said where it is.
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

// A named environment variable that is empty is a mistake to report: the flag
// said where the key is.
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
