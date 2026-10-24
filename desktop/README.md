# KeepUpdated macOS

This source prepares 0.1.2 / build 10 as a local desktop candidate. The proposed public release is CLI-only. The desktop includes English/Simplified Chinese UI, provider-specific previews, local PKG/system preparation, helper status and registration readback, operation recovery and separate restart previews. See [coverage and limits](../docs/coverage.md).

The App uses one Check for Updates action, alongside Update All and Scan Locations. The footer shows real work processed in the current check stage, including unresolved items; it is not a time estimate or update-success rate.

App rows show the installed app's icon, with a system fallback when unavailable. Select Items reveals batch checkboxes; ending selection clears the selection. Update All and single-item previews remain available without entering selection mode.

Inventory groups start collapsed. Skill titles show the skill name; shared client locations appear in the summary and expanded details. Registered plugins expose their individual Skills, while unverified and unregistered caches stay in a collapsed details section. Same-name MCP connections form display groups: expand one to see each client, disabled state and local or network connection. Linked package names identify the installation used for updates. A display group never becomes an operation target, and every original connection ID is retained.

Python Tools shows direct pipx/uv tools above their environments and dependency libraries. Dependencies cannot be selected as independent update targets. Language runtimes and terminal tools have separate categories; package-manager command aliases and background launch settings stay in details. Empty categories are hidden. Counts describe the main display groups rather than all supporting records; Homebrew retains its existing installation count and version-family behavior.

App components still require an explicit parent link and strict bundle containment. Python relationships require matching environment evidence and containment. Missing, conflicting or cyclic ownership is never inferred from a similar name. Every inventory record remains available in the tree and Source & Evidence; discovery and operation IDs are preserved.

Click outside a row's controls to expand its details and children. Folders offer a separate uninstall preview for all original members, including members hidden by search or collapsed subgroups; they have no update or selection control. A parent checkbox selects only that parent. Select All includes all matching previewable items, including items outside the current rendering window. The manager toolbar combines Home, Back, the category count, search and status filters. Click the search icon or press Command-F to expand search; a nonempty query stays visible. Search and Updates only expand matching ordinary groups with ancestor context; supporting details stay collapsed until opened. An explicit collapse lasts until the filter changes. Status and action columns stay aligned.

KeepUpdated checks its own public stable release once per app session, in parallel with the initial inventory scan. The footer and Settings share the result. Only a verified `up_to_date` result displays “App is up to date”; unavailable, unpublished, failed and newer-preview states remain distinct. Click to update opens Settings, where installation still requires its existing confirmation and verified signature. Reopening Settings or changing language does not repeat the startup check. Manual retries are available in Settings. Read-only version checks do not lock inventory or prevent closing Settings. App termination cancels only the read-only check; cancelled callbacks cannot replace a newer result. Prepare/install/handoff retain the exclusive operation gate and dismissal protection. About and Help remain in the app menus. Help shows the packaged version, build and source commit (or explicit unknown values), with links to the guide, support, privacy and private security reporting. Builds with local changes also show their source-tree digest.

Update Permissions is available from the relevant preparation, operation preview and diagnostics screens. App self-update installation has controlled TLS and signature fixture coverage. Public-release, Developer ID and packaged privileged-execution acceptance remain outstanding. Diagnostics still reflect report data gaps; signing alone does not clear them.

Use the categories and search at the top to browse installations. Updating all or individual items creates a plan first and executes it after confirmation. Uninstalling has a separate confirmation, with optional cleanup of settings and personal data whose ownership has been verified. Items requiring extra permissions, application exit, restart or installation scripts are listed separately. Unsupported, unknown and failed items remain visible in the plan.

When selected references point to one verified installation, the preview names the ready action that handles them. Each reference remains visible under Shared Installation References. Blocked/manual reasons stay intact; conflicting source, candidate or action evidence is not merged. Results link to the actual shared action and keep references in Not executed, including when that action fails or remains unverified.

The optional `canonical_id` field is omitted from older plans and reports. They still decode with their original hashes; an older duplicate plan whose rebuilt preview now includes a relationship needs a fresh preview and confirmation. The desktop continues sending the original plan bytes to apply.

Add other Agents' MCP configurations, Skill roots and project directories through scan locations. Only the selected paths are stored, not configuration contents.

Building requires Xcode with a macOS 26+ SDK. Earlier macOS versions retain native fallback styles.

For development, run from this directory:

```sh
swift test --scratch-path /private/tmp/keepupdated-desktop-build
swift build --scratch-path /private/tmp/keepupdated-desktop-build
```

Debug builds accept an absolute path to the matching CLI through `KEEPUPDATED_CLI_PATH`. Release builds always use `Contents/Resources/keepupdated` inside the app.

`--demo` uses a synthetic inventory to demonstrate categories, search, operation previews, confirmation and results. Demo updates and uninstalls do not run the real CLI or modify installations.

From the repository root, run `bash scripts/desktop/build.sh --out dist/product-v0.1.2` to produce `KeepUpdated_0.1.2_darwin_arm64.zip` and its checksum file. The ZIP contains the complete `KeepUpdated.app`. Verify and extract it, then move the app to Applications before opening it.

The script signs in a local temporary directory, then extracts the ZIP to check its signature, bundled CLI version and Skill entry point. The deliverable is a ZIP; no bare `.app` is exported into the source directory. Local candidates use ad-hoc signing, without Developer ID signing or Apple notarization.
