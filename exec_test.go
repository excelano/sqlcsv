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

func TestExecSelectArithmeticProjection(t *testing.T) {
	e, out, _ := newExec(t)
	// Priority * 10 evaluated per row. Label synthesizes to "Priority * 10".
	stmt, err := Parse("SELECT ID, Priority * 10 WHERE Status = 'Open' AND Priority IS NOT NULL")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "ID\tPriority * 10" {
		t.Errorf("header: %q", lines[0])
	}
	// Open rows with non-null priority: ID 1 (P=3), ID 3 (P=5) → 30, 50.
	if !strings.Contains(out.String(), "1\t30") {
		t.Errorf("expected 1\\t30: %q", out.String())
	}
	if !strings.Contains(out.String(), "3\t50") {
		t.Errorf("expected 3\\t50: %q", out.String())
	}
}

func TestExecSelectAlias(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT Priority * 10 AS scaled WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "scaled" {
		t.Errorf("header should use alias: %q", lines[0])
	}
	if lines[1] != "30" {
		t.Errorf("value: %q", lines[1])
	}
}

func TestExecSelectMixedColumnAndExpression(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT Title, Priority, Priority * 2 AS doubled WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "Title\tPriority\tdoubled" {
		t.Errorf("header: %q", lines[0])
	}
	if lines[1] != "Alpha\t3\t6" {
		t.Errorf("row: %q", lines[1])
	}
}

func TestExecSelectLiteralProjection(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID, 'fixed' AS label WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "1\tfixed") {
		t.Errorf("expected literal projection: %q", out.String())
	}
}

func TestExecSelectArithmeticNullRenders(t *testing.T) {
	// Row 4 has Priority=NULL. Priority * 2 should render as empty trailing
	// field. TrimSpace would eat the trailing tab/empty field, so strip only
	// the final newline.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID, Priority * 2 WHERE ID = 4")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	parts := strings.Split(lines[1], "\t")
	if len(parts) != 2 || parts[0] != "4" || parts[1] != "" {
		t.Errorf("NULL arithmetic should render as 4\\t<empty>: parts=%q", parts)
	}
}

func TestExecSelectDuplicateLabelRejected(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("SELECT ID, ID")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("duplicate output label should error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should explain: %v", err)
	}
}

func TestExecSelectDuplicateResolvedByAlias(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT ID, ID AS dup WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "ID\tdup") {
		t.Errorf("alias should resolve collision: %q", out.String())
	}
}

func TestExecSelectDistinctOverComputed(t *testing.T) {
	// Priority * 0 evaluates to 0 for every non-null row, NULL for row 4.
	// SELECT DISTINCT collapses to two output rows: one "0" and one empty.
	// Each Fprintln appends a newline, so 3 lines means 3 newlines in the
	// raw output.
	e, out, _ := newExec(t)
	stmt, err := Parse("SELECT DISTINCT Priority * 0")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.String()
	if n := strings.Count(got, "\n"); n != 3 {
		t.Fatalf("expected 3 newlines (header + 2 rows), got %d: %q", n, got)
	}
	if got != "Priority * 0\n0\n\n" {
		t.Errorf("rendered: %q", got)
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

func TestExecUpdateComputed(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("UPDATE SET Priority = Priority + 10 WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := e.Table.Rows[0][3].Int; got != 13 {
		t.Errorf("row 0 Priority = %d, want 13 (was 3 + 10)", got)
	}
	if got := e.Table.Rows[1][3].Int; got != 1 {
		t.Errorf("row 1 Priority = %d, want unchanged 1", got)
	}
}

func TestExecUpdateComputedAcrossRows(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("UPDATE SET Priority = Priority * 2 WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Rows 1, 3, 4 are Open. Row 4 has Priority=NULL → stays NULL.
	// Row 1: 3 → 6. Row 3: 5 → 10. Row 2 (Done): unchanged at 1.
	wants := []struct {
		idx  int
		want int64
		null bool
	}{
		{0, 6, false},
		{1, 1, false},
		{2, 10, false},
		{3, 0, true},
	}
	for _, w := range wants {
		cell := e.Table.Rows[w.idx][3]
		if cell.Null != w.null {
			t.Errorf("row %d: null=%v, want %v", w.idx, cell.Null, w.null)
		}
		if !w.null && cell.Int != w.want {
			t.Errorf("row %d: Priority=%d, want %d", w.idx, cell.Int, w.want)
		}
	}
}

func TestExecUpdateComputedNullPropagates(t *testing.T) {
	e, _, _ := newExec(t)
	// Row 4 has Priority=NULL. Adding 1 keeps it NULL — does not crash, does
	// not coerce.
	stmt, err := Parse("UPDATE SET Priority = Priority + 1 WHERE ID = 4")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !e.Table.Rows[3][3].Null {
		t.Errorf("row 3 (ID=4) Priority should remain NULL, got %+v", e.Table.Rows[3][3])
	}
}

func TestExecUpdateComputedPreviewShowsExpression(t *testing.T) {
	e, buf, _ := newExec(t)
	stmt, err := Parse("UPDATE SET Priority = Priority + 1 WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(buf.String(), "SET Priority = Priority + 1") {
		t.Errorf("preview should reflect source expression: %q", buf.String())
	}
}

func TestExecUpdateComputedSwapSemantics(t *testing.T) {
	// Standard SQL: every SET RHS evaluates against the pre-update row.
	// SET a = b, b = a should swap; using the new value of `a` for `b` would
	// leave both columns equal to the old `b`.
	e, _, _ := newExec(t)
	stmt, err := Parse("UPDATE SET ID = Priority, Priority = ID WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Row 0 originally: ID=1, Priority=3. After swap: ID=3, Priority=1.
	if e.Table.Rows[0][0].Int != 3 {
		t.Errorf("ID = %d, want 3 (swap)", e.Table.Rows[0][0].Int)
	}
	if e.Table.Rows[0][3].Int != 1 {
		t.Errorf("Priority = %d, want 1 (swap)", e.Table.Rows[0][3].Int)
	}
}

func TestExecUpdateUnknownColumnInExpression(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("UPDATE SET Priority = Nope + 1 WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("expected error for unknown column in SET expression")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Errorf("error should mention column name: %v", err)
	}
}

func TestExecUpdateAggregateInSetRejected(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := Parse("UPDATE SET Priority = COUNT(*) WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("aggregates in SET should be rejected")
	}
	if !strings.Contains(err.Error(), "aggregate") {
		t.Errorf("error should mention aggregate: %v", err)
	}
}

func TestExecUpdateComputedFloatIntoIntRejected(t *testing.T) {
	// 3 / 2 = 1.5 in v2 (/ always promotes to float). Storing 1.5 into an
	// int column has no lossless representation, so coerceEvalCell should
	// reject. SET Priority = 3 / 2 WHERE ID = 1 exercises that path.
	e, _, _ := newExec(t)
	stmt, err := Parse("UPDATE SET Priority = 3 / 2 WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("float result into int column should error")
	}
}

func TestExecUpdateLiteralStillWorks(t *testing.T) {
	// Slice 2 should not regress the v1 literal-SET path.
	e, _, path := newExec(t)
	stmt, err := Parse("UPDATE SET Status = 'Done' WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadCSV(path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range reloaded.Rows {
		if row[2].Str != "Done" {
			t.Errorf("row %d: Status %q, want Done", i, row[2].Str)
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
