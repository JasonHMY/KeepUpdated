package manage

import (
	"context"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// AppStoreBinding remains decodable for saved plans. Store updates now require
// manual handling, including plans created before that policy changed.
type AppStoreBinding struct {
	ADAMID           string `json:"adam_id"`
	BundleID         string `json:"bundle_id"`
	Path             string `json:"path"`
	ReceiptSHA256    string `json:"receipt_sha256"`
	ManagerVersion   string `json:"manager_version"`
	Store            string `json:"store"`
	MinimumOSVersion string `json:"minimum_os_version"`
}

// ResolveAppStoreTargets never queries managers, signatures or store candidates.
// Navigation uses the inventory's existing product identity, or the Updates page.
func ResolveAppStoreTargets(ctx context.Context, report audit.Report, s Selection, _ Options, _ Runner) (map[string]ResolvedTarget, error) {
	out := map[string]ResolvedTarget{}
	if s.Action != "update" {
		return out, ctx.Err()
	}
	for _, i := range report.Items {
		if i.Kind == "application" && i.Owner == "app-store" && (s.All || contains(s.IDs, i.ID)) {
			out[i.ID] = ResolvedTarget{Provider: "app-store", Scope: i.Path, Reason: "app_store_manual_update"}
		}
	}
	return out, ctx.Err()
}

func appStorePlan(_ audit.Item, p PlanItem, _ audit.Report, _ Options, _ Selection) PlanItem {
	p.Status, p.Reason = "manual", "app_store_manual_update"
	return p
}

func applyAppStore(_ context.Context, p PlanItem, _ Selection, _ Options, _ Engine, _ string) ItemResult {
	return ItemResult{ID: p.ID, Name: p.Name, BeforeVersion: p.FromVersion, Status: "manual", Reason: "app_store_manual_update"}
}
