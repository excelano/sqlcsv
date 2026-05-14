package main

import (
	"fmt"
	"io"
	"strings"
)

// Executor binds a parsed statement to the loaded CSV Table and runs it.
// One Executor per session.
//
// Confirm is the interactive "Apply? [y/N]" callback used by the REPL. When
// non-nil, write executors will call it after the dry-run preview to decide
// whether to commit (unless commit is already true via the trailing '!').
// --exec mode leaves Confirm nil so writes either dry-run or commit explicitly
// based on --commit.
//
// OutputPath, when non-empty, redirects committed writes to a different file
// than the bound CSV. Empty means "write back to Table.Path".
type Executor struct {
	Table              *Table
	Format             string
	ConfirmDestructive bool
	Confirm            func() bool
	OutputPath         string
	Out                io.Writer
}

// Execute dispatches to the per-statement handler. The commit flag distinguishes
// dry-run (commit=false: preview only) from a real write (commit=true: preview
// + apply). It is ignored for SELECT.
func (e *Executor) Execute(stmt Stmt, commit bool) error {
	switch s := stmt.(type) {
	case *SelectStmt:
		return e.executeSelect(s)
	case *UpdateStmt:
		return e.executeUpdate(s, commit)
	case *DeleteStmt:
		return e.executeDelete(s, commit)
	case *InsertStmt:
		return e.executeInsert(s, commit)
	}
	return fmt.Errorf("internal: unknown statement type %T", stmt)
}

func (e *Executor) executeSelect(sel *SelectStmt) error {
	cols, err := e.resolveProjection(sel)
	if err != nil {
		return err
	}
	if err := ValidatePredicate(sel.Where, e.Table.Schema); err != nil {
		return err
	}
	ctx := NewEvalContext(e.Table)
	rows := make([]map[string]any, 0, len(e.Table.Rows))
	for _, row := range e.Table.Rows {
		ok, err := Matches(sel.Where, row, ctx)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		m := make(map[string]any, len(cols))
		for _, c := range cols {
			idx := ctx.ColIdx[c]
			m[c] = row[idx].AsAny(e.Table.Schema[c].Type)
		}
		rows = append(rows, m)
	}
	return Render(e.Out, Result{Columns: cols, Rows: rows}, e.Format)
}

// resolveProjection decides which columns to return. SELECT * uses every
// column in header order. An explicit list is validated against the schema.
func (e *Executor) resolveProjection(sel *SelectStmt) ([]string, error) {
	if sel.Star {
		return append([]string(nil), e.Table.Columns...), nil
	}
	for _, c := range sel.Columns {
		if _, ok := e.Table.Schema[c]; !ok {
			return nil, fmt.Errorf("unknown column %q (not in CSV header)", c)
		}
	}
	return sel.Columns, nil
}

func (e *Executor) executeUpdate(upd *UpdateStmt, commit bool) error {
	if err := ValidatePredicate(upd.Where, e.Table.Schema); err != nil {
		return err
	}
	cells, err := e.buildAssignmentCells(upd.Assignments)
	if err != nil {
		return err
	}

	matches, err := e.findMatches(upd.Where)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.Out, "Would update %d row%s in %s:\n", len(matches), plural(len(matches)), e.Table.Path)
	for _, a := range upd.Assignments {
		fmt.Fprintf(e.Out, "  SET %s = %s\n", a.Column, renderLiteral(a.Value))
	}
	e.printSample(matches)

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}
	if len(matches) == 0 {
		return e.flush()
	}
	for _, idx := range matches {
		for _, a := range upd.Assignments {
			ci := e.colIndex(a.Column)
			e.Table.Rows[idx][ci] = cells[a.Column]
		}
	}
	if err := e.flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "Updated %d of %d row%s. Wrote %s.\n", len(matches), len(matches), plural(len(matches)), e.targetPath())
	return nil
}

func (e *Executor) executeDelete(del *DeleteStmt, commit bool) error {
	if del.Where == nil && commit && !e.ConfirmDestructive && e.Confirm == nil {
		return fmt.Errorf("bare DELETE (no WHERE) requires --confirm-destructive")
	}
	if err := ValidatePredicate(del.Where, e.Table.Schema); err != nil {
		return err
	}

	matches, err := e.findMatches(del.Where)
	if err != nil {
		return err
	}

	if del.Where == nil {
		fmt.Fprintf(e.Out, "Would delete ALL %d row%s from %s:\n", len(matches), plural(len(matches)), e.Table.Path)
	} else {
		fmt.Fprintf(e.Out, "Would delete %d row%s from %s:\n", len(matches), plural(len(matches)), e.Table.Path)
	}
	e.printSample(matches)

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}
	if len(matches) == 0 {
		return e.flush()
	}
	e.Table.Rows = removeIndices(e.Table.Rows, matches)
	if err := e.flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "Deleted %d row%s. Wrote %s.\n", len(matches), plural(len(matches)), e.targetPath())
	return nil
}

func (e *Executor) executeInsert(ins *InsertStmt, commit bool) error {
	if len(ins.Columns) != len(ins.Values) {
		return fmt.Errorf("INSERT has %d column%s but %d value%s",
			len(ins.Columns), plural(len(ins.Columns)),
			len(ins.Values), plural(len(ins.Values)))
	}
	seen := map[string]bool{}
	for _, c := range ins.Columns {
		if seen[c] {
			return fmt.Errorf("INSERT column %q appears twice", c)
		}
		seen[c] = true
	}
	assigns := make([]Assignment, len(ins.Columns))
	for i, c := range ins.Columns {
		assigns[i] = Assignment{Column: c, Value: ins.Values[i]}
	}
	cells, err := e.buildAssignmentCells(assigns)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.Out, "Would insert row into %s:\n", e.Table.Path)
	for _, c := range ins.Columns {
		fmt.Fprintf(e.Out, "  %s = %s\n", c, renderLiteral(findValue(ins, c)))
	}

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}

	newRow := make(Row, len(e.Table.Columns))
	for i, name := range e.Table.Columns {
		if c, ok := cells[name]; ok {
			newRow[i] = c
		} else {
			newRow[i] = Cell{Null: true}
		}
	}
	e.Table.Rows = append(e.Table.Rows, newRow)
	if err := e.flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "Inserted row. Wrote %s.\n", e.targetPath())
	return nil
}

// decideCommit resolves a write's commit/abort decision after the preview has
// been shown.
//   - commit=true (trailing '!' in REPL, --commit in --exec): proceed silently.
//   - REPL (Confirm != nil): ask the user; on "y", proceed; otherwise "(aborted)".
//   - --exec without --commit (Confirm == nil): never commit; print the
//     "(dry run; pass --commit to apply)" hint.
func (e *Executor) decideCommit(commit bool) (bool, string) {
	if commit {
		return true, ""
	}
	if e.Confirm == nil {
		return false, "(dry run; pass --commit to apply)"
	}
	if e.Confirm() {
		return true, ""
	}
	return false, "(aborted)"
}

// findMatches returns the row indices that satisfy the predicate. A nil
// predicate matches every row.
func (e *Executor) findMatches(where Predicate) ([]int, error) {
	ctx := NewEvalContext(e.Table)
	out := make([]int, 0)
	for i, row := range e.Table.Rows {
		ok, err := Matches(where, row, ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, i)
		}
	}
	return out, nil
}

func (e *Executor) buildAssignmentCells(assigns []Assignment) (map[string]Cell, error) {
	cells := make(map[string]Cell, len(assigns))
	for _, a := range assigns {
		info, ok := e.Table.Schema[a.Column]
		if !ok {
			return nil, fmt.Errorf("unknown column %q", a.Column)
		}
		c, err := CoerceLiteral(a.Value, info.Type)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", a.Column, err)
		}
		cells[a.Column] = c
	}
	return cells, nil
}

func (e *Executor) colIndex(name string) int {
	for i, n := range e.Table.Columns {
		if n == name {
			return i
		}
	}
	return -1
}

// printSample emits a small preview table: row# + a primary column (Title when
// present, else the first column). At most previewSampleMax rows; a trailing
// "... N more" line counts what was elided.
func (e *Executor) printSample(rowIndices []int) {
	if len(rowIndices) == 0 {
		return
	}
	previewCols := e.previewColumns()
	header := append([]string{"row"}, previewCols...)
	sample := rowIndices
	if len(sample) > previewSampleMax {
		sample = sample[:previewSampleMax]
	}
	rows := make([]map[string]any, len(sample))
	for i, ri := range sample {
		m := map[string]any{"row": int64(ri + 1)}
		for _, c := range previewCols {
			ci := e.colIndex(c)
			m[c] = e.Table.Rows[ri][ci].AsAny(e.Table.Schema[c].Type)
		}
		rows[i] = m
	}
	fmt.Fprintln(e.Out, "Sample:")
	_ = writeTableBody(e.Out, header, rows)
	if len(rowIndices) > previewSampleMax {
		fmt.Fprintf(e.Out, "  ... %d more\n", len(rowIndices)-previewSampleMax)
	}
}

const previewSampleMax = 5

// previewColumns picks the column to show alongside the row number in write
// previews. Prefers Title (case-insensitive match), then Name, then the first
// column. The goal is to surface enough identifying detail for a human to
// recognize a row.
func (e *Executor) previewColumns() []string {
	for _, candidate := range []string{"Title", "Name", "name", "title"} {
		if _, ok := e.Table.Schema[candidate]; ok {
			return []string{candidate}
		}
	}
	for _, c := range e.Table.Columns {
		if strings.EqualFold(c, "title") || strings.EqualFold(c, "name") {
			return []string{c}
		}
	}
	if len(e.Table.Columns) > 0 {
		return []string{e.Table.Columns[0]}
	}
	return nil
}

// flush persists the in-memory Table to disk, either at OutputPath or at the
// originally bound path.
func (e *Executor) flush() error {
	return SaveCSV(e.Table, e.OutputPath)
}

func (e *Executor) targetPath() string {
	if e.OutputPath != "" {
		return e.OutputPath
	}
	return e.Table.Path
}

// removeIndices returns rows with the listed indices removed. indices must
// be sorted ascending (findMatches produces them that way).
func removeIndices(rows []Row, indices []int) []Row {
	out := make([]Row, 0, len(rows)-len(indices))
	j := 0
	for i, r := range rows {
		if j < len(indices) && indices[j] == i {
			j++
			continue
		}
		out = append(out, r)
	}
	return out
}

// findValue picks the literal Value for column c out of an InsertStmt's
// parallel Columns/Values lists. Used only by the preview path; the
// pre-validation has already checked length parity.
func findValue(ins *InsertStmt, c string) Value {
	for i, name := range ins.Columns {
		if name == c {
			return ins.Values[i]
		}
	}
	return Value{Kind: ValNull}
}
