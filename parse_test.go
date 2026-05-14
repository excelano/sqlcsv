package main

import (
	"reflect"
	"strings"
	"testing"
)

func vstr(s string) Value  { return Value{Kind: ValString, Str: s} }
func vnum(n string) Value  { return Value{Kind: ValNumber, Num: n} }
func vbool(b bool) Value   { return Value{Kind: ValBool, Bool: b} }
func vnull() Value         { return Value{Kind: ValNull} }
func cmp(c, op string, v Value) *Comparison { return &Comparison{Column: c, Op: op, Value: v} }
func isnull(c string, not bool) *NullTest    { return &NullTest{Column: c, Not: not} }
func and(l, r Predicate) *BinaryOp          { return &BinaryOp{Op: "AND", L: l, R: r} }
func or(l, r Predicate) *BinaryOp           { return &BinaryOp{Op: "OR", L: l, R: r} }
func not(p Predicate) *NotOp                { return &NotOp{Inner: p} }

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Stmt
		wantErr string
	}{
		// SELECT projections
		{
			name:  "select star",
			input: "SELECT *",
			want:  &SelectStmt{Star: true},
		},
		{
			name:  "select star lowercase",
			input: "select *",
			want:  &SelectStmt{Star: true},
		},
		{
			name:  "select single column",
			input: "SELECT Title",
			want:  &SelectStmt{Columns: []string{"Title"}},
		},
		{
			name:  "select multiple columns",
			input: "SELECT Title, Status, Priority",
			want:  &SelectStmt{Columns: []string{"Title", "Status", "Priority"}},
		},
		{
			name:  "select with quoted identifier",
			input: `SELECT Title, "Created Date"`,
			want:  &SelectStmt{Columns: []string{"Title", "Created Date"}},
		},
		{
			name:  "select with escaped quote in identifier",
			input: `SELECT "He said ""hi"""`,
			want:  &SelectStmt{Columns: []string{`He said "hi"`}},
		},

		// SELECT with WHERE: comparison operators
		{
			name:  "select where equals",
			input: "SELECT Title WHERE Status = 'Open'",
			want:  &SelectStmt{Columns: []string{"Title"}, Where: cmp("Status", "=", vstr("Open"))},
		},
		{
			name:  "select where not equals",
			input: "SELECT Title WHERE Status != 'Open'",
			want:  &SelectStmt{Columns: []string{"Title"}, Where: cmp("Status", "!=", vstr("Open"))},
		},
		{
			name:  "select where less than",
			input: "SELECT Title WHERE Priority < 3",
			want:  &SelectStmt{Columns: []string{"Title"}, Where: cmp("Priority", "<", vnum("3"))},
		},
		{
			name:  "select where less or equal",
			input: "SELECT Title WHERE Priority <= 3",
			want:  &SelectStmt{Columns: []string{"Title"}, Where: cmp("Priority", "<=", vnum("3"))},
		},
		{
			name:  "select where greater than",
			input: "SELECT Title WHERE Priority > 3",
			want:  &SelectStmt{Columns: []string{"Title"}, Where: cmp("Priority", ">", vnum("3"))},
		},
		{
			name:  "select where greater or equal",
			input: "SELECT Title WHERE Priority >= 3",
			want:  &SelectStmt{Columns: []string{"Title"}, Where: cmp("Priority", ">=", vnum("3"))},
		},

		// Value kinds
		{
			name:  "negative number",
			input: "SELECT * WHERE Balance = -1.5",
			want:  &SelectStmt{Star: true, Where: cmp("Balance", "=", vnum("-1.5"))},
		},
		{
			name:  "bool true",
			input: "SELECT * WHERE Archived = TRUE",
			want:  &SelectStmt{Star: true, Where: cmp("Archived", "=", vbool(true))},
		},
		{
			name:  "bool false lowercase",
			input: "SELECT * WHERE Archived = false",
			want:  &SelectStmt{Star: true, Where: cmp("Archived", "=", vbool(false))},
		},
		{
			name:  "string with escaped quote",
			input: "SELECT * WHERE Name = 'O''Brien'",
			want:  &SelectStmt{Star: true, Where: cmp("Name", "=", vstr("O'Brien"))},
		},

		// IS NULL / IS NOT NULL
		{
			name:  "is null",
			input: "SELECT * WHERE DueDate IS NULL",
			want:  &SelectStmt{Star: true, Where: isnull("DueDate", false)},
		},
		{
			name:  "is not null",
			input: "SELECT * WHERE DueDate IS NOT NULL",
			want:  &SelectStmt{Star: true, Where: isnull("DueDate", true)},
		},

		// AND / OR / NOT / parens / precedence
		{
			name:  "and",
			input: "SELECT * WHERE A = 1 AND B = 2",
			want:  &SelectStmt{Star: true, Where: and(cmp("A", "=", vnum("1")), cmp("B", "=", vnum("2")))},
		},
		{
			name:  "or",
			input: "SELECT * WHERE A = 1 OR B = 2",
			want:  &SelectStmt{Star: true, Where: or(cmp("A", "=", vnum("1")), cmp("B", "=", vnum("2")))},
		},
		{
			name:  "not",
			input: "SELECT * WHERE NOT Archived = TRUE",
			want:  &SelectStmt{Star: true, Where: not(cmp("Archived", "=", vbool(true)))},
		},
		{
			name:  "and binds tighter than or",
			input: "SELECT * WHERE A = 1 OR B = 2 AND C = 3",
			want: &SelectStmt{Star: true, Where: or(
				cmp("A", "=", vnum("1")),
				and(cmp("B", "=", vnum("2")), cmp("C", "=", vnum("3"))),
			)},
		},
		{
			name:  "parens override precedence",
			input: "SELECT * WHERE (A = 1 OR B = 2) AND C = 3",
			want: &SelectStmt{Star: true, Where: and(
				or(cmp("A", "=", vnum("1")), cmp("B", "=", vnum("2"))),
				cmp("C", "=", vnum("3")),
			)},
		},
		{
			name:  "double not",
			input: "SELECT * WHERE NOT NOT A = 1",
			want:  &SelectStmt{Star: true, Where: not(not(cmp("A", "=", vnum("1"))))},
		},

		// UPDATE
		{
			name:  "update single assignment",
			input: "UPDATE SET Status = 'Done'",
			want: &UpdateStmt{
				Assignments: []Assignment{{Column: "Status", Value: vstr("Done")}},
			},
		},
		{
			name:  "update multiple assignments with where",
			input: "UPDATE SET Status = 'Done', Priority = 1 WHERE ID = 42",
			want: &UpdateStmt{
				Assignments: []Assignment{
					{Column: "Status", Value: vstr("Done")},
					{Column: "Priority", Value: vnum("1")},
				},
				Where: cmp("ID", "=", vnum("42")),
			},
		},

		// DELETE
		{
			name:  "delete bare",
			input: "DELETE",
			want:  &DeleteStmt{},
		},
		{
			name:  "delete with where",
			input: "DELETE WHERE Status = 'Archived'",
			want:  &DeleteStmt{Where: cmp("Status", "=", vstr("Archived"))},
		},

		// INSERT
		{
			name:  "insert single column",
			input: "INSERT (Title) VALUES ('New')",
			want: &InsertStmt{
				Columns: []string{"Title"},
				Values:  []Value{vstr("New")},
			},
		},
		{
			name:  "insert multiple columns",
			input: "INSERT (Title, Status, Priority) VALUES ('Migration', 'Open', 3)",
			want: &InsertStmt{
				Columns: []string{"Title", "Status", "Priority"},
				Values:  []Value{vstr("Migration"), vstr("Open"), vnum("3")},
			},
		},
		{
			name:  "insert with null value",
			input: "INSERT (Title, DueDate) VALUES ('Task', NULL)",
			want: &InsertStmt{
				Columns: []string{"Title", "DueDate"},
				Values:  []Value{vstr("Task"), vnull()},
			},
		},

		// Mixed case keywords
		{
			name:  "mixed case keywords",
			input: "Select Title Where Status = 'Open' And Priority > 2",
			want: &SelectStmt{
				Columns: []string{"Title"},
				Where:   and(cmp("Status", "=", vstr("Open")), cmp("Priority", ">", vnum("2"))),
			},
		},

		// Whitespace tolerance
		{
			name:  "whitespace tolerant",
			input: "  SELECT   Title   WHERE   Status='Open'  ",
			want: &SelectStmt{
				Columns: []string{"Title"},
				Where:   cmp("Status", "=", vstr("Open")),
			},
		},

		// Negative cases
		{
			name:    "empty input",
			input:   "",
			wantErr: "empty input",
		},
		{
			name:    "select with no projection",
			input:   "SELECT",
			wantErr: "expected column name",
		},
		{
			name:    "select with from",
			input:   "SELECT * FROM Foo",
			wantErr: "unexpected",
		},
		{
			name:    "select with trailing comma",
			input:   "SELECT Title,",
			wantErr: "expected column name",
		},
		{
			name:    "where with no predicate",
			input:   "SELECT * WHERE",
			wantErr: "expected column name",
		},
		{
			name:    "comparison missing value",
			input:   "SELECT * WHERE A =",
			wantErr: "expected literal value",
		},
		{
			name:    "RHS column not allowed",
			input:   "SELECT * WHERE A = B",
			wantErr: "expected literal value",
		},
		{
			name:    "equals null is rejected",
			input:   "SELECT * WHERE A = NULL",
			wantErr: "use IS NULL",
		},
		{
			name:    "not equals null is rejected",
			input:   "SELECT * WHERE A != NULL",
			wantErr: "use IS NULL",
		},
		{
			name:    "trailing junk after statement",
			input:   "SELECT Title WHERE A = 1 EXTRA",
			wantErr: "unexpected",
		},
		{
			name:    "is followed by non-null",
			input:   "SELECT * WHERE A IS 1",
			wantErr: "expected NULL",
		},
		{
			name:    "is not followed by non-null",
			input:   "SELECT * WHERE A IS NOT 1",
			wantErr: "expected NULL",
		},
		{
			name:    "update without set",
			input:   "UPDATE Status = 'Done'",
			wantErr: "expected SET",
		},
		{
			name:    "insert without column list",
			input:   "INSERT VALUES ('A')",
			wantErr: "expected '('",
		},
		{
			name:    "insert missing values keyword",
			input:   "INSERT (A) ('B')",
			wantErr: "expected VALUES",
		},
		{
			name:    "insert missing values paren",
			input:   "INSERT (A) VALUES",
			wantErr: "expected '('",
		},
		{
			name:    "unterminated string",
			input:   "SELECT * WHERE A = 'unfinished",
			wantErr: "unterminated string",
		},
		{
			name:    "unterminated quoted ident",
			input:   `SELECT "unfinished`,
			wantErr: "unterminated quoted identifier",
		},
		{
			name:    "bang without equals",
			input:   "SELECT * WHERE A ! 1",
			wantErr: "expected '=' after '!'",
		},
		{
			name:    "negative without digit",
			input:   "SELECT * WHERE A = -",
			wantErr: "expected digit after '-'",
		},
		{
			name:    "decimal without digit",
			input:   "SELECT * WHERE A = 1.",
			wantErr: "expected digit after '.'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (parsed: %#v)", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("AST mismatch.\n got:  %#v\n want: %#v", got, tt.want)
			}
		})
	}
}

func TestPreProcess(t *testing.T) {
	tests := []struct {
		input      string
		wantClean  string
		wantCommit bool
	}{
		{"SELECT *", "SELECT *", false},
		{"SELECT *;", "SELECT *", false},
		{"SELECT *  ;  ", "SELECT *", false},
		{"DELETE WHERE A = 1 !", "DELETE WHERE A = 1", true},
		{"DELETE WHERE A = 1!", "DELETE WHERE A = 1", true},
		{"DELETE WHERE A = 1 !;", "DELETE WHERE A = 1", true},
		{"DELETE WHERE A = 1 ;!", "DELETE WHERE A = 1", true},
		{"   ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, commit := PreProcess(tt.input)
			if got != tt.wantClean {
				t.Errorf("cleaned: got %q, want %q", got, tt.wantClean)
			}
			if commit != tt.wantCommit {
				t.Errorf("commit: got %v, want %v", commit, tt.wantCommit)
			}
		})
	}
}
