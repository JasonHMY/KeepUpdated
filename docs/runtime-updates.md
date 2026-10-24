# Shared runtime and native CLI updates

Runtime updates use a separate, single-item preview. Ordinary batch updates keep
protected runtimes manual, including when `--all` is combined with consent flags.

```sh
keepupdated plan --action update --id INVENTORY_ID \
  --consent runtime_update --special > runtime-plan.json
keepupdated apply --plan runtime-plan.json --confirm EXACT_PLAN_ID
```

Review the owner, current and target versions, affected paths, known consumers and
unknown impact before confirming the exact plan ID. `runtime_update` acknowledges
the shared runtime change. `--special` separately acknowledges installation scripts
or package-manager hooks. Neither option removes pins or permits a source change.
Reports and plans contain local paths; keep them outside Git.

## Supported paths

| Installation | Update path | Boundaries |
| --- | --- | --- |
| Official standalone uv in the default user executable directory | Exact `uv self update VERSION`; verify the official release checksum and installed binary hashes | Requires the cargo-dist receipt and both `uv` and `uvx`; the selected installation must be active on PATH. |
| Homebrew core runtime/tool formulae: `node`, `python`, `python3`, `go`, `openjdk`, `bun`, `uv`, `pipx`, `npm`, `pnpm` | Existing Homebrew resolver and executor, with a separate runtime review | Requires a linked active keg selected on PATH, verified manager prefix and reverse dependency query; uses the current owner candidate. |
| npm, pnpm, Bun global packages; pipx and uv tools | Existing package-manager providers | This change does not turn project Python dependencies into independent global tools. |
| Versioned or pinned formulae, other native CLIs, other runtime managers, shared libraries such as OpenSSL and SQLite | Manual | No pin removal, source migration, elevation or runtime uninstall is added. |

Major version changes, and minor version changes before version 1.0, remain manual.
Unknown receipts, custom uv installation prefixes, receipt source overrides and
modified standalone uv binaries also remain manual. The standalone adapter accepts
a narrow receipt schema; unfamiliar fields fail closed instead of losing a possible
constraint. It does not infer a pin policy from a project lockfile.

## What the preview verifies

Homebrew resolves the configured prefix and installed reverse dependencies before
planning. Ordinary formula and cask updates cannot indirectly upgrade a protected
runtime. Each protected dependency must have an owner-verified, non-outdated
installed version matching the active inventory version. Dependencies that may
change, are missing, or cannot be verified require separate handling followed by
a fresh parent preview; adding consent to the parent does not bypass this guard.
If a protected dependency changes unexpectedly after execution, the result stays
unverified even when the parent reached its requested version. The preview includes consumers found in inventory and names returned by
Homebrew that inventory could not fully describe. The active keg's file contents,
links and modes are fingerprinted; changes after preview invalidate the plan.
Existing local-modification evidence blocks planning. Homebrew receipts alone do
not establish that every installed file still matches an upstream bottle.

Standalone uv reads its receipt without executing a local binary during scan or
preview. The resolver downloads the official current and target release archives,
checks their published SHA-256 values, and compares both installed executables
with the current official release. Updates never inherit alternate download hosts,
tokens or project configuration. Shell profile changes are disabled.

Both paths rebuild the exact plan before mutation. Consumer, installation, owner,
source or candidate drift requires a new preview. Consumers are impact information,
not permission to upgrade them. Existing Homebrew dependency paths remain visible;
automatic dependent rebuilds, autoremove and installation cleanup remain disabled.
Unobserved applications and project scripts may still depend on a runtime. The
preview does not claim a complete dependency graph or compatibility guarantee.

## Results and recovery

The native uv path backs up `uv`, `uvx` and the receipt before execution, checking
both copied bytes and persisted backups against the approved hashes. Success
requires the exact target version, source and official binary hashes after the
command. A normal command failure restores the original files only when all
current files still match the known original or candidate contents. Unexpected
content, cancellation or an incomplete recovery remains a failure requiring review;
the backup path stays in the private result. Recovery does not overwrite unknown
files. Recovery captures each destination separately and retains the displaced
object, then links the restored file only if the destination is still absent.
Concurrent edits stop recovery; the original backup and `recovery.json` identify
the retained objects. Homebrew retains its existing old-keg/no-cleanup behavior; this adapter does
not provide a Homebrew downgrade transaction.

## Validation

Controlled tests cover separate consent, explicit single-item selection, ordinary
batch exclusion, major transitions, pins/unsupported receipt fields, source and
prefix changes, consumer drift, content drift, checksum rejection, exact commands,
post-update verification, indirect protected-runtime upgrades, unchanged-runtime
dependencies, backup-byte tampering and concurrent edits during native recovery.

Standalone uv has a narrow isolated official-binary path. Real Homebrew mutation, Intel execution and complete consumer compatibility remain unverified. A version/hash check does not establish every dependent app works after a runtime change.

Owner protocols: [uv installation and updates](https://docs.astral.sh/uv/getting-started/installation/),
[uv installer options](https://docs.astral.sh/uv/reference/installer/), and
[Homebrew commands](https://docs.brew.sh/Manpage).
