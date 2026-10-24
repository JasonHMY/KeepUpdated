package audit

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func electronFixture(t *testing.T, identity, config string) (*Scanner, Item) {
	t.Helper()
	s := fixture(t)
	s.Report.Architecture = "arm64"
	i := item("application", "fixture", filepath.Join(s.Config.Home, "Applications/Fixture.app"), "standalone")
	i.Identity, i.Version, i.Architecture = identity, "1.0.0", []string{"arm64"}
	write(t, filepath.Join(i.Path, "Contents/Resources/app-update.yml"), config)
	return s, i
}

func TestElectronKnownProductsOnlyQueryChannelMetadata(t *testing.T) {
	for identity, product := range electronProducts {
		t.Run(identity, func(t *testing.T) {
			config := "provider: generic\nurl: '" + strings.TrimRight(product.base, "/") + "'\nchannel: " + product.channel + "\nupdaterCacheDirName: fixture\nuseMultipleRangeRequest: false\n"
			s, i := electronFixture(t, identity, config)
			s.electronSource(&i)
			wantURL := product.base + product.channel + "-mac.yml"
			if i.Source != "electron-generic" || i.lookup != wantURL {
				t.Fatal(i)
			}
			calls := 0
			s.feedClient.Transport = transport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.String() != wantURL {
					t.Fatal("unexpected payload or feed request", r.Method, r.URL)
				}
				return response(200, "version: 2.0.0\nfiles:\n  - url: versions/2.0.0/Fixture-2.0.0-mac-arm64.zip?token=SECRET\n    sha512: fixture\n    size: 100\nreleaseDate: '2026-10-18T00:00:00Z'\nreleaseNotes: |-\n  Version: ignored\n  - text\nbuild: \"2001001\"\n"), nil
			})
			s.checkElectron(&i)
			if calls != 1 || i.Update.Status != "available" || i.Latest != "2.0.0" {
				t.Fatal(calls, i)
			}
			b, _ := json.Marshal(i)
			if strings.Contains(string(b), "SECRET") {
				t.Fatal("download URL escaped into report")
			}
		})
	}
}

func TestElectronSourceRejectsUnverifiedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, identity, config, owner, source string
		arch                                  []string
	}{
		{"wrong-identity", "other.app", "provider: generic\nurl: https://desktop-release.notion-static.com\nchannel: arm64\n", "standalone", "unknown", []string{"arm64"}},
		{"component", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nchannel: arm64\n", "app-component", "unknown", []string{"arm64"}},
		{"app-store", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nchannel: arm64\n", "app-store", "app-store", []string{"arm64"}},
		{"private", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nprivate: true\nchannel: arm64\n", "standalone", "unknown", []string{"arm64"}},
		{"draft", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nreleaseType: draft\nchannel: arm64\n", "standalone", "unknown", []string{"arm64"}},
		{"headers", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nrequestHeaders:\n  Authorization: secret\nchannel: arm64\n", "standalone", "unknown", []string{"arm64"}},
		{"duplicate", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nchannel: arm64\nchannel: beta\n", "standalone", "unknown", []string{"arm64"}},
		{"channel", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nchannel: beta\n", "standalone", "unknown", []string{"arm64"}},
		{"wrong-product-feed", "notion.id", "provider: generic\nurl: https://example.org\nchannel: arm64\n", "standalone", "unknown", []string{"arm64"}},
		{"credentials", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com?token=secret\nchannel: arm64\n", "standalone", "unknown", []string{"arm64"}},
		{"insecure", "notion.id", "provider: generic\nurl: http://desktop-release.notion-static.com\nchannel: arm64\n", "standalone", "unknown", []string{"arm64"}},
		{"architecture", "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nchannel: arm64\n", "standalone", "unknown", []string{"x86_64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, i := electronFixture(t, tc.identity, tc.config)
			i.Owner, i.Source, i.Architecture = tc.owner, tc.source, tc.arch
			s.electronSource(&i)
			if i.lookup != "" || i.Source != tc.source {
				t.Fatal("unverified source accepted", i)
			}
		})
	}
}

func TestElectronManifestNeverAssumesConstraintsOrArchitecture(t *testing.T) {
	for _, tc := range []struct{ name, body, installed, status, code string }{
		{"current", "version: 1.0.0\nfiles:\n  - url: Fixture-arm64.zip\n    sha512: fixture\n", "1.0.0", "current", "exact_source_version_match"},
		{"intel", "version: 2.0.0\nfiles:\n  - url: Fixture-x64.zip\n", "1.0.0", "unknown", "electron_architecture_unresolved"},
		{"unmarked", "version: 2.0.0\nfiles:\n  - url: Fixture.zip\n", "1.0.0", "unknown", "electron_architecture_unresolved"},
		{"conflicting-arch", "version: 2.0.0\nfiles:\n  - url: Fixture-arm64-x64.zip\n", "1.0.0", "unknown", "electron_architecture_unresolved"},
		{"dmg-only", "version: 2.0.0\nfiles:\n  - url: Fixture-arm64.dmg\n", "1.0.0", "unknown", "electron_architecture_unresolved"},
		{"staging", "version: 2.0.0\nfiles:\n  - url: Fixture-arm64.zip\nstagingPercentage: 50\n", "1.0.0", "unknown", "electron_constraints_unresolved"},
		{"minimum-kernel", "version: 2.0.0\nfiles:\n  - url: Fixture-arm64.zip\nminimumSystemVersion: 25.0.0\n", "1.0.0", "unknown", "electron_constraints_unresolved"},
		{"unknown-constraint", "version: 2.0.0\nfiles:\n  - url: Fixture-arm64.zip\nrequireSpecialLicense: true\n", "1.0.0", "unknown", "electron_constraints_unresolved"},
		{"duplicate-version", "version: 1.0.0\nversion: 2.0.0\nfiles:\n  - url: Fixture-arm64.zip\n", "1.0.0", "unknown", "electron_metadata_unresolved"},
		{"duplicate-url", "version: 2.0.0\nfiles:\n  - url: Fixture-arm64.zip\n    url: Fixture-x64.zip\n", "1.0.0", "unknown", "electron_metadata_unresolved"},
		{"orphan-file", "version: 2.0.0\nfiles:\n    url: Fixture-arm64.zip\n", "1.0.0", "unknown", "electron_metadata_unresolved"},
		{"no-artifact", "version: 2.0.0\nreleaseNotes: plain text\n", "1.0.0", "unknown", "electron_metadata_unresolved"},
		{"prerelease", "version: 2.0.0-beta.1\nfiles:\n  - url: Fixture-arm64.zip\n", "1.0.0", "unknown", "version_scheme_or_active_version_unresolved"},
		{"installed-prerelease", "version: 2.0.0\nfiles:\n  - url: Fixture-arm64.zip\n", "1.0.0-beta.1", "unknown", "version_scheme_or_active_version_unresolved"},
		{"newer-local", "version: 1.0.0\nfiles:\n  - url: Fixture-arm64.zip\n", "2.0.0", "unknown", "installed_newer_than_source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, i := electronFixture(t, "notion.id", "provider: generic\nurl: https://desktop-release.notion-static.com\nchannel: arm64\n")
			i.Version = tc.installed
			s.electronSource(&i)
			s.feedClient.Transport = transport(func(r *http.Request) (*http.Response, error) { return response(200, tc.body), nil })
			s.checkElectron(&i)
			if i.Update.Status != tc.status || i.Update.Code != tc.code {
				t.Fatal(i)
			}
		})
	}
}
