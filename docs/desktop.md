# Desktop and operation guide

This guide describes the local desktop source candidate, 0.1.2 / build 10. The proposed public assets are CLI-only. Opening the App starts a scan; every update or uninstall requires its own reviewed confirmation. The interface supports English and Simplified Chinese.

## Browsing the inventory

On macOS 27, the toolbar and category controls use Apple's native Liquid Glass. Following [Apple's adoption guidance](https://developer.apple.com/documentation/technologyoverviews/adopting-liquid-glass), glass is used for navigation and actions. Reduced transparency and earlier systems retain readable native fallback styles.

Choose a category at the top and search by name, installation path, source or status description. Rows show the name, recorded version and actions. Details start collapsed and expand to show status, reasons and paths. Source, evidence and dependencies in the detail window also expand on demand. Different installation paths retain their original identities. Category counts describe the main display groups, excluding supporting details; Homebrew retains its installation count and version-family behavior.

Checking for updates allows online queries. A public registry candidate from a read-only check may not be the installation's original source. Before execution, the plan confirms the target again through the original package manager. Expanded details show states such as not checked, missing Skill source record, unknown installation source or version-query timeout, with an explanation and next step.

Update availability and the ability to execute inside the app are evaluated separately. The updates-only filter uses check results. Items with a supported original manager can be checked and previewed before an update conclusion is available. Ordinary apps may only show how to update even when a newer version exists. Unknown and unsupported items remain visible; the actual preview determines whether execution is possible.

The public stable channel is separate from an app's automatic-update settings. Skill checks can read existing source lockfiles. When upstream changes exist but local content differs from the installation record, the changes view lets you inspect the diff; direct replacement cannot be confirmed.

Add other Agents' directories through scan locations. Selected paths stay in local settings. The app does not store configuration contents or change Agent loading rules. It scans explicit projects and known roots, without searching the entire disk for every project.

## Updating

Updating all checks the full scan scope, regardless of the current category or search. Alternatively, select rows, select all previewable items in the current filter, then preview the selected items. Category and search changes retain selections; clearing the selection removes all of them.

Global, selected and single-item operations all produce a preview first, grouped into ready, needs attention and no action needed. Ready items start expanded. Uninstall previews show unavailable items directly, and previews with nothing executable expand their results. Review current and target versions, affected items and paths, then confirm. Execution checks installation contents, manager, source and candidate again. Changes invalidate the old plan.

Copied Skills show added, modified, deleted, permission and binary changes directly in the plan; long text diffs start collapsed. After confirmation, the original directory is backed up before installing the pinned previewed content and reading it back. Multiple entries sharing one directory execute once; independent copies affect only the selected path. The app stores its own path bindings and does not rewrite the original manager's lockfile.

When the public GitHub API confirms a rate limit, Skill checks and previews can read a fixed revision through public Git, verify content and generate exact diffs. No new account, credentials or sign-in are required. A different read method is not approval to update. Local edits still block replacement, and execution still requires preview confirmation.

If local content differs from the installation baseline, sources conflict, diffs are too large or verification is incomplete, the app shows available explanations and diffs without offering forced replacement. On failure, it restores original content where safe. Concurrent changes or process interruption retain a recovery record, with an incomplete Skill update diagnostic. The backup path in results lets you inspect the original directory.

Node packages with installation scripts or dependency builds need a separate confirmation. Once approved, the original manager installs them. Graceful app exit and optional reopening require exact app selections and their own review. Refusal, timeout or identity drift stops the update. No forced exit or automatic restart is permitted.

Another operation cannot start while one is running. Scans and previews can be cancelled. Once an update or uninstall begins, wait for it to return so interruption is not mistaken for rollback. The UI shows the actual stage, without an estimated percentage. Final status follows each item's readback; a partial failure does not erase completed items.

Results summarize completed, failed or unverified, and unexecuted items. Failed items can be checked again to generate a new preview and confirmation; the old plan is not replayed. After closing results, the last result can be reopened during the current app session. The latest confirmed operation is saved privately and loaded into Last Results after reopening. Each item retains its recorded result and known backup or recovery locations. A missing final result means the operation may still be running or may have been interrupted; it does not prove its processes stopped. Reload Saved Results reads the latest snapshot without executing anything. Failed, unverified and not-yet-started items can be scanned again for a new preview using their original IDs. No executable plan, cleanup choice, app exit/reopen choice or privileged consent is restored. About and Help shows the packaged version, build and source commit, plus a source-tree digest for builds with local changes. Missing build information is shown as unknown. It also links to the user guide, support, privacy and private security reporting.

If the saved record cannot be read, a notice stays visible even after a successful scan. Open Last Results for the error details. An unreadable record blocks new Apply operations until it is inspected; keep a private copy for investigation rather than deleting backup files. The record is local to this Mac and contains installation names, IDs and recovery paths. Do not include it in public reports. Only the latest operation is retained; existing provider backups and recovery records keep their own lifecycle.

## Uninstalling and cleanup

Uninstalling an item or a folder has a separate confirmation. A folder selects all its original inventory members, including members hidden by search or collapsed subgroups. The preview lists executable removals and unavailable members; only executable actions run after confirmation. Version labels are read-only. Changing cleanup options regenerates the plan and shows the exact paths to be handled. App cleanup currently covers only Preferences, Caches and Saved Application State paths for the exact bundle ID. It does not guess other directories by similar names.

- Ordinary apps, user Skills and supported cleanup items move to Trash. Cases such as unsafe cross-volume moves fail and preserve the originals.
- Skill symlinks remove only the selected discovery entry, preserving the shared source and other entries.
- MCP removes one server definition from the specified configuration file, preserving other definitions, credentials and shared packages. A permission-restricted backup is created before writing.
- Packages uninstall through their original manager. Other known installation or MCP references block removal and are listed.
- Personal data whose ownership cannot be established is excluded from automatic cleanup. Plugin caches, system components and complex installers require the indicated separate handling.

## Agents use the same interface

Any Agent with local execution permission can read the packaged standard `SKILL.md` and call `scan`, `check`, `plan` and `apply`. KeepUpdated has no Agent brand allowlist or model-login requirement. A remote Agent sees its execution host; the user's environment controls whether the client can load a directory and execute commands.

Previews and results use the same JSON contract as the desktop app. Agents must not rewrite `manual` as success or bypass unsupported sources by modifying old plans or guessing shell commands.

## Known limits

The UI displays more categories than automatic operations cover. Only the narrow signed Sparkle ZIP/single-app DMG, original-manager and verified Skill/plugin paths in [coverage](coverage.md) can execute. App Store and general vendor installers remain manual. Privileged package/system paths lack signed-device acceptance; Apple Silicon OS installation is blocked. Complex YAML/JSON5 edits are unsupported. Git Skills require clean state, tracking and verified sources.

Local builds are ad-hoc signed without Developer ID or notarization. macOS 13 hardware, Intel runtime, every Agent client's native confirmation, VoiceOver, real privileged execution and customer-device self-update remain unverified. See [validation limits](coverage.md#validation-limits).

Collapsed inventory rows align icons and names at each hierarchy level, including rows without a disclosure arrow and rows in selection mode. They retain recorded versions, available updates, App Store/vendor links and ordinary removal actions. Check & Preview, Review MCP, and removal of runtime, command-line and system records appear in expanded details with ownership, source, check status, group summaries and paths. Copied Skills without a verified source remain listed with their explanation but no managed preview. Folder removal still opens a complete confirmation preview. Row expansion, selection and action buttons remain separate controls. All operation previews retain real inventory IDs.

Update All immediately lists apps for manual review while preparing the remaining inventory IDs. Each listed app retains its actual check status: current, unknown and available are distinct. Only a confirmed available update uses the App Store update label; other states keep a neutral store link. If every selected item is manual, no CLI plan is started. Unknown nonmanual items remain in the plan. App Store buttons use the existing verified product ID or the fixed Updates page; links open only when clicked. No store candidate or installation query runs during plan/apply, including for saved older plans.

Confirmed available updates for top-level apps without an installer offer **Open App** and **Open App to update yourself**. The same action is available when a preview ultimately needs manual handling. Opening rechecks the original path, bundle identity and version; it does not mark an update successful. Supported installation channels and explicit package reviews retain their existing previews.

All long operations use linear progress. Counted phases show processed work and the current object; preparation or authorization with an unknown total shows its stage without a percentage. Plan and apply stream progress through repeated preflight and result checks. Cancelling a preview and retrying starts a new progress stream; late callbacks from the previous command are ignored.
