# sqlcsv

A SQL REPL for CSV files. Bind to a single CSV at startup, then run SELECT, UPDATE, DELETE, and INSERT against it. Writes preview first, then apply on confirmation.

## Example

```
$ sqlcsv tasks.csv
Connected to: tasks.csv (5 columns, 248 rows)
sqlcsv REPL — type "help" for commands, "quit" to exit.

sqlcsv> SELECT Title, Status WHERE Priority > 2
| Title              | Status      |
| ------------------ | ----------- |
| Migrate auth layer | Open        |
| Backfill activity  | In Progress |
(2 rows)

sqlcsv> UPDATE SET Status = 'Done' WHERE Modified < '2024-01-01'
Would update 8 rows in tasks.csv:
  SET Status = "Done"
Sample:
| id | Title              |
| -- | ------------------ |
| 41 | Q3 invoice cleanup |
| 47 | Audit log purge    |
  ... 6 more
Apply? [y/N]: y
Updated 8 of 8 rows. Wrote tasks.csv.
```

## Why

CSV files are everywhere, and editing them in bulk is awkward. Spreadsheet apps choke past a few hundred thousand rows, sed and awk are powerful but unforgiving, and writing a script for each one-off transform is overkill. sqlcsv is the smallest tool that lets you write one SQL statement, see what it would change, commit if it is right.

It is a sibling to [spsql](https://github.com/excelano/spsql), which does the same thing for SharePoint Lists. Same grammar, same preview-and-apply flow, no auth and no network.

## Install

Prebuilt binary (Linux and macOS, x86_64 and arm64):

```
curl -fsSL https://raw.githubusercontent.com/excelano/sqlcsv/main/install.sh | sh
```

If the installer needs to write to a root-owned directory like `/usr/local/bin` (typical when upgrading a previously sudo-installed copy), wrap `sh`, not `curl`:

```
curl -fsSL https://raw.githubusercontent.com/excelano/sqlcsv/main/install.sh | sudo sh
```

Pin to a specific version:

```
SQLCSV_VERSION=v0.1.1 curl -fsSL https://raw.githubusercontent.com/excelano/sqlcsv/main/install.sh | sh
```

Install elsewhere than `/usr/local/bin` (or `~/.local/bin` if not writable):

```
SQLCSV_INSTALL_DIR=$HOME/bin curl -fsSL https://raw.githubusercontent.com/excelano/sqlcsv/main/install.sh | sh
```

From source (Go 1.24 or later):

```
go install github.com/excelano/sqlcsv@latest
```

### Upgrade

Re-run the installer. If sqlcsv is already on your `PATH`, it upgrades the existing copy in place rather than scattering a duplicate into the default directory. If you explicitly set `SQLCSV_INSTALL_DIR` to a different directory than the existing copy, the installer warns and leaves both in place — `PATH` order then decides which version runs.

### Uninstall

```
curl -fsSL https://raw.githubusercontent.com/excelano/sqlcsv/main/uninstall.sh | sh
```

The uninstaller removes the `sqlcsv` binary it finds on `PATH` and asks before removing `~/.config/sqlcsv/` (REPL history). Run twice if you have duplicate installs in multiple directories. Skip the prompts with `SQLCSV_UNINSTALL_YES=1`; also drop the config dir with `SQLCSV_PURGE=1`.

## Usage

### Interactive REPL

```
sqlcsv <path>
```

Opens a prompt bound to the file. Arrow keys recall history, Ctrl-R searches it, Ctrl-D exits. History persists at `~/.config/sqlcsv/history` across sessions.

The REPL accepts SQL statements one per line plus a few meta-commands as plain words (case-insensitive): `help` or `?` shows command help, `describe` prints the column schema with inferred types, `refresh` re-reads the file from disk, and `quit` or `exit` leaves the REPL.

Writes (INSERT, UPDATE, DELETE) preview by default. sqlcsv prints the affected count, a sample of the rows that match, and then prompts `Apply? [y/N]:`. Anything but `y` cancels. Append `!` to skip the prompt and commit immediately:

```sql
UPDATE SET Status = 'Done' WHERE Modified < '2024-01-01' !
```

When a write is applied, sqlcsv rewrites the bound file. Pass `--output FILE` at startup to write to a different file instead.

### One-shot mode

```
sqlcsv <path> --exec "<sql>"
```

Runs one statement and exits. Writes need `--commit`; a bare DELETE (no WHERE clause) additionally needs `--confirm-destructive`. Output auto-detects to ASCII table on an interactive terminal and TSV when piped. Override with `--format=json` for JSON, useful for scripts that consume the results.

### CSV dialect

By default, sqlcsv expects a header row, comma delimiter, double-quote quoting, and UTF-8. Override with:

- `--no-header` — file has no header; columns are named `col1`, `col2`, ...
- `--delim CHAR` — single-character delimiter other than `,` (use `\t` for tab)
- `--quote CHAR` — single-character quote character other than `"`

### Type inference

sqlcsv samples the first 1024 rows and infers a type per column: `int`, `float`, `bool`, `date`, or `string`. Comparisons use the inferred type, so `Priority > 2` does numeric compare and `Modified < '2024-01-01'` does date compare. The `describe` command shows what was inferred. Override at startup with `--type Name=string,Priority=int` if inference picks wrong.

## SQL subset

sqlcsv implements the same deliberately small SQL grammar as spsql: SELECT and DML with literal values, simple WHERE predicates, no JOINs, no subqueries. See [GRAMMAR.md](GRAMMAR.md) for the full specification.

## Security

sqlcsv runs locally and only touches files your OS user already has access to. No network calls, no telemetry. See [SECURITY.md](SECURITY.md) for the full policy and the vulnerability reporting process.

## License

MIT. See [LICENSE](LICENSE).
