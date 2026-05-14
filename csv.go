package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ColumnType is the inferred type of a CSV column. CSV stores everything as
// text; sqlcsv samples on load and promotes each column to the most specific
// type where every non-empty cell parses cleanly. The chosen type drives how
// comparisons evaluate in WHERE clauses and how literals coerce on writes.
type ColumnType int

const (
	TypeString ColumnType = iota
	TypeInt
	TypeFloat
	TypeBool
	TypeDate
)

func (t ColumnType) String() string {
	switch t {
	case TypeInt:
		return "int"
	case TypeFloat:
		return "float"
	case TypeBool:
		return "bool"
	case TypeDate:
		return "date"
	default:
		return "string"
	}
}

// ParseColumnType maps a --type=... flag value back to a ColumnType.
func ParseColumnType(s string) (ColumnType, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "string", "str", "text":
		return TypeString, nil
	case "int", "integer":
		return TypeInt, nil
	case "float", "number", "num":
		return TypeFloat, nil
	case "bool", "boolean":
		return TypeBool, nil
	case "date", "datetime":
		return TypeDate, nil
	}
	return 0, fmt.Errorf("unknown type %q (expected string, int, float, bool, or date)", s)
}

// ColumnInfo describes one column in the bound CSV. Name is the header text
// (or "colN" when --no-header is set). Type is the inferred or user-overridden
// kind. CSV headers are user-facing already, so there is no separate
// display/internal distinction like spsql has for SharePoint columns.
type ColumnInfo struct {
	Name string
	Type ColumnType
}

// Cell is a typed CSV value. Exactly one of the Str/Int/Float/Bool/Date fields
// is meaningful, picked by the column's ColumnType. An empty CSV cell becomes
// a Cell with Null = true regardless of the column type.
type Cell struct {
	Null  bool
	Str   string
	Int   int64
	Float float64
	Bool  bool
	Date  time.Time
}

// Row is one record of the CSV in the same column order as Table.Columns.
type Row []Cell

// Table is the in-memory representation of the bound CSV. Columns preserves
// header order; Schema maps name to type info; Rows holds the typed records.
// Dialect fields are retained so Save can write the file back in the same
// format it came from.
type Table struct {
	Path      string
	Columns   []string
	Schema    map[string]ColumnInfo
	Rows      []Row
	Delim     rune
	HasHeader bool
}

// LoadOptions controls CSV parsing and type inference. Zero values mean
// "use defaults": comma delimiter, header row present, type inference
// enabled.
type LoadOptions struct {
	Delim     rune
	NoHeader  bool
	TypeHints map[string]ColumnType
	SampleN   int
}

// LoadCSV reads the file at path and returns a fully populated Table. Type
// inference runs over the first SampleN rows (default 1024); a column gets
// the most specific type where every sampled non-empty cell parses.
func LoadCSV(path string, opts LoadOptions) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	delim := opts.Delim
	if delim == 0 {
		delim = ','
	}
	r := csv.NewReader(f)
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}

	var columns []string
	var dataStart int
	if opts.NoHeader {
		ncol := len(records[0])
		columns = make([]string, ncol)
		for i := range columns {
			columns[i] = fmt.Sprintf("col%d", i+1)
		}
		dataStart = 0
	} else {
		columns = append(columns, records[0]...)
		dataStart = 1
	}
	ncol := len(columns)

	rawRows := make([][]string, 0, len(records)-dataStart)
	for _, rec := range records[dataStart:] {
		row := make([]string, ncol)
		for i := 0; i < ncol && i < len(rec); i++ {
			row[i] = rec[i]
		}
		rawRows = append(rawRows, row)
	}

	sampleN := opts.SampleN
	if sampleN <= 0 {
		sampleN = 1024
	}
	if sampleN > len(rawRows) {
		sampleN = len(rawRows)
	}

	schema := make(map[string]ColumnInfo, ncol)
	for i, name := range columns {
		var t ColumnType
		if hint, ok := opts.TypeHints[name]; ok {
			t = hint
		} else {
			t = inferColumn(rawRows[:sampleN], i)
		}
		schema[name] = ColumnInfo{Name: name, Type: t}
	}

	rows := make([]Row, len(rawRows))
	for ri, rec := range rawRows {
		row := make(Row, ncol)
		for ci, raw := range rec {
			row[ci] = parseCell(raw, schema[columns[ci]].Type)
		}
		rows[ri] = row
	}

	return &Table{
		Path:      path,
		Columns:   columns,
		Schema:    schema,
		Rows:      rows,
		Delim:     delim,
		HasHeader: !opts.NoHeader,
	}, nil
}

// SaveCSV writes the Table back to its bound path (or to dst if non-empty).
// Cells emit in their canonical string form; NULL becomes an empty field.
func SaveCSV(t *Table, dst string) error {
	if dst == "" {
		dst = t.Path
	}
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	w.Comma = t.Delim
	defer w.Flush()

	if t.HasHeader {
		if err := w.Write(t.Columns); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
	}
	rec := make([]string, len(t.Columns))
	for _, row := range t.Rows {
		for i, name := range t.Columns {
			rec[i] = formatCell(row[i], t.Schema[name].Type)
		}
		if err := w.Write(rec); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	w.Flush()
	return w.Error()
}

// inferColumn picks the most specific ColumnType where every non-empty cell
// in the column index parses. Order of specificity: int, float, date, bool,
// then string. A column of all empty cells defaults to string.
func inferColumn(sample [][]string, idx int) ColumnType {
	allInt, allFloat, allBool, allDate := true, true, true, true
	seenNonEmpty := false
	for _, row := range sample {
		if idx >= len(row) {
			continue
		}
		cell := strings.TrimSpace(row[idx])
		if cell == "" {
			continue
		}
		seenNonEmpty = true
		if allInt && !looksLikeInt(cell) {
			allInt = false
		}
		if allFloat && !looksLikeFloat(cell) {
			allFloat = false
		}
		if allBool && !looksLikeBool(cell) {
			allBool = false
		}
		if allDate && !looksLikeDate(cell) {
			allDate = false
		}
		if !allInt && !allFloat && !allBool && !allDate {
			return TypeString
		}
	}
	if !seenNonEmpty {
		return TypeString
	}
	switch {
	case allInt:
		return TypeInt
	case allFloat:
		return TypeFloat
	case allDate:
		return TypeDate
	case allBool:
		return TypeBool
	default:
		return TypeString
	}
}

func looksLikeInt(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

func looksLikeFloat(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func looksLikeBool(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no":
		return true
	}
	return false
}

func looksLikeDate(s string) bool {
	_, err := parseDateString(s)
	return err == nil
}

// parseDateString tries the ISO 8601 forms sqlcsv supports. Anything outside
// these formats falls through to string, intentionally; predictable beats
// aggressive guessing.
func parseDateString(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not an ISO 8601 date: %q", s)
}

// parseCell converts a raw CSV cell to a typed Cell using the column's
// inferred type. An unparseable cell becomes NULL rather than failing the
// load; pin the column to string via --type if you prefer the raw text.
func parseCell(raw string, t ColumnType) Cell {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Cell{Null: true}
	}
	switch t {
	case TypeInt:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return Cell{Int: n}
		}
		return Cell{Null: true}
	case TypeFloat:
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return Cell{Float: n}
		}
		return Cell{Null: true}
	case TypeBool:
		switch strings.ToLower(s) {
		case "true", "yes":
			return Cell{Bool: true}
		case "false", "no":
			return Cell{Bool: false}
		}
		return Cell{Null: true}
	case TypeDate:
		if dt, err := parseDateString(s); err == nil {
			return Cell{Date: dt}
		}
		return Cell{Null: true}
	default:
		return Cell{Str: raw}
	}
}

// formatCell renders a typed Cell back to a CSV field string. NULL becomes
// the empty string. Dates that have no time component render as date-only;
// the rest use RFC 3339. Floats use the shortest round-trippable form.
func formatCell(v Cell, t ColumnType) string {
	if v.Null {
		return ""
	}
	switch t {
	case TypeInt:
		return strconv.FormatInt(v.Int, 10)
	case TypeFloat:
		return strconv.FormatFloat(v.Float, 'g', -1, 64)
	case TypeBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case TypeDate:
		if v.Date.Hour() == 0 && v.Date.Minute() == 0 && v.Date.Second() == 0 {
			return v.Date.Format("2006-01-02")
		}
		return v.Date.Format(time.RFC3339)
	default:
		return v.Str
	}
}

// CoerceLiteral converts a parsed SQL literal (parser.Value) to a Cell
// compatible with the given ColumnType. Used by INSERT and UPDATE to coerce
// user-supplied literals at write time. Returns an error if the literal
// cannot meaningfully coerce (for example 'abc' into an int column).
func CoerceLiteral(lit Value, t ColumnType) (Cell, error) {
	if lit.Kind == ValNull {
		return Cell{Null: true}, nil
	}
	switch t {
	case TypeString:
		switch lit.Kind {
		case ValString:
			return Cell{Str: lit.Str}, nil
		case ValNumber:
			return Cell{Str: lit.Num}, nil
		case ValBool:
			if lit.Bool {
				return Cell{Str: "true"}, nil
			}
			return Cell{Str: "false"}, nil
		}
	case TypeInt:
		switch lit.Kind {
		case ValNumber:
			if n, err := strconv.ParseInt(lit.Num, 10, 64); err == nil {
				return Cell{Int: n}, nil
			}
		case ValString:
			if n, err := strconv.ParseInt(lit.Str, 10, 64); err == nil {
				return Cell{Int: n}, nil
			}
		}
		return Cell{}, fmt.Errorf("cannot coerce %s to int", renderLiteral(lit))
	case TypeFloat:
		switch lit.Kind {
		case ValNumber:
			if n, err := strconv.ParseFloat(lit.Num, 64); err == nil {
				return Cell{Float: n}, nil
			}
		case ValString:
			if n, err := strconv.ParseFloat(lit.Str, 64); err == nil {
				return Cell{Float: n}, nil
			}
		}
		return Cell{}, fmt.Errorf("cannot coerce %s to float", renderLiteral(lit))
	case TypeBool:
		switch lit.Kind {
		case ValBool:
			return Cell{Bool: lit.Bool}, nil
		case ValString:
			switch strings.ToLower(lit.Str) {
			case "true", "yes", "1":
				return Cell{Bool: true}, nil
			case "false", "no", "0":
				return Cell{Bool: false}, nil
			}
		case ValNumber:
			switch lit.Num {
			case "1":
				return Cell{Bool: true}, nil
			case "0":
				return Cell{Bool: false}, nil
			}
		}
		return Cell{}, fmt.Errorf("cannot coerce %s to bool", renderLiteral(lit))
	case TypeDate:
		if lit.Kind == ValString {
			if dt, err := parseDateString(lit.Str); err == nil {
				return Cell{Date: dt}, nil
			}
		}
		return Cell{}, fmt.Errorf("cannot coerce %s to date", renderLiteral(lit))
	}
	return Cell{}, fmt.Errorf("unknown column type for coercion")
}

func renderLiteral(lit Value) string {
	switch lit.Kind {
	case ValString:
		return "'" + strings.ReplaceAll(lit.Str, "'", "''") + "'"
	case ValNumber:
		return lit.Num
	case ValBool:
		if lit.Bool {
			return "TRUE"
		}
		return "FALSE"
	case ValNull:
		return "NULL"
	}
	return "?"
}

// Compare returns -1, 0, or +1 for the natural ordering of two Cells under
// the given column type. NULL is less than any non-null value. Used by
// predicate evaluation in filter.go.
func Compare(a, b Cell, t ColumnType) int {
	if a.Null && b.Null {
		return 0
	}
	if a.Null {
		return -1
	}
	if b.Null {
		return 1
	}
	switch t {
	case TypeInt:
		switch {
		case a.Int < b.Int:
			return -1
		case a.Int > b.Int:
			return 1
		}
		return 0
	case TypeFloat:
		switch {
		case a.Float < b.Float:
			return -1
		case a.Float > b.Float:
			return 1
		}
		return 0
	case TypeBool:
		ai, bi := 0, 0
		if a.Bool {
			ai = 1
		}
		if b.Bool {
			bi = 1
		}
		return ai - bi
	case TypeDate:
		switch {
		case a.Date.Before(b.Date):
			return -1
		case a.Date.After(b.Date):
			return 1
		}
		return 0
	default:
		return strings.Compare(a.Str, b.Str)
	}
}

// Render returns the human-readable form of a Cell for table/TSV output.
// NULL renders as the empty string.
func (v Cell) Render(t ColumnType) string {
	if v.Null {
		return ""
	}
	return formatCell(v, t)
}

// AsAny returns the Cell as an untyped Go value suitable for JSON encoding.
// NULL becomes nil; everything else returns the underlying typed value.
func (v Cell) AsAny(t ColumnType) any {
	if v.Null {
		return nil
	}
	switch t {
	case TypeInt:
		return v.Int
	case TypeFloat:
		return v.Float
	case TypeBool:
		return v.Bool
	case TypeDate:
		return formatCell(v, t)
	default:
		return v.Str
	}
}
