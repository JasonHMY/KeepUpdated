# Desktop language support

KeepUpdated supports English and Simplified Chinese. The default follows the primary system language; unsupported languages use English. Settings provides an immediate, persistent override. Demo mode keeps its language preference in a separate defaults domain.

`Resources/en.lproj/Localizable.strings` and `Resources/zh-Hans.lproj/Localizable.strings` contain the same keys. Keys are readable English fallback text. Use `L10n.text` with format arguments for displayed strings and `LocalizedMessage` for notices retained across language changes. Views observe `LanguageSettings.shared`. Dates and file sizes use the selected locale.

Machine reason codes, installation identities, paths, version values, source evidence and raw diagnostics remain unchanged. Known reasons are mapped in the desktop layer. Unknown codes get a localized explanation and retain the code in technical details. Unrecognized coverage prose is shown as a separately labelled original diagnostic record.

The desktop build copies the SwiftPM resource bundle into `Contents/Resources` and validates both language tables before signing. Localized `InfoPlist.strings` are also copied to the main app resources for the native restart permission purpose; system-owned authorization dialogs follow macOS language rules. It supports flat SwiftPM resource bundles and bundles with their own `Contents/Resources` directory. The app declares English as its development region and both supported localizations.

## Provider integration

The app retains the exact CLI plan bytes for `apply`; Swift decoding is for presentation only. A new preview must return the same explicit permissions that were requested.

- `selection.consents` and `required_consents` distinguish `runtime_update`, `privileged_install` and `restart_system`. Unknown permissions block execution. `special` does not authorize elevation, app exit or restart.
- `selection.quit_apps` and `reopen_apps` use exact application IDs. Reopen requires an exit selection. `applications` and `application_lifecycle` display bound identities and paths. Exit and reopen results are shown separately from installation results.
- `selection.mcp_reference` and `mcp.package_reference` display the exact replacement reference. The same review requires an explicit configuration-change/client-reload approval, sent as `special` for that one reference migration. Script approval does not preapprove it, and changing the reference clears approval. A `configured` result is configuration success, not a server installation claim.
- `binding.resolved_target.application_update` displays the verified app candidate and developer identity.
- `item.plugin` admits registered plugin-cache Skills to preview without marking them as update-available or active in a session. `plugin_update` shows the whole plugin, included Skills, exact commit and explicit cached/remote provenance. `reload_required` gives next-session or `/reload-plugins` guidance without automatically reloading a client.
- `runtime_review` displays current/target versions, compatibility classification, consumers and unknown impact. Listed consumers are not authorized update targets.
- `marketplace_refresh` reports attempted command, known/unknown source state and before/after commits independently of plugin installation. An unknown state is never presented as unchanged.
- `special prepare --id ID [--package PATH]` supplies read-only package/system candidates. The UI selects one backend-discovered request, writes it to a temporary private file for ordinary `plan --privileged-request`, and verifies the returned selection and ready-item request match it. `prepared` does not mean executable.
- Settings exposes `special status` and explicit `special register-helper`; the native bridge owns the separate registration confirmation. Missing signature/helper prerequisites remain blocked. Demo mode never invokes these commands.
- `privileged_operation_id`, backup and recovery paths are retained. `special operation-status ID` must match the exact operation before a waiting-restart record can open a separate restart preview, bound to one current system inventory ID. Both `privileged_install` and `restart_system` remain explicit choices in the new plan; installation never triggers restart automatically. Privileged failures use recovery lookup instead of the ordinary retry shortcut.

The permission UI does not collect passwords or bypass backend availability checks. Helper registration requires its own explicit user action and native confirmation. Signing and real device/VM validation remain separate requirements. Older CLIs reject unsupported flags; their error is retained instead of being shown as a successful update.

## Verification

Run `swift test --package-path desktop --scratch-path /path/to/isolated-scratch`. Tests cover catalog parity, source keys, format arguments, language fallback, persistence, live notice changes, explicit permission checks and exact plan-byte forwarding through an isolated CLI fixture.

For UI checks, launch a separately built app with `--demo`. Verify both languages, Settings, menus, filters, details, previews, results, diagnostics and persistence after reopening. Demo operations never call the real CLI. Do not use actual update or uninstall actions to validate localization.
