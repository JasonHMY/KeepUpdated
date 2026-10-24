package manage

import (
	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"testing"
)

func TestSkillshareProvenanceDoesNotAuthorizeAnyUpdateLane(t *testing.T) {
	for _, source := range []string{"git", "local"} {
		i := audit.Item{Kind: "skill", Name: "example", Path: "/example/SKILL.md", Source: source, Owner: ".agents", SkillManager: &audit.SkillManagerSource{Manager: "skillshare", Code: "skillshare_managed"}}
		p := skillPlan(i, PlanItem{}, Options{}, Selection{Action: "update"})
		if p.Status != "blocked" || p.Reason != "skillshare_managed" || p.Provider != "" {
			t.Fatal(p)
		}
	}
}
