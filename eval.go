package main

import (
	"fmt"
	"strconv"
	"strings"
)

// EvalCell is the typed result of evaluating an Expr against a row. The Cell
// shape matches the column-cell representation so it can flow into the same
// Compare path as raw column values; Type tells the caller which Cell field
// is meaningful.
type EvalCell struct {
	Cell Cell
	Type ColumnType
}

// EvalExpr evaluates an expression tree against a single row. Slice 1 handles
// columns, literals, and arithmetic. Aggregates are recognized at parse time
// but rejected here until slice 4 wires the accumulator path.
func EvalExpr(e Expr, row Row, ctx *EvalContext) (EvalCell, error) {
	switch n := e.(type) {
	case *ColumnExpr:
		idx, ok := ctx.ColIdx[n.Name]
		if !ok {
			return EvalCell{}, fmt.Errorf("unknown column %q", n.Name)
		}
		return EvalCell{Cell: row[idx], Type: ctx.Schema[n.Name].Type}, nil
	case *LiteralExpr:
		return evalLiteralExpr(n)
	case *BinaryExpr:
		return evalBinary(n, row, ctx)
	case *AggregateExpr:
		return EvalCell{}, fmt.Errorf("aggregates parse in v2.0-alpha but executor support lands in v2.0")
	}
	return EvalCell{}, fmt.Errorf("internal: unhandled expression type %T", e)
}

// evalLiteralExpr converts a parser Value into a typed EvalCell. Numbers with
// a decimal point become floats; integer-shaped numbers become ints, falling
// back to float on int64 overflow. NULL literals carry TypeString as a
// placeholder; the Null flag is what callers actually check.
func evalLiteralExpr(l *LiteralExpr) (EvalCell, error) {
	v := l.Value
	switch v.Kind {
	case ValNull:
		return EvalCell{Cell: Cell{Null: true}, Type: TypeString}, nil
	case ValBool:
		return EvalCell{Cell: Cell{Bool: v.Bool}, Type: TypeBool}, nil
	case ValString:
		return EvalCell{Cell: Cell{Str: v.Str}, Type: TypeString}, nil
	case ValNumber:
		if strings.ContainsRune(v.Num, '.') {
			f, err := strconv.ParseFloat(v.Num, 64)
			if err != nil {
				return EvalCell{}, fmt.Errorf("invalid number literal %q", v.Num)
			}
			return EvalCell{Cell: Cell{Float: f}, Type: TypeFloat}, nil
		}
		if n, err := strconv.ParseInt(v.Num, 10, 64); err == nil {
			return EvalCell{Cell: Cell{Int: n}, Type: TypeInt}, nil
		}
		f, err := strconv.ParseFloat(v.Num, 64)
		if err != nil {
			return EvalCell{}, fmt.Errorf("invalid number literal %q", v.Num)
		}
		return EvalCell{Cell: Cell{Float: f}, Type: TypeFloat}, nil
	}
	return EvalCell{}, fmt.Errorf("internal: unknown literal kind %d", v.Kind)
}

// evalBinary handles +, -, *, /. Any NULL operand propagates NULL. `+`, `-`,
// and `*` stay int when both operands are int; otherwise the result is float.
// `/` always returns float (SQLite-style — int division would silently
// truncate column-arithmetic results in ways that surprise spreadsheet
// users). Divide-by-zero yields NULL rather than an error so a single bad
// row does not abort the whole scan.
func evalBinary(b *BinaryExpr, row Row, ctx *EvalContext) (EvalCell, error) {
	l, err := EvalExpr(b.L, row, ctx)
	if err != nil {
		return EvalCell{}, err
	}
	r, err := EvalExpr(b.R, row, ctx)
	if err != nil {
		return EvalCell{}, err
	}
	if l.Cell.Null || r.Cell.Null {
		return EvalCell{Cell: Cell{Null: true}, Type: arithResultType(l.Type, r.Type, b.Op)}, nil
	}
	if !isNumericType(l.Type) {
		return EvalCell{}, fmt.Errorf("arithmetic %q not supported on %s value", b.Op, l.Type)
	}
	if !isNumericType(r.Type) {
		return EvalCell{}, fmt.Errorf("arithmetic %q not supported on %s value", b.Op, r.Type)
	}
	if b.Op == "/" {
		lf := numericFloat(l)
		rf := numericFloat(r)
		if rf == 0 {
			return EvalCell{Cell: Cell{Null: true}, Type: TypeFloat}, nil
		}
		return EvalCell{Cell: Cell{Float: lf / rf}, Type: TypeFloat}, nil
	}
	if l.Type == TypeInt && r.Type == TypeInt {
		li, ri := l.Cell.Int, r.Cell.Int
		var out int64
		switch b.Op {
		case "+":
			out = li + ri
		case "-":
			out = li - ri
		case "*":
			out = li * ri
		default:
			return EvalCell{}, fmt.Errorf("internal: unsupported op %q", b.Op)
		}
		return EvalCell{Cell: Cell{Int: out}, Type: TypeInt}, nil
	}
	lf := numericFloat(l)
	rf := numericFloat(r)
	var out float64
	switch b.Op {
	case "+":
		out = lf + rf
	case "-":
		out = lf - rf
	case "*":
		out = lf * rf
	default:
		return EvalCell{}, fmt.Errorf("internal: unsupported op %q", b.Op)
	}
	return EvalCell{Cell: Cell{Float: out}, Type: TypeFloat}, nil
}

func isNumericType(t ColumnType) bool {
	return t == TypeInt || t == TypeFloat
}

// numericFloat reads the float value out of a numeric EvalCell, promoting an
// int operand to float64. The caller has already checked Null and that the
// type is numeric.
func numericFloat(e EvalCell) float64 {
	if e.Type == TypeInt {
		return float64(e.Cell.Int)
	}
	return e.Cell.Float
}

// arithResultType picks the type that a NULL arithmetic result should carry.
// Division is always float; otherwise int-int stays int and any float operand
// promotes to float. Mirrors evalBinary's branching for non-NULL inputs.
func arithResultType(l, r ColumnType, op string) ColumnType {
	if op == "/" {
		return TypeFloat
	}
	if l == TypeInt && r == TypeInt {
		return TypeInt
	}
	return TypeFloat
}

// validateExpr walks an expression tree and rejects column references that
// don't exist in the schema. Catches typos before the row scan begins.
// Aggregate nodes pass validation here; the executor decides whether they
// are allowed in the calling context.
func validateExpr(e Expr, schema map[string]ColumnInfo) error {
	switch n := e.(type) {
	case *ColumnExpr:
		if _, ok := schema[n.Name]; !ok {
			return fmt.Errorf("unknown column %q", n.Name)
		}
		return nil
	case *LiteralExpr:
		return nil
	case *BinaryExpr:
		if err := validateExpr(n.L, schema); err != nil {
			return err
		}
		return validateExpr(n.R, schema)
	case *AggregateExpr:
		if !n.Star {
			return validateExpr(n.Arg, schema)
		}
		return nil
	}
	return fmt.Errorf("internal: unhandled expression type %T", e)
}
