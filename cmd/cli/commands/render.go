package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"golang.org/x/term"
)

// Table is a left-aligned, column-aligned text table. Width is measured in runes
// because mod names are often not ASCII, and padding by bytes misaligns every
// column after it.
type Table struct {
	headers []string
	rows    [][]string
}

// NewTable starts a table with the given headers. A header is never truncated:
// headers are written here, so one that does not fit means the table needs fewer
// columns.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers}
}

// Add appends a row. Short rows are padded and long rows truncated to the header
// count, so a row that gained a field cannot shift every column after it.
func (t *Table) Add(cells ...string) {
	row := make([]string, len(t.headers))
	for i := range row {
		if i < len(cells) {
			row[i] = sanitizeCell(cells[i])
		}
	}

	t.rows = append(t.rows, row)
}

func (t *Table) Addf(format string, args ...any) {
	t.Add(fmt.Sprintf(format, args...))
}

func (t *Table) Empty() bool { return len(t.rows) == 0 }

func (t *Table) Len() int { return len(t.rows) }

// Render writes the table. Its name avoids io.WriterTo, whose signature counts
// bytes. A terminal output is shaped to that terminal's grid — see gridOf.
func (t *Table) Render(w io.Writer) error {
	if len(t.rows) == 0 {
		return nil
	}

	widths, floors := t.measure()
	rows := t.rows

	var elided int

	grid, isTerminal := gridOf(w)
	if isTerminal {
		widths = fitWidths(widths, floors, grid.width)
		rows, elided = rowsForHeight(rows, grid.height)
	}

	var out strings.Builder

	writeRow := func(cells []string) {
		for i, cell := range cells {
			cell = fitCell(cell, widths[i])
			if i == len(cells)-1 {
				out.WriteString(cell)
				continue
			}

			out.WriteString(cell)
			out.WriteString(
				strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+columnPadding),
			)
		}

		out.WriteString("\n")
	}
	writeRow(t.headers)

	for _, row := range rows {
		writeRow(row)
	}

	if elided > 0 {
		out.WriteString(elidedNote(elided))
	}

	_, err := io.WriteString(w, out.String())

	return err
}

// columnPadding is the padding between two columns. Two spaces are what the
// golden files expect.
const columnPadding = 2

// minColumnWidth is how narrow a column may be squeezed; below this the values
// have become letters, and a table of letters is unreadable.
const minColumnWidth = 4

// measure returns each column's natural width and floor. The floor is the wider
// of the header and minColumnWidth, since a header is never truncated.
func (t *Table) measure() (widths, floors []int) {
	widths = make([]int, len(t.headers))

	floors = make([]int, len(t.headers))
	for i, header := range t.headers {
		widths[i] = utf8.RuneCountInString(header)
		floors[i] = max(widths[i], minColumnWidth)
	}

	for _, row := range t.rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}

	return widths, floors
}

// fitWidths narrows widths until the table fits, returning them unchanged when it
// cannot. The widest column that can give way does so one at a time: shrinking
// every column equally sacrifices four readable columns for one unreadable one.
func fitWidths(widths, floors []int, available int) []int {
	if available <= 0 || columnsWidth(widths) <= available {
		return widths
	}

	narrowed := append([]int(nil), widths...)
	for columnsWidth(narrowed) > available {
		widest := -1
		for i, width := range narrowed {
			if i < len(floors) && width > floors[i] && (widest < 0 || width > narrowed[widest]) {
				widest = i
			}
		}

		if widest < 0 {
			return widths
		}

		narrowed[widest]--
	}

	return narrowed
}

// columnsWidth is the rendered width: cells plus the padding between neighbours.
func columnsWidth(widths []int) int {
	if len(widths) == 0 {
		return 0
	}

	total := columnPadding * (len(widths) - 1)
	for _, width := range widths {
		total += width
	}

	return total
}

// fitCell shortens a value to width runes with an ellipsis, since a hard cut reads
// as a rendering fault.
func fitCell(text string, width int) string {
	if width <= 0 {
		return ""
	}

	runes := []rune(text)
	if len(runes) <= width {
		return text
	}

	if width == 1 {
		return "…"
	}

	return string(runes[:width-1]) + "…"
}

// rowsForHeight returns the rows a terminal of this many lines can show and how
// many it cannot. The header and closing note take a line each; the count is
// floored at one row.
func rowsForHeight(rows [][]string, height int) ([][]string, int) {
	// One line for the header and one for the note: a table needing no note still
	// loses the reserved line, which is caution bought with a note never losing a row.
	room := height - 2
	if room < 1 {
		room = 1
	}

	if len(rows) <= room {
		return rows, 0
	}

	return rows[:room], len(rows) - room
}

// elidedNote says how many rows the terminal was too short for. It is a line of
// its own, so nothing on screen is mistakable for data.
func elidedNote(count int) string {
	word := "rows"
	if count == 1 {
		word = "row"
	}

	return fmt.Sprintf("... %d more %s not shown\n", count, word)
}

type terminalGrid struct {
	width  int
	height int
}

// gridOf returns the grid w is written into and whether w is a terminal. A false
// answer renders the table at its natural size, which keeps output a contract
// across pipes, files and tests.
func gridOf(w io.Writer) (terminalGrid, bool) {
	file, ok := w.(*os.File)
	if !ok {
		return terminalGrid{}, false
	}

	descriptor := int(file.Fd())
	if !term.IsTerminal(descriptor) {
		return terminalGrid{}, false
	}

	width, height, err := term.GetSize(descriptor)
	if err != nil || width <= 0 {
		return terminalGrid{}, false
	}

	return terminalGrid{width: width, height: height}, true
}

// sanitizeCell makes a value safe for a table: control characters go, since a tab
// or newline would break alignment. Surrounding whitespace is always trimmed, or
// misalignment would depend on the content of an unrelated field.
func sanitizeCell(cell string) string {
	if !strings.ContainsFunc(cell, needsReplacing) {
		return strings.TrimSpace(cell)
	}

	var out strings.Builder
	out.Grow(len(cell))

	for _, r := range cell {
		switch r {
		case '\t':
			out.WriteString(" ")
		case '\n', '\r':
			out.WriteString(" ")
		default:
			if r < 0x20 || r == 0x7f {
				continue
			}

			out.WriteRune(r)
		}
	}

	return strings.TrimSpace(out.String())
}

func needsReplacing(r rune) bool {
	switch r {
	case '\t', '\n', '\r':
		return true
	}

	return r < 0x20 || r == 0x7f
}

// maxCellWidth bounds prose columns. A description has no natural length, and a
// table that wraps is unreadable; a longer value is cut with an ellipsis.
const maxCellWidth = 48

func Clip(text string) string {
	text = sanitizeCell(text)
	if utf8.RuneCountInString(text) <= maxCellWidth {
		return text
	}

	runes := []rune(text)

	if maxCellWidth <= 1 {
		return "…"
	}

	return string(runes[:maxCellWidth-1]) + "…"
}

// Fields is an aligned key/value block for "show one thing". A block suits prose
// values, which a wrapped two-column table cannot present readably.
type Fields struct {
	keys   []string
	values []string
}

// Set records a value. The value is sanitized but not clipped: this view exists
// to show a path or a description in full, and a truncated path is useless.
func (f *Fields) Set(key, value string) {
	f.keys = append(f.keys, sanitizeCell(key))
	f.values = append(f.values, sanitizeCell(value))
}

func (f *Fields) Setf(key, format string, args ...any) {
	f.Set(key, fmt.Sprintf(format, args...))
}

func (f *Fields) SetOrDash(key, value string) {
	f.Set(key, orDash(value))
}

// Render writes the block. See Table.Render for the naming rationale.
func (f *Fields) Render(w io.Writer) error {
	width := 0
	for _, key := range f.keys {
		if n := utf8.RuneCountInString(key); n > width {
			width = n
		}
	}

	var out strings.Builder
	for i, key := range f.keys {
		out.WriteString(key)
		out.WriteString(":")
		out.WriteString(strings.Repeat(" ", width-utf8.RuneCountInString(key)+1))
		out.WriteString(f.values[i])
		out.WriteString("\n")
	}

	_, err := io.WriteString(w, out.String())

	return err
}

// writeJSON prints indented JSON followed by a newline; without it two documents
// in a row are not parseable. Encoding failures name the value's type.
func writeJSON(w io.Writer, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"cannot render %T as JSON", value)
	}

	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return err
	}

	return nil
}

// writeJSONOrTable is the one place the --json decision is made: a command that
// decides for itself can forget the flag, and the failure mode is JSON on a
// terminal or a table in a script.
func (s *session) writeJSONOrTable(w io.Writer, value any, table *Table, nothing string) error {
	if s.opts.asJSON {
		return writeJSON(w, value)
	}

	if table != nil && !table.Empty() {
		return table.Render(w)
	}

	if nothing == "" {
		return nil
	}

	_, err := fmt.Fprintln(w, nothing)

	return err
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}

// humanSize renders a byte count with a binary unit: cache sizes span four orders
// of magnitude and a raw byte count is unreadable at both ends of that range.
func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTP"[exp])
}

// orDash renders an absent value as a dash. An empty cell is ambiguous between
// "nothing" and "the value was blank and we trimmed it".
func orDash(text string) string {
	if strings.TrimSpace(text) == "" {
		return "-"
	}

	return text
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return "-"
	}

	return Clip(strings.Join(names, ", "))
}

// describeError renders a single-lined error for a table cell, keeping the code:
// it tells whether a missing credential and an unreachable source are the same
// problem. It takes text because callers also have a JSON view.
func describeError(text string) string {
	if strings.TrimSpace(text) == "" {
		return "-"
	}

	return Clip(text)
}

// fatalf builds a user-facing argument error, so every "you passed the wrong
// thing" message arrives as INVALID_ARGUMENT and a caller can branch on it.
func fatalf(format string, args ...any) error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument, format, args...)
}
