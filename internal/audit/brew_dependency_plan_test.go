package audit_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func brewPrefixWrite(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func brewPrefixRoot(t *testing.T) string {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func brewPrefixFormula(t *testing.T, root, name, dependencies string) {
	t.Helper()
	keg := filepath.Join(root, "Cellar", name, "1.0.0")
	brewPrefixWrite(t, filepath.Join(keg, "INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"},"arch":"arm64","runtime_dependencies":[`+dependencies+`]}`, 0644)
	if err := os.MkdirAll(filepath.Join(root, "opt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keg, filepath.Join(root, "opt", name)); err != nil {
		t.Fatal(err)
	}
	brewPrefixWrite(t, filepath.Join(root, "bin/brew"), "#!/bin/sh\nexit 99\n", 0755)
}

func TestBrewCrossPrefixConsumerUninstallPlan(t *testing.T) {
	h := brewPrefixRoot(t)
	a, b := filepath.Join(h, "prefix-a"), filepath.Join(h, "prefix-b")
	brewPrefixFormula(t, a, "zlib", "")
	brewPrefixFormula(t, b, "zlib", "")
	config := audit.Config{Home: h, Path: filepath.Join(h, "empty-bin"), BrewRoots: []string{a, b}, Timeout: time.Second}
	options := manage.Options{Home: h, BrewRoots: config.BrewRoots, Now: time.Now()}
	baseline := audit.BrewDependencyFixtureReport(config)
	id := ""
	for _, item := range baseline.Items {
		if item.Path == filepath.Join(a, "Cellar/zlib") {
			id = item.ID
		}
	}
	if id == "" {
		t.Fatal("synthetic A/zlib was not read")
	}
	selection := manage.Selection{Action: "uninstall", IDs: []string{id}}
	plan, err := manage.NewPlan(baseline, selection, options)
	if err != nil || len(plan.Items) != 1 || plan.Items[0].Status != "ready" {
		t.Fatal("baseline is not executable", plan.Items, err)
	}
	t.Logf("baseline: %s / %s", plan.Items[0].Status, plan.Items[0].Reason)
	brewPrefixFormula(t, b, "consumer", `{"full_name":"zlib"}`)
	report := audit.BrewDependencyFixtureReport(config)
	plan, err = manage.NewPlan(report, selection, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("with B/consumer receipt: %s / %s; affected_ids=%v; queries=%v", plan.Items[0].Status, plan.Items[0].Reason, plan.Items[0].AffectedIDs, report.Queries)
	if plan.Items[0].Status != "ready" {
		t.Fatalf("independent prefix B consumer incorrectly blocks A/zlib: %s", plan.Items[0].Reason)
	}
}
