package main

import (
	"strings"
	"testing"
)

// fixtureTable builds a small in-memory Table representative of a typical CSV:
// mixed types, NULL cells, enough rows to exercise filtering. Used as the
// shared fixture for the filter and exec tests.
func fixtureTable() *Table {
	cols := []string{"ID", "Title", "Status", "Priority", "Archived", "Modified"}
	schema := map[string]ColumnInfo{
		"ID":       {Name: "ID", Type: TypeInt},
		"Title":    {Name: "Title", Type: TypeString},
		"Status":   {Name: "Status", Type: TypeString},
		"Priority": {Name: "Priority", Type: TypeInt},
		"Archived": {Name: "Archived", Type: TypeBool},
		"Modified": {Name: "Modified", Type: TypeDate},
	}
	rows := []Row{
		{Cell{Int: 1}, Cell{Str: "Alpha"}, Cell{Str: "Open"}, Cell{Int: 3}, Cell{Bool: false}, Cell{Date: mustDate("2024-01-15")}},
		{Cell{Int: 2}, Cell{Str: "Beta"}, Cell{Str: "Done"}, Cell{Int: 1}, Cell{Bool: true}, Cell{Date: mustDate("2023-11-30")}},
		{Cell{Int: 3}, Cell{Str: "Gamma"}, Cell{Str: "Open"}, Cell{Int: 5}, Cell{Bool: false}, Cell{Null: true}},
		{Cell{Int: 4}, Cell{Str: "Delta"}, Cell{Str: "Open"}, Cell{Null: true}, Cell{Bool: false}, Cell{Date: mustDate("2024-03-01")}},
	}
	return &Table{
		Path:      "fixture.csv",
		Columns:   cols,
		Schema:    schema,
		Rows:      rows,
		Delim:     ',',
		HasHeader: true,
	}
}

func TestMatchesNilPredicate(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	ok, err := Matches(nil, tbl.Rows[0], ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("nil predicate should match every row")
	}
}

func TestMatchesComparisons(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)

	tests := []struct {
		name    string
		pred    Predicate
		wantIDs []int64 // matching row IDs in order
	}{
		{"int equals", cmp("Priority", "=", vnum("1")), []int64{2}},
		{"int greater", cmp("Priority", ">", vnum("2")), []int64{1, 3}},
		{"int less or equal", cmp("Priority", "<=", vnum("3")), []int64{1, 2}},
		{"string equals", cmp("Status", "=", vstr("Open")), []int64{1, 3, 4}},
		{"string not equals", cmp("Status", "!=", vstr("Open")), []int64{2}},
		{"bool true", cmp("Archived", "=", vbool(true)), []int64{2}},
		{"date before", cmp("Modified", "<", vstr("2024-01-01")), []int64{2}},
		{"date on or after", cmp("Modified", ">=", vstr("2024-01-01")), []int64{1, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := matchingIDs(t, tbl, ctx, tt.pred)
			if !equalIDs(ids, tt.wantIDs) {
				t.Fatalf("got %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

func TestMatchesNullTests(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)

	ids := matchingIDs(t, tbl, ctx, isnull("Modified", false))
	if !equalIDs(ids, []int64{3}) {
		t.Fatalf("IS NULL: got %v, want [3]", ids)
	}
	ids = matchingIDs(t, tbl, ctx, isnull("Modified", true))
	if !equalIDs(ids, []int64{1, 2, 4}) {
		t.Fatalf("IS NOT NULL: got %v, want [1 2 4]", ids)
	}
}

func TestMatchesNullExcludesFromComparison(t *testing.T) {
	// Row 4 has Priority = NULL. Comparing NULL with anything is UNKNOWN,
	// which means the row is excluded from WHERE results regardless of op.
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	for _, op := range []string{"=", "!=", "<", "<=", ">", ">="} {
		ids := matchingIDs(t, tbl, ctx, cmp("Priority", op, vnum("99")))
		for _, id := range ids {
			if id == 4 {
				t.Fatalf("op %q: row 4 (Priority=NULL) leaked into result", op)
			}
		}
	}
}

func TestMatchesLogicalOps(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)

	tests := []struct {
		name    string
		pred    Predicate
		wantIDs []int64
	}{
		{
			name:    "AND",
			pred:    and(cmp("Status", "=", vstr("Open")), cmp("Priority", ">", vnum("2"))),
			wantIDs: []int64{1, 3},
		},
		{
			name:    "OR",
			pred:    or(cmp("Status", "=", vstr("Done")), cmp("Priority", "=", vnum("5"))),
			wantIDs: []int64{2, 3},
		},
		{
			name:    "NOT",
			pred:    not(cmp("Status", "=", vstr("Open"))),
			wantIDs: []int64{2},
		},
		{
			name: "compound: open AND not archived AND priority >= 3",
			pred: and(
				and(cmp("Status", "=", vstr("Open")), not(cmp("Archived", "=", vbool(true)))),
				cmp("Priority", ">=", vnum("3")),
			),
			wantIDs: []int64{1, 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := matchingIDs(t, tbl, ctx, tt.pred)
			if !equalIDs(ids, tt.wantIDs) {
				t.Fatalf("got %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

// Three-valued logic: NULL OR TRUE = TRUE; NULL AND TRUE = NULL (excludes).
func TestMatchesThreeValuedLogic(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)

	// Row 4: Priority=NULL. The NULL-tainted comparison is UNKNOWN.
	// "Priority > 0 OR Status = 'Open'" should still match row 4 because
	// the second branch is TRUE: UNKNOWN OR TRUE = TRUE.
	pred := or(cmp("Priority", ">", vnum("0")), cmp("Status", "=", vstr("Open")))
	ids := matchingIDs(t, tbl, ctx, pred)
	found4 := false
	for _, id := range ids {
		if id == 4 {
			found4 = true
		}
	}
	if !found4 {
		t.Fatal("UNKNOWN OR TRUE should let row 4 through")
	}

	// "Priority > 0 AND Status = 'Open'" should NOT match row 4 because
	// UNKNOWN AND TRUE = UNKNOWN, which is excluded.
	pred2 := and(cmp("Priority", ">", vnum("0")), cmp("Status", "=", vstr("Open")))
	ids2 := matchingIDs(t, tbl, ctx, pred2)
	for _, id := range ids2 {
		if id == 4 {
			t.Fatal("UNKNOWN AND TRUE should exclude row 4")
		}
	}
}

func TestValidatePredicate(t *testing.T) {
	tbl := fixtureTable()
	err := ValidatePredicate(cmp("Nope", "=", vstr("x")), tbl.Schema)
	if err == nil {
		t.Fatal("expected error for unknown column")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("error should mention column name: %v", err)
	}

	err = ValidatePredicate(isnull("Nope", false), tbl.Schema)
	if err == nil {
		t.Fatal("expected error for unknown column in IS NULL")
	}

	err = ValidatePredicate(and(cmp("Status", "=", vstr("Open")), cmp("Nope", "=", vstr("x"))), tbl.Schema)
	if err == nil {
		t.Fatal("expected error for unknown column nested in AND")
	}

	// Valid predicate passes.
	if err := ValidatePredicate(cmp("Status", "=", vstr("Open")), tbl.Schema); err != nil {
		t.Fatalf("valid predicate should pass: %v", err)
	}
}

func TestTriLogic(t *testing.T) {
	// Spot-check the truth tables — these power three-valued WHERE semantics.
	tests := []struct {
		name string
		got  triVal
		want triVal
	}{
		{"T AND T", triAnd(triTrue, triTrue), triTrue},
		{"T AND F", triAnd(triTrue, triFalse), triFalse},
		{"T AND U", triAnd(triTrue, triUnknown), triUnknown},
		{"F AND U", triAnd(triFalse, triUnknown), triFalse},
		{"U AND U", triAnd(triUnknown, triUnknown), triUnknown},
		{"T OR F", triOr(triTrue, triFalse), triTrue},
		{"F OR U", triOr(triFalse, triUnknown), triUnknown},
		{"T OR U", triOr(triTrue, triUnknown), triTrue},
		{"NOT T", triNot(triTrue), triFalse},
		{"NOT F", triNot(triFalse), triTrue},
		{"NOT U", triNot(triUnknown), triUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func matchingIDs(t *testing.T, tbl *Table, ctx *EvalContext, pred Predicate) []int64 {
	t.Helper()
	var out []int64
	for _, row := range tbl.Rows {
		ok, err := Matches(pred, row, ctx)
		if err != nil {
			t.Fatalf("Matches: %v", err)
		}
		if ok {
			out = append(out, row[0].Int)
		}
	}
	return out
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
