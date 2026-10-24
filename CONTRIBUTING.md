# Source, builds and feedback

This public repository contains reviewed KeepUpdated source snapshots. Development branches, intermediate fixes, review discussions and internal validation records are maintained privately. Published snapshots remain open source under [the MIT License](LICENSE), and their code can be compared and built independently.

## Feedback

Use [Issues](https://github.com/JasonHMY/KeepUpdated/issues) for ordinary bugs and feature suggestions. Include the version, source commit, macOS/architecture, steps, expected result and a minimal redacted error excerpt. Public PRs are not the development handoff for this repository. Discuss a proposed contribution with the maintainer before opening one. Follow the [Code of Conduct](CODE_OF_CONDUCT.md).

Report vulnerabilities through [SECURITY.md](SECURITY.md). Do not upload full scan reports, MCP configurations, credentials, private paths or chat records. Use synthetic examples.

## Build from a snapshot

Clone the public repository or extract a versioned source archive. `go.mod` declares Go 1.25.0; package builds use Go 1.27.2. The language baseline has not been separately tested with Go 1.25. Swift packages require macOS and Xcode; desktop builds need a macOS 26+ SDK.

```sh
go mod download
go test -race ./...
go vet ./...
swift test --package-path desktop --scratch-path /private/tmp/keepupdated-desktop-build
swift test --package-path privileged --scratch-path /private/tmp/keepupdated-privileged-build
```

Choose checks appropriate to the changes. Run mutation tests in isolated directories with controlled software. Do not enable `KEEPUPDATED_LIVE_RUNTIME_TEST` or `KEEPUPDATED_LIVE_SYSTEM_TEST` without explicit opt-in. These tests do not authorize updating software on the host Mac, changing services or client settings, or starting discovered servers.

See [the build and release guide](docs/release.md) for clean-source packaging, paired Skills, licenses and cross-package verification. Use a new output directory for every candidate. Source archives without Git metadata need a local Git commit before using the builders, which bind artifacts to the actual source commit. Such a local commit has its own identity and is not the published snapshot commit.

## Publication

The maintainer selects a reviewed final snapshot, version and exact public assets. Source snapshots and public releases are separate decisions. Public CI checks only the published snapshot with read-only permissions and does not upload local App, Agent or plugin candidates, publish Releases or change repository settings. Intermediate development history is never merged into this repository. Published tags and assets are immutable; corrections use a new version.
