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

// Table is a left-aligned, column-aligned text table.
//
// It is not a general-purpose table: there is one rule it enforces that a
// general-purpose table would not, which is that a column's width is measured
// in runes, not bytes. A mod name is very often not ASCII, and padding by byte
// length misaligns every column after it for every non-English user.
type Table struct {
	headers []string
	rows    [][]string
}

// NewTable starts a table with the given headers. A header cell is never
// truncated: they are written by this program, not by a source, so a header
// that does not fit means the table is too wide and needs fewer columns.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers}
}

// Add appends a row. Short rows are padded with empty cells and long rows are
// truncated to the header count, so a row that gained a field cannot shift
// every column after it.
func (t *Table) Add(cells ...string) {
	row := make([]string, len(t.headers))
	for i := range row {
		if i < len(cells) {
			row[i] = sanitizeCell(cells[i])
		}
	}

	t.rows = append(t.rows, row)
}

// Addf appends a row built with fmt.Sprintf.
func (t *Table) Addf(format string, args ...any) {
	t.Add(fmt.Sprintf(format, args...))
}

// Empty reports whether the table has no rows, which is how a command decides
// between printing a table and printing "nothing to show".
func (t *Table) Empty() bool { return len(t.rows) == 0 }

// Len reports how many rows the table holds.
func (t *Table) Len() int { return len(t.rows) }

// Render writes the table. It is not named WriteTo: that name belongs to
// io.WriterTo, whose signature counts bytes, and a method that does not count
// them would be a lie a compiler check would rightly refuse.
//
// A table with no rows renders as nothing at all rather than as a header with
// nothing under it: a header on its own reads as a rendering bug, and the caller
// has a better sentence for the empty case.
//
// When it is written to a terminal, the table is shaped to fit that terminal's
// grid: wide columns are squeezed rather than left to wrap, and a table taller
// than the screen is cut with a line saying how many rows it left out. See
// gridOf for why that is only done for a terminal.
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

// columnPadding is the padding Render puts between two columns. Two spaces is
// enough to tell the columns apart without making a wide table any wider, and
// it is what every recorded transcript was written with, so changing it would
// make all of them fail for no reason.
const columnPadding = 2

// minColumnWidth is how narrow a column may be squeezed before it stops being
// worth squeezing. A column is as wide as its longest cell, and one long mod
// name or path can make a column ten times the width of the rest, so the width
// is usually one cell's doing; but a column narrower than this has stopped being
// a value and become a letter, and a table of letters is not a smaller table, it
// is an unreadable one.
const minColumnWidth = 4

// measure returns the natural width of every column and the narrowest each may
// be squeezed to.
//
// The floor is the wider of the column's header and minColumnWidth. A header is
// never truncated — see NewTable — so a column can never be narrower than the
// word above it, which is also the right answer on its own: a header is the one
// cell in the column that is certainly short and certainly load-bearing.
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

// fitWidths narrows widths until the table fits in available columns, and
// returns the natural widths unchanged when it cannot.
//
// The widest column that can still give way is the one that gives, one column at
// a time, because that is where the width is not information: a column is sized
// by its longest cell, so one long path makes it far wider than the rest and
// shrinking every column equally would sacrifice four readable columns to make
// room for one unreadable one. When nothing is left to give the natural widths
// come back, because a table that overflows and wraps still says everything it
// has to say, while a table whose columns have been squeezed past their floors
// says nothing at all.
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

// columnsWidth is the rendered width of a row with these column widths: the
// cells themselves plus the padding between each pair of neighbours.
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

// fitCell shortens a value to width runes, marking anything dropped.
//
// An ellipsis rather than a hard cut, because a column cut mid-word looks like a
// rendering fault while one cut with an ellipsis looks like a column that ran out
// of room, which is what happened.
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

// rowsForHeight returns the rows a terminal of this many lines can show, and
// how many it cannot.
//
// The header and the closing note each take a line of their own, so a complete
// table of N rows needs N+2 lines. A terminal too small for that shows what it
// can; there is no size at which showing nothing is the better answer, so the
// count is floored at one row rather than at zero.
func rowsForHeight(rows [][]string, height int) ([][]string, int) {
	// One line for the header and one for the note. A table that needs no note
	// still loses the line reserved for one, which is a line of caution in
	// exchange for the note never being the reason a row went missing.
	room := height - 2
	if room < 1 {
		room = 1
	}

	if len(rows) <= room {
		return rows, 0
	}

	return rows[:room], len(rows) - room
}

// elidedNote is the line that says how many rows a terminal was too short for.
//
// It is a line of its own rather than a change to the last row shown, so that
// nothing on screen is mistakable for data, and it does not suggest a way to see
// the rest: which answer is right depends on the command, and a hint that is
// wrong for the command you ran sends you somewhere useless.
func elidedNote(count int) string {
	word := "rows"
	if count == 1 {
		word = "row"
	}

	return fmt.Sprintf("... %d more %s not shown\n", count, word)
}

// terminalGrid is the character grid a table is being written into.
type terminalGrid struct {
	width  int
	height int
}

// gridOf returns the grid w is written into, and whether w is a terminal.
//
// Every false answer means the same thing — nobody is looking at a fixed-width
// screen — and the table is then rendered at its natural size. That is what keeps
// output a contract: a pipe, a file and a test's buffer all get every row in
// full, whatever terminal this process happens to be attached to. A table that
// quietly wrapped itself to the width of the window it happened to open in would
// be a different document to every reader of the same command.
//
// The writer is narrowed to an *os.File because that is the only thing a
// descriptor can be got from. Anything else is a buffer or a writer somebody
// supplied, and a buffer has no width to honour.
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

// sanitizeCell makes a value safe to put in a table: control characters go, and
// a tab or newline would otherwise break the column alignment of every row
// after it. A mod description scraped from a source regularly contains both.
//
// Surrounding whitespace is trimmed in every case, not only when something was
// replaced. A value that reaches here padded with spaces shifts its own column,
// and trimming only the values that happened to contain a control character
// would make the misalignment depend on the content of an unrelated field.
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

// maxCellWidth bounds the columns that hold prose. A description is the one
// field with no natural length, and a table that wraps is a table nobody can
// read in a terminal. A source description longer than this is truncated with
// an ellipsis so it is visibly shortened rather than silently cut.
const maxCellWidth = 48

// Clip shortens a value to maxCellWidth runes, marking anything dropped.
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

// Fields is an aligned key/value block, the shape a "show one thing in detail"
// command prints.
//
// It is a block rather than a two-column table because the second column holds
// prose: a table of one very long cell wraps, and a wrapped table cannot be
// read down the left edge.
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

// Setf records a value built with fmt.Sprintf.
func (f *Fields) Setf(key, format string, args ...any) {
	f.Set(key, fmt.Sprintf(format, args...))
}

// SetOrDash records a value, or a dash when it is blank.
func (f *Fields) SetOrDash(key, value string) {
	f.Set(key, orDash(value))
}

// Render writes the block. See Table.Render for why this is not WriteTo.
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

// writeJSON prints a value as indented JSON followed by a newline.
//
// A trailing newline matters: without it, two JSON documents printed in a row
// run together and the combined stream is not parseable. Encoding failures are
// wrapped with the value's type, because "json: unsupported type" on its own
// does not say which command produced it.
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

// writeJSONOrTable is the one place the --json decision is made.
//
// It is a single function on purpose: a command that decides for itself can
// forget to check the flag, and the failure mode is JSON on a terminal or a
// table in a script, which is exactly the kind of thing that gets piped into
// jq and fails three months later.
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

// yesNo renders a boolean the way a person reads it in a table: "yes"/"no"
// rather than "true"/"false", because the column header is usually about
// state, not about a Go value.
func yesNo(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}

// humanSize renders a byte count with a binary unit. Cache sizes span four
// orders of magnitude between an empty cache and a full one, and a raw byte
// count is unreadable at both ends of that range.
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

// orDash renders an absent value as a dash.
//
// A dash rather than an empty cell: an empty column in a table is ambiguous
// between "nothing" and "the value was blank and we trimmed it", and a person
// scanning a cache listing should not have to tell those apart.
func orDash(text string) string {
	if strings.TrimSpace(text) == "" {
		return "-"
	}

	return text
}

// joinNames renders a list for a cell, clipping it when it is long.
func joinNames(names []string) string {
	if len(names) == 0 {
		return "-"
	}

	return Clip(strings.Join(names, ", "))
}

// describeError renders an already-single-lined error for a table cell,
// keeping the manager's error code: the code is what tells a person whether a
// missing credential and an unreachable source are the same problem, and a
// table of results has no other column for it.
//
// It takes the message rather than the error because the callers that have a
// table also have a JSON view of the same result, and the JSON keeps the
// unclipped text.
func describeError(text string) string {
	if strings.TrimSpace(text) == "" {
		return "-"
	}

	return Clip(text)
}

// fatalf builds a user-facing argument error. The argument validation in this
// package goes through here so every "you passed the wrong thing" message
// reaches the terminal as INVALID_ARGUMENT rather than as a bare string that a
// caller cannot branch on.
func fatalf(format string, args ...any) error {
	return appErrors.New(appErrors.ErrCodeInvalidArgument, format, args...)
}
