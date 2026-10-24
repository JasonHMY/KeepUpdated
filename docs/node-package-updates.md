# Node global package updates

A Node update preview separates the latest registry candidate from the exact
version allowed by the original request. For example, `^1.0.0` can select `1.1.0`
while `2.0.0` remains an informational latest candidate. An exact declaration of
`1.0.0` stays fixed. The public scan's `channel: latest` does not establish user
intent.

## Verified capabilities

| Manager | Update capability | Selector preservation |
| --- | --- | --- |
| npm 11.19.1 | Global `PREFIX/lib/node_modules`, verified native root/direct row and registry | `install --global --prefix PREFIX --no-save -- NAME@VERSION` |
| pnpm 10.18.3 | Global layout 5, isolated linker, matching `.modules.yaml`; virtual store at `.pnpm` or `node_modules/.pnpm` | `update --global=false --dir SCOPE --no-save --depth=0 -- NAME@VERSION`, with the original bin and virtual store explicitly bound |
| Bun 1.4.3 | Original global directory, direct manifest entry, installed metadata and native global-bin query | `add --global --no-save -- NAME@VERSION` in the owning global directory |

These are exact verified manager versions, not open-ended major-version ranges.
Other versions, pnpm isolated-global groups and unsupported layouts stay manual.
This does not add a runtime updater or migrate installations.

The table omits existing script-control and source options for readability. The
plan still binds the manager executable/hash, version/layout, original scope/bin,
configuration digest, selector source and manifest digest. On macOS the pnpm
configuration digest also covers its independent native
`$HOME/Library/Preferences/pnpm/rc`, including the file's absence. The clean manager
environment does not pass `XDG_CONFIG_HOME`; native rc credentials stay in the
file and only contribute to its digest. Query and mutation use
the same manager environment. pnpm metadata queries use the same directory options
as the update; native global root/list queries independently verify ownership.
An unknown or mismatched bin destination prevents an update preview.

pnpm 10.18.3 [forces `save = true` in global mode](https://github.com/pnpm/pnpm/blob/v10.18.3/config/config/src/index.ts),
even with `--no-save`. KeepUpdated therefore addresses the existing global
manifest as a directory, disables workspace discovery and pnpmfile loading, and
supplies the verified bin, isolated linker and existing virtual-store location.
It disables implicit package-manager downloads. Queries and mutations share
`--config.use-node-version=null`: pnpm's typed null disables implicit runtime
provisioning while retaining the running Node version for engine checks. A
configured `use-node-version`, including one in the native rc, still keeps the
update manual. Creating, changing or deleting that rc invalidates an earlier
preview, including changes to store or resolution settings. This retains the exact
manifest bytes and refreshes the existing global command entry. It does not use broad
update-all, rewrite the manifest itself or repair it after the command.

Bun's [`--no-save`](https://bun.sh/docs/pm/cli/add) also leaves the existing lockfile
untouched. A later independent native install still follows the original
manifest/lockfile; KeepUpdated does not claim that those lockfiles now record the
transient exact update. Installed files and command entries are checked separately.

## Version request evidence

The supported request subset is empty/unconstrained, `*`, `latest`, a stable
three-component numeric exact version, or a single `^`/`~` range with such a
version. The original manager resolves that selector read-only. Its result must
also pass the narrow numeric bounds check. Arrays of matching npm/pnpm results
select the highest permitted stable version. An unavailable permitted query never
falls back to latest; a separately unavailable latest hint does not block a valid
permitted result.

pnpm and Bun require exactly one direct entry in the owning global manifest.
Duplicate keys, conflicting dependency sections, alternate source syntax, aliases,
complex ranges, prereleases and ambiguous tags stay manual. Manifest settings
that can change the manager, runtime, workspace or resolution policy also stay
manual. Credentials and unsupported selector text are not put in reports.

For ordinary npm globals without a retained request, npm 11.19.1's
[global root reconstruction](https://github.com/npm/cli/blob/v11.19.1/workspaces/arborist/lib/arborist/build-ideal-tree.js)
uses installed direct package names with an unconstrained request. KeepUpdated
uses an explicitly labelled `npm-default-latest` policy only when the effective
native tag is `latest` and no contradictory request metadata is present. This is
a default policy, not proof that the user never installed a pin. Installed
`package.version` and lockfile exact resolutions alone are not pin declarations.
An owning global manifest declaration can restrict the request; opaque retained
`_requested`, `_from` or `_spec` records require review.

## Saved plans and verification

No global schema version changes. Old Node update plans without the new evidence
must be previewed again. Whole-plan and per-item rebuilds reject manager, layout,
source/configuration, bin or manifest drift before mutation. Readback requires
the approved version and unchanged selector evidence, alongside existing owner,
installation and command-entry checks. Script consent, independent query-failure
handling, uninstalls and the one-attempt rule remain in place.

Every declared command must have an executable payload inside the current package
and an executable entry in the preview's bound bin directory that resolves to that
exact payload. One active command does not verify the other commands. Readback
accepts symlinks and pnpm's plain Node or direct cmd-shim 7.0.0 wrappers, including
the literal `NODE_PATH` block. Custom interpreter/argument/PATH variants and
`directories.bin` without explicit bin declarations remain unverified. Package
commands are never executed for this check. Packages with no bin declarations
retain metadata and selector verification.

Controlled tests cover default npm policy, permitted ranges, pins, conflicting
requests, two prefixes, unknown layouts, drift, query failures, saved-plan
compatibility, exact argv, unchanged selectors and unsuccessful verification.
Bounded package reads through Apply cover multi-command updates with missing,
stale, foreign or mismatched entries, both pnpm stores, supported wrappers and
custom bin directories. These use controlled manager outcomes, not host updates.
The opt-in `scripts/acceptance/node_policy.py` additionally exercises the supplied
exact real managers against synthetic packages in a local registry. It requires
macOS and sandboxes writes to a new directory; it downloads no manager and runs no
host software update. Supply absolute `--npm`, `--pnpm`, `--bun`, `--node` paths
and a new private `--out` directory. Its registry is HTTP for the isolated
command-semantics check; product registry/source guards are tested separately. Its pnpm cases load the
native macOS rc without an XDG override, preserve its custom store, and keep a
synthetic runtime request present while testing the shared no-provisioning argv.
They also verify that an incompatible package engine is still rejected.
