package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func runRecentOperation(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("results", flag.ContinueOnError)
	fs.SetOutput(errOut)
	format := fs.String("format", "json", "json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || *format != "json" {
		fmt.Fprintln(errOut, "invalid arguments; use results --help")
		return 2
	}
	return writeRecentOperation(manage.ActionStateDir(audit.DefaultConfig().Home), out, errOut)
}

func writeRecentOperation(dir string, out, errOut io.Writer) int {
	record, err := manage.ReadRecentOperation(dir)
	if err != nil {
		fmt.Fprintln(errOut, "Saved operation results could not be read:", err)
		return 2
	}
	if json.NewEncoder(out).Encode(record) != nil {
		return 2
	}
	return 0
}
