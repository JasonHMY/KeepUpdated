package desktop

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Native tools are controlled only in these child shells. No identity, Keychain,
// Apple endpoint, installed helper or host update is used by these fixtures.
func runSigningFixture(t *testing.T, body string, wantOK bool) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("uses macOS plutil")
	}
	lib, err := filepath.Abs("signing.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", "-c", `set -euo pipefail
source "$LIBRARY"
source "$(dirname "$LIBRARY")/dmg.sh"
cd "$FIXTURE"
mkdir app evidence
app="$PWD/app"
evidence="$PWD/evidence"
team=ABCDE12345
sign_identity=0123456789012345678901234567890123456789
sign_team="$team"
notary_profile=fixture
evidence_dir="$evidence"
allow_dirty=false
fault=
codesign() {
    printf '%s\n' "$*" >> calls
    local target="${!#}"
    if [[ "$*" = *'--verify'* ]]; then
        [[ "$fault" != signature && "$fault" != "$target" ]] || return 1
        if [[ "$*" = *'-R'* ]]; then
            [[ "$*" = *'anchor apple generic'* && "$*" = *'subject.OU] = "ABCDE12345"'* && "$*" = *'field.1.2.840.113635.100.6.1.13'* ]] || return 1
            local id
            case "$target" in
                */KeepUpdatedAuthorization) id=com.keepupdated.authorization ;;
                */KeepUpdatedHelper) id=com.keepupdated.privileged ;;
                */mas) id=com.keepupdated.mas ;;
                */keepupdated) id=com.keepupdated.cli ;;
                *) id=com.keepupdated.desktop ;;
            esac
            [[ "$*" = *"identifier \"$id\""* ]] || return 1
        fi
    elif [[ "$*" = *'--entitlements -'* ]]; then
        if [[ "$fault" = entitlements ]]; then
            echo '<plist version="1.0"><dict><key>com.apple.security.get-task-allow</key><true/></dict></plist>'
        elif [[ "$target" = */KeepUpdatedAuthorization ]]; then
            echo '<plist version="1.0"><dict><key>com.apple.security.automation.apple-events</key><true/></dict></plist>'
        fi
    elif [[ "$*" = *'--verbose=4'* ]]; then
        [[ "$fault" = runtime ]] || echo 'CodeDirectory v=20500 size=100 flags=0x10000(runtime) hashes=1+1'
        [[ "$fault" = timestamp ]] || echo 'Timestamp=Oct 20, 2026 at 12:00:00'
    fi
    return 0
}
security() {
    [ "$fault" != identity ] || { echo '  1) 0123456789012345678901234567890123456789 "Apple Development: Fixture (ABCDE12345)"'; return 0; }
    echo '  1) 0123456789012345678901234567890123456789 "Developer ID Application: Fixture (ABCDE12345)"'
}
xcrun() {
    printf '%s\n' "$*" >> calls
    case "$1 $2" in
        'notarytool submit')
            [ "$fault" != timeout ] || { echo '{"id":"01234567-0123-0123-0123-012345678901"}'; return 1; }
            if [ "$fault" = rejected ]; then echo '{"id":"01234567-0123-0123-0123-012345678901","status":"Invalid"}'; else echo '{"id":"01234567-0123-0123-0123-012345678901","status":"Accepted"}'; fi ;;
        'notarytool log')
            local hash job=01234567-0123-0123-0123-012345678901
            hash="$(shasum -a 256 "$archive" | cut -d ' ' -f 1)"
            [ "$fault" != hash ] || hash=wrong
            [ "$fault" != job ] || job=wrong
            printf '{"status":"Accepted","issues":null,"jobId":"%s","sha256":"%s"}' "$job" "$hash" > "${!#}" ;;
        'stapler staple'|'stapler validate') [ "$fault" != ticket ] || return 1 ;;
        *) return 1 ;;
    esac
}
spctl() { echo "spctl $*" >> calls; [ "$fault" != gatekeeper ]; }
ditto() { echo 'submitted bytes' > "${!#}"; }
`+body)
	cmd.Env = append(os.Environ(), "LIBRARY="+lib, "FIXTURE="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if (err == nil) != wantOK {
		t.Fatalf("success=%v, want %v: %s\n%v", err == nil, wantOK, out, err)
	}
	return string(out)
}

func TestSigningPreconditions(t *testing.T) {
	for _, tc := range []struct{ name, setup string }{
		{"valid", ""}, {"identity", "fault=identity"}, {"team", "sign_team=OTHER12345"},
		{"dirty", "allow_dirty=true"}, {"profile", "notary_profile="},
		{"evidence", "evidence_dir="}, {"fingerprint", "sign_identity=-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runSigningFixture(t, tc.setup+"\nvalidate_signing_options", tc.name == "valid")
		})
	}
}

func TestNestedSigningOrder(t *testing.T) {
	out := runSigningFixture(t, `sign_desktop "$app" "$sign_identity" fixture.plist
cat calls`, true)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 6 {
		t.Fatalf("unexpected calls: %s", out)
	}
	for i, id := range []string{"com.keepupdated.authorization", "com.keepupdated.privileged", "com.keepupdated.mas", "com.keepupdated.cli", ""} {
		if !strings.Contains(lines[i], "--options runtime --timestamp") || strings.Contains(lines[i], "--deep") || (id != "" && !strings.Contains(lines[i], "--identifier "+id)) {
			t.Errorf("invalid signing call: %s", lines[i])
		}
		if strings.Contains(lines[i], "--entitlements") != (i == 0) {
			t.Errorf("wrong entitlement scope: %s", lines[i])
		}
	}
	out = runSigningFixture(t, `sign_desktop "$app" - fixture.plist; cat calls`, true)
	if strings.Contains(out, "--options runtime") || strings.Count(out, "--timestamp=none") != 5 {
		t.Fatalf("ad-hoc path changed: %s", out)
	}
}

func TestDistributionRefusals(t *testing.T) {
	for _, fault := range []string{"", "signature", "runtime", "timestamp", "entitlements", "ticket", "gatekeeper"} {
		t.Run("fault-"+fault, func(t *testing.T) {
			runSigningFixture(t, "fault="+fault+"\nverify_distribution_app \"$app\" \"$team\"", fault == "")
		})
	}
	for _, path := range []string{"Contents/Resources/keepupdated", "Contents/MacOS/KeepUpdatedAuthorization", "Contents/Library/HelperTools/KeepUpdatedHelper", "Contents/Library/Tools/mas"} {
		t.Run(path, func(t *testing.T) {
			runSigningFixture(t, "fault=\"$app/"+path+"\"\nverify_distribution_app \"$app\" \"$team\"", false)
		})
	}
	runSigningFixture(t, `cp /usr/bin/true "$app/unreviewed"
verify_distribution_app "$app" "$team"`, false)
}

func TestNotarizationGates(t *testing.T) {
	for _, fault := range []string{"", "rejected", "timeout", "ticket", "hash", "job"} {
		t.Run("fault-"+fault, func(t *testing.T) {
			out := runSigningFixture(t, "fault="+fault+`
if notarize_desktop "$app" fixture "$evidence"; then result=0; else result=1; fi
test -s "$evidence/submitted.zip"
test -s "$evidence/submission.json"
cat calls
exit "$result"`, fault == "")
			if (fault == "rejected" || fault == "timeout" || fault == "hash" || fault == "job") && strings.Contains(out, "stapler") {
				t.Fatal("stapled an unaccepted submission")
			}
			if fault == "" && !strings.Contains(out, "stapler validate") {
				t.Fatal("missing ticket verification")
			}
		})
	}
}

func TestEmptySigningRequestDoesNotFallBack(t *testing.T) {
	cmd := exec.Command("/bin/bash", "build.sh", "--sign-identity", "")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Developer ID Application identity") {
		t.Fatalf("empty signing request did not stop at preflight: %s, %v", out, err)
	}
}

func TestRealAdHocSignatureRejected(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires codesign")
	}
	app := filepath.Join(t.TempDir(), "Fixture.app")
	macos := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macos, "Fixture"), binary, 0700); err != nil {
		t.Fatal(err)
	}
	plist := `<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.keepupdated.desktop</string><key>CFBundleExecutable</key><string>Fixture</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--options", "runtime", "--timestamp=none", app).CombinedOutput(); err != nil {
		t.Fatalf("sign fixture: %s, %v", out, err)
	}
	cmd := exec.Command("/bin/bash", "verify-distribution.sh", app, "ABCDE12345", strings.Repeat("a", 40), "0.1.0", "8", strings.Repeat("b", 64))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "failed to satisfy specified code requirement") {
		t.Fatalf("ad-hoc signature not rejected at Developer ID requirement: %s, %v", out, err)
	}
}

func TestDMGVerificationAndDetach(t *testing.T) {
	for _, fault := range []string{"", "corrupt", "attach", "wrong-mount", "different-app", "shortcut", "detach"} {
		t.Run("fault-"+fault, func(t *testing.T) {
			out := runSigningFixture(t, `
mktemp() { /usr/bin/mktemp -d "$FIXTURE/check.XXXXXX"; }
hdiutil() {
    echo "hdiutil $*" >> "$FIXTURE/calls"
    case "$1" in
        verify) [ "$fault" != corrupt ] || return 1 ;;
        attach)
            [ "$fault" != attach ] || return 1
            local mount="$6"
            mkdir "$mount/KeepUpdated.app"
            ln -s /Applications "$mount/Applications"
            if [ "$fault" = different-app ]; then echo changed > "$mount/KeepUpdated.app/extra"; fi
            if [ "$fault" = shortcut ]; then rm "$mount/Applications"; ln -s /tmp "$mount/Applications"; fi
            if [ "$fault" = wrong-mount ]; then mount=/unexpected; fi
            printf '<plist version="1.0"><dict><key>system-entities</key><array><dict><key>mount-point</key><string>%s</string></dict></array></dict></plist>' "$mount" ;;
        detach) [ "$fault" != detach ] && [ "$fault" != attach ] || return 1 ;;
        *) return 1 ;;
    esac
}
trap 'cat "$FIXTURE/calls"' EXIT
fault=`+fault+`
verify_dmg_contents "$FIXTURE/fixture.dmg" "$app"
`, fault == "")
			if fault != "corrupt" && !strings.Contains(out, "hdiutil detach") {
				t.Fatalf("mount attempt was not followed by detach: %s", out)
			}
			if (fault == "detach" || fault == "attach") && !strings.Contains(out, "retained mount and evidence") {
				t.Fatal("uncertain mount must retain evidence")
			}
			if strings.Contains(out, "-force") {
				t.Fatal("must not force detach")
			}
		})
	}
}

func TestDMGSignatureGates(t *testing.T) {
	for _, fault := range []string{"", "signature", "timestamp", "ticket", "gatekeeper"} {
		t.Run("fault-"+fault, func(t *testing.T) {
			runSigningFixture(t, `
# Check the DMG-specific identity requirement; a disk image is not executable code.
codesign() {
    if [[ "$*" = *'--verify'* ]]; then
        [ "$fault" != signature ] || return 1
        [[ "$*" = *'identifier "com.keepupdated.disk-image"'* && "$*" = *'subject.OU] = "ABCDE12345"'* && "$*" = *'field.1.2.840.113635.100.6.1.13'* ]]
    else
        [ "$fault" = timestamp ] || echo 'Timestamp=Oct 20, 2026'
        return 0
    fi
}
fault=`+fault+`
verify_distribution_dmg "$FIXTURE/fixture.dmg" "$team"
`, fault == "")
		})
	}
}
