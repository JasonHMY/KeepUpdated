#!/bin/bash
set -euo pipefail
[ "$#" = 1 ] || { echo 'Usage: scripts/plugin/verify.sh EXTRACTED_PLUGIN' >&2; exit 2; }
plugin="$(cd "$1" && pwd)"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
metadata="$plugin/KeepUpdatedAgent.app/Contents/Resources/AGENT_BUILDINFO.json"
commit="$(git -C "$repo_root" rev-parse HEAD)"
[ "$(plutil -extract working_tree_dirty raw -o - "$metadata")" = false ]
digest="$(plutil -extract source_tree_sha256 raw -o - "$metadata")"
KEEPUPDATED_GO="${KEEPUPDATED_GO:-}" bash "$repo_root/scripts/agent/verify.sh" "$plugin/KeepUpdatedAgent.app" "$commit" "$digest"
go_bin="${KEEPUPDATED_GO:-$repo_root/.local/tools/go/bin/go}"
if [ ! -x "$go_bin" ]; then go_bin="$(command -v go)"; fi
cd "$repo_root"
GOTOOLCHAIN=local "$go_bin" run ./scripts/plugin "$plugin"
