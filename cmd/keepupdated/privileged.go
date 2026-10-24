package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
)

func runSpecial(args []string, out, errOut io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(errOut, `Usage:
  keepupdated special prepare --id ID [--package PATH] [--app-root PATH]
  keepupdated special status
  keepupdated special register-helper
  keepupdated special operation-status ID

prepare selects one exact inventory target; --package selects a local .pkg.
--app-root adds an application root and may be repeated.
status reads helper status; register-helper requests helper registration.
operation-status reads the result for an exact saved operation ID.
Use special prepare --help for preparation options.

  -h, --help  show this help`)
		return 0
	}
	if len(args) == 0 || args[0] != "prepare" {
		return privileged.Control(context.Background(), args, out, errOut)
	}
	fs := flag.NewFlagSet("special prepare", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var ids, roots paths
	var packagePath string
	fs.Var(&ids, "id", "one exact inventory ID")
	fs.Var(&roots, "app-root", "additional application root (repeatable)")
	fs.StringVar(&packagePath, "package", "", "selected local .pkg path; omit for a system target")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || len(ids) != 1 {
		fmt.Fprintln(errOut, "select one exact inventory ID")
		return 2
	}
	config := audit.DefaultConfig()
	for _, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return 2
		}
		config.AppRoots = append(config.AppRoots, absolute)
	}
	if packagePath != "" {
		absolute, err := filepath.Abs(packagePath)
		if err != nil {
			return 2
		}
		packagePath = absolute
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config.Context = ctx
	report := audit.New(config).Scan(false)
	if ctx.Err() != nil {
		return 2
	}
	input, err := preparationTarget(report, ids[0], packagePath)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	result, err := (privileged.NativeClient{}).Prepare(ctx, input)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if json.NewEncoder(out).Encode(result) != nil {
		return 2
	}
	if result.Status != "prepared" {
		return 1
	}
	return 0
}

func preparationTarget(report audit.Report, id, packagePath string) (privileged.PreparationInput, error) {
	var selected *audit.Item
	for n := range report.Items {
		if report.Items[n].ID == id {
			if selected != nil {
				return privileged.PreparationInput{}, errors.New("ambiguous inventory ID")
			}
			selected = &report.Items[n]
		}
	}
	if selected == nil {
		return privileged.PreparationInput{}, errors.New("inventory ID no longer exists")
	}
	i := *selected
	input := privileged.PreparationInput{TargetID: id, InstallationPath: i.Path, BundleID: i.Identity, FromVersion: i.Version}
	if packagePath != "" && i.Kind == "application" && i.Owner != "system" && i.Owner != "homebrew" && i.Owner != "app-store" && i.Owner != "app-component" {
		input.Kind = "package"
		input.PackagePath = packagePath
		return input, nil
	}
	if packagePath == "" && i.Kind == "os" && i.Owner == "system" && i.Source == "apple" {
		input.Kind = "system"
		return input, nil
	}
	return input, errors.New("selected item has no supported privileged preparation flow")
}
