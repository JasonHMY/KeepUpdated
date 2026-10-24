package audit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdateProgressBeforeSlowQueriesFinish(t *testing.T) {
	events := make(chan Progress, 100)
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	s := New(Config{Online: true, Timeout: time.Second, Progress: func(p Progress) { events <- p }})
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-release
		if strings.Contains(r.URL.Path, "broken") {
			return nil, errors.New("controlled failure")
		}
		return response(200, `{"name":"example","version":"2.0"}`), nil
	})
	for _, name := range []string{"one", "two", "three", "broken"} {
		s.Report.Items = append(s.Report.Items, Item{Name: name, Identity: "example", Version: "1.0", Source: "npm-public", lookup: name})
	}
	s.Report.Items = append(s.Report.Items, Item{Name: "unsupported", Update: result("unknown", "unsupported")})
	done := make(chan struct{})
	go func() { s.check(); close(done) }()
	select {
	case p := <-events:
		if p.Phase != "updates" || p.Completed != 0 || *p.Total != 4 {
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		t.Fatal("no progress before query completion")
	}
	for n := 0; n < 4; n++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	select {
	case <-done:
		t.Fatal("slow checks unexpectedly finished")
	default:
	}
	close(release)
	<-done
	close(events)
	previous, last := 0, Progress{}
	for p := range events {
		if p.Completed < previous {
			t.Fatal("regressed", p)
		}
		previous, last = p.Completed, p
	}
	if last.State != "finished" || last.Completed != 4 || last.Unresolved != 1 {
		t.Fatal(last)
	}
	if s.Report.Items[4].Update.Code != "unsupported" {
		t.Fatal("skipped item changed")
	}
}

func TestUpdateCancellationDoesNotCompleteQueuedWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var events []Progress
	s := New(Config{Context: ctx, Online: true, Timeout: time.Second, Progress: func(p Progress) { mu.Lock(); events = append(events, p); mu.Unlock() }})
	started := make(chan struct{}, 4)
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	for n := 0; n < 12; n++ {
		s.Report.Items = append(s.Report.Items, Item{Name: "example", Source: "npm-public", lookup: "example"})
	}
	done := make(chan struct{})
	go func() { s.check(); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
	last := events[len(events)-1]
	if last.State != "cancelled" || last.Completed >= *last.Total {
		t.Fatal(last)
	}
}

func TestScanCancellationEmitsActualStageAndNoCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []Progress
	s := New(Config{Context: ctx, Home: t.TempDir(), Timeout: time.Second, Progress: func(p Progress) {
		events = append(events, p)
		if p.Current == "apps" {
			cancel()
		}
	}})
	r := s.Scan(true)
	last := events[len(events)-1]
	if last.Phase != "scan" || last.State != "cancelled" || last.Completed != 1 || *last.Total != 13 {
		t.Fatal(last)
	}
	if len(r.Diagnostics) == 0 || r.Diagnostics[len(r.Diagnostics)-1].Code != "scan_cancelled" {
		t.Fatal(r.Diagnostics)
	}
}

func TestDependenciesAndSkillGroupsExposeWorkAndUnresolvedResults(t *testing.T) {
	var events []Progress
	s := New(Config{Home: t.TempDir(), Online: true, Timeout: time.Second, Progress: func(p Progress) { events = append(events, p) }})
	s.Report.Items = []Item{{Name: "missing receipt", Kind: "formula", Path: t.TempDir(), ActiveVersion: "1.0", Health: result("ok", "")}}
	s.dependencies()
	last := events[len(events)-1]
	if last.Phase != "dependencies" || last.Completed != 1 || last.Unresolved != 1 {
		t.Fatal(last)
	}
	p, snap := sourceSkill(t, s, "example")
	attachSource(t, s, p, sourceReceipt(snap.tree), "tree")
	queryStarted := false
	s.feedClient.Transport = transport(func(r *http.Request) (*http.Response, error) {
		current := events[len(events)-1]
		if current.Phase != "skill-sources" || current.Completed != 0 || current.Current != "example/skills" {
			t.Fatal("query started before progress", current)
		}
		queryStarted = true
		return response(500, ""), nil
	})
	s.checkSkillSources()
	last = events[len(events)-1]
	if !queryStarted || last.State != "finished" || last.Completed != 1 || last.Unresolved != 1 {
		t.Fatal(last)
	}
}

func TestSharedPhaseProgressSerializesCountsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []Progress
	total := 40
	p := NewPhaseProgress(ctx, func(p Progress) { events = append(events, p) }, "resolve", &total)
	var wg sync.WaitGroup
	for n := 0; n < total; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.Begin("candidate"); p.Done(true) }()
	}
	wg.Wait()
	for _, event := range events {
		if event.Completed > total || event.Unresolved != event.Completed {
			t.Fatal("inconsistent concurrent counts", event)
		}
	}
	cancel()
	p.Done(false)
	p.Finish()
	p.Done(false)
	p.Finish()
	last := events[len(events)-1]
	if last.Completed != total || last.State != "cancelled" {
		t.Fatal(last)
	}
}

func TestManagementDependenciesKeepUnselectedInventory(t *testing.T) {
	s := New(Config{Home: t.TempDir()})
	s.Report.Items = []Item{
		{ID: "selected", Kind: "node-package", Name: "selected", Path: t.TempDir(), Health: result("ok", "")},
		{ID: "other", Kind: "node-package", Name: "other", Path: t.TempDir(), Health: result("ok", "")},
	}
	r := s.CheckManagementDependencies(func(i Item) bool { return i.ID == "selected" })
	if len(r.Items) != 2 {
		t.Fatal("management scope filtered inventory")
	}
	for _, i := range r.Items {
		if i.ID == "selected" && (len(i.Dependencies) != 1 || i.Dependencies[0].Code != "dependency_metadata_unreadable") {
			t.Fatal("selected dependency guard skipped", i)
		}
		if i.ID == "other" && len(i.Dependencies) != 0 {
			t.Fatal("unrelated dependency read", i)
		}
	}
}

func TestIncompletePhaseKeepsActualCountsAndReportsFailure(t *testing.T) {
	var last Progress
	total := 3
	p := NewPhaseProgress(context.Background(), func(event Progress) { last = event }, "resolve", &total)
	p.Done(true)
	p.Finish()
	if last.State != "failed" || last.Completed != 1 || last.Unresolved != 1 || *last.Total != 3 {
		t.Fatal("incomplete phase claimed completion", last)
	}
}
