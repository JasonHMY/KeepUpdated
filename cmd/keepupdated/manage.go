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
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
	"github.com/JasonHMY/KeepUpdated/internal/resolve"
)

func runManage(command string, args []string, out, errOut io.Writer) (code int) {
	diagnostics := &outputWriter{Writer: errOut}
	errOut = diagnostics
	outputFailureCode := 2
	defer func() {
		if diagnostics.failed.Load() {
			code = outputFailureCode
		}
	}()
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := audit.DefaultConfig()
	var projects, apps, brews, configs, skills, ids, quitApps, reopenApps, consents paths
	fs.Var(&projects, "project", "Agent project path (repeatable)")
	fs.Var(&apps, "app-root", "additional application root (repeatable)")
	fs.Var(&brews, "brew-root", "additional Homebrew prefix (repeatable)")
	fs.Var(&configs, "mcp-config", "additional Agent MCP config file (repeatable)")
	fs.Var(&skills, "skills-root", "additional Agent Skills root (repeatable)")
	fs.DurationVar(&c.Timeout, "timeout", 30*time.Second, "per-query timeout (maximum 2m)")
	format := fs.String("format", "json", "json")
	progress := fs.Bool("progress", false, "stream work progress as JSON lines on stderr")
	var selection manage.Selection
	var planPath, confirmation, privilegedPath, idsPath string
	if command == "plan" {
		fs.StringVar(&selection.MCPCandidate, "mcp-candidate", "", "bind the selected MCP registry candidate ID to this exact migration")
		fs.StringVar(&selection.MCPReference, "mcp-reference", "", "migrate one MCP package reference to an exact version (requires --special; configuration only)")
		fs.StringVar(&privilegedPath, "privileged-request", "", "typed package/system/restart request for one exact inventory ID")
		fs.StringVar(&selection.Action, "action", "", "update or uninstall")
		fs.BoolVar(&selection.CachedPlugins, "cached-plugins", false, "use verified cached plugin snapshots without remote refresh")
		fs.Var(&ids, "id", "exact inventory ID (repeatable)")
		fs.StringVar(&idsPath, "ids-file", "", "JSON array of exact inventory IDs (instead of --id or --all)")
		fs.Var(&quitApps, "quit-app", "request normal exit of this exact application ID for update (repeatable)")
		fs.Var(&reopenApps, "reopen-app", "reopen this quit-app ID only after a verified update (repeatable)")
		fs.BoolVar(&selection.All, "all", false, "preview all installed items")
		fs.BoolVar(&selection.Cleanup, "cleanup", false, "include supported personal-data cleanup paths")
		fs.Var(&consents, "consent", "explicit action consent: runtime_update, privileged_install, restart_system (repeatable)")
		fs.BoolVar(&selection.Special, "special", false, "preview separately confirmed operations with installation scripts")
	} else {
		fs.StringVar(&planPath, "plan", "", "path to the exact preview JSON")
		fs.StringVar(&confirmation, "confirm", "", "exact plan_id approved by the user")
	}
	fs.Usage = func() { fmt.Fprintf(fs.Output(), "Usage: keepupdated %s [options]\n\n", command); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if idsPath != "" {
		if len(ids) > 0 || selection.All {
			fmt.Fprintln(errOut, "--ids-file cannot be combined with --id or --all")
			return 2
		}
		var err error
		ids, err = readSelectionIDs(idsPath)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
	}
	selection.IDs, selection.QuitApps, selection.ReopenApps = ids, quitApps, reopenApps
	if err := manage.ValidateLifecycleSelection(selection); err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	var consentErr error
	selection.Consents, consentErr = manage.NormalizeConsents(consents)
	if consentErr != nil {
		fmt.Fprintln(errOut, consentErr)
		return 2
	}
	if fs.NArg() != 0 || *format != "json" || c.Timeout <= 0 || c.Timeout > 2*time.Minute {
		fmt.Fprintln(errOut, "invalid arguments; use "+command+" --help")
		return 2
	}
	if command == "plan" && selection.MCPCandidate != "" && (selection.MCPReference == "" || len(selection.MCPCandidate) != 64) {
		fmt.Fprintln(errOut, "MCP candidate selection requires an exact reference and candidate ID")
		return 2
	}
	if command == "plan" && selection.MCPReference != "" && (selection.Action != "update" || selection.All || len(ids) != 1 || !selection.Special || selection.Cleanup) {
		fmt.Fprintln(errOut, "MCP reference migration requires update, one explicit ID and --special")
		return 2
	}
	if command == "plan" && (selection.Action != "update" && selection.Action != "uninstall" || selection.All == (len(ids) > 0) || selection.Cleanup && selection.Action != "uninstall") || command == "apply" && (planPath == "" || confirmation == "") {
		fmt.Fprintln(errOut, "select an action and exact targets, or an approved plan; use "+command+" --help")
		return 2
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(errOut, "installation management currently requires macOS")
		return 2
	}
	for _, pair := range []struct {
		input  []string
		target *[]string
	}{{projects, &c.Projects}, {apps, &c.AppRoots}, {brews, &c.BrewRoots}, {configs, &c.MCPConfigs}, {skills, &c.SkillRoots}} {
		for _, path := range pair.input {
			absolute, err := filepath.Abs(path)
			if err != nil {
				fmt.Fprintln(errOut, "invalid path")
				return 2
			}
			*pair.target = append(*pair.target, absolute)
		}
	}
	if privilegedPath != "" {
		file, err := os.OpenFile(privilegedPath, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			fmt.Fprintln(errOut, "privileged request unavailable")
			return 2
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			file.Close()
			fmt.Fprintln(errOut, "privileged request must be a bounded regular file")
			return 2
		}
		request, err := privileged.Decode(file)
		file.Close()
		if err != nil {
			fmt.Fprintln(errOut, "invalid typed privileged request")
			return 2
		}
		selection.PrivilegedRequest = &request
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *progress {
		c.Progress = progressWriter(errOut)
	}
	options := manage.Options{Context: ctx, Progress: c.Progress, ManagerRoots: c.ManagerRoots, ManagerBins: c.ManagerBins, Home: c.Home, BrewRoots: c.BrewRoots, AppRoots: c.AppRoots}
	if len(c.SkillRoots) > 0 || len(c.Projects) > 0 {
		for _, path := range []string{".codex/skills", ".agents/skills", ".claude/skills", ".config/opencode/skills", ".hermes/skills", ".openclaw/skills"} {
			options.SkillRoots = append(options.SkillRoots, filepath.Join(c.Home, path))
		}
		options.SkillRoots = append(options.SkillRoots, c.SkillRoots...)
		for _, project := range c.Projects {
			for _, rel := range []string{".opencode/skills", ".agents/skills", ".claude/skills", ".codex/skills"} {
				options.SkillRoots = append(options.SkillRoots, filepath.Join(project, rel))
			}
		}
	}
	resolver := resolve.Resolver{Options: options, Timeout: c.Timeout}
	snapshot := func(ctx context.Context) (audit.Report, error) {
		if ctx.Err() != nil {
			return audit.Report{}, ctx.Err()
		}
		config := c
		config.Context = ctx
		scanner := audit.New(config)
		report := scanner.Scan(false)
		report = scanner.CheckManagementDependencies(managementDependencyScope(report, selection))
		return report, ctx.Err()
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if command == "plan" {
		report, err := snapshot(ctx)
		if err != nil {
			fmt.Fprintln(errOut, "inventory cancelled")
			return 2
		}
		options.ResolvedTargets, err = resolver.Resolve(ctx, report, selection)
		if err != nil {
			fmt.Fprintln(errOut, "owner-native candidate query cancelled")
			return 2
		}
		plan, err := manage.NewPlan(report, selection, options)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
		if encoder.Encode(plan) != nil {
			return 2
		}
		return 0
	}
	f, err := os.OpenFile(planPath, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		fmt.Fprintln(errOut, "plan file unavailable")
		return 2
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 16<<20 {
		fmt.Fprintln(errOut, "plan must be a bounded regular file")
		return 2
	}
	plan, err := manage.DecodePlan(f)
	if err != nil {
		fmt.Fprintln(errOut, "plan JSON is invalid")
		return 2
	}
	selection = plan.Selection
	result, err := manage.Apply(ctx, plan, confirmation, manage.Engine{Snapshot: snapshot, Options: options, ResolveTargets: resolver.Resolve})
	if result.State != "" {
		outputFailureCode = 1
	}
	code = applyExit(result)
	if err != nil {
		fmt.Fprintln(errOut, err)
		if result.State == "" {
			return 2
		}
		code = 2
		for _, item := range result.Results {
			if item.Reason != "operation_not_started" {
				code = 1
				break
			}
		}
		fmt.Fprintf(errOut, "operation %s: result persistence is unconfirmed; preserve this output and inspect results and current state; do not retry this plan\n", result.PlanID)
	}
	if writeErr := encoder.Encode(result); writeErr != nil {
		fmt.Fprintf(errOut, "operation %s: could not write apply result: %v; inspect results and current state; do not retry this plan\n", result.PlanID, writeErr)
		return 1
	}
	return code
}

func readSelectionIDs(path string) ([]string, error) {
	const limit = 16 << 20
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("selection file unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("selection must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("selection file could not be read within the size limit")
	}
	var ids []string
	if json.Unmarshal(data, &ids) != nil || len(ids) == 0 {
		return nil, errors.New("selection must be a non-empty JSON array of inventory IDs")
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("selection contains an empty inventory ID")
		}
	}
	return ids, nil
}

func applyExit(result manage.ApplyResult) int {
	if result.State == "in_progress" {
		return 1
	}
	for _, item := range result.Results {
		for _, app := range item.ApplicationLifecycle {
			if app.ReopenStatus != "not_requested" && app.ReopenStatus != "reopened" {
				return 1
			}
		}
		if item.Status != "updated" && item.Status != "uninstalled" && item.Status != "configured" && !(item.Status == "skipped" && (item.Reason == "already_current" || item.Reason == "duplicate_installation_target")) {
			return 1
		}
	}
	return 0
}

// Uninstall guards need reverse dependencies; shared-runtime review needs the
// complete consumer graph. Ordinary updates only need selected installation rows
// and their presentation/manager aliases. Discovery itself always stays complete.
func managementDependencyScope(report audit.Report, selection manage.Selection) func(audit.Item) bool {
	if selection.All || selection.Action == "uninstall" {
		return nil
	}
	selected := map[string]bool{}
	for _, id := range selection.IDs {
		selected[id] = true
	}
	for changed := true; changed; {
		changed = false
		for _, item := range report.Items {
			if selected[item.ID] {
				if item.Kind == "formula" && manage.ProtectedRuntime(item.Name) || item.Owner == "uv-standalone" {
					return nil
				}
				for _, id := range item.Related {
					if !selected[id] {
						selected[id], changed = true, true
					}
				}
			}
		}
	}
	return func(item audit.Item) bool { return selected[item.ID] }
}
