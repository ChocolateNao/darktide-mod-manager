package commands

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableRendersNothingWithoutRows(t *testing.T) {
	table := NewTable("ID", "VERSION")
	require.True(t, table.Empty(), "a table with no rows reports Empty() = false")

	assert.Equal(t, 0, table.Len(), "a table with no rows reports Len() = %d, want 0", table.Len())

	var out bytes.Buffer
	require.NoError(t, table.Render(&out), "render")
	// A header with nothing under it reads as a bug. Every caller has a better
	// sentence for the empty case, which is why they ask first.
	assert.Equal(t, 0, out.Len(), "an empty table rendered %q, want nothing", out.String())
}

func TestTableAlignsColumnsOnRunes(t *testing.T) {
	table := NewTable("ID", "NAME")
	// "Böhm" is four runes and six bytes. Padding by byte length would push the
	// second column of the previous row out by two spaces, which is exactly the
	// misalignment a non-English mod name causes.
	table.Add("a", "Böhm")
	table.Add("longer-id", "wide")

	var out bytes.Buffer
	require.NoError(t, table.Render(&out), "render")

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(
		t,
		lines,
		3,
		"rendered %d lines, want 3 (header and two rows):\n%s",
		len(lines),
		out.String(),
	)

	header := strings.Index(lines[0], "NAME")
	require.GreaterOrEqual(t, header, 0, "the header lost its own column: %q", lines[0])
	// Every row must place its second column exactly where the header does.
	// Measuring the second column by finding the value itself is the only way
	// to catch this: the padding is a run of spaces, and counting them says
	// nothing about whether the width was measured in runes.
	for i, want := range []string{"Böhm", "wide"} {
		got := strings.Index(lines[i+1], want)
		require.GreaterOrEqual(t, got, 0, "line %d lost its value: %q", i+1, lines[i+1])

		assert.Equal(t, header, got, "line %d puts %q at %d, the header puts its column at %d:\n%s",
			i+1, want, got, header, out.String())
	}
}

func TestTablePadsShortRowsAndDropsExtraCells(t *testing.T) {
	table := NewTable("A", "B", "C")
	// A row that lost a field, and one that gained one, must not shift the
	// columns of the other rows.
	table.Add("only-a")
	table.Add("a", "b", "c", "surplus")

	var out bytes.Buffer
	require.NoError(t, table.Render(&out), "render")

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 3, "rendered %d lines, want 3:\n%s", len(lines), out.String())

	assert.NotContains(
		t,
		out.String(),
		"surplus",
		"a cell past the header count was rendered: %q",
		out.String(),
	)
	// The first column is as wide as "longer-id" in the other test, here as wide
	// as "only-a"; "a" is padded up to it and the second column of both rows
	// lands on the same offset.
	second := strings.Index(lines[2], "b")
	assert.Equal(
		t,
		strings.Index(lines[1], "only-a")+len("only-a")+2,
		second,
		"the padded row put its second column at %d:\n%s",
		second,
		out.String(),
	)

	assert.Equal(
		t,
		strings.Index(lines[0], "B"),
		second,
		"row 2 puts its second column at %d, the header at %d:\n%s",
		second,
		strings.Index(lines[0], "B"),
		out.String(),
	)
	// The missing cells are empty rather than absent, so a row of the wrong
	// length cannot shorten the last column.
	assert.True(
		t,
		strings.HasPrefix(lines[1], "only-a  "),
		"the short row was not padded: %q",
		lines[1],
	)
}

func TestTableAddfFormatsOneCell(t *testing.T) {
	table := NewTable("ID", "SIZE")
	table.Addf("betterui")
	table.Addf("%d files", 3)

	require.Equal(t, 2, table.Len(), "Addf added %d rows, want 2", table.Len())

	var out bytes.Buffer
	require.NoError(t, table.Render(&out), "render")

	assert.Contains(
		t,
		out.String(),
		"3 files",
		"Addf did not format its argument:\n%s",
		out.String(),
	)
}

func TestSanitizeCellKeepsColumnsAligned(t *testing.T) {
	tests := []struct {
		name  string
		cell  string
		want  string
		note  string
		runes string
	}{
		{
			name: "a tab becomes one space",
			cell: "a\tb",
			want: "a b",
			note: "a raw tab would push the next column to an unpredictable offset",
		},
		{
			name: "newlines collapse",
			cell: "line one\nline two",
			want: "line one line two",
			note: "a multi-line description is a single row, and a row with two lines breaks the table",
		},
		{
			name: "carriage returns go too",
			cell: "windows\r\n",
			want: "windows",
			note: "Windows line endings arrive as a pair, and a bare \\r moves the cursor to column zero",
		},
		{
			name: "other control characters are dropped",
			cell: "bell\x07 and null\x00 here",
			want: "bell and null here",
			note: "a control character in a terminal can erase the row it is on, so it goes entirely",
		},
		{
			name: "the delete character goes",
			cell: "a\x7fb",
			want: "ab",
			note: "0x7f is DEL, which some terminals act on",
		},
		{
			name: "surrounding whitespace is trimmed",
			cell: "  padded  ",
			want: "padded",
			note: "leading whitespace shifts a column, and it must be trimmed whether or not the value needed replacing",
		},
		{
			name: "an ordinary value is untouched",
			cell: "Böhm 1.2.0 (vältaa)",
			want: "Böhm 1.2.0 (vältaa)",
			note: "non-ASCII text is the point of measuring in runes, so it must survive",
		},
		{
			name: "an emoji is one rune, not four bytes",
			cell: "🛠 mod",
			want: "🛠 mod",
			note: "a surrogate pair is two runes in Go, so this is two cells wide",
		},
		{
			name: "a value of only control characters empties",
			cell: "\x01\x02",
			want: "",
			note: "a cell of nothing is honest; padding it would invent content",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sanitizeCell(test.cell)
			assert.Equal(
				t,
				test.want,
				got,
				"sanitizeCell(%q) = %q, want %q",
				test.cell,
				got,
				test.want,
			)
		})
	}
}

func TestTableSanitizesWhatItStores(t *testing.T) {
	// The sanitising has to happen in Add rather than in Render: Render measures
	// what it prints, so a value it only cleaned on the way out would be
	// measured at its uncleaned width and the columns would still be wrong.
	table := NewTable("NAME", "STATE")
	table.Add("first\nsecond", "yes")

	var out bytes.Buffer
	require.NoError(t, table.Render(&out), "render")

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(
		t,
		lines,
		2,
		"a newline in a cell produced %d lines, want 2:\n%s",
		len(lines),
		out.String(),
	)

	assert.True(
		t,
		strings.HasPrefix(lines[1], "first second"),
		"the cell was not sanitised before measuring: %q",
		lines[1],
	)
}

// TestRenderToAPipeIgnoresTheTerminalThisProcessHappensToHave runs the table
// through a real terminal-shaped file and still expects the full document.
//
// A pipe, a file and a test's buffer all get every row in full: output is a
// contract, and a table that wrapped itself to whichever window it was run in
// would be a different document to every reader of the same command. The
// terminal branch is reached only when gridOf says the writer really is a
// terminal, and this is the case that keeps the pipe branch honest.
func TestRenderToAPipeIgnoresTheTerminalThisProcessHappensToHave(t *testing.T) {
	table := NewTable("ID", "DESCRIPTION")
	for i := range 40 {
		table.Add(fmt.Sprintf("%d", i), strings.Repeat("wide ", 20))
	}

	whole := renderTable(t, table)
	// os.Stdout is a terminal when the developer ran the tests in a terminal,
	// so writing through a pipe is what isolates the case under test.
	reader, writer, err := os.Pipe()
	require.NoError(t, err, "create a pipe")

	require.NoError(t, table.Render(writer), "render to the pipe")

	_ = writer.Close()

	read, err := io.ReadAll(reader)
	require.NoError(t, err, "read the pipe")

	assert.Equal(t, whole, string(read),
		"a pipe got a different document than a buffer (%d and %d bytes)",
		len(read),
		len(whole),
	)

	assert.NotContains(
		t,
		whole,
		"not shown",
		"a pipe was told that rows exist which it is not being shown",
	)
}

func TestGridOfIsFalseForAnythingButATerminal(t *testing.T) {
	_, terminal := gridOf(&bytes.Buffer{})
	assert.False(t, terminal, "a buffer was taken for a terminal")

	file, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	require.NoError(t, err, "create a file")

	defer file.Close()
	// A regular file has a descriptor but no grid, which is the case a device
	// check alone would get wrong.
	_, terminal = gridOf(file)
	assert.False(t, terminal, "a regular file was taken for a terminal")
}

func TestFitWidthsSqueezesTheWidestColumnFirst(t *testing.T) {
	// One long value makes its column wide; the width that is not information is
	// where the room has to come from, not the four short columns.
	widths := fitWidths([]int{4, 60}, []int{4, 4}, 20)
	assert.LessOrEqual(
		t,
		columnsWidth(widths),
		20,
		"columnsWidth = %d, want at most 20",
		columnsWidth(widths),
	)

	assert.Equal(
		t,
		4,
		widths[0],
		"the narrow column was squeezed to %d, want it left at 4",
		widths[0],
	)

	assert.Equal(
		t,
		20-4-columnPadding,
		widths[1],
		"wide column = %d, want the whole surplus",
		widths[1],
	)
}

func TestFitWidthsStopsAtTheFloors(t *testing.T) {
	// A table too narrow for its own headers is rendered over the edge rather
	// than squeezed into single letters: an overflowing table still wraps into
	// something readable, while a table of one-character columns says nothing.
	widths, floors := (&Table{
		headers: []string{"MOD", "STATE", "SOURCE"},
		rows:    [][]string{{strings.Repeat("x", 30), "ok", "nexus"}},
	}).measure()
	assert.True(t, slices.Equal(fitWidths(widths, floors, 4), widths),
		"fitWidths = %v, want the natural widths %v once every column is at its floor",
		fitWidths(widths, floors, 4),
		widths,
	)

	assert.True(
		t,
		slices.Equal(fitWidths([]int{10, 10}, []int{4, 4}, 1000), []int{10, 10}),
		"a table that already fits came back as %v",
		fitWidths([]int{10, 10}, []int{4, 4}, 1000),
	)

	assert.True(
		t,
		slices.Equal(fitWidths([]int{10}, []int{4}, 0), []int{10}),
		"an unknown width must leave the table alone, got %v",
		fitWidths([]int{10}, []int{4}, 0),
	)
}

func TestMeasureKeepsHeadersWholeAndFloorsThem(t *testing.T) {
	table := NewTable("MOD", "NEEDS KEY")
	table.Add(strings.Repeat("x", 30), "y")

	widths, floors := table.measure()
	assert.Equal(t, 30, widths[0], "widths = %v, want [30 %d]", widths, len("NEEDS KEY"))
	assert.Equal(
		t,
		len("NEEDS KEY"),
		widths[1],
		"widths = %v, want [30 %d]",
		widths,
		len("NEEDS KEY"),
	)
	// A header is never truncated, so a column is never narrower than the word
	// above it — and never narrower than minColumnWidth either, which is what
	// raises the short one.
	assert.Equal(
		t,
		minColumnWidth,
		floors[0],
		"floors = %v, want [%d %d]",
		floors,
		minColumnWidth,
		len("NEEDS KEY"),
	)
	assert.Equal(
		t,
		len("NEEDS KEY"),
		floors[1],
		"floors = %v, want [%d %d]",
		floors,
		minColumnWidth,
		len("NEEDS KEY"),
	)

	short := NewTable("A", "B")
	short.Add("x", "y")

	_, floors = short.measure()
	assert.Equal(t, minColumnWidth, floors[0],
		"floor = %d, want a column narrower than a word to stay at %d",
		floors[0],
		minColumnWidth,
	)
}

func TestFitCellStaysOnRuneBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text  string
		want  string
		width int
	}{
		{"", "", 4},
		{"abc", "abc", 4},
		{"abc", "abc", 3},
		{"abcdef", "ab…", 3},
		{"abcdef", "…", 1},
		{"abcdef", "", 0},
		{"héllo wörld", "héll…", 5},
		{"日本語のテキスト", "日本語…", 4},
	} {
		got := fitCell(tc.text, tc.width)
		assert.Equal(
			t,
			tc.want,
			got,
			"fitCell(%q, %d) = %q, want %q",
			tc.text,
			tc.width,
			got,
			tc.want,
		)
	}
}

// sameRows reports whether two sets of rows are the same rows, since a row is a
// slice and slices cannot be compared with ==.
func sameRows(got, want [][]string) bool {
	return slices.EqualFunc(got, want, slices.Equal)
}

func TestRowsForHeightReservesTheHeaderAndTheNote(t *testing.T) {
	rows := make([][]string, 50)
	for i := range rows {
		rows[i] = []string{fmt.Sprintf("row-%d", i)}
	}
	// A table that fits is handed back whole, so a tall terminal is never the
	// reason a row is missing.
	got, elided := rowsForHeight(
		rows,
		52,
	)
	assert.Equal(
		t,
		0,
		elided,
		"rowsForHeight(50 rows, 52 lines) = %d rows, %d elided, want all 50 and none",
		len(got),
		elided,
	)
	assert.Len(
		t,
		got,
		50,
		"rowsForHeight(50 rows, 52 lines) = %d rows, %d elided, want all 50 and none",
		len(got),
		elided,
	)
	assert.True(
		t,
		sameRows(got, rows),
		"rowsForHeight(50 rows, 52 lines) = %d rows, %d elided, want all 50 and none",
		len(got),
		elided,
	)

	got, elided = rowsForHeight(rows, 12)
	assert.Len(
		t,
		got,
		10,
		"rowsForHeight(50 rows, 12 lines) = %d rows, %d elided, want 10 and 40",
		len(got),
		elided,
	)
	assert.Equal(
		t,
		40,
		elided,
		"rowsForHeight(50 rows, 12 lines) = %d rows, %d elided, want 10 and 40",
		len(got),
		elided,
	)

	assert.True(
		t,
		sameRows(got, rows[:10]),
		"rowsForHeight must keep the first rows and drop the last",
	)
	// There is no size at which showing nothing is the better answer.
	for _, height := range []int{0, 1, 2} {
		got, _ := rowsForHeight(rows, height)
		assert.Len(t, got, 1, "rowsForHeight at height %d = %d rows, want 1", height, len(got))
	}
}

func TestElidedNoteSaysHowManyAndSuggestNothing(t *testing.T) {
	got, want := elidedNote(1), "... 1 more row not shown\n"
	assert.Equal(t, want, got, "elidedNote(1) = %q, want %q", got, want)

	got, want = elidedNote(4000), "... 4000 more rows not shown\n"
	assert.Equal(t, want, got, "elidedNote(4000) = %q, want %q", got, want)
	// Which flag shows the rest differs per command, so a hint here would be
	// wrong for most of the commands that print it.
	assert.False(
		t,
		strings.ContainsAny(elidedNote(3), "-"),
		"the note suggests a way to see the rest, which is per command",
	)
}

// renderTable returns what the table says into a buffer, which is what every
// caller and every recorded transcript reads.
func renderTable(t *testing.T, table *Table) string {
	t.Helper()

	var out bytes.Buffer
	require.NoError(t, table.Render(&out), "render")

	return out.String()
}

func TestClipShortensAndMarks(t *testing.T) {
	long := strings.Repeat("x", 200)

	got := Clip(long)
	assert.Equal(
		t,
		maxCellWidth,
		len([]rune(got)),
		"Clip of a 200-rune value is %d runes, want %d",
		len([]rune(got)),
		maxCellWidth,
	)
	// The ellipsis is the point: a value that is silently cut looks complete.
	assert.True(
		t,
		strings.HasSuffix(got, "…"),
		"a clipped value does not end in an ellipsis: %q",
		got[len(got)-6:],
	)
	// A value of exactly the limit is left alone; clipping it would replace a
	// real last character with a mark for no reason.
	atLimit := strings.Repeat("y", maxCellWidth)
	got = Clip(atLimit)
	assert.Equal(
		t,
		atLimit,
		got,
		"Clip shortened a value of exactly the limit to %d runes",
		len([]rune(got)),
	)
	// A rune count just over the limit.
	over := strings.Repeat("y", maxCellWidth+1)
	got = Clip(over)
	assert.Equal(t, atLimit[:maxCellWidth-1]+"…", got, "Clip of a limit-plus-one value = %q", got)
	// Prose is sanitised on the way in, so a description with a newline cannot
	// smuggle a second line past the clipper.
	mixed := strings.Repeat("a", maxCellWidth) + "\n" + strings.Repeat("b", 10)

	got = Clip(mixed)
	assert.NotContains(t, got, "\n", "Clip left a newline in place: %q", got)
}

func TestFieldsAlignsAndShowsBlanks(t *testing.T) {
	fields := &Fields{}
	fields.Set("id", "betterui")
	fields.Setf("version", "%d.%d.%d", 1, 2, 0)
	fields.SetOrDash("author", "")
	fields.SetOrDash("path", "/games/darktide/mods")

	var out bytes.Buffer
	require.NoError(t, fields.Render(&out), "render")

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 4, "rendered %d lines, want 4:\n%s", len(lines), out.String())
	// The block is scannable because the *values* line up, which is what the
	// padding achieves. The colons deliberately do not: a key longer than
	// "version" has to push its own value right to keep that column straight.
	value := strings.Index(lines[0], "betterui")
	require.GreaterOrEqual(t, value, 0, "the header row lost its value: %q", lines[0])

	for i, want := range []string{"1.2.0", "-", "/games/darktide/mods"} {
		got := strings.Index(lines[i+1], want)
		require.GreaterOrEqual(t, got, 0, "line %d lost its value %q: %q", i+1, want, lines[i+1])

		assert.Equal(
			t,
			value,
			got,
			"line %d puts %q at %d, line 0 puts its value at %d",
			i+1,
			want,
			got,
			value,
		)
	}

	assert.True(
		t,
		strings.HasSuffix(lines[2], "-"),
		"a blank value did not become a dash: %q",
		lines[2],
	)
	// A path is the one value that must not be clipped: a truncated path is not
	// a path.
	long := strings.Repeat("p", 200)
	fields.Set("long", long)
	out.Reset()

	require.NoError(t, fields.Render(&out), "render")

	assert.Contains(
		t,
		out.String(),
		long,
		"Fields clipped a long value; a truncated path is useless",
	)
}

func TestFieldsRendersNothingWhenEmpty(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, (&Fields{}).Render(&out), "render")

	assert.Equal(t, 0, out.Len(), "an empty block rendered %q, want nothing", out.String())
}

func TestWriteJSONEndsWithANewline(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, writeJSON(&out, map[string]int{"files": 2}), "writeJSON")
	// Two documents printed in a row have to be separable, and they are only
	// separable if each one ends its line.
	assert.True(
		t,
		strings.HasSuffix(out.String(), "}\n"),
		"writeJSON did not end with a newline: %q",
		out.String(),
	)

	assert.Contains(
		t,
		out.String(),
		"\n  \"files\": 2",
		"writeJSON did not indent: %q",
		out.String(),
	)
}

func TestWriteJSONNamesTheTypeItCouldNotEncode(t *testing.T) {
	var out bytes.Buffer

	err := writeJSON(&out, map[string]any{"bad": make(chan int)})
	require.Error(t, err, "encoding a channel succeeded, want an error")
	// "json: unsupported type: chan int" does not say which command produced
	// it, and a CLI has dozens of them.
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"the failure is not INVALID_ARGUMENT, so a script cannot branch on it",
	)

	assert.Contains(
		t,
		err.Error(),
		"map[string]interface {}",
		"the error does not name the value's type: %v",
		err,
	)
}

func TestWriteJSONOrTableFollowsTheFlag(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		rows   []string
		asJSON bool
	}{
		{name: "a table when the flag is off", asJSON: false, rows: []string{"a"}, want: "ID\na\n"},
		{
			name:   "JSON when the flag is on",
			asJSON: true,
			rows:   []string{"a"},
			want:   "{\n  \"id\": \"a\"\n}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &session{}
			s.opts.asJSON = test.asJSON

			table := NewTable("ID")
			for _, row := range test.rows {
				table.Add(row)
			}

			var out bytes.Buffer
			require.NoError(t, s.writeJSONOrTable(
				&out,
				map[string]string{"id": "a"},
				table,
				"nothing here",
			), "writeJSONOrTable")

			assert.Equal(t, test.want, out.String(), "wrote %q, want %q", out.String(), test.want)
		})
	}
}

func TestWriteJSONOrTablePrefersTheEmptySentence(t *testing.T) {
	// The empty case has to win over an empty table, or a command whose result
	// is legitimately empty prints a bare header.
	tests := []struct {
		name     string
		nothing  string
		want     string
		asJSON   bool
		withList bool
	}{
		{
			name:     "an empty table prints the sentence",
			asJSON:   false,
			withList: true,
			nothing:  "nothing here",
			want:     "nothing here\n",
		},
		{
			name:    "a nil table prints the sentence too",
			asJSON:  false,
			nothing: "nothing here",
			want:    "nothing here\n",
		},
		{name: "a nil sentence prints nothing at all", asJSON: false, withList: true, want: ""},
		{name: "a nil table and a nil sentence print nothing", asJSON: false, want: ""},
		{
			name:     "JSON wins even when the list is empty",
			asJSON:   true,
			withList: true,
			nothing:  "nothing here",
			want:     "{\n  \"id\": \"a\"\n}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &session{}
			s.opts.asJSON = test.asJSON

			var table *Table
			if test.withList {
				table = NewTable("ID")
			}

			var out bytes.Buffer
			require.NoError(t, s.writeJSONOrTable(
				&out,
				map[string]string{"id": "a"},
				table,
				test.nothing,
			), "writeJSONOrTable")

			assert.Equal(t, test.want, out.String(), "wrote %q, want %q", out.String(), test.want)
		})
	}
}

func TestYesNo(t *testing.T) {
	// "true"/"false" in a table column headed STATE reads like a Go value
	// leaked into the output.
	got := yesNo(true)
	assert.Equal(t, "yes", got, "yesNo(true) = %q, want \"yes\"", got)

	got = yesNo(false)
	assert.Equal(t, "no", got, "yesNo(false) = %q, want \"no\"", got)
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		want  string
		note  string
		bytes int64
	}{
		{bytes: 0, want: "0 B", note: "an empty cache is not an error"},
		{bytes: 1, want: "1 B"},
		{bytes: 1023, want: "1023 B", note: "just under a kibibyte stays in bytes"},
		{bytes: 1024, want: "1.0 KiB"},
		{bytes: 1536, want: "1.5 KiB", note: "the fraction is what makes the unit readable"},
		{bytes: 1024 * 1024, want: "1.0 MiB"},
		{bytes: 1024 * 1024 * 1024, want: "1.0 GiB"},
		{bytes: 1024 * 1024 * 1024 * 1024, want: "1.0 TiB"},
		{
			bytes: -5,
			want:  "-5 B",
			note:  "a negative count is a bug elsewhere; rendering it must not panic",
		},
	}
	for _, test := range tests {
		got := humanSize(test.bytes)
		assert.Equal(t, test.want, got, "humanSize(%d) = %q, want %q", test.bytes, got, test.want)
	}
}

func TestOrDash(t *testing.T) {
	tests := []struct {
		in   string
		want string
		note string
	}{
		{in: "", want: "-", note: "an empty column is ambiguous between nothing and a blank value"},
		{in: "   ", want: "-", note: "a value that is only whitespace is blank"},
		{in: "\t\n", want: "-"},
		{in: "value", want: "value", note: "a real value is passed through, untrimmed"},
		{in: " padded ", want: " padded "},
	}
	for _, test := range tests {
		got := orDash(test.in)
		assert.Equal(t, test.want, got, "orDash(%q) = %q, want %q", test.in, got, test.want)
	}
}

func TestJoinNames(t *testing.T) {
	got := joinNames(nil)
	assert.Equal(t, "-", got, "joinNames(nil) = %q, want a dash", got)

	got = joinNames([]string{})
	assert.Equal(t, "-", got, "joinNames of an empty slice = %q, want a dash", got)

	got = joinNames([]string{"a", "b"})
	assert.Equal(t, "a, b", got, "joinNames = %q, want %q", got, "a, b")
	// A tag list has no natural length, so it has to be clipped like any other
	// prose cell.
	many := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		many = append(many, strings.Repeat("t", 4))
	}

	got = joinNames(many)
	assert.True(t, strings.HasSuffix(got, "…"), "a long list was not clipped: %q", got)
}

func TestDescribeError(t *testing.T) {
	got := describeError("")
	assert.Equal(t, "-", got, "describeError of nothing = %q, want a dash", got)

	got = describeError(
		oneLine(appErrors.New(appErrors.ErrCodeGameNotFound, "no Darktide installation was found")),
	)
	// The code travels with the message: in a table of source results the code
	// is what tells a person whether a missing credential and an unreachable
	// server are the same problem, and there is no other column for it.
	assert.Contains(t, got, "GAME_NOT_FOUND", "the cell dropped the error code: %q", got)

	assert.Contains(t, got, "no Darktide installation", "the cell lost the message: %q", got)
	// A multi-line error must not become a multi-line cell, and a long one must
	// not become a column too wide to read.
	multiline := oneLine(appErrors.New(appErrors.ErrCodeConfigInvalid, "first line\nsecond line"))
	got = describeError(multiline)
	assert.NotContains(t, got, "\n", "describeError left a newline in the cell: %q", got)

	long := oneLine(appErrors.New(appErrors.ErrCodeSearchFailed, "%s", strings.Repeat("x", 300)))
	got = describeError(long)
	assert.True(t, strings.HasSuffix(got, "…"), "a long error was not clipped: %q", got)
}

func TestFatalfIsABranchableArgumentError(t *testing.T) {
	err := fatalf("--max-age-days cannot be negative, got %d", -1)
	assert.True(
		t,
		appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"fatalf produced an error a script cannot branch on: %v",
		err,
	)

	assert.Contains(t, err.Error(), "got -1", "fatalf did not format its arguments: %v", err)
}
