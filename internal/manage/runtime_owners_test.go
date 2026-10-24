package manage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestVoltaInnerPackagePlanAndApplyNeverMutate(t *testing.T) {
	for _, scenario := range []string{"owner", "legacy-default", "custom-root", "custom-marker", "alias"} {
		for _, action := range []string{"update", "uninstall"} {
			t.Run(scenario+"-"+action, func(t *testing.T) {
				f := setup(t)
				i := f.npm("one", "example")
				root := filepath.Join(f.home, ".volta")
				if scenario == "custom-root" || scenario == "custom-marker" {
					root = filepath.Join(f.home, "custom")
				}
				p := filepath.Join(root, "tools/image/packages/example/lib/node_modules/example")
				f.write(filepath.Join(p, "package.json"), `{"name":"example","version":"1.0.0"}`, 0644)
				i.Path, i.RealPath = p, p
				if scenario == "owner" {
					i.Owner = "volta"
					i.Source = "volta"
				}
				if scenario == "custom-root" {
					f.o.ManagerRoots = map[string]string{"volta": root}
				}
				if scenario == "custom-marker" {
					f.write(filepath.Join(root, "layout.v4"), "", 0644)
				}
				if scenario == "alias" {
					alias := filepath.Join(f.home, "alias")
					if err := os.Symlink(p, alias); err != nil {
						t.Fatal(err)
					}
					i.Path = alias
				}
				f.r.Items = []audit.Item{i}
				plan := f.plan(Selection{Action: action, IDs: []string{i.ID}, Special: true})
				if plan.Items[0].Status != "manual" || plan.Items[0].Provider != "volta" || plan.Items[0].Reason != "volta_owner_manual" {
					t.Fatal(plan.Items)
				}
				e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
					t.Fatal("inner package mutation attempted")
					return nil, nil
				}))
				out, err := Apply(context.Background(), plan, plan.PlanID, e)
				if err != nil || len(out.Results) != 1 || out.Results[0].Executed || out.Results[0].Changed {
					t.Fatal(out, err)
				}
				if _, err := os.Stat(filepath.Join(p, "package.json")); err != nil {
					t.Fatal("package removed", err)
				}
			})
		}
	}
}

func TestVoltaOwnerAppearingAfterPreviewBlocksOldNpmPlan(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	plan := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	if plan.Items[0].Status != "ready" {
		t.Fatal(plan.Items)
	}
	// An old, otherwise valid inner npm preview cannot bypass newly observed ownership.
	f.write(filepath.Join(f.home, ".npm-global/layout.v4"), "", 0644)
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		t.Fatal("stale npm plan mutated Volta")
		return nil, nil
	}))
	if _, err := Apply(context.Background(), plan, plan.PlanID, e); err == nil {
		t.Fatal("old owner binding accepted")
	}
}

func TestVoltaManagerCannotBeUsedByCachedOrdinaryPackagePlan(t *testing.T) {
	for _, action := range []string{"update", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			f := setup(t)
			i := f.npm("one", "example")
			plan := f.plan(Selection{Action: action, IDs: []string{i.ID}})
			if plan.Items[0].Status != "ready" {
				t.Fatal(plan.Items)
			}
			manager := f.o.ManagerPaths["npm"]
			shim := filepath.Join(f.home, ".volta/bin/npm")
			f.write(shim, "controlled Volta shim", 0755)
			if err := os.Remove(manager); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(shim, manager); err != nil {
				t.Fatal(err)
			}
			fresh := f.plan(Selection{Action: action, IDs: []string{i.ID}})
			if fresh.Items[0].Status != "manual" || fresh.Items[0].Reason != "volta_owner_manual" {
				t.Fatal(fresh.Items)
			}
			e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("Volta manager dispatched"); return nil, nil }))
			if _, err := Apply(context.Background(), plan, plan.PlanID, e); err == nil {
				t.Fatal("cached manager plan accepted")
			}
		})
	}
}
