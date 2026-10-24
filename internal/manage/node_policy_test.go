package manage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func (f *fixture) nodePolicy(i audit.Item, scope, manager, version string) *NodePolicy {
	f.t.Helper()
	selector, err := audit.ReadNodeSelector(i.Owner, i.Name, i.Path, scope)
	if err != nil {
		f.t.Fatal(err)
	}
	fingerprint, err := fileHash(manager)
	if err != nil {
		f.t.Fatal(err)
	}
	return &NodePolicy{ManagerVersion: version, ManagerFingerprint: fingerprint, Layout: NodeLayout(i.Owner, version), Bin: NodeBin(i.Owner, Binding{Scope: scope}, f.o), Selection: selector, LatestVersion: i.Latest}
}

func (f *fixture) pnpmStore(i audit.Item, scope, store string) {
	f.t.Helper()
	real := filepath.Join(scope, store, strings.ReplaceAll(i.Name, "/", "+")+"@"+i.Version, "node_modules", filepath.FromSlash(i.Name))
	if err := os.MkdirAll(filepath.Dir(real), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(i.Path, real); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(real, i.Path); err != nil {
		f.t.Fatal(err)
	}
	relative := "../.pnpm"
	if store == "node_modules/.pnpm" {
		relative = ".pnpm"
	}
	f.write(filepath.Join(scope, "node_modules/.modules.yaml"), "layoutVersion: 5\nnodeLinker: isolated\npackageManager: pnpm@10.18.3\nvirtualStoreDir: "+relative+"\n", 0644)
}

func TestNodePolicyDriftAndOldPreviewRejectBeforeMutation(t *testing.T) {
	for _, change := range []string{"missing-policy", "manager-content", "manager-version", "bin", "manifest", "pin", "out-of-range", "unknown-layout"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			i := f.npm("one", "example")
			p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
			if p.Items[0].Status != "ready" {
				t.Fatal(p.Items)
			}
			target := f.o.ResolvedTargets[i.ID]
			policy := *target.Node
			target.Node = &policy
			switch change {
			case "missing-policy":
				p.Items[0].Binding.ResolvedTarget.Node = nil
				p.PlanID = planHash(p)
			case "manager-content":
				f.write(target.ManagerPath, "changed manager", 0755)
			case "manager-version":
				policy.ManagerVersion = "12.0.0"
			case "bin":
				policy.Bin = filepath.Join(f.home, "other-bin")
			case "manifest":
				f.write(filepath.Join(target.Scope, "lib/package.json"), `{"dependencies":{"example":"^1.0.0"}}`, 0644)
			case "pin":
				policy.Selection.Selector = "1.0.0"
			case "out-of-range":
				policy.Selection.Selector = "~1.0.0"
			case "unknown-layout":
				policy.Layout = "new-global-layout"
			}
			f.o.ResolvedTargets[i.ID] = target
			mutations := 0
			_, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { mutations++; return nil, nil })))
			if err == nil || mutations != 0 {
				t.Fatalf("accepted %s: error=%v mutations=%d", change, err, mutations)
			}
		})
	}
}

func TestNodeReadbackRequiresPreservedSelector(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	scope := f.o.ResolvedTargets[i.ID].Scope
	f.write(filepath.Join(scope, "lib/package.json"), `{"dependencies":{"example":"^1.0.0"}}`, 0644)
	target := f.o.ResolvedTargets[i.ID]
	target.Node = f.nodePolicy(i, scope, target.ManagerPath, "11.19.1")
	f.o.ResolvedTargets[i.ID] = target
	p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	i.Version = "1.1.0"
	f.write(filepath.Join(i.Path, "package.json"), `{"name":"example","version":"1.1.0"}`, 0644)
	if !installationVerified(i, p.Items[0], audit.Report{}) {
		t.Fatal("preserved selector not verified")
	}
	f.write(filepath.Join(scope, "lib/package.json"), `{"dependencies":{"example":"1.1.0"}}`, 0644)
	if installationVerified(i, p.Items[0], audit.Report{}) {
		t.Fatal("selector rewrite reported success")
	}
}

func TestUnknownNodeLayoutOrReceiptIsManualForOnlyThatUpdate(t *testing.T) {
	for _, reason := range []string{"owner_layout_unsupported", "owner_receipt_unsupported"} {
		f := setup(t)
		i := f.npm("unknown", "unknown")
		f.npm("valid", "valid")
		f.o.ResolvedTargets[i.ID] = ResolvedTarget{ResolutionError: reason}
		p := f.plan(Selection{Action: "update", All: true})
		for _, item := range p.Items {
			want := "ready"
			if item.ID == i.ID {
				want = "manual"
			}
			if item.Status != want {
				t.Fatal(reason, item)
			}
		}
	}
}
