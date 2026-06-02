package main

import (
	"fmt"
	"io"
	"sort"
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
	plan, err := e.planProjection(sel)
	if err != nil {
		return err
	}
	if err := ValidatePredicate(sel.Where, e.Table.Schema); err != nil {
		return err
	}
	if err := e.validateOrderBy(sel.OrderBy); err != nil {
		return err
	}
	ctx := NewEvalContext(e.Table)

	matched := make([]int, 0, len(e.Table.Rows))
	for i, row := range e.Table.Rows {
		ok, err := Matches(sel.Where, row, ctx)
		if err != nil {
			return err
		}
		if ok {
			matched = append(matched, i)
		}
	}

	grouped := len(sel.GroupBy) > 0
	aggregated := grouped
	if !grouped {
		for _, p := range plan {
			if hasAggregate(p.Expr) {
				aggregated = true
				break
			}
		}
	}
	// ORDER BY over aggregated output needs to sort the output rows, not the
	// matched source rows; slice 6 rewires sortByKeys for that. Until then,
	// pair them only with the row-stream path.
	if aggregated && len(sel.OrderBy) > 0 {
		return fmt.Errorf("ORDER BY with GROUP BY or aggregates lands in v2.0")
	}
	if sel.Having != nil && !aggregated {
		return fmt.Errorf("HAVING requires GROUP BY or aggregate projections")
	}

	var projected [][]Cell
	if grouped {
		projected, err = e.evalGroupedAggregation(sel, plan, matched, ctx)
		if err != nil {
			return err
		}
	} else if aggregated {
		projected, err = e.evalImplicitAggregation(plan, sel.Having, matched, ctx)
		if err != nil {
			return err
		}
		if sel.Having != nil {
			projected, err = e.applyImplicitHaving(sel.Having, projected, ctx)
			if err != nil {
				return err
			}
		}
	} else {
		if len(sel.OrderBy) > 0 {
			e.sortByKeys(matched, sel.OrderBy, ctx)
		}
		// Evaluate the projection per matched row before DISTINCT / LIMIT so
		// dedup operates on the user-visible output values rather than raw
		// source cells. SELECT DISTINCT price * 0 collapses across all rows.
		projected = make([][]Cell, 0, len(matched))
		for _, idx := range matched {
			row := e.Table.Rows[idx]
			out := make([]Cell, len(plan))
			for i, p := range plan {
				res, err := EvalExpr(p.Expr, row, ctx)
				if err != nil {
					return err
				}
				out[i] = res.Cell
			}
			projected = append(projected, out)
		}
	}

	if sel.Distinct {
		seen := make(map[string]struct{}, len(projected))
		out := projected[:0]
		for _, pr := range projected {
			key := projectedKey(pr, plan)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, pr)
		}
		projected = out
	}

	projected = applyOffsetLimitRows(projected, sel.Offset, sel.Limit)

	labels := make([]string, len(plan))
	for i, p := range plan {
		labels[i] = p.Label
	}
	rows := make([]map[string]any, len(projected))
	for i, pr := range projected {
		m := make(map[string]any, len(plan))
		for j, p := range plan {
			m[p.Label] = pr[j].AsAny(p.Type)
		}
		rows[i] = m
	}
	return Render(e.Out, Result{Columns: labels, Rows: rows}, e.Format)
}

// projEntry is one entry in the SELECT projection plan: the output column
// label, the expression to evaluate per row, and the result type used for
// dedup key formatting and rendering.
type projEntry struct {
	Label string
	Type  ColumnType
	Expr  Expr
}

// evalGroupedAggregation handles SELECT ... GROUP BY [HAVING ...]. Each
// matched row contributes to the group identified by its GROUP BY column
// tuple; the first time a key is seen the group is allocated with a fresh
// slot table that covers every aggregate found in projection AND HAVING.
// After the scan, each group's slots finalize and the per-group projection
// runs against a synthetic row that carries only the GROUP BY column
// values (every other projection reference is either inside an aggregate
// or rejected at plan time). Groups emerge in insertion order — first row
// to introduce a key wins.
func (e *Executor) evalGroupedAggregation(sel *SelectStmt, plan []projEntry, matched []int, ctx *EvalContext) ([][]Cell, error) {
	groupCols := make(map[string]bool, len(sel.GroupBy))
	groupColIdx := make([]int, len(sel.GroupBy))
	groupColTypes := make([]ColumnType, len(sel.GroupBy))
	for i, c := range sel.GroupBy {
		groupCols[c] = true
		groupColIdx[i] = ctx.ColIdx[c]
		groupColTypes[i] = e.Table.Schema[c].Type
	}
	if sel.Having != nil {
		if err := validateAggregatedHaving(sel.Having, groupCols, e.Table.Schema); err != nil {
			return nil, err
		}
	}

	templateAggs := collectAllAggregates(plan, sel.Having)

	type group struct {
		keyCells   []Cell
		slots      []*aggSlot
		slotByExpr map[*AggregateExpr]*aggSlot
	}
	var groupOrder []string
	byKey := make(map[string]*group)

	for _, idx := range matched {
		row := e.Table.Rows[idx]
		key, keyCells := groupKey(row, groupColIdx, groupColTypes)
		g, ok := byKey[key]
		if !ok {
			g = &group{keyCells: keyCells, slotByExpr: make(map[*AggregateExpr]*aggSlot, len(templateAggs))}
			for _, a := range templateAggs {
				s, err := newAggSlot(a, e.Table.Schema)
				if err != nil {
					return nil, err
				}
				g.slots = append(g.slots, s)
				g.slotByExpr[a] = s
			}
			byKey[key] = g
			groupOrder = append(groupOrder, key)
		}
		for _, s := range g.slots {
			if err := s.advance(row, ctx); err != nil {
				return nil, err
			}
		}
	}

	out := make([][]Cell, 0, len(groupOrder))
	syntheticRow := make(Row, len(e.Table.Columns))
	for _, key := range groupOrder {
		g := byKey[key]
		for i := range syntheticRow {
			syntheticRow[i] = Cell{}
		}
		for i, col := range sel.GroupBy {
			syntheticRow[ctx.ColIdx[col]] = g.keyCells[i]
		}
		ctx.AggResults = make(map[*AggregateExpr]EvalCell, len(g.slots))
		for a, s := range g.slotByExpr {
			ctx.AggResults[a] = s.finalize()
		}
		if sel.Having != nil {
			ok, err := Matches(sel.Having, syntheticRow, ctx)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		rowOut := make([]Cell, len(plan))
		for i, p := range plan {
			res, err := EvalExpr(p.Expr, syntheticRow, ctx)
			if err != nil {
				return nil, err
			}
			rowOut[i] = res.Cell
		}
		out = append(out, rowOut)
	}
	return out, nil
}

// applyImplicitHaving filters the single row produced by implicit
// aggregation through a HAVING predicate. The aggregate ctx state is
// already populated by evalImplicitAggregation, so Matches sees the
// finalized values via ctx.AggResults. The HAVING predicate is restricted
// to aggregate expressions (no bare columns); the synthetic row is a
// length-correct placeholder so length-indexed predicates don't panic
// even though they shouldn't reach the row at all.
func (e *Executor) applyImplicitHaving(having Predicate, projected [][]Cell, ctx *EvalContext) ([][]Cell, error) {
	if err := validateAggregatedHaving(having, nil, e.Table.Schema); err != nil {
		return nil, err
	}
	if len(projected) == 0 {
		return projected, nil
	}
	row := make(Row, len(e.Table.Columns))
	ok, err := Matches(having, row, ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return [][]Cell{}, nil
	}
	return projected, nil
}

// validateAggregatedHaving applies the same column-reference rules to a
// HAVING predicate that the projection uses: bare columns must be in the
// allowed (GROUP BY) set; aggregates are validated for argument shape.
// For implicit aggregation, allowed is empty — any bare column produces an
// error. NullTest, LIKE, IN, and BETWEEN bind to bare column names by
// shape, so they require their column to be in the allowed set.
func validateAggregatedHaving(p Predicate, allowed map[string]bool, schema map[string]ColumnInfo) error {
	switch n := p.(type) {
	case *BinaryOp:
		if err := validateAggregatedHaving(n.L, allowed, schema); err != nil {
			return err
		}
		return validateAggregatedHaving(n.R, allowed, schema)
	case *NotOp:
		return validateAggregatedHaving(n.Inner, allowed, schema)
	case *Comparison:
		if err := validateExpr(n.LExpr, schema); err != nil {
			return err
		}
		if bare := bareColumnNotIn(n.LExpr, allowed); bare != "" {
			if len(allowed) == 0 {
				return fmt.Errorf("HAVING: column %q must appear inside an aggregate (no GROUP BY)", bare)
			}
			return fmt.Errorf("HAVING: column %q must appear in GROUP BY or be wrapped in an aggregate", bare)
		}
		for _, a := range collectAggregates(n.LExpr, nil) {
			if err := validateAggregate(a, schema); err != nil {
				return err
			}
		}
		return nil
	case *NullTest:
		return havingRequiresGroupCol(n.Column, allowed)
	case *LikeOp:
		return havingRequiresGroupCol(n.Column, allowed)
	case *InOp:
		return havingRequiresGroupCol(n.Column, allowed)
	case *BetweenOp:
		return havingRequiresGroupCol(n.Column, allowed)
	}
	return fmt.Errorf("internal: unhandled HAVING predicate type %T", p)
}

func havingRequiresGroupCol(col string, allowed map[string]bool) error {
	if !allowed[col] {
		if len(allowed) == 0 {
			return fmt.Errorf("HAVING: column %q can only appear under GROUP BY (no GROUP BY here)", col)
		}
		return fmt.Errorf("HAVING: column %q must appear in GROUP BY", col)
	}
	return nil
}

// collectAllAggregates pulls aggregate nodes from every plan expression and
// the HAVING predicate into one ordered, deduplicated list. The slot table
// allocates one slot per unique AggregateExpr pointer; sharing across
// projection and HAVING avoids accumulating COUNT(*) twice when both
// reference it.
func collectAllAggregates(plan []projEntry, having Predicate) []*AggregateExpr {
	var out []*AggregateExpr
	seen := make(map[*AggregateExpr]bool)
	add := func(a *AggregateExpr) {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, p := range plan {
		for _, a := range collectAggregates(p.Expr, nil) {
			add(a)
		}
	}
	if having != nil {
		for _, a := range collectAggregatesFromPredicate(having, nil) {
			add(a)
		}
	}
	return out
}

// groupKey builds a stable string key from a row's GROUP BY column values,
// using the same encoding scheme as projectedKey so mixed types, NULL
// groups, and string-boundary cases all distinguish cleanly. The returned
// cells are the per-key values in GROUP BY order; the caller stashes them
// on the group so the projection can re-emit them later.
func groupKey(row Row, idx []int, types []ColumnType) (string, []Cell) {
	cells := make([]Cell, len(idx))
	var b strings.Builder
	for i, ci := range idx {
		c := row[ci]
		cells[i] = c
		if c.Null {
			b.WriteString("N|")
			continue
		}
		switch types[i] {
		case TypeInt:
			fmt.Fprintf(&b, "I:%d|", c.Int)
		case TypeFloat:
			fmt.Fprintf(&b, "F:%g|", c.Float)
		case TypeBool:
			fmt.Fprintf(&b, "B:%t|", c.Bool)
		case TypeDate:
			fmt.Fprintf(&b, "D:%d|", c.Date.UnixNano())
		default:
			fmt.Fprintf(&b, "S:%d:%s|", len(c.Str), c.Str)
		}
	}
	return b.String(), cells
}

// evalImplicitAggregation handles SELECT with aggregates and no GROUP BY:
// one slot per unique AggregateExpr pointer, advance once per matched row,
// finalize, then evaluate each projection expression once with the slot
// values injected via ctx.AggResults. The result is always exactly one
// output row, even when no rows matched the WHERE — COUNT(*) returns 0 and
// the other aggregates return NULL. When HAVING is also present, its
// aggregates must share the slot table so the predicate evaluator can
// resolve them by pointer identity.
func (e *Executor) evalImplicitAggregation(plan []projEntry, having Predicate, matched []int, ctx *EvalContext) ([][]Cell, error) {
	slotByExpr := make(map[*AggregateExpr]*aggSlot)
	var slots []*aggSlot
	for _, a := range collectAllAggregates(plan, having) {
		if _, ok := slotByExpr[a]; ok {
			continue
		}
		s, err := newAggSlot(a, e.Table.Schema)
		if err != nil {
			return nil, err
		}
		slotByExpr[a] = s
		slots = append(slots, s)
	}
	for _, idx := range matched {
		row := e.Table.Rows[idx]
		for _, s := range slots {
			if err := s.advance(row, ctx); err != nil {
				return nil, err
			}
		}
	}
	ctx.AggResults = make(map[*AggregateExpr]EvalCell, len(slots))
	for a, s := range slotByExpr {
		ctx.AggResults[a] = s.finalize()
	}
	out := make([]Cell, len(plan))
	for i, p := range plan {
		// The row arg is unused: aggregated projections cannot reach a bare
		// column (planProjection rejected them), so EvalExpr never touches it.
		res, err := EvalExpr(p.Expr, nil, ctx)
		if err != nil {
			return nil, err
		}
		out[i] = res.Cell
	}
	return [][]Cell{out}, nil
}

// applyOffsetLimitRows mirrors applyOffsetLimit but operates on projected
// row slices instead of source-row indices. Slice 3 needs both shapes
// because DISTINCT now runs after projection.
func applyOffsetLimitRows(rows [][]Cell, offset, limit *int) [][]Cell {
	if offset != nil {
		if *offset >= len(rows) {
			return rows[:0]
		}
		rows = rows[*offset:]
	}
	if limit != nil && *limit < len(rows) {
		rows = rows[:*limit]
	}
	return rows
}

// projectedKey builds a dedup key from a projected row's typed cells. Same
// scheme as distinctKey but reads from a positional Cell slice instead of
// a Row keyed by column index.
func projectedKey(pr []Cell, plan []projEntry) string {
	var b strings.Builder
	for i, p := range plan {
		c := pr[i]
		if c.Null {
			b.WriteString("N|")
			continue
		}
		switch p.Type {
		case TypeInt:
			fmt.Fprintf(&b, "I:%d|", c.Int)
		case TypeFloat:
			fmt.Fprintf(&b, "F:%g|", c.Float)
		case TypeBool:
			fmt.Fprintf(&b, "B:%t|", c.Bool)
		case TypeDate:
			fmt.Fprintf(&b, "D:%d|", c.Date.UnixNano())
		default:
			fmt.Fprintf(&b, "S:%d:%s|", len(c.Str), c.Str)
		}
	}
	return b.String()
}

// validateOrderBy rejects sort keys that don't name a column in the table.
// Catching this here avoids a runtime nil deref deep in the comparator.
func (e *Executor) validateOrderBy(keys []OrderKey) error {
	for _, k := range keys {
		if _, ok := e.Table.Schema[k.Column]; !ok {
			return fmt.Errorf("unknown column %q in ORDER BY", k.Column)
		}
	}
	return nil
}

// sortByKeys does an in-place stable sort of row indices by the ORDER BY keys.
// Stability matters: ties on key N preserve the original (input) order, which
// gives users a predictable result. NULLs sort to the high end: last in ASC,
// first in DESC — the Postgres convention.
func (e *Executor) sortByKeys(indices []int, keys []OrderKey, ctx *EvalContext) {
	sort.SliceStable(indices, func(i, j int) bool {
		ra, rb := e.Table.Rows[indices[i]], e.Table.Rows[indices[j]]
		for _, k := range keys {
			ci := ctx.ColIdx[k.Column]
			t := e.Table.Schema[k.Column].Type
			cmp := compareForOrder(ra[ci], rb[ci], t)
			if k.Desc {
				cmp = -cmp
			}
			if cmp != 0 {
				return cmp < 0
			}
		}
		return false
	})
}

// compareForOrder is a NULLs-go-high variant of Compare: a NULL cell is treated
// as the maximum value, so ASC puts NULLs at the bottom of the result and DESC
// puts them at the top. This matches Postgres's default; SQLite goes the other
// way, but Postgres semantics are the more common reference point.
func compareForOrder(a, b Cell, t ColumnType) int {
	if a.Null && b.Null {
		return 0
	}
	if a.Null {
		return 1
	}
	if b.Null {
		return -1
	}
	// Delegate to the existing typed comparator for the non-NULL case.
	return Compare(a, b, t)
}

// planProjection builds the typed projection plan for a SELECT. SELECT *
// synthesizes one entry per table column. Otherwise each user projection
// becomes a plan entry whose label is the alias if present, else the
// expression's source-text rendering. Duplicate output labels are
// rejected — the caller must alias to disambiguate, since the render
// layer keys output rows by label.
//
// Under GROUP BY, every bare column reference must name a GROUP BY column.
// Without GROUP BY but with aggregates anywhere, bare columns are rejected
// outright (Postgres-strict). Each aggregate node is validated up front so
// SUM(Title) and similar fail at plan time, before the row scan.
func (e *Executor) planProjection(sel *SelectStmt) ([]projEntry, error) {
	groupCols := make(map[string]bool, len(sel.GroupBy))
	for _, c := range sel.GroupBy {
		if _, ok := e.Table.Schema[c]; !ok {
			return nil, fmt.Errorf("unknown column %q in GROUP BY", c)
		}
		if groupCols[c] {
			return nil, fmt.Errorf("duplicate column %q in GROUP BY", c)
		}
		groupCols[c] = true
	}
	grouped := len(sel.GroupBy) > 0

	if sel.Star {
		if grouped {
			return nil, fmt.Errorf("SELECT * with GROUP BY is not supported; list the GROUP BY columns explicitly")
		}
		plan := make([]projEntry, len(e.Table.Columns))
		for i, name := range e.Table.Columns {
			plan[i] = projEntry{
				Label: name,
				Type:  e.Table.Schema[name].Type,
				Expr:  &ColumnExpr{Name: name},
			}
		}
		return plan, nil
	}
	anyAgg := false
	for _, pr := range sel.Columns {
		if hasAggregate(pr.Expr) {
			anyAgg = true
			break
		}
	}
	plan := make([]projEntry, 0, len(sel.Columns))
	seen := make(map[string]struct{}, len(sel.Columns))
	for _, pr := range sel.Columns {
		if err := validateExpr(pr.Expr, e.Table.Schema); err != nil {
			return nil, err
		}
		switch {
		case grouped:
			if bare := bareColumnNotIn(pr.Expr, groupCols); bare != "" {
				return nil, fmt.Errorf("column %q must appear in GROUP BY or be wrapped in an aggregate", bare)
			}
		case anyAgg:
			if bare := bareColumn(pr.Expr); bare != "" {
				return nil, fmt.Errorf("column %q must appear inside an aggregate or in GROUP BY", bare)
			}
		}
		if grouped || anyAgg {
			for _, a := range collectAggregates(pr.Expr, nil) {
				if err := validateAggregate(a, e.Table.Schema); err != nil {
					return nil, err
				}
			}
		}
		t, err := exprType(pr.Expr, e.Table.Schema)
		if err != nil {
			return nil, err
		}
		label := pr.Alias
		if label == "" {
			label = renderExpr(pr.Expr)
		}
		if _, dup := seen[label]; dup {
			return nil, fmt.Errorf("duplicate output column %q; use AS to give them distinct names", label)
		}
		seen[label] = struct{}{}
		plan = append(plan, projEntry{Label: label, Type: t, Expr: pr.Expr})
	}
	return plan, nil
}

func (e *Executor) executeUpdate(upd *UpdateStmt, commit bool) error {
	if err := ValidatePredicate(upd.Where, e.Table.Schema); err != nil {
		return err
	}
	if err := e.validateAssignments(upd.Assignments); err != nil {
		return err
	}

	matches, err := e.findMatches(upd.Where)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.Out, "Would update %d row%s in %s:\n", len(matches), plural(len(matches)), e.Table.Path)
	for _, a := range upd.Assignments {
		fmt.Fprintf(e.Out, "  SET %s = %s\n", a.Column, renderExpr(a.Value))
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

	ctx := NewEvalContext(e.Table)
	for _, idx := range matches {
		row := e.Table.Rows[idx]
		// Standard SQL UPDATE semantics: every SET RHS evaluates against the
		// pre-update row, so SET a = b, b = a swaps without using the new
		// value of a. Stage the new cells first, then write them all at once.
		newCells := make(map[string]Cell, len(upd.Assignments))
		for _, a := range upd.Assignments {
			info := e.Table.Schema[a.Column]
			result, err := EvalExpr(a.Value, row, ctx)
			if err != nil {
				return fmt.Errorf("UPDATE column %q: %w", a.Column, err)
			}
			cell, err := coerceEvalCell(result, info.Type, a.Column)
			if err != nil {
				return err
			}
			newCells[a.Column] = cell
		}
		for col, cell := range newCells {
			row[e.colIndex(col)] = cell
		}
	}
	if err := e.flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "Updated %d of %d row%s. Wrote %s.\n", len(matches), len(matches), plural(len(matches)), e.targetPath())
	return nil
}

// validateAssignments checks each SET target column exists, each RHS
// expression references known columns only, and no aggregate slips into
// SET (aggregates are meaningful only in projection / HAVING contexts).
func (e *Executor) validateAssignments(assigns []Assignment) error {
	for _, a := range assigns {
		if _, ok := e.Table.Schema[a.Column]; !ok {
			return fmt.Errorf("unknown column %q", a.Column)
		}
		if err := validateExpr(a.Value, e.Table.Schema); err != nil {
			return err
		}
		if hasAggregate(a.Value) {
			return fmt.Errorf("column %q: aggregates are not allowed in SET", a.Column)
		}
	}
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
		assigns[i] = Assignment{Column: c, Value: &LiteralExpr{Value: ins.Values[i]}}
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

// buildAssignmentCells is the INSERT-side helper for evaluating literal
// assignments once and reusing the result. UPDATE uses a per-row eval path
// in executeUpdate because computed RHSes depend on the source row. Only
// LiteralExpr is reachable here because executeInsert wraps each input
// Value in a LiteralExpr before calling.
func (e *Executor) buildAssignmentCells(assigns []Assignment) (map[string]Cell, error) {
	cells := make(map[string]Cell, len(assigns))
	for _, a := range assigns {
		info, ok := e.Table.Schema[a.Column]
		if !ok {
			return nil, fmt.Errorf("unknown column %q", a.Column)
		}
		lit, ok := a.Value.(*LiteralExpr)
		if !ok {
			return nil, fmt.Errorf("internal: INSERT requires literal values")
		}
		c, err := CoerceLiteral(lit.Value, info.Type)
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

// renderExpr formats an expression as readable SQL text for write previews.
// Binary children are parenthesized when their op has lower precedence than
// the parent, so the preview reflects user intent even after the parser
// flattens precedence into the tree shape.
func renderExpr(e Expr) string {
	return renderExprPrec(e, 0)
}

func renderExprPrec(e Expr, parentPrec int) string {
	switch n := e.(type) {
	case *LiteralExpr:
		return renderLiteral(n.Value)
	case *ColumnExpr:
		return n.Name
	case *BinaryExpr:
		prec := opPrec(n.Op)
		s := renderExprPrec(n.L, prec) + " " + n.Op + " " + renderExprPrec(n.R, prec)
		if prec < parentPrec {
			s = "(" + s + ")"
		}
		return s
	case *AggregateExpr:
		if n.Star {
			return n.Func + "(*)"
		}
		return n.Func + "(" + renderExpr(n.Arg) + ")"
	}
	return "?"
}

func opPrec(op string) int {
	switch op {
	case "+", "-":
		return 1
	case "*", "/":
		return 2
	}
	return 0
}
