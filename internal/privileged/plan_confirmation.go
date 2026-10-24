package privileged

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

const ApprovalSchema = "keepupdated/approval/v1"

type PlanConfirmation struct {
	Schema     string `json:"schema"`
	Challenge  string `json:"challenge"`
	PlanDigest string `json:"plan_digest"`
	Action     string `json:"action"`
	ExpiresAt  string `json:"expires_at"`
	Summary    string `json:"summary"`
	Details    string `json:"details"`
}
type PlanDecision struct {
	Schema     string `json:"schema"`
	Challenge  string `json:"challenge"`
	PlanDigest string `json:"plan_digest"`
	Approved   bool   `json:"approved"`
}

// OrdinaryApprovalAvailability uses the sealed same-bundle bridge, without
// relaxing any Developer ID requirements on privileged execution.
func OrdinaryApprovalAvailability(ctx context.Context) string {
	_, reason := bridge(ctx, false)
	return reason
}
func ConfirmPlan(ctx context.Context, request PlanConfirmation) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	path, reason := bridge(ctx, false)
	if reason != "" {
		return false, errors.New(reason)
	}
	data, e := json.Marshal(request)
	if e != nil || len(data) > 4<<20 {
		return false, errors.New("confirmation exceeds display limit")
	}
	command := exec.CommandContext(ctx, path, "confirm-plan")
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	command.Stdin = bytes.NewReader(data)
	var output limitedOutput
	command.Stdout = &output
	if e = command.Run(); e != nil {
		return false, errors.New("native confirmation cancelled or unavailable")
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return decodePlanDecision(output.Bytes(), output.overflow, request)
}
func decodePlanDecision(data []byte, overflow bool, request PlanConfirmation) (bool, error) {
	if overflow {
		return false, errors.New("invalid confirmation response")
	}
	var response PlanDecision
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&response) != nil || d.Decode(new(any)) != io.EOF || response.Schema != ApprovalSchema || response.Challenge != request.Challenge || response.PlanDigest != request.PlanDigest {
		return false, errors.New("confirmation binding mismatch")
	}
	return response.Approved, nil
}
