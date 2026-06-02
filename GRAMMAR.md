# sqlcsv Grammar (v1)

The formal grammar for the SQL subset that `sqlcsv` accepts in its REPL. Anything outside this grammar produces a clear parse error pointing at the unsupported construct, rather than silently misinterpreting input.

This grammar is identical to [spsql's grammar](https://github.com/excelano/spsql/blob/main/GRAMMAR.md). The two tools share parser code so that one mental model covers both.

## Notation

This document uses a compact EBNF-style notation. `:=` defines a rule. `|` separates alternatives. `( )` groups. `( )?` is optional. `( )*` is zero or more repetitions. `( )+` is one or more. Terminal strings appear in double quotes. Character sets use `[...]` and negation `[^...]`. Whitespace between tokens is significant only as a separator and otherwise ignored.

## Statements

```ebnf
statement     := select_stmt | update_stmt | delete_stmt | insert_stmt

select_stmt   := "SELECT" "DISTINCT"? projection
                 ( "WHERE" predicate )?
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

## Projection and Assignment

```ebnf
projection    := "*" | column ( "," column )*
assignment    := column "=" value
```

`SELECT *` returns every column in the file. A column list returns only those columns in the order given.

`SELECT DISTINCT` collapses rows that have identical values across the projected columns. Deduplication runs after `WHERE`, on the typed values (so an integer `1` and the string `"1"` from different columns are not treated as equal). Two `NULL`s in the same projected column are considered equal for the purpose of deduplication, matching standard SQL.

`ORDER BY` sorts rows by one or more keys. Each key is a column name with an optional `ASC` (default) or `DESC` direction. Sort comparisons use the column's inferred type — integers compare as numbers, dates as dates, strings byte-wise. The sort is stable: rows tied on every key keep their original relative order. `NULL` values sort to the high end — last in `ASC`, first in `DESC` — following the Postgres convention.

`LIMIT n` takes at most the first n rows of the result, and `OFFSET m` skips the first m rows. Both require a non-negative integer literal; floats and negatives are parse errors. `OFFSET` can stand alone without `LIMIT`. `LIMIT 0` is legal and returns no rows.

Clauses must appear in the order: `WHERE`, `ORDER BY`, `LIMIT`, `OFFSET`. Execution order is `WHERE` → `DISTINCT` → `ORDER BY` → `OFFSET` → `LIMIT`, which is the standard SQL pipeline.

## Predicates

```ebnf
predicate     := disjunction
disjunction   := conjunction ( "OR" conjunction )*
conjunction   := negation ( "AND" negation )*
negation      := "NOT" negation | atom
atom          := comparison | null_test | like_test | in_test | between_test | "(" predicate ")"

comparison    := column op value
op            := "=" | "!=" | "<" | ">" | "<=" | ">="
null_test     := column "IS" "NOT"? "NULL"
like_test     := column "NOT"? "LIKE" string
in_test       := column "NOT"? "IN" "(" value ( "," value )* ")"
between_test  := column "NOT"? "BETWEEN" value "AND" value
```

Operator precedence, from lowest to highest, is `OR`, `AND`, `NOT`. `NOT` is right-associative. Parentheses override precedence.

Comparisons are only between a column and a literal value. `col1 = col2` is not allowed in v1.

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

Keywords (`SELECT`, `UPDATE`, `WHERE`, `AND`, etc.) are case-insensitive. Identifiers (column names) are case-sensitive and must match a column's header in the bound CSV file. Column names containing spaces, punctuation, or non-ASCII characters must be quoted with double quotes. If the file was loaded with `--no-header`, columns are named `col1`, `col2`, and so on.

String literals are coerced to the destination column's inferred type at execution time. Numeric columns parse integers and floats. Date columns parse ISO 8601 (`'2024-01-01'` or `'2024-01-01T12:00:00Z'`). Boolean columns accept the literals `TRUE` / `FALSE` (case-insensitive) or any of the strings `'true'`, `'false'`, `'1'`, `'0'`, `'yes'`, `'no'`. A coercion failure (for example, writing `'abc'` to an int column) is a runtime error and the write does not apply.

Empty CSV cells are treated as `NULL` for the purposes of `IS NULL` and `IS NOT NULL`. Only those tests work on `NULL`; `col = NULL` is a parse error, since `=` with `NULL` is always undefined in SQL.

`LIKE` matches a string column against a pattern. `%` matches zero or more characters; `_` matches exactly one. A backslash escapes the next character (`\%` matches a literal `%`, `\_` a literal `_`). LIKE only works on string columns; running it against a numeric, date, or boolean column is a clear error rather than a silent coercion. `NOT LIKE` negates the match. A NULL cell makes the result UNKNOWN, which excludes the row, matching standard SQL.

`IN` tests for set membership: `col IN (v1, v2, v3)`. The value list must be non-empty and must contain only literals — sub-queries are not supported. Values are coerced to the column's type, so `Priority IN (1, 2, 3)` works against an integer column and `Status IN ('Open', 'Done')` against a string column. `NOT IN` negates the match. NULL on the column side excludes the row; NULL inside the list is a parse error.

`BETWEEN` is inclusive on both bounds: `col BETWEEN low AND high` is equivalent to `col >= low AND col <= high`. Bounds must be literal values, not NULL, and are coerced to the column's type. `NOT BETWEEN` negates the match. NULL on the column side excludes the row.

Statements are terminated by end of input. A trailing semicolon is accepted but not required.

## REPL pre-processing

Before SQL reaches the parser, the REPL and `--exec` mode strip two trailing tokens if present. Neither is part of the grammar above.

A trailing **`;`** is stripped silently. Multi-statement input is not supported in v1; one statement per line.

A trailing **`!`** is stripped and recorded as a "skip prompt" signal for write statements. In REPL mode, `INSERT`, `UPDATE`, and `DELETE` normally print a preview and ask `Apply? [y/N]:` before committing. The `!` suffix skips the prompt and commits immediately. On `SELECT`, the suffix is silently accepted but has no effect. In `--exec` mode the suffix is rejected with an error pointing the user toward `--commit`.

## Examples

Valid statements under the v1 grammar:

```sql
SELECT *
SELECT Title, Status, "Created Date"
SELECT DISTINCT Status
SELECT DISTINCT Status, Priority WHERE Archived = FALSE
SELECT Title WHERE Status = 'Open' ORDER BY Modified DESC
SELECT Title WHERE Status = 'Open' ORDER BY Priority DESC, Modified ASC
SELECT Title ORDER BY Modified DESC LIMIT 10
SELECT Title ORDER BY ID LIMIT 25 OFFSET 50
SELECT Title WHERE Status = 'Open'
SELECT Title WHERE Status = 'Open' AND Priority > 2
SELECT Title WHERE (Status = 'Open' OR Status = 'In Review') AND NOT Archived = TRUE
SELECT Title WHERE DueDate IS NULL
SELECT Title WHERE DueDate IS NOT NULL AND Modified < '2024-01-01'
SELECT Title WHERE Title LIKE 'Fix%'
SELECT Title WHERE Title NOT LIKE '%draft%'
SELECT Title WHERE Status IN ('Open', 'In Progress')
SELECT Title WHERE Priority NOT IN (4, 5)
SELECT Title WHERE Priority BETWEEN 1 AND 3
SELECT Title WHERE Modified BETWEEN '2024-01-01' AND '2024-06-30'

UPDATE SET Status = 'Done' WHERE ID = 42
UPDATE SET Status = 'Done', Priority = 1 WHERE Status = 'Open'

DELETE
DELETE WHERE Status = 'Archived'

INSERT (Title, Status) VALUES ('New project', 'Open')
INSERT (Title, Status, Priority) VALUES ('Migration', 'Open', 3)
```

`DELETE` with no `WHERE` deletes every row in the file. This is intentional and follows SQL semantics; the dry-run safety mechanism prevents accidents in practice.

## Out of scope for v1

The grammar deliberately excludes most of SQL. Each excluded construct produces a parse error that names the unsupported feature, so unsupported queries fail fast and obviously.

Permanently out of scope: `JOIN` of any form. sqlcsv operates on a single file per session by design. To combine data across files, run a SELECT against each, redirect to a new CSV, and load it.

The v1 grammar is now feature-complete relative to the original v1.x scope.

Planned for v2, requiring extra work: aggregates (`COUNT`, `SUM`, `AVG`, `MIN`, `MAX`), `GROUP BY`, `HAVING`, and computed assignments like `SET col = col + 1`.

No current plan: subqueries, scalar functions (`LOWER`, `UPPER`, `YEAR`, etc.), `AS` aliases, `UNION` / `INTERSECT` / `EXCEPT`, common table expressions, and SQL comments. None are technically impossible, but each adds parser complexity for a use case that has not surfaced yet.
