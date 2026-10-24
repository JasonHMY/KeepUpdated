#!/bin/bash
# Keep numeric Mach-O versions comparable across Xcode's x.y and x.y.0 output.
verify_equal() {
    local label="$1" actual="$2" expected="$3"
    if [ "$actual" != "$expected" ]; then
        printf 'Agent verification failed: %s (expected %s; got %s).\n' "$label" "$expected" "$actual" >&2
        return 1
    fi
}
verify_version() {
    local label="$1" actual="$2" expected="$3" actual_key expected_key
    if ! [[ "$actual" =~ ^[0-9]+([.][0-9]+){0,2}$ ]] || ! [[ "$expected" =~ ^[0-9]+([.][0-9]+){0,2}$ ]]; then
        printf 'Agent verification failed: %s has an invalid numeric version (expected %s; got %s).\n' "$label" "$expected" "$actual" >&2
        return 1
    fi
    actual_key="$(printf '%s' "$actual" | awk -F. '{printf "%d.%d.%d", $1, $2, $3}')"
    expected_key="$(printf '%s' "$expected" | awk -F. '{printf "%d.%d.%d", $1, $2, $3}')"
    verify_equal "$label" "$actual_key" "$expected_key"
}

verify_payload() {
    local manifest="$1" app="$2" metadata="$3" commit="$4" digest="$5" key dirty
    if [ ! -f "$manifest" ] || [ -L "$manifest" ] || [ "$(stat -f %z "$manifest")" -gt 65536 ]; then
        echo 'Agent verification failed: missing or unsafe signed payload manifest.' >&2
        return 1
    fi
    verify_equal "payload schema" "$(plutil -extract schema raw -o - "$manifest")" keepupdated/agent-payload/v1 || return 1
    verify_equal "payload source commit" "$(plutil -extract source_commit raw -o - "$manifest")" "$commit" || return 1
    verify_equal "payload source digest" "$(plutil -extract source_tree_sha256 raw -o - "$manifest")" "$digest" || return 1
    verify_equal "payload version" "$(plutil -extract version raw -o - "$manifest")" 0.1.2 || return 1
    verify_equal "payload target" "$(plutil -extract target raw -o - "$manifest")" darwin/arm64 || return 1
    dirty="$(plutil -extract working_tree_dirty raw -o - "$manifest")" || return 1
    case "$dirty" in true|false) ;; *) echo 'Agent verification failed: invalid payload source state.' >&2; return 1 ;; esac
    for key in version source_commit source_tree_sha256 working_tree_dirty target cli_sha256; do
        verify_equal "payload $key binding" "$(plutil -extract "$key" raw -o - "$manifest")" "$(plutil -extract "$key" raw -o - "$metadata")" || return 1
    done
    verify_equal "signed CLI SHA-256" "$(shasum -a 256 "$app/Contents/Resources/keepupdated" | cut -d ' ' -f 1)" "$(plutil -extract cli_sha256 raw -o - "$manifest")" || return 1
    verify_equal "signed companion SHA-256" "$(shasum -a 256 "$app/Contents/MacOS/KeepUpdatedAuthorization" | cut -d ' ' -f 1)" "$(plutil -extract companion_sha256 raw -o - "$manifest")" || return 1
}
