package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterh/liner"
)

// configDir returns ~/.config/sqlcsv, where REPL history lives.
func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "sqlcsv")
}

// runREPL drives the interactive prompt: read a line (with arrow-key history
// and inline editing via peterh/liner), classify it as a meta-command or SQL
// statement, dispatch, repeat. Returns nil on clean EOF (^D) or quit, an
// error on unrecoverable read failure.
//
// While running it wires exec.Confirm to reuse the same liner.State for the
// y/N confirmation, so the user sees consistent editing behavior at every
// prompt.
func runREPL(exec *Executor) error {
	line := liner.NewLiner()
	defer line.Close()
	line.SetCtrlCAborts(true)

	historyPath := filepath.Join(configDir(), "history")
	loadHistory(line, historyPath)
	defer saveHistory(line, historyPath)

	exec.Confirm = func() bool {
		ans, err := line.Prompt("Apply? [y/N]: ")
		if err != nil {
			return false
		}
		ans = strings.ToLower(strings.TrimSpace(ans))
		return ans == "y" || ans == "yes"
	}

	fmt.Fprintln(os.Stderr, `sqlcsv REPL — type "help" for commands, "quit" to exit.`)

	for {
		input, err := line.Prompt("sqlcsv> ")
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(os.Stderr)
			return nil
		}
		if errors.Is(err, liner.ErrPromptAborted) {
			continue
		}
		if err != nil {
			return err
		}

		trimmed := strings.TrimSpace(input)
		if trimmed == "" {
			continue
		}
		line.AppendHistory(trimmed)

		switch classifyMeta(trimmed) {
		case metaCmdQuit:
			return nil
		case metaCmdHelp:
			printHelp(exec.Out)
			continue
		case metaCmdDescribe:
			if err := describeSchema(exec); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			continue
		case metaCmdRefresh:
			if err := refreshTable(exec); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			continue
		}

		cleaned, commit := PreProcess(trimmed)
		if cleaned == "" {
			continue
		}
		stmt, perr := Parse(cleaned)
		if perr != nil {
			printParseError(os.Stderr, cleaned, perr)
			continue
		}
		if err := exec.Execute(stmt, commit); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}
}

func loadHistory(line *liner.State, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = line.ReadHistory(f)
}

func saveHistory(line *liner.State, path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = line.WriteHistory(f)
}

type metaCmd int

const (
	metaCmdNone metaCmd = iota
	metaCmdQuit
	metaCmdHelp
	metaCmdDescribe
	metaCmdRefresh
)

func classifyMeta(line string) metaCmd {
	cmd := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";"))
	switch strings.ToUpper(cmd) {
	case "QUIT", "EXIT":
		return metaCmdQuit
	case "HELP", "?":
		return metaCmdHelp
	case "DESCRIBE":
		return metaCmdDescribe
	case "REFRESH":
		return metaCmdRefresh
	}
	return metaCmdNone
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `Statements (one per line; trailing ';' optional):
  SELECT [* | col1, col2, ...] [WHERE pred]
  UPDATE SET col = val [, col = val ...] [WHERE pred]
  DELETE [WHERE pred]
  INSERT (col1, col2, ...) VALUES (val1, val2, ...)

Writes preview by default and prompt "Apply? [y/N]" before committing.
Append '!' to skip the prompt and commit immediately
(e.g. "DELETE WHERE Status = 'Archived' !").

Meta-commands (case-insensitive):
  quit, exit         Exit the REPL.
  help, ?            This help.
  describe           Print the bound CSV's columns and inferred types.
  refresh            Re-read the bound CSV from disk (discarding unsaved edits).`)
}

// describeSchema prints the bound CSV's columns and inferred types.
func describeSchema(exec *Executor) error {
	rows := make([]map[string]any, 0, len(exec.Table.Columns))
	for _, name := range exec.Table.Columns {
		info := exec.Table.Schema[name]
		rows = append(rows, map[string]any{
			"name": info.Name,
			"type": info.Type.String(),
		})
	}
	return Render(exec.Out, Result{
		Columns: []string{"name", "type"},
		Rows:    rows,
	}, exec.Format)
}

// refreshTable re-reads the bound CSV from disk in case it changed externally.
// Dialect and type hints from the original load are preserved.
func refreshTable(exec *Executor) error {
	hints := make(map[string]ColumnType, len(exec.Table.Schema))
	for name, info := range exec.Table.Schema {
		hints[name] = info.Type
	}
	opts := LoadOptions{
		Delim:     exec.Table.Delim,
		NoHeader:  !exec.Table.HasHeader,
		TypeHints: hints,
	}
	t, err := LoadCSV(exec.Table.Path, opts)
	if err != nil {
		return err
	}
	exec.Table = t
	fmt.Fprintf(exec.Out, "Refreshed %s: %d columns, %d rows.\n", t.Path, len(t.Columns), len(t.Rows))
	return nil
}

func printParseError(out io.Writer, input string, err error) {
	pe, ok := err.(*ParseError)
	if !ok {
		fmt.Fprintf(out, "Parse error: %v\n", err)
		return
	}
	fmt.Fprintf(out, "Parse error: %s\n", pe.Msg)
	fmt.Fprintf(out, "  %s\n", input)
	if pe.Pos >= 0 && pe.Pos <= len(input) {
		fmt.Fprintf(out, "  %s^\n", strings.Repeat(" ", pe.Pos))
	}
}
