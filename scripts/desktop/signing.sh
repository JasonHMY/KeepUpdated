#!/bin/bash
# Shared by the builder and independent release verifier. No credentials are loaded here.

distribution_error() { echo "$*" >&2; return 1; }

validate_signing_options() {
    [[ "$sign_identity" =~ ^[A-Fa-f0-9]{40}$ ]] || { distribution_error 'Use the SHA-1 fingerprint of a Developer ID Application identity.'; return 1; }
    [[ "$sign_team" =~ ^[A-Z0-9]{10}$ ]] || { distribution_error 'A ten-character signing Team ID is required.'; return 1; }
    [ -n "$notary_profile" ] && [ -n "$evidence_dir" ] || { distribution_error 'Signing requires --notary-profile and a new --evidence directory.'; return 1; }
    [ "$allow_dirty" = false ] || { distribution_error 'Developer ID distribution does not allow --allow-dirty.'; return 1; }
    security find-identity -v -p codesigning | awk -v fingerprint="$sign_identity" -v team="$sign_team" '
        toupper($2) == toupper(fingerprint) && index($0, "\"Developer ID Application: ") && index($0, "(" team ")\"") { found=1 }
        END { exit !found }' || { distribution_error 'No matching valid local Developer ID Application identity and Team.'; return 1; }
}

sign_desktop() {
    local app="$1" identity="$2" entitlements="$3"
    local options=(--force --sign "$identity" --timestamp=none)
    if [ "$identity" != - ]; then options=(--force --sign "$identity" --options runtime --timestamp); fi
    codesign "${options[@]}" --identifier com.keepupdated.authorization --entitlements "$entitlements" "$app/Contents/MacOS/KeepUpdatedAuthorization" || return 1
    codesign "${options[@]}" --identifier com.keepupdated.privileged "$app/Contents/Library/HelperTools/KeepUpdatedHelper" || return 1
    codesign "${options[@]}" --identifier com.keepupdated.mas "$app/Contents/Library/Tools/mas" || return 1
    codesign "${options[@]}" --identifier com.keepupdated.cli "$app/Contents/Resources/keepupdated" || return 1
    codesign "${options[@]}" "$app" || return 1
    codesign --verify --deep --strict "$app"
}

verify_distribution_signature() {
    local path="$1" identifier="$2" team="$3" details entitlements
    local requirement="anchor apple generic and identifier \"$identifier\" and certificate leaf[subject.OU] = \"$team\" and certificate leaf[field.1.2.840.113635.100.6.1.13] exists"
    codesign --verify --strict -R "=$requirement" "$path" || return 1
    details="$(codesign -d --verbose=4 "$path" 2>&1)" || return 1
    printf '%s\n' "$details" | grep -Eq '^CodeDirectory .*flags=.*\(.*runtime.*\)' || { distribution_error 'Hardened Runtime is missing.'; return 1; }
    printf '%s\n' "$details" | grep -Eq '^Timestamp=.+$' || { distribution_error 'Secure signing timestamp is missing.'; return 1; }
    entitlements="$(codesign -d --entitlements - --xml "$path" 2>/dev/null)" || return 1
    # Only the authorization bridge needs Apple Events. No runtime exceptions or debugging grants.
    if [ "$identifier" = com.keepupdated.authorization ]; then
        [ -n "$entitlements" ] || return 1
        local expected='<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>com.apple.security.automation.apple-events</key><true/></dict></plist>'
        [ "$(printf '%s' "$entitlements" | plutil -convert json -o - -- -)" = "$(printf '%s' "$expected" | plutil -convert json -o - -- -)" ] || { distribution_error 'Unexpected authorization entitlements.'; return 1; }
    elif [ -n "$entitlements" ]; then
        [ "$(printf '%s' "$entitlements" | plutil -convert json -o - -- -)" = '{}' ] || { distribution_error 'Unexpected executable entitlements.'; return 1; }
    fi
    printf 'Verified Developer ID signature, runtime, timestamp and entitlements: %s\n' "$identifier"
}

verify_distribution_app() {
    local app="$1" team="$2" ticket="${3:-required}" file relative
    [[ "$team" =~ ^[A-Z0-9]{10}$ ]] || return 1
    case "$ticket" in required|pending) ;; *) return 1 ;; esac
    codesign --verify --deep --strict "$app" || return 1
    verify_distribution_signature "$app" com.keepupdated.desktop "$team" || return 1
    verify_distribution_signature "$app/Contents/Resources/keepupdated" com.keepupdated.cli "$team" || return 1
    verify_distribution_signature "$app/Contents/MacOS/KeepUpdatedAuthorization" com.keepupdated.authorization "$team" || return 1
    verify_distribution_signature "$app/Contents/Library/HelperTools/KeepUpdatedHelper" com.keepupdated.privileged "$team" || return 1
    verify_distribution_signature "$app/Contents/Library/Tools/mas" com.keepupdated.mas "$team" || return 1
    # Fail when a new Mach-O is added without an explicit signing/entitlement decision.
    while IFS= read -r -d '' file; do
        if /usr/bin/file -b "$file" | grep -q 'Mach-O'; then
            relative="${file#"$app"/}"
            case "$relative" in
                Contents/MacOS/KeepUpdatedDesktop|Contents/Resources/keepupdated|Contents/MacOS/KeepUpdatedAuthorization|Contents/Library/HelperTools/KeepUpdatedHelper|Contents/Library/Tools/mas) ;;
                *) distribution_error "Unreviewed Mach-O payload: $relative"; return 1 ;;
            esac
        fi
    done < <(find "$app" -type f -print0)
    if [ "$ticket" = required ]; then
        xcrun stapler validate "$app" || return 1
        spctl --assess --type execute --verbose=4 "$app" || return 1
    fi
}

notarize_desktop() {
    local app="$1" profile="$2" evidence="$3"
    # Retain the submitted bytes and Apple responses privately even if submission fails or times out.
    ditto -c -k --keepParent --norsrc --noextattr --noacl "$app" "$evidence/submitted.zip" || return 1
    submit_notarization "$evidence/submitted.zip" "$profile" "$evidence" || return 1
    xcrun stapler staple "$app" > "$evidence/staple.log" 2>&1 || return 1
    xcrun stapler validate "$app" >> "$evidence/staple.log" 2>&1
}

submit_notarization() {
    local archive="$1" profile="$2" evidence="$3" submission_id status
    xcrun notarytool submit "$archive" --keychain-profile "$profile" --wait --timeout 20m --output-format json > "$evidence/submission.json" 2> "$evidence/submission.stderr" || {
        distribution_error 'Notary submission failed or timed out; inspect private evidence before retrying.'; return 1;
    }
    submission_id="$(plutil -extract id raw -o - "$evidence/submission.json")" || return 1
    [[ "$submission_id" =~ ^[A-Fa-f0-9-]{36}$ ]] || return 1
    xcrun notarytool log "$submission_id" --keychain-profile "$profile" "$evidence/notary-log.json" > "$evidence/log.stdout" 2> "$evidence/log.stderr" || return 1
    status="$(plutil -extract status raw -o - "$evidence/submission.json")" || return 1
    [ "$status" = Accepted ] && [ "$(plutil -extract status raw -o - "$evidence/notary-log.json")" = Accepted ] || {
        distribution_error 'Apple did not accept this submission; inspect the private notary log.'; return 1;
    }
    [ "$(plutil -extract jobId raw -o - "$evidence/notary-log.json")" = "$submission_id" ] &&
    [ "$(plutil -extract sha256 raw -o - "$evidence/notary-log.json")" = "$(shasum -a 256 "$archive" | cut -d ' ' -f 1)" ] || {
        distribution_error 'Notary log does not match the submitted archive and job.'; return 1;
    }
}
