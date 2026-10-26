# Local MCP

`keepupdated mcp` is a local stdio server using the shared CLI inventory, resolver and execution engine. The standalone CLI supports inventory and previews but has no sealed native confirmation companion for execution. Agent and plugin packages remain local candidates outside the public CLI-only 0.1.2 release.

The independent `KeepUpdatedAgent.app` contains the CLI, its matching repository
Skill, licenses and a small native confirmation companion. It has no inventory
window, daemon, network listener or global launcher. Start the server through
its packaged executable:

```sh
/Applications/KeepUpdatedAgent.app/Contents/Resources/keepupdated mcp
```

Configure that absolute command and the single argument `mcp` in your client's
local stdio configuration. The actual installation location may differ. The
package includes `MCP_LAUNCH.json` as relative launch metadata, not an automatically
installed client configuration or store manifest. KeepUpdated never edits a
client's settings or installs other MCP servers. A standalone CLI without the
sealed companion can scan and preview; execution is unavailable.

## Tools

| Tool | Input and result |
| --- | --- |
| `get_capabilities` | No options. Version/source, shared scan/plan/apply schemas, platform, default-location scope, per-operation recovery, lack of execution cancellation, native approval availability and fixed limits. Privileged execution remains blocked through this interface. |
| `scan_inventory` | No options. Scan the CLI's default locations and return counts, diagnostics and an opaque snapshot handle. |
| `check_updates` | Optional `online` Boolean. Public version queries require `true`. Retains unknown states and diagnostics in the saved snapshot. |
| `list_items` | Snapshot handle, offset and limit (default 20, maximum 50). Returns a page of installation records. |
| `get_item` | Snapshot handle and exact installation ID. Returns the saved record. |
| `preview_changes` | `action` (`update` or `uninstall`) and 1–100 exact IDs. Optional cleanup, script scope, runtime-update scope, cached-plugin mode, quit and reopen IDs. Saves an immutable plan and returns per-item summaries, expiry, plan handle and its operation ID. |
| `request_execution` | Only the opaque plan handle. Opens native confirmation, then returns this operation's state. Repeat requests return its existing decision; they do not approve or execute again. |
| `get_operation` | Only the operation ID, also returned during preview so a lost submission response can be recovered. Returns that operation's state and complete saved per-item results. |

The packaged Skill includes the eight-tool workflow and the corresponding CLI options. MCP `get_operation` uses the saved operation ID; CLI `results` is a separate latest-operation record and cannot replace it. See the [six-entry-point comparison](entry-points.md).

Preview options describe requested scope. They do not approve it. There is no
`approved` input, arbitrary command, environment, path, administrator request or
caller-controlled timeout. Custom scan roots, MCP reference migrations, special
privileged installs, restarts and self-updates use their existing CLI/desktop
contracts; the first MCP interface does not expose them.

## Approval and operation lifetime

The server reads its saved plan and generates the full native confirmation.
The dialog shows every selected item, blocked/manual entries, versions, cleanup,
script and app-exit effects, complete plan details and available Skill text diffs.
English and Simplified Chinese confirmation labels use the existing app language
preference, falling back to the system language and then English. Cancel is the
default. Approval expires after at most 120 seconds or the plan's earlier expiry.
A missing companion, cancellation, timeout or mismatched response starts no work.
The response binds a fresh challenge to the plan digest; no approval token is
returned to the model.

**After approval and submission, closing the chat does not cancel the operation.**
One short-lived detached worker owns the operation and its result. It uses the
existing cross-CLI execution lock, fresh source/content checks, attempted-plan
claim, backups and verification. Each manager retains its 15-minute maximum;
the complete operation has a separate fixed two-hour context deadline. There is
no tool that force-kills an installation. Process-group escape and power loss
remain outside the existing process/durability guarantees.

`finished` means execution returned, not that every installation succeeded.
Inspect each result, including blocked, skipped, failed and verification-unknown
items. A worker disappearance or unconfirmed handoff is `unknown`, never success.
A pre-execution failure can be `failed` with no changed items. Do not retry an old
operation; inspect recovery evidence, scan again and review a fresh plan. The
server does not restart workers automatically. Results are stored per operation
and cannot be replaced by another CLI command's global most-recent record.

This protects the MCP parameter interface. It does not provide unforgeable proof
of a human against a caller that already has arbitrary same-user shell, file or
Accessibility control. The separate Developer ID/helper/admin checks on existing
privileged execution are unchanged. An ad-hoc Agent bundle does not enable
privileged installation.

## Private state and bounds

Snapshots, immutable plans, decisions and operation checkpoints live in the
user's `Library/Application Support/KeepUpdated/actions/mcp` directory. Directories
and files must be owned and private; symlinks and unsafe ancestors are rejected.
Each file has a 16 MiB bound; native confirmation is limited to 4 MiB. On new
reads/previews, retention keeps the latest 64 snapshots and at most 64 plans.
It evicts the oldest unsubmitted previews as needed. Decided terminal records with resolved per-item results
remain through their plan's validity window (normally 30 minutes), then become
eligible for eviction. Active, interrupted, uncertain and damaged operation
records are preserved; backups and the enclosing action directory's attempted
records are never removed. If all plan slots are protected, new previews wait
for terminal expiry or recovery instead of discarding evidence. An evicted
handle returns an explicit unavailable/expired error and cannot submit or replay
work. Retention runs on demand, with no background job or manual cache deletion
required for repeated normal scans and previews. This is local recovery state,
not a synchronized history service.

MCP stdout contains only protocol messages. Server diagnostics go to stderr;
workers do not inherit the protocol streams or maintain unbounded log files.
The official Go SDK v1.8.0 negotiates legacy initialize-based protocol versions
and the 2026-07-28 protocol. Real client acceptance remains separate from SDK
transport tests; see [validation limits](coverage.md#validation-limits).

Build and verify an independent candidate using the [release guide](release.md).
Developer ID signing, notarization, older-device testing and public distribution
remain separate gates.

## Desktop plugin packaging and annotations

The [local plugin candidate](release.md#local-desktop-plugin-candidate) packages
this same server with its native confirmation App and repository Skill.
Installation grants no approval for a software update or uninstall.

| Tools | readOnlyHint | destructiveHint | openWorldHint |
| --- | --- | --- | --- |
| `get_capabilities`, `list_items`, `get_item`, `get_operation` | true | false | false |
| `scan_inventory` | false | true | false |
| `check_updates`, `preview_changes` | false | true | true |
| `request_execution` | false | true | true |

Scans and previews write private snapshots/plans and can evict older cache records;
their destructive hint reflects cache retention, not installation changes.
`check_updates` can query public metadata when requested, and previews can resolve
remote candidates. Execution may update, remove, quit or reopen the selected
software after native confirmation, including contacting upstream managers.
Annotations describe possible effects; they do not replace approval or guards.
