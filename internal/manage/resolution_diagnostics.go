package manage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ResolutionFailureReason only accepts public reason codes. Neither subprocess
// output nor filesystem errors may be copied into a plan.
func ResolutionFailureReason(code string) string {
	switch code {
	case "python_configuration_requires_review", "python_receipt_schema_unsupported", "python_backend_unsupported", "python_injected_tools_require_review", "python_locked_tool_requires_review", "python_entrypoints_require_review", "python_interpreter_unverified", "python_requirement_constraints_require_review", "python_receipt_constraints_require_review", "python_requirement_installation_mismatch", "python_manager_capability_unsupported", "python_policy_requires_new_preview", "python_target_outside_constraints", "uv_receipt_source_settings_require_review":
		return code
	case "volta_owner_manual", "node_runtime_selection_requires_review", "node_manager_layout_unsupported", "node_selector_requires_review", "node_selector_installation_mismatch", "node_selector_target_mismatch", "owner_candidate_unavailable", "owner_unavailable", "owner_permission_denied", "owner_query_timeout", "owner_network_unavailable", "owner_query_failed", "owner_metadata_invalid", "owner_layout_unsupported", "owner_receipt_unsupported", "owner_source_requires_review", "owner_version_pinned", "owner_installation_changed", "owner_version_unsupported", "owner_effects_require_review", "owner_configuration_unreadable":
		return code
	default:
		return "owner_resolution_failed"
	}
}

// CommandFailure exposes a fixed reason without retaining stderr, arguments or paths.
type CommandFailure struct {
	Code string
	// Retain only the exit status for commands whose nonzero result is a predicate.
	exitCode int
}

func (e CommandFailure) Error() string { return ResolutionFailureReason(e.Code) }

func commandFailure(err error, stderr string) error {
	failure := CommandFailure{Code: "owner_query_failed"}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		failure.exitCode = exit.ExitCode()
	}
	code := "owner_query_failed"
	switch {
	case errors.Is(err, os.ErrNotExist):
		code = "owner_unavailable"
	case errors.Is(err, os.ErrPermission):
		code = "owner_permission_denied"
	default:
		text := strings.ToLower(stderr)
		for _, marker := range []string{"eacces", "eperm", "permission denied", "operation not permitted", "401 unauthorized", "403 forbidden"} {
			if strings.Contains(text, marker) {
				failure.Code = "owner_permission_denied"
				return failure
			}
		}
		for _, marker := range []string{"enotfound", "eai_again", "econnrefused", "econnreset", "etimedout", "network access disabled", "could not resolve host", "failed to connect", "connection refused", "certificate verify failed", "unable to get local issuer certificate"} {
			if strings.Contains(text, marker) {
				failure.Code = "owner_network_unavailable"
				return failure
			}
		}
	}
	failure.Code = code
	return failure
}

// queryFailureReason classifies errors without using their possibly private text.
// Callers must check the request context first: only a child timeout is recoverable.
func queryFailureReason(err error) string {
	var command CommandFailure
	switch {
	case errors.As(err, &command):
		return ResolutionFailureReason(command.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return "owner_query_timeout"
	case errors.Is(err, os.ErrPermission):
		return "owner_permission_denied"
	default:
		return "owner_resolution_failed"
	}
}

func queryFailureTarget(provider, scope string, err error) ResolvedTarget {
	reason := queryFailureReason(err)
	return ResolvedTarget{Provider: provider, Scope: scope, Reason: reason, ResolutionError: reason}
}
