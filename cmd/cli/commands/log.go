package commands

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/logging"
	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// logLevels are the levels `dmm logs level` accepts, ordered quietest first so
// the help text and any listing read in the same direction the levels do.
var logLevels = []string{"trace", "debug", "info", "warn", "error"}

// defaultTailLines is how many records `dmm logs tail` shows without -n: 100
// rather than 20, because the tail is run to answer "what just happened".
const defaultTailLines = 100

// logView is the JSON shape of one log record, separate from logging.LogLine for
// the reason every table here has a --json form, and because the text view folds
// the timestamp down to the second and inlines the fields.
type logView struct {
	Fields    any    `json:"fields,omitempty"`
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	ModID     string `json:"mod_id,omitempty"`
	Caller    string `json:"caller,omitempty"`
	Message   string `json:"message"`
}

func newLogCommand(s *session) *cobra.Command {
	group := &cobra.Command{
		Use:   "logs",
		Short: "Read and manage the manager's logs",
		Long: "Read, filter and manage the log files the manager writes.\n\n" +
			"Every line shown here has already been through the same redaction the\n" +
			"file sink applies, so a credential cannot appear on screen unless it is\n" +
			"absent from the file too. See `dmm logs dir` for where the files are and\n" +
			"`dmm logs level` for the current verbosity.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newLogTailCommand(s),
		newLogListCommand(s),
		newLogDirCommand(s),
		newLogLevelCommand(s),
		newLogOpenCommand(s),
		newLogCleanCommand(s),
	)

	return group
}

// logService returns the log service the manager was built with. It goes through
// the session's container so `dmm logs level debug` changes the level of the
// logger this invocation is already using and `logs dir` reports one answer.
func (s *session) logService(ctx context.Context) (*logging.Service, error) {
	container, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}

	if container.Logging == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"this build has no log service; logging is unavailable")
	}

	return container.Logging, nil
}

// logPath is the file currently being written.
func logPath(service *logging.Service) string {
	dir := service.Directory()
	if dir == "" {
		return ""
	}

	return logging.LogPath(dir)
}

func newLogTailCommand(s *session) *cobra.Command {
	var (
		count     int
		follow    bool
		levelName string
	)

	command := &cobra.Command{
		Use:   "tail",
		Short: "Print the most recent log records",
		Long: "Print the last records of the current log file.\n\n" +
			"With --follow, keep printing as new records arrive. The follow survives\n" +
			"rotation: when the file rolls over, the tail reopens the new one and\n" +
			"carries on rather than exiting, which is the moment a long-running\n" +
			"session is most likely to be watching.\n\n" +
			"Ctrl-c stops it. --level filters what is printed without changing the\n" +
			"level the manager itself writes at.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := s.logService(cmd.Context())
			if err != nil {
				return err
			}

			minLevel := service.Level()

			if level := strings.TrimSpace(levelName); level != "" {
				// Validated rather than handed to logging.ParseLevel, which falls
				// back to info: a misspelled filter would print a different set of records.
				name, err := logLevelName(level)
				if err != nil {
					return err
				}

				minLevel = logging.ParseLevel(name)
			}

			path := logPath(service)
			if path == "" {
				return appErrors.New(appErrors.ErrCodeConfigInvalid,
					"no log file is being written (--no-log-file disables it)")
			}

			lines, err := logging.Tail(path, count, minLevel)
			if err != nil {
				return err
			}

			for _, line := range lines {
				if err := s.writeLogLine(line); err != nil {
					return err
				}
			}

			if !follow {
				// "no records yet" is reported rather than left as silence: a
				// tail that printed nothing is indistinguishable from a broken one.
				if len(lines) == 0 {
					_, err = fmt.Fprintf(s.env.Err, "no log records yet in %s\n", path)
					return err
				}

				return nil
			}

			return s.followLogs(cmd.Context(), path, minLevel)
		},
	}
	flags := command.Flags()
	flags.IntVarP(&count, "lines", "n", defaultTailLines, "how many records to print")
	flags.BoolVarP(&follow, "follow", "f", false, "keep printing new records as they arrive")
	flags.StringVar(&levelName, "level", "", "only print records at or above this level")

	return command
}

// followLogs streams new records until the context is cancelled. A cancelled
// context is the ordinary ending and reported as success, so `tail -f` in a
// script does not look like it failed.
func (s *session) followLogs(ctx context.Context, path string, minLevel zerolog.Level) error {
	stream, err := logging.Follow(ctx, path)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-stream:
			if !ok {
				// A closed channel is the end of the log, not a failure: only
				// one of "nothing more" and "the stream broke" should stop a script.
				return nil
			}
			// The backlog's filter applies to the stream too: a follower that
			// started filtered and then printed everything would be worse than none.
			if level := logging.ParseLevel(line.Level); level < minLevel {
				continue
			}

			if err := s.writeLogLine(line); err != nil {
				return err
			}
		}
	}
}

// writeLogLine renders one record in the format the flags asked for. JSON is one
// object per line rather than an array because a follower streams.
func (s *session) writeLogLine(line logging.LogLine) error {
	if s.opts.asJSON {
		return writeJSON(s.env.Out, logView{
			Timestamp: line.Timestamp,
			Level:     line.Level,
			ModID:     line.ModID,
			Caller:    line.Caller,
			Message:   line.Message,
			Fields:    line.Fields,
		})
	}

	_, err := fmt.Fprintln(s.env.Out, describeLogLine(line))

	return err
}

// describeLogLine renders a record the way a person reads a log: the time, the
// severity, and what happened. Fields are appended rather than hidden and the
// severity spelled out, because a log is not the place to be terse.
func describeLogLine(line logging.LogLine) string {
	var out strings.Builder
	if stamp := formatLogTime(line.Timestamp); stamp != "" {
		out.WriteString(stamp)
		out.WriteString(" ")
	}

	if level := strings.ToLower(strings.TrimSpace(line.Level)); level != "" {
		out.WriteString(level)
		out.WriteString(" ")
	}

	if mod := strings.TrimSpace(line.ModID); mod != "" {
		out.WriteString("[" + mod + "] ")
	}

	out.WriteString(line.Message)

	if extra := describeLogFields(line.Fields); extra != "" {
		out.WriteString(" ")
		out.WriteString(extra)
	}

	return out.String()
}

// formatLogTime shortens an RFC3339 stamp to the second, which is the resolution
// a log is read at. The zone is kept when it is not UTC because two timestamps
// without one are ambiguous the moment somebody compares them across machines.
func formatLogTime(stamp string) string {
	trimmed := strings.TrimSpace(stamp)
	if trimmed == "" {
		return ""
	}

	if i := strings.IndexByte(trimmed, '.'); i >= 0 {
		// Drop the fractional seconds, keeping any zone suffix.
		rest := trimmed[i:]
		if j := strings.IndexAny(rest, "Z+-"); j > 0 {
			return trimmed[:i] + rest[j:]
		}

		return trimmed[:i]
	}

	return trimmed
}

// describeLogFields renders the extra fields of a record as key=value pairs,
// sorted so two records with the same fields read the same way.
func describeLogFields(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}

	only := 0

	for key := range fields {
		if key != appField {
			only++
		}
	}

	if only == 0 {
		return ""
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		if key == appField {
			continue
		}

		keys = append(keys, key)
	}

	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+renderLogValue(fields[key]))
	}

	return strings.Join(pairs, " ")
}

// appField is the tag every record carries, dropped from the text view because
// it is the same on every line; the JSON view keeps it for scripts filtering on it.
const appField = "app"

func renderLogValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "-"
	case string:
		if typed == "" {
			return `""`
		}

		return typed
	case float64:
		// JSON numbers decode as float64 and a whole number that prints as "3"
		// reads better than one that prints as "3.000000".
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}

		return strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

// logFileView is the JSON shape of one log file on disk.
type logFileView struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	HumanSize  string `json:"human_size"`
	ModTime    string `json:"mod_time"`
	Size       int64  `json:"size"`
	Rotated    bool   `json:"rotated"`
	Compressed bool   `json:"compressed"`
}

func newLogListCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the log files and what they take up",
		Long: "List the current log file and any rotated ones, newest first.\n\n" +
			"The total is reported because the size limit is a bound, not a promise:\n" +
			"seeing the real on-disk number is how a user can confirm the rotation\n" +
			"settings are doing what they think.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := s.logService(cmd.Context())
			if err != nil {
				return err
			}

			dir := service.Directory()
			if dir == "" {
				return appErrors.New(appErrors.ErrCodeConfigInvalid,
					"no log directory is in use (--no-log-file disables the file sink)")
			}

			files, err := logging.ListFiles(dir)
			if err != nil {
				return err
			}

			views := make([]logFileView, 0, len(files))

			var total int64
			for _, file := range files {
				total += file.Size
				views = append(views, logFileView{
					Name:       file.Name,
					Path:       file.Path,
					Size:       file.Size,
					HumanSize:  humanSize(file.Size),
					ModTime:    formatStamp(file.ModTime),
					Rotated:    file.Rotated,
					Compressed: file.Compressed,
				})
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]any{
					"directory":  dir,
					"files":      views,
					"total_size": total,
					"total":      humanSize(total),
				})
			}

			table := NewTable("name", "size", "modified", "kind")
			for _, file := range files {
				table.Add(file.Name, humanSize(file.Size), formatStamp(file.ModTime),
					describeLogFile(file))
			}

			if table.Empty() {
				// An empty directory is stated rather than printed as nothing: a
				// bare prompt reads as "no answer" when the answer is "nothing logged yet".
				_, err := fmt.Fprintf(s.env.Out, "no log files in %s\n", dir)
				return err
			}

			if err := table.Render(s.env.Out); err != nil {
				return err
			}
			// The total comes after the table because it is about the directory,
			// not any one file; in the JSON form it rides along as a field instead.
			_, err = fmt.Fprintf(s.env.Out, "%s in %s\n", humanSize(total), dir)

			return err
		},
	}
}

// describeLogFile names a file's role, which is the column a person scanning the
// listing is actually looking for.
func describeLogFile(file logging.LogFile) string {
	switch {
	case file.Rotated && file.Compressed:
		return "rotated, compressed"
	case file.Rotated:
		return "rotated"
	default:
		return "current"
	}
}

func newLogDirCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "dir",
		Short: "Print the directory the logs are written to",
		Long: "Print the resolved log directory.\n\n" +
			"It is resolved rather than configured: --log-dir wins over the\n" +
			"DMM_LOG_DIR environment variable, which wins over logging.directory in\n" +
			"the settings file, which wins over the platform default. Printing the\n" +
			"answer is how a person finds out which of those actually applied.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := s.logService(cmd.Context())
			if err != nil {
				return err
			}

			dir := service.Directory()
			if dir == "" {
				return appErrors.New(appErrors.ErrCodeConfigInvalid,
					"no log directory is in use (--no-log-file disables the file sink)")
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]any{
					"directory": dir,
					"file":      logPath(service),
					"level":     service.Level().String(),
				})
			}

			_, err = fmt.Fprintln(s.env.Out, dir)

			return err
		},
	}
}

func newLogLevelCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "level [level]",
		Short: "Show or change the log level",
		Long: "Print the current log level, or set it.\n\n" +
			"With a level, the change is applied to the running manager immediately —\n" +
			"the writer stack is not rebuilt, so nothing buffered is lost — and saved\n" +
			"as logging.level so the next run starts at the same level.\n\n" +
			"Levels, quietest first: trace, debug, info, warn, error.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := s.logService(cmd.Context())
			if err != nil {
				return err
			}

			if len(args) == 0 {
				if s.opts.asJSON {
					return writeJSON(s.env.Out, map[string]any{
						"level": service.Level().String(),
					})
				}

				_, err = fmt.Fprintln(s.env.Out, service.Level().String())

				return err
			}

			name, err := logLevelName(args[0])
			if err != nil {
				return err
			}

			service.SetLevel(logging.ParseLevel(name))
			// Saved through the settings store rather than the file, so the
			// value is validated and formatted as `dmm config set logging.level` would.
			client, err := s.client(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.SetConfigValue(cmd.Context(), "logging.level", name); err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]any{"level": name})
			}

			_, err = fmt.Fprintf(s.env.Out, "log level: %s\n", name)

			return err
		},
	}
}

// logLevelName validates a level a person typed. Validation rather than
// logging.ParseLevel, which falls back to info: a misspelled level would be
// accepted as a silent change to a different verbosity.
func logLevelName(value string) (string, error) {
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

func newLogOpenCommand(s *session) *cobra.Command {
	return &cobra.Command{
		Use:   "open",
		Short: "Open the log directory in the file manager",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := s.logService(cmd.Context())
			if err != nil {
				return err
			}

			dir := service.Directory()
			if dir == "" {
				return appErrors.New(appErrors.ErrCodeConfigInvalid,
					"no log directory is in use (--no-log-file disables the file sink)")
			}

			if err := platform.OpenDirectory(
				cmd.Context(),
				domain.CurrentPlatform(),
				dir,
			); err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]any{"directory": dir})
			}

			_, err = fmt.Fprintf(s.env.Out, "opened %s\n", dir)

			return err
		},
	}
}

func newLogCleanCommand(s *session) *cobra.Command {
	var all bool

	command := &cobra.Command{
		Use:   "clean",
		Short: "Delete log files",
		Long: "Delete the rotated log files, keeping the current one.\n\n" +
			"Pass --all to truncate the current file as well. The rotation settings are\n" +
			"kept either way: this removes records, it does not change how many the\n" +
			"manager keeps or how large they get.\n\n" +
			"Nothing here is prompted for. The files being removed are the manager's\n" +
			"own logs, in its own log directory, and the count of what went is printed\n" +
			"so a mistake is visible.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := s.logService(cmd.Context())
			if err != nil {
				return err
			}

			dir := service.Directory()
			if dir == "" {
				return appErrors.New(appErrors.ErrCodeConfigInvalid,
					"no log directory is in use (--no-log-file disables the file sink)")
			}

			removed, err := logging.Clean(dir, all)
			if err != nil {
				return err
			}

			if s.opts.asJSON {
				return writeJSON(s.env.Out, map[string]any{
					"directory": dir,
					"removed":   removed,
				})
			}
			// Zero is stated rather than left as silence, so "cleaned" cannot be
			// mistaken for "there was something and it is gone".
			_, err = fmt.Fprintf(s.env.Out, "logs cleaned: %s\n", plural(removed, "file", "files"))

			return err
		},
	}
	command.Flags().BoolVar(&all, "all", false, "truncate the current log file as well")

	return command
}
