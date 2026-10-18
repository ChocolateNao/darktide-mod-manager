package api

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/logging"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// LoggingOptions are the per-process logging overrides the CLI and GUI hand down.
// Kept apart from logging.Config on purpose: config.json records preferences that
// should survive, whereas these describe one invocation. Nothing here is ever
// written back.
type LoggingOptions struct {
	Level   string
	Dir     string
	Format  string
	Redact  []string
	Console bool
	NoFile  bool
}

// ZerologLogger adapts a zerolog logger to the Printf-style Logger this package
// exposes. It exists so the pre-zerolog plumbing (wire diagnostics, the Wails
// command base) has somewhere to write.
type ZerologLogger struct {
	// Logger is the destination. A nil Logger discards.
	Logger *zerolog.Logger
	// Level is the severity Printf reports at.
	Level zerolog.Level
}

// NewZerologLogger wraps l, reporting Printf calls at warn level. Warn is the right
// default: everything routed through it is a "this did not work" diagnostic. Error
// would overstate a failure the user was already told about; debug would hide it.
func NewZerologLogger(l *zerolog.Logger) *ZerologLogger {
	return &ZerologLogger{Logger: l, Level: zerolog.WarnLevel}
}

// Printf implements Logger.
func (z *ZerologLogger) Printf(format string, args ...any) {
	if z == nil || z.Logger == nil {
		return
	}

	level := z.Level
	if level == zerolog.NoLevel {
		level = zerolog.WarnLevel
	}
	// A recovered panic here must not escalate a failed operation into a crash:
	// the caller returns the error separately and it is the error that matters.
	defer func() { _ = recover() }()

	z.Logger.WithLevel(level).Msgf(format, args...)
}

// loggingConfig folds the persisted config block and the per-process overrides into
// one logging.Config. logging.level wins over the legacy advanced.log_level, but
// only once the user has set it: migrateLevel already copied the legacy value
// across.
func loggingConfig(cfg *domain.Config, opts LoggingOptions) logging.Config {
	out := logging.DefaultConfig()
	if cfg != nil {
		out.Level = cfg.Logging.Level
		out.MaxSizeMB = cfg.Logging.MaxSizeMB
		out.MaxBackups = cfg.Logging.MaxBackups
		out.MaxAgeDays = cfg.Logging.MaxAgeDays
		out.Compress = cfg.Logging.Compress
		out.Directory = cfg.Logging.Directory
	}

	if level := strings.TrimSpace(opts.Level); level != "" {
		out.Level = level
	}

	if dir := strings.TrimSpace(opts.Dir); dir != "" {
		out.Override = dir
	}

	if opts.Console {
		out.Console = true
	}

	if format := strings.TrimSpace(opts.Format); format != "" {
		out.Format = format
	}

	if len(opts.Redact) > 0 {
		out.Redact = append(out.Redact, opts.Redact...)
	}

	out.NoFile = opts.NoFile

	return out.Normalize()
}

// logLevels are the spellings a person may type for a log level, shortest form
// first so a suggestion can offer the one people usually reach for.
var logLevels = []string{"trace", "debug", "info", "warn", "error"}

// parseLogLevel validates a level named by a user and returns it in zerolog's
// spelling. It exists beside logging.ParseLevel because that one falls back to info
// for an unrecognised value; a person typing a setting into a field deserves to be
// told "verbse" is not a level.
func parseLogLevel(value string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	for _, level := range logLevels {
		if trimmed == level {
			return level, nil
		}
	}

	return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
		"%q is not a log level: expected one of %s", strings.TrimSpace(value),
		strings.Join(logLevels, ", "))
}

// migrateLevel copies the legacy advanced.log_level into logging.level for a config
// that predates the logging block, so an existing setting keeps working. It reports
// whether it changed anything, so the caller can avoid saving an unchanged config.
func migrateLevel(cfg *domain.Config) bool {
	if cfg == nil || cfg.Logging.Level != "" {
		return false
	}

	level := strings.TrimSpace(cfg.Advanced.LogLevel)
	if level == "" {
		return false
	}

	cfg.Logging.Level = level

	return true
}
