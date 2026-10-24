package audit

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

type countRead struct {
	reader io.Reader
	n      int
}

func (c *countRead) Read(p []byte) (int, error) { n, e := c.reader.Read(p); c.n += n; return n, e }
func (c *countRead) Close() error               { return nil }
func TestPyPIOnlyReadsProjectInfo(t *testing.T) {
	s := fixture(t)
	r := &countRead{reader: strings.NewReader(`{"info":{"name":"example","version":"2.0"},"releases":"` + strings.Repeat("x", metadataLimit+1000) + `"}`)}
	s.client.Transport = transport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: r, Header: http.Header{}}, nil
	})
	var info struct{ Name, Version string }
	if code := s.pypiInfo("example", &info); code != "" || info.Version != "2.0" {
		t.Fatal(code, info)
	}
	if r.n > 4096 {
		t.Fatal("read releases history", r.n)
	}
}
func TestPyPILimitStillAppliesToInfo(t *testing.T) {
	s := fixture(t)
	s.client.Transport = transport(func(req *http.Request) (*http.Response, error) {
		return response(200, `{"info":{"description":"`+strings.Repeat("x", metadataLimit+1)+`"}}`), nil
	})
	var info any
	if code := s.pypiInfo("example", &info); code != "output_limit" {
		t.Fatal(code)
	}
}
func TestBrewAliasDoesNotBecomeVersionUpgrade(t *testing.T) {
	s := fixture(t)
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/Aliases/"):
			return response(200, "../Formula/e/example-compat.rb"), nil
		case strings.Contains(r.URL.Path, "example-compat.json"):
			return response(200, `{"name":"example-compat","versions":{"stable":"3.0"}}`), nil
		}
		return response(404, ""), nil
	})
	i := item("formula", "example", "fixture", "homebrew")
	i.lookup = "example"
	i.Source = "homebrew/core"
	s.missingBrew(&i)
	if i.Update.Code != "upstream_alias_requires_review" || i.Update.Status != "unknown" || i.Latest != "3.0" {
		t.Fatal(i)
	}
}
func TestBrewAbsentDefinitionRequiresBothEvidence(t *testing.T) {
	for _, status := range []int{404, 500} {
		s := fixture(t)
		s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "cask_renames.json") {
				return response(200, "{}"), nil
			}
			return response(status, ""), nil
		})
		i := item("cask", "gone", "fixture", "homebrew")
		i.lookup = "gone"
		i.Source = "homebrew/cask"
		s.missingBrew(&i)
		if status == 404 && i.Update.Code != "upstream_definition_absent" || status == 500 && i.Update.Status != "error" {
			t.Fatal(status, i)
		}
	}
}
func TestPublicFeedURLAndAddressBoundary(t *testing.T) {
	for _, u := range []string{"http://example.org/a.xml", "https://user:SECRET@example.org/a.xml", "https://example.org/a.xml?token=SECRET", "https://127.0.0.1/a", "https://[::1]/a", "https://localhost/a", "https://host.local/a", "https://example.org:444/a", "file:///tmp/feed"} {
		if publicURL(u, true) {
			t.Fatal(u)
		}
	}
	for _, u := range []string{"https://example.org/appcast.xml", "https://example.org/appcast.xml?channel=production"} {
		if !publicURL(u, true) {
			t.Fatal(u)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1", "10.1.1.1", "192.168.1.1", "100.64.0.1", "169.254.169.254", "::ffff:127.0.0.1", "fc00::1", "2001:db8::1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Fatal(ip)
		}
	}
}
func TestSparkleBuildChannelArchitectureAndOS(t *testing.T) {
	raw := `<rss xmlns:s="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel>
 <item><s:version>999</s:version><s:channel>beta</s:channel></item>
 <item><s:version>888</s:version><s:minimumSystemVersion>99.0</s:minimumSystemVersion></item>
 <item><s:version>777</s:version><s:hardwareRequirements>x86_64</s:hardwareRequirements></item>
 <item><s:version>666</s:version><enclosure s:os="windows"/></item>
 <item><enclosure s:version="12" s:shortVersionString="1.2"/><s:minimumSystemVersion>13.0</s:minimumSystemVersion></item>
 <item><s:version>11</s:version><s:shortVersionString>1.1</s:shortVersionString></item>
 </channel></rss>`
	var feed appcast
	if e := xml.Unmarshal([]byte(raw), &feed); e != nil {
		t.Fatal(e)
	}
	i := item("application", "example", "fixture", "standalone")
	i.Version = "1.1"
	i.Build = "11"
	selectSparkle(&i, feed, "14.0", "arm64")
	if i.LatestBuild != "12" || i.Latest != "1.2" || i.Update.Status != "available" {
		t.Fatal(i)
	}
	i.Build = "12"
	selectSparkle(&i, feed, "14.0", "arm64")
	if i.Update.Status != "current" {
		t.Fatal(i)
	}
}
func TestSparkleUnknownConstraintCannotReportCurrent(t *testing.T) {
	i := item("application", "example", "fixture", "standalone")
	i.Build = "1"
	feed := appcast{Items: []castItem{{Version: "1"}, {Version: "2", Hardware: "future-cpu"}}}
	selectSparkle(&i, feed, "14.0", "arm64")
	if i.Update.Status != "unknown" {
		t.Fatal(i)
	}
}
func TestSparkleDoesNotFetchEnclosuresOrExposeFeedSecrets(t *testing.T) {
	s := fixture(t)
	calls := 0
	s.feedClient.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(200, `<rss xmlns:s="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel><item><s:version>2</s:version><enclosure url="https://example.org/package.zip?token=SECRET"/></item></channel></rss>`), nil
	})
	i := item("application", "example", "fixture", "standalone")
	i.Build = "1"
	i.lookup = "https://example.org/appcast.xml"
	s.checkSparkle(&i)
	if calls != 1 || i.Update.Status != "available" {
		t.Fatal(calls, i)
	}
	b, _ := json.Marshal(i)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal("secret escaped")
	}
	i.lookup = "https://example.org/appcast.xml?token=SECRET"
	s.checkSparkle(&i)
	if calls != 1 || i.Update.Status != "unknown" {
		t.Fatal(calls, i)
	}
}
func TestVendorExactIdentityAndChannel(t *testing.T) {
	s := fixture(t)
	i := item("application", "Google Chrome", "fixture", "standalone")
	i.Identity = "example.other"
	s.vendorSource(&i)
	if i.Source != "unknown" {
		t.Fatal(i)
	}
	i.Identity = "com.google.Chrome"
	s.vendorSource(&i)
	if i.Source != "vendor:chrome" {
		t.Fatal(i)
	}
	i.Identity = "com.microsoft.VSCode"
	i.Source = "unknown"
	s.vendorSource(&i)
	if i.Update.Code != "vendor_channel_unresolved" {
		t.Fatal(i)
	}
}
func TestVendorArchitectureAndPayloadUncertainty(t *testing.T) {
	s := fixture(t)
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"latestVersion":"2.0","minimumVersion":"1.0"}`), nil
	})
	i := item("application", "example", "fixture", "standalone")
	i.Source = "vendor:vscode"
	s.checkVendor(&i)
	if i.Update.Code != "vendor_architecture_unresolved" {
		t.Fatal(i)
	}
	i.Source = "vendor:obsidian"
	i.Version = "1.0"
	i.payloadVersions = []string{"2.0"}
	s.checkVendor(&i)
	if i.Latest != "2.0" || i.Update.Code != "application_payload_activity_unknown" {
		t.Fatal(i)
	}
}

func TestVendorResponseIdentityAndPlatform(t *testing.T) {
	s := fixture(t)
	s.Report.Architecture = "arm64"
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("order_by") != "version desc" || !strings.Contains(r.URL.Path, "mac_arm64/channels/stable") {
			t.Error("wrong platform/channel/order", r.URL)
		}
		return response(200, `{"versions":[{"name":"chrome/platforms/mac_arm64/channels/stable/versions/2.0.0.0","version":"2.0.0.0"}]}`), nil
	})
	i := item("application", "Chrome", "fixture", "standalone")
	i.Source = "vendor:chrome"
	i.Architecture = []string{"arm64", "x86_64"}
	i.Version = "1.0.0.0"
	s.checkVendor(&i)
	if i.Update.Status != "available" {
		t.Fatal(i)
	}
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"versions":[{"name":"chrome/platforms/win/channels/beta/versions/9.0","version":"9.0"}]}`), nil
	})
	s.checkVendor(&i)
	if i.Update.Code != "identity_mismatch" {
		t.Fatal(i)
	}
}
func TestFeedRedirectSafety(t *testing.T) {
	client := publicClient(0)
	for _, u := range []string{"http://example.org/feed", "https://127.0.0.1/feed", "https://user:secret@example.org/feed"} {
		r, _ := http.NewRequest("GET", u, nil)
		if client.CheckRedirect(r, nil) == nil {
			t.Fatal(u)
		}
	}
	r, _ := http.NewRequest("GET", "https://release-assets.githubusercontent.com/feed?signature=synthetic", nil)
	if client.CheckRedirect(r, make([]*http.Request, 6)) == nil {
		t.Fatal("redirect limit missing")
	}
	if client.CheckRedirect(r, make([]*http.Request, 1)) != nil {
		t.Fatal("safe signed redirect rejected")
	}
}
func TestOfflineCheckDoesNotRequestFeedsOrVendors(t *testing.T) {
	s := fixture(t)
	tr := transport(func(r *http.Request) (*http.Response, error) { t.Fatal("unexpected network", r.URL); return nil, nil })
	s.client.Transport = tr
	s.feedClient.Transport = tr
	for _, source := range []string{"sparkle", "vendor:chrome"} {
		i := item("application", "fixture", source, "standalone")
		i.Source = source
		i.lookup = "https://example.org/feed"
		s.add(i)
	}
	s.check()
	for _, i := range s.Report.Items {
		if i.Update.Code != "online_query_required" {
			t.Fatal(i)
		}
	}
}

func TestConflictingSparkleFieldsNeverCurrent(t *testing.T) {
	for _, body := range []string{`<s:version>1</s:version><s:version>2</s:version>`, `<s:version>2</s:version><enclosure s:version="3"/>`, `<enclosure s:version="1" s:version="2"/>`} {
		var feed appcast
		raw := `<rss xmlns:s="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel><item>` + body + `</item></channel></rss>`
		if err := xml.Unmarshal([]byte(raw), &feed); err != nil {
			continue
		}
		i := item("application", "example", "fixture", "standalone")
		i.Build = "2"
		selectSparkle(&i, feed, "14.0", "arm64")
		if i.Update.Status != "unknown" || i.Update.Code != "appcast_conflicting_metadata" {
			t.Fatal(i)
		}
	}
}
