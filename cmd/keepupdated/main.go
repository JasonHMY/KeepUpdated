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
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

var version = "0.1.2-dev"
var commit = "unknown"
var buildDate = "unknown"

type paths []string

func (p *paths) String() string { return strings.Join(*p, ",") }
func (p *paths) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("path cannot be empty")
	}
	*p = append(*p, v)
	return nil
}
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func usage(out io.Writer) {
	fmt.Fprintln(out, `KeepUpdated — macOS inventory, updates and uninstall

Usage:
  keepupdated scan [options]
  keepupdated check [--online] [options]
  keepupdated plan --action update|uninstall (--id ID | --all) [options]
  keepupdated apply --plan PATH --confirm PLAN_ID [options]
  keepupdated results [--format json]
  keepupdated mcp
  keepupdated special prepare --id ID [--package PATH]
  keepupdated special status|register-helper|operation-status ID
  keepupdated self-update check|prepare|handoff [options]
  keepupdated version [--format json]

scan reads installed metadata. check also inspects static dependencies.
scan and check never install or upgrade software. check --online queries updates.
plan previews changes and queries the original manager; apply executes only an
explicitly confirmed plan after checking the installation and source again.
Use COMMAND --help for options.

Exit codes: 0 report generated, 1 report has errors/diagnostic gaps,
2 invalid arguments, unsupported platform or pre-execution failure.
Output failures return 2, except after apply returns a result (1).
Apply preserves available results on errors; inspect stderr and never retry a plan.
Unknown/unsupported update states can occur with exit 0; inspect the report.
Reports contain local paths and inventory. Keep them private.`)
}
func runCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		usage(errOut)
		return 2
	}
	command := args[0]
	if command == "--help" || command == "-h" || command == "help" {
		if len(args) != 1 {
			fmt.Fprintln(errOut, "help takes no arguments")
			return 2
		}
		usage(out)
		return 0
	}
	if command == "version" || command == "--version" {
		return showVersion(args[1:], out, errOut)
	}
	if command == "mcp" || command == "mcp-worker" {
		return runLocalMCP(command, args[1:], out, errOut)
	}
	if command == "self-update" {
		return runSelfUpdate(args[1:], out, errOut)
	}
	if command == "special" {
		return runSpecial(args[1:], out, errOut)
	}
	if command == "results" {
		return runRecentOperation(args[1:], out, errOut)
	}
	if command == "plan" || command == "apply" {
		return runManage(command, args[1:], out, errOut)
	}
	if command != "scan" && command != "check" {
		fmt.Fprintln(errOut, "unsupported command; use keepupdated --help")
		return 2
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := audit.DefaultConfig()
	format := fs.String("format", "json", "json or table")
	progress := fs.Bool("progress", false, "stream work progress as JSON lines on stderr")
	fs.BoolVar(&c.Online, "online", false, "query public update metadata (check only)")
	fs.BoolVar(&c.SystemUpdates, "system-updates", false, "run macOS softwareupdate --list (check --online only)")
	fs.DurationVar(&c.Timeout, "timeout", 20*time.Second, "per-query timeout (maximum 2m)")
	fs.StringVar(&c.Country, "store-country", "US", "two-letter App Store country")
	var projects, apps, brews, mcpConfigs, skillRoots paths
	fs.Var(&projects, "project", "project path for MCP and .venv inspection (repeatable)")
	fs.Var(&apps, "app-root", "additional application root (repeatable)")
	fs.Var(&brews, "brew-root", "additional Homebrew prefix (repeatable)")
	fs.Var(&mcpConfigs, "mcp-config", "additional Agent MCP config file (repeatable)")
	fs.Var(&skillRoots, "skills-root", "additional Agent Skills root (repeatable)")
	fs.Usage = func() { fmt.Fprintf(fs.Output(), "Usage: keepupdated %s [options]\n\n", command); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || (*format != "json" && *format != "table") || c.Timeout <= 0 || c.Timeout > 2*time.Minute || len(c.Country) != 2 || strings.IndexFunc(c.Country, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') }) >= 0 {
		fmt.Fprintln(errOut, "invalid arguments; use "+command+" --help")
		return 2
	}
	if command == "scan" && (c.Online || c.SystemUpdates) || c.SystemUpdates && !c.Online {
		fmt.Fprintln(errOut, "network queries require check --online")
		return 2
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(errOut, "live scanning currently requires macOS")
		return 2
	}
	c.Projects = projects
	for _, pair := range []struct {
		input  []string
		target *[]string
	}{{apps, &c.AppRoots}, {brews, &c.BrewRoots}, {mcpConfigs, &c.MCPConfigs}, {skillRoots, &c.SkillRoots}} {
		for _, p := range pair.input {
			q, e := filepath.Abs(p)
			if e != nil {
				fmt.Fprintln(errOut, "invalid path")
				return 2
			}
			*pair.target = append(*pair.target, q)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c.Context = ctx
	if *progress {
		c.Progress = progressWriter(errOut)
	}
	r := audit.New(c).Scan(command == "check")
	r.ToolVersion = version
	r.SourceCommit = commit
	if err := renderReport(out, *format, r); err != nil {
		fmt.Fprintln(errOut, "could not write report")
		return 2
	}
	return reportExit(r)
}
func showVersion(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(errOut)
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || (*format != "text" && *format != "json") {
		fmt.Fprintln(errOut, "invalid version arguments")
		return 2
	}
	var err error
	if *format == "json" {
		err = json.NewEncoder(out).Encode(struct {
			Version               string `json:"version"`
			Commit                string `json:"commit"`
			BuildDate             string `json:"build_date"`
			GoVersion             string `json:"go_version"`
			OS                    string `json:"os"`
			Arch                  string `json:"arch"`
			Schema                string `json:"schema"`
			PlanSchema            string `json:"plan_schema"`
			ApplySchema           string `json:"apply_schema"`
			RecentOperationSchema string `json:"recent_operation_schema"`
		}{version, commit, buildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH, audit.Schema, manage.PlanSchema, manage.ApplySchema, manage.RecentOperationSchema})
	} else {
		_, err = fmt.Fprintf(out, "keepupdated %s\ncommit: %s\nbuilt: %s\nplatform: %s/%s\n", version, commit, buildDate, runtime.GOOS, runtime.GOARCH)
	}
	if err != nil {
		return 2
	}
	return 0
}
func renderReport(out io.Writer, format string, r audit.Report) error {
	if format == "json" {
		e := json.NewEncoder(out)
		e.SetIndent("", "  ")
		return e.Encode(r)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tNAME\tINSTALLED\tACTIVE\tUPDATE\tUPDATE CODE\tHEALTH\tHEALTH CODE")
	for _, i := range r.Items {
		fmt.Fprintf(w, "%s\t%q\t%q\t%q\t%s\t%s\t%s\t%s\n", i.Kind, i.Name, i.Version, i.ActiveVersion, i.Update.Status, i.Update.Code, i.Health.Status, i.Health.Code)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "\nTotal: %d; available: %d; current: %d; unknown: %d; unsupported: %d; errors: %d\n", len(r.Items), r.Summary["available"], r.Summary["current"], r.Summary["unknown"], r.Summary["unsupported"], r.Summary["error"]); err != nil {
		return err
	}
	for _, d := range r.Diagnostics {
		if _, err := fmt.Fprintf(out, "Diagnostic: %s %s %q\n", d.Adapter, d.Code, d.Path); err != nil {
			return err
		}
	}
	return nil
}
func reportExit(r audit.Report) int {
	if len(r.Diagnostics) > 0 {
		return 1
	}
	for _, i := range r.Items {
		if i.Update.Status == "error" || i.Health.Status == "error" {
			return 1
		}
	}
	return 0
}
