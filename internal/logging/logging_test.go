package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests here cover the fan-out rather than the code that feeds it: that each
// sink receives what it should and nothing it should not, that a level change is
// one store rather than a rebuild, that a secret never arrives, and that
// rotation, following and closing behave correctly.

// newService builds a Service over a temporary log directory and closes it when
// the test ends.
//
// Nothing here reaches for the process environment. DMM_LOG_DIR is cleared
// rather than assumed absent, so a developer with it exported gets the same
// directory the test asked for; Directory is always absolute, so the resolution
// under test is the one with nothing else claiming it.
func newService(t *testing.T, cfg Config) *Service {
	t.Helper()
	t.Setenv(EnvLogDir, "")

	if cfg.Directory == "" {
		cfg.Directory = t.TempDir()
	}

	s := &Service{level: newAtomicLevel(ParseLevel(cfg.Level))}
	s.applyConfig(cfg)
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// captureStderr runs f with os.Stderr replaced by a pipe and returns what was
// written. The console sink writes to os.Stderr by design, so this is how a test
// reaches it without the package offering a way to redirect output — a seam that
// exists only for tests is a seam production code can then start depending on.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err, "create a pipe")

	original := os.Stderr
	os.Stderr = writer

	collected := make(chan string, 1)

	go func() {
		var buf bytes.Buffer

		_, _ = io.Copy(&buf, reader)
		collected <- buf.String()
	}()

	func() {
		defer func() {
			os.Stderr = original
			_ = writer.Close()
		}()

		f()
	}()

	out := <-collected
	_ = reader.Close()

	return out
}

// recordingEvents collects the EventLog records a service publishes.
type recordingEvents struct {
	lines []LogLine
	kinds []services.EventType
	mu    sync.Mutex
}

func (r *recordingEvents) Publish(event services.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.kinds = append(r.kinds, event.Type)
	if line, ok := event.Data.(LogLine); ok {
		r.lines = append(r.lines, line)
	}
}

func (r *recordingEvents) snapshot() ([]LogLine, []services.EventType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]LogLine(nil), r.lines...), append([]services.EventType(nil), r.kinds...)
}

// eventText renders published records the way a GUI would serialise them, so a
// secret carried in a field is visible to a substring check.
func eventText(lines []LogLine) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, fmt.Sprintf("%+v", line))
	}

	return out
}

// captureSink records what a sink was handed and how often it was flushed, so the
// Close contract can be asserted without a file behind it.
type captureSink struct {
	data    bytes.Buffer
	syncs   int
	closes  int
	mu      sync.Mutex
	failing bool
}

func (c *captureSink) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.data.Write(p)
}

func (c *captureSink) Sync() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.syncs++
	if c.failing {
		return fmt.Errorf("sink refuses to flush")
	}

	return nil
}

func (c *captureSink) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closes++

	return nil
}

func (c *captureSink) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.data.String()
}

// messagesIn returns the message of every record in a JSON log.
func messagesIn(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s: %v", path)

	var messages []string
	for _, record := range parseLines(data, TraceLevel) {
		messages = append(messages, record.Message)
	}

	return messages
}

func equal(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}

func TestTheWriterStackProducesJSONLines(t *testing.T) {
	s := newService(t, Config{Level: "trace"})
	events := &recordingEvents{}
	s.SetEvents(events)

	lg := s.Logger()
	lg.Debug().Str("mod", "sword").Msg("installing")
	lg.Info().Int("count", 2).Msg("installed")

	err := s.Close()
	require.NoError(t, err, "Close: %v", err)

	data, err := os.ReadFile(LogPath(s.Directory()))
	require.NoError(t, err, "read the log file")

	records := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	require.Len(t, records, 2, "the log file holds %d records, want 2:\n%s", len(records), data)

	for i, record := range records {
		var fields map[string]any
		if err := json.Unmarshal(record, &fields); err != nil {
			assert.Fail(t, "record %d is not valid JSON: %v\n%s", i, err, record)
			continue
		}

		assert.Equal(
			t,
			appName,
			fields["app"],
			"record %d is not tagged with the app name: %v",
			i,
			fields["app"],
		)

		assert.False(
			t,
			fields["level"] == nil || fields["message"] == nil,
			"record %d is missing its level or message: %v",
			i,
			fields,
		)
	}

	lines, kinds := events.snapshot()
	require.Len(t, lines, 2, "the GUI stream saw %d records, want 2", len(lines))

	for _, kind := range kinds {
		assert.Equal(
			t,
			services.EventLog,
			kind,
			"the stream published %q, want %q",
			kind,
			services.EventLog,
		)
	}

	assert.Equal(t, "sword", lines[0].ModID, "the GUI stream lost the mod id: %+v", lines[0])
}

func TestEachSinkHonoursItsOwnLevel(t *testing.T) {
	dir := t.TempDir()
	// The service is built inside the capture because the console sink binds to
	// os.Stderr when the writer stack is assembled, not on every write.
	printed := captureStderr(t, func() {
		s := newService(t, Config{Level: "trace", Console: true, Directory: dir})
		s.SetEvents(&recordingEvents{})

		lg := s.Logger()
		lg.Trace().Msg("at-trace")
		lg.Debug().Msg("at-debug")
		lg.Info().Msg("at-info")
		lg.Warn().Msg("at-warn")
		lg.Error().Msg("at-error")

		err := s.Close()
		require.NoError(t, err, "Close: %v", err)
	})

	// The file is at trace and the GUI stream is at debug, so the only record those
	// two disagree about is the trace one. The console drops both, because it has
	// to stay readable at whatever level the file is set to.
	got := messagesIn(t, LogPath(dir))
	assert.True(
		t,
		equal(got, "at-trace", "at-debug", "at-info", "at-warn", "at-error"),
		"the file holds %v, want every record",
		got,
	)

	for _, level := range []string{"trace", "debug"} {
		assert.NotContains(
			t,
			printed,
			"at-"+level,
			"the console printed a %s record:\n%q",
			level,
			printed,
		)
	}

	for _, level := range []string{"info", "warn", "error"} {
		assert.Contains(
			t,
			printed,
			"at-"+level,
			"the console dropped an %s record:\n%q",
			level,
			printed,
		)
	}
}

// messagesInText pulls the messages out of console output, which is not JSON. It
// is a substring scan rather than a parse: the console format is a presentation
// choice, and asserting on its exact shape would make this test fail every time
// the format is made nicer.
func messagesInText(printed string) []string {
	var messages []string

	for _, record := range strings.Split(strings.TrimRight(printed, "\n"), "\n") {
		// The console writer ends each record with its fields; the message is the
		// last word before them, and every message used here is a single word.
		fields := strings.Fields(record)
		if len(fields) == 0 {
			continue
		}

		messages = append(messages, fields[len(fields)-1])
	}

	return messages
}

func TestTheConsoleCanBeAskedForText(t *testing.T) {
	dir := t.TempDir()

	printed := captureStderr(t, func() {
		s := newService(t, Config{Level: "info", Console: true, Format: FormatText, Directory: dir})
		s.Logger().Info().Str("mod", "sword").Msg("mod enabled")

		err := s.Close()
		require.NoError(t, err, "Close: %v", err)
	})
	if strings.Contains(printed, `{"level"`) {
		assert.Fail(t, "the console wrote JSON while the format is text: %s", printed)
	}

	for _, want := range []string{"mod enabled", "sword", "INF"} {
		assert.Contains(t, printed, want, "the console output is missing %q: %s", want, printed)
	}
	// The file keeps its own format whatever the console is set to: it is read back
	// by machine, and a pretty console must not cost that.
	got := messagesIn(t, LogPath(dir))
	assert.True(t, equal(got, "mod enabled"), "the log file holds %v, want the record as JSON", got)
}

func TestSetLevelIsOneStoreAndTakesEffectImmediately(t *testing.T) {
	s := newService(t, Config{Level: "info"})
	// The copy is taken before the change on purpose: it is what a caller that
	// cached the logger holds on to, so a SetLevel that rebuilt the writer graph
	// would leave this copy writing to a stack nobody is reading any more.
	lg := *s.Logger()

	lg.Debug().Msg("before")
	s.SetLevel(zerolog.DebugLevel)
	lg.Debug().Msg("after")

	got := s.Level()
	assert.Equal(t, zerolog.DebugLevel, got, "Level is %v, want debug", got)

	err := s.Close()
	require.NoError(t, err, "Close: %v", err)

	messages := messagesIn(t, LogPath(s.Directory()))
	assert.True(
		t,
		equal(messages, "after"),
		"the log holds %v, want only the record written after the change",
		messages,
	)
}

func TestSecretsAreMaskedBeforeAnySinkSeesThem(t *testing.T) {
	const secret = "hunter2-do-not-log-me"

	dir := t.TempDir()
	events := &recordingEvents{}
	printed := captureStderr(t, func() {
		s := newService(t, Config{
			Level:     "trace",
			Console:   true,
			Redact:    []string{"nexus_api_key"},
			Directory: dir,
		})
		s.SetEvents(events)

		lg := s.Logger()
		lg.Info().Str("token", secret).Msg("first")
		lg.Info().Str("Nexus_API_Key", secret).Msg("second")
		lg.Info().Str("password", secret).Msg("third")

		err := s.Close()
		require.NoError(t, err, "Close: %v", err)
	})

	data, err := os.ReadFile(LogPath(dir))
	require.NoError(t, err, "read the log file")

	lines, _ := events.snapshot()

	sinks := map[string]string{
		"the log file":   string(data),
		"the console":    printed,
		"the GUI stream": strings.Join(eventText(lines), "\n"),
	}
	for name, sink := range sinks {
		assert.NotContains(t, sink, secret, "a secret reached %s:\n%s", name, sink)
		// The mask has to be readable: a log that says nothing about what was
		// hidden is a log nobody trusts.
		assert.Contains(t, sink, mask, "nothing was masked on %s:\n%s", name, sink)
	}
}

func TestAFieldNobodyNamedIsLeftAlone(t *testing.T) {
	// Masking everything would make the log useless, which fails in the same
	// direction as masking nothing: a reader who cannot tell a masked field from a
	// real one stops reading the log at all.
	const secret = "a-version-number-that-is-not-secret"

	s := newService(t, Config{Level: "info"})
	s.Logger().Info().Str("build_hash", secret).Msg("started")

	err := s.Close()
	require.NoError(t, err, "Close: %v", err)

	data, err := os.ReadFile(LogPath(s.Directory()))
	require.NoError(t, err, "read the log file")

	assert.Contains(
		t,
		string(data),
		secret,
		"a field that was not on the redact list was masked:\n%s",
		data,
	)
}

func TestSetRedactFieldsTakesEffectWithoutARestart(t *testing.T) {
	const secret = "another-secret"

	s := newService(t, Config{Level: "info"})

	s.Logger().Info().Str("build_hash", secret).Msg("before")
	// "harmless_looking_field" normalises to something no built-in list would
	// match, so the only thing that can mask it is the list the caller just set.
	s.SetRedactFields([]string{"harmless_looking_field"})
	s.Logger().Info().Str("harmless_looking_field", secret).Msg("after")

	err := s.Close()
	require.NoError(t, err, "Close: %v", err)

	data, err := os.ReadFile(LogPath(s.Directory()))
	require.NoError(t, err, "read the log file")

	assert.Contains(
		t,
		string(data),
		secret,
		"the record written before the list changed was masked as well",
	)

	got := strings.Count(string(data), mask)
	assert.Equal(t, 1, got, "the log holds %d masked values, want 1:\n%s", got, data)
}

func TestTheRotationSettingsDecideWhatIsKept(t *testing.T) {
	s := newService(t, Config{Level: "info", MaxSizeMB: 1, MaxBackups: 2, Compress: true})
	chunk := strings.Repeat("x", 1024)
	// Past two megabytes, so there are more generations than MaxBackups allows and
	// the oldest one is the one that has to go.
	for i := 0; i < 2600; i++ {
		s.Logger().Info().Str("chunk", chunk).Msg("filling the log")
	}

	err := s.Close()
	require.NoError(t, err, "Close: %v", err)

	// lumberjack compresses and prunes on a goroutine after a rotation rather than
	// inside the write, so the last generation is still being worked on here.
	files := awaitCompressed(t, s.Directory(), 2)

	var rotated int

	for _, file := range files {
		if file.Rotated {
			rotated++
		}
	}

	assert.Equal(t, 2, rotated, "MaxBackups of 2 left %d generations, want 2", rotated)

	assert.Len(
		t,
		files,
		3,
		"the directory holds %d files, want the active one plus two backups",
		len(files),
	)
}

// awaitCompressed waits for every rotated file in dir to be gzipped, and returns
// the listing. lumberjack does that work on a goroutine after the rotation that
// caused it, so a listing taken immediately afterwards catches it half done.
func awaitCompressed(t *testing.T, dir string, want int) []LogFile {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)

	for {
		files, err := ListFiles(dir)
		require.NoError(t, err, "ListFiles")

		rotated, compressed := 0, 0

		for _, file := range files {
			if file.Rotated {
				rotated++
			}

			if file.Compressed {
				compressed++
			}
		}

		if rotated >= want && rotated == compressed {
			return files
		}

		require.False(
			t,
			time.Now().After(deadline),
			"%d rotated files, %d compressed; want %d of each",
			rotated,
			compressed,
			want,
		)

		time.Sleep(50 * time.Millisecond)
	}
}

func TestFollowSurvivesARotation(t *testing.T) {
	s := newService(t, Config{Level: "info", MaxSizeMB: 1, MaxBackups: 5})
	path := LogPath(s.Directory())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := Follow(ctx, path)
	require.NoError(t, err, "Follow")

	// Past a megabyte, so lumberjack renames the file aside and starts a new one
	// while the follower is still holding the old descriptor.
	chunk := strings.Repeat("y", 1024)
	for i := 0; i < 2600; i++ {
		s.Logger().Info().Str("chunk", chunk).Msg("filling the log")
	}

	s.Logger().Info().Msg("after the rotation")

	awaitMessage(t, stream, "after the rotation")
}

// awaitMessage drains stream until a record with the given message arrives. A
// closed channel means the follower gave up, which is a failure rather than a
// quiet end: the only thing that closes it is the context.
func awaitMessage(t *testing.T, stream <-chan LogLine, want string) {
	t.Helper()

	deadline := time.After(30 * time.Second)

	for {
		select {
		case line, ok := <-stream:
			require.True(t, ok, "the follower stopped without reporting %q", want)

			if line.Message == want {
				return
			}
		case <-deadline:
			require.FailNow(t, "timed out waiting for %q", want)
		}
	}
}

func TestCloseFlushesEverySinkAndCanBeCalledTwice(t *testing.T) {
	capture := &captureSink{}
	s := &Service{
		level: newAtomicLevel(zerolog.InfoLevel),
		sinks: []io.Writer{capture},
		root:  newLogger(nil),
	}

	err := s.Close()
	require.NoError(t, err, "Close: %v", err)

	err = s.Close()
	require.NoError(t, err, "Close again: %v", err)

	assert.False(
		t,
		capture.syncs != 1 || capture.closes != 1,
		"two closes synced %d times and closed %d times, want 1 and 1",
		capture.syncs,
		capture.closes,
	)

	// A sink that cannot be flushed is reported once, on the first close, and the
	// second call repeats that answer rather than pretending it worked.
	failing := &captureSink{failing: true}
	other := &Service{
		level: newAtomicLevel(zerolog.InfoLevel),
		sinks: []io.Writer{failing},
		root:  newLogger(nil),
	}

	first := other.Close()
	require.NotNil(t, first, "a sink that could not be flushed was reported as a clean close")

	again := other.Close()
	assert.False(
		t,
		again == nil || again.Error() != first.Error(),
		"the second close returned %v, want the first result unchanged",
		again,
	)
}

func TestAnUnusableDirectoryDegradesInsteadOfFailing(t *testing.T) {
	// A path that cannot be a directory: a file already sits where the log
	// directory should go.
	base := t.TempDir()

	blocked := filepath.Join(base, "logs")
	err := os.WriteFile(blocked, []byte("not a directory"), 0o644)
	require.NoError(t, err, "block the log directory: %v", err)

	s := &Service{level: newAtomicLevel(ParseLevel("info"))}
	printed := captureStderr(t, func() {
		s.applyConfig(Config{Level: "info", Directory: blocked, Console: true})
		s.Logger().Info().Msg("still logging")
	})
	t.Cleanup(func() { _ = s.Close() })

	dir := s.Directory()
	assert.Equal(t, "", dir, "the service reports %q as its log directory, want none", dir)

	assert.Contains(
		t,
		printed,
		"still logging",
		"logging stopped when the file sink could not be opened: %q",
		printed,
	)
}

func TestTailReturnsTheNewestRecordsWithinTheLevel(t *testing.T) {
	path := LogPath(t.TempDir())
	levels := []zerolog.Level{
		zerolog.DebugLevel, zerolog.InfoLevel, zerolog.InfoLevel, zerolog.WarnLevel,
	}

	var records []byte

	for _, lvl := range levels {
		record, err := json.Marshal(map[string]any{
			"time":    time.Now().Format(time.RFC3339),
			"level":   lvl.String(),
			"app":     appName,
			"message": lvl.String(),
		})
		require.NoError(t, err, "marshal a record")

		records = append(records, append(record, '\n')...)
	}

	err := os.WriteFile(path, records, 0o644)
	require.NoError(t, err, "write the log file: %v", err)

	lines, err := Tail(path, 2, zerolog.InfoLevel)
	require.NoError(t, err, "Tail")

	require.Len(t, lines, 2, "Tail returned %d records, want 2", len(lines))

	assert.False(
		t,
		lines[0].Message != "info" || lines[1].Message != "warn",
		"Tail returned %q and %q, want the last two at or above info",
		lines[0].Message,
		lines[1].Message,
	)

	all, err := Tail(path, 10, TraceLevel)
	require.NoError(t, err, "Tail")

	assert.Len(
		t,
		all,
		len(levels),
		"Tail returned %d records with no filter, want %d",
		len(all),
		len(levels),
	)
	// A missing file is not an error: `dmm logs tail -f` is expected to work before
	// the first line is ever written.
	missing, err := Tail(LogPath(t.TempDir()), 5, TraceLevel)
	assert.False(
		t,
		err != nil || missing != nil,
		"Tail on a missing file returned %v, %v; want nil, nil",
		missing,
		err,
	)
}

func TestConfigSurvivesARoundTripAndIgnoresUnknownKeys(t *testing.T) {
	original := Config{
		Level:      "warn",
		Format:     FormatText,
		Directory:  "/var/log/dmm",
		MaxSizeMB:  25,
		MaxBackups: 3,
		MaxAgeDays: 90,
		Compress:   true,
		Console:    true,
		Redact:     []string{"nexus_api_key"},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal")
	// A config written by a newer build carries keys this one has never heard of,
	// and a config file that fails to load loses the user's whole setup.
	withExtras := strings.TrimSuffix(string(data), "}") +
		`,"future_option":{"nested":true},"another_one":"kept"}`

	var decoded Config

	err = json.Unmarshal([]byte(withExtras), &decoded)
	require.NoError(t, err, "unmarshal: %v", err)

	assert.False(
		t,
		decoded.Level != original.Level || decoded.Directory != original.Directory,
		"the round trip changed the settings: %+v",
		decoded,
	)

	assert.False(
		t,
		len(decoded.Redact) != 1 || decoded.Redact[0] != "nexus_api_key",
		"the redact list did not survive: %v",
		decoded.Redact,
	)
	// The command-line overrides are deliberately not persisted: they describe one
	// invocation, and writing them down would make them the next one's defaults.
	assert.False(
		t,
		decoded.Override != "" || decoded.NoFile,
		"a per-invocation override was written to disk: %+v",
		decoded,
	)
}

func TestDefaultsAreTheDocumentedOnes(t *testing.T) {
	cfg := DefaultConfig()
	assert.False(
		t,
		cfg.MaxSizeMB != 10 || cfg.MaxBackups != 5 || cfg.MaxAgeDays != 30 || !cfg.Compress,
		"the defaults are %+v, want 10 MB, five backups, 30 days and compression",
		cfg,
	)
	// A partially filled block falls back rather than landing on a policy nobody
	// chose. For Compress that means "on" even though false is the zero value, the
	// same convention domain.CacheConfig.VerifyHashes uses.
	filled := Config{MaxSizeMB: 25}.Normalize()
	assert.False(
		t,
		filled.MaxBackups != DefaultMaxBackups || !filled.Compress,
		"a partial block normalised to %+v",
		filled,
	)

	assert.Equal(
		t,
		25,
		filled.MaxSizeMB,
		"Normalize overwrote a value that was set: %d",
		filled.MaxSizeMB,
	)
	// A level nobody recognises falls back to info: a typo must not silence the
	// log, which is the one failure a user cannot notice.
	assert.Equal(
		t,
		zerolog.InfoLevel,
		ParseLevel("nonsense"),
		"an unrecognised level did not fall back to info",
	)
}

func TestResolveDirectoryFollowsTheDocumentedPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "local"))
	t.Setenv(EnvLogDir, "")

	configured := filepath.Join(home, "configured")
	fromEnv := filepath.Join(home, "from-env")
	fromFlag := filepath.Join(home, "from-flag")

	if got, err := ResolveDirectory("", ""); err != nil {
		require.FailNow(t, "ResolveDirectory: %v", err)
	} else if !strings.Contains(got, platform.AppName) || !filepath.IsAbs(got) {
		assert.Fail(t, "with nothing set the log directory is %q", got)
	}

	t.Setenv(EnvLogDir, fromEnv)

	got, err := ResolveDirectory("", configured)
	assert.False(
		t,
		err != nil || got != fromEnv,
		"the environment did not outrank the config file: %q, %v",
		got,
		err,
	)

	got, err = ResolveDirectory(fromFlag, configured)
	assert.False(
		t,
		err != nil || got != fromFlag,
		"the flag did not outrank everything: %q, %v",
		got,
		err,
	)
	// A relative path scatters logs according to the working directory, which is
	// the one thing a log file's location must not do. The config file is not
	// checked, because a value there is as likely to be written by hand as by the
	// flag parser and refusing to start over it would be worse than a log in an
	// odd place.
	_, err = ResolveDirectory("relative/logs", "")
	assert.Error(t, err, "a relative --log-dir was accepted")
}

func TestListFilesAndCleanLeaveOtherFilesAlone(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{LogFileName, "dmm-2026-01-02T03-04-05.000.log.gz", "notes.txt"} {
		err := os.WriteFile(filepath.Join(dir, name), []byte("content"), 0o644)
		require.NoError(t, err, "write %s: %v", name, err)
	}

	files, err := ListFiles(dir)
	require.NoError(t, err, "ListFiles")

	var names []string
	for _, file := range files {
		names = append(names, file.Name)
	}

	sort.Strings(names)

	assert.True(
		t,
		equal(names, "dmm-2026-01-02T03-04-05.000.log.gz", LogFileName),
		"ListFiles returned %v, want the two log files and nothing else",
		names,
	)

	removed, err := Clean(dir, false)
	require.NoError(t, err, "Clean")

	assert.Equal(t, 1, removed, "Clean removed %d files, want just the rotated one", removed)

	_, err = os.Stat(filepath.Join(dir, "notes.txt"))
	assert.NoError(t, err, "Clean deleted a file that is not a log")

	_, err = os.Stat(filepath.Join(dir, LogFileName))
	assert.NoError(t, err, "Clean without --all removed the active log file")

	_, err = Clean(dir, true)
	require.NoError(t, err, "Clean --all: %v", err)

	info, err := os.Stat(filepath.Join(dir, LogFileName))
	switch {
	case err != nil:
		assert.Fail(t, "Clean --all removed the active log file: %v", err)
	case info.Size() != 0:
		assert.Fail(t, "Clean --all left %d bytes behind; want an empty file", info.Size())
	}
}

func TestParseLineTellsALogLineFromAStrayByteRange(t *testing.T) {
	if _, ok := ParseLine([]byte(`{"level":"info","message":"installed"}` + "\n")); !ok {
		assert.Fail(t, "a well-formed record was rejected")
	}
	// A file being appended to can end mid-record, and a tail must not report a
	// half-written line as a log entry.
	for _, raw := range [][]byte{[]byte(""), []byte(`{"level":"info"`), []byte("not json\n")} {
		_, ok := ParseLine(raw)
		assert.False(t, ok, "%q was accepted as a record", raw)
	}

	line, _ := ParseLine(
		[]byte(
			`{"level":"info","message":"installed","mod":"sword","version":"1.2.0","app":"darktide-mod-manager"}`,
		),
	)
	assert.Equal(t, "sword", line.ModID, "ModID is %q, want the mod field", line.ModID)

	assert.False(
		t,
		line.Message != "installed" || line.Level != "info",
		"the record decoded as %+v",
		line,
	)

	assert.Equal(
		t,
		"1.2.0",
		line.Fields["version"],
		"the extra fields are %v, want version among them",
		line.Fields,
	)

	_, kept := line.Fields["message"]
	assert.False(t, kept, "the message was left in the generic field map as well")
}
