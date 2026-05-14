package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInferColumn(t *testing.T) {
	tests := []struct {
		name   string
		sample [][]string
		want   ColumnType
	}{
		{"all ints", [][]string{{"1"}, {"2"}, {"-3"}, {"0"}}, TypeInt},
		{"int promotes to float when one decimal appears", [][]string{{"1"}, {"2.5"}, {"3"}}, TypeFloat},
		{"all floats", [][]string{{"1.0"}, {"2.5"}, {"3.14"}}, TypeFloat},
		{"all dates iso", [][]string{{"2024-01-01"}, {"2024-02-15"}}, TypeDate},
		{"date+time mix", [][]string{{"2024-01-01"}, {"2024-02-15T12:00:00Z"}}, TypeDate},
		{"bool words", [][]string{{"true"}, {"false"}, {"yes"}, {"no"}}, TypeBool},
		{"falls back to string when mixed", [][]string{{"open"}, {"closed"}, {"in-progress"}}, TypeString},
		{"empty cells skipped during inference", [][]string{{"1"}, {""}, {"2"}}, TypeInt},
		{"all empty defaults to string", [][]string{{""}, {""}}, TypeString},
		{"single non-int kills int inference", [][]string{{"1"}, {"2"}, {"abc"}}, TypeString},
		{"0/1 stay as int, not bool", [][]string{{"0"}, {"1"}, {"0"}}, TypeInt},
		{"leading-zero ZIP codes stay as string", [][]string{{"07030"}, {"10001"}, {"02101"}}, TypeString},
		{"single leading-zero entry knocks column out of int", [][]string{{"1"}, {"2"}, {"007"}}, TypeString},
		{"NaN keeps column out of float", [][]string{{"1.5"}, {"2.5"}, {"NaN"}}, TypeString},
		{"Infinity keeps column out of float", [][]string{{"1.5"}, {"Inf"}}, TypeString},
		{"signed leading zero is rejected", [][]string{{"-01"}, {"-02"}}, TypeString},
		{"plain zero is fine", [][]string{{"0"}, {"5"}, {"-7"}}, TypeInt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferColumn(tt.sample, 0)
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseCell(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		typ  ColumnType
		want Cell
	}{
		{"empty is null", "", TypeInt, Cell{Null: true}},
		{"whitespace is null", "   ", TypeInt, Cell{Null: true}},
		{"int parses", "42", TypeInt, Cell{Int: 42}},
		{"int negative", "-7", TypeInt, Cell{Int: -7}},
		{"int unparseable becomes null", "abc", TypeInt, Cell{Null: true}},
		{"float parses", "3.14", TypeFloat, Cell{Float: 3.14}},
		{"bool true word", "true", TypeBool, Cell{Bool: true}},
		{"bool yes", "yes", TypeBool, Cell{Bool: true}},
		{"bool false word", "false", TypeBool, Cell{Bool: false}},
		{"bool no", "no", TypeBool, Cell{Bool: false}},
		{"bool unparseable becomes null", "maybe", TypeBool, Cell{Null: true}},
		{"date iso", "2024-01-15", TypeDate, Cell{Date: mustDate("2024-01-15")}},
		{"date with time", "2024-01-15T12:00:00Z", TypeDate, Cell{Date: mustTime("2024-01-15T12:00:00Z")}},
		{"date unparseable becomes null", "yesterday", TypeDate, Cell{Null: true}},
		{"string keeps raw", "hello world", TypeString, Cell{Str: "hello world"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCell(tt.raw, tt.typ)
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFormatCell(t *testing.T) {
	tests := []struct {
		name string
		cell Cell
		typ  ColumnType
		want string
	}{
		{"null", Cell{Null: true}, TypeInt, ""},
		{"int", Cell{Int: 42}, TypeInt, "42"},
		{"float whole", Cell{Float: 3.0}, TypeFloat, "3"},
		{"float fractional", Cell{Float: 3.14}, TypeFloat, "3.14"},
		{"bool true", Cell{Bool: true}, TypeBool, "true"},
		{"bool false", Cell{Bool: false}, TypeBool, "false"},
		{"date only", Cell{Date: mustDate("2024-01-15")}, TypeDate, "2024-01-15"},
		{"datetime", Cell{Date: mustTime("2024-01-15T12:00:00Z")}, TypeDate, "2024-01-15T12:00:00Z"},
		{"string", Cell{Str: "hello"}, TypeString, "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCell(tt.cell, tt.typ)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCoerceLiteral(t *testing.T) {
	tests := []struct {
		name    string
		lit     Value
		typ     ColumnType
		want    Cell
		wantErr string
	}{
		{"null is universal", Value{Kind: ValNull}, TypeInt, Cell{Null: true}, ""},
		{"int literal to int", vnum("42"), TypeInt, Cell{Int: 42}, ""},
		{"string of int to int", vstr("42"), TypeInt, Cell{Int: 42}, ""},
		{"string of non-int to int errors", vstr("abc"), TypeInt, Cell{}, "cannot coerce"},
		{"float literal to float", vnum("3.14"), TypeFloat, Cell{Float: 3.14}, ""},
		{"int literal to float promotes", vnum("3"), TypeFloat, Cell{Float: 3.0}, ""},
		{"bool literal to bool", vbool(true), TypeBool, Cell{Bool: true}, ""},
		{"string true to bool", vstr("true"), TypeBool, Cell{Bool: true}, ""},
		{"string 1 to bool", vstr("1"), TypeBool, Cell{Bool: true}, ""},
		{"date string to date", vstr("2024-01-15"), TypeDate, Cell{Date: mustDate("2024-01-15")}, ""},
		{"number to date errors", vnum("42"), TypeDate, Cell{}, "cannot coerce"},
		{"int literal to string", vnum("42"), TypeString, Cell{Str: "42"}, ""},
		{"bool literal to string", vbool(true), TypeString, Cell{Str: "true"}, ""},
		{"string to string", vstr("hello"), TypeString, Cell{Str: "hello"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CoerceLiteral(tt.lit, tt.typ)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		a, b Cell
		typ  ColumnType
		want int
	}{
		{"null < non-null", Cell{Null: true}, Cell{Int: 1}, TypeInt, -1},
		{"non-null > null", Cell{Int: 1}, Cell{Null: true}, TypeInt, 1},
		{"null == null", Cell{Null: true}, Cell{Null: true}, TypeInt, 0},
		{"int less", Cell{Int: 1}, Cell{Int: 2}, TypeInt, -1},
		{"int equal", Cell{Int: 2}, Cell{Int: 2}, TypeInt, 0},
		{"int greater", Cell{Int: 3}, Cell{Int: 2}, TypeInt, 1},
		{"float less", Cell{Float: 1.5}, Cell{Float: 2.5}, TypeFloat, -1},
		{"bool false < true", Cell{Bool: false}, Cell{Bool: true}, TypeBool, -1},
		{"date before", Cell{Date: mustDate("2024-01-01")}, Cell{Date: mustDate("2024-02-01")}, TypeDate, -1},
		{"string lexical", Cell{Str: "apple"}, Cell{Str: "banana"}, TypeString, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compare(tt.a, tt.b, tt.typ)
			if signOf(got) != signOf(tt.want) {
				t.Fatalf("got %d, want sign %d", got, tt.want)
			}
		})
	}
}

func TestLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	dst := filepath.Join(dir, "out.csv")
	content := "ID,Title,Score,Active,When\n" +
		"1,Alpha,3.5,true,2024-01-15\n" +
		"2,Beta,4.0,false,2024-02-20\n" +
		"3,Gamma,,true,\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}

	wantTypes := map[string]ColumnType{
		"ID": TypeInt, "Title": TypeString, "Score": TypeFloat,
		"Active": TypeBool, "When": TypeDate,
	}
	for name, want := range wantTypes {
		got := tbl.Schema[name].Type
		if got != want {
			t.Errorf("column %s: inferred %v, want %v", name, got, want)
		}
	}
	if len(tbl.Rows) != 3 {
		t.Fatalf("rows: got %d, want 3", len(tbl.Rows))
	}
	if !tbl.Rows[2][2].Null {
		t.Error("Gamma.Score should be null (empty cell)")
	}

	if err := SaveCSV(tbl, dst); err != nil {
		t.Fatalf("SaveCSV: %v", err)
	}
	saved, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Headers + row count match; cell representations may differ slightly
	// (3.5 stays 3.5, 4.0 stringifies as 4 via %g — that's expected).
	lines := strings.Split(strings.TrimSpace(string(saved)), "\n")
	if len(lines) != 4 {
		t.Fatalf("saved has %d lines, want 4 (header + 3 rows)", len(lines))
	}
	if lines[0] != "ID,Title,Score,Active,When" {
		t.Errorf("header: got %q", lines[0])
	}
}

func TestLoadNoHeader(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nh.csv")
	content := "1,Alpha,3.5\n2,Beta,4.0\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{NoHeader: true})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	wantCols := []string{"col1", "col2", "col3"}
	for i, want := range wantCols {
		if tbl.Columns[i] != want {
			t.Errorf("col[%d] = %q, want %q", i, tbl.Columns[i], want)
		}
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(tbl.Rows))
	}
}

func TestLoadCustomDelim(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tab.tsv")
	content := "id\tname\n1\tAlpha\n2\tBeta\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{Delim: '\t'})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if len(tbl.Columns) != 2 || tbl.Columns[0] != "id" || tbl.Columns[1] != "name" {
		t.Fatalf("columns: %v", tbl.Columns)
	}
	if tbl.Rows[0][1].Str != "Alpha" {
		t.Errorf("row 0 name: got %q, want Alpha", tbl.Rows[0][1].Str)
	}
}

func TestLoadTypeHintOverride(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "hint.csv")
	content := "ID,Code\n1,001\n2,002\n3,003\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{
		TypeHints: map[string]ColumnType{"Code": TypeString},
	})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if got := tbl.Schema["Code"].Type; got != TypeString {
		t.Fatalf("Code type: got %v, want string (hint override)", got)
	}
	if got := tbl.Schema["ID"].Type; got != TypeInt {
		t.Fatalf("ID type: got %v, want int (auto inferred)", got)
	}
	if tbl.Rows[0][1].Str != "001" {
		t.Errorf("Code should preserve leading zeros, got %q", tbl.Rows[0][1].Str)
	}
}

// TestLoadStripsUTF8BOM exercises the BOM peek-and-skip path. Excel's
// "Save as CSV UTF-8" writes one and would otherwise corrupt the first
// column header.
func TestLoadStripsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "bom.csv")
	content := "\xef\xbb\xbfID,Name\n1,Alpha\n2,Beta\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if tbl.Columns[0] != "ID" {
		t.Fatalf("first column = %q, want %q (BOM not stripped)", tbl.Columns[0], "ID")
	}
	if _, ok := tbl.Schema["ID"]; !ok {
		t.Fatalf("Schema lookup for ID failed; keys: %v", keysOf(tbl.Schema))
	}
}

// TestLoadAutoDetectsLeadingZeros verifies the inference rule that pulls
// columns with leading-zero values out of TypeInt without needing a
// --type override.
func TestLoadAutoDetectsLeadingZeros(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "zip.csv")
	content := "Name,Zip\nAlice,07030\nBob,10001\nCarol,02101\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if got := tbl.Schema["Zip"].Type; got != TypeString {
		t.Fatalf("Zip type: got %v, want string", got)
	}
	if tbl.Rows[0][1].Str != "07030" {
		t.Errorf("Zip cell: got %q, want %q (leading zero lost)", tbl.Rows[0][1].Str, "07030")
	}
}

func TestLoadRejectsDuplicateHeaders(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dup.csv")
	if err := os.WriteFile(src, []byte("ID,Amount,Amount\n1,10,20\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCSV(src, LoadOptions{})
	if err == nil {
		t.Fatal("expected error for duplicate header")
	}
	if !strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "Amount") {
		t.Fatalf("error should name the duplicate: %v", err)
	}
}

func TestLoadRejectsEmptyHeader(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(src, []byte("ID, ,Name\n1, ,Alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCSV(src, LoadOptions{})
	if err == nil {
		t.Fatal("expected error for empty/whitespace header")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error should mention empty: %v", err)
	}
}

// TestLoadTrimsHeaderWhitespace: a CSV with " Name " in the header should
// load successfully and the column should be addressable as "Name".
func TestLoadTrimsHeaderWhitespace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "trim.csv")
	if err := os.WriteFile(src, []byte(" ID , Name \n1,Alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if tbl.Columns[0] != "ID" || tbl.Columns[1] != "Name" {
		t.Fatalf("columns: %v, want [ID Name]", tbl.Columns)
	}
}

func keysOf(m map[string]ColumnInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestParseColumnType(t *testing.T) {
	tests := []struct {
		in   string
		want ColumnType
		err  bool
	}{
		{"string", TypeString, false},
		{"str", TypeString, false},
		{"int", TypeInt, false},
		{"integer", TypeInt, false},
		{"float", TypeFloat, false},
		{"bool", TypeBool, false},
		{"date", TypeDate, false},
		{"datetime", TypeDate, false},
		{"INT", TypeInt, false},
		{"  bool  ", TypeBool, false},
		{"junk", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseColumnType(tt.in)
			if tt.err {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func signOf(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
