package manage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
)

func storeManualFixture(t *testing.T) (*fixture, audit.Item) {
	f := setup(t)
	path := filepath.Join(f.home, "Applications/Example.app")
	// Neither receipts nor signatures are needed to direct the user to the store.
	i := audit.Item{ID: "store-example", Kind: "application", Name: "Example", Path: path, RealPath: path, Identity: "com.example.store", Owner: "app-store", Source: "app-store", Version: "1.0", Latest: "2.0", Update: audit.Result{Status: "available"}}
	f.r.Items = []audit.Item{i}
	return f, i
}

func TestAppStoreUpdateIsManualWithoutQueries(t *testing.T) {
	for _, all := range []bool{false, true} {
		for _, productID := range []int64{0, 123456789} {
			f, item := storeManualFixture(t)
			if productID != 0 {
				f.r.Items[0].AppStore = &audit.AppStoreProduct{ID: productID, BundleID: item.Identity, Country: "au"}
			}
			selection := Selection{Action: "update", All: all, Special: true, Consents: []string{ConsentPrivilegedInstall}}
			if !all {
				selection.IDs = []string{item.ID}
			}
			calls := 0
			runner := runFunc(func(context.Context, Command) ([]byte, error) { calls++; return nil, nil })
			targets, err := ResolveAppStoreTargets(context.Background(), f.r, selection, f.o, runner)
			if err != nil || calls != 0 || targets[item.ID].Reason != "app_store_manual_update" {
				t.Fatal(targets, err, calls)
			}
			f.o.ResolvedTargets = targets
			p := f.plan(selection)
			pi := p.Items[0]
			if pi.Status != "manual" || pi.Reason != "app_store_manual_update" || pi.RequiresAdmin || pi.RequiresScripts || pi.PrivilegedRequest != nil || pi.Binding.ResolvedTarget != nil {
				t.Fatal(pi)
			}
			result, err := Apply(context.Background(), p, p.PlanID, f.engine(runner))
			if err != nil || calls != 0 || len(result.Results) != 1 || result.Results[0].Executed || result.Results[0].Status == "updated" {
				t.Fatal(result, err, calls)
			}
		}
	}
}

func TestAppStoreMixedSelectionKeepsManagedUpdate(t *testing.T) {
	f, item := storeManualFixture(t)
	node := f.npm("node", "example")
	p := f.plan(Selection{Action: "update", All: true})
	statuses := map[string]string{}
	for _, pi := range p.Items {
		statuses[pi.ID] = pi.Status
	}
	if statuses[item.ID] != "manual" || statuses[node.ID] != "ready" {
		t.Fatal(p.Items)
	}
}

func TestSavedAutomaticStorePlanCannotBypassManualPolicy(t *testing.T) {
	f, item := storeManualFixture(t)
	s := Selection{Action: "update", IDs: []string{item.ID}, Special: true, Consents: []string{ConsentPrivilegedInstall}}
	p := f.plan(s)
	// Model an authentic saved plan from the retired automatic update policy.
	p.Items[0].Status, p.Items[0].Reason = "ready", "app_store_update_verified"
	p.Items[0].Binding.ResolvedTarget = &ResolvedTarget{Provider: "app-store", AppStore: &AppStoreBinding{ADAMID: "123456789"}}
	p.PlanID = planHash(p)
	calls := 0
	runner := runFunc(func(context.Context, Command) ([]byte, error) { calls++; return nil, nil })
	_, err := Apply(context.Background(), p, p.PlanID, f.engine(runner))
	if err == nil || !strings.Contains(err.Error(), "plan no longer matches") || calls != 0 {
		t.Fatal(err, calls)
	}
	result := applyAppStore(context.Background(), p.Items[0], s, f.o, f.engine(runner), p.PlanID)
	if result.Status != "manual" || result.Reason != "app_store_manual_update" || result.Executed || calls != 0 {
		t.Fatal(result, calls)
	}
}

func TestAppStoreResolverPreservesUninstallAndUnselectedScope(t *testing.T) {
	f, _ := storeManualFixture(t)
	for _, s := range []Selection{{Action: "uninstall", All: true}, {Action: "update", IDs: []string{"other"}}} {
		out, err := ResolveAppStoreTargets(context.Background(), f.r, s, f.o, nil)
		if err != nil || len(out) != 0 {
			t.Fatal(out, err)
		}
	}
}

func TestUnsupportedApplicationSkipsProcessAndCandidateQueries(t *testing.T) {
	f, item := storeManualFixture(t)
	item.Owner, item.Source = "standalone", "vendor"
	f.r.Items = []audit.Item{item}
	calls := 0
	out, err := ResolveApplicationTargets(context.Background(), f.r, Selection{Action: "update", All: true}, f.o, runFunc(func(context.Context, Command) ([]byte, error) { calls++; return nil, nil }))
	if err != nil || len(out) != 0 || calls != 0 {
		t.Fatal(out, err, calls)
	}
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Reason != "application_update_requires_vendor_adapter" || p.Items[0].Status != "manual" {
		t.Fatal(p.Items)
	}
}

func TestStorePolicyPrecedesPrivilegedPreparation(t *testing.T) {
	f, item := storeManualFixture(t)
	client := &privilegeFixture{checkStatus: "ready"}
	f.o.PrivilegedClient = client
	for _, request := range []*privileged.Request{
		{Kind: "app_store_update", AppStore: &privileged.AppStoreTarget{}},
		{Kind: "package", Package: &privileged.PackageTarget{}},
	} {
		selection := Selection{Action: "update", IDs: []string{item.ID}, PrivilegedRequest: request, Special: true, Consents: []string{ConsentPrivilegedInstall}}
		targets, err := ResolvePrivilegedTargets(context.Background(), f.r, selection, f.o, nil)
		if err != nil || client.checks != 0 || targets[item.ID].Reason != "app_store_manual_update" {
			t.Fatal(targets, err, client.checks)
		}
		p := f.plan(selection)
		if p.Items[0].Status != "manual" || p.Items[0].Reason != "app_store_manual_update" {
			t.Fatal(p.Items)
		}
	}
}

func TestAppStoreUninstallStillUsesExactClosedApplicationBinding(t *testing.T) {
	f, item := storeManualFixture(t)
	f.write(filepath.Join(item.Path, "Contents/Info.plist"), "isolated app metadata", 0644)
	f.o.AppRoots = []string{filepath.Dir(item.Path)}
	f.r.Items[0].ActivityKnown = true
	f.o.ResolvedTargets = map[string]ResolvedTarget{item.ID: {Provider: "native-app", Scope: item.Path, Source: "fixture", OriginVerified: true, ApplicationState: "closed_application_verified"}}
	p := f.plan(Selection{Action: "uninstall", IDs: []string{item.ID}})
	if p.Items[0].Status != "ready" || p.Items[0].Reason != "closed_user_application_to_trash" {
		t.Fatal(p.Items)
	}
}
