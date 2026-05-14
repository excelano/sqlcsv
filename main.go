package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

var (
	flagCSV      = flag.String("csv", "", "CSV file path (required)")
	flagExec     = flag.String("exec", "", "Run one SQL statement and exit (non-REPL mode)")
	flagFormat   = flag.String("format", "", "Output format: table | tsv | json (auto-detected if blank)")
	flagCommit   = flag.Bool("commit", false, "Commit writes in --exec mode (required for INSERT/UPDATE/DELETE)")
	flagConfirm  = flag.Bool("confirm-destructive", false, "Required for bare DELETE in --exec mode")
	flagOutput   = flag.String("output", "", "Write committed changes to this path instead of the bound CSV")
	flagNoHeader = flag.Bool("no-header", false, "CSV has no header row; columns are named col1, col2, ...")
	flagDelim    = flag.String("delim", ",", "Single-character field delimiter (use \\t for tab)")
	flagTypes    = flag.String("type", "", "Comma-separated column type overrides, e.g. Priority=int,Tags=string")
)

func main() {
	flag.Parse()

	if *flagCSV == "" {
		fmt.Fprintln(os.Stderr, "Error: --csv is required")
		flag.Usage()
		os.Exit(2)
	}

	delim, err := parseDelim(*flagDelim)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	hints, err := parseTypeHints(*flagTypes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	t, err := LoadCSV(*flagCSV, LoadOptions{
		Delim:     delim,
		NoHeader:  *flagNoHeader,
		TypeHints: hints,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load CSV: %v\n", err)
		os.Exit(1)
	}

	exec := &Executor{
		Table:              t,
		Format:             *flagFormat,
		ConfirmDestructive: *flagConfirm,
		OutputPath:         *flagOutput,
		Out:                os.Stdout,
	}

	if *flagExec != "" {
		cleaned, bangCommit := PreProcess(*flagExec)
		if bangCommit {
			fmt.Fprintln(os.Stderr, "Error: trailing '!' is not supported in --exec mode; use --commit")
			os.Exit(2)
		}
		stmt, err := Parse(cleaned)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
			os.Exit(1)
		}
		if err := exec.Execute(stmt, *flagCommit); err != nil {
			fmt.Fprintf(os.Stderr, "Execution error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "Connected to: %s (%d columns, %d rows)\n", t.Path, len(t.Columns), len(t.Rows))

	if err := runREPL(exec); err != nil {
		fmt.Fprintf(os.Stderr, "REPL error: %v\n", err)
		os.Exit(1)
	}
}

// parseDelim accepts a single-character delimiter, with `\t` as a special
// case for tab.
func parseDelim(s string) (rune, error) {
	if s == `\t` || s == "\t" {
		return '\t', nil
	}
	runes := []rune(s)
	if len(runes) != 1 {
		return 0, fmt.Errorf("--delim must be one character (or \\t for tab), got %q", s)
	}
	return runes[0], nil
}

// parseTypeHints parses a "name=type,name=type" string into a map suitable
// for LoadOptions.TypeHints.
func parseTypeHints(s string) (map[string]ColumnType, error) {
	if s == "" {
		return nil, nil
	}
	out := map[string]ColumnType{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			return nil, fmt.Errorf("--type entry %q has no '=' (expected name=type)", pair)
		}
		name := strings.TrimSpace(pair[:eq])
		typeStr := strings.TrimSpace(pair[eq+1:])
		t, err := ParseColumnType(typeStr)
		if err != nil {
			return nil, fmt.Errorf("--type %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}
