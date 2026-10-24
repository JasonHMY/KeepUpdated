package localmcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type runnerFunc func(context.Context, manage.Command) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, c manage.Command) ([]byte, error) { return f(ctx, c) }

type testFixture struct {
	Home    string
	Report  audit.Report
	Plan    manage.Plan
	Options manage.Options
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func writeTest(t *testing.T, path string, b []byte, mode os.FileMode) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, b, mode); e != nil {
		t.Fatal(e)
	}
}
func newFixture(t *testing.T, count int) *testFixture {
	t.Helper()
	home := canonicalTemp(t)
	f := &testFixture{Home: home, Report: audit.Report{Schema: audit.Schema, Items: []audit.Item{}, Summary: map[string]int{"available": count}}}
	manager := filepath.Join(home, ".npm-global/bin/npm")
	managerBody := []byte("fixture manager, never executed\n")
	writeTest(t, manager, managerBody, 0755)
	sum := sha256.Sum256(managerBody)
	f.Options = manage.Options{Home: home, BrewRoots: []string{}, AppRoots: []string{}, ManagerPaths: map[string]string{"npm": manager}, Now: time.Now().UTC().Add(-time.Second), ResolvedTargets: map[string]manage.ResolvedTarget{}}
	scope := filepath.Join(home, ".npm-global")
	for n := 0; n < count; n++ {
		name := fmt.Sprintf("example-%d", n)
		id := fmt.Sprintf("item-%d", n)
		p := filepath.Join(scope, "lib/node_modules", name)
		body := []byte(fmt.Sprintf(`{"name":%q,"version":"1.0.0","_resolved":"https://registry.npmjs.org/%s/-/%s-1.0.0.tgz"}`, name, name, name))
		writeTest(t, filepath.Join(p, "package.json"), body, 0644)
		item := audit.Item{ID: id, Kind: "node-package", Name: name, Identity: name, Path: p, RealPath: p, Owner: "npm", Source: "npm-public", Channel: "latest", Version: "1.0.0", Latest: "1.1.0", Health: audit.Result{Status: "ok"}, Update: audit.Result{Status: "available"}}
		f.Report.Items = append(f.Report.Items, item)
		selector, e := audit.ReadNodeSelector("npm", name, p, scope)
		if e != nil {
			t.Fatal(e)
		}
		f.Options.ResolvedTargets[id] = manage.ResolvedTarget{Provider: "npm", ManagerPath: manager, Scope: scope, OriginVerified: true, Source: "fixture native configuration", Version: "1.1.0", Node: &manage.NodePolicy{ManagerVersion: "11.19.1", ManagerFingerprint: hex.EncodeToString(sum[:]), Layout: manage.NodeLayout("npm", "11.19.1"), Bin: manage.NodeBin("npm", manage.Binding{Scope: scope}, f.Options), Selection: selector, LatestVersion: "1.1.0"}}
	}
	var ids []string
	for _, i := range f.Report.Items {
		ids = append(ids, i.ID)
	}
	p, e := manage.NewPlan(f.Report, manage.Selection{Action: "update", IDs: ids}, f.Options)
	if e != nil {
		t.Fatal(e)
	}
	f.Plan = p
	for _, i := range p.Items {
		if i.Status != "ready" {
			t.Fatalf("fixture not ready: %+v", i)
		}
	}
	return f
}
func (f *testFixture) engine(run manage.Runner) manage.Engine {
	return manage.Engine{Options: f.Options, StateDir: filepath.Join(f.Home, "actions"), Runner: run, Snapshot: func(context.Context) (audit.Report, error) { return f.Report, nil }, ResolveTargets: func(context.Context, audit.Report, manage.Selection) (map[string]manage.ResolvedTarget, error) {
		return f.Options.ResolvedTargets, nil
	}}
}
func (f *testFixture) service(t *testing.T) *service {
	t.Helper()
	return &service{store: store{filepath.Join(f.Home, "mcp")}, backend: Backend{Scan: func(context.Context, bool, bool) (audit.Report, error) { return f.Report, nil }, Preview: func(context.Context, manage.Selection) (manage.Plan, error) { return f.Plan, nil }}, confirm: func(context.Context, privileged.PlanConfirmation) (bool, error) { return false, nil }, start: func(string) error { return errors.New("unexpected worker") }}
}
func savePlan(t *testing.T, s *service, p manage.Plan) string {
	t.Helper()
	id, e := handle()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.store.write(id, "plan", p, true); e != nil {
		t.Fatal(e)
	}
	return id
}
func requireOperation(t *testing.T, s *service, id, state string) Operation {
	t.Helper()
	op, e := s.operation(id)
	if e != nil {
		t.Fatal(e)
	}
	if op.State != state {
		t.Fatalf("state=%s want=%s: %+v", op.State, state, op)
	}
	return op
}
func connectMemory(t *testing.T, s *service, protocol string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	a, b := mcp.NewInMemoryTransports()
	ss, e := s.server("test-version", "test-commit").Connect(ctx, a, nil)
	if e != nil {
		t.Fatal(e)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	cs, e := client.Connect(ctx, b, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	return cs
}
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	r, e := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func structured(t *testing.T, r *mcp.CallToolResult) map[string]any {
	t.Helper()
	if r.IsError {
		t.Fatalf("tool error: %+v", r.Content)
	}
	b, e := json.Marshal(r.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	return m
}

func TestSDKProtocolsSchemasAndTools(t *testing.T) {
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			f := newFixture(t, 2)
			s := f.service(t)
			cs := connectMemory(t, s, protocol)
			caps := structured(t, callTool(t, cs, "get_capabilities", map[string]any{}))
			for key, want := range map[string]string{"schema": audit.Schema, "plan_schema": manage.PlanSchema, "apply_schema": manage.ApplySchema, "inventory_scope": "default_locations", "operation_results": "per_operation_id", "execution_cancellation": "not_supported"} {
				if caps[key] != want {
					t.Fatalf("capability %s = %v, want %s", key, caps[key], want)
				}
			}
			list, e := cs.ListTools(context.Background(), nil)
			if e != nil {
				t.Fatal(e)
			}
			if len(list.Tools) != 8 {
				t.Fatalf("tools=%d", len(list.Tools))
			}
			wantHints := map[string][3]bool{
				"get_capabilities":  {true, false, false},
				"scan_inventory":    {false, true, false},
				"check_updates":     {false, true, true},
				"list_items":        {true, false, false},
				"get_item":          {true, false, false},
				"preview_changes":   {false, true, true},
				"request_execution": {false, true, true},
				"get_operation":     {true, false, false},
			}
			for _, tool := range list.Tools {
				want, ok := wantHints[tool.Name]
				a := tool.Annotations
				if !ok || a == nil || a.DestructiveHint == nil || a.OpenWorldHint == nil || [3]bool{a.ReadOnlyHint, *a.DestructiveHint, *a.OpenWorldHint} != want {
					t.Fatalf("unexpected annotations for %s: %+v", tool.Name, a)
				}

				if tool.InputSchema == nil {
					t.Fatalf("missing schema: %s", tool.Name)
				}
				b, _ := json.Marshal(tool.InputSchema)
				if bytes.Contains(b, []byte("shell")) || bytes.Contains(b, []byte("approved")) {
					t.Fatalf("unsafe schema %s: %s", tool.Name, b)
				}
			}
			snapshot := structured(t, callTool(t, cs, "scan_inventory", map[string]any{}))["snapshot_handle"].(string)
			page := structured(t, callTool(t, cs, "list_items", map[string]any{"snapshot_handle": snapshot, "limit": 1}))
			if page["total"] != float64(2) || len(page["items"].([]any)) != 1 {
				t.Fatal(page)
			}
			item := structured(t, callTool(t, cs, "get_item", map[string]any{"snapshot_handle": snapshot, "id": "item-0"}))
			if item["id"] != "item-0" {
				t.Fatal(item)
			}
			for _, args := range []map[string]any{{"snapshot_handle": snapshot, "limit": 51}, {"snapshot_handle": snapshot, "offset": -1}, {"snapshot_handle": snapshot, "offset": 9}} {
				if !callTool(t, cs, "list_items", args).IsError {
					t.Fatal("invalid page accepted")
				}
			}
			p := structured(t, callTool(t, cs, "preview_changes", map[string]any{"action": "update", "ids": []string{"item-0"}}))
			id := p["plan_handle"].(string)
			if !validHandle(id) || id == f.Plan.PlanID {
				t.Fatal("plan handle not opaque")
			}
			if !callTool(t, cs, "request_execution", map[string]any{"plan_handle": id, "approved": true}).IsError {
				t.Fatal("forged approval accepted")
			}
			op := structured(t, callTool(t, cs, "request_execution", map[string]any{"plan_handle": id}))
			if op["state"] != "not_started" {
				t.Fatal(op)
			}
		})
	}
}

func TestRequestDenialCancellationExpiryAndReplay(t *testing.T) {
	for _, mode := range []string{"denied", "error", "cancelled", "deadline", "expired", "expires-during"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			p := f.Plan
			confirms := 0
			starts := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "expired" {
				p.ExpiresAt = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
			}
			s.confirm = func(c context.Context, r privileged.PlanConfirmation) (bool, error) {
				confirms++
				if r.PlanDigest != p.PlanID || !validHandle(r.Challenge) || !strings.Contains(r.Details, "item-0") {
					t.Error("unbound summary")
				}
				switch mode {
				case "denied":
					return false, nil
				case "error":
					return false, errors.New("unavailable")
				case "cancelled":
					cancel()
					return true, nil
				case "deadline":
					return false, context.DeadlineExceeded
				case "expires-during":
					time.Sleep(400 * time.Millisecond)
					return true, nil
				}
				return true, nil
			}
			if mode == "expires-during" {
				f.Options.TTL = 300 * time.Millisecond
				f.Options.Now = time.Now().UTC()
				p, _ = manage.NewPlan(f.Report, f.Plan.Selection, f.Options)
			}
			s.start = func(string) error { starts++; return nil }
			id := savePlan(t, s, p)
			op, e := s.request(ctx, id)
			if e != nil || op.State != "not_started" || starts != 0 {
				t.Fatalf("op=%+v err=%v starts=%d", op, e, starts)
			}
			_, e = s.request(context.Background(), id)
			if e != nil || starts != 0 {
				t.Fatalf("replay=%v starts=%d", e, starts)
			}
			want := 1
			if mode == "expired" {
				want = 0
			}
			if confirms != want {
				t.Fatalf("confirmed=%d want=%d", confirms, want)
			}
			if _, e = os.Stat(filepath.Join(s.store.dir, id+".decision")); !os.IsNotExist(e) {
				t.Fatalf("unapproved decision persisted: %v", e)
			}
		})
	}
}

func TestConcurrentRequestHasOneDecisionAndWorker(t *testing.T) {
	f := newFixture(t, 1)
	s := f.service(t)
	id := savePlan(t, s, f.Plan)
	var confirms, starts atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	s.confirm = func(context.Context, privileged.PlanConfirmation) (bool, error) {
		confirms.Add(1)
		close(entered)
		<-release
		return true, nil
	}
	s.start = func(id string) error {
		starts.Add(1)
		op := Operation{ID: id, PlanHandle: id, PlanID: f.Plan.PlanID, State: "finished", Result: &manage.ApplyResult{Schema: manage.ApplySchema, PlanID: f.Plan.PlanID, State: "finished", OriginalIDs: []string{f.Plan.Items[0].ID}, Results: []manage.ItemResult{{ID: f.Plan.Items[0].ID, Status: "updated"}}}}
		return s.store.write(id, "operation", op, false)
	}
	done := make(chan error, 1)
	go func() { _, e := s.request(context.Background(), id); done <- e }()
	<-entered
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op, e := s.request(context.Background(), id)
			if e == nil && op.ID != id {
				e = errors.New("wrong replay operation")
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for n := 0; n < 3; n++ {
		op, e := s.request(context.Background(), id)
		if e != nil || op.State != "finished" {
			t.Fatalf("replay %+v %v", op, e)
		}
	}
	if confirms.Load() != 1 || starts.Load() != 1 {
		t.Fatalf("confirm=%d starts=%d", confirms.Load(), starts.Load())
	}
}

func TestStoreGuardsAndAtomicClaims(t *testing.T) {
	for _, mode := range []string{"symlink-dir", "world-readable-dir", "symlink-file", "world-readable-file", "hardlink-file", "oversized", "trailing", "depth"} {
		t.Run(mode, func(t *testing.T) {
			root := canonicalTemp(t)
			s := store{filepath.Join(root, "state")}
			id, _ := handle()
			if e := s.write(id, "record", map[string]int{"a": 1}, true); e != nil {
				t.Fatal(e)
			}
			p, _ := s.path(id, "record")
			switch mode {
			case "symlink-dir":
				if e := os.Rename(s.dir, s.dir+"-real"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(s.dir+"-real", s.dir); e != nil {
					t.Fatal(e)
				}
			case "world-readable-dir":
				_ = os.Chmod(s.dir, 0755)
			case "symlink-file":
				_ = os.Rename(p, p+"-real")
				_ = os.Symlink(p+"-real", p)
			case "world-readable-file":
				_ = os.Chmod(p, 0644)
			case "hardlink-file":
				if e := os.Link(p, p+"-link"); e != nil {
					t.Fatal(e)
				}
			case "oversized":
				if e := os.Truncate(p, fileLimit+1); e != nil {
					t.Fatal(e)
				}
			case "trailing":
				_ = os.WriteFile(p, []byte(`{"a":1} {"a":2}`), 0600)
			case "depth":
				s.dir = root + strings.Repeat("/nested", 65)
			}
			var out map[string]int
			if e := s.read(id, "record", &out); e == nil {
				t.Fatalf("accepted unsafe %s", mode)
			}
		})
	}
	s := store{filepath.Join(canonicalTemp(t), "state")}
	id, _ := handle()
	f, e := s.claim(id, "lock")
	if e != nil {
		t.Fatal(e)
	}
	if !s.active(id, "lock") {
		t.Fatal("live claim not active")
	}
	if _, e = s.claim(id, "lock"); !os.IsExist(e) {
		t.Fatalf("duplicate claim=%v", e)
	}
	_ = f.Close()
	if s.active(id, "lock") {
		t.Fatal("closed claim active")
	}
	if _, e = s.claim(id, "lock"); !os.IsExist(e) {
		t.Fatal("dead claim replayed")
	}
}

func TestWorkerResultsReplayDriftAndRecovery(t *testing.T) {
	f := newFixture(t, 2)
	s := f.service(t)
	id := savePlan(t, s, f.Plan)
	if e := s.store.write(id, "decision", decision{PlanID: f.Plan.PlanID, Approved: true}, true); e != nil {
		t.Fatal(e)
	}
	calls := 0
	engine := f.engine(runnerFunc(func(ctx context.Context, c manage.Command) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("controlled failure")
		}
		i := &f.Report.Items[1]
		i.Version = "1.1.0"
		writeTest(t, filepath.Join(i.Path, "package.json"), []byte(fmt.Sprintf(`{"name":%q,"version":"1.1.0"}`, i.Name)), 0644)
		return nil, nil
	}))
	if e := runWorker(context.Background(), s.store, id, func(manage.Plan) manage.Engine { return engine }); e != nil {
		t.Fatal(e)
	}
	op := requireOperation(t, s, id, "finished")
	if calls != 2 || op.Result == nil || len(op.Result.Results) != 2 || op.Result.Results[0].Status == "updated" || op.Result.Results[1].Status != "updated" {
		t.Fatalf("partial results lost: %+v calls=%d", op, calls)
	}
	if e := runWorker(context.Background(), s.store, id, func(manage.Plan) manage.Engine { return engine }); e == nil || calls != 2 {
		t.Fatal("worker replay executed")
	}
	// A later CLI journal belongs to another plan and cannot replace this result.
	p2, e := manage.NewPlan(f.Report, manage.Selection{Action: "update", IDs: []string{"item-0"}}, f.Options)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = manage.Apply(context.Background(), p2, p2.PlanID, engine)
	old := requireOperation(t, s, id, "finished")
	if old.PlanID != f.Plan.PlanID || old.Result.PlanID != f.Plan.PlanID || len(old.Result.Results) != 2 {
		t.Fatal("recent CLI mixed with operation")
	}
	t.Run("source-drift", func(t *testing.T) {
		f := newFixture(t, 1)
		s := f.service(t)
		id := savePlan(t, s, f.Plan)
		_ = s.store.write(id, "decision", decision{PlanID: f.Plan.PlanID, Approved: true}, true)
		f.Report.Items[0].Source = "changed-source"
		calls := 0
		e := runWorker(context.Background(), s.store, id, func(manage.Plan) manage.Engine {
			return f.engine(runnerFunc(func(context.Context, manage.Command) ([]byte, error) { calls++; return nil, nil }))
		})
		if e == nil || calls != 0 {
			t.Fatalf("drift executed calls=%d err=%v", calls, e)
		}
		requireOperation(t, s, id, "failed")
	})
	t.Run("unapproved", func(t *testing.T) {
		f := newFixture(t, 1)
		s := f.service(t)
		id := savePlan(t, s, f.Plan)
		if e := runWorker(context.Background(), s.store, id, func(manage.Plan) manage.Engine { t.Fatal("unapproved engine"); return manage.Engine{} }); e == nil {
			t.Fatal("missing decision accepted")
		}
	})
	t.Run("dead-worker", func(t *testing.T) {
		f := newFixture(t, 1)
		s := f.service(t)
		id := savePlan(t, s, f.Plan)
		_ = s.store.write(id, "operation", Operation{ID: id, PlanHandle: id, PlanID: f.Plan.PlanID, State: "running"}, true)
		requireOperation(t, s, id, "unknown")
	})
}

// These hooks exist only in the Go test executable. The shipped CLI has no
// fixture arguments, environment approval switch or injectable approver.
type diskFixture struct {
	Home    string
	Report  audit.Report
	Plan    manage.Plan
	Targets map[string]manage.ResolvedTarget
}

func persistFixture(t *testing.T, f *testFixture) {
	t.Helper()
	b, e := json.Marshal(diskFixture{f.Home, f.Report, f.Plan, f.Options.ResolvedTargets})
	if e != nil {
		t.Fatal(e)
	}
	writeTest(t, filepath.Join(f.Home, "fixture.json"), b, 0600)
}
func loadFixture(dir string) (*testFixture, error) {
	b, e := os.ReadFile(filepath.Join(dir, "fixture.json"))
	if e != nil {
		return nil, e
	}
	var d diskFixture
	if e = json.Unmarshal(b, &d); e != nil {
		return nil, e
	}
	return &testFixture{Home: d.Home, Report: d.Report, Plan: d.Plan, Options: manage.Options{Home: d.Home, BrewRoots: []string{}, AppRoots: []string{}, ManagerPaths: map[string]string{"npm": filepath.Join(d.Home, ".npm-global/bin/npm")}, ResolvedTargets: d.Targets}}, nil
}
func helperCommand(dir, mode string) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=^TestLocalMCPSubprocess$")
	c.Env = append(os.Environ(), "KEEPUPDATED_TEST_HELPER="+mode, "KEEPUPDATED_TEST_DIR="+dir)
	return c
}
func TestLocalMCPSubprocess(t *testing.T) {
	mode := os.Getenv("KEEPUPDATED_TEST_HELPER")
	if mode == "" {
		return
	}
	dir := os.Getenv("KEEPUPDATED_TEST_DIR")
	f, e := loadFixture(dir)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(2)
	}
	s := f.service(t)
	switch mode {
	case "server", "server-delay-reply", "wait-approval":
		s.confirm = func(ctx context.Context, _ privileged.PlanConfirmation) (bool, error) {
			if mode == "wait-approval" {
				_ = os.WriteFile(filepath.Join(dir, "approval-entered"), []byte("entered"), 0600)
				<-ctx.Done()
				_ = os.WriteFile(filepath.Join(dir, "approval-cancelled"), []byte("cancelled"), 0600)
				return false, ctx.Err()
			}
			return true, nil
		}
		s.start = func(id string) error {
			c := helperCommand(dir, "worker")
			c.Env = append(c.Env, "KEEPUPDATED_TEST_HANDLE="+id)
			c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if e := c.Start(); e != nil {
				return e
			}
			_ = os.WriteFile(filepath.Join(dir, "worker-pid"), []byte(fmt.Sprint(c.Process.Pid)), 0600)
			go func() { _ = c.Wait() }()
			if mode == "server-delay-reply" {
				time.Sleep(30 * time.Second)
			}
			return nil
		}
		ctx, cancel := signalContext()
		defer cancel()
		e = s.server("fixture", "fixture-source").Run(ctx, &mcp.StdioTransport{})
		if e != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, e)
		}
	case "worker":
		id := os.Getenv("KEEPUPDATED_TEST_HANDLE")
		run := runnerFunc(func(ctx context.Context, c manage.Command) ([]byte, error) {
			if e := os.WriteFile(filepath.Join(dir, "mutation-entered"), []byte("entered"), 0600); e != nil {
				return nil, e
			}
			for {
				if _, e := os.Stat(filepath.Join(dir, "release-worker")); e == nil {
					break
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
			for n := range f.Report.Items {
				f.Report.Items[n].Version = "1.1.0"
				i := f.Report.Items[n]
				if e := os.WriteFile(filepath.Join(i.Path, "package.json"), []byte(fmt.Sprintf(`{"name":%q,"version":"1.1.0"}`, i.Name)), 0644); e != nil {
					return nil, e
				}
			}
			return nil, nil
		})
		e = runWorker(context.Background(), s.store, id, func(manage.Plan) manage.Engine { return f.engine(run) })
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(3)
		}
	default:
		os.Exit(4)
	}
	os.Exit(0)
}
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
func waitFor(t *testing.T, reason string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", reason)
}
func fileExists(p string) bool { _, e := os.Stat(p); return e == nil }
func connectProcess(t *testing.T, dir, mode string) (*mcp.ClientSession, *exec.Cmd) {
	t.Helper()
	c := helperCommand(dir, mode)
	c.Stderr = os.Stderr
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: c, TerminateDuration: 250 * time.Millisecond}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, c
}
func TestStdioHandshakeAndProtocolOnlyOutput(t *testing.T) {
	f := newFixture(t, 1)
	persistFixture(t, f)
	c := helperCommand(f.Home, "server")
	stdin, e := c.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	stdout, e := c.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if e = c.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Process.Kill() })
	_, e = io.WriteString(stdin, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"fixture\",\"version\":\"1\"}}}\n")
	if e != nil {
		t.Fatal(e)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	if !scanner.Scan() {
		t.Fatalf("no initialize response: %s", stderr.String())
	}
	var response map[string]any
	if e = json.Unmarshal(scanner.Bytes(), &response); e != nil {
		t.Fatalf("stdout not protocol: %s", scanner.Text())
	}
	result := response["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" {
		t.Fatal(result)
	}
	_, _ = io.WriteString(stdin, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	if !scanner.Scan() {
		t.Fatal("no tools response")
	}
	if e = json.Unmarshal(scanner.Bytes(), &response); e != nil {
		t.Fatalf("stdout not protocol: %s", scanner.Text())
	}
	tools := response["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 8 {
		t.Fatal(len(tools))
	}
	_ = stdin.Close()
	if e = c.Wait(); e != nil {
		t.Fatalf("EOF exit: %v %s", e, stderr.String())
	}
}
func TestDetachedWorkerSurvivesServerExitAndLostReply(t *testing.T) {
	for _, shutdown := range []string{"EOF", "SIGINT", "SIGTERM", "SIGKILL", "lost-reply"} {
		t.Run(shutdown, func(t *testing.T) {
			f := newFixture(t, 1)
			persistFixture(t, f)
			s := f.service(t)
			id := savePlan(t, s, f.Plan)
			serverMode := "server"
			if shutdown == "lost-reply" {
				serverMode = "server-delay-reply"
			}
			cs, c := connectProcess(t, f.Home, serverMode)
			if shutdown == "lost-reply" {
				done := make(chan struct{})
				go func() {
					_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_execution", Arguments: map[string]any{"plan_handle": id}})
					close(done)
				}()
				waitFor(t, "worker manager entry", func() bool { return fileExists(filepath.Join(f.Home, "mutation-entered")) })
				select {
				case <-done:
					t.Fatal("fixture replied before deliberate response loss")
				default:
				}
				_ = c.Process.Kill()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("lost call did not end")
				}
			} else {
				op := structured(t, callTool(t, cs, "request_execution", map[string]any{"plan_handle": id}))
				if op["state"] != "running" {
					t.Fatal(op)
				}
				waitFor(t, "worker entry", func() bool { return fileExists(filepath.Join(f.Home, "mutation-entered")) })
				switch shutdown {
				case "EOF":
					_ = cs.Close()
				case "SIGINT":
					_ = c.Process.Signal(os.Interrupt)
				case "SIGTERM":
					_ = c.Process.Signal(syscall.SIGTERM)
				case "SIGKILL":
					_ = c.Process.Kill()
				}
			}
			requireOperation(t, s, id, "running")
			// A reconnect queries the original ID and replays the original request. It
			// must reuse the running worker rather than re-apply the plan.
			cs2, _ := connectProcess(t, f.Home, "server")
			before, e := os.ReadFile(filepath.Join(f.Home, "worker-pid"))
			if e != nil {
				t.Fatal(e)
			}
			op := structured(t, callTool(t, cs2, "request_execution", map[string]any{"plan_handle": id}))
			if op["state"] != "running" {
				t.Fatal(op)
			}
			after, _ := os.ReadFile(filepath.Join(f.Home, "worker-pid"))
			if !bytes.Equal(before, after) {
				t.Fatal("reconnect launched another worker")
			}
			writeTest(t, filepath.Join(f.Home, "release-worker"), []byte("continue"), 0600)
			waitFor(t, "worker finished result", func() bool { o, e := s.operation(id); return e == nil && o.State == "finished" })
			final := requireOperation(t, s, id, "finished")
			if final.Result == nil || len(final.Result.Results) != 1 || final.Result.Results[0].Status != "updated" {
				t.Fatalf("missing final update: %+v", final)
			}
		})
	}
}
func TestServerEOFCancelsUnapprovedRequest(t *testing.T) {
	f := newFixture(t, 1)
	persistFixture(t, f)
	s := f.service(t)
	id := savePlan(t, s, f.Plan)
	c := helperCommand(f.Home, "wait-approval")
	stdin, e := c.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	stdout, e := c.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	c.Stderr = os.Stderr
	if e = c.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(context.Background(), &mcp.IOTransport{Reader: stdout, Writer: stdin}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cs.Close() })
	done := make(chan struct{})
	go func() {
		_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_execution", Arguments: map[string]any{"plan_handle": id}})
		close(done)
	}()
	waitFor(t, "native confirmation entered", func() bool { return fileExists(filepath.Join(f.Home, "approval-entered")) })
	_ = stdin.Close()
	waitFor(t, "unapproved confirmation context cancelled", func() bool { return fileExists(filepath.Join(f.Home, "approval-cancelled")) })
	if fileExists(filepath.Join(f.Home, "worker-pid")) || fileExists(filepath.Join(f.Home, "mutation-entered")) {
		t.Fatal("EOF started mutation")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request did not return")
	}
}
func TestRequestRejectsForgedDigest(t *testing.T) {
	f := newFixture(t, 1)
	s := f.service(t)
	p := f.Plan
	p.Items[0].Name = "edited"
	id := savePlan(t, s, p)
	s.confirm = func(context.Context, privileged.PlanConfirmation) (bool, error) {
		t.Fatal("edited plan reached confirmation")
		return true, nil
	}
	op, e := s.request(context.Background(), id)
	if e != nil || op.State != "not_started" {
		t.Fatalf("op=%+v err=%v", op, e)
	}
}

func TestWorkerUsesCLIGlobalLockAndStopsAfterCheckpointFailure(t *testing.T) {
	t.Run("global-lock", func(t *testing.T) {
		f := newFixture(t, 1)
		s := f.service(t)
		ids := []string{savePlan(t, s, f.Plan), savePlan(t, s, f.Plan)}
		for _, id := range ids {
			if e := s.store.write(id, "decision", decision{PlanID: f.Plan.PlanID, Approved: true}, true); e != nil {
				t.Fatal(e)
			}
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		var calls atomic.Int32
		engine := f.engine(runnerFunc(func(context.Context, manage.Command) ([]byte, error) {
			calls.Add(1)
			close(entered)
			<-release
			return nil, errors.New("controlled manager failure")
		}))
		done := make(chan error, 1)
		go func() {
			done <- runWorker(context.Background(), s.store, ids[0], func(manage.Plan) manage.Engine { return engine })
		}()
		<-entered
		e := runWorker(context.Background(), s.store, ids[1], func(manage.Plan) manage.Engine { return engine })
		if e == nil || calls.Load() != 1 {
			t.Fatalf("parallel worker escaped Apply lock: calls=%d err=%v", calls.Load(), e)
		}
		// Direct CLI execution uses exactly the same state directory and lock.
		_, e = manage.Apply(context.Background(), f.Plan, f.Plan.PlanID, engine)
		if e == nil || calls.Load() != 1 {
			t.Fatalf("CLI escaped worker lock: calls=%d err=%v", calls.Load(), e)
		}
		close(release)
		if e = <-done; e != nil {
			t.Fatal(e)
		}
	})
	t.Run("checkpoint-failure", func(t *testing.T) {
		f := newFixture(t, 2)
		s := f.service(t)
		id := savePlan(t, s, f.Plan)
		_ = s.store.write(id, "decision", decision{PlanID: f.Plan.PlanID, Approved: true}, true)
		calls := 0
		engine := f.engine(runnerFunc(func(context.Context, manage.Command) ([]byte, error) {
			calls++
			p, e := s.store.path(id, "operation")
			if e != nil {
				return nil, e
			}
			if e = os.Chmod(p, 0644); e != nil {
				return nil, e
			}
			return nil, errors.New("controlled manager failure")
		}))
		e := runWorker(context.Background(), s.store, id, func(manage.Plan) manage.Engine { return engine })
		if e == nil || calls != 1 {
			t.Fatalf("checkpoint failure continued later items: calls=%d err=%v", calls, e)
		}
		if _, e = s.operation(id); e == nil {
			t.Fatal("unsafe failed journal reported success")
		}
	})
}
func TestCrashedWorkerIsUnknownAndNeverAutomaticallyRetried(t *testing.T) {
	f := newFixture(t, 1)
	persistFixture(t, f)
	s := f.service(t)
	id := savePlan(t, s, f.Plan)
	cs, _ := connectProcess(t, f.Home, "server")
	op := structured(t, callTool(t, cs, "request_execution", map[string]any{"plan_handle": id}))
	if op["state"] != "running" {
		t.Fatal(op)
	}
	waitFor(t, "manager entry", func() bool { return fileExists(filepath.Join(f.Home, "mutation-entered")) })
	pidBytes, e := os.ReadFile(filepath.Join(f.Home, "worker-pid"))
	if e != nil {
		t.Fatal(e)
	}
	var pid int
	if _, e = fmt.Sscan(string(pidBytes), &pid); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Kill(pid, syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	waitFor(t, "unknown crashed worker", func() bool { o, e := s.operation(id); return e == nil && o.State == "unknown" })
	for n := 0; n < 3; n++ {
		op = structured(t, callTool(t, cs, "request_execution", map[string]any{"plan_handle": id}))
		if op["state"] != "unknown" {
			t.Fatal(op)
		}
	}
	after, _ := os.ReadFile(filepath.Join(f.Home, "worker-pid"))
	if !bytes.Equal(pidBytes, after) {
		t.Fatal("crashed operation restarted")
	}
	if fileExists(filepath.Join(f.Home, "release-worker")) {
		t.Fatal("unexpected release")
	}
}

func TestSDKRequestCancellationStopsPendingApproval(t *testing.T) {
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			id := savePlan(t, s, f.Plan)
			entered := make(chan struct{})
			cancelled := make(chan struct{})
			var starts atomic.Int32
			s.confirm = func(ctx context.Context, _ privileged.PlanConfirmation) (bool, error) {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				return false, ctx.Err()
			}
			s.start = func(string) error { starts.Add(1); return nil }
			cs := connectMemory(t, s, protocol)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "request_execution", Arguments: map[string]any{"plan_handle": id}})
				done <- e
			}()
			<-entered
			cancel()
			select {
			case <-cancelled:
			case <-time.After(3 * time.Second):
				t.Fatal("SDK cancellation did not reach native approval")
			}
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("cancelled client request succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("client cancellation did not return")
			}
			if starts.Load() != 0 {
				t.Fatal("cancelled approval started mutation")
			}
		})
	}
}

func TestPlanAliasIsStableAndPrivateCacheIsBounded(t *testing.T) {
	f := newFixture(t, 1)
	s := f.service(t)
	first, e := s.savePlan(f.Plan)
	if e != nil {
		t.Fatal(e)
	}
	if !validHandle(first) || first == f.Plan.PlanID {
		t.Fatal("invalid alias")
	}
	var wg sync.WaitGroup
	results := make(chan string, 16)
	errs := make(chan error, 16)
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); id, e := s.savePlan(f.Plan); results <- id; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for id := range results {
		if id != first {
			t.Fatalf("same plan acquired multiple handles: %s %s", first, id)
		}
	}
	var stored manage.Plan
	if e = s.store.read(first, "plan", &stored); e != nil || stored.PlanID != f.Plan.PlanID {
		t.Fatalf("alias lost original plan: %v", e)
	}

}
func TestOperationRejectsDamagedPlanResultBindings(t *testing.T) {
	for _, mode := range []string{"handle", "plan", "result"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			id := savePlan(t, s, f.Plan)
			op := Operation{ID: id, PlanHandle: id, PlanID: f.Plan.PlanID, State: "finished", Result: &manage.ApplyResult{PlanID: f.Plan.PlanID}}
			switch mode {
			case "handle":
				op.PlanHandle = strings.Repeat("1", 64)
			case "plan":
				op.PlanID = strings.Repeat("1", 64)
			case "result":
				op.Result.PlanID = strings.Repeat("1", 64)
			}
			if e := s.store.write(id, "operation", op, true); e != nil {
				t.Fatal(e)
			}
			if _, e := s.operation(id); e == nil {
				t.Fatalf("damaged %s binding accepted", mode)
			}
		})
	}
}

func TestSavedPlanAliasRejectsDamage(t *testing.T) {
	for _, mode := range []string{"invalid-handle", "other-plan", "edited-plan"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			id, e := s.savePlan(f.Plan)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "invalid-handle":
				e = s.store.write(f.Plan.PlanID, "plan-handle", "../other", false)
			case "other-plan":
				other := f.Plan
				other.PlanID = strings.Repeat("2", 64)
				otherID := savePlan(t, s, other)
				e = s.store.write(f.Plan.PlanID, "plan-handle", otherID, false)
			case "edited-plan":
				edited := f.Plan
				edited.Items[0].Name = "modified"
				e = s.store.write(id, "plan", edited, false)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.savePlan(f.Plan); e == nil {
				t.Fatalf("damaged alias %s reused", mode)
			}
		})
	}
}
