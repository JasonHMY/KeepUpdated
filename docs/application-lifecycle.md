# Application exit and reopen

Supported Homebrew cask updates can request normal exit of explicitly selected applications, then optionally reopen them after the update is verified. This uses the existing plan/apply engine and Homebrew runner. App-only cask source, candidate, ownership and installer-effect checks still apply. It does not add an independent app updater or privileged execution.

## CLI and JSON

Create a preview with the exact cask ID and each application ID from the inventory:

```sh
keepupdated plan --action update --id CASK_ID \
  --quit-app APPLICATION_ID --reopen-app APPLICATION_ID > plan.json
keepupdated apply --plan plan.json --confirm EXACT_PLAN_ID
```

Both application flags are repeatable. `--reopen-app` must also appear in `--quit-app`. Exit choices require explicit installation IDs and cannot be combined with `--all` or uninstall. Ordinary previews keep the existing closed-application guard. `--special` does not authorize application exit.

The existing schema IDs remain unchanged. New optional fields are:

| Location | Field | Meaning |
| --- | --- | --- |
| `selection` | `quit_apps`, `reopen_apps` | Exact application inventory IDs; included in the plan hash |
| `items[]` | `application_lifecycle` | Approved bundle, content and running-instance bindings |
| Application `resolved_target` | `application_state` | Independent closed or running-instance evidence |
| Application `resolved_target` | `application_lifecycle` | Read-only lifecycle proof, composable with an update candidate |
| `results[]` | `application_lifecycle` | Per-application exit and reopen outcomes |

An abbreviated lifecycle target looks like this. Paths and values are synthetic:

```json
{
  "id": "APPLICATION_ID",
  "path": "/Applications/Example.app",
  "identity": "com.example.Example",
  "version": "1.0",
  "real_path": "/Applications/Example.app",
  "fingerprint": "CONTENT_DIGEST",
  "instances": [{
    "pid": 1234,
    "launched_at": "1700000000.125",
    "executable": "/Applications/Example.app/Contents/MacOS/Example",
    "path": "/Applications/Example.app",
    "identity": "com.example.Example"
  }],
  "reopen": true
}
```

A ready preview can retain `requires_quit: true`. The command builder still refuses that item until the engine obtains and compares a fresh closed-application plan after normal exit. `requires_restart` continues to mean an operating-system restart.

## Execution and outcomes

The engine verifies the complete approved plan before requesting any exit. It checks bundle contents and each exact PID, launch timestamp, executable, bundle path and identity. A fixed AppKit bridge calls `NSRunningApplication.terminate`; it never uses an application name to choose a process or requests force termination. The bridge is embedded in the CLI and runs through the existing Runner using macOS `osascript`; no helper installation or CGO dependency is required.

A successful request is recorded as `requested`, not `exited`. The engine waits up to 30 seconds for the approved instances and all processes inside the bundle to disappear. Applications may refuse, ask about unsaved documents, or delay exit. If macOS accepts the request but the app stays open, the result is `application_quit_timed_out_or_unsaved_work`; the backend does not claim to know which dialog or refusal caused it. Cancellation stops further requests and updating. An already delivered exit request cannot be withdrawn.

After exit, the engine checks the installation, source, manager and exact update candidate again. Only the approved activity transition may differ. Changed contents, a moved bundle, a replacement PID/launch, a remaining helper or a restarted application blocks updating. The final check does not pursue or terminate new processes. These checks observe state immediately before execution; they cannot prevent another actor launching an app after the last check. Homebrew retains `--no-quit`.

Reopen runs only when the manager command succeeds and the installed result is verified. It rereads the exact bundle identity/version and launches its path with a new instance so Launch Services cannot activate another copy with the same bundle ID. The running path and identity are checked afterward. A successful launch command without an observable matching instance remains `verification_unknown`.

Exit and reopen have separate status/reason fields. Reopen failure leaves the installation result intact, and the CLI returns nonzero for a requested reopen that failed, was skipped or could not be verified. If updating fails or a later preparation step stops, applications already exited remain closed; the result reports this without automatically relaunching an unverified installation.

## Provider integration

`ResolveApplicationLifecycle`, `QuitApplications`, `VerifyApplicationsClosed` and `ReopenApplication` provide the shared backend. Providers must retain their own source/candidate, permissions, replacement, recovery and installed-result checks. Only invoke `ReopenApplication` after both a verified provider update and an `exited` outcome for an approved target.

Use `MergeApplicationTarget(candidate, proof)` to combine update resolution with lifecycle evidence for the same scope. It preserves candidate version, source, provider and reason, adding `application_state` and `application_lifecycle`. Mismatched scopes discard the lifecycle proof. `ResolveApplicationTargets` also preserves candidates already supplied in `Options.ResolvedTargets`. A missing lifecycle proof never establishes that an application is closed.

The cask integration narrows per-item IDs by copying the original Selection, preserving additional selection/consent fields. Desktop presentation and other app providers can consume the optional fields without changing schema IDs. Application paths that need another owner's permission remain manual with `requires_admin`; this feature does not request elevation.

## Verification boundary

Controlled-command tests cover consent, stale paths/content/instances, normal exit, refusal, cancellation, helper processes, restart races, changed post-exit candidates, update failure and independent reopen failure. Darwin tests compile a temporary AppKit `.app`, exercise real termination callbacks, refuse exit with an edited `NSDocument`, delay termination, cancel execution, reopen, and protect another same-name/same-ID app at a different path. One complete Apply test combines that real application with a controlled cask manager and post-update verification.

The tests do not upgrade installed Homebrew software or quit the user's applications. The cask manager in the integration test is controlled; this is not a claim of live Homebrew upgrade acceptance. Third-party save dialogs, every macOS release, signed/notarized distribution, and the shared desktop UI require their own acceptance. App bundles with external symlinks, files above the existing 256 MiB per-file hash limit, more than 100,000 entries or more than 8 GiB of metadata-counted content remain ineligible for automatic exit rather than receiving incomplete fingerprints.
