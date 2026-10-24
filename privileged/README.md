# Privileged updates

The ordinary `plan` / `apply` flow calls one native authorization boundary for package, system and App Store operations. It never passes shell text, arbitrary root arguments, environment variables or passwords. The regular CLI continues to run as the user.

The current ad-hoc candidate **cannot execute privileged updates**. It contains the bridge and helper, but both require a valid Developer ID signature, hardened runtime and system-approved helper registration. No helper is registered by a build, test, scan or check. Signed-device acceptance remains outstanding.

## Consumer contract

Go providers use `internal/privileged.Client`: `Check(ctx, Request)` is read-only; `Execute(ctx, Request, Confirmation(request))` invokes the native confirmation and authorization flow. Production uses `NativeClient`; fixtures can inject a client through `manage.Options.PrivilegedClient`. A provider attaches its verified `PrivilegedRequest` to its `PlanItem`; the existing apply engine rechecks the whole plan and current item before dispatching it. The ordinary command runner cannot execute this request.

`Selection.consents` and `PlanItem.required_consents` accept only `runtime_update`, `privileged_install` and `restart_system`. Values are sorted and deduplicated; unknown values fail. `special=true` retains its previous meaning. Privileged actions require one explicit inventory ID and never enter an all-items batch.

Read-only preparation supplies candidates for the desktop picker without asking the user to enter hashes, receipt IDs or technical JSON:

```sh
keepupdated special prepare --id EXACT_APP_ID --package /absolute/selected.pkg
keepupdated special prepare --id EXACT_SYSTEM_ID
```

The response contains `targetID`, `status`, `reason`, `candidates` and `executionBlocker`. `prepared` means the candidate metadata was verified; it does not authorize execution. The desktop selects one request from `candidates` and passes it to the existing plan flow. The response envelope itself is not a plan. Preparation refreshes the inventory ID and uses the same component-package and system-list parsers as execution. It never downloads an installer, registers a helper or executes an update. A sealed ad-hoc App can prepare candidates while reporting the signature/helper or Apple Silicon execution blocker. The standalone CLI archive has no matching native bridge and remains blocked.

For an explicit prepared package or system target:

```sh
keepupdated plan --action update --id EXACT_ID \
  --privileged-request request.json --consent privileged_install > plan.json
keepupdated apply --plan plan.json --confirm EXACT_PLAN_ID
```

System requests also require `--special` to approve their declared installation scripts; `privileged_install` does not substitute for this choice.

A restart uses a separate request and adds `--consent restart_system`. It references an earlier `privileged_operation_id` with an `awaiting_restart` result. Installation commands never include a restart flag. The client opens the native macOS restart dialog, where the user can cancel; the helper verifies the installed state after a different boot.

The bridge carries the Apple Events entitlement and the app declares its restart-dialog purpose. The only emitted event opens the system restart dialog. TCC denial remains a visible pending-restart result; no fallback forces a reboot. [Apple entitlement requirements](https://developer.apple.com/documentation/BundleResources/Entitlements/com.apple.security.automation.apple-events), [required usage description](https://developer.apple.com/documentation/bundleresources/information-property-list/nsappleeventsusagedescription).

Service and recovery operations are separate:

```sh
keepupdated special status
keepupdated special register-helper
keepupdated special operation-status OPERATION_ID
```

Registration displays a specific service confirmation before calling `SMAppService`. Registration is not installation consent. Each installation also displays its exact target and effects, then requests a short-lived administrator authorization right tied to that operation's digest. [Apple SMAppService documentation](https://developer.apple.com/documentation/servicemanagement/smappservice)

## Typed targets

| Kind | Bound fields | Current scope |
| --- | --- | --- |
| `package` | `installationPath`, `bundleID`, installed Info/CodeResources SHA-256 values, package `path`, `sha256`, `teamID`, `receiptID`, `fromVersion`, `version`, `source=local-signed-package` | A higher-version, script-free component package for an existing signed app and matching receipt. The package's bundle metadata must map to that exact app; distribution packages and script-bearing packages are rejected. |
| `system` | Exact Apple `label`, `version`, `build`, `fromVersion`, `fromBuild` | An exact macOS update from `softwareupdate --list`, with independent OS version/build readback. Apple Silicon installation is blocked because this boundary does not implement volume-owner authorization. |
| `app_store_update` | `appStore`: `adamID`, `bundleID`, canonical `/Applications/…app` path, receipt/Info/CodeResources SHA-256 values, current/expected versions, region, manager digest and `managerVersion=7.0.0` | Only a sealed, same-team-signed bundled `Contents/Library/Tools/mas` with system-only dylib dependencies. Packaging may contain pinned inputs, but current App Store policy remains manual before manager or authorization queries; absence reports `app_store_manager_not_bundled`. |
| `restart` | `restartOf` | A prior operation owned by the same user, awaiting restart in this boot. No forced reboot or automatic application termination. |

App Store execution constructs only `mas update --inaccurate --check-min-os ADAM_ID`. The helper derives the user context from the authenticated XPC peer. It rechecks the app signature, metadata hashes, precise Spotlight identity, store region, catalog version, OS compatibility and running processes. The manager cannot pin an older store version; changed catalog results are blocked before execution, and any unexpected resulting version remains unverified. No sign-in credentials are collected.

The package source label means an explicitly chosen, locally verified signed artifact. It does not claim that a URL or an upstream release feed was verified. Providers supplying downloaded artifacts must retain their own download/source checks before constructing this request.

Apple Silicon OS installation requires volume ownership. Administrator Authorization Services rights alone do not provide that capability. The code does not request passwords, bootstrap tokens, new device management enrollment or relaxed system protections. [Apple volume-ownership documentation](https://support.apple.com/en-au/guide/deployment/dep24dbdcf9e/web)

## Execution and recovery

The signed bridge and helper authenticate each other with XPC code-signing requirements. The helper stages bytes from one open file descriptor into root-owned private storage and verifies the copied hash and signature. It installs the staged path, preserving the approved artifact if the original path changes. The helper serializes work, binds authorization to the exact prepared request and connection, and writes an exclusive durable attempt record before starting a mutation. Before invoking the bridge, the Go caller binds `Request.operationID` to its already durable outer plan ID. This known key names the helper journal and is returned even if the native response is missing. `keepupdated special operation-status ORIGINAL_PLAN_ID` therefore remains available after a killed caller or lost final reply. Authorization itself still binds the full native plan digest. A lost reply must be recovered through status, not an automatic retry.

Cancellation and XPC disconnect set a thread-safe flag immediately, outside the work queue. A cancellation during backup is observed before the atomic transition into installation. After that transition the installer is allowed to finish and its actual result is read back. Each XPC connection prepares one operation.

Package and App Store operations retain the prior app and relevant receipt/metadata in private helper storage. The initial durable attempt record includes `backupPath` and `recoveryPath` before mutation, so a helper crash that leaves no final result still exposes the recovery location. An empty, truncated or mismatched final record falls back to that valid attempted record as `verification_unknown`; status never retries the installation or rewrites the damaged evidence. This is recovery evidence, not a promise that restoring an app reverses system files, services or store state. System updates have no automatic full-system backup or rollback. Cancellation is supported before execution; disconnecting during an installation does not kill the native installer or erase its recovery record.

Results distinguish `blocked`, `cancelled`, `failed`, `verification_unknown`, `installed` and `awaiting_restart`. The Go adapter maps a verified `installed` result to the existing `updated` status. Exit zero from an installer is insufficient; package receipt and app identity/version, or system version/build, must be read back. Waiting for restart remains separate from completion.

The native confirmations include English and Simplified Chinese copy and follow the desktop’s `com.keepupdated.desktop` / `interfaceLanguage` choice (`system`, `en`, `zh-Hans`). Unsupported preferences or primary system languages fall back to English. macOS owns the language of its administrator dialog. The shared desktop UI owns presentation of `required_consents`, `privileged_request` and recovery status; it must preserve the original plan bytes.

## Verification boundary

```sh
swift test --package-path privileged --scratch-path /private/tmp/keepupdated-privileged-tests
```

Tests use temporary files, controlled commands and injected authorization. An additional real `pkgbuild`/`xar` roundtrip checks static component metadata and rejects its unsigned artifact; it never installs that package. They cover exact confirmation, target/content drift, wrong signatures and metadata, cancellation, connection ownership, replay, durable results, failed/unverified installs and restart readback. They do not register a helper, invoke an administrator prompt, install a package, update the OS, update an App Store app or restart a Mac.

The existing CI runs these tests and packages the actual bridge/helper. The packaging check verifies nested signatures and confirms that the extracted ad-hoc bridge reports `developer_signature_required`. It does not enable the service.

Still required before release: same-team Developer ID signing and notarization, SMAppService registration/revocation on supported macOS versions, XPC peer rejection and native authorization cancellation on a signed test device, a reviewed sealed mas distribution, account-backed App Store acceptance, and real package/system/restart recovery in a disposable VM. These requirements are not cleared by packaging or source tests.
