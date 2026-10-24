package manage

import (
	"context"
	"reflect"
)

// Capture the complete new per-plugin plan immediately after this batch's
// verified refresh. Later preflight still requires exact equality with it.
func reconcilePluginRefresh(ctx context.Context, approved Plan, actor PlanItem, result ItemResult, e Engine, o Options) map[string]PlanItem {
	out := map[string]PlanItem{}
	a := actor.PluginUpdate
	effect := result.MarketplaceRefresh
	if a == nil || !a.RefreshMarketplace || result.Status != "updated" || !result.CommandSucceeded || effect == nil || !effect.Attempted || !effect.CommandSucceeded || !effect.StateKnown || effect.BeforeCommit != a.MarketplaceBeforeCommit || effect.AfterCommit != a.Commit || effect.Reason != "plugin_marketplace_approved_commit_verified" || e.ResolveTargets == nil {
		return out
	}
	originals := map[string]PlanItem{}
	selection := Selection{Action: "update", Special: approved.Selection.Special}
	for _, p := range approved.Items {
		v := p.PluginUpdate
		if p.ID == actor.ID || p.Status != "ready" || p.Provider != "claude-plugin" || v == nil || !v.RefreshMarketplace || v.Marketplace != a.Marketplace || v.MarketplaceBeforeCommit != a.MarketplaceBeforeCommit || v.Commit != a.Commit || v.SourceIdentity != a.SourceIdentity {
			continue
		}
		originals[p.ID] = p
		selection.IDs = append(selection.IDs, p.ID)
	}
	if len(selection.IDs) == 0 {
		return out
	}
	report, err := e.Snapshot(ctx)
	if err != nil {
		return out
	}
	targets, err := e.ResolveTargets(ctx, report, selection)
	if err != nil {
		return out
	}
	o.ResolvedTargets = targets
	current, err := NewPlan(report, selection, o)
	if err != nil {
		return out
	}
	for _, p := range current.Items {
		if original, ok := originals[p.ID]; ok && pluginRefreshTransition(original, p) {
			out[p.ID] = p
		}
	}
	return out
}

// Only refresh bookkeeping can change: installed content, target commit/tree,
// source identity, client, settings, scope and affected Skills remain exact.
func pluginRefreshTransition(original, current PlanItem) bool {
	a, b := original.PluginUpdate, current.PluginUpdate
	if a == nil || b == nil || !a.RefreshMarketplace || !a.RemoteVerified || b.RefreshMarketplace || !b.RemoteVerified || b.MarketplaceBeforeCommit != a.Commit || original.Binding.ResolvedTarget == nil || current.Binding.ResolvedTarget == nil || current.Status != "ready" {
		return false
	}
	expected := original
	preview := *a
	preview.RefreshMarketplace = false
	preview.MarketplaceBeforeCommit = a.Commit
	preview.SourceDigest = b.SourceDigest
	// Only a previously absent, now fully verified target cache can be added by
	// the successful native operation; existing approved caches stay exact.
	if a.TargetCacheFingerprint == "" {
		preview.TargetCacheFingerprint = b.TargetCacheFingerprint
	}
	expected.PluginUpdate = &preview
	target := *original.Binding.ResolvedTarget
	target.Source = b.SourceDigest
	target.Reason = "plugin_remote_verified_requires_reload"
	expected.Binding.ResolvedTarget = &target
	expected.Reason = target.Reason
	expected.AffectedPaths = nil
	for _, path := range original.AffectedPaths {
		if path != a.Marketplace {
			expected.AffectedPaths = append(expected.AffectedPaths, path)
		}
	}
	return reflect.DeepEqual(expected, current)
}
