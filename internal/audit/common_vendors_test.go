package audit

import (
	"net/http"
	"strings"
	"testing"
)

func commonTestItem(t *testing.T, s *Scanner, id, version string) Item {
	t.Helper()
	i := item("application", "Fixture", "fixture.app", "standalone")
	i.Identity, i.Version = id, version
	if id == "us.zoom.xos" {
		if m := zoomDisplayVersion.FindStringSubmatch(version); m != nil {
			i.Build = m[1] + "." + m[2]
		}
	}
	i.Architecture = []string{"arm64"}
	s.Report.Architecture, s.osVersion = "arm64", "27.0"
	if !s.commonVendorSource(&i) {
		t.Fatal("source missing", id)
	}
	return i
}

func TestCommonVendorPublicChannelAndIdentity(t *testing.T) {
	s := fixture(t)
	for _, tc := range []struct{ id, version string }{
		{"com.1password.1password", "8.12.40"}, {"us.zoom.xos", "7.2.2.88465"},
		{"com.raycast.macos", "2.8.2.0"}, {"com.getdropbox.dropbox", "275.4.3565"},
		{"io.tailscale.ipn.macsys", "1.104.1"},
	} {
		i := commonTestItem(t, s, tc.id, tc.version)
		if i.Channel != "public-stable" || !strings.Contains(strings.Join(i.Evidence, ","), "configured-update-channel:not-inspected") || i.lookup == "" {
			t.Fatal(i)
		}
	}
	for _, tc := range []struct{ id, version string }{
		{"com.1password.1password", "8.12.42-34"}, {"com.raycast.macos", "2.9.0-beta"},
		{"com.getdropbox.dropbox", "276.3.5225"}, {"io.tailscale.ipn.macsys", "1.105.0"},
	} {
		i := commonTestItem(t, s, tc.id, tc.version)
		if i.lookup != "" || i.Update.Code != "vendor_channel_unresolved" {
			t.Fatal(i)
		}
	}
	for _, id := range []string{"io.tailscale.ipn.macos", "com.raycast.unrelated", "notion.id"} {
		i := item("application", "Raycast", "fixture", "standalone")
		i.Identity = id
		if s.commonVendorSource(&i) || i.Source != "unknown" {
			t.Fatal(i)
		}
	}
}

func TestCommonVendorOnePasswordPayloadIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, status string }{
		{`{"version":"8.12.40","sources":[{"url":"https://cache.agilebits.com/dist/1P/mac8/1Password-8.12.40-aarch64.zip"}]}`, "available"},
		{`{"version":"8.12.40","sources":[{"url":"https://cache.agilebits.com/dist/1P/mac8/1Password-8.12.40-x86_64.zip"}]}`, "unknown"},
		{`{"version":"8.12.40","sources":[{"url":"https://evil.example/dist/1P/mac8/1Password-8.12.40-aarch64.zip"}]}`, "unknown"},
		{`{"version":"8.12.42-34","sources":[]}`, "unknown"},
	} {
		s := fixture(t)
		s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.URL.Host != "app-updates.agilebits.com" || !strings.Contains(r.URL.Path, "/aarch64/OPM8/") {
				t.Fatal(r)
			}
			return response(200, tc.raw), nil
		})
		i := commonTestItem(t, s, "com.1password.1password", "8.12.38")
		if !s.checkCommonVendor(&i) || i.Update.Status != tc.status {
			t.Fatal(i)
		}
	}
}

func TestCommonVendorHeadDoesNotDownload(t *testing.T) {
	for _, tc := range []struct{ id, installed, location, status string }{
		{"us.zoom.xos", "6.2.0 (40111)", "https://cdn.zoom.us/prod/7.2.2.88465/arm64/zoomusInstallerFull.pkg", "available"},
		{"us.zoom.xos", "6.2.0 (40111)", "https://cdn.zoom.us/prod/7.2.2.88465/zoomusInstallerFull.pkg", "unknown"},
		{"us.zoom.xos", "6.2.0 (40111)", "https://evil.example/prod/7.2.2.88465/arm64/zoomusInstallerFull.pkg", "unknown"},
		{"com.getdropbox.dropbox", "273.4.1", "https://edge.dropboxstatic.com/dbx-releng/client/Dropbox%20274.4.4905.arm64.dmg", "available"},
		{"com.getdropbox.dropbox", "275.4.3565", "https://edge.dropboxstatic.com/dbx-releng/client/Dropbox%20274.4.4905.arm64.dmg", "unknown"},
		{"com.getdropbox.dropbox", "273.4.1", "https://edge.dropboxstatic.com/dbx-releng/client/Dropbox%20274.4.4905.dmg", "unknown"},
	} {
		s := fixture(t)
		calls := 0
		s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "HEAD" || r.URL.Host != "www.zoom.us" && r.URL.Host != "www.dropbox.com" {
				t.Fatal("payload requested", r)
			}
			res := response(302, "")
			res.Header.Set("Location", tc.location)
			return res, nil
		})
		i := commonTestItem(t, s, tc.id, tc.installed)
		s.checkCommonVendor(&i)
		if calls != 1 || i.Update.Status != tc.status {
			t.Fatal(tc, calls, i)
		}
	}
}

func TestCommonVendorZoomNormalizationIsIdentityScoped(t *testing.T) {
	i := Item{Identity: "us.zoom.xos", Version: "6.2.0 (40111)", Build: "6.2.0.40111"}
	if commonInstalledVersion(&i) != "6.2.0.40111" {
		t.Fatal(i)
	}
	i.Build = "6.2.0.99"
	if commonInstalledVersion(&i) != "" {
		t.Fatal("inconsistent build accepted")
	}
	i.Version, i.Build = "6.2.0", "6.2.0.40111"
	if commonInstalledVersion(&i) != i.Build {
		t.Fatal("numeric short version lost its build")
	}
	i.Build = ""
	if commonInstalledVersion(&i) != "" {
		t.Fatal("missing Zoom build accepted")
	}
	i.Identity = "unrelated"
	if commonInstalledVersion(&i) != i.Version {
		t.Fatal("global normalization")
	}
}

func TestCommonVendorRaycastPlatformAndMinimum(t *testing.T) {
	for _, tc := range []struct{ platform, arch, os, status string }{
		{"macos", "arm64", "27.0", "available"}, {"windows", "arm64", "27.0", "unknown"},
		{"macos", "x64", "27.0", "unknown"}, {"macos", "arm64", "15.0", "unknown"},
	} {
		s := fixture(t)
		i := commonTestItem(t, s, "com.raycast.macos", "2.8.1.0")
		s.osVersion = tc.os
		s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Query().Get("platform") != "macos" || r.URL.Query().Get("architecture") != "arm64" {
				t.Fatal(r.URL)
			}
			return response(200, `{"version":"2.8.2.0","commit_sha":"d6aec411b00b484749742a8a14214f0041f356da","builds":[{"platform":"`+tc.platform+`","architecture":"`+tc.arch+`","format":"dmg","url":"https://x-r2.raycast-releases.com/Raycast_2.8.2.0_d6aec411b0_arm64.dmg"}]}`), nil
		})
		s.checkCommonVendor(&i)
		if i.Update.Status != tc.status {
			t.Fatal(tc, i)
		}
	}
}

func TestCommonVendorTailscaleUsesBuildAndPreservesChannel(t *testing.T) {
	s := fixture(t)
	i := commonTestItem(t, s, "io.tailscale.ipn.macsys", "1.102.0")
	i.Build = "101.102.0"
	s.feedClient.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://pkgs.tailscale.com/stable/appcast.xml" {
			t.Fatal(r.URL)
		}
		return response(200, `<rss xmlns:s="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel><item><s:version>101.104.1</s:version><s:shortVersionString>1.104.1</s:shortVersionString><s:minimumSystemVersion>13.5</s:minimumSystemVersion><enclosure/></item></channel></rss>`), nil
	})
	s.checkSparkle(&i)
	if i.Update.Status != "available" || i.Latest != "1.104.1" || i.LatestBuild != "101.104.1" || i.Channel != "public-stable" {
		t.Fatal(i)
	}
}
