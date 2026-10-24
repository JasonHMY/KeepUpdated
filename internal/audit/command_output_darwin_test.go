package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplicationMetadataOutputLimitIsolated(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Oversized", "Normal"} {
		contents := filepath.Join(dir, name+".app", "Contents")
		if err := os.MkdirAll(contents, 0700); err != nil {
			t.Fatal(err)
		}
		metadata := map[string]string{
			"CFBundleIdentifier":         "com.example." + strings.ToLower(name),
			"CFBundleShortVersionString": "1.0.0",
		}
		if name == "Oversized" {
			metadata["Padding"] = strings.Repeat("x", 9<<20)
		}
		raw, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(contents, "Info.plist"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := New(Config{Home: dir, AppRoots: []string{dir}, Timeout: 10 * time.Second})
	s.apps()
	if len(s.Report.Items) != 2 {
		t.Fatalf("discovery lost an item: %d", len(s.Report.Items))
	}
	for _, item := range s.Report.Items {
		if item.Name == "Oversized" {
			if item.Health.Status != "error" || item.Health.Code != "bundle_metadata_unreadable" || item.Version != "" || item.Update.Status != "unknown" {
				t.Fatalf("oversized plutil output accepted: %+v", item)
			}
		} else if item.Health.Status != "ok" || item.Health.Code != "bundle_metadata_readable" || item.Version != "1.0.0" {
			t.Fatalf("unrelated item failed: %+v", item)
		}
	}
}
