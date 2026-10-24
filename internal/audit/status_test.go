package audit

import "testing"

func TestScanDistinguishesUnqueriedVersionFromMissingSource(t *testing.T) {
	s := New(Config{})
	queryable := item("node-package", "example", "/example/package", "npm")
	queryable.lookup = "example"
	missing := item("application", "Example", "/Example.app", "standalone")
	s.add(queryable)
	s.add(missing)
	s.finish()
	for _, i := range s.Report.Items {
		if i.ID == queryable.ID && i.Update.Code != "online_query_required" {
			t.Fatal("known version lookup presented as missing source", i.Update)
		}
		if i.ID == missing.ID && i.Update.Code != "no_update_source" {
			t.Fatal("invented a version lookup", i.Update)
		}
		if i.Update.Status != "unknown" {
			t.Fatal("unqueried source was treated as a confirmed version")
		}
	}
}
