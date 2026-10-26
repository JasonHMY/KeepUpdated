# Coverage and limits

This page applies to 0.1.2 / build 10. Its public release assets are the CLI with matching Skill, the source archive and checksums; App, Agent and plugin packages remain local candidates. Inventory coverage does not imply automatic update support.

## Classification and evidence

This page is the current capability index. Provider guides below define exact layouts and guards. The validation limits below distinguish implemented paths from device and distribution acceptance.

Describe each object by type, owner, scope and associations before assigning an
operation. These dimensions reuse the current [report contract](report.md) and
[plan bindings](../internal/manage/model.go); they do not add JSON fields.

| User-facing dimension | Existing fields and source | Interpretation and limit |
| --- | --- | --- |
| Object type | Inventory `kind`; [constructors](../internal/audit/model.go) | Apps, packages, executable entries, runtimes, Python environments, Skills, MCP definitions and OS records differ. A formula/cask can supply an app or runtime. An executable can also be a package manager. |
| Management owner | `owner`, owner evidence; [App/cask association](../internal/audit/apps_brew.go) | The identified manager, client, parent app or discovery context. `PATH` and `configuration` are contexts, not executable update providers. An app needs an actual cask artifact match before Homebrew owns its update. |
| Source and channel | `source`, `channel`, `evidence`, `update.code`; [package sources](../internal/audit/packages.go) | Adapter-specific source information or lookup candidate. Public npm/PyPI candidates, current manager configuration and local receipt declarations do not prove historical installation origin or payload integrity. `channel=latest` need not be the user's original selector. |
| Installation and usage scope | `path`, `real_path`, activity fields, owner evidence; [MCP definitions](../internal/audit/configs_skills.go) | Distinguish manager prefix, tool environment, user/client/project configuration and remote reference when evidence permits. There is no universal inventory scope enum. Unknown activity or scope stays unknown; an explicit project does not authorize a full dependency crawl. |
| Plugin registration scope | `plugin.scope`, `plugin_cache`; [plugin records](../internal/audit/plugin_skills.go) | Registered user/project/local scope and cache evidence, not proof that a session loaded it. The accepted Claude update lane is user scope only. Codex cache versions remain unverified. |
| Product and installation identity | `identity`, `id`, `path`, `real_path` | Product/package identity can repeat across installations. Inventory IDs identify observations by kind and path. A shared name or identity does not merge independent copies. MCP paths are encoded definition locators; their resolved command may be shared. |
| Associations and dependencies | `related`, `dependencies`; [dependency checks](../internal/audit/dependencies.go) | `related` is an untyped item-ID list. `dependencies.relationship` distinguishes declared dependency roles, but neither is a complete typed installation/consumer graph. Absence of a link does not establish absence of consumers. |
| Action and affected scope | Plan `provider`, `binding.scope`, `affected_ids`, `affected_paths`; [planning](../internal/manage/plan.go) | The provider can differ from inventory owner after resolution. Scope is adapter-specific: prefix, environment, plugin root or configuration section. Only the fresh plan determines ready/manual/blocked/skipped and binds the exact effects. |

The user-facing **OS** category means macOS (`kind=os`). Do not classify every
`owner=system` record as an OS: client-bundled Skills also use that owner and
remain Skills. Bun can be both a runtime and the manager of global Node packages;
those roles do not create two installations. Python tool support means the
supported direct pipx/uv tools, not every Python environment or dependency.

Keep three counts separate: inventory observations, independent installations
where established, and display product groups. `summary.total` counts inventory
rows. The scanner deduplicates only identical IDs. Planning separately checks
`provider + real_path + identity + binding.scope` and matching action evidence;
only then can repeated selections use `duplicate_installation_target` and one
canonical action. A conflicting duplicate is blocked rather than silently merged.
Preserve MCP references and Skill aliases for selection and impact, without counting them as additional
verified installed packages. Do not infer a complete installation count from
names, `related` or `real_path` alone.

Compatibility remains `keepupdated/v1`, `keepupdated/plan/v1` and
`keepupdated/apply/v1`. Keep IDs, raw field meanings, saved plan bytes and unknown
reason codes intact. Display labels do not rewrite source evidence or grant
execution. Missing optional evidence remains unknown.

## First-release support matrix

This table describes this source snapshot. **Yes** means the narrow documented path is
implemented. **Separate** means an exact additional preview/consent is required.
Every update and uninstall still requires confirmation and fresh source/content
checks. A preview may legitimately return manual or blocked.

**Unknown** means the available evidence cannot answer the question. **Manual**
means use the original owner or an explicit user route. **Unsupported** means
KeepUpdated offers no executable path for that operation. **Unverified** describes
missing acceptance or failed post-state verification; it is not another object
type. The literal report/plan/result statuses remain as defined in
[the report contract](report.md#plans-and-execution-results). `available` does not
establish compatibility or approval, and a successful command alone is not a
verified update.

| Object / owning path | Discover | Check for updates | Preview | Execute update | Uninstall | Post-state verification and real acceptance |
| --- | --- | --- | --- | --- | --- | --- |
| Homebrew core formula; ordinary app-only cask | Receipts, kegs, actual app artifacts | Verified core/cask source | Yes | Yes; runtime rules below | Yes, within guards | Exact owner/version/path readback; controlled mutation fixtures only. Real Homebrew upgrade remains unverified. Custom taps, pins and complex installers stay manual. |
| Standalone App | Known application roots | Supported vendor, Sparkle and Electron sources | Yes for supported actions | Signed full Sparkle ZIP / supported single-app DMG only | Ordinary user-manageable non-running bundles to Trash; optional exact supported cleanup | Signature, bundle/build and installed content checks. Narrow signed ZIP/DMG paths; [native-app limits](native-app-updates.md) apply. Other vendor installers remain manual. |
| App Store App | Receipt and bundle identity | Explicit country + Mac product lookup | Update returns manual; removal has ordinary app guards | Manual store route | Eligible ordinary app removal only | No automatic store transaction. A verified product link is navigation evidence, not install acceptance. See [App Store policy](providers/app-store.md). |
| Global npm / pnpm / Bun package | Known and supported explicit roots | Public candidates or verified owner query | Yes for supported manager/layout/selector | Yes; scripts need separate consent | Yes | Exact version, preserved selector and every declared command entry. Limited to the [documented versions/layouts](node-package-updates.md). |
| Direct pipx / uv tool | Supported environments and receipts | Public candidate or original-manager query | Yes for supported receipt/backend | Yes | Yes | Exact dist-info, original intent/interpreter and all exposed commands; removal verifies environment and entries. [Supported combinations](python-tools.md) cover specified pipx/uv versions only. |
| Other Python environment / dependency | Known site-packages, configured Homebrew prefixes and explicit project .venv; bounded scan | Candidate/static dependency checks where supported; origin may be unknown | Manual | Unsupported by the tool provider | Unsupported by the tool provider | No general pip, Conda or environment solver/update support; Python package commands are not run to verify health. |
| Protected Homebrew runtime/tool | Linked active keg + PATH evidence | Original core owner | Separate single-item runtime/script consent | Narrow supported formulae | Manual | Content/active binding/consumer guards; real Homebrew mutation unverified. Major transitions and versioned/pinned formulae stay manual. See [runtime scope](runtime-updates.md). |
| Official standalone uv | Narrow receipt + uv/uvx identity | Exact source during preview | Separate | Yes | Manual | Official target binary hashes, receipt and recovery checks; narrow official-source path. Custom prefixes/receipts remain manual. |
| Other standalone CLI / retained runtime; Volta | Known PATH entries/retained roots; Volta layout v4 records | Owner-specific or unknown; no generic standalone updater | Manual | Unsupported generic path; Volta manual | Manual | Stored Volta selections do not prove active versions or registry origin. Finding a Yarn image does not implement a Yarn provider. |
| Independent user Skill: Git / verified copy | Known or explicit roots, aliases and receipts | Exact supported GitHub source/ref/content | Yes | Yes, within source and baseline guards | User directory/link to Trash | Exact revision/content and backups. Verified source/content binding; local edits and incomplete baselines block replacement. Link removal preserves its target. |
| Supported Claude skill-only plugin | Skill + verified plugin registry | Public Git marketplace during preview | Whole plugin + marketplace effects | Yes, enabled user scope only | Manual in client | Registry, native list and complete plugin content. Whole-plugin effects; [reload remains separate](providers/plugin-skills.md). |
| Other plugin/cache, client-bundled or Skillshare-owned Skill | Bounded roots and supported provenance | Owner-specific/manual; cache activity can be unverified | Manual/blocked for updates | Manual through owner | Client-managed entries manual; eligible user Skill entries retain ordinary removal guards | Cache presence is not registration or session reload. Skillshare provenance never enables the copied-Skill updater. |
| Local MCP with verified installed owner | Static definition and executable binding, including supported configured manager roots/bins | Original package owner | Yes | Delegates to the unique verified package | Supported local definition only | Shared package executes once; package readback does not prove server startup/reload. Exact ownership/reference guards; [MCP contract](mcp-updates.md). |
| Local MCP launcher reference | Supported static npx/bunx/uvx/uv tool run forms | Narrow Codex+npx source-bound candidate query; other forms require exact input | Separate exact reference | Configuration edit only | Supported local definition only | Byte-preserving edit + backup/readback; result is `configured`. No download, server startup or real-client reload acceptance follows from the edit. |
| Remote MCP | Static connection definition | Unsupported remote-version check | Supported definition removal only | Unsupported remote service update | Local connection definition only | Configuration backup/readback; remote endpoint is never contacted or uninstalled. This is a reference, not a local software installation. |
| macOS OS | Version and architecture | Explicit `--system-updates` query | Typed separate preparation paths only | No accepted packaged OS-install support | Unsupported | Apple Silicon install is blocked; Intel fixture path is not supported hardware. Install/restart/device acceptance remains open. |
| Privileged script-free app PKG | Selected package bound to existing app/receipt | Explicit candidate preparation | Separate native bridge flow | Implemented boundary; signed-device acceptance open | No generic PKG removal | Controlled authorization/recovery tests only. Script-bearing/distribution PKGs blocked; standalone archive lacks the native bridge. |

The validation limits below describe remaining client/device gates. General launchd service control, arbitrary editor/host
extensions and unlisted executors are unsupported. Observed services remain
health context, not another automatic update target.

The [six-entry-point comparison](entry-points.md) records App/CLI/MCP/Skill/plugin/
Agent exposure. Ordinary providers share the engine. Graceful quit and verified
reopen require explicit lifecycle choices; combined real signed-app/lifecycle
acceptance remains open. Skill wrappers forward supported plan flags, but that
does not establish special-workflow client acceptance, and they do not expose
dedicated `special` or `self-update` commands. KeepUpdated self-update uses its
separate contract: ad-hoc builds cannot install it; real same-Team signed versions,
public assets and recovery remain release gates. `results` reads the latest
ordinary operation only and never grants retry. macOS 13+ Apple Silicon remains
the target; minimum-OS hardware is unverified and Intel is cross-compiled only.

## Validation limits

Source tests, controlled fixtures, isolated real-manager samples, actual extracted packages and customer-device behavior are different evidence. A supported adapter is limited to the documented source, manager version, layout and guards. Isolated samples do not certify arbitrary apps, managers or client versions.

- macOS 13 hardware testing remains pending. Intel has compilation checks only and no release package.
- Homebrew mutations use controlled commands; real Homebrew upgrade acceptance remains open. Node and Python support is limited to the exact combinations in their guides.
- Native signed-app ZIP/DMG paths have isolated samples. Arbitrary third-party save dialogs, combined signed-app exit/update/reopen and customer-download behavior remain unverified. Process interruption checks do not establish power-loss durability.
- App Store updates are manual. Apple Silicon OS installation is blocked; Intel system fixtures do not establish packaged OS installation support.
- Script-free component PKG, helper registration/revocation, native authorization/XPC, restart and recovery require signed-device and disposable-VM acceptance. Script-bearing/distribution PKGs remain blocked. The CLI tarball has no native bridge.
- Local App, Agent and plugin builds are ad-hoc signed. Developer ID identity, notarization, quarantined download/launch and same-Team self-update acceptance remain open. The ad-hoc App cannot install an in-app update.
- A wrapper or stdio check does not establish every Agent client's Skill loading, desktop chat, native confirmation, live reload, VoiceOver or customer-device acceptance. Plugin registration does not prove a running session loaded its new version.
- MCP reference replacement changes configuration only. Public-registry server installation/startup and real-client reload are not established by that edit.
- Manager operations have no general rollback guarantee. A finished operation requires per-item readback; unknown, interrupted or unreadable records never authorize replay.

## Provider guides

- [Native app updates](native-app-updates.md) and [graceful exit/reopening](application-lifecycle.md)
- [Node packages](node-package-updates.md), [Python tools](python-tools.md) and [runtimes](runtime-updates.md)
- [MCP ownership and references](mcp-updates.md), [plugin-owned Skills](providers/plugin-skills.md), and [manual App Store updates](providers/app-store.md)
- [Desktop use](desktop.md), [MCP tools](mcp.md), [entry points](entry-points.md), [privileged contracts](../privileged/README.md) and [signed distribution](macos-distribution.md)

The [report contract](report.md), [progress contract](progress.md) and [source references](sources.md) describe machine data and upstream interfaces. They do not extend update authority.
