package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
		if ctx != nil && ctx.AggResults != nil {
			if v, ok := ctx.AggResults[n]; ok {
				return v, nil
			}
		}
		return EvalCell{}, fmt.Errorf("aggregate %s evaluated outside an aggregation context", n.Func)
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

// coerceEvalCell converts an expression result to a Cell suitable for storage
// in a column of the given target type. NULL passes through. Same-type results
// copy directly. Cross-type results route through CoerceLiteral so the rules
// match literal coercion in INSERT/UPDATE — string↔number, bool↔string, etc.
// behave identically. Float→int succeeds only when the float value is exactly
// representable as an integer; partial values surface as coercion errors
// rather than silently truncating.
func coerceEvalCell(e EvalCell, target ColumnType, colName string) (Cell, error) {
	if e.Cell.Null {
		return Cell{Null: true}, nil
	}
	if e.Type == target {
		return e.Cell, nil
	}
	cell, err := CoerceLiteral(evalCellAsValue(e), target)
	if err != nil {
		return Cell{}, fmt.Errorf("column %q: %w", colName, err)
	}
	return cell, nil
}

// evalCellAsValue formats a non-NULL EvalCell as a parser-shaped Value so it
// can be coerced via the shared CoerceLiteral path. Float formatting uses
// the shortest round-trippable representation; date formatting uses RFC3339
// because CoerceLiteral parses dates from ISO 8601 strings.
func evalCellAsValue(e EvalCell) Value {
	if e.Cell.Null {
		return Value{Kind: ValNull}
	}
	switch e.Type {
	case TypeInt:
		return Value{Kind: ValNumber, Num: strconv.FormatInt(e.Cell.Int, 10)}
	case TypeFloat:
		return Value{Kind: ValNumber, Num: strconv.FormatFloat(e.Cell.Float, 'g', -1, 64)}
	case TypeBool:
		return Value{Kind: ValBool, Bool: e.Cell.Bool}
	case TypeString:
		return Value{Kind: ValString, Str: e.Cell.Str}
	case TypeDate:
		return Value{Kind: ValString, Str: e.Cell.Date.Format(time.RFC3339)}
	}
	return Value{Kind: ValNull}
}

// exprType derives the result type of an expression without evaluating it.
// Numeric literals with a decimal point are floats; integer-shaped numerics
// are ints. NULL literals default to TypeString — the Null flag is what
// matters at evaluation time. Arithmetic mirrors evalBinary: / always yields
// float, + - * stay int when both operands are int and promote otherwise.
// Aggregates are rejected here; planProjection handles them in slice 4.
func exprType(e Expr, schema map[string]ColumnInfo) (ColumnType, error) {
	switch n := e.(type) {
	case *ColumnExpr:
		info, ok := schema[n.Name]
		if !ok {
			return TypeString, fmt.Errorf("unknown column %q", n.Name)
		}
		return info.Type, nil
	case *LiteralExpr:
		switch n.Value.Kind {
		case ValNumber:
			if strings.ContainsRune(n.Value.Num, '.') {
				return TypeFloat, nil
			}
			return TypeInt, nil
		case ValString, ValNull:
			return TypeString, nil
		case ValBool:
			return TypeBool, nil
		}
		return TypeString, fmt.Errorf("internal: unknown literal kind %d", n.Value.Kind)
	case *BinaryExpr:
		lt, err := exprType(n.L, schema)
		if err != nil {
			return TypeString, err
		}
		rt, err := exprType(n.R, schema)
		if err != nil {
			return TypeString, err
		}
		return arithResultType(lt, rt, n.Op), nil
	case *AggregateExpr:
		return aggregateOutputType(n, schema)
	}
	return TypeString, fmt.Errorf("internal: unhandled expression type %T", e)
}

// aggregateOutputType derives the static result type for an aggregate node.
// COUNT is always int; AVG is always float; SUM/MIN/MAX inherit the static
// type of the argument expression. The runtime path may promote SUM from int
// to float on the first float input, but the static type used for projection
// dedup keys and rendering tracks the argument's declared type.
func aggregateOutputType(a *AggregateExpr, schema map[string]ColumnInfo) (ColumnType, error) {
	if a.Star {
		return TypeInt, nil
	}
	switch a.Func {
	case "COUNT":
		return TypeInt, nil
	case "AVG":
		return TypeFloat, nil
	case "SUM", "MIN", "MAX":
		return exprType(a.Arg, schema)
	}
	return TypeString, fmt.Errorf("internal: unknown aggregate %q", a.Func)
}

// hasAggregate reports whether the expression tree contains an aggregate
// node. UPDATE SET and WHERE forbid aggregates; SELECT projection and
// HAVING permit them.
func hasAggregate(e Expr) bool {
	switch n := e.(type) {
	case *AggregateExpr:
		return true
	case *BinaryExpr:
		return hasAggregate(n.L) || hasAggregate(n.R)
	}
	return false
}

// bareColumn returns the name of a ColumnExpr that appears outside any
// AggregateExpr in the tree, or "" if every column reference is wrapped in
// an aggregate. Used to reject `SELECT Title, COUNT(*)` when no GROUP BY is
// in play — Postgres-strict semantics per Pass 2 decisions.
func bareColumn(e Expr) string {
	switch n := e.(type) {
	case *ColumnExpr:
		return n.Name
	case *BinaryExpr:
		if c := bareColumn(n.L); c != "" {
			return c
		}
		return bareColumn(n.R)
	case *AggregateExpr:
		return ""
	}
	return ""
}

// bareColumnNotIn returns the name of a column reference that appears
// outside any AggregateExpr and is not in the allowed set, or "" if every
// such reference is permitted. Used by GROUP BY validation: bare columns
// must be one of the GROUP BY columns; aggregate arguments may reference
// any column.
func bareColumnNotIn(e Expr, allowed map[string]bool) string {
	switch n := e.(type) {
	case *ColumnExpr:
		if allowed[n.Name] {
			return ""
		}
		return n.Name
	case *BinaryExpr:
		if c := bareColumnNotIn(n.L, allowed); c != "" {
			return c
		}
		return bareColumnNotIn(n.R, allowed)
	case *AggregateExpr:
		return ""
	}
	return ""
}

// collectAggregatesFromPredicate gathers aggregate nodes reachable from a
// HAVING predicate. Comparison LHSes pass through collectAggregates;
// NullTest/LIKE/IN/BETWEEN bind to bare column names directly and contribute
// no aggregates.
func collectAggregatesFromPredicate(p Predicate, out []*AggregateExpr) []*AggregateExpr {
	switch n := p.(type) {
	case *BinaryOp:
		out = collectAggregatesFromPredicate(n.L, out)
		return collectAggregatesFromPredicate(n.R, out)
	case *NotOp:
		return collectAggregatesFromPredicate(n.Inner, out)
	case *Comparison:
		return collectAggregates(n.LExpr, out)
	}
	return out
}

// collectAggregates walks the tree and appends each AggregateExpr to out.
// Order is left-to-right, depth-first. Pointer identity defines slot
// uniqueness — distinct AST nodes produce distinct slots even if they read
// the same column. Nested aggregates are rejected at validate time, so this
// walker never recurses through an AggregateExpr's Arg.
func collectAggregates(e Expr, out []*AggregateExpr) []*AggregateExpr {
	switch n := e.(type) {
	case *AggregateExpr:
		return append(out, n)
	case *BinaryExpr:
		out = collectAggregates(n.L, out)
		return collectAggregates(n.R, out)
	}
	return out
}

// validateAggregate checks an AggregateExpr is well-formed: the function is
// known, COUNT is the only one that accepts *, the argument validates
// against the schema, no nested aggregates, and SUM/AVG arguments are
// numeric. MIN/MAX accept any comparable type; runtime Compare handles the
// type-specific path.
func validateAggregate(a *AggregateExpr, schema map[string]ColumnInfo) error {
	switch a.Func {
	case "COUNT", "SUM", "AVG", "MIN", "MAX":
	default:
		return fmt.Errorf("unknown aggregate function %q", a.Func)
	}
	if a.Star {
		if a.Func != "COUNT" {
			return fmt.Errorf("%s(*) is not valid; only COUNT(*) is supported", a.Func)
		}
		return nil
	}
	if err := validateExpr(a.Arg, schema); err != nil {
		return err
	}
	if hasAggregate(a.Arg) {
		return fmt.Errorf("%s: nested aggregates are not allowed", a.Func)
	}
	argT, err := exprType(a.Arg, schema)
	if err != nil {
		return err
	}
	if a.Func == "SUM" || a.Func == "AVG" {
		if argT != TypeInt && argT != TypeFloat {
			return fmt.Errorf("%s requires a numeric argument, got %s", a.Func, argT)
		}
	}
	return nil
}

// aggSlot is the per-aggregate accumulator. One slot per unique AggregateExpr
// in the projection plan; advance(row) consumes one input row, finalize()
// produces the aggregated result. The state union is wide enough to cover
// every function — each one reads only the fields its semantics require.
type aggSlot struct {
	Expr    *AggregateExpr
	ArgType ColumnType

	count      int64
	sumInt     int64
	sumFloat   float64
	sumIsInt   bool
	minMaxCell Cell
	hasValue   bool
}

// newAggSlot builds a slot for an aggregate node. ArgType is the static type
// of the argument expression (used for MIN/MAX comparison and the static
// output type of SUM/MIN/MAX); COUNT(*) carries TypeInt as a placeholder.
// sumIsInt starts true; the first float-typed value flips it and converts
// any int sum collected so far into the float accumulator.
func newAggSlot(a *AggregateExpr, schema map[string]ColumnInfo) (*aggSlot, error) {
	s := &aggSlot{Expr: a, sumIsInt: true, ArgType: TypeInt}
	if !a.Star {
		t, err := exprType(a.Arg, schema)
		if err != nil {
			return nil, err
		}
		s.ArgType = t
	}
	return s, nil
}

// advance folds one row into the accumulator. COUNT(*) counts unconditionally;
// every other function evaluates the argument expression and skips NULL,
// matching standard SQL aggregate NULL semantics.
func (s *aggSlot) advance(row Row, ctx *EvalContext) error {
	if s.Expr.Star {
		s.count++
		return nil
	}
	v, err := EvalExpr(s.Expr.Arg, row, ctx)
	if err != nil {
		return err
	}
	if v.Cell.Null {
		return nil
	}
	switch s.Expr.Func {
	case "COUNT":
		s.count++
	case "SUM":
		s.hasValue = true
		s.count++
		if v.Type == TypeFloat {
			if s.sumIsInt {
				s.sumFloat = float64(s.sumInt)
				s.sumIsInt = false
			}
			s.sumFloat += v.Cell.Float
		} else {
			if s.sumIsInt {
				s.sumInt += v.Cell.Int
			} else {
				s.sumFloat += float64(v.Cell.Int)
			}
		}
	case "AVG":
		s.hasValue = true
		s.count++
		if v.Type == TypeFloat {
			s.sumFloat += v.Cell.Float
		} else {
			s.sumFloat += float64(v.Cell.Int)
		}
	case "MIN":
		if !s.hasValue || Compare(v.Cell, s.minMaxCell, s.ArgType) < 0 {
			s.minMaxCell = v.Cell
			s.hasValue = true
		}
	case "MAX":
		if !s.hasValue || Compare(v.Cell, s.minMaxCell, s.ArgType) > 0 {
			s.minMaxCell = v.Cell
			s.hasValue = true
		}
	}
	return nil
}

// finalize closes the accumulator and yields the EvalCell that represents
// this aggregate's value for the output row. COUNT always produces an
// integer (0 over an empty set). SUM/AVG/MIN/MAX produce NULL over an empty
// or all-NULL set; their static type is preserved so projection rendering
// stays consistent.
func (s *aggSlot) finalize() EvalCell {
	switch s.Expr.Func {
	case "COUNT":
		return EvalCell{Cell: Cell{Int: s.count}, Type: TypeInt}
	case "SUM":
		if !s.hasValue {
			return EvalCell{Cell: Cell{Null: true}, Type: s.ArgType}
		}
		if s.sumIsInt {
			return EvalCell{Cell: Cell{Int: s.sumInt}, Type: TypeInt}
		}
		return EvalCell{Cell: Cell{Float: s.sumFloat}, Type: TypeFloat}
	case "AVG":
		if !s.hasValue {
			return EvalCell{Cell: Cell{Null: true}, Type: TypeFloat}
		}
		return EvalCell{Cell: Cell{Float: s.sumFloat / float64(s.count)}, Type: TypeFloat}
	case "MIN", "MAX":
		if !s.hasValue {
			return EvalCell{Cell: Cell{Null: true}, Type: s.ArgType}
		}
		return EvalCell{Cell: s.minMaxCell, Type: s.ArgType}
	}
	return EvalCell{Cell: Cell{Null: true}, Type: TypeString}
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
