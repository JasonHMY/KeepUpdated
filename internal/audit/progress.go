package audit

import (
	"context"
	"sync"
)

// Progress counts processed work, not successful results. The report remains the
// authority for unsupported, unknown and failed checks. Total nil means unknown.
type Progress struct {
	Schema     string `json:"schema"`
	Sequence   uint64 `json:"sequence,omitempty"`
	Phase      string `json:"phase"`
	Completed  int    `json:"completed"`
	Total      *int   `json:"total"`
	Current    string `json:"current"`
	State      string `json:"state"`
	Unresolved int    `json:"unresolved"`
}

// PhaseProgress serializes real work counts before the next potentially slow task.
// The transport assigns sequence numbers across phases, including repeated phases.
type PhaseProgress struct {
	mu       sync.Mutex
	ctx      context.Context
	callback func(Progress)
	event    Progress
}

func NewPhaseProgress(ctx context.Context, callback func(Progress), phase string, total *int) *PhaseProgress {
	if ctx == nil {
		ctx = context.Background()
	}
	if total != nil {
		value := *total
		total = &value
	}
	p := &PhaseProgress{ctx: ctx, callback: callback, event: Progress{Schema: "keepupdated/progress/v1", Phase: phase, Total: total, State: "running"}}
	p.emit()
	return p
}

func (s *Scanner) progress(phase string, total int) *PhaseProgress {
	return NewPhaseProgress(s.Config.Context, s.Config.Progress, phase, &total)
}
func (s *Scanner) cancelled() bool { return s.Config.Context != nil && s.Config.Context.Err() != nil }
func (p *PhaseProgress) emit() {
	if p.callback != nil {
		p.callback(p.event)
	}
}
func (p *PhaseProgress) Begin(current string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.event.State != "running" || p.ctx.Err() != nil {
		return
	}
	p.event.Current = current
	p.emit()
}
func (p *PhaseProgress) Done(unresolved bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.event.State != "running" || p.ctx.Err() != nil {
		return
	}
	p.event.Completed++
	if unresolved {
		p.event.Unresolved++
	}
	p.emit()
}
func (p *PhaseProgress) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.event.State != "running" {
		return
	}
	p.event.State = "finished"
	if p.event.Total != nil && p.event.Completed != *p.event.Total {
		p.event.State = "failed"
	}
	if p.ctx.Err() != nil {
		p.event.State = "cancelled"
	}
	p.emit()
}
func (p *PhaseProgress) begin(current string) { p.Begin(current) }
func (p *PhaseProgress) done(unresolved bool) { p.Done(unresolved) }
func (p *PhaseProgress) finish()              { p.Finish() }
