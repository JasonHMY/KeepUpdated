package privileged

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOrdinaryConfirmationResponseBinding(t *testing.T) {
	request := PlanConfirmation{Schema: ApprovalSchema, Challenge: strings.Repeat("a", 64), PlanDigest: strings.Repeat("b", 64)}
	for _, fault := range []string{"none", "denied", "challenge", "digest", "schema", "extra", "trailing", "overflow", "malformed"} {
		t.Run(fault, func(t *testing.T) {
			response := map[string]any{"schema": ApprovalSchema, "challenge": request.Challenge, "plan_digest": request.PlanDigest, "approved": true}
			switch fault {
			case "denied":
				response["approved"] = false
			case "challenge":
				response["challenge"] = strings.Repeat("c", 64)
			case "digest":
				response["plan_digest"] = strings.Repeat("d", 64)
			case "schema":
				response["schema"] = "other"
			case "extra":
				response["token"] = "not-accepted"
			}
			data, _ := json.Marshal(response)
			if fault == "trailing" {
				data = append(data, []byte("{}")...)
			}
			if fault == "malformed" {
				data = []byte("{")
			}
			approved, err := decodePlanDecision(data, fault == "overflow", request)
			if approved != (fault == "none") {
				t.Fatalf("approved=%t error=%v", approved, err)
			}
			if (fault == "none" || fault == "denied") != (err == nil) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
