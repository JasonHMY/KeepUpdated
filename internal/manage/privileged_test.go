package manage

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
)

type privilegeFixture struct {
	checks      int
	calls       int
	result      privileged.Result
	checkStatus string
	err         error
}

func (f *privilegeFixture) Check(_ context.Context, r privileged.Request) (privileged.Result, error) {
	f.checks++
	return privileged.Result{Status: f.checkStatus, Reason: "fixture_prerequisites"}, nil
}
func (f *privilegeFixture) Execute(_ context.Context, r privileged.Request, confirmation string) (privileged.Result, error) {
	if confirmation != privileged.Confirmation(r) {
		panic("wrong exact request")
	}
	f.calls++
	return f.result, f.err
}
func systemFixture(t *testing.T) (*fixture, *privilegeFixture, Selection) {
	f := setup(t)
	f.r.Items = []audit.Item{{ID: "os", Kind: "os", Name: "macOS", Path: "/System/Library/CoreServices/SystemVersion.plist", Owner: "system", Source: "apple", Channel: "stable", Version: "13.6.6"}}
	client := &privilegeFixture{checkStatus: "ready", result: privileged.Result{PlanID: "operation", Status: "awaiting_restart", Reason: "system_tool_requested_restart_target_not_yet_verified", Executed: true, CommandSucceeded: true, ObservedVersion: "13.6.6"}}
	f.o.PrivilegedClient = client
	s := Selection{Action: "update", Special: true, IDs: []string{"os"}, Consents: []string{ConsentPrivilegedInstall}, PrivilegedRequest: &privileged.Request{Kind: "system", System: &privileged.SystemTarget{Label: "macOS Ventura 13.6.7-22G720", Version: "13.6.7", Build: "22G720", FromVersion: "13.6.6", FromBuild: "22G630"}}}
	var err error
	f.o.ResolvedTargets, err = ResolvePrivilegedTargets(context.Background(), f.r, s, f.o, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f, client, s
}
func TestConsentsAreCanonicalAndNotSpecial(t *testing.T) {
	f := setup(t)
	f.npm("a", "fixture")
	s := Selection{Action: "update", IDs: []string{"a"}, Consents: []string{ConsentRuntimeUpdate, ConsentPrivilegedInstall, ConsentRuntimeUpdate}}
	p := f.plan(s)
	if !reflect.DeepEqual(p.Selection.Consents, []string{ConsentPrivilegedInstall, ConsentRuntimeUpdate}) {
		t.Fatal(p.Selection)
	}
	if HasConsents(Selection{Special: true}, ConsentPrivilegedInstall) || HasConsents(Selection{Special: true}, ConsentRestartSystem) {
		t.Fatal("special elevated")
	}
	s.Consents = []string{"root"}
	if _, err := NewPlan(f.r, s, f.o); err == nil {
		t.Fatal("unknown consent accepted")
	}
}
func TestPrivilegedPlanUsesOrdinaryExactApplyAndPreservesConsents(t *testing.T) {
	f, client, s := systemFixture(t)
	p := f.plan(s)
	if p.Items[0].Status != "ready" || !p.Items[0].RequiresAdmin || p.Items[0].RequiresRestart {
		t.Fatal(p.Items)
	}
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		t.Fatal("ordinary runner used for root action")
		return nil, nil
	}))
	e.ResolveTargets = func(ctx context.Context, r audit.Report, current Selection) (map[string]ResolvedTarget, error) {
		if !reflect.DeepEqual(current.Consents, s.Consents) || !reflect.DeepEqual(current.PrivilegedRequest, s.PrivilegedRequest) {
			t.Fatal("consent lost during replan")
		}
		return ResolvePrivilegedTargets(ctx, r, current, f.o, nil)
	}
	result, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || result.Results[0].Status != "awaiting_restart" || result.Results[0].PrivilegedOperationID != p.PlanID {
		t.Fatal(result, client)
	}
	if _, err = Apply(context.Background(), p, p.PlanID, e); err == nil {
		t.Fatal("plan replayed")
	}
}
func TestPrivilegedConsentMissingAndBatchNeverExecute(t *testing.T) {
	for _, mode := range []string{"special", "missing", "all"} {
		t.Run(mode, func(t *testing.T) {
			f, client, s := systemFixture(t)
			if mode == "all" {
				s.All = true
				s.IDs = nil
			} else {
				s.Consents = nil
				s.Special = mode == "special"
			}
			p := f.plan(s)
			if p.Items[0].Status == "ready" {
				t.Fatal(p)
			}
			_, err := Apply(context.Background(), p, p.PlanID, f.engine(nil))
			if err != nil {
				t.Fatal(err)
			}
			if client.calls != 0 {
				t.Fatal("unapproved operation")
			}
		})
	}
}
func TestPrivilegedPrerequisiteOrCandidateDriftBlocks(t *testing.T) {
	f, client, s := systemFixture(t)
	p := f.plan(s)
	client.checkStatus = "blocked"
	e := f.engine(nil)
	e.ResolveTargets = func(ctx context.Context, r audit.Report, s Selection) (map[string]ResolvedTarget, error) {
		return ResolvePrivilegedTargets(ctx, r, s, f.o, nil)
	}
	if _, err := Apply(context.Background(), p, p.PlanID, e); err == nil {
		t.Fatal("changed prerequisite accepted")
	}
	if client.calls != 0 {
		t.Fatal("executed after drift")
	}
}
func TestRestartNeedsOwnConsentAndCannotHideInstallFailure(t *testing.T) {
	f, client, s := systemFixture(t)
	s.PrivilegedRequest = &privileged.Request{Kind: "restart", RestartOf: "prior-operation"}
	f.o.ResolvedTargets, _ = ResolvePrivilegedTargets(context.Background(), f.r, s, f.o, nil)
	p := f.plan(s)
	if p.Items[0].Status == "ready" || !p.Items[0].RequiresRestart {
		t.Fatal(p)
	}
	s.Consents = append(s.Consents, ConsentRestartSystem)
	p = f.plan(s)
	if p.Items[0].Status != "ready" {
		t.Fatal(p)
	}
	client.result = privileged.Result{Status: "failed", Reason: "native_installer_failed", Executed: true, ObservedVersion: "13.6.7"}
	r, err := Apply(context.Background(), p, p.PlanID, f.engine(nil))
	if err != nil || r.Results[0].Status != "failed" {
		t.Fatal(r, err)
	}
}

func TestLostPrivilegedReplyKeepsDurablePlanRecoveryKey(t *testing.T) {
	f, client, s := systemFixture(t)
	p := f.plan(s)
	client.result = privileged.Result{}
	client.err = context.Canceled
	result, err := Apply(context.Background(), p, p.PlanID, f.engine(nil))
	if err != nil {
		t.Fatal(err)
	}
	item := result.Results[0]
	if item.Status != "verification_unknown" || item.PrivilegedOperationID != p.PlanID {
		t.Fatal(item)
	}
	if _, err := os.Stat(filepath.Join(f.state, p.PlanID+".attempted")); err != nil {
		t.Fatal("missing durable caller key", err)
	}
	if _, err := Apply(context.Background(), p, p.PlanID, f.engine(nil)); err == nil {
		t.Fatal("lost reply retried installation")
	}
	if client.calls != 1 {
		t.Fatal("unexpected repeat", client.calls)
	}
}

func TestSystemScriptsNeedTheirOwnExplicitSelection(t *testing.T) {
	f, client, s := systemFixture(t)
	s.Special = false
	blocked := f.plan(s)
	if blocked.Items[0].Status == "ready" || !blocked.Items[0].RequiresScripts || blocked.Items[0].SpecialApproved {
		t.Fatal(blocked.Items)
	}
	s.Special = true
	approved := f.plan(s)
	if approved.Items[0].Status != "ready" || !approved.Items[0].SpecialApproved || approved.PlanID == blocked.PlanID {
		t.Fatal(approved.Items)
	}
	if client.calls != 0 {
		t.Fatal("preview executed")
	}
}
