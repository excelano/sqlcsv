# sqlcsv (archived)

**sqlcsv has been superseded by [xql](https://github.com/excelano/xql).** xql is one CLI that runs the same SQL-shaped grammar against pluggable backends; its `csv` backend ships with full sqlcsv v2.0 parity. Existing sqlcsv scripts and commands work against `xql csv` unchanged.

## Migrate

On Debian or Ubuntu, swap through the Excelano apt repository:

```sh
sudo apt install xql
sudo apt remove sqlcsv   # whenever you're ready
```

Prebuilt binary (Linux and macOS, x86_64 and arm64):

```
curl -fsSL https://raw.githubusercontent.com/excelano/xql/main/install.sh | sh
```

From source (Go 1.24 or later):

```
go install github.com/excelano/xql/cmd/xql@latest
```

## Command translation

| Old | New |
|-----|-----|
| `sqlcsv data.csv` | `xql data.csv` (or `xql csv data.csv`) |
| `sqlcsv data.csv --exec "..."` | `xql csv data.csv --exec "..."` |
| `~/.config/sqlcsv/history` | `~/.config/xql/history-csv` |

REPL history does not migrate automatically. Copy `~/.config/sqlcsv/history` to `~/.config/xql/history-csv` if you want to carry it across.

## What stays here

This repository is archived but the history, tags, and release artifacts remain accessible for reference. The SQL grammar lives at [xql/GRAMMAR.md](https://github.com/excelano/xql) (forthcoming) — the v2.0 grammar from this repo carries forward unchanged.

## License

MIT — see [LICENSE](LICENSE).
