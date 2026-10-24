package manage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func runtimeFixture(t *testing.T) (*fixture, audit.Item) {
	f := setup(t)
	root := f.o.BrewRoots[0]
	f.write(filepath.Join(root, "bin/brew"), "fixture manager", 0755)
	i := audit.Item{ID: "runtime", Name: "node", Identity: "node", Kind: "formula", Owner: "homebrew", Path: filepath.Join(root, "Cellar/node"), Source: "homebrew/core", Version: "22.1.0", ActiveVersion: "22.1.0", Active: true, ActivityKnown: true, Health: audit.Result{Status: "ok"}}
	f.write(filepath.Join(i.Path, i.Version, "INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
	f.write(filepath.Join(i.Path, i.Version, "bin/node"), "original runtime", 0755)
	f.r.Items = []audit.Item{i, {ID: "consumer", Name: "consumer", Kind: "formula", Version: "1.0.0", Path: filepath.Join(root, "Cellar/consumer"), Dependencies: []audit.Dependency{{Name: "node", Status: "ok"}}}}
	f.r.Items = append(f.r.Items, audit.Item{ID: "active-executable", Kind: "executable", Name: "node", Path: filepath.Join(root, "bin/node"), RealPath: filepath.Join(i.Path, i.Version, "bin/node"), Active: true, ActivityKnown: true, Health: audit.Result{Status: "ok"}})
	f.o.ResolvedTargets = map[string]ResolvedTarget{i.ID: {Provider: "homebrew", ManagerPath: filepath.Join(root, "bin/brew"), Scope: root, Source: "homebrew/core:fixture", Version: "22.2.0", OriginVerified: true, RuntimeScopeVerified: true, RuntimeDependents: []string{"consumer"}}}
	return f, i
}

func TestRuntimeRequiresSeparateConsentAndSingleExplicitSelection(t *testing.T) {
	for _, s := range []Selection{
		{Action: "update", All: true},
		{Action: "update", All: true, Special: true, Consents: []string{"runtime_update"}},
		{Action: "update", IDs: []string{"runtime"}, Special: true},
		{Action: "update", IDs: []string{"runtime", "consumer"}, Consents: []string{"runtime_update"}},
	} {
		f, _ := runtimeFixture(t)
		p := f.plan(s)
		for _, pi := range p.Items {
			if pi.ID == "runtime" && pi.Status == "ready" {
				t.Fatalf("unsafe selection: %+v", s)
			}
		}
	}
	f, _ := runtimeFixture(t)
	p := f.plan(Selection{Action: "update", IDs: []string{"runtime"}, Special: true, Consents: []string{"runtime_update", "runtime_update"}})
	pi := p.Items[0]
	if pi.Status != "ready" || !pi.SpecialApproved || len(pi.RequiredConsents) != 1 || pi.RequiredConsents[0] != "runtime_update" || len(p.Selection.Consents) != 1 || pi.RuntimeReview == nil || len(pi.RuntimeReview.Consumers) != 1 || len(pi.RuntimeReview.UnknownImpact) == 0 {
		t.Fatalf("missing review: %+v", pi)
	}
	c, err := commandFor(pi, "update", f.o, f.state)
	if err != nil || strings.Join(c.Args, " ") != "upgrade --formula node" || !contains(c.Env, "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK=1") {
		t.Fatal(c, err)
	}
	if _, err := NewPlan(f.r, Selection{Action: "update", IDs: []string{"runtime"}, Special: true, Consents: []string{"unknown"}}, f.o); err == nil {
		t.Fatal("unknown consent accepted")
	}
}

func TestRuntimeDriftAndMajorVersionStayBlocked(t *testing.T) {
	for _, kind := range []string{"major", "non-current", "non-current-prefix", "wrong-executable", "unknown-dependents", "consumer-drift", "content-drift", "uninstall"} {
		t.Run(kind, func(t *testing.T) {
			f, i := runtimeFixture(t)
			s := Selection{Action: "update", IDs: []string{i.ID}, Special: true, Consents: []string{"runtime_update"}}
			if kind == "major" {
				x := f.o.ResolvedTargets[i.ID]
				x.Version = "23.0.0"
				f.o.ResolvedTargets[i.ID] = x
			}
			if kind == "non-current" {
				f.r.Items[0].Active = false
			}
			if kind == "non-current-prefix" {
				f.r.Items[2].Active = false
			}
			if kind == "wrong-executable" {
				f.r.Items[2].Name = "npm"
			}
			if kind == "unknown-dependents" {
				x := f.o.ResolvedTargets[i.ID]
				x.RuntimeScopeVerified = false
				f.o.ResolvedTargets[i.ID] = x
			}
			if kind == "uninstall" {
				s.Action = "uninstall"
			}
			p := f.plan(s)
			drift := kind == "consumer-drift" || kind == "content-drift"
			if !drift {
				if p.Items[0].Status == "ready" {
					t.Fatal(p.Items)
				}
				return
			}
			if p.Items[0].Status != "ready" {
				t.Fatal(p.Items)
			}
			if kind == "consumer-drift" {
				f.r.Items[1].Dependencies = append(f.r.Items[1].Dependencies, audit.Dependency{Name: "new-impact"})
			} else {
				f.write(filepath.Join(i.Path, i.Version, "bin/node"), "locally modified runtime", 0755)
			}
			executed := false
			_, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { executed = true; return nil, nil })))
			if err == nil || executed {
				t.Fatal("runtime drift executed", err)
			}
		})
	}
}

func TestRuntimeSuccessfulCommandNeedsExactFreshVersion(t *testing.T) {
	for _, mode := range []string{"correct", "stale-executable", "no-update"} {
		correct := mode == "correct"
		f, i := runtimeFixture(t)
		p := f.plan(Selection{Action: "update", IDs: []string{i.ID}, Special: true, Consents: []string{"runtime_update"}})
		out, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
			if mode != "no-update" {
				f.r.Items[0].Version = "22.2.0"
				f.r.Items[0].ActiveVersion = "22.2.0"
				f.write(filepath.Join(i.Path, "22.2.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
			}
			if correct {
				f.r.Items[2].RealPath = filepath.Join(i.Path, "22.2.0/bin/node")
				f.r.Items[2].Version = "22.2.0"
			}
			return nil, nil
		})))
		if err != nil || len(out.Results) != 1 {
			t.Fatal(out, err)
		}
		if correct && out.Results[0].Status != "updated" || !correct && out.Results[0].Status != "verification_unknown" {
			t.Fatal(out)
		}
	}
}

func TestRuntimeVersionedFormulaAndLibrariesRemainProtected(t *testing.T) {
	for _, name := range []string{"node@22", "ruby", "php", "openssl", "sqlite", "icu4c"} {
		f, i := runtimeFixture(t)
		newPath := filepath.Join(f.o.BrewRoots[0], "Cellar", name)
		if err := os.Rename(i.Path, newPath); err != nil {
			t.Fatal(err)
		}
		f.r.Items[0].Name, f.r.Items[0].Identity, f.r.Items[0].Path = name, name, newPath
		p := f.plan(Selection{Action: "update", IDs: []string{i.ID}, Special: true, Consents: []string{"runtime_update"}})
		if p.Items[0].Status == "ready" {
			t.Fatal(name, p.Items)
		}
	}
}

func TestParentFormulaCannotUpgradeProtectedRuntimeIndirectly(t *testing.T) {
	for _, all := range []bool{false, true} {
		for _, consent := range []bool{false, true} {
			f, node := runtimeFixture(t)
			parent := audit.Item{ID: "parent", Name: "example", Kind: "formula", Owner: "homebrew", Path: filepath.Join(f.o.BrewRoots[0], "Cellar/example"), Source: "homebrew/core", Version: "1.0.0", ActiveVersion: "1.0.0", Health: audit.Result{Status: "ok"}}
			f.write(filepath.Join(parent.Path, "1.0.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
			f.r.Items = append(f.r.Items, parent)
			f.o.ResolvedTargets[parent.ID] = ResolvedTarget{Provider: "homebrew", ManagerPath: filepath.Join(f.o.BrewRoots[0], "bin/brew"), Scope: f.o.BrewRoots[0], Source: "homebrew/core:parent", OriginVerified: true, Version: "1.1.0", AffectedPackages: []string{node.Name}}
			s := Selection{Action: "update", All: all}
			if !all {
				s.IDs = []string{parent.ID}
			}
			if consent {
				s.Special = true
				s.Consents = []string{"runtime_update"}
			}
			p := f.plan(s)
			for _, pi := range p.Items {
				if pi.ID == parent.ID && pi.Status == "ready" {
					t.Fatalf("indirect runtime change ready: all=%v consent=%v", all, consent)
				}
			}
		}
	}
}

func TestParentRuntimeDependencyAllowsOnlyVerifiedUnchangedVersion(t *testing.T) {
	for _, mode := range []string{"current", "newer", "major", "unknown", "inventory-drift"} {
		t.Run(mode, func(t *testing.T) {
			f, node := runtimeFixture(t)
			parent := audit.Item{ID: "parent", Name: "example", Kind: "formula", Owner: "homebrew", Path: filepath.Join(f.o.BrewRoots[0], "Cellar/example"), Source: "homebrew/core", Version: "1.0.0", ActiveVersion: "1.0.0", Health: audit.Result{Status: "ok"}}
			f.write(filepath.Join(parent.Path, "1.0.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
			f.r.Items = append(f.r.Items, parent)
			dependency := RuntimeDependency{Name: node.Name, CurrentVersion: node.Version, TargetVersion: node.Version, Unchanged: true}
			switch mode {
			case "newer":
				dependency.TargetVersion = "22.2.0"
				dependency.Unchanged = false
			case "major":
				dependency.TargetVersion = "23.0.0"
				dependency.Unchanged = false
			case "unknown":
				dependency.Unchanged = false
			case "inventory-drift":
				dependency.CurrentVersion = "22.0.0"
				dependency.TargetVersion = "22.0.0"
			}
			f.o.ResolvedTargets[parent.ID] = ResolvedTarget{Provider: "homebrew", ManagerPath: filepath.Join(f.o.BrewRoots[0], "bin/brew"), Scope: f.o.BrewRoots[0], Source: "homebrew/core:parent", OriginVerified: true, Version: "1.1.0", AffectedPackages: []string{node.Name}, RuntimeDependencies: []RuntimeDependency{dependency}}
			p := f.plan(Selection{Action: "update", IDs: []string{parent.ID}, Special: true, Consents: []string{"runtime_update"}})
			if mode == "current" {
				if p.Items[0].Status != "ready" {
					t.Fatal("unchanged runtime blocked", p.Items)
				}
			} else if p.Items[0].Status != "manual" || p.Items[0].Reason != "protected_runtime_dependency_requires_separate_update" {
				t.Fatal(mode, p.Items)
			}
		})
	}
}

func TestParentUpdateCannotReportSuccessWhenProtectedDependencyChanges(t *testing.T) {
	f, node := runtimeFixture(t)
	parent := audit.Item{ID: "parent", Name: "example", Kind: "formula", Owner: "homebrew", Path: filepath.Join(f.o.BrewRoots[0], "Cellar/example"), Source: "homebrew/core", Version: "1.0.0", ActiveVersion: "1.0.0", Health: audit.Result{Status: "ok"}}
	f.write(filepath.Join(parent.Path, "1.0.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
	f.r.Items = append(f.r.Items, parent)
	f.o.ResolvedTargets[parent.ID] = ResolvedTarget{Provider: "homebrew", ManagerPath: filepath.Join(f.o.BrewRoots[0], "bin/brew"), Scope: f.o.BrewRoots[0], Source: "homebrew/core:parent", OriginVerified: true, Version: "1.1.0", AffectedPackages: []string{node.Name}, RuntimeDependencies: []RuntimeDependency{{Name: node.Name, CurrentVersion: node.Version, TargetVersion: node.Version, Unchanged: true}}}
	p := f.plan(Selection{Action: "update", IDs: []string{parent.ID}})
	if p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
	result, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		f.r.Items[0].Version = "23.0.0"
		f.r.Items[0].ActiveVersion = "23.0.0"
		for n := range f.r.Items {
			if f.r.Items[n].ID == parent.ID {
				f.r.Items[n].Version = "1.1.0"
				f.r.Items[n].ActiveVersion = "1.1.0"
			}
		}
		return nil, nil
	})))
	if err != nil || result.Results[0].Status != "verification_unknown" || !result.Results[0].CommandSucceeded {
		t.Fatal("unexpected protected dependency mutation treated as success", result, err)
	}
}
