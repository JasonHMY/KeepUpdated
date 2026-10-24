# Native application ZIP and DMG updates

The `native-app` provider can update a closed, user-owned macOS application from its signed, embedded Sparkle source. It uses the existing `plan` and exact `apply --confirm` flow. Discovering a Sparkle, Electron or App Store version alone does not enable installation.

## Supported path

The installed bundle must contain a public HTTPS `SUFeedURL` and an Ed25519 `SUPublicEDKey`, pass strict Developer ID signature verification, and have a writable destination parent. The appcast must offer one unambiguous full ZIP or DMG for the compatible default channel. The archive must contain one top-level `.app` and carry a valid signature from the installed key.

The preview binds the original path and complete content hash, bundle ID, developer Team, installed version, exact target version/build, feed URL, archive format/URL/length/signature, host architecture and macOS version. Apply rebuilds that preview before execution. After downloading, it rechecks the source, original content, preferences and running processes before replacement.

Apply verifies the archive before extraction, then validates the staged bundle's version/build, source, architecture, minimum OS, Developer ID identity and Gatekeeper assessment. It uses macOS `renameatx_np(RENAME_SWAP)` to exchange directories atomically. There is no fallback to a sequence that leaves the application path absent. The installed bundle is checked again and the inventory must match its exact build; a build-only update counts as a change.

For a DMG, the complete image bytes pass length and Ed25519 checks before a private, read-only, non-browsable mount. The adapter parses the native mount plist and binds one volume to the private mountpoint and image device. It accepts one root app, an exact `/Applications` link, and known Finder/background decoration. Additional apps, installer packages, unknown payloads and unsafe app-tree entries are rejected. Files and contained relative links are copied individually with metadata preserved, then the mounted app and staged copy are independently verified and their content hashes compared. The image must detach successfully before the existing replacement transaction starts.

Successful updates retain the signed original beside the installation in a private `.KeepUpdated-native-*` directory. This reserved directory is excluded from application discovery. Its `RECOVERY.txt` identifies both locations and content hashes. Keep these local records private.

## Failure and interruption behavior

Before the exchange, a download, signature, compatibility, source or process failure leaves the original installed. After exchange, a failed verification swaps the original back when the installed candidate still has its expected content. The failed candidate is retained. If concurrent content changes or a rollback failure prevent safe restoration, both paths are retained and the result explicitly reports that the original is preserved for recovery.

An unverified mount or failed detach stops the update before exchange. The private container and `DMG-CLEANUP.json` are retained, with the device, mountpoint, image and operation/cleanup reasons; the raw attach response is kept when available. The result's `backup_path` points to this evidence container. Do not remove it while its mount state is unresolved. An explicit cleanup failure never means the app was updated.

`backup_path` holds the original after success. After a result ending in `_original_restored`, the original is back at its installation path and `backup_path` holds the retained failed candidate. Do not infer which content a location contains from its filename alone; the recovery record includes both hashes.

Process interruption immediately before or after the atomic exchange leaves a complete application at the original path and the other tree at the exchange location. This is not a power-loss durability guarantee or an automatic crash-recovery service. A later plan does not delete old recovery material.

## Boundaries

- Running apps use the existing explicit exit/reopen review and exact-instance lifecycle checks. Failed exit or a new process blocks replacement; reopening is separately verified after the update.
- Only UDIF DMGs with one recognized HFS+/HFSX or APFS app volume are supported. PKG, delta archives, App Store packages, vendor-specific installers and arbitrary download links remain outside this adapter.
- Source/key rotation, signed-feed-only policies, informational/major/phased releases, custom channel/skip preferences, and explicit local-change/pin evidence require the vendor or separate review.
- An archive is limited to 512 MiB compressed, 4 GiB extracted, 100,000 entries and 2 GiB per entry. Unsafe paths, special files, duplicate case-insensitive paths, escaping links and link-parent conflicts are rejected. Contained framework symlinks are preserved. ZIP extraction does not restore resource-fork metadata. DMG file copying preserves native metadata and counts data and resource forks against the copy limits; both routes must pass signature verification before replacement.
- The adapter runs no installer scripts, forced exit, elevation, reboot, global tool installation or background service. Only explicitly approved lifecycle reopening may launch the updated app.
- Intel compiles with CGO disabled but has not been accepted on an Intel Mac. Filesystems without atomic directory exchange fail safely.

## Validation limits

Tests cover authenticated synthetic archives, source/content drift, signatures, payload identity, unsafe trees, native mounts, exchange, recovery and controlled failures. Isolated official ZIP/DMG samples do not certify arbitrary Sparkle apps or the customer-download experience. Fixtures with controlled Developer ID/Gatekeeper responses are not actual Developer ID authority. Process-interruption tests do not establish power-loss durability.

## Integration contract

The optional `binding.resolved_target.application_update` object contains:

| Field | Meaning |
| --- | --- |
| `archive` | Public feed/archive URLs, public key, signature, length, exact version/build; `format: "dmg"` for DMG, omitted for legacy ZIP |
| `bundle_id`, `team_id` | Authenticated installed application identity |
| `installed_sha256` | Full original tree fingerprint |
| `architecture`, `macos` | Compatibility context |

Merge this field with `application_lifecycle` on the same resolved target; do not replace one provider's evidence with the other's. Preserve whole-plan equality and explicit application exit/reopen consent during lifecycle integration. `requires_restart` remains reserved for a system restart.

Ready reasons are `signed_application_zip_ready` and `signed_application_dmg_ready`; verified success remains `signed_application_bundle_verified`. Empty/omitted `format` retains the original ZIP JSON and hash. Only `dmg` adds an explicit format field; missing DMG format, unknown formats and mismatched suffix/content require a fresh preview. Format participates in the existing source and plan hashes and fresh comparison. Public URLs and keys are safe to display, but full plans still contain private installation paths. Keep the original JSON for apply.

Protocol references: [Sparkle publishing](https://sparkle-project.org/documentation/publishing/), [Sparkle configuration](https://sparkle-project.org/documentation/customization/), [Apple code signing](https://developer.apple.com/library/archive/technotes/tn2206/_index.html).
