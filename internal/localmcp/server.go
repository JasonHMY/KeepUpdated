package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Backend is application-owned, not supplied through tool input.
type Backend struct {
	Scan    func(context.Context, bool, bool) (audit.Report, error)
	Preview func(context.Context, manage.Selection) (manage.Plan, error)
}
type service struct {
	store   store
	backend Backend
	confirm func(context.Context, privileged.PlanConfirmation) (bool, error)
	start   func(string) error
}
type empty struct{}
type CheckInput struct {
	Online bool `json:"online,omitempty"`
}
type ListInput struct {
	Snapshot string `json:"snapshot_handle"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}
type ItemInput struct {
	Snapshot string `json:"snapshot_handle"`
	ID       string `json:"id"`
}
type PlanInput struct {
	Action        string   `json:"action"`
	IDs           []string `json:"ids"`
	Cleanup       bool     `json:"cleanup,omitempty"`
	Scripts       bool     `json:"allow_scripts,omitempty"`
	Runtime       bool     `json:"runtime_update,omitempty"`
	QuitApps      []string `json:"quit_apps,omitempty"`
	ReopenApps    []string `json:"reopen_apps,omitempty"`
	CachedPlugins bool     `json:"cached_plugins,omitempty"`
}
type PlanHandle struct {
	Handle string `json:"plan_handle"`
}
type OperationHandle struct {
	ID string `json:"operation_id"`
}

func add[I any](server *mcp.Server, name, description string, readOnly, destructive, openWorld bool, fn func(context.Context, I) (any, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, OpenWorldHint: &openWorld}}, func(ctx context.Context, req *mcp.CallToolRequest, in I) (*mcp.CallToolResult, any, error) {
		// Explicitly reject invented approval fields even if SDK schema defaults change.
		if len(req.Params.Arguments) > 65536 {
			return nil, nil, errors.New("tool input exceeds limit")
		}
		decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
		decoder.DisallowUnknownFields()
		if e := decoder.Decode(&in); e != nil {
			return nil, nil, e
		}
		if decoder.Decode(new(any)) != io.EOF {
			return nil, nil, errors.New("trailing tool input")
		}
		out, e := fn(ctx, in)
		return nil, out, e
	})
}
func NewServer(dir, version, commit string, backend Backend) *mcp.Server {
	s := &service{store: store{dir}, backend: backend, confirm: privileged.ConfirmPlan, start: startWorker}
	return s.server(version, commit)
}
func (s *service) server(version, commit string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "keepupdated", Version: version}, &mcp.ServerOptions{Instructions: "Local software inventory and bounded plans. Use exact installation IDs. request_execution always opens native approval; approved work continues after chat closes. Query the returned operation_id and inspect every item; unknown is not success. No shell or privileged installation tools."})
	add(server, "get_capabilities", "Read version, platform and execution availability.", true, false, false, func(ctx context.Context, _ empty) (any, error) {
		reason := privileged.OrdinaryApprovalAvailability(ctx)
		return map[string]any{"version": version, "source_commit": commit,
			"schema": audit.Schema, "plan_schema": manage.PlanSchema, "apply_schema": manage.ApplySchema,
			"protocol_versions": mcp.SupportedProtocolVersions(), "transport": "stdio", "platform": runtime.GOOS, "arch": runtime.GOARCH,
			"inventory_scope": "default_locations", "operation_results": "per_operation_id", "execution_cancellation": "not_supported",
			"approval_available": reason == "", "approval_unavailable_reason": reason, "privileged_execution": "blocked_use_desktop_or_manual", "manager_timeout_seconds": 900, "operation_timeout_seconds": int(OperationTimeout.Seconds()), "approval_timeout_seconds": 120, "maximum_selected_items": 100, "continues_after_chat_closes": true}, nil
	})
	// Snapshot and plan writes can evict old private cache records.
	add(server, "scan_inventory", "Scan default local locations without update queries; save a private snapshot.", false, true, false, func(ctx context.Context, _ empty) (any, error) { return s.scan(ctx, false, false) })
	add(server, "check_updates", "Check default local locations. online explicitly enables public metadata queries; no installation.", false, true, true, func(ctx context.Context, in CheckInput) (any, error) { return s.scan(ctx, true, in.Online) })
	add(server, "list_items", "Read a page from an immutable snapshot (limit 1–50).", true, false, false, func(ctx context.Context, in ListInput) (any, error) {
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Limit < 1 || in.Limit > 50 || in.Offset < 0 {
			return nil, errors.New("invalid page bounds")
		}
		var report audit.Report
		if e := s.store.readCached(in.Snapshot, "snapshot", &report); e != nil {
			return nil, e
		}
		if in.Offset > len(report.Items) {
			return nil, errors.New("offset exceeds snapshot")
		}
		end := min(in.Offset+in.Limit, len(report.Items))
		return map[string]any{"items": report.Items[in.Offset:end], "next_offset": end, "total": len(report.Items)}, nil
	})
	add(server, "get_item", "Read one exact installation from a saved snapshot.", true, false, false, func(ctx context.Context, in ItemInput) (any, error) {
		var report audit.Report
		if e := s.store.readCached(in.Snapshot, "snapshot", &report); e != nil {
			return nil, e
		}
		for _, item := range report.Items {
			if item.ID == in.ID {
				return item, nil
			}
		}
		return nil, errors.New("installation not found in snapshot")
	})
	add(server, "preview_changes", "Resolve exact IDs and save an immutable update/uninstall plan. Options request scope, never approve execution.", false, true, true, func(ctx context.Context, in PlanInput) (any, error) {
		sel, e := in.selection()
		if e != nil {
			return nil, e
		}
		plan, e := s.backend.Preview(ctx, sel)
		if e != nil {
			return nil, e
		}
		// Check approval payload bounds before handing out a plan that cannot be shown.
		if _, e = confirmation(plan); e != nil {
			return nil, e
		}
		id, e := s.savePlan(plan)
		if e != nil {
			return nil, e
		}
		counts := map[string]int{}
		for _, item := range plan.Items {
			counts[item.Status]++
		}
		return map[string]any{"plan_handle": id, "operation_id": id, "expires_at": plan.ExpiresAt, "action": plan.Action, "item_count": len(plan.Items), "items": planSummaries(plan), "statuses": counts, "approval_required": true, "continues_after_chat_closes": true}, nil
	})
	add(server, "request_execution", "Request native approval of a saved plan. Replays return its existing decision/operation. Approved work continues after chat closes.", false, true, true, func(ctx context.Context, in PlanHandle) (any, error) { return s.request(ctx, in.Handle) })
	add(server, "get_operation", "Read this exact operation, including per-item results. Never retry unknown execution automatically.", true, false, false, func(ctx context.Context, in OperationHandle) (any, error) { return s.operation(in.ID) })
	return server
}
func (s *service) scan(ctx context.Context, check, online bool) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	report, e := s.backend.Scan(ctx, check, online)
	if e != nil {
		return nil, e
	}
	id, e := handle()
	if e != nil {
		return nil, e
	}
	if e = s.store.saveCached(id, "snapshot", report); e != nil {
		return nil, e
	}
	return map[string]any{"snapshot_handle": id, "item_count": len(report.Items), "summary": report.Summary, "diagnostics": report.Diagnostics, "coverage": report.Coverage, "online": report.Online}, nil
}
func (in PlanInput) selection() (manage.Selection, error) {
	s := manage.Selection{Action: in.Action, IDs: in.IDs, Cleanup: in.Cleanup, Special: in.Scripts, QuitApps: in.QuitApps, ReopenApps: in.ReopenApps, CachedPlugins: in.CachedPlugins}
	if in.Runtime {
		s.Consents = []string{manage.ConsentRuntimeUpdate}
	}
	if s.Action != "update" && s.Action != "uninstall" || len(s.IDs) < 1 || len(s.IDs) > 100 || s.Cleanup && s.Action != "uninstall" {
		return s, errors.New("select update or uninstall and 1–100 exact IDs; cleanup requires uninstall")
	}
	for _, ids := range [][]string{s.IDs, s.QuitApps, s.ReopenApps} {
		seen := map[string]bool{}
		if len(ids) > 100 {
			return s, errors.New("too many IDs")
		}
		for _, id := range ids {
			if id == "" || len(id) > 4096 || seen[id] {
				return s, errors.New("invalid or duplicate ID")
			}
			seen[id] = true
		}
	}
	return s, manage.ValidateLifecycleSelection(s)
}

type planSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	From    string `json:"from_version"`
	To      string `json:"target_version"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Cleanup bool   `json:"cleanup"`
	Scripts bool   `json:"scripts"`
	Quit    bool   `json:"quit"`
}

func planSummaries(p manage.Plan) []planSummary {
	out := make([]planSummary, 0, len(p.Items))
	for _, i := range p.Items {
		out = append(out, planSummary{ID: i.ID, Name: i.Name, From: i.FromVersion, To: i.TargetVersion, Status: i.Status, Reason: i.Reason, Cleanup: i.Cleanup, Scripts: i.RequiresScripts, Quit: i.RequiresQuit})
	}
	return out
}
