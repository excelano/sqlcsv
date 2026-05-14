# sqlcsv Grammar (v1)

The formal grammar for the SQL subset that `sqlcsv` accepts in its REPL. Anything outside this grammar produces a clear parse error pointing at the unsupported construct, rather than silently misinterpreting input.

This grammar is identical to [spsql's grammar](https://github.com/excelano/spsql/blob/main/GRAMMAR.md). The two tools share parser code so that one mental model covers both.

## Notation

This document uses a compact EBNF-style notation. `:=` defines a rule. `|` separates alternatives. `( )` groups. `( )?` is optional. `( )*` is zero or more repetitions. `( )+` is one or more. Terminal strings appear in double quotes. Character sets use `[...]` and negation `[^...]`. Whitespace between tokens is significant only as a separator and otherwise ignored.

## Statements

```ebnf
statement     := select_stmt | update_stmt | delete_stmt | insert_stmt

select_stmt   := "SELECT" projection ( "WHERE" predicate )?

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

## Predicates

```ebnf
predicate     := disjunction
disjunction   := conjunction ( "OR" conjunction )*
conjunction   := negation ( "AND" negation )*
negation      := "NOT" negation | atom
atom          := comparison | null_test | "(" predicate ")"

comparison    := column op value
op            := "=" | "!=" | "<" | ">" | "<=" | ">="
null_test     := column "IS" "NOT"? "NULL"
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
SELECT Title WHERE Status = 'Open'
SELECT Title WHERE Status = 'Open' AND Priority > 2
SELECT Title WHERE (Status = 'Open' OR Status = 'In Review') AND NOT Archived = TRUE
SELECT Title WHERE DueDate IS NULL
SELECT Title WHERE DueDate IS NOT NULL AND Modified < '2024-01-01'

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

Planned for v1.1: `LIKE` for substring matching, `IN` for set membership, `BETWEEN` for range tests, `ORDER BY`, `LIMIT`, and `OFFSET`.

Planned for v2, requiring extra work: aggregates (`COUNT`, `SUM`, `AVG`, `MIN`, `MAX`), `GROUP BY`, `HAVING`, `DISTINCT`, and computed assignments like `SET col = col + 1`.

No current plan: subqueries, scalar functions (`LOWER`, `UPPER`, `YEAR`, etc.), `AS` aliases, `UNION` / `INTERSECT` / `EXCEPT`, common table expressions, and SQL comments. None are technically impossible, but each adds parser complexity for a use case that has not surfaced yet.
