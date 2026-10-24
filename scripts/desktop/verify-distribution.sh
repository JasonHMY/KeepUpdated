#!/bin/bash
set -euo pipefail
if [ "$#" != 6 ]; then
    echo 'Usage: verify-distribution.sh EXTRACTED_APP TEAM SOURCE_COMMIT VERSION BUILD SOURCE_TREE_SHA256' >&2
    exit 2
fi
source "$(dirname "${BASH_SOURCE[0]}")/signing.sh"
app="$1"; team="$2"; commit="$3"; version="$4"; build="$5"; digest="$6"
[[ "$commit" =~ ^[a-f0-9]{40}$ ]] && [[ "$digest" =~ ^[a-f0-9]{64}$ ]]
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] && [[ "$build" =~ ^[1-9][0-9]*$ ]]
verify_distribution_app "$app" "$team"
plist="$app/Contents/Info.plist"
test "$(plutil -extract CFBundleShortVersionString raw -o - "$plist")" = "$version"
test "$(plutil -extract CFBundleVersion raw -o - "$plist")" = "$build"
test "$(plutil -extract CFBundleIdentifier raw -o - "$plist")" = com.keepupdated.desktop
metadata="$app/Contents/Resources/DESKTOP_BUILDINFO.json"
test "$(plutil -extract working_tree_dirty raw -o - "$metadata")" = false
test "$(plutil -extract source_commit raw -o - "$metadata")" = "$commit"
test "$(plutil -extract source_tree_sha256 raw -o - "$metadata")" = "$digest"
test "$(plutil -extract version raw -o - "$metadata")" = "$version"
test "$(plutil -extract target raw -o - "$metadata")" = darwin/arm64
cli="$app/Contents/Resources/BUILDINFO.json"
test "$(plutil -extract commit raw -o - "$cli")" = "$commit"
test "$(plutil -extract version raw -o - "$cli")" = "$version"
# Run only the signed, identity-checked CLI's read-only version command.
result="$("$app/Contents/Resources/keepupdated" version --format json)"
field() { printf '%s' "$result" | plutil -extract "$1" raw -o - -- -; }
test "$(field commit)" = "$commit"
test "$(field version)" = "$version"
test "$(field os)/$(field arch)" = darwin/arm64
test "$(field schema)" = keepupdated/v1
test "$(field plan_schema)" = keepupdated/plan/v1
test "$(field apply_schema)" = keepupdated/apply/v1
env -u KEEPUPDATED_BIN bash "$app/Contents/Resources/skills/keepupdated/scripts/run.sh" version --format json > /dev/null
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
go_bin="${KEEPUPDATED_GO:-$repo_root/.local/tools/go/bin/go}"
if [ ! -x "$go_bin" ]; then go_bin="$(command -v go)"; fi
(cd "$repo_root" && GOTOOLCHAIN=local "$go_bin" run ./scripts/release --verify-package "$app/Contents/Resources")
printf 'Signed distribution checks passed. Clean-device download, first launch and real self-update remain separate acceptance checks.\n'
