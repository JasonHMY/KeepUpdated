# JSON contract: keepupdated/v1

This page describes the current snapshot contract. Optional provider, consent, lifecycle and recovery fields retain the existing schema IDs. Preserve original plan JSON for apply; configuration changes, marketplace refresh, app reopening and pending restart are separate results from verified installation.

Reports always include `schema`, `command`, UTC time, host OS/architecture, whether network access is allowed, `items`, `summary`, `diagnostics`, `coverage` and the actual local query list, `queries`. The CLI also writes `tool_version` and `source_commit` for comparison with the matching release package. Standard error is kept out of JSON.

Each item's `id` is derived from its category and discovery path. PATH is inherited from the calling process; interactive shell startup files are not read automatically. Symlinks to the same installation may remain separate entries; `real_path` identifies their shared target. Items are sorted by ID. Time and activity state may change between scans.

| Field | Meaning |
| --- | --- |
| `kind`, `name`, `identity` | Installation category, readable name and verifiable product identity; identity is omitted when unavailable |
| `path`, `real_path` | Discovery path and resolved real path. An MCP item's path is a locator made from separately encoded configuration path, scope and name, not an openable file path; the original configuration location is in evidence |
| `owner`, `source`, `channel` | Managing owner, version source and channel; unknown when unproven |
| `installed_build`, `latest_build` | Builds compared by Sparkle, stored separately from readable short versions |
| `dependencies` | Static dependency checks, each with name, optional relationship, publicly reportable version constraint, installed version, status and code; not an upgrade plan |
| `installed_version` | Version observed in installation metadata or a read-only version query |
| `active_version` | Version selected by PATH, Homebrew opt or the running app |
| `installed_versions` | Versions retained under one formula/cask; not evidence that all are in use |
| `active`, `activity_known` | First usable PATH entry, linked keg or matching process path. `activity_known=false` means activity is unverified; `active=false` must not be read as stopped. These fields do not determine uninstallability |
| `architecture` | Architecture from Mach-O or installation receipts; not a guarantee of update-package compatibility |
| `latest_version` | Version returned by the selected source; it may be only a public candidate with an unverified installation source |
| `evidence`, `related` | Check evidence and related item IDs; secret configuration contents are excluded |
| `health` | Local readability/entry state established by specific checks; `ok` does not establish full functionality or validation of all dependencies |
| `update` | Update result and stable reason code |

`update.status` is `current`, `available`, `unknown`, `unsupported` or `error`. `current` means versions match at a confirmed source; `available` means that source has a higher comparable version. Installation compatibility, dependency impact and operation confirmation use the separate plan contract below.

Version comparison accepts only numeric versions and Homebrew `_revision`. Prereleases, comma-combined versions, `latest`, unresolved active versions and installed versions ahead of the source are not forced into an up-to-date result.

Each item has one update status. `summary.total == len(items)`, and the remaining status counts sum to total. Individual failures retain the item while checks continue for others. Discovery gaps such as unreadable directories go into `diagnostics`. Diagnostics indicate that a scan may be incomplete; they do not invent installation counts for those directories.

Common reason codes include `online_query_required`, `no_update_source`, `not_found`, `timeout`, `query_failed`, `identity_mismatch`, `platform_variation_unresolved`, `no_matching_mac_product`, `public_registry_candidate_unverified_origin`, `active_keg_unresolved` and `command_resolved_not_started`. New codes do not remove existing fields. Callers should preserve unfamiliar codes and statuses.

`queries` lists only fixed local queries issued by the program; it is not an arbitrary execution interface. Reports do not retain replayable user configuration arguments. Reading configuration, finding a command path and successfully starting it are different evidence. This version performs only the first two.

Dependency `status` is `ok`, `error`, `unknown` or `skipped`. `skipped` means only that an optional package is absent or a platform condition does not apply. `health` summarizes dependency errors and unverified scope while retaining existing metadata/entry errors. `declared_dependencies_checked` establishes only static checks within the declared scope, not successful execution or the health of all transitive dependencies.

Additional reasons include `upstream_alias_requires_review`, `upstream_rename_requires_review`, `upstream_definition_absent`, `feed_constraints_unresolved`, `application_payload_activity_unknown` and `dependency_not_found_in_scanned_paths`. Upstream absence means public metadata and the official definition both returned 404. It does not infer why the item was removed or recommend uninstalling it.

Sparkle `current/available` uses build comparison, with default channel, OS version and explicit architecture restrictions participating in selection. This is a candidate result from a static feed. Runtime channels, custom version comparators, signatures, installation permissions and actual upgrade feasibility remain unverified.

`dependencies.relationship` distinguishes `dependency`, `optional`, `peer`, `optional-peer` and `runtime`. Same-named dependencies can have separate declarations; callers must not deduplicate by name alone. `runtime_binding_unverified`, `runtime_version_unverified` and `python_search_path_not_verified` indicate missing runtime or resolution-scope evidence, not broken software.

A `service` item's active/activity_known fields are meaningful only after a successful query of its current-user launchd state. Unqueried system daemons remain unknown. Log evidence is byte size only; historical last-exit is not interpreted as a current failure. `python-environment.installed_version` contains only a verified runtime version; the pyvenv creation version is retained as creation-version evidence.

`keepupdated version --format json` supplies version, commit, build_date, go_version, os, arch, schema, plan_schema, apply_schema and recent_operation_schema so clients can verify matching versions and operation protocols. Earlier scanners with the same version number but no operation protocols are not compatible binaries. A client invoking `results` must additionally check `recent_operation_schema=keepupdated/recent-operation/v1`; older same-version candidates may support plan/apply without saved-results recovery. Help and argument errors do not trigger scanning. Table and JSON output use the same report, including health errors, unknown counts and discovery diagnostics.

## Plans and execution results

`plan` returns `keepupdated/plan/v1`, including `plan_id`, creation/expiry times, `action`, the original selection and all `items`. Each item is `ready`, `manual`, `blocked` or `skipped`, with current/target versions, affected paths and items, optional cleanup paths, additional conditions and installation bindings used only for revalidation. Clients may reorder the display, but must pass the original JSON back for execution.

A complete CLI plan is limited to 16 MiB (16,777,216 bytes), including its two-space indentation and final newline. An oversized preview exits 2 before any plan JSON is written. Select fewer items, generate and review a fresh preview, then keep its original JSON for apply. Previously saved compact plans that exceed this limit when formatted for CLI output also require a smaller fresh preview; the plan schema, digest and apply input limit are unchanged.

Ordinary plans do not execute scripts requiring separate confirmation; `--special` creates a new plan. This flag cannot unblock unimplemented operations requiring elevation, exit or restart. Original-manager queries and execution use the same prefix, working directory and configuration scope. Source digests exclude configuration contents and credentials.

`apply --plan PATH --confirm PLAN_ID` checks time, plan digest, installation state, manager and source, then acquires a local exclusive lock and records that the plan has been attempted. Replayed, expired or changed-state plans are rejected. Each item is checked again immediately before execution and read back afterward. A zero manager exit code does not bypass verification.

Results use `keepupdated/apply/v1`, with `plan_id`, start/end times and per-item results:

| Field | Meaning |
| --- | --- |
| `status`, `reason` | `updated`, `uninstalled`, `failed`, `verification_unknown`, `cleanup_failed`, or retained `manual`, `blocked`, `skipped` states and their reasons |
| `before_version`, `after_version` | Versions before execution and after readback; unavailable values are not fabricated |
| `executed`, `command_succeeded` | Whether execution was attempted and whether the original manager returned success; neither replaces the final status |
| `changed` | Whether evidence confirms a change in installed state |
| `related_changes` | Before/after versions and change status of related items |
| `backup_path` | Local backup path for a targeted MCP configuration edit, if present |

Pre-execution rejection returns exit code 2. If the first operation record cannot be saved, no item starts: exit 2 retains the available result JSON with every item marked `operation_not_started`. The plan may already be claimed and must not be retried.

Once item processing starts, checkpoint or final-save failure returns exit 1 with the existing `keepupdated/apply/v1` JSON. It preserves `plan_id` (the ordinary operation identity), `original_ids`, known per-item outcomes and `state: in_progress`, with an empty `finished_at`. Stderr identifies the operation and says persistence is unconfirmed. Software may already have changed; stdout can contain a newer observation than the saved record. A failed checkpoint stops later items. An unfinished record does not prove that a manager has stopped.

If stdout cannot be written, including a closed pipe, apply returns 1 and identifies the operation on stderr. Output may be absent or incomplete. Inspect `results` and current installation state, and check that the saved `plan_id` matches; a saved record is not guaranteed to exist. This is a delivery failure, not evidence that execution was skipped.

Incomplete or unverified items also return 1. Exit code 0 requires a finished report with verified success or permitted up-to-date/duplicate-target skips for every item. Never automatically retry an old plan after an error, lost response or interruption. Inspect current state before generating and explicitly confirming a new preview.

The schema and wrapper arguments are unchanged. Clients must retain stdout on nonzero exits and decode only complete JSON. The desktop already decodes exit 1 and displays unfinished results; initial-save exit 2 still uses its error/recovery path. MCP and Agent workers call the shared engine directly and do not consume this CLI exit code. Older same-version binaries can discard result JSON and return 2 after execution, so their exit code alone cannot establish that nothing changed. Use the matching CLI and Skill from the same source build.

## Recent operation recovery

`keepupdated results --format json` is read-only and returns `keepupdated/recent-operation/v1` with an `operation` field. It is `null` if no saved operation exists. An invalid, oversized, unsupported or unsafe record exits 2 with an explanation on stderr; it is not treated as an empty or successful result.

`operation` uses `keepupdated/apply/v1` with additive `action`, `state` and `original_ids` fields. `state` is `in_progress` until the final report is saved, then `finished`; finished describes report completion, not item success. `finished_at` is empty for an unfinished operation. Results keep the original item order and IDs. A not-yet-started item is `skipped` with `operation_not_started`; a started item without a saved outcome is `verification_unknown` with `operation_in_progress_or_interrupted`. Neither proves that the original process has stopped. Known backup and recovery paths are recorded before installation changes; additional moved paths use `recovery_paths`. Paths may refer to a prepared location whose contents still need inspection.

The latest report is stored at `~/Library/Application Support/KeepUpdated/actions/recent-operation.json`, with a private directory and file, bounded strict decoding, atomic replacement and file/directory sync. Apply saves its identity and item list before installation changes, checkpoints item results, and stops later items if saving fails. A damaged or unsupported previous record is preserved and blocks a new Apply. Reading does not create state directories, take or release execution locks, or resume work. Existing attempt claims and execution locks still apply.

The record contains observations only, with no executable plan or reusable consent. Retry requires a fresh scan, matching original IDs, a new plan and explicit confirmation. Ordinary manager operations still have no general rollback guarantee. These checks cover controlled process interruption; sudden power loss and real signed-device recovery remain release acceptance work.
