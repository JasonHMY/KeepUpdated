#!/bin/bash
# The disk image contains the same app as the self-update ZIP and an Applications shortcut.

create_desktop_dmg() (
    set -euo pipefail
    app="$1"; image="$2"
    [ ! -e "$image" ] || { echo 'DMG already exists; use a new output directory.' >&2; exit 1; }
    scratch="$(mktemp -d /private/tmp/keepupdated-dmg-build.XXXXXX)"
    trap 'rm -rf "$scratch"' EXIT
    ditto "$app" "$scratch/KeepUpdated.app"
    ln -s /Applications "$scratch/Applications"
    hdiutil create -fs HFS+ -format UDZO -volname KeepUpdated -srcfolder "$scratch" "$image"
)

verify_distribution_dmg() {
    local image="$1" team="$2" details
    [[ "$team" =~ ^[A-Z0-9]{10}$ ]] || return 1
    codesign --verify --strict -R "=anchor apple generic and identifier \"com.keepupdated.disk-image\" and certificate leaf[subject.OU] = \"$team\" and certificate leaf[field.1.2.840.113635.100.6.1.13] exists" "$image" || return 1
    details="$(codesign -d --verbose=4 "$image" 2>&1)" || return 1
    printf '%s\n' "$details" | grep -Eq '^Timestamp=.+$' || return 1
    xcrun stapler validate "$image" || return 1
    spctl --assess --type open --context context:primary-signature --verbose=4 "$image"
}

verify_dmg_contents() (
    set -euo pipefail
    image="$1"; expected_app="$2"; team="${3:-}"
    attempted=false; index=0; mounts=0
    hdiutil verify "$image"
    scratch="$(mktemp -d /private/tmp/keepupdated-dmg-check.XXXXXX)"
    mountpoint="$scratch/mount"
    mkdir "$mountpoint"
    cleanup() {
        local result=$?
        if [ "$attempted" = true ] && ! hdiutil detach "$mountpoint" >> "$scratch/detach.log" 2>&1; then
            echo "DMG detach failed; retained mount and evidence at $scratch. Inspect before cleanup." >&2
            exit 1
        fi
        rm -rf "$scratch"
        exit "$result"
    }
    trap cleanup EXIT
    attempted=true
    hdiutil attach -readonly -nobrowse -noautoopen -mountpoint "$mountpoint" -plist "$image" > "$scratch/attach.plist"
    while /usr/libexec/PlistBuddy -c "Print :system-entities:$index" "$scratch/attach.plist" > /dev/null 2>&1; do
        actual="$(/usr/libexec/PlistBuddy -c "Print :system-entities:$index:mount-point" "$scratch/attach.plist" 2>/dev/null || true)"
        if [ -n "$actual" ]; then
            [ "$actual" = "$mountpoint" ] || { echo 'Unexpected DMG mount location.' >&2; exit 1; }
            mounts=$((mounts + 1))
        fi
        index=$((index + 1))
    done
    [ "$mounts" = 1 ] || { echo 'Expected one mounted DMG volume.' >&2; exit 1; }
    [ -L "$mountpoint/Applications" ] && [ "$(readlink "$mountpoint/Applications")" = /Applications ]
    [ -d "$mountpoint/KeepUpdated.app" ] && [ ! -L "$mountpoint/KeepUpdated.app" ]
    diff -rq "$expected_app" "$mountpoint/KeepUpdated.app"
    codesign --verify --deep --strict "$mountpoint/KeepUpdated.app"
    if [ -n "$team" ]; then verify_distribution_app "$mountpoint/KeepUpdated.app" "$team"; fi
)

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -euo pipefail
    [ "$#" = 3 ] || { echo 'Usage: dmg.sh IMAGE VERIFIED_EXTRACTED_APP TEAM' >&2; exit 2; }
    source "$(dirname "${BASH_SOURCE[0]}")/signing.sh"
    verify_distribution_app "$2" "$3"
    verify_distribution_dmg "$1" "$3"
    verify_dmg_contents "$1" "$2" "$3"
    echo 'Signed DMG and app comparison passed; browser download and Finder installation still require device acceptance.'
fi
