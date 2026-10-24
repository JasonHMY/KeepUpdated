# KeepUpdated

KeepUpdated is an open-source Mac software manager. It brings apps, Skills, MCP installations, packages and runtimes into one inventory so you can check for updates, review a plan, then confirm supported updates or removals.

This source snapshot prepares **0.1.2 / build 10**. It is not a publication notice. The proposed release contains the Apple Silicon CLI with its matching Skill, source and checksums. App, independent Agent and plugin packages are local build candidates and are outside that public asset scope. macOS 13 hardware testing is still pending; Intel has build checks only and no release package.

## CLI and Skill

Packaged programs target macOS 13+ on Apple Silicon and need no Go, Node or Python runtime. Once 0.1.2 is published, obtain `keepupdated_0.1.2_darwin_arm64.tar.gz` and `SHA256SUMS` from the [versioned release page](https://github.com/JasonHMY/KeepUpdated/releases/tag/v0.1.2). If it is unavailable, build from source or wait for publication.

```sh
shasum -a 256 -c SHA256SUMS
mkdir keepupdated-0.1.2
tar -xzf keepupdated_0.1.2_darwin_arm64.tar.gz -C keepupdated-0.1.2
cd keepupdated-0.1.2
./keepupdated scan > scan.json
./keepupdated check --online > check.json
./keepupdated plan --action update --id ITEM_ID > plan.json
./keepupdated apply --plan plan.json --confirm EXACT_PLAN_ID
```

Use the original inventory ID and the full ID of the plan you reviewed. `plan --action update --all` previews the entire scan scope. For large exact selections, use `--ids-file ids.json` with a JSON array. Uninstalling uses its own `--action uninstall` preview; add `--cleanup` only for requested supported paths. Expired plans or changed installations require a fresh preview.

An Agent can read [skills/keepupdated/SKILL.md](skills/keepupdated/SKILL.md) in the complete extracted package and execute its local entry point:

```sh
bash skills/keepupdated/scripts/run.sh version --format json
bash skills/keepupdated/scripts/run.sh check --online
./keepupdated scan --skills-root /path/to/agent/skills --mcp-config /path/to/config.json
./keepupdated results --format json
```

The wrapper verifies the matching binary, platform and schemas; it neither downloads nor installs itself. Each client controls how it loads Skills. A shared interface does not establish real-client acceptance in every client. `results` reads the latest saved ordinary operation without replaying it; inspect its plan ID and each item.

## What it manages

Supported operations use the actual installation owner and retain source, content, permission and post-state checks. Coverage includes guarded Homebrew formulae/app-only casks, exact supported npm/pnpm/Bun globals and pipx/uv tools, independent Git or verified-copy Skills, narrow signed Sparkle ZIP/single-app DMG updates, and supported Claude skill-only plugins. MCP ownership can route to an installed package; an exact launcher-reference migration changes configuration only.

App Store updates stay manual and links open only on a user click. General vendor installers, unknown sources, pins and unsupported manager layouts remain manual. Runtime changes, scripts and graceful app exit/reopening need separate review. Privileged script-free PKG paths have controlled checks but lack signed-device acceptance. Apple Silicon OS installation is blocked. See [coverage and validation limits](docs/coverage.md) for exact boundaries.

`scan` and `check` inspect installations; online checks query public metadata. Previews may cache fixed-revision files for real Skill diffs. Local edits block replacement. Only confirmed execution changes installations. KeepUpdated does not execute discovered Skill instructions or start discovered Agents and MCP servers.

## Desktop, MCP and plugin source

The SwiftUI App uses the same CLI engine, with English and Simplified Chinese, native fallback controls on older systems, scan locations, explicit previews and item-level results. [The desktop guide](docs/desktop.md) explains the flow. Build it locally using [the release guide](docs/release.md); there is no desktop archive in the proposed CLI-only release.

`keepupdated mcp` exposes eight bounded local stdio tools. The standalone CLI supports inventory and previews; MCP execution needs the sealed native confirmation companion, absent from its tarball. The lightweight `KeepUpdatedAgent.app` and desktop plugin package that companion with the same CLI and Skill. After native approval, a short-lived worker continues if the chat closes; recovery uses that operation's ID. No client configuration, global installation or persistent service is created automatically. See [MCP](docs/mcp.md) and [the six entry points](docs/entry-points.md).

Local App, Agent and plugin builds are ad-hoc signed without Developer ID signing or Apple notarization. Customer-device launch, native client approval, accessibility and privileged/self-update acceptance remain open. The ad-hoc App cannot install its own update. A release hash does not establish publisher identity.

## Results and privacy

Inventory uses `keepupdated/v1`, plans `keepupdated/plan/v1`, execution results `keepupdated/apply/v1`, and latest ordinary recovery `keepupdated/recent-operation/v1`. Unknown, unsupported and failed checks never mean up to date. Manager exit success alone does not prove installation success. See [the JSON contract](docs/report.md).

Reports, plans and recovery records contain local installation paths. Keep them private. MCP arguments, environment values and credentials are excluded from reports; original managers retain their own configuration. There is no telemetry or automatic upload. Failed or unreadable recovery state is preserved and does not authorize replay.

## Build and feedback

This repository publishes reviewed source snapshots and release-level notes. Development branches, intermediate fixes and review discussions are maintained privately. See [CONTRIBUTING.md](CONTRIBUTING.md) for builds and feedback, and [SECURITY.md](SECURITY.md) for private security reporting.

```sh
go test -race ./...
go vet ./...
swift test --package-path desktop --scratch-path /private/tmp/keepupdated-desktop-build
```

The Go engine uses the standard library and pinned official Go MCP SDK. The desktop uses SwiftUI and system frameworks. Full source, tests, build inputs and required third-party notices are included. MIT License.
