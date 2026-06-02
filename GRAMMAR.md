# sqlcsv Grammar (v2.0-alpha)

The formal grammar for the SQL subset that `sqlcsv` accepts in its REPL. Anything outside this grammar produces a clear parse error pointing at the unsupported construct, rather than silently misinterpreting input.

This grammar is identical to [spsql's grammar](https://github.com/excelano/spsql/blob/main/GRAMMAR.md). The two tools share parser code so that one mental model covers both.

**v2.0-alpha note.** This release ships the v2 grammar in full but the executor still runs the v1 feature set. Arithmetic projections, aggregates, `GROUP BY`, `HAVING`, `AS` aliases, and computed `SET` assignments all parse cleanly, then fail at execution with a clear "lands in v2.0" message. The intent is to publish the grammar surface for review while the executor work continues. Queries that fit the v1 shape execute exactly as they did in v1.x.

## Notation

This document uses a compact EBNF-style notation. `:=` defines a rule. `|` separates alternatives. `( )` groups. `( )?` is optional. `( )*` is zero or more repetitions. `( )+` is one or more. Terminal strings appear in double quotes. Character sets use `[...]` and negation `[^...]`. Whitespace between tokens is significant only as a separator and otherwise ignored.

## Statements

```ebnf
statement     := select_stmt | update_stmt | delete_stmt | insert_stmt

select_stmt   := "SELECT" "DISTINCT"? projection_list
                 ( "WHERE" predicate )?
                 ( "GROUP" "BY" column ( "," column )* )?
                 ( "HAVING" predicate )?
                 ( "ORDER" "BY" sort_key ( "," sort_key )* )?
                 ( "LIMIT" integer )?
                 ( "OFFSET" integer )?

sort_key      := column ( "ASC" | "DESC" )?

update_stmt   := "UPDATE" "SET" assignment ( "," assignment )*
                 ( "WHERE" predicate )?

delete_stmt   := "DELETE" ( "WHERE" predicate )?

insert_stmt   := "INSERT" "(" column ( "," column )* ")"
                 "VALUES" "(" value ( "," value )* ")"
```

Note the absence of `FROM` (SELECT and DELETE) and target list names (UPDATE, INSERT). The bound file is implicit; each REPL session operates on one CSV selected at startup.

Clauses must appear in the order shown. Execution order is `WHERE` → `GROUP BY` → `HAVING` → `DISTINCT` → `ORDER BY` → `OFFSET` → `LIMIT`, which is the standard SQL pipeline.

## Projection and Assignment

```ebnf
projection_list := "*" | projection ( "," projection )*
projection      := expr ( "AS" identifier )?
assignment      := column "=" expr
```

`SELECT *` returns every column in the file. A projection list evaluates each projection per row and returns the results in user order.

A bare column name (`SELECT Title`) projects that column unchanged. An arithmetic expression (`SELECT price * qty`) computes a value per row. An aggregate (`SELECT COUNT(*)`) folds across the row partition produced by `GROUP BY`, or across all matching rows if `GROUP BY` is absent. An optional `AS` clause renames the projection in the result header.

`UPDATE SET col = expr` allows the right-hand side to reference other columns of the row being updated, so `SET counter = counter + 1` works as expected.

`SELECT DISTINCT` collapses rows that have identical values across the projected columns. Deduplication runs after `WHERE` and `GROUP BY`, on the typed values. Two `NULL`s in the same projected column are considered equal for deduplication, matching standard SQL.

`ORDER BY` sorts rows by one or more keys. Each key is a column name with an optional `ASC` (default) or `DESC` direction. Expression keys (`ORDER BY price * qty`) and alias references (`ORDER BY n` referring to an `AS n` projection) are not part of v2.0 and are planned for v2.1.

`LIMIT n` takes at most the first n rows of the result, and `OFFSET m` skips the first m rows. Both require a non-negative integer literal; floats and negatives are parse errors.

## Expressions

```ebnf
expr          := term ( ( "+" | "-" ) term )*
term          := factor ( ( "*" | "/" ) factor )*
factor        := column | literal | aggregate | "(" expr ")"

aggregate     := "COUNT" "(" "*" ")"
               | ( "COUNT" | "SUM" | "AVG" | "MIN" | "MAX" ) "(" expr ")"

literal       := number | string | "TRUE" | "FALSE" | "NULL"
```

Multiplication and division bind tighter than addition and subtraction. Parentheses override precedence inside an expression.

`COUNT`, `SUM`, `AVG`, `MIN`, and `MAX` are recognized as aggregates only when followed by `(`. Anywhere else they parse as bare identifiers, so a column literally named `min` or `count` can still be projected without quoting.

Aggregates may not nest in standard SQL (`SUM(COUNT(*))` is undefined). The parser accepts the shape and the executor rejects it; this keeps the grammar straightforward.

## Predicates

```ebnf
predicate     := disjunction
disjunction   := conjunction ( "OR" conjunction )*
conjunction   := negation ( "AND" negation )*
negation      := "NOT" negation | atom
atom          := comparison | null_test | like_test | in_test | between_test
               | "(" predicate ")"

comparison    := expr op value
op            := "=" | "!=" | "<" | ">" | "<=" | ">="
null_test     := column "IS" "NOT"? "NULL"
like_test     := column "NOT"? ( "LIKE" | "ILIKE" ) string
in_test       := column "NOT"? "IN" "(" value ( "," value )* ")"
between_test  := column "NOT"? "BETWEEN" value "AND" value
```

Operator precedence, from lowest to highest, is `OR`, `AND`, `NOT`. `NOT` is right-associative. Parentheses at the start of a predicate atom group a predicate (`WHERE (A = 1 OR B = 2) AND C = 3`); parentheses inside an expression group an expression (`SELECT (a + b) * c`).

Comparisons accept a full expression on the left and a literal on the right. `WHERE price * qty > 100` and `HAVING COUNT(*) > 5` use the same comparison shape. `col1 = col2` is still not supported; the right side is always a literal.

`IS NULL`, `LIKE`, `ILIKE`, `IN`, and `BETWEEN` constrain the left side to a bare column reference. Expression LHSs (`WHERE (a + b) IS NULL`) are a parse error.

## Columns and Values

```ebnf
column            := identifier | quoted_identifier
identifier        := letter ( letter | digit | "_" )*
quoted_identifier := '"' ( [^"] | '""' )* '"'

value             := string | number | "TRUE" | "FALSE" | "NULL"
string            := "'" ( [^'] | "''" )* "'"
number            := "-"? digit+ ( "." digit+ )?

letter            := "A".."Z" | "a".."z"
digit             := "0".."9"
```

Inside a quoted identifier, escape a double quote by doubling it (`""`). Inside a string literal, escape a single quote by doubling it (`''`). These match standard SQL.

## Semantics notes

Keywords (`SELECT`, `UPDATE`, `WHERE`, `AND`, `GROUP`, `HAVING`, `AS`, etc.) are case-insensitive. Identifiers (column names) are case-sensitive and must match a column's header in the bound CSV file. Column names containing spaces, punctuation, or non-ASCII characters must be quoted with double quotes. If the file was loaded with `--no-header`, columns are named `col1`, `col2`, and so on.

String literals are coerced to the destination column's inferred type at execution time. Numeric columns parse integers and floats. Date columns parse ISO 8601 (`'2024-01-01'` or `'2024-01-01T12:00:00Z'`). Boolean columns accept the literals `TRUE` / `FALSE` (case-insensitive) or any of the strings `'true'`, `'false'`, `'1'`, `'0'`, `'yes'`, `'no'`. A coercion failure (for example, writing `'abc'` to an int column) is a runtime error and the write does not apply.

Empty CSV cells are treated as `NULL` for the purposes of `IS NULL` and `IS NOT NULL`. Only those tests work on `NULL`; `col = NULL` is a parse error, since `=` with `NULL` is always undefined in SQL.

`LIKE` matches a string column against a pattern. `%` matches zero or more characters; `_` matches exactly one. A backslash escapes the next character. `LIKE` only works on string columns. `NOT LIKE` negates the match. A NULL cell makes the result UNKNOWN, which excludes the row.

`ILIKE` is the case-insensitive form of `LIKE`. Both the pattern and the column value are folded to lowercase before matching.

`IN` tests for set membership. The value list must be non-empty and contain only literals. `NOT IN` negates the match. NULL on the column side excludes the row; NULL inside the list is a parse error.

`BETWEEN` is inclusive on both bounds. Bounds must be literal values, not NULL.

Statements are terminated by end of input. A trailing semicolon is accepted but not required.

## Comments

Two comment styles are accepted anywhere whitespace is legal. **Line comments** start with `--` and run to the next newline (or end of input). **Block comments** are delimited by `/*` and `*/` and may span multiple lines. Block comments do not nest, matching ANSI SQL.

Comments are ignored as if they were whitespace. Inside a string literal, `--` and `/* */` are plain characters with no special meaning.

## REPL pre-processing

Before SQL reaches the parser, the REPL and `--exec` mode strip two trailing tokens if present. Neither is part of the grammar above.

A trailing `;` is stripped silently. Multi-statement input is not supported; one statement per line.

A trailing `!` is stripped and recorded as a "skip prompt" signal for write statements. In REPL mode, `INSERT`, `UPDATE`, and `DELETE` normally print a preview and ask `Apply? [y/N]:` before committing. The `!` suffix skips the prompt and commits immediately. On `SELECT`, the suffix is silently accepted but has no effect. In `--exec` mode the suffix is rejected with an error pointing the user toward `--commit`.

## Examples

Valid statements under the v2.0-alpha grammar. Those marked **(executes in v1.x mode)** also run today; those marked **(v2.0 executor)** parse cleanly but error at execution until v2.0 ships.

```sql
-- v1 shape (executes in v1.x mode)
SELECT Title, Status WHERE Priority > 2
SELECT DISTINCT Status WHERE Archived = FALSE
SELECT Title WHERE DueDate IS NULL ORDER BY Modified DESC LIMIT 10
UPDATE SET Status = 'Done' WHERE ID = 42

-- v2 grammar (v2.0 executor)
SELECT Title AS t, Priority AS p
SELECT price * qty AS line_total
SELECT COUNT(*) AS n
SELECT Status, COUNT(*) AS n GROUP BY Status
SELECT Status, AVG(price) GROUP BY Status HAVING AVG(price) > 50
UPDATE SET counter = counter + 1 WHERE id = 7
SELECT * WHERE price * qty > 100
```

## Out of scope

Permanently out of scope: `JOIN` of any form. sqlcsv operates on a single file per session by design. To combine data across files, run a SELECT against each, redirect to a new CSV, and load it.

Planned but not in v2.0: `ORDER BY` with expressions or alias references, `GROUP BY` with expressions, `COUNT(DISTINCT col)`, and scalar functions (`LOWER`, `UPPER`, `YEAR`, etc.).

No current plan: subqueries, `UNION` / `INTERSECT` / `EXCEPT`, and common table expressions. None are technically impossible, but each adds parser complexity for a use case that has not surfaced yet.
