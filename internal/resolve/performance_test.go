package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

type delayedManager struct {
	root            string
	mu              sync.Mutex
	calls           map[string]int
	active, maximum int
	delay           time.Duration
	started         chan struct{}
	once            sync.Once
}

func (f *delayedManager) Run(ctx context.Context, c manage.Command) ([]byte, error) {
	f.mu.Lock()
	f.calls[strings.Join(c.Args[:1], " ")]++
	if c.Args[0] == "config" {
		f.calls["config:"+c.Args[2]]++
	}
	f.active++
	if f.active > f.maximum {
		f.maximum = f.active
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(f.delay):
	}
	switch c.Args[0] {
	case "--version":
		return []byte("11.19.1"), nil
	case "root":
		return []byte(f.root), nil
	case "ls":
		deps := map[string]any{}
		for index := 0; index < 8; index++ {
			deps[fmt.Sprintf("fixture-%d", index)] = map[string]string{"version": "1.0.0"}
		}
		return json.Marshal(map[string]any{"dependencies": deps})
	case "config":
		if c.Args[2] == "tag" {
			return []byte("latest"), nil
		}
		if c.Args[2] == "registry" {
			return []byte("https://registry.example.invalid/"), nil
		}
		return []byte("undefined"), nil
	case "view":
		name := strings.TrimSuffix(c.Args[1], "@latest")
		return json.Marshal(map[string]string{"name": name, "version": "2.0.0"})
	}
	return nil, fmt.Errorf("unexpected command")
}

func performanceFixture(t *testing.T) (Resolver, audit.Report, *delayedManager) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(home, "prefix")
	root := filepath.Join(prefix, "lib/node_modules")
	manager := filepath.Join(prefix, "bin/npm")
	if err := os.MkdirAll(filepath.Dir(manager), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager, []byte("controlled manager"), 0755); err != nil {
		t.Fatal(err)
	}
	report := audit.Report{}
	for index := 0; index < 8; index++ {
		name := fmt.Sprintf("fixture-%d", index)
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "package.json"), []byte(fmt.Sprintf(`{"name":%q,"version":"1.0.0"}`, name)), 0644); err != nil {
			t.Fatal(err)
		}
		report.Items = append(report.Items, audit.Item{ID: name, Kind: "node-package", Name: name, Identity: name, Owner: "npm", Source: "npm-public", Path: path, Version: "1.0.0"})
	}
	alias := report.Items[0]
	alias.ID = "presentation-alias"
	report.Items = append(report.Items, alias)
	runner := &delayedManager{root: root, calls: map[string]int{}, delay: 5 * time.Millisecond}
	return Resolver{Options: manage.Options{Home: home, ManagerPaths: map[string]string{"npm": manager}}, Runner: runner, Timeout: time.Second}, report, runner
}

func TestResolverSharesScopeQueriesAndDeduplicatesAliases(t *testing.T) {
	r, report, runner := performanceFixture(t)
	selection := manage.Selection{Action: "update", All: true}
	// The previous four-worker package loop queried every presentation row,
	// including the same manager root/list/config once for every row.
	beforeStart := time.Now()
	before := make(map[string]manage.ResolvedTarget)
	var baselineMu sync.Mutex
	var workers sync.WaitGroup
	queue := make(chan audit.Item)
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range queue {
				target, _ := r.resolvePackage(context.Background(), item, report, selection)
				baselineMu.Lock()
				before[item.ID] = target
				baselineMu.Unlock()
			}
		}()
	}
	for _, item := range report.Items {
		queue <- item
	}
	close(queue)
	workers.Wait()
	beforeDuration := time.Since(beforeStart)
	beforeCalls := runner.calls
	runner.calls = map[string]int{}
	runner.maximum = 0
	var events []audit.Progress
	r.Options.Progress = func(p audit.Progress) { events = append(events, p) }
	start := time.Now()
	after, err := r.Resolve(context.Background(), report, selection)
	afterDuration := time.Since(start)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal(after, err)
	}
	if runner.maximum < 2 || runner.maximum > 4 {
		t.Fatal("unbounded or absent read concurrency", runner.maximum)
	}
	for _, query := range []string{"--version", "root", "ls", "config:registry", "config:userconfig", "config:globalconfig", "config:tag"} {
		if beforeCalls[query] != 9 || runner.calls[query] != 1 {
			t.Fatal(query, beforeCalls, runner.calls)
		}
	}
	if runner.calls["view"] != 8 || beforeCalls["view"] != 9 {
		t.Fatal("candidate aliases queried twice", beforeCalls, runner.calls)
	}
	last := events[len(events)-1]
	if last.State != "finished" || last.Total == nil || last.Completed != *last.Total {
		t.Fatal(events)
	}
	t.Logf("controlled 9-row / 8-installation fixture, 5 ms/query: before=%s 72 queries, after=%s 15 queries, maximum concurrent queries=%d", beforeDuration, afterDuration, runner.maximum)
	// A new resolution must not reuse the first session's source observations.
	_, err = r.Resolve(context.Background(), report, selection)
	if err != nil || runner.calls["root"] != 2 || runner.calls["config:registry"] != 2 {
		t.Fatal("session survived a fresh resolution", runner.calls, err)
	}
}

func TestResolverCancellationStopsProgressAndQueuedWork(t *testing.T) {
	r, report, runner := performanceFixture(t)
	runner.delay = time.Minute
	runner.started = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	var events []audit.Progress
	r.Options.Progress = func(p audit.Progress) { events = append(events, p) }
	done := make(chan error, 1)
	go func() { _, err := r.Resolve(ctx, report, manage.Selection{Action: "update", All: true}); done <- err }()
	<-runner.started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled resolution succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation stalled")
	}
	last := events[len(events)-1]
	if last.State != "cancelled" || last.Total == nil || last.Completed == *last.Total {
		t.Fatal(events)
	}
	count := len(events)
	time.Sleep(20 * time.Millisecond)
	if len(events) != count || runner.active != 0 || runner.maximum > 4 {
		t.Fatal("late progress or query", len(events), count, runner.active, runner.maximum)
	}
}

func TestResolverAliasesKeepIndependentManagerScopesAndInvalidBindings(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sharedRoot := filepath.Join(home, "shared/node_modules")
	sharedPackage := filepath.Join(sharedRoot, "fixture")
	if err := os.MkdirAll(sharedPackage, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedPackage, "package.json"), []byte(`{"name":"fixture","version":"1.0.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	report := audit.Report{}
	managers := map[string]string{}
	for _, scope := range []string{"first", "second"} {
		prefix := filepath.Join(home, scope)
		if err := os.MkdirAll(filepath.Join(prefix, "lib"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sharedRoot, filepath.Join(prefix, "lib/node_modules")); err != nil {
			t.Fatal(err)
		}
		manager := filepath.Join(prefix, "bin/npm")
		if err := os.WriteFile(manager, []byte("isolated manager "+scope), 0755); err != nil {
			t.Fatal(err)
		}
		managers[manager] = prefix
		report.Items = append(report.Items, audit.Item{ID: scope, Kind: "node-package", Name: "fixture", Identity: "fixture", Owner: "npm", Source: "npm-public", Version: "1.0.0", Path: filepath.Join(prefix, "lib/node_modules/fixture")})
	}
	invalid := report.Items[0]
	invalid.ID, invalid.Path = "invalid", filepath.Join(home, "invalid-layout/fixture")
	if err := os.MkdirAll(filepath.Dir(invalid.Path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sharedPackage, invalid.Path); err != nil {
		t.Fatal(err)
	}
	// Put the invalid alias first so an implementation that deduplicates by real
	// path cannot reuse either its failure or a different scope's candidate.
	report.Items = append([]audit.Item{invalid}, report.Items...)
	var mu sync.Mutex
	candidates := map[string]int{}
	r := Resolver{Options: manage.Options{Home: home}, Runner: queryRunner(func(c manage.Command) ([]byte, error) {
		prefix, ok := managers[c.Path]
		if !ok {
			t.Errorf("queried an unverified manager")
			return nil, fmt.Errorf("unknown manager")
		}
		switch c.Args[0] {
		case "--version":
			return []byte("11.19.1"), nil
		case "root":
			return []byte(filepath.Join(prefix, "lib/node_modules")), nil
		case "ls":
			return []byte(`{"dependencies":{"fixture":{"version":"1.0.0"}}}`), nil
		case "config":
			if c.Args[2] == "tag" {
				return []byte("latest"), nil
			}
			if c.Args[2] == "registry" {
				return []byte("https://" + filepath.Base(prefix) + ".example.invalid/"), nil
			}
			return []byte("undefined"), nil
		case "view":
			mu.Lock()
			candidates[c.Path]++
			mu.Unlock()
			version := "2.0.0"
			if filepath.Base(prefix) == "second" {
				version = "3.0.0"
			}
			return json.Marshal(map[string]string{"name": "fixture", "version": version})
		}
		return nil, fmt.Errorf("unexpected query")
	})}
	out, err := r.Resolve(context.Background(), report, manage.Selection{Action: "update", All: true})
	if err != nil || out["invalid"].ResolutionError == "" {
		t.Fatal(out, err)
	}
	for _, scope := range []string{"first", "second"} {
		prefix := filepath.Join(home, scope)
		got := out[scope]
		version := "2.0.0"
		if scope == "second" {
			version = "3.0.0"
		}
		if !got.OriginVerified || got.Version != version || got.Scope != prefix || got.ManagerPath != filepath.Join(prefix, "bin/npm") || candidates[got.ManagerPath] != 1 {
			t.Fatal(scope, got, candidates)
		}
	}
	if out["first"].Source == out["second"].Source {
		t.Fatal("source observations crossed scopes", out)
	}
}
