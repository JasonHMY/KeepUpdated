package audit

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestSystemUpdateOutputVariants(t *testing.T) {
	for _, tc := range []struct{ output, status string }{{"Software Update Tool\nFinding available software\nNo new software available.", "current"}, {"Software Update found the following new or updated software:\n* Label: example-update\n", "available"}, {"Authentication is required.", "unknown"}, {"", "unknown"}} {
		if r := parseSystemUpdateList(tc.output); r.Status != tc.status {
			t.Fatal(tc, r)
		}
	}
}
func TestLiveSystemUpdateQuery(t *testing.T) {
	if os.Getenv("KEEPUPDATED_LIVE_SYSTEM_TEST") != "1" || runtime.GOOS != "darwin" {
		t.Skip("explicit opt-in macOS query")
	}
	c := DefaultConfig()
	c.SystemUpdates = true
	c.Timeout = 90 * time.Second
	s := New(c)
	s.system()
	if len(s.Report.Items) != 1 {
		t.Fatal(s.Report.Items)
	}
	i := s.Report.Items[0]
	if i.Update.Status != "current" && i.Update.Status != "available" {
		t.Fatal("query did not return a recognized catalog result", i.Update)
	}
	t.Log("system update list parsed:", i.Update.Code)
}
