package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func runSelfUpdate(args []string, out, errOut io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(errOut, `Usage:
  keepupdated self-update check --app PATH
  keepupdated self-update prepare --check PATH --pid PID
  keepupdated self-update handoff --plan PATH --confirm ID
  keepupdated self-update inspect --plan PATH --operation ID --app PATH

check queries the public release for the exact running KeepUpdated.app path.
prepare downloads and stages the checked release for the consenting process ID.
handoff starts a worker to install and reopen after the consenting app exits.
inspect reads the saved operation for the exact app path without changing it.
Use self-update COMMAND --help for options.

  -h, --help  show this help`)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(errOut, "Usage: keepupdated self-update check|prepare|handoff|inspect [options]")
		return 2
	}
	switch args[0] {
	case "check", "prepare", "handoff", "inspect", "finish":
	default:
		return 2
	}
	fs := flag.NewFlagSet("self-update "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	app := fs.String("app", "", "exact running KeepUpdated.app path")
	check := fs.String("check", "", "saved check JSON path")
	pid := fs.Int("pid", 0, "exact consenting desktop process ID")
	plan := fs.String("plan", "", "prepared update plan path")
	operation := fs.String("operation", "", "exact saved operation ID for read-only inspection")
	confirm := fs.String("confirm", "", "exact prepared update ID; consents to install and reopen")
	if e := fs.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || runtime.GOOS != "darwin" || (args[0] != "inspect" && *operation != "") {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var value any
	var err error
	switch args[0] {
	case "inspect":
		if *plan == "" || *operation == "" || *app == "" || *confirm != "" || *check != "" || *pid != 0 {
			return 2
		}
		value = manage.InspectSelfUpdate(ctx, *plan, *operation, *app)
	case "check":
		if *app == "" || *check != "" || *pid != 0 || *plan != "" || *confirm != "" {
			return 2
		}
		value, err = manage.CheckSelfUpdate(ctx, *app)
	case "prepare":
		if *check == "" || *pid <= 0 || *app != "" || *plan != "" || *confirm != "" {
			return 2
		}
		var c manage.SelfUpdateCheck
		f, e := os.Open(*check)
		if e != nil {
			err = e
			break
		}
		defer f.Close()
		if e = json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&c); e != nil {
			err = e
			break
		}
		value, err = manage.PrepareSelfUpdate(ctx, c, *pid)
	case "handoff", "finish":
		if *plan == "" || *confirm == "" || *app != "" || *check != "" || *pid != 0 {
			return 2
		}
		if args[0] == "finish" {
			err = manage.FinishSelfUpdate(ctx, *plan, *confirm)
		} else {
			value, err = manage.StartSelfUpdate(ctx, *plan, *confirm)
		}
	default:
		return 2
	}
	if value != nil {
		if json.NewEncoder(out).Encode(value) != nil {
			return 2
		}
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}
