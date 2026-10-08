// Package logging owns the process-wide log fan-out.
//
// A single zerolog logger is shared by every layer. It is deliberately created
// at TraceLevel and carries no filter of its own: each destination receives a
// LevelWriter that does the filtering, so raising or lowering the level at
// runtime is a single atomic store instead of a rebuild of the writer graph.
// The fan-out has three destinations:
//
//	file    configured level  -> redactingWriter -> lumberjack (dmm.log)
//	events  debug and above   -> redactingWriter -> in-process EventLog emitter
//	console info or more severe -> redactingWriter -> os.Stderr (when enabled)
//
// The whole fan-out is wrapped in a zerolog.SyncWriter so Close flushes.
//
// Two rules shape the code here:
//
//   - Logging must never fail the application. If the log file cannot be
//     opened (read-only home, quota, bad directory) the service degrades to a
//     console-only stack instead of returning an error, because a broken log
//     destination must not stop the mod manager from starting.
//   - Secrets never reach a sink. Every destination is wrapped in a
//     redactingWriter (see redact.go) which masks sensitive fields before the
//     bytes are handed on.
package logging

import (
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

// appName tags every line so multi-process log files stay attributable.
const appName = "darktide-mod-manager"

// Format values accepted by Config.Format.
const (
	// FormatJSON writes newline-delimited JSON. It is the default because the
	// file sink is read back by Tail/Follow and by the GUI.
	FormatJSON = "json"
	// FormatText writes human-readable console output.
	FormatText = "text"
)

// Config is the persisted logging configuration. It is the Go mirror of the
// "logging" block in config.json; unknown keys in the JSON are ignored so a
// config written by a newer build still loads.
type Config struct {
	Level      string   `json:"level"`
	Format     string   `json:"format"`
	Directory  string   `json:"directory"`
	Override   string   `json:"-"`
	Redact     []string `json:"redact,omitempty"`
	MaxSizeMB  int      `json:"max_size_mb"`
	MaxBackups int      `json:"max_backups"`
	MaxAgeDays int      `json:"max_age_days"`
	Compress   bool     `json:"compress"`
	Console    bool     `json:"console"`
	NoFile     bool     `json:"-"`
}

// DefaultConfig returns the documented defaults: 10 MB per file, five backups
// (a 60 MB ceiling across the set), 30 day retention and gzip on rotation.
func DefaultConfig() Config {
	return Config{
		Level:      zerolog.InfoLevel.String(),
		Format:     FormatJSON,
		MaxSizeMB:  DefaultMaxSizeMB,
		MaxBackups: DefaultMaxBackups,
		MaxAgeDays: DefaultMaxAgeDays,
		Compress:   DefaultCompress,
	}
}

// Normalize fills unset fields with the defaults.
//
// Compress follows the same convention as domain.CacheConfig.VerifyHashes: a
// false value means "unset", so compression cannot be switched off from a
// partially filled config block.
func (c Config) Normalize() Config {
	out := c

	out.Level = strings.ToLower(strings.TrimSpace(out.Level))
	if out.Level == "" {
		out.Level = zerolog.InfoLevel.String()
	}

	out.Format = strings.ToLower(strings.TrimSpace(out.Format))
	if out.Format == "" {
		out.Format = FormatJSON
	}

	if out.MaxSizeMB <= 0 {
		out.MaxSizeMB = DefaultMaxSizeMB
	}

	if out.MaxBackups <= 0 {
		out.MaxBackups = DefaultMaxBackups
	}

	if out.MaxAgeDays <= 0 {
		out.MaxAgeDays = DefaultMaxAgeDays
	}

	if !out.Compress {
		out.Compress = DefaultCompress
	}

	return out
}

// TraceLevel is the least severe level.
//
// A reader given no filter wants everything, so "no filter" is spelled out here
// rather than left to the reader to reach for a package it should not have to
// know about.
const TraceLevel = zerolog.TraceLevel

// ParseLevel converts a configured level name into a zerolog level, falling
// back to info for anything unrecognised so a typo can never silence logging
// entirely.
func ParseLevel(name string) zerolog.Level {
	lvl, err := zerolog.ParseLevel(strings.TrimSpace(name))
	if err != nil || lvl == zerolog.NoLevel {
		return zerolog.InfoLevel
	}

	return lvl
}

// atomicLevel is the single source of truth for the active severity. SetLevel
// is one atomic store, so no writer has to be rebuilt while the app runs.
type atomicLevel struct {
	value atomic.Int32
}

func newAtomicLevel(lvl zerolog.Level) *atomicLevel {
	a := &atomicLevel{}
	a.value.Store(int32(lvl))

	return a
}

func (a *atomicLevel) Get() zerolog.Level {
	if a == nil {
		return zerolog.TraceLevel
	}

	v := a.value.Load()
	if v > math.MaxInt8 || v < math.MinInt8 {
		return zerolog.InfoLevel
	}

	return zerolog.Level(v)
}

func (a *atomicLevel) Set(lvl zerolog.Level) {
	if a == nil {
		return
	}

	a.value.Store(int32(lvl))
}

// gate is a zerolog.LevelWriter that drops events outside a dynamic band.
//
// It replaces zerolog.FilteredLevelWriter, whose threshold is fixed at
// construction time; here the lower bound is read from the shared atomicLevel on
// every event so `dmm logs level debug` takes effect immediately.
//
// floor raises the lower bound for a sink that should stay readable: the console
// mirror never prints debug chatter just because the file does.
//
// floored is set explicitly rather than inferred from floor's zero value, because
// zerolog.NoLevel is 6 and DebugLevel is 0: a gate that declared "no floor" by
// leaving the field unset would quietly install a debug floor, and the file sink
// would then lose every trace record it was configured to keep.
type gate struct {
	next    io.Writer
	level   *atomicLevel
	floor   zerolog.Level
	floored bool
}

func (g *gate) threshold() zerolog.Level {
	lvl := g.level.Get()
	if g.floored && lvl < g.floor {
		return g.floor
	}

	return lvl
}

// Write implements io.Writer. A plain Write has already bypassed level
// filtering, so the bytes are forwarded untouched.
func (g *gate) Write(p []byte) (int, error) {
	return g.next.Write(p)
}

func (g *gate) WriteLevel(lvl zerolog.Level, p []byte) (int, error) {
	if lvl < g.threshold() {
		return len(p), nil
	}

	return g.next.Write(p)
}

// EventSink is the slice of the event bus that logging needs. It is declared
// here (rather than importing the bus type) so the logging package can be built
// and exercised without the rest of the application.
type EventSink interface {
	Publish(services.Event)
}

// eventWriter turns a log line into an EventLog event for the GUI. The
// subscription is in-process: the GUI shows what this process logged without
// reading the log file.
type eventWriter struct {
	sink EventSink
}

func (w eventWriter) Write(p []byte) (int, error) {
	line, ok := ParseLine(p)
	if !ok || w.sink == nil {
		return len(p), nil
	}

	w.sink.Publish(services.Event{Type: services.EventLog, ModID: line.ModID, Data: line})

	return len(p), nil
}

// syncer is the "flush this writer" shape the standard library does not name:
// fsync is only exported on *os.File, but any sink that buffers should be
// flushed on Close, so logging matches the method instead of the concrete type.
type syncer interface {
	Sync() error
}

// consoleSink renders lines for a terminal. It implements syncer and io.Closer
// so Close flushes a redirected stderr before the process exits.
type consoleSink struct {
	out io.Writer
	enc *zerolog.ConsoleWriter
}

func newConsoleSink(out io.Writer, format string) *consoleSink {
	if format == FormatText {
		return &consoleSink{
			out: out,
			enc: &zerolog.ConsoleWriter{Out: out, TimeFormat: "15:04:05"},
		}
	}

	return &consoleSink{out: out}
}

func (c *consoleSink) Write(p []byte) (int, error) {
	if c.enc == nil {
		return c.out.Write(p)
	}

	return c.enc.Write(p)
}

// Sync flushes the destination when it can be flushed.
//
// A failure to fsync a terminal or a pipe is not reported. Nothing is buffered on
// the way to a character device — a write is the write — so EINVAL and ENOTTY
// here mean "there is no file to sync", not "data was lost". Reporting them
// would make every `--verbose` run print a close warning on exit, and a warning
// on every run is a warning nobody reads.
func (c *consoleSink) Sync() error {
	s, ok := c.out.(syncer)
	if !ok {
		return nil
	}

	if err := s.Sync(); err != nil && !unflushable(err) {
		return err
	}

	return nil
}

// Close exists so the sink is treated like the file one. It does not sync: the
// close path already syncs every sink before closing any of them, and syncing
// here as well would report the same failure twice.
func (c *consoleSink) Close() error { return nil }

// unflushable reports whether an fsync failure means the destination simply
// cannot be flushed rather than that a write went missing.
func unflushable(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY)
}

// Service is the log fan-out plus the rotation policy behind it.
//
// One Service is created per process. Handlers receive *zerolog.Logger values
// copied from Logger(); those copies stay valid for the life of the process
// because Apply is only meant to run at startup (see Apply).
type Service struct {
	root   zerolog.Logger
	events EventSink
	err    error
	level  *atomicLevel
	rot    *lumberjack.Logger
	dir    string
	sinks  []io.Writer
	cfg    Config
	once   sync.Once
	mu     sync.Mutex
}

// New builds a service from cfg and makes it the process-wide logger.
//
// New never fails: an unusable log directory degrades the stack to console only
// so a logging problem cannot stop the app from starting.
func New(cfg Config) *Service {
	s := &Service{level: newAtomicLevel(ParseLevel(cfg.Level))}
	s.applyConfig(cfg)

	return s
}

// Init is New plus a pointer to the service, for callers that want both.
func Init(cfg Config) (*Service, *zerolog.Logger) {
	s := New(cfg)
	return s, s.Logger()
}

// SetEvents attaches the in-process event sink. It must be called after New
// (the event bus only exists once the container is wired) and rebuilds the
// stack, so like Apply it is a startup-time call.
func (s *Service) SetEvents(sink EventSink) {
	if s == nil || sink == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = sink
	s.rebuild()
}

// Logger returns the shared logger. The pointer stays valid for the life of
// the process; callers should copy the value (`lg := *s.Logger()`) rather than
// swap the writer.
func (s *Service) Logger() *zerolog.Logger {
	if s == nil {
		return &zlog.Logger
	}

	return &s.root
}

// Level reports the active severity.
func (s *Service) Level() zerolog.Level { return s.level.Get() }

// SetLevel changes the active severity without rebuilding any writer.
func (s *Service) SetLevel(lvl zerolog.Level) { s.level.Set(lvl) }

// Directory is the resolved log directory, or "" when no file sink is active.
func (s *Service) Directory() string {
	if s == nil {
		return ""
	}

	return s.dir
}

// Apply rebuilds the fan-out for cfg. It exists for the startup path where the
// command line overrides the config file; because it swaps the writer stack it
// must not race with in-flight logging, so callers run it before services start.
func (s *Service) Apply(cfg Config) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.applyConfig(cfg)
}

// SetRedactFields replaces the extra field names whose values are masked on the
// way to every sink.
//
// It rebuilds the stack, which is why it is a separate call from Apply: the
// caller has a persisted list in hand, not a whole Config. A field that is
// redacted only from the next launch would keep leaking for as long as the user
// left the app running, which is exactly the window they asked to close.
func (s *Service) SetRedactFields(fields []string) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.cfg.Redact = append([]string(nil), fields...)
	s.rebuild()
}

// Close flushes and closes every sink. It is idempotent and safe to call from a
// deferred cleanup; repeated calls return the first result unchanged.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}

	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		var errs []error

		for _, sink := range s.sinks {
			if syncer, ok := sink.(syncer); ok {
				if err := syncer.Sync(); err != nil {
					errs = append(errs, err)
				}
			}

			if closer, ok := sink.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					errs = append(errs, err)
				}
			}
		}

		s.err = errors.Join(errs...)
	})

	return s.err
}

// stack assembles the redacted, level-filtered destinations for cfg. The caller
// holds s.mu.
func (s *Service) stack(cfg Config) []io.Writer {
	s.sinks = nil

	var sinks []io.Writer

	if s.rot != nil {
		sinks = append(sinks, &gate{
			level: s.level,
			next:  NewRedactingWriter(s.rot, cfg.Redact),
		})
		s.sinks = append(s.sinks, s.rot)
	}

	if s.events != nil {
		// The GUI wants debug records even when the file is at info: they are
		// ephemeral and cost nothing to display, while a quiet log file stays
		// quiet.
		sinks = append(sinks, &zerolog.FilteredLevelWriter{
			Writer: zerolog.LevelWriterAdapter{
				Writer: NewRedactingWriter(eventWriter{sink: s.events}, cfg.Redact),
			},
			Level: zerolog.DebugLevel,
		})
	}

	if cfg.Console {
		console := newConsoleSink(os.Stderr, cfg.Format)
		sinks = append(sinks, &gate{
			level:   s.level,
			floor:   zerolog.InfoLevel,
			floored: true,
			next:    NewRedactingWriter(console, cfg.Redact),
		})
		s.sinks = append(s.sinks, console)
	}

	return sinks
}

// applyConfig installs cfg's rotation policy and rebuilds the stack. The caller
// holds s.mu.
func (s *Service) applyConfig(cfg Config) {
	cfg = cfg.Normalize()

	dir, err := ResolveDirectory(cfg.Override, cfg.Directory)
	if err != nil || dir == "" {
		// Fall back to console-only rather than failing the caller; a broken
		// log destination must not stop the app from starting.
		dir = ""
	}

	s.dir = dir
	switch {
	case dir == "" || cfg.NoFile:
		s.rot = nil
	case EnsureDirectory(dir) != nil:
		// An unusable directory degrades to console-only for the same reason a
		// bad level falls back to info: the app still starts and still says what
		// happened. The failure is reported by the caller of Directory, which
		// finds the file sink absent rather than a sink that fails on write.
		s.rot = nil
		s.dir = ""
	default:
		s.rot = newRotator(dir, cfg)
	}

	s.cfg = cfg
	s.rebuild()
}

// rebuild swaps in a fresh writer stack. The caller holds s.mu.
func (s *Service) rebuild() {
	// zerolog's global level starts at debug, and it is consulted when the event
	// is created — before any writer sees it. Leaving it there would quietly
	// discard the trace records the file sink is configured to keep, so it is
	// opened all the way up here. Every path that installs a stack goes through
	// this function, which is what keeps that promise true however the Service was
	// built.
	zerolog.SetGlobalLevel(zerolog.TraceLevel)

	s.root = newLogger(s.stack(s.cfg))
	zlog.Logger = s.root
}

// newLogger builds the root logger. It is kept at trace on purpose: filtering
// belongs to the sink gates so SetLevel stays a single atomic store.
func newLogger(sinks []io.Writer) zerolog.Logger {
	return zerolog.New(zerolog.SyncWriter(zerolog.MultiLevelWriter(sinks...))).
		With().Timestamp().Str("app", appName).Logger()
}

// NewBootstrap returns a throwaway logger for code that runs before the config
// is known (CLI flag parsing, container construction failures). It writes plain
// lines to w so the user sees what went wrong even if the real log file was
// never opened.
func NewBootstrap(w io.Writer, debug bool) *zerolog.Logger {
	if w == nil {
		w = io.Discard
	}

	level := zerolog.InfoLevel
	if debug {
		level = zerolog.DebugLevel
	}

	lg := zerolog.New(w).Level(level).With().Timestamp().Str("app", appName).Logger()

	return &lg
}

// NewConsoleOnly returns the fallback logger used when no log file can be
// opened. Output goes to stderr because the GUI has no other terminal.
func NewConsoleOnly(debug bool) *zerolog.Logger {
	level := zerolog.InfoLevel
	if debug {
		level = zerolog.DebugLevel
	}

	lg := zerolog.New(newConsoleSink(os.Stderr, FormatText)).
		Level(level).With().Timestamp().Str("app", appName).Logger()

	return &lg
}

// Debug logs at debug level through the process-wide logger.
func Debug() *zerolog.Event { return zlog.Debug() }

// Info logs at info level through the process-wide logger.
func Info() *zerolog.Event { return zlog.Info() }

// Warn logs at warn level through the process-wide logger.
func Warn() *zerolog.Event { return zlog.Warn() }

// Error logs at error level through the process-wide logger.
func Error() *zerolog.Event { return zlog.Error() }
