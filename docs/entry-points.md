# Product entry points

KeepUpdated has six entry points and one inventory, resolver and management engine. Version numbers alone do not establish matching contents: compare source commits, digests, executables and the paired Skill. For 0.1.2 / build 10, only the CLI, matching Skill, source and checksums are public release assets.

## Implementation and package contents

| Form | Implementation | Delivered contents and identity |
| --- | --- | --- |
| App | `desktop/` invokes the bundled CLI; native companion handles authorization | `KeepUpdated.app`: desktop UI, CLI, Skill, authorization companion, helper and pinned mas input. Helper presence does not grant authorization; App Store updates stay manual. Plist version/build, `DESKTOP_BUILDINFO.json`, CLI `BUILDINFO.json`. |
| CLI | `cmd/keepupdated/`, shared `internal/audit`, `resolve` and `manage` | Tarball: executable, Skill, build metadata and linked notices. No native companion. `version --format json` binds product version, commit, platform and data schemas. |
| MCP | `internal/localmcp/`, launched by CLI `mcp` | Same executable, eight stdio tools, immutable plans and per-operation recovery. `get_capabilities` reports version/source, shared schemas, native approval availability and fixed scope/limits. No separate product version. |
| Skill | `skills/keepupdated/` | Same instructions and version/schema-checked CLI launcher in CLI, App and Agent packages. Connected MCP workflow uses native confirmation; explicit local CLI workflow uses exact-plan confirmation. No independent executor or automatic installation. |
| Desktop plugin | `plugin/`, `scripts/plugin/` | Portable and Codex manifests, stdio launch configurations, icon, top-level Skill and delegating launcher, complete sealed Agent App and payload manifest. Both manifests must match the CLI product version. Marketplace catalog stays outside the plugin ZIP. |
| Independent Agent | `scripts/agent/`, existing authorization companion | `KeepUpdatedAgent.app`: CLI, Skill, notices, ordinary native confirmation, `MCP_LAUNCH.json`, `AGENT_BUILDINFO.json`; sibling `KeepUpdatedAgent_PAYLOAD.json` binds signed executables. No inventory window, privileged helper or mas. |

The plugin and Agent expose MCP by default. Their included Skill can also use the
paired CLI within its existing permissions. They do not constitute separate update
engines. Do not extract an executable from its sealed App to work around missing
native approval. No package installs a global Skill, edits client settings or starts
a persistent service.

## Capabilities and intentional boundaries

In the table, **shared** means the same guarded engine and original managers, after
an exact preview and confirmation. It does not mean every discovered installation
can be changed. Provider coverage and tested layouts remain in [coverage](coverage.md).
Plugin and Agent columns describe their MCP entry; their local Skill entry follows
the Skill column.

| Operation | App | CLI | MCP | Skill | Plugin | Agent |
| --- | --- | --- | --- | --- | --- | --- |
| Inventory and optional online update checks | Shared | `scan` / `check --online` | `scan_inventory` / `check_updates` | CLI or connected MCP | MCP | MCP |
| Custom scan locations | Scan Locations | Explicit root/config/project flags | Default locations only | CLI flags; MCP boundary retained | MCP | MCP |
| Exact selection | Original IDs, selected/all scope | Repeated `--id`, `--ids-file` or requested `--all` | 1–100 exact IDs; pages of at most 50 | Uses chosen interface's scope | MCP | MCP |
| Update/uninstall preview | Shared exact-ID plan from product installation choices | `plan` | `preview_changes` and opaque handle | Uses chosen interface | MCP | MCP |
| Ordinary execution | Explicit UI confirmation | `apply --confirm PLAN_ID` | `request_execution`, native dialog | CLI exact-plan or MCP native confirmation | MCP | MCP |
| Scripts, supported runtime updates, quit/reopen | Separate review and explicit choices | `--special`, runtime consent, exact lifecycle flags | Bounded preview options; native confirmation | Documents both routes | MCP | MCP |
| Supported skill-only plugin updates | Whole-plugin and marketplace effects | Same engine; explicit cached mode | Same engine; explicit cached mode | Same scope; client reload is separate | MCP | MCP |
| MCP reference migration | Separate configuration review | Exact `--special --mcp-reference` plan | Not exposed | CLI flags; client acceptance remains open | Not through MCP | Not through MCP |
| Privileged PKG, system/restart | Dedicated native contracts; device gates open | Needs matching full App bridge; not in standalone tarball | Not exposed | Dedicated `special` command not exposed | No privileged helper | No privileged helper |
| KeepUpdated self-update | Dedicated Settings flow; signed-release gates open | Dedicated command with full App identity checks | Not exposed | Dedicated command not exposed | Not through MCP | Not through MCP |
| Cancellation | Existing stage-specific UI controls | SIGINT/SIGTERM, bounded cleanup | Decline before execution; no cancel tool after submission | Follows chosen interface | MCP | MCP |
| Ordinary result recovery | Latest saved ordinary operation | `results` | `get_operation` with exact operation ID | Explains and uses the matching record | MCP | MCP |

App Store installation is manual across all forms. Apple Silicon OS installation
remains blocked. An App-only presentation, a manager's refusal or a missing native
privilege contract is not a synchronization defect to bypass. The full App's helper
and mas payload are intentional distribution differences from the lightweight Agent.

Cancellation and failure do not roll back every manager operation or prove success.
MCP workers continue after chat/stdio closes; the server retains results by operation
ID. CLI/App ordinary recovery retains only the latest operation, which must match the
expected plan ID. Dedicated special/self-update records remain separate. Unknown and
unreadable records never authorize replay; use a fresh reviewed plan after inspection.

## Version and content checks

Current source uses product version/build 0.1.2 / 10 across all six entry points. App, Agent and plugin remain local candidates. Protocol schema IDs are unchanged.
CLI `version` now declares `recent_operation_schema` so the Skill refuses `results`
against an older same-version binary without that contract. Existing scan/plan/apply
compatibility remains intact. MCP capabilities expose the shared scan/plan/apply
schemas, default-location scope, per-operation recovery and lack of execution
cancellation. These are additive descriptions of existing behavior.

The [release version table](release.md#release-policy) covers App, CLI, Skill, Agent,
plugin manifests, verification and CI. Every candidate must use a selected committed
baseline. The [cross-package readback](release.md#cross-package-candidate-readback)
compares actual extracted artifacts and catches stale binaries/metadata, a different
App/Agent source digest or build, mismatched plugin versions and stale Skill content.
Each builder still owns signatures, notices, architecture and its package-specific
payload checks; the plugin verifier also initializes both launch configurations and
checks that MCP schemas match the CLI.

## Acceptance layers

Source tests, controlled fixtures, extracted packages, installed copies and actual
Agent/client interactions are separate evidence. A source-only correction does not
update an installed plugin, and a newer App preview does not update a retained CLI
or Agent archive. Build new candidates from one reviewed source commit and record all four archives and their six entry-point checks privately.

The existing [client/device validation limits](coverage.md#validation-limits) still apply. Wrapper
and stdio checks do not establish an LLM client's native confirmation workflow,
VoiceOver, minimum-OS hardware, signed privileged execution or public distribution.
No host update, global installation or client-configuration change is required to
verify synchronization.
