package logging

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// tailLimit caps how much of a log file Tail will read. Rotation caps the file
// at MaxSizeMB, so this is above any file we can be handed while still bounding
// the memory a CLI tail can use.
const tailLimit = 16 << 20

// followPoll is how often Follow checks for more bytes. Polling is deliberate:
// inotify, kqueue and ReadDirectoryChangesW have three different APIs and two of
// them miss the common case (a rotated file being renamed).
const followPoll = 200 * time.Millisecond

// LogLine is one decoded log record. It is the payload behind `dmm logs tail`
// rows and behind the GUI's log:entry events, so both surfaces agree on shape.
type LogLine struct {
	// Timestamp is the RFC3339 stamp zerolog wrote.
	Timestamp string `json:"timestamp"`
	// Level is the record severity ("debug", "info", ...).
	Level string `json:"level"`
	// Caller is the source location, present only when caller data is on.
	Caller string `json:"caller,omitempty"`
	// Message is the human-readable text.
	Message string `json:"message"`
	// ModID scopes the record to a mod, when the call site said so.
	ModID string `json:"mod_id,omitempty"`
	// Fields holds everything else, already redacted by the sink.
	Fields map[string]any `json:"fields,omitempty"`
	// Raw is the original JSON line, kept for a detail view. It is excluded
	// from JSON so a GUI event payload does not carry the line twice.
	Raw string `json:"-"`
}

// LogFile is one entry in the log directory.
type LogFile struct {
	ModTime    time.Time `json:"mod_time"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	Rotated    bool      `json:"rotated"`
	Compressed bool      `json:"compressed"`
}

// ParseLine decodes a zerolog JSON line. It reports false for anything that is
// not a JSON object, which is how the reader tells a log line from a stray
// byte range in a file being appended to.
func ParseLine(raw []byte) (LogLine, bool) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(bytes.TrimRight(raw, "\n"), &fields); err != nil {
		return LogLine{}, false
	}

	line := LogLine{Raw: string(bytes.TrimRight(raw, "\n"))}

	for key, value := range fields {
		text := rawString(value)

		switch normalizeKey(key) {
		case "timestamp", "time":
			line.Timestamp = text
		case "level":
			line.Level = text
		case "caller":
			line.Caller = text
		case "message", "msg":
			line.Message = text
		case "mod", "modid":
			line.ModID = text
		default:
			if line.Fields == nil {
				line.Fields = map[string]any{}
			}

			line.Fields[key] = decodeValue(value)
		}
	}

	return line, true
}

// rawString renders a JSON scalar as text. Objects and arrays are kept in their
// JSON form so a structured field is still readable in a tail.
func rawString(value json.RawMessage) string {
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text
	}

	return string(value)
}

// decodeValue turns a raw field into something JSON can carry back out: scalars
// become Go values and nested structures become maps and slices.
func decodeValue(value json.RawMessage) any {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return string(value)
	}

	return decoded
}

// Tail returns the last n records of the log file at path, oldest first.
//
// A missing file is not an error: `--follow` must work before the first line is
// written. Pass zerolog.TraceLevel to keep everything.
func Tail(path string, n int, minLevel zerolog.Level) ([]LogLine, error) {
	if n <= 0 {
		n = 200
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigInvalid,
			"could not read log file %s",
			path,
		)
	}

	if len(data) > tailLimit {
		data = data[len(data)-tailLimit:]
	}

	lines := parseLines(data, minLevel)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return lines, nil
}

// parseLines decodes every complete line in data that passes minLevel. A
// trailing partial line (the file is being appended to) is ignored.
func parseLines(data []byte, minLevel zerolog.Level) []LogLine {
	var lines []LogLine

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line, ok := ParseLine(scanner.Bytes())
		if !ok {
			continue
		}

		if level, err := zerolog.ParseLevel(line.Level); err != nil || level < minLevel {
			continue
		}

		lines = append(lines, line)
	}

	return lines
}

// Follow streams records appended to path until ctx is cancelled, surviving
// rotation: a shrink means lumberjack renamed the file aside, so the reader
// reopens the path.
//
// The channel closes with the context, so callers range over it for a clean exit.
func Follow(ctx context.Context, path string) (<-chan LogLine, error) {
	file, err := os.Open(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigInvalid,
			"could not open log file %s",
			path,
		)
	}
	// Start at the end: a follower wants new records, not the whole history.
	offset := int64(0)

	if file != nil {
		if info, err := file.Stat(); err == nil {
			offset = info.Size()
		} else {
			offset = 0
		}

		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			_ = file.Close()

			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigInvalid,
				"could not seek log file %s",
				path,
			)
		}
	}

	out := make(chan LogLine, 256)
	go follow(ctx, path, file, offset, out)

	return out, nil
}

// follow is the body of Follow. It polls for bytes and reopens on rotation.
func follow(ctx context.Context, path string, file *os.File, offset int64, out chan<- LogLine) {
	defer close(out)

	ticker := time.NewTicker(followPoll)
	defer ticker.Stop()

	for {
		if file != nil && rotated(path, file, offset) {
			_ = file.Close()
			file, offset = nil, 0
		}

		if file == nil {
			if opened, err := os.Open(path); err == nil {
				file, offset = opened, 0
			}
		}

		if file != nil {
			offset = stream(ctx, file, offset, out)
		}

		select {
		case <-ctx.Done():
			if file != nil {
				_ = file.Close()
			}

			return
		case <-ticker.C:
		}
	}
}

// rotated reports whether the file at path is no longer the one file holds.
//
// The check is on the path, not the held handle, which is why this exists.
// lumberjack rotates by renaming the current file aside: the handle stays
// readable but nothing writes to it again, so its size never grows and never
// looks shorter than the offset — a follower watching only its handle would sit
// on the renamed generation forever. The size check covers the other change,
// `dmm logs clean --all` truncating the same file in place.
func rotated(path string, file *os.File, offset int64) bool {
	held, err := file.Stat()
	if err != nil {
		return true
	}

	current, err := os.Stat(path)
	if err != nil {
		// The path has gone away entirely. Reopening will either find the new
		// generation or wait for it; either way there is nothing more to read here.
		return true
	}

	return !os.SameFile(held, current) || current.Size() < offset
}

// stream reads whatever is available in file and sends the decoded records. It
// returns the offset just past the last complete line it consumed.
func stream(ctx context.Context, file *os.File, offset int64, out chan<- LogLine) int64 {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset
	}

	reader := bufio.NewReader(file)
	consumed := offset

	for {
		raw, err := reader.ReadBytes('\n')
		if err != nil || len(raw) == 0 {
			// A partial line: leave the offset at the last newline so the rest
			// is re-read once the writer finishes it.
			return consumed
		}

		consumed += int64(len(raw))
		if line, ok := ParseLine(raw); ok {
			select {
			case out <- line:
			case <-ctx.Done():
				return consumed
			}
		}
	}
}

// ListFiles describes the log files in dir, most recently modified first. It
// reports an error only when dir cannot be listed at all; an empty directory
// simply has no log files yet.
func ListFiles(dir string) ([]LogFile, error) {
	if dir == "" {
		return nil, appErrors.New(
			appErrors.ErrCodeInvalidArgument,
			"log directory is not configured",
		)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigInvalid,
			"could not list log directory %s",
			dir,
		)
	}

	var files []LogFile

	for _, entry := range entries {
		if entry.IsDir() || !isLogName(entry.Name()) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, LogFile{
			Name:       entry.Name(),
			Path:       filepath.Join(dir, entry.Name()),
			Size:       info.Size(),
			ModTime:    info.ModTime(),
			Rotated:    entry.Name() != LogFileName,
			Compressed: strings.HasSuffix(entry.Name(), ".gz"),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].ModTime.Equal(files[j].ModTime) {
			return files[i].Name < files[j].Name
		}

		return files[i].ModTime.After(files[j].ModTime)
	})

	return files, nil
}

// isLogName reports whether name looks like a file this package wrote, so a
// user's unrelated droppings in the log directory are left alone.
func isLogName(name string) bool {
	base := strings.TrimSuffix(name, ".gz")
	return base == LogFileName || strings.HasPrefix(base, "dmm-")
}

// Clean removes log files from dir and reports how many went. Rotated files are
// removed first; with all set the active file is truncated too, which is what
// the GUI's "clear logs" button wants.
func Clean(dir string, all bool) (int, error) {
	files, err := ListFiles(dir)
	if err != nil {
		return 0, err
	}

	removed := 0

	for _, file := range files {
		if !file.Rotated && !all {
			continue
		}

		if !file.Rotated {
			if err := os.Truncate(file.Path, 0); err != nil {
				return removed, appErrors.Wrap(
					err,
					appErrors.ErrCodeConfigInvalid,
					"could not truncate %s",
					file.Path,
				)
			}

			removed++

			continue
		}

		if err := os.Remove(file.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, appErrors.Wrap(
				err,
				appErrors.ErrCodeConfigInvalid,
				"could not remove %s",
				file.Path,
			)
		}

		removed++
	}

	return removed, nil
}
