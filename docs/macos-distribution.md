# Signed macOS distribution

Direct downloads use a Developer ID-signed, notarized app in a DMG for drag-to-Applications installation, plus the existing ZIP for self-updates. Mac App Store distribution is a later, separate project. The default build remains ad-hoc for development and ordinary CI. This workflow is implemented, but no production Developer ID, notarization or customer-device acceptance is claimed yet.

Apple references: [Developer ID certificates](https://developer.apple.com/help/account/certificates/create-developer-id-certificates/), [notarization requirements](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution), [custom workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow), [Hardened Runtime](https://developer.apple.com/documentation/security/hardened-runtime) and [nested code signing](https://developer.apple.com/library/archive/technotes/tn2206/).

Developer ID identifies the publisher. Hardened Runtime constrains executable behavior; a secure timestamp records signing time. Apple notarization scans submitted software and issues a ticket. Stapling includes that ticket in the app for offline discovery. Gatekeeper evaluates the resulting app on the receiving Mac. A valid signature or a local launch alone does not prove that downloaded software passes that evaluation.

## Account setup by the maintainer

1. Choose the long-term individual or organization that will publish KeepUpdated, then [enroll in the Apple Developer Program](https://developer.apple.com/programs/enroll/). Complete identity verification, agreements and payment personally. Apple lists USD 99 per membership year, with local pricing where available; check the actual enrollment offer. Free Apple Development signing is insufficient for this route.
2. The Account Holder creates a **Developer ID Application** certificate using Xcode's certificate management or Certificates, Identifiers & Profiles. Keep its private key in the signing Mac's Keychain. Developer ID Installer is for PKGs and is not needed for the ZIP or DMG. Do not send passwords, private keys or certificate export packages through chats, Git or PRs.
3. Create a local notarization credential profile with the interactive command below. Enter the Apple Account, Team and app-specific password locally when prompted. The script only receives the profile name. Do not add credentials to ordinary PR workflows or repository secrets just to test this change.

```sh
xcrun notarytool store-credentials KeepUpdated-notary
security find-identity -v -p codesigning
```

Record the selected Developer ID Application identity's SHA-1 fingerprint and ten-character Team ID privately. The Team must remain stable across app updates: the running authenticated app is the updater's trust root, and a downloaded candidate cannot replace that trust root with a different Team. A Team migration requires separate design and approval.

## Prepare a candidate

Use the release policy in [release.md](release.md). Select a clean, reviewed source commit with passing current-head CI. Set the intended version and a monotonically increasing `CFBundleVersion` through the release source changes first. The current defaults are 0.1.2 / build 10. These local preparation values do not publish a version or make same-build updates valid.

From that source checkout, substitute the real local fingerprint and Team for the placeholders. Use a new output directory and a new evidence directory immediately under this worktree's ignored `.local/`:

```sh
bash scripts/desktop/build.sh --dmg \
  --out "$PWD/dist/new-signed-candidate" \
  --sign-identity '40_HEX_CHARACTER_CERTIFICATE_FINGERPRINT' \
  --team-id 'YOURTEAMID' \
  --notary-profile KeepUpdated-notary \
  --evidence "$PWD/.local/new-signing-evidence"
```

`--dmg` creates both ZIP and DMG; omit it for a ZIP-only build. Without signing options it creates an ad-hoc app in an unsigned development DMG. All four signing options are required together; dirty-source signing is rejected. This command signs and submits the generated app to Apple's notary service, but does not publish it. An unlocked signing Keychain and network access to Apple are required. Credential creation, Keychain access changes and signing authority must be approved before this command is used.

The builder signs from the inside out, with Hardened Runtime and secure timestamps on each executable. It does not use `codesign --deep` to sign, register a helper or execute an update.

| Component | Signature identifier | Entitlements |
| --- | --- | --- |
| App / `Contents/MacOS/KeepUpdatedDesktop` | `com.keepupdated.desktop` | None |
| CLI / `Contents/Resources/keepupdated` | `com.keepupdated.cli` | None |
| Authorization bridge / `Contents/MacOS/KeepUpdatedAuthorization` | `com.keepupdated.authorization` | `com.apple.security.automation.apple-events=true` |
| Helper / `Contents/Library/HelperTools/KeepUpdatedHelper` | `com.keepupdated.privileged` | None |
| Bundled manager / `Contents/Library/Tools/mas` | `com.keepupdated.mas` | None |

All components must satisfy Apple's Developer ID Application requirement with the selected Team. The bridge retains its existing, separately approved restart-dialog capability. No debugging, JIT, unsigned-memory or library-validation exceptions are added. The CLI remains at its existing resource path because the Skill and updater bind that exact location; it is explicitly signed and verified in addition to the outer resource seal. Whole-app updates replace the complete sealed bundle. A newly bundled Mach-O outside this list stops formal verification until its signing policy is reviewed.

The builder retains the submitted ZIP and Apple responses in the private evidence directory, requires `Accepted` in both the response and notary log, matches the log job ID and archive SHA-256, staples the app, then creates a new final ZIP. It extracts that ZIP again and verifies nested signatures, runtime flags, timestamps, exact entitlement sets, the stapled ticket and Gatekeeper acceptance before writing final checksums. With `--dmg`, it copies the verified ZIP app into a read-only HFS+ disk image alongside an `/Applications` shortcut. It signs the DMG with the same Developer ID and a secure timestamp, separately submits and staples that image, and assesses its primary signature with Gatekeeper. A read-only mount must contain the same app bytes as the ZIP and the expected shortcut; detach failure stops delivery and retains the mount/evidence for inspection. The shared checksum file includes both final assets only after both pass. The submitted ZIP and DMG are retained before stapling and are not the final distribution assets. `DESKTOP_BUILDINFO.json` is written before signing and directs the reviewer to the actual ticket and external evidence; it never fabricates a later notarization result inside an already signed bundle.

Review `notary-log.json` for warnings even on acceptance. On rejection, log-fetch failure, timeout or stapling failure, preserve the private directory and inspect the saved submission ID with `xcrun notarytool info` / `log` using the same profile. Do not repeatedly upload while an earlier submission is pending. No successful checksum is produced on failure. Use a new candidate/evidence directory for a subsequent complete build; never overwrite retained bytes or change a published tag/asset.

## Independent verification

Check the final checksum file, extract the actual final ZIP into a new directory, and verify against the **reviewed** Team, commit, version, build and source-tree digest. Obtain those expected values from the selected source and private build record, not solely from untrusted downloaded metadata:

```sh
(cd /path/to/new-signed-candidate && shasum -a 256 -c KeepUpdated_0.1.2_SHA256SUMS)
ditto -x -k /path/to/new-signed-candidate/KeepUpdated_0.1.2_darwin_arm64.zip /path/to/new-extraction
bash scripts/desktop/verify-distribution.sh \
  /path/to/new-extraction/KeepUpdated.app \
  YOURTEAMID SOURCE_COMMIT 0.1.2 BUILD_NUMBER SOURCE_TREE_SHA256
```

For the DMG from the same build, compare its contents against that independently verified extracted app:

```sh
bash scripts/desktop/dmg.sh \
  /path/to/new-signed-candidate/KeepUpdated_0.1.2_darwin_arm64.dmg \
  /path/to/new-extraction/KeepUpdated.app YOURTEAMID
```

Use the selected release version in filenames and arguments. Verification compares the bundled CLI, CLI build record, desktop build record and plist; it checks the three protocols and matching packaged Skill as well. The build itself also checks the SDK/minimum OS, pinned mas source/licenses and unchanged source before and after packaging. Keep the final hashes and private logs together. These tools do not certify the separate standalone CLI tarball, which remains on its existing build path.

Before public release, use a separate Mac or disposable VM without development trust overrides. Download the exact final candidate through a browser, retain quarantine, extract the ZIP normally or open the DMG and drag the app to a writable Applications location, eject the image and launch through Finder. Test both download formats separately. Record the OS/architecture, asset hash, quarantine presence, first-launch prompts and read-only version/inventory behavior. Test the stapled app offline as well. Do not disable Gatekeeper or clear quarantine to pass this gate.

A real self-update needs two signed, notarized versions from the same Team, increasing version/build, and the actual public stable-release assets with GitHub digest metadata. The unchanged updater still checks source, hashes, identity, current content and explicit install/reopen consent. Until those conditions exist, keep that gate open. Helper registration, Authorization Services/XPC, privileged installs and restart/recovery need their own disposable-device acceptance; this packaging task does not activate them on the developer's Mac.

Only after the maintainer approves the version, source commit and exact final assets may the release be published under [release.md](release.md#pre-distribution-acceptance). Ordinary hosted CI remains credential-free with read-only permissions. A passing command fixture, ad-hoc package or local launch does not satisfy the production acceptance gates above.

## Current release gates

Select the final reviewed source before building. Check signatures, source metadata, notarization, checksums and device behavior on the exact generated assets. No production signing or customer-device acceptance is implied by a local build.

The maintainer's remaining account steps are to activate paid membership (if not active), create a local Developer ID Application signing identity with its private key, then store and validate the local `KeepUpdated-notary` profile. Membership/agreements/payment and new security access remain personal actions. Once configured, existing tools can perform signing and notarization without sharing private keys or passwords. Public release approval and clean-device/self-update/privileged acceptance remain separate gates.
