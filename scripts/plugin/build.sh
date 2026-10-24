#!/bin/bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
[ "$#" = 2 ] && [ "$1" = --out ] || { echo 'Usage: scripts/plugin/build.sh --out NEW_DIRECTORY' >&2; exit 2; }
output_dir="$2"
[ ! -e "$output_dir" ] && [ ! -L "$output_dir" ] || { echo 'Output exists; choose a new directory.' >&2; exit 1; }
cd "$repo_root"
[ -z "$(git status --porcelain --untracked-files=normal)" ] || { echo 'Commit source before building a plugin candidate.' >&2; exit 1; }
commit="$(git rev-parse HEAD)"
go_bin="${KEEPUPDATED_GO:-$repo_root/.local/tools/go/bin/go}"
if [ ! -x "$go_bin" ]; then go_bin="$(command -v go)"; fi
scratch="$(mktemp -d /private/tmp/keepupdated-plugin.XXXXXX)"
trap 'rm -rf "$scratch"' EXIT
KEEPUPDATED_GO="$go_bin" bash scripts/agent/build.sh --out "$scratch/agent"
plugin="$scratch/marketplace/plugins/keepupdated"
mkdir -p "$plugin/assets" "$scratch/marketplace/.agents/plugins"
ditto -x -k "$scratch/agent/KeepUpdatedAgent_0.1.2_darwin_arm64.zip" "$plugin"
cp -R plugin/. "$plugin/"
cp skills/keepupdated/SKILL.md "$plugin/skills/keepupdated/SKILL.md"
# Reuse the existing app icon without changing the signed bundle.
sips -s format png desktop/Assets/KeepUpdated.icns --out "$plugin/assets/logo.png" >/dev/null
chmod 755 "$plugin/skills/keepupdated/scripts/run.sh"
cat > "$scratch/marketplace/.agents/plugins/marketplace.json" <<'JSON'
{"name":"keepupdated-local","interface":{"displayName":"KeepUpdated Local"},"plugins":[{"name":"keepupdated","source":{"source":"local","path":"./plugins/keepupdated"},"policy":{"installation":"AVAILABLE","authentication":"ON_INSTALL"},"category":"Productivity"}]}
JSON
KEEPUPDATED_GO="$go_bin" bash scripts/plugin/verify.sh "$plugin"
archive=KeepUpdatedPlugin_0.1.2_darwin_arm64.zip
ditto -c -k --norsrc --noextattr --noacl --keepParent "$plugin" "$scratch/$archive"
mkdir "$scratch/extracted"
ditto -x -k "$scratch/$archive" "$scratch/extracted"
KEEPUPDATED_GO="$go_bin" bash scripts/plugin/verify.sh "$scratch/extracted/keepupdated"
[ "$commit" = "$(git rev-parse HEAD)" ] && [ -z "$(git status --porcelain --untracked-files=normal)" ] || { echo 'Source changed during build; rebuild stable source.' >&2; exit 1; }
mkdir -p "$output_dir"
cp "$scratch/$archive" "$output_dir/"
ditto --norsrc --noextattr --noacl "$scratch/marketplace" "$output_dir/marketplace"
KEEPUPDATED_GO="$go_bin" bash scripts/plugin/verify.sh "$output_dir/marketplace/plugins/keepupdated"
(cd "$output_dir" && shasum -a 256 "$archive" > KeepUpdatedPlugin_0.1.2_SHA256SUMS)
printf 'Plugin candidate: %s/%s\nLocal marketplace: %s/marketplace\n' "$output_dir" "$archive" "$output_dir"
