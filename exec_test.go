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
