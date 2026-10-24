# Python tool updates

KeepUpdated updates or removes one direct pipx/uv tool through its original
manager. It binds the manager executable and version, original receipt, actual
interpreter, environment configuration, and exposed commands before execution.
Project dependencies, ordinary environments and PEP 668 packages are outside
this provider.

## Supported receipts

These are exact tested combinations, not minimum versions:

| Manager | Receipt | Supported installation |
| --- | --- | --- |
| pipx 1.8.0 | `pipx_metadata_version = 0.5` | pip backend, one direct package with optional extras |
| pipx 1.18.4 | `pipx_metadata_version = 0.12` | recorded `pip` backend, one named environment matching the package |
| uv 0.13.1 | generated `uv-receipt.toml` | one registry requirement with optional extras and Python selector |

Main requirements may be unconstrained or use numeric conjunctions of `<`, `<=`,
`>`, `>=` and `!=`. An exact `==` requirement or pipx pin stays manual. The native
outdated query supplies a candidate, which must satisfy the original range.
An excluded latest candidate is reported as outside the range; this does not
claim there is no older compatible update. There is no general Python solver,
marker evaluation or rewrite of the user's requirements.

Unknown manager versions, schemas, backends and fields remain manual. So do
injected packages, multiple requirements, separate constraints/overrides,
lock files, extra resource exposure, custom pip arguments and direct URLs.
Resolved versions inside a lock are not treated as user pins: the unsupported
lock has its own result. Receipt-specific and configured uv indexes remain manual
for updates. Configured install-only constraints and non-default solving settings are also rejected before
the candidate query. The uv configuration guard accepts only bare keys and table
paths with single-line scalar values; quoted/dotted keys, inline tables and other
unhandled TOML forms stay manual. This prevents pip-only settings from selecting
a source that `uv tool upgrade` would not use. A recognized receipt source setting
or main requirement pin does not prevent a verified native uninstall. Other unsupported receipt semantics still require manual removal.

pipx source settings are supported in `[global]`. Command-specific and `DEFAULT`
source overrides remain manual, including `index-url`, `extra-index-url`,
`find-links`, `no-index` and their underscore spellings. Global source settings,
including multiline values, are retained. Apply rechecks this policy, so a saved
preview created by the former file-hash-only resolver cannot dispatch an update
with unverified command-specific sources.

The interpreter path and bytes, `pyvenv.cfg`, receipt intent, manager bytes,
exposed links and script contents must still match at apply. Requested extras
must remain declared by the installed distribution. Every exposed
command must belong to the main distribution's console scripts and wheel RECORD.
For pipx 1.18.4 only, the known native `-E` shebang addition is reversed in memory
when checking the RECORD hash. The rest of the launcher must match exactly.
Existing MCP ownership checks remain independent and are not weakened by this
exception.

## Execution and readback

pipx receives an exact temporary constraint through its environment. The original
requirement and extras remain in its receipt. uv receives an exact transient
upgrade requirement and `--no-build`. Both keep the bound manager, environment
and configured source; neither switches to raw pip for execution.

uv 0.13.1 persists `--no-build` in its receipt. KeepUpdated first saves the original
receipt privately. After a successful command, it restores the original receipt
only if that temporary option is the sole semantic difference, the interpreter
still matches, and the file has not changed during restoration. Unexpected native
or concurrent changes are retained for review; they are not overwritten. This
receipt backup is not a backup or rollback of the whole Python environment.

A successful update requires the exact target version in one matching dist-info
record inside the original environment, preserved receipt intent and verified
exposed commands. A dependency-only change, ignored target, successful no-op,
ambiguous record or changed interpreter cannot report `updated`. Uninstall also
requires removal of the environment and all bound exposed entries; inventory
omission or dangling command links do not prove removal.

Python policy fields are additive in plan v1. Saved plans without them require a
new preview. Observation times and installed package versions are not included
in the stable intent fingerprint. The desktop continues executing the original
saved plan bytes.

## Validation limits

Supported combinations above are exact versions and receipt layouts. Controlled tests and isolated native-manager samples do not establish every Python installation or client reload. Fixtures cover policy rejection, manager/interpreter/source drift, command ownership, wrong targets, receipt restoration and uninstall readback. Custom-index uv updates remain manual. No MCP server startup is part of verification.

The schema and command behavior were checked against upstream
[pipx 1.8.0 metadata](https://github.com/pypa/pipx/blob/1.8.0/src/pipx/pipx_metadata_file.py),
[pipx 1.18.4 metadata](https://github.com/pypa/pipx/blob/1.18.4/src/pipx/pipx_metadata_file.py),
[pipx 1.18.4 launcher exposure](https://github.com/pypa/pipx/blob/1.18.4/src/pipx/commands/common.py),
[uv 0.13.1 tool receipts](https://github.com/astral-sh/uv/blob/0.13.1/crates/uv-tool/src/tool.rs)
[uv 0.13.1 upgrade](https://github.com/astral-sh/uv/blob/0.13.1/crates/uv-tool-commands/src/upgrade.rs),
[uv pip configuration scope](https://docs.astral.sh/uv/concepts/configuration-files/#configuring-the-pip-interface)
and [pip command sections](https://pip.pypa.io/en/stable/topics/configuration/#per-command-section).
