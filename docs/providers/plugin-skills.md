# Plugin-managed Skills

A selected Claude Skill can check its public marketplace and update its owning plugin through the native client. The preview covers one marketplace metadata refresh and one whole-plugin update, including the other affected Skills. KeepUpdated never replaces plugin cache files or edits the client's installation registry itself.

## Inventory and counts

Discovery stops below each `SKILL.md` root so embedded upstream/reference examples are not counted as separate installations. Category folders without an entry remain traversable, and an explicitly configured nested root is still scanned. Hidden backups matching `.name.bak-YYYYMMDDHHMMSS` or `.name.bak-label-YYYYMMDD` are skipped. Ordinary names containing `backup` and hidden system Skills remain discoverable. No files are removed or moved.

The desktop Skills count represents presentation groups for ordinary same-name Skills, plus system and registered plugin Skills. It is separate from installation-entry counts. Real-path aliases and independent same-name copies remain expandable, with their original paths, IDs and independent actions; sharing a name does not prove equal content or authorize a combined update.

The optional `plugin_cache` report field records client, cache root, plugin ID, version and registration state separately from `plugin` ownership. A complete supported Claude registry distinguishes registered roots from unregistered caches, including old versions. Invalid, missing or ambiguous registration remains unverified. Unregistered and unverified cache entries remain visible in folders and have separate counts. An explicit alias to an unregistered cache is retained as an unverified external reference.

Codex cache layout and `enabled` settings do not establish the currently registered version, and remote plugins may have no local setting. Codex caches therefore stay unverified; no version is selected by sorting names or timestamps. Discovery does not invoke a client or claim a running session has loaded a plugin.

## Supported lane

The first lane supports enabled, user-scope Claude plugins from a Git marketplace with a GitHub HTTPS origin and a relative plugin source. It verifies the original source, installed commit, complete plugin content and the marketplace's current state. Plugins can use an explicit numeric version or Claude's commit-derived version when the manifest has no version. Markdown Skills, Markdown documentation and license/notice files are supported; the plugin manifest must contain metadata only.

The default preview reads the public branch and an isolated temporary Git snapshot. It validates the candidate catalog, plugin files and version without refreshing the installed marketplace. The old source commit must remain in the approved target's history. A moved ref during preparation, a pin, local edits, an unverified or modified target cache, or an ambiguous source stops the update.

Apply saves the original plugin files, registry and marketplace catalog. It then calls `claude plugin marketplace update <name> --json` and reads the result independently. The source, commit, catalog and plugin content must match the approved preview before `claude plugin update <id> --scope user --json` can run. If refresh fails, advances to another commit, or changes the source, plugin installation stops. The result reports any observed marketplace change separately; a stopped installation does not imply the source was untouched or rolled back. A failed or incomplete refresh verification sets `state_known` to false even when its commit and catalog did not change, because ordinary files may have changed.

The native plugin-update command also attempts a marketplace refresh and has no exact-version flag. Its final update and list commands therefore run under the macOS system `sandbox-exec` network-deny profile. This blocks both Git and the client's GitHub archive downloads, keeping the verified local snapshot fixed for that invocation. The native command may report a refresh failure while installing the approved snapshot. Exit status alone never establishes success.

`sandbox-exec` is a deprecated macOS facility, not a new privileged helper or background service. Its executable fingerprint is bound to the plan. If it is unavailable or the sandboxed owner command fails, this lane cannot execute an unisolated fallback. A future platform replacement needs its own implementation and acceptance.

## Cached fallback and archive caches

`plan --cached-plugins` explicitly selects the verified local snapshot without authorizing a marketplace refresh. The selection is stored in the plan and reused by apply. It makes no latest-remote claim. An archive cache may still need a read-only public Git snapshot to verify its existing content.

Current Claude versions can replace a Git marketplace checkout with an archive containing `.gcs-sha`. KeepUpdated treats that marker as a claim and verifies the cached files against the corresponding public Git commit. The native archive omits the entire `.github` repository-automation directory; a partial `.github` directory is still checked. Missing or modified files elsewhere are rejected. The selected plugin and catalog must be complete. Empty client bookkeeping directories do not change Git file equality, but the installed plugin's full filesystem fingerprint still binds them.

## Boundaries and recovery

Directory/file marketplaces can load directly from source folders, so their cache directories do not establish the loaded version. Those sources, GitHub shorthand, remote/archive/command source declarations, pinned refs, custom config roots, project/local/managed scopes, and ambiguous installations remain with the client. A locally generated archive for an otherwise verified Git source is supported as described above.

Hooks, MCP servers, plugin dependencies, executable files, package manifests and other runtime components require a separate implementation and review. This lane does not accept an install command, launch an agent session, quit an app, enable a plugin, or remove and reinstall one. External Skill links fixed to a cache version remain manual: a whole-plugin update would not migrate those links. Explicit versions cannot silently downgrade or change content without changing version. Commit-derived versions are bound to verified Git history.

Before the owner command, KeepUpdated saves and verifies the original plugin files and registry in private action state. Fresh updates also retain the previous marketplace catalog and binding. A failed command with unchanged original state is reported as failed with the original preserved. Partial changes or uncertain readback retain the backup and require inspection through Claude. A deleted or unreadable approved target cache cannot be reported as the complete original state preserved, even if the active registry entry did not change. KeepUpdated does not restore raw client state automatically. Do not overwrite the whole saved registry, which may include other plugin changes.

After update, the adapter reads the installation registry and `claude plugin list --json`, then checks the installed commit, path, version and Skill files. `updated` means the registered disk installation was verified. `reload_required: true` means existing sessions still need `/reload-plugins` or a new session. No running session's loaded version is claimed.

The inspected Codex CLI exposes plugin add/list/remove and marketplace upgrade, but no standalone plugin-update command. Marketplace upgrade has not been accepted as an exact, single-plugin update path. Codex cache Skills remain manual with an instruction to use the client's plugin management. System Skills follow client upgrades. Existing independent Git and copied-Skill providers are unchanged.

## Desktop contract

Schema IDs stay unchanged. Optional `Item.plugin`, `PlanItem.plugin_update`, `ItemResult.reload_required` and `ItemResult.marketplace_refresh` carry the provider data. `Selection.cached_plugins` is an explicit opt-in; the default is remote discovery. The same plugin selected through multiple Skills executes once, with the other rows marked `duplicate_installation_target`.

When distinct selected plugins share a marketplace, only a completed, verified refresh from this batch can satisfy their refresh step. The backend captures the exact post-refresh per-plugin plan and still requires full equality at the next preflight. It does not accept later source, content, settings, client or remote-commit drift. Native commands may also prepare another selected plugin's target cache. Only a previously absent cache whose every file matches the approved snapshot can be reconciled; its complete fingerprint is then bound. A preexisting approved cache must remain unchanged.

The desktop must expose the preview action for registered plugin-cache rows. A fresh preview must show `refresh_marketplace`, the marketplace path, before/approved commits and whole-plugin scope. A cached preview must say remote freshness is not established. After apply, render `marketplace_refresh` even when the plugin's `executed` is false. If `state_known` is false, `changed: false` must not be displayed as verified unchanged: its attempted/command_succeeded/state_known/changed fields describe source effects independently. Show the reload action after a verified installation.

The current desktop preserves the original plan JSON for apply and must continue preserving optional provider fields. [plugin-skills-ui.json](plugin-skills-ui.json) contains synthetic JSON examples and English/Simplified Chinese copy. The desktop retains the same fields and boundaries.

## Verification

Controlled tests use real isolated Git repositories with controlled transport and owner commands. They cover source discovery, aliases, deduplication, local edits, pins, source/settings/manager drift, target cache preparation and changes, unsupported components, backups and failed or unactivated installs. Fresh-source cases include read-only preview, exact refresh, Git-to-archive refresh, moving remotes, partial refresh, source replacement and modified archives.

Run real owner acceptance with a built CLI and an existing Claude executable:

```sh
python3 scripts/acceptance/plugin_owner.py --cli /path/to/keepupdated --claude /path/to/claude
python3 scripts/acceptance/plugin_remote.py --cli /path/to/keepupdated --claude /path/to/claude
python3 scripts/acceptance/plugin_remote.py --batch --cli /path/to/keepupdated --claude /path/to/claude
```

The first script uses a synthetic local marketplace and the explicit cached lane. The second downloads the inspected historical Anthropic `frontend-design` source, installs it in a temporary home, and exercises the product's default scan/plan/apply path against the real public remote. The `--batch` variant checks two official plugins with one shared refresh. Both scripts use native install/update/list commands, check registration/content/backups, and delete only their own temporary fixtures. Neither launches an agent session or changes host plugins.

The inspected client is Claude Code 2.1.302. Plugin-update JSON requires at least 2.1.268; fresh marketplace-update JSON requires at least 2.1.287. The adapter uses the short version probe because this native build intermittently truncates long piped help output. Live session reload and common desktop presentation require separate acceptance.

## References

- [Claude plugin commands](https://code.claude.com/docs/en/plugins/cli-reference): update, scopes, JSON results and native listing.
- [Claude plugin manifest](https://code.claude.com/docs/en/plugins-reference): components and version behavior.
- [Anthropic's official marketplace](https://github.com/anthropics/claude-plugins-official): public native acceptance source.
- [Git environment variables](https://git-scm.com/docs/git): protocol and configuration isolation. Git isolation alone does not block native archive downloads.
- The installed macOS `sandbox-exec(1)` manual documents the per-process command and its deprecation.
- [OpenAI plugin documentation](https://learn.chatgpt.com/docs/plugins): client plugin management.
