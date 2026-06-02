package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newExec produces an Executor bound to a fresh fixture Table, with output
// captured to an in-memory buffer for assertion. Each test starts from a clean
// fixture so mutations don't leak between cases.
func newExec(t *testing.T) (*Executor, *bytes.Buffer, string) {
	t.Helper()
	tbl := fixtureTable()
	path := filepath.Join(t.TempDir(), "fixture.csv")
	tbl.Path = path
	// Persist so write tests have a real file to rewrite over.
	if err := SaveCSV(tbl, ""); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	return &Executor{Table: tbl, Format: "tsv", Out: buf}, buf, path
}

func TestExecSelectStar(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT *")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("SELECT * lines: %d, want 5 (header + 4 rows): %q", len(lines), out.String())
	}
	if lines[0] != "ID\tTitle\tStatus\tPriority\tArchived\tModified" {
		t.Errorf("header: %q", lines[0])
	}
}

func TestExecSelectProjection(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT Title, Priority WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// header + 3 Open rows
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %q", len(lines), out.String())
	}
	if lines[0] != "Title\tPriority" {
		t.Errorf("header: %q", lines[0])
	}
}

func TestExecSelectDistinct(t *testing.T) {
	// Fixture statuses: Open, Done, Open, Open → distinct {Open, Done}.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT DISTINCT Status")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("DISTINCT Status: %d lines, want 3 (header + Open + Done): %q", len(lines), out.String())
	}
	if lines[0] != "Status" {
		t.Errorf("header: %q", lines[0])
	}
	// First-seen order: Open, then Done.
	if lines[1] != "Open" || lines[2] != "Done" {
		t.Errorf("expected first-seen order Open, Done; got %q, %q", lines[1], lines[2])
	}
}

func TestExecSelectDistinctStarNoCollapse(t *testing.T) {
	// All four fixture rows differ across the full row, so DISTINCT * is a no-op.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT DISTINCT *")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("DISTINCT *: %d lines, want 5 (header + 4 rows): %q", len(lines), out.String())
	}
}

func TestExecSelectDistinctWithWhere(t *testing.T) {
	// WHERE filters first, then DISTINCT. Priority > 2 matches rows 1 (Open,3),
	// 3 (Open,5), 4 (Open,NULL). DISTINCT Status collapses to just Open.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT DISTINCT Status WHERE Priority > 2")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("DISTINCT + WHERE: %d lines, want 2: %q", len(lines), out.String())
	}
	if lines[1] != "Open" {
		t.Errorf("got %q, want Open", lines[1])
	}
}

func TestExecSelectDistinctMultiColumn(t *testing.T) {
	// Status × Archived pairs: (Open,false), (Done,true), (Open,false), (Open,false).
	// Distinct: (Open,false), (Done,true) → 2 rows.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT DISTINCT Status, Archived")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("DISTINCT 2-col: %d lines, want 3 (header + 2): %q", len(lines), out.String())
	}
}

func TestExecSelectDistinctNullsCollapse(t *testing.T) {
	// Two rows with NULL Priority should collapse to one under DISTINCT —
	// matches SQL's NULL-equal-to-NULL semantics for DISTINCT.
	tbl := &Table{
		Path:    "x.csv",
		Columns: []string{"Priority"},
		Schema:  map[string]ColumnInfo{"Priority": {Name: "Priority", Type: TypeInt}},
		Rows: []Row{
			{Cell{Null: true}},
			{Cell{Int: 1}},
			{Cell{Null: true}},
		},
		Delim:     ',',
		HasHeader: true,
	}
	buf := &bytes.Buffer{}
	e := &Executor{Table: tbl, Format: "tsv", Out: buf}
	stmt, err := Parse("SELECT DISTINCT Priority")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (header + NULL + 1): %q", len(lines), buf.String())
	}
}

func TestExecSelectOrderByAsc(t *testing.T) {
	// Fixture priorities (in row order): 3, 1, 5, NULL.
	// ASC, NULLs LAST: 1, 3, 5, NULL → IDs 2, 1, 3, 4.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID ORDER BY Priority ASC")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "2", "1", "3", "4"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out.String())
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d: got %q, want %q", i, lines[i], w)
		}
	}
}

func TestExecSelectOrderByDesc(t *testing.T) {
	// DESC, NULLs FIRST: NULL, 5, 3, 1 → IDs 4, 3, 1, 2.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID ORDER BY Priority DESC")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "4", "3", "1", "2"}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d: got %q, want %q", i, lines[i], w)
		}
	}
}

func TestExecSelectOrderByMultiKey(t *testing.T) {
	// ORDER BY Status ASC, Priority DESC.
	// Statuses: Open, Done, Open, Open. Priorities: 3, 1, 5, NULL.
	// ASC by Status: Done first (ID 2), then three Opens.
	// Within Opens, DESC by Priority with NULLs FIRST in DESC:
	//   NULL (ID 4), 5 (ID 3), 3 (ID 1).
	// Expected ID order: 2, 4, 3, 1.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID ORDER BY Status ASC, Priority DESC")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "2", "4", "3", "1"}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d: got %q, want %q", i, lines[i], w)
		}
	}
}

func TestExecSelectOrderByUnknownColumn(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("SELECT * ORDER BY Nope")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Nope") || !strings.Contains(err.Error(), "ORDER BY") {
		t.Fatalf("error should name column and clause: %v", err)
	}
}

func TestExecSelectLimit(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID ORDER BY ID LIMIT 2")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "1", "2"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out.String())
	}
}

func TestExecSelectOffset(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID ORDER BY ID OFFSET 2")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "3", "4"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out.String())
	}
}

func TestExecSelectLimitOffset(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID ORDER BY ID LIMIT 1 OFFSET 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "2"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out.String())
	}
}

func TestExecSelectOffsetPastEnd(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID OFFSET 100")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Header only.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || lines[0] != "ID" {
		t.Fatalf("expected header only, got %q", out.String())
	}
}

func TestExecSelectLimitZero(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID LIMIT 0")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || lines[0] != "ID" {
		t.Fatalf("expected header only, got %q", out.String())
	}
}

func TestExecSelectDistinctOrderLimit(t *testing.T) {
	// Combine all the new clauses with DISTINCT.
	// Statuses: Open, Done, Open, Open. DISTINCT → {Open, Done}.
	// ORDER BY Status ASC → Done, Open.
	// LIMIT 1 → Done.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT DISTINCT Status ORDER BY Status ASC LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || lines[1] != "Done" {
		t.Fatalf("want header + Done, got %q", out.String())
	}
}

func TestExecSelectUnknownColumn(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("SELECT Nope")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected error for unknown column")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("error should name column: %v", err)
	}
}

func TestExecUpdateDryRun(t *testing.T) {
	e, out, path := newExec(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := Parse("UPDATE SET Status = 'Done' WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "Would update 3 rows") {
		t.Errorf("expected preview header in output: %q", out.String())
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("expected dry-run hint: %q", out.String())
	}
	// File should be unchanged.
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Error("dry-run modified the file")
	}
	// In-memory rows should also be unchanged.
	if e.Table.Rows[0][2].Str != "Open" {
		t.Errorf("dry-run mutated in-memory row 0 Status: %q", e.Table.Rows[0][2].Str)
	}
}

func TestExecUpdateCommit(t *testing.T) {
	e, _, path := newExec(t)
	stmt, err := Parse("UPDATE SET Status = 'Done' WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// In-memory: all rows now have Status = "Done".
	for i, row := range e.Table.Rows {
		if row[2].Str != "Done" {
			t.Errorf("row %d: Status %q, want Done", i, row[2].Str)
		}
	}
	// On-disk: reload and verify.
	reloaded, err := LoadCSV(path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range reloaded.Rows {
		if row[2].Str != "Done" {
			t.Errorf("reloaded row %d: Status %q, want Done", i, row[2].Str)
		}
	}
}

func TestExecDeleteWithWhere(t *testing.T) {
	e, _, path := newExec(t)
	stmt, err := Parse("DELETE WHERE Status = 'Done'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(e.Table.Rows) != 3 {
		t.Errorf("rows after delete: %d, want 3", len(e.Table.Rows))
	}
	for _, row := range e.Table.Rows {
		if row[2].Str == "Done" {
			t.Errorf("Done row survived delete")
		}
	}
	reloaded, _ := LoadCSV(path, LoadOptions{})
	if len(reloaded.Rows) != 3 {
		t.Errorf("on-disk rows: %d, want 3", len(reloaded.Rows))
	}
}

func TestExecBareDeleteRequiresConfirmDestructive(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("DELETE")
	if err != nil {
		t.Fatal(err)
	}
	// commit=true, no Confirm, no ConfirmDestructive → should reject.
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("expected error for bare DELETE")
	}
	if !strings.Contains(err.Error(), "confirm-destructive") {
		t.Fatalf("error should mention flag: %v", err)
	}
}

func TestExecBareDeleteWithConfirmDestructive(t *testing.T) {
	e, _, path := newExec(t)
	e.ConfirmDestructive = true
	stmt, err := Parse("DELETE")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(e.Table.Rows) != 0 {
		t.Errorf("rows after bare delete: %d, want 0", len(e.Table.Rows))
	}
	reloaded, _ := LoadCSV(path, LoadOptions{})
	if len(reloaded.Rows) != 0 {
		t.Errorf("on-disk rows: %d, want 0", len(reloaded.Rows))
	}
}

func TestExecInsert(t *testing.T) {
	e, _, path := newExec(t)
	stmt, err := Parse("INSERT (ID, Title, Status, Priority, Archived, Modified) VALUES (99, 'New Row', 'Open', 4, FALSE, '2024-05-14')")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(e.Table.Rows) != 5 {
		t.Fatalf("rows after insert: %d, want 5", len(e.Table.Rows))
	}
	last := e.Table.Rows[4]
	if last[0].Int != 99 || last[1].Str != "New Row" || last[2].Str != "Open" {
		t.Errorf("inserted row wrong: %+v", last)
	}
	reloaded, _ := LoadCSV(path, LoadOptions{})
	if len(reloaded.Rows) != 5 {
		t.Errorf("on-disk rows: %d, want 5", len(reloaded.Rows))
	}
}

func TestExecInsertPartialColumnsFillsNull(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("INSERT (ID, Title) VALUES (99, 'Partial')")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	last := e.Table.Rows[4]
	if last[0].Int != 99 || last[1].Str != "Partial" {
		t.Errorf("ID/Title wrong: %+v", last)
	}
	for i := 2; i < len(last); i++ {
		if !last[i].Null {
			t.Errorf("col %d should be NULL (unspecified), got %+v", i, last[i])
		}
	}
}

func TestExecInsertColumnValueMismatch(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("INSERT (ID, Title) VALUES (99)")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("expected error for column/value count mismatch")
	}
}

func TestExecCoercionFailureReported(t *testing.T) {
	e, _, _ := newExec(t)
	// Priority is int; 'abc' as a string cannot coerce.
	stmt, err := Parse("UPDATE SET Priority = 'abc'")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("expected coercion error")
	}
	if !strings.Contains(err.Error(), "Priority") {
		t.Fatalf("error should name column: %v", err)
	}
}

func TestExecOutputPath(t *testing.T) {
	e, _, originalPath := newExec(t)
	dst := filepath.Join(t.TempDir(), "out.csv")
	e.OutputPath = dst

	originalBytes, _ := os.ReadFile(originalPath)

	stmt, err := Parse("UPDATE SET Priority = 9 WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	afterOriginal, _ := os.ReadFile(originalPath)
	if !bytes.Equal(originalBytes, afterOriginal) {
		t.Error("OutputPath set but original file was modified")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	reloaded, _ := LoadCSV(dst, LoadOptions{})
	if reloaded.Rows[0][3].Int != 9 {
		t.Errorf("output row 0 Priority: %d, want 9", reloaded.Rows[0][3].Int)
	}
}

func TestExecDecideCommit(t *testing.T) {
	// commit=true bypasses everything.
	e := &Executor{}
	ok, msg := e.decideCommit(true)
	if !ok || msg != "" {
		t.Errorf("commit=true: got (%v, %q), want (true, \"\")", ok, msg)
	}

	// Exec mode (no Confirm): never commits, prints dry-run hint.
	e = &Executor{}
	ok, msg = e.decideCommit(false)
	if ok || !strings.Contains(msg, "dry run") {
		t.Errorf("exec dry-run: got (%v, %q)", ok, msg)
	}

	// REPL mode with user saying y → proceed.
	e = &Executor{Confirm: func() bool { return true }}
	ok, msg = e.decideCommit(false)
	if !ok || msg != "" {
		t.Errorf("repl yes: got (%v, %q)", ok, msg)
	}

	// REPL mode with user saying n → aborted.
	e = &Executor{Confirm: func() bool { return false }}
	ok, msg = e.decideCommit(false)
	if ok || msg != "(aborted)" {
		t.Errorf("repl no: got (%v, %q)", ok, msg)
	}
}

func TestRemoveIndices(t *testing.T) {
	rows := []Row{
		{Cell{Int: 1}}, {Cell{Int: 2}}, {Cell{Int: 3}}, {Cell{Int: 4}}, {Cell{Int: 5}},
	}
	got := removeIndices(rows, []int{0, 2, 4})
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0][0].Int != 2 || got[1][0].Int != 4 {
		t.Errorf("got %+v, want [2, 4]", got)
	}
}
