# Reporting security issues

Report vulnerabilities through [GitHub private vulnerability reporting](https://github.com/JasonHMY/KeepUpdated/security/advisories/new). Do not put exploit details in public Issues or PRs before coordinated disclosure.

Reports for the current public source snapshot and matching packages receive best-effort review. There is no promised support period, stability commitment or fixed response time. The 0.1.2 release contains the CLI and matching Skill only; App, Agent and plugin builds remain local candidates.

Include the version, source commit, platform, installation source, concrete impact and minimal reproduction steps or an isolated example. Relevant concerns include unconfirmed operations, bypassed source/content checks, out-of-scope changes and exposed credentials. Remove credentials and unrelated private information even from a private report. Do not upload full local scans, MCP configurations or chats. Ordinary functional bugs belong in [Issues](https://github.com/JasonHMY/KeepUpdated/issues).

The maintainer reviews reports and coordinates any disclosure privately. Do not test another person's device or perform destructive tests on real user installations without authorization.

Packaged source defaults to ad-hoc signing without Developer ID or notarization. Checksums do not establish developer identity or customer-device launch acceptance. Privileged execution and self-updates retain separate signing, native authorization and device gates. See [coverage](docs/coverage.md) and [distribution limits](docs/macos-distribution.md).
