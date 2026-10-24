#!/bin/bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output_dir="$repo_root/dist/product-v0.1.2"
allow_dirty=false
while [ "$#" -gt 0 ]; do
    case "$1" in
        --allow-dirty) allow_dirty=true; shift ;;
        --out) [ "$#" -ge 2 ] || exit 2; output_dir="$2"; shift 2 ;;
        *) echo 'Usage: scripts/agent/build.sh [--allow-dirty] [--out NEW_DIRECTORY]' >&2; exit 2 ;;
    esac
done
cd "$repo_root"
version=0.1.2
archive="KeepUpdatedAgent_${version}_darwin_arm64.zip"
checksums="KeepUpdatedAgent_${version}_SHA256SUMS"
for path in "$output_dir/$archive" "$output_dir/$checksums"; do
    if [ -e "$path" ] || [ -L "$path" ]; then echo 'Output exists; choose a new --out directory.' >&2; exit 1; fi
done
commit="$(git rev-parse HEAD)"
source_status="$(git status --porcelain --untracked-files=normal)"
dirty=false
if [ -n "$source_status" ]; then dirty=true; fi
if [ "$dirty" = true ] && [ "$allow_dirty" != true ]; then echo 'Commit reviewed source, or use --allow-dirty for a local candidate.' >&2; exit 1; fi
source_digest() {
    git ls-files --cached --others --exclude-standard -z | {
        while IFS= read -r -d '' path; do
            mode="$(stat -f '%p' "./$path")" || exit 1
            if [ -L "$path" ]; then
                kind=symlink
                target="$(readlink -n "./$path" && printf .)" || exit 1
            elif [ -f "$path" ]; then kind=file; target=
            else echo "Unsupported source entry: $path" >&2; exit 1; fi
            printf '%s\0%s\0%s\0%s\0' "$path" "$kind" "$mode" "$target"
            shasum -a 256 -- "$path" || exit 1
        done
    } | shasum -a 256 | cut -d ' ' -f 1
}
digest="$(source_digest)"
go_bin="${KEEPUPDATED_GO:-$repo_root/.local/tools/go/bin/go}"
if [ ! -x "$go_bin" ]; then go_bin="$(command -v go || true)"; fi
[ -n "$go_bin" ] && [ -x "$go_bin" ] || { echo 'Go compiler not found; set KEEPUPDATED_GO.' >&2; exit 1; }
sdk_path="$(xcrun --sdk macosx --show-sdk-path)"
sdk_version="$(xcrun --sdk macosx --show-sdk-version)"
[[ "$sdk_version" =~ ^[0-9]+([.][0-9]+){0,2}$ ]] && [ "${sdk_version%%.*}" -ge 26 ] || { echo 'macOS SDK 26 or later required.' >&2; exit 1; }
scratch="$(mktemp -d /private/tmp/keepupdated-agent.XXXXXX)"
trap 'rm -rf "$scratch"' EXIT
package="$scratch/package"
app="$package/KeepUpdatedAgent.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
release_args=(--version "$version" --out "$scratch/cli")
if [ "$allow_dirty" = true ]; then release_args+=(--allow-dirty); fi
GOTOOLCHAIN=local "$go_bin" run ./scripts/release "${release_args[@]}"
tar -xzf "$scratch/cli/keepupdated_${version}_darwin_arm64.tar.gz" -C "$app/Contents/Resources"
# Build the companion only; never package or register a helper.
swift build --package-path "$repo_root/privileged" --scratch-path "$scratch/swift" -c release --product KeepUpdatedAuthorization --arch arm64 --sdk "$sdk_path" \
    -Xlinker -platform_version -Xlinker macos -Xlinker 13.0 -Xlinker "$sdk_version" \
    -Xswiftc -debug-prefix-map -Xswiftc "$repo_root=." \
    -Xswiftc -file-prefix-map -Xswiftc "$repo_root=."
products="$(swift build --package-path "$repo_root/privileged" --scratch-path "$scratch/swift" -c release --arch arm64 --sdk "$sdk_path" --show-bin-path)"
cp "$products/KeepUpdatedAuthorization" "$app/Contents/MacOS/KeepUpdatedAuthorization"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>KeepUpdatedAgent</string>
<key>CFBundleIdentifier</key><string>com.keepupdated.agent</string>
<key>CFBundleExecutable</key><string>KeepUpdatedAuthorization</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$version</string>
<key>CFBundleVersion</key><string>10</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>LSUIElement</key><true/>
<key>CFBundleDevelopmentRegion</key><string>en</string>
<key>CFBundleLocalizations</key><array><string>en</string><string>zh-Hans</string></array>
</dict></plist>
PLIST
metadata="$app/Contents/Resources/AGENT_BUILDINFO.json"
plutil -create xml1 "$metadata"
plutil -insert version -string "$version" "$metadata"
plutil -insert source_commit -string "$commit" "$metadata"
plutil -insert source_tree_sha256 -string "$digest" "$metadata"
plutil -insert working_tree_dirty -bool "$dirty" "$metadata"
plutil -insert target -string darwin/arm64 "$metadata"
plutil -insert minimum_macos -string 13.0 "$metadata"
plutil -insert sdk_version -string "$sdk_version" "$metadata"
plutil -insert signing -string 'ad-hoc; no Developer ID; not notarized' "$metadata"
plutil -insert cli_sha256 -string "$(shasum -a 256 "$app/Contents/Resources/keepupdated" | cut -d ' ' -f 1)" "$metadata"
plutil -convert json "$metadata"
cat > "$app/Contents/Resources/MCP_LAUNCH.json" <<'JSON'
{"schema":"keepupdated/mcp-launch/v1","relative_to":"this_file","command":"keepupdated","args":["mcp"]}
JSON
# Ad-hoc identity is suitable only for local review, without administrator rights.
xattr -cr "$app"
codesign --force --sign - --timestamp=none --identifier com.keepupdated.cli "$app/Contents/Resources/keepupdated"
# Bind the signed CLI; the main companion signature also seals these Resources.
plutil -replace cli_sha256 -string "$(shasum -a 256 "$app/Contents/Resources/keepupdated" | cut -d ' ' -f 1)" "$metadata"
codesign --force --sign - --timestamp=none --identifier com.keepupdated.agent "$app"
# The main executable seals Resources. Record its final signed bytes beside the
# app to avoid circular resource hashes or signature-removal byte roundtrips.
payload="$package/KeepUpdatedAgent_PAYLOAD.json"
plutil -create xml1 "$payload"
plutil -insert schema -string keepupdated/agent-payload/v1 "$payload"
plutil -insert version -string "$version" "$payload"
plutil -insert source_commit -string "$commit" "$payload"
plutil -insert source_tree_sha256 -string "$digest" "$payload"
plutil -insert working_tree_dirty -bool "$dirty" "$payload"
plutil -insert target -string darwin/arm64 "$payload"
plutil -insert cli_sha256 -string "$(shasum -a 256 "$app/Contents/Resources/keepupdated" | cut -d ' ' -f 1)" "$payload"
plutil -insert companion_sha256 -string "$(shasum -a 256 "$app/Contents/MacOS/KeepUpdatedAuthorization" | cut -d ' ' -f 1)" "$payload"
plutil -convert json "$payload"
check_source() {
    [ "$commit" = "$(git rev-parse HEAD)" ] && [ "$source_status" = "$(git status --porcelain --untracked-files=normal)" ] && [ "$digest" = "$(source_digest)" ] || { echo 'Source changed during build; rebuild stable source.' >&2; exit 1; }
}
check_source
KEEPUPDATED_GO="$go_bin" bash scripts/agent/verify.sh "$app" "$commit" "$digest"
mkdir -p "$output_dir"
ditto -c -k --norsrc --noextattr --noacl "$package" "$output_dir/$archive"
mkdir "$scratch/extracted"
ditto -x -k "$output_dir/$archive" "$scratch/extracted"
KEEPUPDATED_GO="$go_bin" bash scripts/agent/verify.sh "$scratch/extracted/KeepUpdatedAgent.app" "$commit" "$digest"
check_source
(cd "$output_dir" && shasum -a 256 "$archive" > "$checksums")
printf 'Agent candidate: %s/%s\n' "$output_dir" "$archive"
