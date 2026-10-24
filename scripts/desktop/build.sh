#!/bin/bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output_dir="$repo_root/dist/product-v0.1.2"
allow_dirty=false
with_dmg=false
sign_identity=
signing_requested=false
sign_team=
notary_profile=
evidence_dir=
source "$repo_root/scripts/desktop/signing.sh"
source "$repo_root/scripts/desktop/dmg.sh"
while [ "$#" -gt 0 ]; do
    case "$1" in
        --sign-identity|--team-id|--notary-profile|--evidence)
            signing_requested=true
            [ "$#" -ge 2 ] || { echo "$1 requires a value" >&2; exit 2; }
            case "$1" in
                --sign-identity) sign_identity="$2" ;;
                --team-id) sign_team="$2" ;;
                --notary-profile) notary_profile="$2" ;;
                --evidence) evidence_dir="$2" ;;
            esac
            shift 2 ;;
        --dmg) with_dmg=true; shift ;;
        --allow-dirty) allow_dirty=true; shift ;;
        --out) [ "$#" -ge 2 ] || { echo "--out requires a directory" >&2; exit 2; }; output_dir="$2"; shift 2 ;;
        *) echo "Usage: scripts/desktop/build.sh [--allow-dirty] [--dmg] [--out DIRECTORY] [--sign-identity SHA1 --team-id TEAM --notary-profile PROFILE --evidence NEW_PRIVATE_DIRECTORY]" >&2; exit 2 ;;
    esac
done
cd "$repo_root"
version=0.1.2
if [ "$signing_requested" = true ]; then
    validate_signing_options
    # Evidence must be ignored and outside the published assets, and must never replace a prior run.
    case "$evidence_dir" in "$repo_root"/.local/*) ;; *) echo "--evidence must be a new absolute directory under this worktree's .local/" >&2; exit 1 ;; esac
    mkdir -p "$repo_root/.local"
    (umask 077; mkdir "$evidence_dir")
    evidence_dir="$(cd "$evidence_dir" && pwd -P)"
    case "$evidence_dir" in "$repo_root"/.local/*) ;; *) echo 'Evidence path escapes .local/.' >&2; exit 1 ;; esac
fi
commit="$(git rev-parse HEAD)"
dirty=false
source_status="$(git status --porcelain --untracked-files=normal)"
if [ -n "$source_status" ]; then dirty=true; fi
if [ "$dirty" = true ] && [ "$allow_dirty" != true ]; then
    echo "Source has local changes. Commit reviewed source, or use --allow-dirty for a local candidate." >&2
    exit 1
fi
if [ -e "$output_dir/KeepUpdated_${version}_darwin_arm64.zip" ] || [ -e "$output_dir/KeepUpdated_${version}_SHA256SUMS" ] || { [ "$with_dmg" = true ] && [ -e "$output_dir/KeepUpdated_${version}_darwin_arm64.dmg" ]; }; then
    echo "Output already exists. Use a new --out directory to preserve the earlier candidate." >&2
    exit 1
fi
go_bin="${KEEPUPDATED_GO:-$repo_root/.local/tools/go/bin/go}"
if [ ! -x "$go_bin" ]; then go_bin="$(command -v go || true)"; fi
if [ -z "$go_bin" ] || [ ! -x "$go_bin" ]; then echo "Go compiler not found; set KEEPUPDATED_GO to its absolute path." >&2; exit 1; fi
swift_sdk_path="$(xcrun --sdk macosx --show-sdk-path)"
swift_sdk_version="$(xcrun --sdk macosx --show-sdk-version)"
if ! [[ "$swift_sdk_version" =~ ^[0-9]+([.][0-9]+){0,2}$ ]] || [ "${swift_sdk_version%%.*}" -lt 26 ]; then
    echo "Desktop builds require the active Xcode macOS SDK 26 or newer." >&2
    exit 1
fi
version_key() { printf '%s' "$1" | awk -F. '{printf "%d.%d.%d", $1, $2, $3}'; }
assert_macos_sdk() {
    local build_info linked_sdk minimum_os platform
    build_info="$(xcrun vtool -show-build "$1")"
    linked_sdk="$(printf '%s\n' "$build_info" | awk '$1 == "sdk" {print $2}')"
    minimum_os="$(printf '%s\n' "$build_info" | awk '$1 == "minos" {print $2}')"
    platform="$(printf '%s\n' "$build_info" | awk '$1 == "platform" {print $2}')"
    if [ "$platform" != MACOS ] || ! [[ "$linked_sdk" =~ ^[0-9]+([.][0-9]+){0,2}$ ]] || [ "$(version_key "$linked_sdk")" != "$(version_key "$swift_sdk_version")" ] || [ "$(version_key "$minimum_os")" != 13.0.0 ]; then
        printf 'Desktop Mach-O must target macOS 13.0 and link the selected SDK %s:\n%s\n' "$swift_sdk_version" "$build_info" >&2
        exit 1
    fi
}
source_digest() {
    git ls-files --cached --others --exclude-standard -z | {
        while IFS= read -r -d '' path; do
            mode="$(stat -f '%p' "./$path")" || exit 1
            if [ -L "$path" ]; then
                kind=symlink
                target="$(readlink -n "./$path" && printf .)" || exit 1
            elif [ -f "$path" ]; then
                kind=file
                target=
            else
                echo "Unsupported source entry: $path" >&2
                exit 1
            fi
            printf '%s\0%s\0%s\0%s\0' "$path" "$kind" "$mode" "$target"
            shasum -a 256 -- "$path" || exit 1
        done
    } | shasum -a 256 | cut -d ' ' -f 1
}
digest="$(source_digest)"
scratch="$(mktemp -d /private/tmp/keepupdated-desktop.XXXXXX)"
trap 'rm -rf "$scratch"' EXIT
app="$scratch/KeepUpdated.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$repo_root/desktop/Assets/KeepUpdated.icns" "$app/Contents/Resources/KeepUpdated.icns"
release_args=(--version "$version" --out "$scratch/cli")
if [ "$allow_dirty" = true ]; then release_args+=(--allow-dirty); fi
GOTOOLCHAIN=local "$go_bin" run ./scripts/release "${release_args[@]}"
tar -xzf "$scratch/cli/keepupdated_${version}_darwin_arm64.tar.gz" -C "$app/Contents/Resources"
swift build --package-path "$repo_root/desktop" --scratch-path "$scratch/swift" -c release --arch arm64 --sdk "$swift_sdk_path" \
    -Xlinker -platform_version -Xlinker macos -Xlinker 13.0 -Xlinker "$swift_sdk_version" \
    -Xswiftc -debug-prefix-map -Xswiftc "$repo_root=." \
    -Xswiftc -file-prefix-map -Xswiftc "$repo_root=."
swift_products="$(swift build --package-path "$repo_root/desktop" --scratch-path "$scratch/swift" -c release --arch arm64 --sdk "$swift_sdk_path" --show-bin-path)"
assert_macos_sdk "$swift_products/KeepUpdatedDesktop"
cp "$swift_products/KeepUpdatedDesktop" "$app/Contents/MacOS/KeepUpdatedDesktop"
strip -S "$app/Contents/MacOS/KeepUpdatedDesktop"
resource_bundle="KeepUpdatedDesktop_KeepUpdatedDesktop.bundle"
cp -R "$swift_products/$resource_bundle" "$app/Contents/Resources/$resource_bundle"
language_resources="$app/Contents/Resources/$resource_bundle"
if [ -d "$language_resources/Contents/Resources" ]; then language_resources="$language_resources/Contents/Resources"; fi
for language in en zh-Hans; do
    test -s "$language_resources/$language.lproj/Localizable.strings"
    plutil -lint "$language_resources/$language.lproj/Localizable.strings"
    mkdir -p "$app/Contents/Resources/$language.lproj"
    cp "$language_resources/$language.lproj/InfoPlist.strings" "$app/Contents/Resources/$language.lproj/InfoPlist.strings"
    plutil -lint "$app/Contents/Resources/$language.lproj/InfoPlist.strings"
done
# Build the native boundary without registering or running the helper.
swift build --package-path "$repo_root/privileged" --scratch-path "$scratch/privileged" -c release --arch arm64 --sdk "$swift_sdk_path" \
    -Xlinker -platform_version -Xlinker macos -Xlinker 13.0 -Xlinker "$swift_sdk_version" \
    -Xswiftc -debug-prefix-map -Xswiftc "$repo_root=." \
    -Xswiftc -file-prefix-map -Xswiftc "$repo_root=."
privileged_products="$(swift build --package-path "$repo_root/privileged" --scratch-path "$scratch/privileged" -c release --arch arm64 --sdk "$swift_sdk_path" --show-bin-path)"
mkdir -p "$app/Contents/Library/HelperTools" "$app/Contents/Library/LaunchDaemons"
cp "$privileged_products/KeepUpdatedAuthorization" "$app/Contents/MacOS/KeepUpdatedAuthorization"
cp "$privileged_products/KeepUpdatedHelper" "$app/Contents/Library/HelperTools/KeepUpdatedHelper"
cp "$repo_root/privileged/Resources/com.keepupdated.privileged.plist" "$app/Contents/Library/LaunchDaemons/"
assert_macos_sdk "$app/Contents/MacOS/KeepUpdatedAuthorization"
assert_macos_sdk "$app/Contents/Library/HelperTools/KeepUpdatedHelper"
# Prepare pinned upstream bytes without installing or running the App Store manager.
GOTOOLCHAIN=local "$go_bin" run ./scripts/prepare-mas --arch arm64 --out "$scratch/mas-prepared" > "$scratch/mas-source.json"
mkdir -p "$app/Contents/Library/Tools" "$app/Contents/Resources/ThirdParty/mas"
cp "$scratch/mas-prepared/mas" "$app/Contents/Library/Tools/mas"
cp -R "$scratch/mas-prepared/licenses" "$app/Contents/Resources/ThirdParty/mas/"
cp "$scratch/mas-prepared/SOURCE.json" "$app/Contents/Resources/ThirdParty/mas/"
cat > "$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>KeepUpdated</string>
<key>CFBundleDisplayName</key><string>KeepUpdated</string>
<key>CFBundleIdentifier</key><string>com.keepupdated.desktop</string>
<key>CFBundleExecutable</key><string>KeepUpdatedDesktop</string>
<key>CFBundleIconFile</key><string>KeepUpdated.icns</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$version</string>
<key>CFBundleVersion</key><string>10</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>LSApplicationCategoryType</key><string>public.app-category.utilities</string>
<key>NSHighResolutionCapable</key><true/>
<key>CFBundleDevelopmentRegion</key><string>en</string>
<key>CFBundleLocalizations</key><array><string>en</string><string>zh-Hans</string></array>
<key>NSAppleEventsUsageDescription</key><string>Show the macOS restart confirmation only after you approve a separate restart operation.</string>
</dict></plist>
EOF
metadata="$app/Contents/Resources/DESKTOP_BUILDINFO.json"
plutil -create xml1 "$metadata"
plutil -insert version -string "$version" "$metadata"
plutil -insert source_commit -string "$commit" "$metadata"
plutil -insert source_tree_sha256 -string "$digest" "$metadata"
plutil -insert working_tree_dirty -bool "$dirty" "$metadata"
plutil -insert target -string "darwin/arm64" "$metadata"
plutil -insert minimum_macos -string "13.0" "$metadata"
plutil -insert sdk_version -string "$swift_sdk_version" "$metadata"
signing_description="ad-hoc; no Developer ID; not notarized"
if [ -n "$sign_identity" ]; then signing_description="Developer ID; notarization required; verify stapled ticket and external evidence"; fi
plutil -insert signing -string "$signing_description" "$metadata"
plutil -insert build_date -string "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$metadata"
plutil -convert json "$metadata"
if [ "$commit" != "$(git rev-parse HEAD)" ] || [ "$source_status" != "$(git status --porcelain --untracked-files=normal)" ] || [ "$digest" != "$(source_digest)" ]; then
    echo "Source changed during the build. Rebuild once the source is stable." >&2
    exit 1
fi
# Remove Finder metadata only from this generated staging bundle before signing.
xattr -cr "$app"
sign_desktop "$app" "${sign_identity:--}" "$repo_root/privileged/Resources/authorization.entitlements.plist"
if [ -n "$sign_identity" ]; then
    verify_distribution_app "$app" "$sign_team" pending > "$evidence_dir/signatures.log" 2>&1
    notarize_desktop "$app" "$notary_profile" "$evidence_dir"
fi
mkdir -p "$output_dir"
archive="KeepUpdated_${version}_darwin_arm64.zip"
ditto -c -k --keepParent --norsrc --noextattr --noacl "$app" "$output_dir/$archive"
mkdir -p "$scratch/verify"
ditto -x -k "$output_dir/$archive" "$scratch/verify"
verified_app="$scratch/verify/KeepUpdated.app"
codesign --verify --deep --strict "$verified_app"
test "$(plutil -extract CFBundleIconFile raw -o - "$verified_app/Contents/Info.plist")" = KeepUpdated.icns
cmp "$repo_root/desktop/Assets/KeepUpdated.icns" "$verified_app/Contents/Resources/KeepUpdated.icns"
assert_macos_sdk "$verified_app/Contents/MacOS/KeepUpdatedDesktop"
test -f "$verified_app/Contents/Library/LaunchDaemons/com.keepupdated.privileged.plist"
codesign --verify --strict "$verified_app/Contents/MacOS/KeepUpdatedAuthorization"
codesign --verify --strict "$verified_app/Contents/Library/HelperTools/KeepUpdatedHelper"
codesign --verify --strict -R '=identifier "com.keepupdated.mas"' "$verified_app/Contents/Library/Tools/mas"
cmp "$app/Contents/Library/Tools/mas" "$verified_app/Contents/Library/Tools/mas"
cmp "$scratch/mas-prepared/SOURCE.json" "$verified_app/Contents/Resources/ThirdParty/mas/SOURCE.json"
diff -rq "$scratch/mas-prepared/licenses" "$verified_app/Contents/Resources/ThirdParty/mas/licenses"
codesign -d --entitlements - --xml "$verified_app/Contents/MacOS/KeepUpdatedAuthorization" > "$scratch/authorization-entitlements.plist" 2>/dev/null
test "$(plutil -extract 'com\.apple\.security\.automation\.apple-events' raw -o - "$scratch/authorization-entitlements.plist")" = true
if [ -z "$sign_identity" ]; then
    privilege_status="$("$verified_app/Contents/MacOS/KeepUpdatedAuthorization" status)"
    if [ "$(printf '%s' "$privilege_status" | plutil -extract status raw -o - -- -)" != developer_signature_required ]; then
        echo "Ad-hoc candidate must not enable privileged execution." >&2
        exit 1
    fi
fi
if [ -n "$sign_identity" ]; then
    verify_distribution_app "$verified_app" "$sign_team" > "$evidence_dir/final-verification.log" 2>&1
    cp "$verified_app/Contents/Resources/DESKTOP_BUILDINFO.json" "$evidence_dir/desktop-buildinfo.json"
fi
metadata_json="$("$verified_app/Contents/Resources/keepupdated" version --format json)"
metadata_field() { printf '%s' "$metadata_json" | plutil -extract "$1" raw -o - -- -; }
expected_commit="$commit"
if [ "$dirty" = true ]; then expected_commit+="+dirty"; fi
if [ "$(metadata_field version)" != "$version" ] || [ "$(metadata_field commit)" != "$expected_commit" ] || [ "$(metadata_field os)" != darwin ] || [ "$(metadata_field arch)" != arm64 ] || [ "$(metadata_field schema)" != keepupdated/v1 ] || [ "$(metadata_field plan_schema)" != keepupdated/plan/v1 ] || [ "$(metadata_field apply_schema)" != keepupdated/apply/v1 ]; then
    echo "Archived CLI metadata does not match the source or platform." >&2
    exit 1
fi
env -u KEEPUPDATED_BIN bash "$verified_app/Contents/Resources/skills/keepupdated/scripts/run.sh" --help > /dev/null
GOTOOLCHAIN=local "$go_bin" run ./scripts/release --verify-package "$verified_app/Contents/Resources"
assets=("$archive")
if [ "$with_dmg" = true ]; then
    image="KeepUpdated_${version}_darwin_arm64.dmg"
    create_desktop_dmg "$verified_app" "$output_dir/$image"
    if [ -n "$sign_identity" ]; then
        codesign --force --sign "$sign_identity" --timestamp --identifier com.keepupdated.disk-image "$output_dir/$image"
        mkdir -m 700 "$evidence_dir/dmg"
        cp "$output_dir/$image" "$evidence_dir/dmg/submitted.dmg"
        submit_notarization "$evidence_dir/dmg/submitted.dmg" "$notary_profile" "$evidence_dir/dmg"
        xcrun stapler staple "$output_dir/$image" > "$evidence_dir/dmg/staple.log" 2>&1
        verify_distribution_dmg "$output_dir/$image" "$sign_team" > "$evidence_dir/dmg/verification.log" 2>&1
        verify_dmg_contents "$output_dir/$image" "$verified_app" "$sign_team" >> "$evidence_dir/dmg/verification.log" 2>&1
    else
        verify_dmg_contents "$output_dir/$image" "$verified_app"
    fi
    assets+=("$image")
fi
if [ "$commit" != "$(git rev-parse HEAD)" ] || [ "$source_status" != "$(git status --porcelain --untracked-files=normal)" ] || [ "$digest" != "$(source_digest)" ]; then
    echo "Source changed during signing or verification; no final checksum will be delivered." >&2
    exit 1
fi
(cd "$output_dir" && shasum -a 256 "${assets[@]}" > "KeepUpdated_${version}_SHA256SUMS")
if [ -n "$sign_identity" ]; then
    cp "$output_dir/KeepUpdated_${version}_SHA256SUMS" "$evidence_dir/final-SHA256SUMS"
fi
printf 'Desktop archive: %s/%s\nVerified CLI: %s (%s)\n' "$output_dir" "$archive" "$version" "$expected_commit"
