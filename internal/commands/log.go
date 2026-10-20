package commands

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/logging"
	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// LogSettings is what the Logs page renders its header from. It is separate from
// logging.Config because the frontend needs the *resolved* values: showing the
// configured Directory would send the user looking in the wrong place once
// --log-dir and the environment have had their say.
type LogSettings struct {
	// Level is the severity currently in force.
	Level string `json:"level"`
	// Directory is the resolved log directory, empty when no file sink is active.
	Directory string `json:"directory"`
	// File is the log file currently being written.
	File string `json:"file"`
	// The remaining fields mirror logging.Config, the persisted values a
	// settings form edits.
	MaxSizeMB    int  `json:"max_size_mb"`
	MaxBackups   int  `json:"max_backups"`
	MaxAgeDays   int  `json:"max_age_days"`
	Compress     bool `json:"compress"`
	ConsoleOnly  bool `json:"console_only"`
	RedactFields bool `json:"has_redact_fields"`
}

type LogCommands struct {
	*base
}

func NewLogCommands(deps Deps) *LogCommands {
	return &LogCommands{base: newBase(deps)}
}

func newLogCommands(b *base) *LogCommands {
	return &LogCommands{base: b}
}

// logService returns the log service, or the error to report. Every method goes
// through it so ready() still refuses calls after a shutdown.
func (l *LogCommands) logService() (*logging.Service, error) {
	if _, _, _, err := l.ready(); err != nil {
		return nil, err
	}

	if l.logging == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"this build has no log service; check the wiring in main")
	}

	return l.logging, nil
}

// TailLogs returns the most recent records, filtered to minLevel.
func (l *LogCommands) TailLogs(count int, minLevel string) ([]logging.LogLine, error) {
	service, err := l.logService()
	if err != nil {
		return nil, err
	}

	if count <= 0 {
		count = defaultLogPageSize
	}

	level := logging.TraceLevel

	if trimmed := strings.TrimSpace(minLevel); trimmed != "" {
		name, err := logLevelOf(trimmed)
		if err != nil {
			return nil, err
		}

		level = logging.ParseLevel(name)
	}

	path := logging.LogPath(service.Directory())
	if path == "" {
		return nil, appErrors.New(appErrors.ErrCodeConfigInvalid,
			"no log file is being written")
	}

	lines, err := logging.Tail(path, count, level)
	if err != nil {
		return nil, l.fail(err, "read the log")
	}

	if lines == nil {
		return []logging.LogLine{}, nil
	}

	return lines, nil
}

// defaultLogPageSize is how many records TailLogs returns when the caller does
// not say. It matches the CLI's default so both surfaces show the same window of
// history.
const defaultLogPageSize = 100

// SetRedactFields replaces the extra field names whose values are masked on the
// way to every sink. The names are accepted and never returned: the list is a map
// of where the user's secrets are. An empty list clears the extras, or deleting
// the last entry could not be expressed.
func (l *LogCommands) SetRedactFields(fields []string) error {
	service, err := l.logService()
	if err != nil {
		return err
	}

	cleaned := make([]string, 0, len(fields))
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}

	client, ctx, cancel, err := l.ready()
	if err != nil {
		return err
	}
	defer cancel()

	cfg, err := client.GetConfig(ctx)
	if err != nil {
		return l.fail(err, "read the logging settings")
	}

	update := cfg.Clone()

	update.Logging.Redact = cleaned
	if err := client.UpdateConfig(ctx, update); err != nil {
		return l.fail(err, "save the redaction list")
	}
	// Applied live as well: a user who has just asked the manager to stop logging
	// a field should not wait for the next launch.
	service.SetRedactFields(cleaned)

	return nil
}

func (l *LogCommands) ListLogFiles() ([]logging.LogFile, error) {
	service, err := l.logService()
	if err != nil {
		return nil, err
	}

	files, err := logging.ListFiles(service.Directory())
	if err != nil {
		return nil, l.fail(err, "list the log files")
	}

	if files == nil {
		return []logging.LogFile{}, nil
	}

	return files, nil
}

func (l *LogCommands) OpenLogDirectory() error {
	service, err := l.logService()
	if err != nil {
		return err
	}

	dir := service.Directory()
	if dir == "" {
		return appErrors.New(appErrors.ErrCodeConfigInvalid, "no log directory is in use")
	}

	_, ctx, cancel, err := l.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := platform.OpenDirectory(ctx, domain.CurrentPlatform(), dir); err != nil {
		return l.fail(err, "open the log directory")
	}

	return nil
}

func (l *LogCommands) ClearLogs(all bool) error {
	service, err := l.logService()
	if err != nil {
		return err
	}

	dir := service.Directory()
	if dir == "" {
		return appErrors.New(appErrors.ErrCodeConfigInvalid, "no log directory is in use")
	}

	if _, err := logging.Clean(dir, all); err != nil {
		return l.fail(err, "clear the logs")
	}

	return nil
}

func logLevelOf(level string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(level))
	switch trimmed {
	case "trace", "debug", "info", "warn", "error":
		return trimmed, nil
	}

	return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
		"%q is not a log level: expected trace, debug, info, warn or error",
		strings.TrimSpace(level))
}
