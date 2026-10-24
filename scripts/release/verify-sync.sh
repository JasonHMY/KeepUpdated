#!/bin/bash
# Compare actual extracted candidates after each package's own verification.
set -euo pipefail
[ "$#" = 5 ] || { echo 'Usage: verify-sync.sh CLI_DIR APP AGENT_APP PLUGIN_DIR SOURCE_COMMIT' >&2; exit 2; }
cli="$1"; app="$2"; agent="$3"; plugin="$4"; commit="$5"
[[ "$commit" =~ ^[a-f0-9]{40}$ ]] || { echo 'A clean source commit is required.' >&2; exit 2; }
plugin_agent="$plugin/KeepUpdatedAgent.app"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/keepupdated-sync.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
field() { /usr/bin/plutil -extract "$2" raw -o - "$1"; }
equal() {
    [ "$2" = "$3" ] || { printf 'Candidate mismatch: %s\n' "$1" >&2; exit 1; }
}
version="$(field "$cli/BUILDINFO.json" version)"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'A packaged product version is required.' >&2; exit 1; }
digest="$(field "$app/Contents/Resources/DESKTOP_BUILDINFO.json" source_tree_sha256)"
[[ "$digest" =~ ^[a-f0-9]{64}$ ]] || { echo 'Missing source tree digest.' >&2; exit 1; }
build="$(field "$app/Contents/Info.plist" CFBundleVersion)"
[[ "$build" =~ ^[0-9]+$ ]] || { echo 'Missing bundle build number.' >&2; exit 1; }

for resources in "$cli" "$app/Contents/Resources" "$agent/Contents/Resources" "$plugin_agent/Contents/Resources"; do
    "$resources/keepupdated" version --format json > "$scratch/version.json"
    for key in version commit build_date go_version; do
        expected="$(field "$cli/BUILDINFO.json" "$key")"
        equal "CLI build metadata $key" "$(field "$resources/BUILDINFO.json" "$key")" "$expected"
        equal "executable $key" "$(field "$scratch/version.json" "$key")" "$expected"
    done
    equal 'selected source commit' "$(field "$resources/BUILDINFO.json" commit)" "$commit"
    equal 'CLI target' "$(field "$resources/BUILDINFO.json" target)" darwin/arm64
    equal 'executable OS' "$(field "$scratch/version.json" os)" darwin
    equal 'executable architecture' "$(field "$scratch/version.json" arch)" arm64
    for binding in schema:keepupdated/v1 plan_schema:keepupdated/plan/v1 apply_schema:keepupdated/apply/v1 recent_operation_schema:keepupdated/recent-operation/v1; do
        equal "executable ${binding%%:*}" "$(field "$scratch/version.json" "${binding%%:*}")" "${binding#*:}"
    done
    for relative in skills/keepupdated/SKILL.md skills/keepupdated/scripts/run.sh; do
        cmp "$cli/$relative" "$resources/$relative"
    done
    # Hashes identify the delivered bytes; different signing modes may change them.
    shasum -a 256 "$resources/keepupdated"
done

for bundle in "$app" "$agent" "$plugin_agent"; do
    equal 'bundle version' "$(field "$bundle/Contents/Info.plist" CFBundleShortVersionString)" "$version"
    equal 'bundle build' "$(field "$bundle/Contents/Info.plist" CFBundleVersion)" "$build"
    metadata="$bundle/Contents/Resources/AGENT_BUILDINFO.json"
    if [ "$bundle" = "$app" ]; then metadata="$bundle/Contents/Resources/DESKTOP_BUILDINFO.json"; fi
    equal 'bundle metadata version' "$(field "$metadata" version)" "$version"
    equal 'bundle target' "$(field "$metadata" target)" darwin/arm64
    equal 'bundle source commit' "$(field "$metadata" source_commit)" "$commit"
    equal 'bundle source digest' "$(field "$metadata" source_tree_sha256)" "$digest"
    equal 'clean bundle source' "$(field "$metadata" working_tree_dirty)" false
    equal 'bundle minimum OS' "$(field "$bundle/Contents/Info.plist" LSMinimumSystemVersion)" "$(field "$metadata" minimum_macos)"
done
for manifest in "$plugin/plugin.json" "$plugin/.codex-plugin/plugin.json"; do
    equal 'plugin identity' "$(field "$manifest" name)" keepupdated
    equal 'plugin version' "$(field "$manifest" version)" "$version"
done
cmp "$cli/skills/keepupdated/SKILL.md" "$plugin/skills/keepupdated/SKILL.md"
env -u KEEPUPDATED_BIN bash "$plugin/skills/keepupdated/scripts/run.sh" version --format json > "$scratch/plugin-version.json"
for key in version commit schema plan_schema apply_schema recent_operation_schema; do
    equal "plugin launcher $key" "$(field "$scratch/plugin-version.json" "$key")" "$(field "$scratch/version.json" "$key")"
done
printf 'App, CLI, MCP payload, Skill, plugin and Agent agree: %s (%s), %s, tree %s\n' "$version" "$build" "$commit" "$digest"
