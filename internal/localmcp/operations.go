package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
)

const OperationTimeout = 2 * time.Hour

type Operation struct {
	ID         string              `json:"operation_id"`
	PlanHandle string              `json:"plan_handle"`
	PlanID     string              `json:"plan_id"`
	State      string              `json:"state"`
	Reason     string              `json:"reason,omitempty"`
	Result     *manage.ApplyResult `json:"result,omitempty"`
}
type decision struct {
	PlanID   string `json:"plan_id"`
	Approved bool   `json:"approved"`
}

func confirmation(p manage.Plan) (privileged.PlanConfirmation, error) {
	nonce, e := handle()
	if e != nil {
		return privileged.PlanConfirmation{}, e
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "%s: %d item(s) / 项\n", p.Action, len(p.Items))
	for _, i := range p.Items {
		fmt.Fprintf(&summary, "\n%s\nID: %s\n%s → %s\n%s: %s\nCleanup / 清理: %t; Scripts / 脚本: %t; Quit / 退出: %t; Admin / 管理员: %t; Restart / 重启: %t\n", i.Name, i.ID, i.FromVersion, i.TargetVersion, i.Status, i.Reason, i.Cleanup, i.RequiresScripts, i.RequiresQuit, i.RequiresAdmin, i.RequiresRestart)
	}
	// The complete immutable plan includes paths, related installations, quit/reopen
	// selections, runtime review and every stored Skill diff. Nothing is truncated.
	details, e := json.MarshalIndent(p, "", "  ")
	if e != nil {
		return privileged.PlanConfirmation{}, e
	}
	var diffs strings.Builder
	for _, item := range p.Items {
		if item.SkillUpdate != nil {
			for _, change := range item.SkillUpdate.FileChanges {
				if change.Diff != "" {
					fmt.Fprintf(&diffs, "\n%s\n%s\n", item.Name, change.Diff)
				}
			}
		}
	}
	r := privileged.PlanConfirmation{Schema: privileged.ApprovalSchema, Challenge: nonce, PlanDigest: p.PlanID, Action: p.Action, ExpiresAt: p.ExpiresAt, Summary: summary.String(), Details: diffs.String() + "\n" + string(details)}
	b, e := json.Marshal(r)
	if e != nil || len(b) > 4<<20 {
		return r, errors.New("plan too large for native approval; select fewer items")
	}
	return r, nil
}
func (s *service) operation(id string) (Operation, error) {
	var op Operation
	if e := s.store.read(id, "operation", &op); e != nil {
		if os.IsNotExist(e) {
			var p manage.Plan
			planErr := s.store.read(id, "plan", &p)
			if os.IsNotExist(planErr) {
				return Operation{}, errors.New("operation_unavailable_or_expired")
			}
			if planErr != nil {
				return Operation{}, planErr
			}
			if planErr == nil {
				state, reason := "not_started", ""
				for _, kind := range []string{"decision-lock", "decision", "worker"} {
					path, err := s.store.path(id, kind)
					if err != nil {
						return Operation{}, err
					}
					if _, err = os.Lstat(path); !os.IsNotExist(err) {
						state, reason = "unknown", "operation_record_missing_do_not_retry"
						break
					}
				}
				return Operation{ID: id, PlanHandle: id, PlanID: p.PlanID, State: state, Reason: reason}, nil
			}
		}
		return op, e
	}
	var plan manage.Plan
	if e := s.store.readCached(id, "plan", &plan); e != nil {
		return Operation{}, e
	}
	if op.ID != id || op.PlanHandle != id || op.PlanID != plan.PlanID || (op.Result != nil && op.Result.PlanID != plan.PlanID) {
		return Operation{}, errors.New("operation identity mismatch")
	}
	switch op.State {
	case "not_started", "awaiting_approval", "starting", "running", "failed", "unknown":
	case "finished":
		if op.Result == nil || op.Result.Schema != manage.ApplySchema || op.Result.State != "finished" || len(op.Result.Results) != len(plan.Items) || len(op.Result.OriginalIDs) != len(plan.Items) {
			return Operation{}, errors.New("finished operation result is incomplete")
		}
		for i, item := range plan.Items {
			if op.Result.Results[i].ID != item.ID || op.Result.OriginalIDs[i] != item.ID || op.Result.Results[i].Status == "" {
				return Operation{}, errors.New("finished operation item binding mismatch")
			}
		}
	default:
		return Operation{}, errors.New("unknown operation record state")
	}
	if op.State == "running" && !s.store.active(id, "worker") {
		op.State = "unknown"
		op.Reason = "worker_not_running_do_not_retry"
	}
	if op.State == "awaiting_approval" && !s.store.active(id, "decision-lock") {
		op.State = "unknown"
		op.Reason = "confirmation_interrupted_not_retried"
	}
	if op.State == "starting" && !s.store.active(id, "decision-lock") && !s.store.active(id, "worker") {
		op.State = "unknown"
		op.Reason = "handoff_unconfirmed_do_not_retry"
	}
	return op, nil
}
func (s *service) request(ctx context.Context, id string) (Operation, error) {
	cacheLock, e := s.store.lock(cacheLockID, "cache-lock")
	if e != nil {
		return Operation{}, e
	}
	var p manage.Plan
	if e = s.store.readCached(id, "plan", &p); e != nil {
		cacheLock.Close()
		return Operation{}, e
	}
	lock, e := s.store.claim(id, "decision-lock")
	cacheLock.Close()
	if os.IsExist(e) {
		return s.operation(id)
	}
	if e != nil {
		return Operation{}, e
	}
	defer lock.Close()
	op := Operation{ID: id, PlanHandle: id, PlanID: p.PlanID, State: "awaiting_approval"}
	save := func() error { return s.store.write(id, "operation", op, false) }
	if e = save(); e != nil {
		return op, e
	}
	deny := func(reason string) (Operation, error) {
		op.State = "not_started"
		op.Reason = reason
		return op, save()
	}
	if e = manage.ValidatePlan(p, time.Now()); e != nil {
		return deny(e.Error())
	}
	exp, e := time.Parse(time.RFC3339Nano, p.ExpiresAt)
	if e != nil || !time.Now().Before(exp) {
		return deny("plan_expired")
	}
	req, e := confirmation(p)
	if e != nil {
		return deny(e.Error())
	}
	approveCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	approved, e := s.confirm(approveCtx, req)
	if e != nil || !approved || approveCtx.Err() != nil || ctx.Err() != nil {
		return deny("confirmation_denied_cancelled_or_unavailable")
	}
	if !time.Now().Before(exp) {
		return deny("plan_expired_during_confirmation")
	}
	if e = s.store.write(id, "decision", decision{PlanID: p.PlanID, Approved: true}, true); e != nil {
		return deny("decision_not_saved")
	}
	op.State = "starting"
	op.Reason = "approved_operation_continues_after_chat_closes"
	if e = save(); e != nil {
		return op, e
	}
	// Commit point: the durable decision belongs to this operation. After starting
	// the child, client cancellation must never kill it or trigger another Apply.
	if ctx.Err() != nil {
		return deny("request_cancelled_before_handoff")
	}
	if e = s.start(id); e != nil {
		return deny("worker_start_failed_no_retry")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return op, nil
		case <-deadline.C:
			op.State = "unknown"
			op.Reason = "worker_handoff_unconfirmed_query_operation"
			return op, nil
		case <-tick.C:
			current, err := s.operation(id)
			if err != nil {
				return op, err
			}
			if current.State == "running" || current.State == "finished" || current.State == "failed" {
				return current, nil
			}
		}
	}
}
func startWorker(id string) error {
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	command := exec.Command(executable, "mcp-worker", id)
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	command.Env = []string{"HOME=" + home}
	// Preserve only the launching CLI's inventory selectors. Tool inputs cannot
	// supply environment values; mutation commands still use ExecRunner's clean env.
	for _, key := range []string{"PATH", "XDG_STATE_HOME", "VOLTA_HOME", "PIPX_HOME", "UV_TOOL_DIR", "BUN_INSTALL_GLOBAL_DIR", "PNPM_HOME", "PIPX_BIN_DIR", "UV_TOOL_BIN_DIR", "BUN_INSTALL_BIN", "BUN_INSTALL"} {
		if value, ok := os.LookupEnv(key); ok {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// nil streams bind to /dev/null; no inherited MCP pipe or unbounded log.
	if e = command.Start(); e != nil {
		return e
	}
	go func() { _ = command.Wait() }()
	return nil
}

// Worker is an internal CLI entry. Its only input is an opaque handle in the
// fixed user-private store; the server's durable bound decision is mandatory.
func Worker(ctx context.Context, dir, id string, engine func(manage.Plan) manage.Engine) error {
	return runWorker(ctx, store{dir}, id, engine)
}
func runWorker(ctx context.Context, st store, id string, engine func(manage.Plan) manage.Engine) error {
	var p manage.Plan
	var d decision
	if e := st.read(id, "plan", &p); e != nil {
		return e
	}
	if e := st.read(id, "decision", &d); e != nil {
		return e
	}
	if !d.Approved || d.PlanID != p.PlanID {
		return errors.New("approved plan required")
	}
	lock, e := st.claim(id, "worker")
	if e != nil {
		return e
	}
	defer lock.Close()
	op := Operation{ID: id, PlanHandle: id, PlanID: p.PlanID, State: "running", Reason: "approved_operation_continues_after_chat_closes"}
	save := func() error { return st.write(id, "operation", op, false) }
	if e = save(); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	eng := engine(p)
	eng.Options.Context = ctx
	eng.RecordOperation = func(r manage.ApplyResult) error { op.Result = &r; return save() }
	result, applyErr := manage.Apply(ctx, p, p.PlanID, eng)
	op.Result = &result
	op.State = "finished"
	op.Reason = "inspect_each_item_result"
	if applyErr != nil {
		op.State = "failed"
		op.Reason = applyErr.Error()
		if result.State == "in_progress" {
			op.State = "unknown"
		}
	}
	if e = save(); e != nil {
		return e
	}
	return applyErr
}

// One plan digest has one opaque handle even across concurrent preview calls.
func (s *service) savePlan(p manage.Plan) (string, error) {
	if e := manage.ValidatePlan(p, time.Now()); e != nil {
		return "", e
	}
	lock, e := s.store.lock(cacheLockID, "cache-lock")
	if e != nil {
		return "", e
	}
	defer lock.Close()
	var existing string
	if e = s.store.read(p.PlanID, "plan-handle", &existing); e == nil {
		var saved manage.Plan
		if !validHandle(existing) {
			return "", errors.New("invalid saved plan handle")
		}
		if e = s.store.read(existing, "plan", &saved); e != nil {
			return "", e
		}
		if saved.PlanID != p.PlanID {
			return "", errors.New("saved plan handle binding mismatch")
		}
		if e = manage.ValidatePlan(saved, time.Now()); e != nil {
			return "", e
		}
		return existing, nil
	} else if !os.IsNotExist(e) {
		return "", e
	}
	id, e := handle()
	if e != nil {
		return "", e
	}
	if e = s.store.retain("plan"); e != nil {
		return "", e
	}
	if e = s.store.write(id, "plan", p, true); e != nil {
		return "", e
	}
	if e = s.store.write(p.PlanID, "plan-handle", id, true); e != nil {
		return "", e
	}
	return id, nil
}
