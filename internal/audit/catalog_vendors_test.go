package audit

import (
	"net/http"
	"strings"
	"testing"
)

func TestCatalogJetBrainsDownloadMatchesProductAndArchitecture(t *testing.T) {
	for _, tc := range []struct {
		name, link string
		valid      bool
	}{
		{"correct", "https://download.jetbrains.com/idea/idea-2026.2.3-aarch64.dmg", true},
		{"different-product", "https://download.jetbrains.com/python/pycharm-2026.2.3-aarch64.dmg", false},
		{"intel-in-arm-slot", "https://download.jetbrains.com/idea/idea-2026.2.3.dmg", false},
		{"different-version", "https://download.jetbrains.com/idea/idea-2026.2.30-aarch64.dmg", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			s.Report.Architecture = "arm64"
			s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "data.services.jetbrains.com" || r.URL.Query().Get("code") != "IIU" || r.URL.Query().Get("type") != "release" {
					t.Fatal(r.URL)
				}
				return response(200, `{"IIU":[{"version":"2026.2.3","type":"release","build":"262.10968.63","downloads":{"macM1":{"link":"`+tc.link+`"}}}]}`), nil
			})
			i := Item{Source: "vendor:jetbrains", Identity: "com.jetbrains.intellij", Version: "2026.1", Build: "IU-261.10000.1", Architecture: []string{"arm64"}, lookup: "IIU"}
			s.checkCatalogVendor(&i)
			if tc.valid && i.Update.Status != "available" || !tc.valid && i.Update.Status != "unknown" {
				t.Fatal(i)
			}
		})
	}
}

func TestCatalogMosEnclosureChannelDoesNotBecomeStable(t *testing.T) {
	s := fixture(t)
	s.osVersion, s.Report.Architecture = "27.0", "arm64"
	s.feedClient.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://mos.caldis.me/appcast.xml" {
			t.Fatal(r.URL)
		}
		return response(200, `<rss xmlns:s="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel>
<item><s:version>20260531.1</s:version><s:shortVersionString>4.2.1</s:shortVersionString><enclosure/></item>
<item><s:version>20260820.1</s:version><s:shortVersionString>4.3.0</s:shortVersionString><enclosure s:channel="beta"/></item>
</channel></rss>`), nil
	})
	i := Item{Owner: "standalone", Source: "unknown", Identity: "com.caldis.Mos", Version: "4.0.0", Build: "20260220.1", Architecture: []string{"arm64"}}
	s.catalogVendorSource(&i)
	s.checkSparkle(&i)
	if i.Update.Status != "available" || i.Latest != "4.2.1" {
		t.Fatal("beta enclosure selected as public stable", i)
	}
}

func TestCatalogJetBrainsEqualMarketingVersionUsesBuild(t *testing.T) {
	for _, tc := range []struct{ build, status string }{
		{"IU-262.10968.62", "available"},
		{"IU-262.10968.63", "current"},
		{"IU-262.10968.99", "unknown"},
		{"PY-262.10968.62", "unknown"},
		{"262.10968.62", "unknown"},
		{"", "unknown"},
	} {
		s := fixture(t)
		s.Report.Architecture = "arm64"
		s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
			return response(200, `{"IIU":[{"version":"2026.2.3","type":"release","build":"262.10968.63","downloads":{"macM1":{"link":"https://download.jetbrains.com/idea/idea-2026.2.3-aarch64.dmg"}}}]}`), nil
		})
		i := Item{Source: "vendor:jetbrains", Identity: "com.jetbrains.intellij", Version: "2026.2.3", Build: tc.build, Architecture: []string{"arm64"}, lookup: "IIU"}
		s.checkCatalogVendor(&i)
		if i.Update.Status != tc.status {
			t.Fatal(tc, i)
		}
	}
}

func TestCatalogJetBrainsNonNumericReleaseVersionRemainsUnknown(t *testing.T) {
	s := fixture(t)
	s.Report.Architecture = "arm64"
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"IIU":[{"version":"2026.3-EAP","type":"release","build":"263.10000.1","downloads":{"macM1":{"link":"https://download.jetbrains.com/idea/idea-2026.3-EAP-aarch64.dmg"}}}]}`), nil
	})
	i := Item{Source: "vendor:jetbrains", Identity: "com.jetbrains.intellij", Version: "2026.2.3", Build: "IU-262.10968.63", Architecture: []string{"arm64"}, lookup: "IIU"}
	s.checkCatalogVendor(&i)
	if i.Update.Status != "unknown" {
		t.Fatal(i)
	}
}

func TestCatalogGitHubWrongPlatformAndPrereleaseFailClosed(t *testing.T) {
	for _, raw := range []string{
		`{"tag_name":"v4.0.15","prerelease":true,"assets":[{"name":"CC-Switch-v4.0.15-macOS.dmg","browser_download_url":"https://github.com/farion1231/cc-switch/releases/download/v4.0.15/CC-Switch-v4.0.15-macOS.dmg"}]}`,
		`{"tag_name":"v4.0.15","assets":[{"name":"CC-Switch-v4.0.15-Windows-arm64.msi","browser_download_url":"https://github.com/farion1231/cc-switch/releases/download/v4.0.15/CC-Switch-v4.0.15-Windows-arm64.msi"}]}`,
		`{"tag_name":"v4.0.15","assets":[{"name":"CC-Switch-v4.0.15-macOS.dmg","browser_download_url":"https://github.com/other/repo/releases/download/v4.0.15/CC-Switch-v4.0.15-macOS.dmg"}]}`,
	} {
		s := fixture(t)
		s.Report.Architecture = "arm64"
		s.client.Transport = transport(func(r *http.Request) (*http.Response, error) { return response(200, raw), nil })
		i := Item{Source: "vendor:cc-switch", Version: "4.0.0", Architecture: []string{"arm64"}}
		s.checkCatalogVendor(&i)
		if i.Update.Status != "unknown" {
			t.Fatal(i)
		}
	}
}

func TestCatalogOpenBotExactMacArchitecture(t *testing.T) {
	for _, arch := range []string{"arm64", "x64"} {
		s := fixture(t)
		s.Report.Architecture = "arm64"
		s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/repos/nightly-labs/openbot/releases/latest" {
				t.Fatal(r.URL)
			}
			return response(200, `{"tag_name":"v0.43.0","assets":[{"name":"OpenBot-0.43.0-`+arch+`.dmg","browser_download_url":"https://github.com/nightly-labs/openbot/releases/download/v0.43.0/OpenBot-0.43.0-`+arch+`.dmg"}]}`), nil
		})
		i := Item{Source: "vendor:openbot", Version: "0.42.0", Architecture: []string{"arm64"}}
		s.checkCatalogVendor(&i)
		if (i.Update.Status == "available") != (arch == "arm64") {
			t.Fatal(i)
		}
	}
}

func TestCatalogXmindUsesMetadataHEADOnly(t *testing.T) {
	for _, target := range []string{"https://dl3.xmind.app/Xmind-for-macOS-26.05.01107-202609082317.dmg", "https://dl3.xmind.app/Xmind-for-Windows-26.05.01107-202609082317.exe"} {
		s := fixture(t)
		s.Report.Architecture = "arm64"
		calls := 0
		s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "HEAD" || r.URL.String() != "https://xmind.app/zen/download/mac/" {
				t.Fatal("payload or wrong source requested", r.Method, r.URL)
			}
			rsp := response(302, "")
			rsp.Header = http.Header{"Location": []string{target}}
			return rsp, nil
		})
		i := Item{Source: "vendor:xmind", Version: "24.01.14362", Architecture: []string{"arm64"}}
		s.checkCatalogVendor(&i)
		if calls != 1 || (i.Update.Status == "available") != strings.Contains(target, "macOS") {
			t.Fatal(calls, i)
		}
	}
}
