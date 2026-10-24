#!/bin/bash
set -Eeuo pipefail
stage=arguments
trap 'printf "Agent verification stopped during %s (line %s).\n" "$stage" "$LINENO" >&2' ERR
source "$(dirname "${BASH_SOURCE[0]}")/verify-checks.sh"
[ "$#" = 3 ] || { echo 'Usage: scripts/agent/verify.sh EXTRACTED_APP SOURCE_COMMIT SOURCE_TREE_SHA256' >&2; exit 2; }
app="$1"; commit="$2"; digest="$3"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
[[ "$commit" =~ ^[a-f0-9]{40}$ ]] && [[ "$digest" =~ ^[a-f0-9]{64}$ ]]
plist="$app/Contents/Info.plist"
resources="$app/Contents/Resources"
metadata="$resources/AGENT_BUILDINFO.json"
field() { plutil -extract "$1" raw -o - "$2"; }
stage="bundle and source metadata"
verify_equal "bundle identifier" "$(field CFBundleIdentifier "$plist")" com.keepupdated.agent
verify_equal "main executable" "$(field CFBundleExecutable "$plist")" KeepUpdatedAuthorization
verify_equal "bundle version" "$(field CFBundleShortVersionString "$plist")" 0.1.2
verify_equal "bundle build" "$(field CFBundleVersion "$plist")" 10
verify_version "bundle minimum macOS" "$(field LSMinimumSystemVersion "$plist")" 13.0
verify_equal "accessory application" "$(field LSUIElement "$plist")" true
verify_equal "source commit" "$(field source_commit "$metadata")" "$commit"
verify_equal "source digest" "$(field source_tree_sha256 "$metadata")" "$digest"
verify_equal "metadata version" "$(field version "$metadata")" 0.1.2
verify_equal "metadata target" "$(field target "$metadata")" darwin/arm64
stage="sealed bundle and executable signatures"
codesign --verify --deep --strict -R '=identifier "com.keepupdated.agent"' "$app"
codesign --verify --strict -R '=identifier "com.keepupdated.cli"' "$resources/keepupdated"
codesign --verify --strict -R '=identifier "com.keepupdated.agent"' "$app/Contents/MacOS/KeepUpdatedAuthorization"
stage="signed payload manifest"
verify_payload "$(dirname "$app")/KeepUpdatedAgent_PAYLOAD.json" "$app" "$metadata" "$commit" "$digest"
stage="payload allowlist"
test ! -e "$app/Contents/Library"
while IFS= read -r -d '' file; do
    if /usr/bin/file -b "$file" | grep -q 'Mach-O'; then
        case "${file#"$app"/}" in Contents/MacOS/KeepUpdatedAuthorization|Contents/Resources/keepupdated) ;; *) echo 'Unexpected Agent executable.' >&2; exit 1 ;; esac
        verify_equal "executable architecture" "$(lipo -archs "$file")" arm64
    fi
done < <(find "$app" -type f -print0)
stage="companion platform and SDK"
companion_build="$(xcrun vtool -show-build "$app/Contents/MacOS/KeepUpdatedAuthorization")"
verify_equal "companion platform" "$(printf '%s\n' "$companion_build" | awk '$1=="platform"{print $2}')" MACOS
verify_version "companion minimum macOS" "$(printf '%s\n' "$companion_build" | awk '$1=="minos"{print $2}')" 13.0
verify_version "companion SDK" "$(printf '%s\n' "$companion_build" | awk '$1=="sdk"{print $2}')" "$(field sdk_version "$metadata")"
stage="CLI source and version metadata"
expected_commit="$commit"
if [ "$(field working_tree_dirty "$metadata")" = true ]; then expected_commit+="+dirty"; fi
verify_equal "CLI source metadata" "$(field commit "$resources/BUILDINFO.json")" "$expected_commit"
result="$("$resources/keepupdated" version --format json)"
for key in version commit build_date; do verify_equal "CLI $key" "$(printf '%s' "$result" | plutil -extract "$key" raw -o - -- -)" "$(field "$key" "$resources/BUILDINFO.json")"; done
stage="MCP launch metadata"
verify_equal "MCP command" "$(field command "$resources/MCP_LAUNCH.json")" keepupdated
verify_equal "MCP argument" "$(field args.0 "$resources/MCP_LAUNCH.json")" mcp
stage="matching repository Skill"
cmp "$repo_root/skills/keepupdated/SKILL.md" "$resources/skills/keepupdated/SKILL.md"
cmp "$repo_root/skills/keepupdated/scripts/run.sh" "$resources/skills/keepupdated/scripts/run.sh"
env -u KEEPUPDATED_BIN bash "$resources/skills/keepupdated/scripts/run.sh" version --format json > /dev/null
go_bin="${KEEPUPDATED_GO:-$repo_root/.local/tools/go/bin/go}"
if [ ! -x "$go_bin" ]; then go_bin="$(command -v go)"; fi
stage="linked module licenses"
(cd "$repo_root" && GOTOOLCHAIN=local "$go_bin" run ./scripts/release --verify-package "$resources")
printf 'Agent bundle identity, payloads, matching Skill and linked notices verified.\n'
