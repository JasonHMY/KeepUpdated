package manage

import "github.com/JasonHMY/KeepUpdated/internal/audit"

// The resolver scheduler folds owner observations into its shared group count.
// Direct callers receive an indeterminate current-object event.
func resolutionCurrent(o Options, current string) {
	if o.Context != nil && o.Context.Err() != nil {
		return
	}
	if o.Progress != nil {
		o.Progress(audit.Progress{Schema: "keepupdated/progress/v1", Phase: "resolve", Current: current, State: "running"})
	}
}
