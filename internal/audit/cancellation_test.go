package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScanCancellationStopsQueriesAndDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := New(Config{Context: ctx, Home: t.TempDir(), Timeout: time.Minute})
	start := time.Now()
	if _, err := s.run("/bin/sleep", "10"); !errors.Is(err, context.Canceled) {
		t.Fatalf("query ignored cancellation: %v", err)
	}
	report := s.Scan(true)
	if len(report.Items) != 0 || time.Since(start) > time.Second {
		t.Fatal("cancelled scan continued discovery")
	}
	found := false
	for _, diagnostic := range report.Diagnostics {
		found = found || diagnostic.Code == "scan_cancelled"
	}
	if !found {
		t.Fatal("cancelled scan was reported as complete")
	}
}
