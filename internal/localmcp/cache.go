package localmcp

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

const cacheLockID = "0000000000000000000000000000000000000000000000000000000000000000"
const retainedRecords = 64

func (s store) readCached(id, kind string, v any) error {
	e := s.read(id, kind, v)
	if os.IsNotExist(e) {
		return fmt.Errorf("%s_handle_unavailable_or_expired", kind)
	}
	return e
}
func (s store) saveCached(id, kind string, v any) error {
	lock, e := s.lock(cacheLockID, "cache-lock")
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = s.retain(kind); e != nil {
		return e
	}
	return s.write(id, kind, v, true)
}

// retain runs only while holding the cache lock. Request submission takes the
// same lock before creating its permanent decision claim, so an unsubmitted
// preview cannot be evicted while it becomes an active approval.
func (s store) retain(kind string) error {
	if kind != "snapshot" && kind != "plan" {
		return errors.New("invalid cache kind")
	}
	entries, e := os.ReadDir(s.dir)
	if e != nil {
		return e
	}
	type record struct {
		id       string
		modified time.Time
	}
	var records []record
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "."+kind) {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), "."+kind)
		if !validHandle(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		records = append(records, record{id, info.ModTime()})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].modified.Equal(records[j].modified) {
			return records[i].id < records[j].id
		}
		return records[i].modified.Before(records[j].modified)
	})
	needed := len(records) - retainedRecords + 1
	for _, record := range records {
		if needed <= 0 {
			return nil
		}
		if kind == "snapshot" {
			if e = s.removeCached(record.id, kind); e != nil {
				return e
			}
			needed--
			continue
		}
		var plan manage.Plan
		if e = s.read(record.id, "plan", &plan); e != nil {
			continue
		} // preserve damaged evidence
		created, err := time.Parse(time.RFC3339Nano, plan.CreatedAt)
		if err != nil || manage.ValidatePlan(plan, created) != nil {
			continue
		}
		if !s.safePlanGroup(record.id, plan) || !s.disposablePlan(record.id, plan) {
			continue
		}
		var alias string
		err = s.read(plan.PlanID, "plan-handle", &alias)
		if err != nil && !os.IsNotExist(err) || err == nil && alias != record.id {
			continue
		}
		// Remove the plan first: a partially interrupted eviction remains unavailable,
		// never a runnable plan without its old decision/worker claim.
		if e = s.removeCached(record.id, "plan"); e != nil {
			return e
		}
		for _, extension := range []string{"operation", "decision", "decision-lock", "worker"} {
			if e = s.removeCached(record.id, extension); e != nil {
				return e
			}
		}
		if e = s.removeCached(plan.PlanID, "plan-handle"); e != nil {
			return e
		}
		if e = s.removeCached(plan.PlanID, "preview-lock"); e != nil {
			return e
		}
		needed--
	}
	if needed > 0 {
		return errors.New("MCP plan cache is occupied by active, uncertain or unexpired decided operations; query their results or wait for plan expiry")
	}
	return nil
}
func (s store) disposablePlan(id string, p manage.Plan) bool {
	claimed := false
	for _, kind := range []string{"decision-lock", "decision", "worker", "operation"} {
		path, e := s.path(id, kind)
		if e != nil {
			return false
		}
		_, e = os.Lstat(path)
		if e == nil {
			claimed = true
		} else if !os.IsNotExist(e) {
			return false
		}
	}
	if !claimed {
		return true
	}
	// Keep decisions through their plan's validity window. This prevents the same
	// digest from acquiring another approval after eviction; Apply's attempted
	// records live outside this cache and are never removed here.
	expiry, e := time.Parse(time.RFC3339Nano, p.ExpiresAt)
	if e != nil || time.Now().Before(expiry) || s.active(id, "decision-lock") || s.active(id, "worker") {
		return false
	}
	op, e := (&service{store: s}).operation(id)
	if e != nil {
		return false
	}
	switch op.State {
	case "finished":
		for _, item := range op.Result.Results {
			if !resolvedForRetention(item) {
				return false
			}
		}
		return true
	case "failed":
		return op.Result != nil && op.Result.State == "" && len(op.Result.Results) == 0
	case "not_started":
		path, e := s.path(id, "worker")
		if e != nil {
			return false
		}
		_, e = os.Lstat(path)
		return os.IsNotExist(e)
	default:
		return false
	}
}
func (s store) removeCached(id, kind string) error {
	path, e := s.path(id, kind)
	if e != nil {
		return e
	}
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !private(st, false) {
		return errors.New("unsafe cache record; retained for inspection")
	}
	if e = os.Remove(path); e != nil {
		return e
	}
	return syncDir(s.dir)
}

// Validate the complete record group before removing any part of it. Unsafe or
// malformed auxiliary state is evidence to preserve, not permission to prune.
func (s store) safePlanGroup(id string, p manage.Plan) bool {
	for _, entry := range []struct{ id, kind string }{{id, "plan"}, {id, "operation"}, {id, "decision"}, {id, "decision-lock"}, {id, "worker"}, {p.PlanID, "plan-handle"}, {p.PlanID, "preview-lock"}} {
		path, e := s.path(entry.id, entry.kind)
		if e != nil {
			return false
		}
		st, e := os.Lstat(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || !private(st, false) {
			return false
		}
		if (entry.kind == "decision-lock" || entry.kind == "worker" || entry.kind == "preview-lock") && st.Size() != 0 {
			return false
		}
	}
	var approved decision
	e := s.read(id, "decision", &approved)
	return os.IsNotExist(e) || e == nil && approved.Approved && approved.PlanID == p.PlanID
}
func resolvedForRetention(item manage.ItemResult) bool {
	switch item.Status {
	case "updated", "uninstalled", "configured":
	case "skipped", "blocked", "manual", "unsupported":
		if item.Executed || item.Changed {
			return false
		}
	default:
		return false
	}
	if item.MarketplaceRefresh != nil && (!item.MarketplaceRefresh.StateKnown || item.MarketplaceRefresh.Attempted && !item.MarketplaceRefresh.CommandSucceeded) {
		return false
	}
	for _, app := range item.ApplicationLifecycle {
		if app.QuitStatus != "not_requested" && app.QuitStatus != "exited" {
			return false
		}
		if app.ReopenStatus != "not_requested" && app.ReopenStatus != "reopened" {
			return false
		}
	}
	for _, change := range item.RelatedChanges {
		if change.Status != "added" && change.Status != "version_changed" {
			return false
		}
	}
	return true
}
