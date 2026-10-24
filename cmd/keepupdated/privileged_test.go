package main

import (
	"bytes"
	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"testing"
)

func TestPreparationRequiresExactSupportedInventoryTarget(t *testing.T) {
	app := audit.Item{ID: "app", Kind: "application", Path: "/Applications/Fixture.app", Identity: "com.example.fixture", Version: "1.0", Owner: "independent"}
	system := audit.Item{ID: "os", Kind: "os", Path: "/System/Library/CoreServices/SystemVersion.plist", Version: "13.6.6", Owner: "system", Source: "apple"}
	report := audit.Report{Items: []audit.Item{app, system}}
	p, err := preparationTarget(report, "app", "/example/update.pkg")
	if err != nil || p.InstallationPath != app.Path || p.BundleID != app.Identity || p.FromVersion != app.Version || p.Kind != "package" {
		t.Fatal(p, err)
	}
	p, err = preparationTarget(report, "os", "")
	if err != nil || p.Kind != "system" {
		t.Fatal(p, err)
	}
	for _, id := range []string{"missing", "app"} {
		if _, err := preparationTarget(report, id, ""); err == nil {
			t.Fatal("unsupported target", id)
		}
	}
	report.Items = append(report.Items, app)
	if _, err := preparationTarget(report, "app", "/example/update.pkg"); err == nil {
		t.Fatal("ambiguous ID")
	}
}
func TestPreparationHelpAndInvalidArgumentsDoNotScan(t *testing.T) {
	for _, args := range [][]string{{"special", "prepare", "--id", "a", "--id", "b"}, {"special", "prepare"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatal(code, out.String(), errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if run([]string{"special", "prepare", "--help"}, &out, &errOut) != 0 {
		t.Fatal(errOut.String())
	}
}
