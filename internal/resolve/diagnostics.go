package resolve

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func failureReason(err error) string {
	var command manage.CommandFailure
	switch {
	case errors.As(err, &command):
		return manage.ResolutionFailureReason(command.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return "owner_query_timeout"
	case errors.Is(err, os.ErrPermission):
		return "owner_permission_denied"
	}
	// Route returns fixed policy codes before running the manager.
	if code := manage.ResolutionFailureReason(err.Error()); strings.HasPrefix(code, "python_") || code == "uv_receipt_source_settings_require_review" || code == "owner_version_pinned" {
		return code
	}
	// Only our own fixed errors are mapped; never expose an unknown error string.
	switch err.Error() {
	case "python candidate unavailable":
		return "owner_candidate_unavailable"
	case "original_manager_unavailable":
		return "owner_unavailable"
	case "installation permission unverified", "manager permission unverified":
		return "owner_permission_denied"
	case "manager root invalid", "manager root differs from installation", "installation_outside_manager_scope", "manager_prefix_mismatch", "original_manager_scope_unverified":
		return "owner_layout_unsupported"
	case "installed package is not the manager direct target", "not_direct_global_dependency", "python_dependency_not_direct_tool", "python_tool_receipt_unverified", "not a direct Bun installation", "Bun global manifest unavailable":
		return "owner_receipt_unsupported"
	case "package_source_unverified", "untrusted_or_custom_tap", "manager registry unsupported", "recorded source differs from current registry", "cask source changed", "brew source requires review", "brew dependency source requires review":
		return "owner_source_requires_review"
	case "brew version pinned", "package version pinned":
		return "owner_version_pinned"
	case "brew installation changed", "python installation changed", "active_version_unverified":
		return "owner_installation_changed"
	case "version scheme needs owner review", "brew version scheme unsupported", "Bun version scheme unsupported", "python candidate not a newer numeric release":
		return "owner_version_unsupported"
	case "cask effects unknown", "cask installation effects need review":
		return "owner_effects_require_review"
	case "manager configuration unreadable":
		return "owner_configuration_unreadable"
	case "installed metadata invalid", "owner candidate unavailable", "brew metadata unavailable", "cask candidate unverified", "python owner query unavailable", "Bun candidate unavailable":
		return "owner_metadata_invalid"
	default:
		return "owner_resolution_failed"
	}
}
