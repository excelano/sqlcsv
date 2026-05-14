package main

import "fmt"

// triVal encodes SQL three-valued logic. WHERE keeps only rows whose
// predicate evaluates to triTrue; triUnknown (NULL-tainted comparisons)
// and triFalse drop the row.
type triVal int

const (
	triFalse   triVal = -1
	triUnknown triVal = 0
	triTrue    triVal = 1
)

// EvalContext bundles the per-table state evalPredicate needs. colIdx maps
// column name to row position; schema carries inferred types. Built once per
// statement and reused across rows.
type EvalContext struct {
	ColIdx map[string]int
	Schema map[string]ColumnInfo
}

// NewEvalContext constructs an EvalContext from a Table.
func NewEvalContext(t *Table) *EvalContext {
	idx := make(map[string]int, len(t.Columns))
	for i, name := range t.Columns {
		idx[name] = i
	}
	return &EvalContext{ColIdx: idx, Schema: t.Schema}
}

// Matches returns true if the predicate evaluates to TRUE for the row.
// A nil predicate matches every row. Unknown (NULL-tainted) results return
// false, matching standard SQL WHERE semantics.
func Matches(p Predicate, row Row, ctx *EvalContext) (bool, error) {
	if p == nil {
		return true, nil
	}
	v, err := evalPredicate(p, row, ctx)
	if err != nil {
		return false, err
	}
	return v == triTrue, nil
}

// ValidatePredicate walks a predicate and returns the first column reference
// that does not exist in the schema. Lets callers reject bad queries before
// scanning the whole table.
func ValidatePredicate(p Predicate, schema map[string]ColumnInfo) error {
	if p == nil {
		return nil
	}
	switch n := p.(type) {
	case *BinaryOp:
		if err := ValidatePredicate(n.L, schema); err != nil {
			return err
		}
		return ValidatePredicate(n.R, schema)
	case *NotOp:
		return ValidatePredicate(n.Inner, schema)
	case *Comparison:
		if _, ok := schema[n.Column]; !ok {
			return fmt.Errorf("unknown column %q", n.Column)
		}
		return nil
	case *NullTest:
		if _, ok := schema[n.Column]; !ok {
			return fmt.Errorf("unknown column %q", n.Column)
		}
		return nil
	}
	return fmt.Errorf("internal: unhandled predicate type %T", p)
}

func evalPredicate(p Predicate, row Row, ctx *EvalContext) (triVal, error) {
	switch n := p.(type) {
	case *BinaryOp:
		l, err := evalPredicate(n.L, row, ctx)
		if err != nil {
			return triFalse, err
		}
		r, err := evalPredicate(n.R, row, ctx)
		if err != nil {
			return triFalse, err
		}
		switch n.Op {
		case "AND":
			return triAnd(l, r), nil
		case "OR":
			return triOr(l, r), nil
		}
		return triFalse, fmt.Errorf("internal: unsupported binary op %q", n.Op)
	case *NotOp:
		inner, err := evalPredicate(n.Inner, row, ctx)
		if err != nil {
			return triFalse, err
		}
		return triNot(inner), nil
	case *Comparison:
		return evalComparison(n, row, ctx)
	case *NullTest:
		return evalNullTest(n, row, ctx)
	}
	return triFalse, fmt.Errorf("internal: unhandled predicate type %T", p)
}

func evalComparison(c *Comparison, row Row, ctx *EvalContext) (triVal, error) {
	idx, ok := ctx.ColIdx[c.Column]
	if !ok {
		return triFalse, fmt.Errorf("unknown column %q", c.Column)
	}
	info := ctx.Schema[c.Column]
	cell := row[idx]
	if cell.Null {
		return triUnknown, nil
	}
	lit, err := CoerceLiteral(c.Value, info.Type)
	if err != nil {
		return triFalse, fmt.Errorf("WHERE %s %s: %w", c.Column, c.Op, err)
	}
	cmp := Compare(cell, lit, info.Type)
	switch c.Op {
	case "=":
		return boolTri(cmp == 0), nil
	case "!=":
		return boolTri(cmp != 0), nil
	case "<":
		return boolTri(cmp < 0), nil
	case "<=":
		return boolTri(cmp <= 0), nil
	case ">":
		return boolTri(cmp > 0), nil
	case ">=":
		return boolTri(cmp >= 0), nil
	}
	return triFalse, fmt.Errorf("internal: unsupported comparison op %q", c.Op)
}

func evalNullTest(n *NullTest, row Row, ctx *EvalContext) (triVal, error) {
	idx, ok := ctx.ColIdx[n.Column]
	if !ok {
		return triFalse, fmt.Errorf("unknown column %q", n.Column)
	}
	isNull := row[idx].Null
	if n.Not {
		return boolTri(!isNull), nil
	}
	return boolTri(isNull), nil
}

func boolTri(b bool) triVal {
	if b {
		return triTrue
	}
	return triFalse
}

// triAnd, triOr, triNot implement Kleene's three-valued logic, matching
// standard SQL.
func triAnd(a, b triVal) triVal {
	if a == triFalse || b == triFalse {
		return triFalse
	}
	if a == triUnknown || b == triUnknown {
		return triUnknown
	}
	return triTrue
}

func triOr(a, b triVal) triVal {
	if a == triTrue || b == triTrue {
		return triTrue
	}
	if a == triUnknown || b == triUnknown {
		return triUnknown
	}
	return triFalse
}

func triNot(a triVal) triVal {
	switch a {
	case triTrue:
		return triFalse
	case triFalse:
		return triTrue
	}
	return triUnknown
}
