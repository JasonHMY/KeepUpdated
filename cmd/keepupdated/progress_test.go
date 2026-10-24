package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestProgressTransportPreservesReportAndFinalEvent(t *testing.T) {
	var out, errOut bytes.Buffer
	emit := progressWriter(&errOut)
	total := 1000
	event := audit.Progress{Schema: "keepupdated/progress/v1", Phase: "updates", Total: &total, State: "running", Current: strings.Repeat("x", 1000) + "\n"}
	emit(event)
	for n := 1; n <= total; n++ {
		event.Completed = n
		emit(event)
	}
	event.State = "finished"
	emit(event)
	report := audit.Report{Schema: audit.Schema, Items: []audit.Item{}}
	if err := renderReport(&out, "json", report); err != nil {
		t.Fatal(err)
	}
	var decoded audit.Report
	if json.Unmarshal(out.Bytes(), &decoded) != nil || decoded.Schema != audit.Schema {
		t.Fatal("report polluted")
	}
	lines := strings.Split(strings.TrimSpace(errOut.String()), "\n")
	if len(lines) > 10 {
		t.Fatalf("progress was not throttled: %d", len(lines))
	}
	var first, last audit.Progress
	if json.Unmarshal([]byte(lines[0]), &first) != nil || json.Unmarshal([]byte(lines[len(lines)-1]), &last) != nil {
		t.Fatal("invalid JSON")
	}
	if first.Completed != 0 || len(first.Current) > 256 || last.Completed != total || last.State != "finished" {
		t.Fatal(first, last)
	}
}

func TestProgressPendingCountArrivesDuringNextSlowWork(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	emit := progressWriter(writer)
	received := make(chan audit.Progress, 10)
	go func() {
		decoder := json.NewDecoder(reader)
		for {
			var p audit.Progress
			if decoder.Decode(&p) != nil {
				return
			}
			received <- p
		}
	}()
	total := 3
	p := audit.Progress{Schema: "keepupdated/progress/v1", Phase: "updates", Total: &total, State: "running", Current: "first"}
	emit(p)
	<-received
	p.Completed = 1
	p.Current = "slow second query"
	emit(p)
	select {
	case got := <-received:
		if got.Completed != 1 || got.Current != p.Current {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("pending progress waited for next work completion")
	}
	p.State = "cancelled"
	emit(p)
}

func TestProgressSequenceCoversConcurrentAndRepeatedPhases(t *testing.T) {
	var out bytes.Buffer
	emit := progressWriter(&out)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			emit(audit.Progress{Schema: "keepupdated/progress/v1", Phase: "scan", State: "finished"})
		}()
	}
	wg.Wait()
	emit(audit.Progress{Schema: "keepupdated/progress/v1", Phase: "verify", State: "finished"})
	emit(audit.Progress{Schema: "keepupdated/progress/v1", Phase: "scan", State: "cancelled"})
	decoder := json.NewDecoder(&out)
	var previous uint64
	for decoder.More() {
		var p audit.Progress
		if err := decoder.Decode(&p); err != nil {
			t.Fatal(err)
		}
		if p.Sequence != previous+1 {
			t.Fatal("transport sequence regressed", p)
		}
		previous = p.Sequence
	}
	if previous != 10 {
		t.Fatalf("lost concurrent events: %d", previous)
	}
}

func TestManagementDependencyScopePreservesConsumerGuards(t *testing.T) {
	r := audit.Report{Items: []audit.Item{
		{ID: "app", Kind: "application", Related: []string{"manager"}},
		{ID: "manager", Kind: "cask", Related: []string{"alias"}},
		{ID: "alias", Kind: "cask"},
		{ID: "other", Kind: "node-package"},
		{ID: "runtime", Kind: "formula", Owner: "homebrew", Name: "node"},
		{ID: "versioned-runtime", Kind: "formula", Owner: "homebrew", Name: "python@3.12"},
	}}
	scope := managementDependencyScope(r, manage.Selection{Action: "update", IDs: []string{"app"}})
	if scope == nil || !scope(r.Items[0]) || !scope(r.Items[1]) || !scope(r.Items[2]) || scope(r.Items[3]) {
		t.Fatal("selected aliases were not scoped correctly")
	}
	for _, selection := range []manage.Selection{
		{Action: "uninstall", IDs: []string{"app"}},
		{Action: "update", All: true},
		{Action: "update", IDs: []string{"runtime"}},
		{Action: "update", IDs: []string{"versioned-runtime"}},
	} {
		if managementDependencyScope(r, selection) != nil {
			t.Fatal("consumer dependency guards narrowed", selection)
		}
	}
}

func TestManageProgressFlagKeepsArgumentDiagnostics(t *testing.T) {
	for _, command := range []string{"plan", "apply"} {
		var out, stderr bytes.Buffer
		if run([]string{command, "--progress"}, &out, &stderr) != 2 || out.Len() != 0 || strings.Contains(stderr.String(), "flag provided but not defined") || !strings.Contains(stderr.String(), "select an action") {
			t.Fatalf("%s: %s", command, stderr.String())
		}
	}
}
