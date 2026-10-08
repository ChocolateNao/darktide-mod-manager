package commands

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/logging"
	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// LogSettings is what the Logs page renders its header from.
//
// It is a struct rather than the log service's Config because the frontend needs
// the *resolved* values: Directory is where the files actually are after
// --log-dir, the environment variable and the settings file have all had their
// say, and a page that showed the configured value instead would send the user
// looking in the wrong place.
type LogSettings struct {
	// Level is the severity currently in force.
	Level string `json:"level"`
	// Directory is the resolved log directory, empty when no file sink is active.
	Directory string `json:"directory"`
	// File is the log file currently being written.
	File string `json:"file"`
	// The remaining fields mirror logging.Config so a settings form can edit
	// them; they are the persisted values, which is what a form should show.
	MaxSizeMB    int  `json:"max_size_mb"`
	MaxBackups   int  `json:"max_backups"`
	MaxAgeDays   int  `json:"max_age_days"`
	Compress     bool `json:"compress"`
	ConsoleOnly  bool `json:"console_only"`
	RedactFields bool `json:"has_redact_fields"`
}

// LogCommands is the Logs page as a frontend sees it: read the log, filter it,
// and manage the files behind it.
//
// The live stream is not here. It arrives as api.EventLog events on the event
// bus — a Wails frontend is already subscribed for other events, and a second
// subscription just for logs would be two mechanisms delivering the same records.
type LogCommands struct {
	*base
}

// NewLogCommands builds the log command set.
func NewLogCommands(deps Deps) *LogCommands {
	return &LogCommands{base: newBase(deps)}
}

func newLogCommands(b *base) *LogCommands {
	return &LogCommands{base: b}
}

// logService returns the log service, or the error to report.
//
// Every method goes through this rather than holding the service itself, because
// ready() is also what refuses calls after a shutdown — and "read the log" on a
// window that is closing should fail like every other operation rather than
// quietly succeed against a service whose sinks are already closed.
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

// GetLogSettings reports the active and configured logging settings.
func (l *LogCommands) GetLogSettings() (*LogSettings, error) {
	service, err := l.logService()
	if err != nil {
		return nil, err
	}

	settings := &LogSettings{
		Level:        service.Level().String(),
		Directory:    service.Directory(),
		RedactFields: false,
	}
	if settings.Directory != "" {
		settings.File = logging.LogPath(settings.Directory)
	}
	// The persisted half comes from the configuration store rather than from the
	// service: the service's own copy has already been folded together with the
	// command line, and a settings form that showed an override as though it
	// were saved would write the override to disk on the next save.
	client, ctx, cancel, err := l.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	cfg, err := client.GetConfig(ctx)
	if err != nil {
		return nil, l.fail(err, "read the logging settings")
	}

	settings.MaxSizeMB = cfg.Logging.MaxSizeMB
	settings.MaxBackups = cfg.Logging.MaxBackups
	settings.MaxAgeDays = cfg.Logging.MaxAgeDays
	settings.Compress = cfg.Logging.Compress
	// Whether any extra field is configured, never which ones: the redact list
	// is a list of names the user considers sensitive, and a settings page that
	// rendered it would be a page that encourages adding a field to it by name.
	settings.RedactFields = len(cfg.Logging.Redact) > 0

	return settings, nil
}

// SetLogLevel changes the level of the running manager and saves it, so the next
// launch starts at the same level without the user editing the settings file.
//
// The change is applied before it is saved. That ordering means a failure to
// write the file still leaves the window at the level the user just picked, which
// is what they were trying to achieve; the reverse would leave the settings
// claiming a level the running manager is not at.
func (l *LogCommands) SetLogLevel(level string) error {
	service, err := l.logService()
	if err != nil {
		return err
	}

	name, err := logLevelOf(level)
	if err != nil {
		return err
	}

	client, ctx, cancel, err := l.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.SetConfigValue(ctx, "logging.level", name); err != nil {
		return l.fail(err, "save the log level")
	}

	service.SetLevel(logging.ParseLevel(name))

	return nil
}

// logLevelOf validates a level a person typed into the GUI.
//
// The same validation the CLI does, in the same order, so a level the terminal
// accepts cannot be refused by the window for looking different.
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

// TailLogs returns the most recent records, filtered to minLevel.
//
// count is the number of records asked for; a non-positive one takes the default
// rather than being an error, because a UI paginator that starts at zero has not
// made a mistake worth refusing a page over.
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
		// An empty slice rather than a nil one: a JS array is not null, and a
		// frontend that maps over the result would have to special-case a state
		// that means nothing here.
		return []logging.LogLine{}, nil
	}

	return lines, nil
}

// defaultLogPageSize is how many records TailLogs returns when the caller does
// not say. It matches the CLI's default so the two surfaces show the same window
// of history for the same invocation.
const defaultLogPageSize = 100

// ListLogFiles describes the log files, newest first.
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

// OpenLogDirectory opens the log directory in the OS file manager.
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

// ClearLogs deletes the rotated log files and, with all set, truncates the current
// one. The rotation settings are kept: this removes records, it does not change
// how many are kept or how large they get.
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

// SetRedactFields replaces the extra field names whose values are masked on the
// way to every sink.
//
// The names are accepted and never returned. A field name in this list is one the
// user believes holds something sensitive, and a list of those is a map of where
// the secrets are — the log file itself is the safe place for that map to exist,
// and the GUI is not. GetLogSettings therefore reports only whether any are
// configured.
//
// An empty list clears the extras. It is not treated as "leave them alone",
// because a frontend that sends an empty list after the user deleted the last
// entry would otherwise have no way to express that.
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
	// Applied live as well, or the change would not take effect until the next
	// launch — and a user who has just told the manager to stop logging a field
	// should not have to wait for that to take effect.
	service.SetRedactFields(cleaned)

	return nil
}
