package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/localmcp"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
	"github.com/JasonHMY/KeepUpdated/internal/resolve"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func mcpStateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(manage.ActionStateDir(home), "mcp")
}
func mcpEngine(selection manage.Selection) manage.Engine {
	c := audit.DefaultConfig()
	c.Timeout = 30 * time.Second
	options := manage.Options{Home: c.Home, BrewRoots: c.BrewRoots, AppRoots: c.AppRoots, ManagerRoots: c.ManagerRoots, ManagerBins: c.ManagerBins}
	resolver := resolve.Resolver{Options: options, Timeout: c.Timeout}
	return manage.Engine{Options: options, ResolveTargets: resolver.Resolve, Snapshot: func(ctx context.Context) (audit.Report, error) {
		c := c
		c.Context = ctx
		scanner := audit.New(c)
		report := scanner.Scan(false)
		report = scanner.CheckManagementDependencies(managementDependencyScope(report, selection))
		return report, ctx.Err()
	}}
}
func runLocalMCP(command string, args []string, out, errOut io.Writer) int {
	if command == "mcp" && len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(errOut, `Usage: keepupdated mcp

Serve local MCP over stdio using the default scan locations.
No server options are accepted.

  -h, --help  show this help`)
		return 0
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(errOut, "local MCP requires macOS")
		return 2
	}
	if command == "mcp-worker" {
		if len(args) != 1 {
			return 2
		}
		if e := localmcp.Worker(context.Background(), mcpStateDir(), args[0], func(p manage.Plan) manage.Engine { return mcpEngine(p.Selection) }); e != nil {
			fmt.Fprintln(errOut, e)
			return 1
		}
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(errOut, "Usage: keepupdated mcp (local stdio, default scan locations)")
		return 2
	}
	backend := localmcp.Backend{
		Scan: func(ctx context.Context, check, online bool) (audit.Report, error) {
			c := audit.DefaultConfig()
			c.Context = ctx
			c.Online = online
			r := audit.New(c).Scan(check)
			r.ToolVersion = version
			r.SourceCommit = commit
			return r, ctx.Err()
		},
		Preview: func(ctx context.Context, s manage.Selection) (manage.Plan, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			eng := mcpEngine(s)
			r, e := eng.Snapshot(ctx)
			if e != nil {
				return manage.Plan{}, e
			}
			o := eng.Options
			o.Context = ctx
			o.ResolvedTargets, e = eng.ResolveTargets(ctx, r, s)
			if e != nil {
				return manage.Plan{}, e
			}
			return manage.NewPlan(r, s, o)
		},
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := localmcp.NewServer(mcpStateDir(), version, commit, backend)
	if e := server.Run(ctx, &mcp.StdioTransport{MaxLineLength: 1 << 20}); e != nil && ctx.Err() == nil {
		fmt.Fprintln(errOut, "MCP session ended:", e)
		return 1
	}
	return 0
}
