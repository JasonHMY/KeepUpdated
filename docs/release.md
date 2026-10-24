# Builds and releases

This source prepares **0.1.2 / build 10**, with no publication implied. The proposed public release contains exactly four assets: `keepupdated_0.1.2_darwin_arm64.tar.gz` (CLI and matching Skill), `SHA256SUMS`, `keepupdated_0.1.2_source.tar.gz` and `SOURCE_SHA256SUMS`. App, independent Agent and plugin archives remain local candidates.

Packaged programs target Apple Silicon and macOS 13+. Running them needs no Go, Node or Python runtime. macOS 13 hardware testing remains pending. Intel has compilation checks only and no release package. [Coverage](coverage.md#validation-limits) records remaining client, device, signing and privileged-operation limits.

## Release policy

The public repository receives reviewed release snapshots with final source and release-level notes. Intermediate development branches, fixes, reviews and internal validation records remain private. Copy only the selected reviewed files into an independent public repository; never merge private history or push all private refs. Public CI verifies the resulting exact snapshot with read-only permissions. It does not publish Releases, upload local packages or change settings.

Versions follow [Semantic Versioning](https://semver.org/). Source defaults retain `-dev`; package builders inject the selected version, actual source commit and its timestamp. A source snapshot and a public release require separate maintainer decisions. Before publication, confirm clean committed source, current-source CI, applicable acceptance limits and the exact asset hashes. A local build, passing fixture or shared version does not clear untested gates.

| Value | Matching locations |
| --- | --- |
| Product and CLI version | `cmd/keepupdated/main.go`, `scripts/release/main.go`, matching tests |
| App and Agent version/build | `scripts/desktop/build.sh`, `scripts/agent/build.sh`, Agent verification and fixtures; 0.1.2 / 10 |
| Plugin version/assets | `plugin/plugin.json`, `plugin/.codex-plugin/plugin.json`, `scripts/plugin/` |
| Matching Skill | `skills/keepupdated/SKILL.md`, `scripts/run.sh` and release tests |
| Snapshot CI | `.github/workflows/verify.yml` |
| User instructions | README, desktop guide, this file and CHANGELOG |

Scan, plan, apply and recent-operation schema IDs do not change mechanically with product versions. Check CLI version metadata, MCP capabilities, Skill guards, desktop decoding and their existing tests together.

Build only after source edits stop. Use new output directories and retain previous assets. Only after the maintainer confirms the version, source and files may `v0.1.2` be tagged and its approved assets published. Download the actual assets afterward and check their hashes and source identity. Published tags and assets are immutable; corrections use a new version.

The source archive must match the selected public commit and complete public file list, including modes and required licenses. Generate it from that commit, for example `git archive --format=tar.gz --prefix=keepupdated-0.1.2/ HEAD`, and write a separate `SOURCE_SHA256SUMS`. A private-to-public source mapping belongs outside the public tree.

## Cross-package candidate readback

The [six entry points](entry-points.md) share a selected product version and source.
After each package's own signature, payload, notice and launch checks, extract the
actual CLI tarball, App ZIP, Agent ZIP and plugin ZIP into separate directories.
Compare those extracted contents together:

```sh
bash scripts/release/verify-sync.sh \
  /path/to/extracted-cli \
  /path/to/extracted-app/KeepUpdated.app \
  /path/to/extracted-agent/KeepUpdatedAgent.app \
  /path/to/extracted-plugin/keepupdated SOURCE_COMMIT
```

This reads each actual CLI's version/schema output, checks its BUILDINFO against
the selected clean source, compares App/Agent source-tree digests and bundle builds,
checks both plugin manifest versions, compares bundled Skills and CLI launchers,
and calls the plugin's delegating launcher. It reports binary SHA-256 values;
different signing modes may change those bytes. CI runs it on its extracted
archives. Do not point it at a staging folder and call that archive acceptance.
The check supplements each package's own verifier; it does not replace signatures,
native approval, actual client/device acceptance or public-release approval.
A previously installed copy can remain older until separately requested installation;
record that distinction instead of silently replacing it.

## Obtaining a release package

Once published, download `keepupdated_0.1.2_darwin_arm64.tar.gz` and `SHA256SUMS` from the [0.1.2 release](https://github.com/JasonHMY/KeepUpdated/releases/tag/v0.1.2). This preparation does not publish that page or those assets. Verify them in the download directory, then extract and run:

```sh
shasum -a 256 -c SHA256SUMS
mkdir keepupdated-0.1.2
tar -xzf keepupdated_0.1.2_darwin_arm64.tar.gz -C keepupdated-0.1.2
cd keepupdated-0.1.2
./keepupdated version --format json
./keepupdated --help
./keepupdated scan > scan.json
./keepupdated check --online --store-country AU > check.json
```

Verification must report `OK`. Keep the included `BUILDINFO.json`, which records source commit, version, toolchain and target platform.
`scan.json` and `check.json` are private local reports that may contain software inventories and local paths. Review them before sharing.
See [coverage.md](coverage.md) for check capabilities and remaining limits.

## Building from source

`go.mod` declares Go 1.25.0 as the language baseline, as required by the pinned official MCP Go SDK v1.8.0. Current acceptance and release builds use Go 1.27.2; the Go 1.25 toolchain has not been tested separately. The toolchain comes from [official Go downloads](https://go.dev/dl/).
Go 1.27 requires at least macOS 13, as documented in the [Go release notes](https://go.dev/doc/go1.27#linker). This is not a claim that every macOS version has been tested on hardware.
The following steps build a selected release source. See [CONTRIBUTING.md](../CONTRIBUTING.md) for independent builds and feedback. For later versions, synchronize version fields first, then run from the repository root. Download the pinned module versions before the offline packaging step:

```sh
go mod download
go test -race ./...
go vet ./...
go run ./scripts/release --version 0.1.2 --out /path/to/new-cli-candidate
```

The builder requires a clean worktree by default so a package cannot claim a commit that omits its actual source.
During development, `--allow-dirty` creates a local candidate with `+dirty` in commit metadata. Do not distribute it as a formal release.
Go only needs to be available in the current shell; this process does not change global PATH, services or schedules.

Default CLI outputs are `dist/product-v0.1.2/keepupdated_0.1.2_darwin_arm64.tar.gz` and `dist/product-v0.1.2/SHA256SUMS`. An existing archive or checksum file causes refusal. Specify a new `--out` directory; do not delete old files to bypass the guard. Preserve earlier outputs in their own directories.
The archive contains `keepupdated`, `LICENSE`, `README.md`, `BUILDINFO.json`, runtime notices, `LICENSES/modules.json`, original linked-module notices under `LICENSES/modules/`, `LICENSES/ModuleNotices.txt`, and the matching Skill and wrapper.
`LICENSES/Go.txt` preserves the build toolchain's Go LICENSE unchanged. Notices use the actual `go list -deps` for the same target platform and retain Go PATENTS, LICENSE/PATENTS for imported standard-library vendor components, and original fiat-crypto notices. Sources are identified only by GOROOT-relative paths.
Third-party modules are selected from the built executable’s Go metadata, then their original license, notice and patent files are collected from the same target’s imported package directories and module roots. `LICENSES/modules.json` records module versions, sums and notice hashes alongside source commit, toolchain and target. Missing licenses or module replacements stop packaging. Compiler tools and unlinked modules are excluded. The builder uses cached pinned modules and disables network downloads during compilation.

Run `go run ./scripts/release --verify-package /path/to/extracted-package` to compare the manifest with the actual executable and verify every listed notice hash. Full App and Agent packaging run the same check on their extracted Resources directories.
Repository directories, local inventories, caches, logs, credentials and development tools are not packaged.
Builds use `CGO_ENABLED=0`, `-trimpath` and `-buildvcs=false`; version metadata is injected from a fixed source commit and commit time.
CLI rebuilds with identical source, Go version and target platform should have the same SHA-256. Use separate new directories for the initial and verification builds:

```sh
go run ./scripts/release --version 0.1.2 --out /path/to/new-cli-rebuild
cmp /path/to/new-cli-candidate/SHA256SUMS /path/to/new-cli-rebuild/SHA256SUMS
cd /path/to/new-cli-candidate
shasum -a 256 -c SHA256SUMS
tar -tzf keepupdated_0.1.2_darwin_arm64.tar.gz
```

`.github/workflows/verify.yml` runs formatting, race tests, vet, repeated packaging, checksum verification and extracted version/help checks on an Apple Silicon runner.
It also runs the same version/help checks through the extracted matching Skill script. Fixed fixtures test version rejection, path selection, argument forwarding and exit-code forwarding.
All Actions are pinned to full commits. The token has only `contents: read`; Git credentials are not persisted, and CI neither creates Releases nor pushes.
CI does not scan local software or check users' software online. It uploads no artifacts or release assets.

## Desktop package

Run `bash scripts/desktop/build.sh --out /path/to/new-desktop-candidate` to generate `KeepUpdated_0.1.2_darwin_arm64.zip` and `KeepUpdated_0.1.2_SHA256SUMS`. The default directory remains `dist/product-v0.1.2`. The ZIP contains the complete `KeepUpdated.app`. Add `--dmg` to also create `KeepUpdated_0.1.2_darwin_arm64.dmg` with the same app and an Applications shortcut; the desktop checksum file then covers both assets. The ZIP remains the self-update payload. Building requires an Xcode/Swift toolchain with a macOS 26+ SDK; running the app does not require those development tools. The desktop package includes the matching CLI, Skill and licenses. Its displayed version is 0.1.2 and `CFBundleVersion` is 10, continuing the monotonically increasing build sequence.

The script rejects dirty worktrees and existing desktop outputs by default. Use `--out /path/to/new-directory` to retain the previous candidate; `--allow-dirty` is for local development only. Source digests are checked before and after building. Source changes during the build prevent delivery of a mixed version. The app is signed in a temporary directory, then the actual ZIP is extracted and its signature checked, avoiding interference from metadata attached by desktop sync directories. `DESKTOP_BUILDINFO.json` records source, digest and signing state.

Desktop ZIPs do not promise byte-for-byte reproducibility: build time, ZIP timestamps and signing metadata may differ. Independently check each asset's SHA-256, source commit and digest, plist version/build, bundled CLI version and three schemas, matching Skill, signature, minimum OS and linked SDK. The CLI's identical-hash requirement does not apply to desktop ZIPs.

Default desktop packages are ad-hoc signed, without Developer ID signing or Apple notarization. The optional [signed distribution workflow](macos-distribution.md) adds explicit Developer ID signing, separate app/DMG notarization, ticket stapling and independent verification; it requires separately configured local credentials and does not publish a release. A successful local launch does not mean every customer device will open them without a warning. Update the actual distribution status and acceptance results after developer signing and notarization are available; do not claim them in advance.

## Using the package with a local Agent

The complete extracted package includes the repository Skill. It is not automatically installed into any client or global directory.
For clients that explicitly load Skill paths, give the Agent the actual absolute path in the extracted package, for example:

> Read `/absolute/path/to/keepupdated-0.1.2/skills/keepupdated/SKILL.md`, use it to scan local software read-only, and explain the report's coverage and gaps.

Explicit loading differs by client. Successfully reading the path does not establish native Skill installation or automatic discovery in that client. See [validation limits](coverage.md#validation-limits); ordinary Skill commands do not expose the dedicated `special` or `self-update` CLI entry points.
You can also run the same entry point directly:

```sh
bash /absolute/path/to/keepupdated-0.1.2/skills/keepupdated/scripts/run.sh version --format json
bash /absolute/path/to/keepupdated-0.1.2/skills/keepupdated/scripts/run.sh scan
bash /absolute/path/to/keepupdated-0.1.2/skills/keepupdated/scripts/run.sh check --online --store-country AU
```

The 0.1.2 CLI declares `recent_operation_schema=keepupdated/recent-operation/v1`; its matching wrapper also accepts `results --format json` for read-only recovery.

On every call, the script verifies `0.1.2`, `darwin/arm64`, `keepupdated/v1`, `plan_schema=keepupdated/plan/v1` and `apply_schema=keepupdated/apply/v1`, then forwards arguments unchanged and preserves the program's exit code. Binaries without the required plan/apply protocols are rejected.
By default, it uses only the binary at the complete package's relative root. It does not call a same-named PATH program, download, install or change client settings.
If only the Skill folder was moved, the user can explicitly set `KEEPUPDATED_BIN=/absolute/path/to/keepupdated` to select the matching binary. The same version checks still apply.
A missing or mismatched binary stops execution. Obtain the complete package from the fixed `v0.1.2` release page identified by the script and verify it; do not execute a changing main branch.

## Pre-distribution acceptance

Confirm that the selected source commit is reachable from `main`, its worktree is clean, and matching CI and acceptance checks have passed. After extraction, check version, help, packaged files and metadata. Scans and checks use isolated or authorized environments; public evidence uses synthetic and redacted data only. Desktop packages also require the independent signature and SDK checks above.
After the maintainer explicitly approves the version, source commit and asset scope, create the version tag on that source commit, attach only the approved archives and their checksum files, then download and read back the actual assets. A combined CLI/desktop release includes both archives and both checksum files; a CLI-only release follows the asset scope above. Confirm tag target, asset names, hashes and source metadata agree.
Build scripts and CI do not perform these release actions automatically.

Local candidates have no Developer ID signing or Apple notarization. Ad-hoc signing does not verify a developer identity.
A successful local run does not establish the Gatekeeper experience after a browser download. Without a real quarantined-download test, do not claim macOS will allow it without a warning.
If blocked, verify the source and follow [Apple's normal opening procedure](https://support.apple.com/102445), or build from source. Do not disable Gatekeeper globally.

## Independent local MCP Agent package

Run `bash scripts/agent/build.sh --out /path/to/new-agent-candidate` after source edits stop. It builds `KeepUpdatedAgent_0.1.2_darwin_arm64.zip` and `KeepUpdatedAgent_0.1.2_SHA256SUMS`. This is a separate lightweight `KeepUpdatedAgent.app`: `Contents/Resources/keepupdated` runs the `mcp` command; `Contents/MacOS/KeepUpdatedAuthorization` supplies ordinary native confirmation. The Resources directory includes the matching repository Skill, CLI metadata and all linked notices. It contains no desktop inventory window, privileged helper, App Store manager or global PATH launcher.

`MCP_LAUNCH.json` describes the package-relative CLI command and `mcp` argument; it is launch metadata, not an installed client configuration. Configure a client explicitly with the extracted bundle’s absolute `Contents/Resources/keepupdated` path and the `mcp` argument. Packaging does not edit any Agent configuration, install a Skill or start a service.

The current builder produces an ad-hoc local review candidate. Developer ID signing, notarization and a quarantined customer-device launch remain release gates. Ordinary confirmation does not grant administrator access; capabilities and previews retain blocked/manual results when privileged support is absent.

`AGENT_BUILDINFO.json` binds source commit/tree digest, version, platform, selected SDK and signed CLI hash inside the sealed App. The ZIP contains exactly `KeepUpdatedAgent.app` and the sibling `KeepUpdatedAgent_PAYLOAD.json`. This external manifest records the final signed CLI and companion hashes and repeats the source/version binding; it avoids changing the main executable’s signed Resources after signing. Keep the manifest beside the extracted App for independent package verification. The archive checksum covers both entries. The builder extracts its actual ZIP and checks bundle/executable identities, signatures, architecture/minimum OS, source metadata, matching Skill and module licenses. Repeat that check with `KEEPUPDATED_GO=/absolute/path/to/go bash scripts/agent/verify.sh /path/to/KeepUpdatedAgent.app SOURCE_COMMIT SOURCE_TREE_SHA256`. Source must match the selected checkout. This development verifier requires Go and Xcode tools; running the packaged MCP does not.

## In-app updates after public release

Settings can check the unauthenticated public stable release for
`JasonHMY/KeepUpdated`. Drafts and prereleases do not become update candidates.
An inaccessible repository, failed query or unverified current app is not reported
as up to date. The current ad-hoc candidate cannot install an in-app update.

A future release must include `KeepUpdated_X.Y.Z_darwin_arm64.zip` and
`KeepUpdated_X.Y.Z_SHA256SUMS` on the same public `vX.Y.Z` release. Both assets need
GitHub's SHA-256 digest metadata. The checksum file must include the exact ZIP
name and digest. The ZIP must contain one `KeepUpdated.app`, with the matching
version, a strictly increasing build number, compatible architecture/minimum OS,
and valid Developer ID signatures accepted by Gatekeeper.

The authenticated currently running app is the developer trust root: its bundled
CLI and actual parent app instance are bound before accepting its Developer ID
Team. A candidate must retain that Team and bundle identity. A release hash alone
does not establish developer identity. Changing developer identity requires a
separately reviewed distribution path; it is not learned from the download.

After explicit download/install/reopen consent, the app verifies and stages the
update before closing. A private copy of the authenticated installed CLI waits
outside the app bundle, binds the exact process and original/candidate content,
then exchanges the directories atomically and verifies the installed app before
reopening that path. It requires a user-writable installation and does not use
root, a registered service or the privileged helper. The original app and a local
recovery record remain beside the installation. Settings can reveal that record;
missing or unreadable recovery state does not mean installation succeeded.

Tests cover release and signature failures and an isolated native process/atomic
exchange/reopen path. The fixture uses controlled signature authority. Real
Developer ID/notarization, published assets and a quarantined customer-device
update still require acceptance against the future release. These checks do not establish customer-device acceptance or publish a release.

## Local desktop plugin candidate

After source edits stop and the checkout is clean, run:

```sh
bash scripts/plugin/build.sh --out /path/to/new-plugin-candidate
```

This reuses the Agent builder and produces `KeepUpdatedPlugin_0.1.2_darwin_arm64.zip`,
its checksum file, and a separate `marketplace/` directory named `keepupdated-local`.
The ZIP contains one `keepupdated/` plugin; the marketplace catalog stays outside
it. Keep the output outside a disposable worktree when it will be installed.
Use a local directory outside file-provider or cloud-sync folders: added Finder
metadata can invalidate strict App verification. The builder also verifies the
final marketplace copy and fails if the destination changes the sealed bundle.

The portable root `plugin.json` and `mcp.json` use Agent Plugins 1.0. The Codex
compatibility manifests declare the same identity, presentation and stdio command.
The portable command uses a contained `./` path; `${PLUGIN_ROOT}` is used in
`cwd`, not the command. The legacy command retains its root placeholder. Both
resolve to the CLI inside the complete, sealed `KeepUpdatedAgent.app`; the
payload manifest remains beside the App. Top-level Skills delegate to the App's
existing version/schema-checked launcher. The icon comes from the existing desktop
asset. No runtime, helper, installer, hook or scheduled job is added.

The builder extracts the actual archive and verifies the Agent signatures,
architecture, metadata, Skill and linked notices, then validates plugin paths,
executable modes, manifests and icon. It initializes both launch configurations
with an isolated home, discovers all eight tools and calls `get_capabilities`.
It does not scan the host, open approval dialogs or execute updates. Repeat with
`KEEPUPDATED_GO=/absolute/path/to/go bash scripts/plugin/verify.sh /path/to/keepupdated`.
This development check requires Go and Xcode tools; the installed plugin does not.

For an explicitly requested local installation, inspect the installed client's
help, then register the generated marketplace with
`codex plugin marketplace add /path/to/new-plugin-candidate/marketplace` and install
`keepupdated@keepupdated-local` using that client's supported flow. The builder
does not alter the user's global marketplace or client configuration. Verify the
installed copy's executable modes and strict signatures, tool discovery and a
harmless call in the target desktop client before claiming installation acceptance.
A command-line stdio check alone does not verify desktop chat or native approval UI.

This remains an ad-hoc macOS 13+ Apple Silicon candidate. Public submission,
organization eligibility, Developer ID signing, notarization and customer-device
acceptance are separate gates. See the official [plugin packaging guide](https://developers.openai.com/plugins/build/plugins)
and [desktop-only submission requirements](https://developers.openai.com/plugins/deploy/desktop-only).
