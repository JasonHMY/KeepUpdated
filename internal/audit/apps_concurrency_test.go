package audit

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestApplicationMetadataBoundedAndDeterministic(t *testing.T) {
	var applications []Item
	for n := 0; n < 12; n++ {
		applications = append(applications, item("application", fmt.Sprint(n), fmt.Sprintf("/fixture/%02d.app", n), "standalone"))
	}
	var active, maximum atomic.Int32
	started := make(chan struct{}, len(applications))
	release := make(chan struct{})
	inspect := func(s *Scanner, i Item) Item {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		started <- struct{}{}
		<-release
		i.Health = result("ok", "bundle_metadata_readable")
		s.Report.Queries = append(s.Report.Queries, i.Name)
		return i
	}
	s := New(Config{})
	done := make(chan struct{})
	go func() { s.inspectApplications(applications, inspect); close(done) }()
	for n := 0; n < 4; n++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("independent metadata reads did not start concurrently")
		}
	}
	if got := active.Load(); got != 4 {
		t.Fatalf("active queries = %d, want bounded pool of 4", got)
	}
	close(release)
	<-done
	if maximum.Load() != 4 || len(s.Report.Items) != len(applications) {
		t.Fatalf("concurrency or inventory mismatch: %d / %d", maximum.Load(), len(s.Report.Items))
	}
	for n, i := range s.Report.Items {
		if i.ID != applications[n].ID || s.Report.Queries[n] != applications[n].Name {
			t.Fatal("concurrent completion changed discovery order")
		}
	}
}

func TestApplicationMetadataCancellationStopsPendingWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var events []Progress
	s := New(Config{Context: ctx, Progress: func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, p)
		if p.Current != "" {
			cancel()
		}
	}})
	applications := make([]Item, 20)
	for n := range applications {
		applications[n] = item("application", fmt.Sprint(n), "", "standalone")
	}
	var calls atomic.Int32
	s.inspectApplications(applications, func(_ *Scanner, i Item) Item { calls.Add(1); return i })
	if calls.Load() != 0 {
		t.Fatal("query started after cancellation from its progress event")
	}
	last := events[len(events)-1]
	if last.State != "cancelled" || last.Completed != 0 || *last.Total != len(applications) {
		t.Fatalf("cancelled work was counted as processed: %+v", last)
	}
}

func TestApplicationMetadataControlledLatency(t *testing.T) {
	// Synthetic read latency isolates scheduler behavior from host inventory and I/O.
	var applications []Item
	for n := 0; n < 12; n++ {
		applications = append(applications, item("application", fmt.Sprint(n), "", "standalone"))
	}
	inspect := func(s *Scanner, i Item) Item {
		time.Sleep(20 * time.Millisecond)
		i.Health = result("ok", "bundle_metadata_readable")
		s.Report.Queries = append(s.Report.Queries, i.Name)
		return i
	}
	serial := New(Config{})
	start := time.Now()
	for _, i := range applications {
		serial.add(inspect(serial, i))
	}
	serialTime := time.Since(start)
	parallel := New(Config{})
	start = time.Now()
	parallel.inspectApplications(applications, inspect)
	parallelTime := time.Since(start)
	if !reflect.DeepEqual(serial.Report.Items, parallel.Report.Items) || !reflect.DeepEqual(serial.Report.Queries, parallel.Report.Queries) {
		t.Fatal("parallel reads changed inventory or query coverage")
	}
	t.Logf("12 synthetic 20ms metadata reads: serial=%s parallel=%s; calls=%d/%d", serialTime, parallelTime, len(serial.Report.Queries), len(parallel.Report.Queries))
}
