# Operation progress

`scan --progress`, `check --progress`, `plan --progress` and `apply --progress` emit newline-delimited JSON events on
stderr. Stdout remains the existing report (JSON by default, or the requested
table). Calls without `--progress` keep their existing output. `check` performs
one fresh scan followed by dependency, Skill-source and update checks.

```json
{"schema":"keepupdated/progress/v1","sequence":12,"phase":"updates","completed":3,"total":8,"current":"Example","state":"running","unresolved":1}
```

`completed` counts processed work in the current phase, including work with
unresolved results. It is not a successful-update count. `total` is the phase's
work count, or null when unknown. `current` is the most recently started work
label; concurrent update workers can have other requests in flight. It is not a
path or command to execute. Labels are limited to 256 Unicode code points, with
control characters removed. Events and reports can still identify installed
software and repositories; keep them private.

| Phase | Unit | Current label | Unresolved unit |
| --- | --- | --- | --- |
| `scan` | Completed discovery adapter, including process inspection | Stable adapter code | Adapter added diagnostics |
| `applications` | Application bundle metadata read | Application name | Metadata health is not `ok` |
| `dependencies` | Eligible package inspected | Package name | Health result is not `ok` |
| `skill-sources` | Repository/ref group inspected | Repository name | Offline or any group member lacks a `current`/`available` update result |
| `updates` | Item with an update lookup processed | Item name | Update result is not `current`/`available` |
| `resolve` | Owner-source group or distinct package installation resolved | Current source/item name | Group contains an unresolved target |
| `plan` | Selected inventory item planned | Item name | Item requires manual handling or is blocked |
| `apply` | Approved plan item processed | Item name | Result is not verified success or an already-current/duplicate skip |
| `verify` | Fresh verification snapshot; total unknown | Approved plan or item name | Snapshot unavailable |

Other installations, including unsupported sources without a lookup, remain in
the report and do not inflate the update-query total. A zero total means no work
in that phase; clients should show an indeterminate indicator rather than divide
by zero. Scan adapter codes are `processes`, `apps`, `homebrew`, `packages`,
`executables`, `runtimes`, `configurations`, `skills`, `skill-origins`,
`skill-transactions`, `system`, `services` and `relationships`.

`state` is `running`, `finished`, `cancelled` or `failed`. An interrupted phase with a known incomplete total is `failed` unless cancelled. `finished` means the phase's work
was processed, not that all checks succeeded. Cancellation stops counting
unfinished work. There is no overall success/100% event; the final report and
exit code remain authoritative, including unknown, unsupported and failed
results. A process killed before it can handle cancellation may have no terminal
event. Clients must not infer completion from EOF.

`sequence` is assigned at the serialized stderr writer and increases for every emitted event. It is optional for compatibility with older scan/check output. New consumers use it to accept nested application discovery and repeated scan/resolve/plan stages during apply. Without it, the desktop retains legacy phase-order checks. Each new CLI command starts a separate stream; sequence numbers do not authorize events from an earlier command.

The first event is emitted before phase work. Frequent intermediate events are
coalesced to at most one per 50 ms, apart from phase starts and the first work
label. Terminal events are always emitted. Telemetry write failures do not alter
the report. Consumers should drain stdout and stderr concurrently, accept split
UTF-8 sequences, ignore unknown/malformed progress records and retain ordinary
stderr diagnostics. The desktop bounds progress lines to 8 KiB, diagnostics to
64 KiB and report output to 64 MiB; oversized reports fail rather than decode a
partial result. A new run has a fresh parser and operation identity.
