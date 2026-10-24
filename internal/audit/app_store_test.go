package audit

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAppStoreCatalogRequiresUniqueMacProduct(t *testing.T) {
	for _, tc := range []struct{ name, results, status, code string }{
		{"mac", `{"bundleId":"com.example.store","kind":"mac-software","version":"2.0","trackId":123,"trackViewUrl":"https://example.invalid/wrong"}`, "available", ""},
		{"mac-desktop", `{"bundleId":"com.example.store","kind":"software","supportedDevices":["MacDesktop-MacDesktop"],"version":"2.0"}`, "unknown", "store_product_id_unverified"},
		{"ios", `{"bundleId":"com.example.store","kind":"software","supportedDevices":["iPhone16-iPhone16"],"version":"2.0"}`, "unknown", "no_matching_mac_product"},
		{"different-bundle", `{"bundleId":"com.example.other","kind":"mac-software","version":"2.0"}`, "unknown", "no_matching_mac_product"},
		{"ambiguous-same-version", `{"bundleId":"com.example.store","kind":"mac-software","version":"2.0"},{"bundleId":"com.example.store","kind":"mac-software","version":"2.0"}`, "unknown", "ambiguous_store_results"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			s.Config.Online = true
			s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "itunes.apple.com" || r.URL.Query().Get("bundleId") != "com.example.store" || r.URL.Query().Get("entity") != "macSoftware" {
					t.Fatal("unbound lookup", r.URL)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"results":[` + tc.results + `]}`))}, nil
			})
			i := item("application", "Example", "/fixture/Example.app", "app-store")
			i.Source = "app-store"
			i.Identity = "com.example.store"
			i.Version = "1.0"
			i.lookup = i.Identity
			s.add(i)
			s.check()
			got := s.Report.Items[0]
			if tc.name == "mac" && (got.AppStore == nil || got.AppStore.ID != 123 || got.AppStore.BundleID != i.Identity) {
				t.Fatal(got)
			}
			if tc.status == "unknown" && got.AppStore != nil {
				t.Fatal("unverified product has navigation", got)
			}
			if got.Update.Status != tc.status || tc.code != "" && got.Update.Code != tc.code {
				t.Fatal(got.Update)
			}
		})
	}
}

func TestAppStoreNavigationAndUniversalMacVersion(t *testing.T) {
	for _, tc := range []struct {
		name, id, macVersion, secondVersion, bundle, country, resultID, assetKind string
		failure                                                                   bool
		wantVersion, wantStatus                                                   string
		wantLink                                                                  bool
	}{
		{name: "universal-current", id: "123", macVersion: "8.0", wantVersion: "8.0", wantStatus: "current", wantLink: true},
		{name: "universal-update", id: "123", macVersion: "8.1", wantVersion: "8.1", wantStatus: "available", wantLink: true},
		{name: "conflicting-offers", id: "123", macVersion: "8.1", secondVersion: "9.0", wantStatus: "unknown"},
		{name: "mismatched-bundle", id: "123", macVersion: "8.1", bundle: "com.example.other", wantStatus: "unknown"},
		{name: "mismatched-region", id: "123", macVersion: "8.1", country: "gb", wantStatus: "unknown"},
		{name: "wrong-result-id", id: "123", macVersion: "8.1", resultID: "999", wantStatus: "unknown"},
		{name: "ios-offer-only", id: "123", macVersion: "8.1", assetKind: "iosSoftware", wantStatus: "unknown"},
		{name: "missing-version", id: "123", wantStatus: "unknown"},
		{name: "lookup-failed", id: "123", failure: true, wantStatus: "error"},
		{name: "negative-id", id: "-1", wantStatus: "unknown"},
		{name: "zero-id", id: "0", wantStatus: "unknown"},
		{name: "fraction-id", id: "1.5", wantStatus: "unknown"},
		{name: "string-id", id: `"123"`, wantStatus: "unknown"},
		{name: "overflow-id", id: "9223372036854775808", wantStatus: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			s.Config.Online = true
			s.Config.Country = "AU"
			s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
				body := `{"results":[{"bundleId":"com.example.store","kind":"software","trackId":` + tc.id + `,"supportedDevices":["MacDesktop-MacDesktop"],"version":"13.0"}]}`
				status := 200
				if r.URL.Host == "uclient-api.itunes.apple.com" {
					if r.URL.Query().Get("platform") != "macappstore" || r.URL.Query().Get("id") != "123" || r.URL.Query().Get("cc") != "au" {
						t.Fatal(r.URL)
					}
					bundle := tc.bundle
					if bundle == "" {
						bundle = "com.example.store"
					}
					country := tc.country
					if country == "" {
						country = "au"
					}
					resultID := tc.resultID
					if resultID == "" {
						resultID = "123"
					}
					assetKind := tc.assetKind
					if assetKind == "" {
						assetKind = "macSoftware"
					}
					offer := `{"assets":[{"flavor":"` + assetKind + `"}],"version":{"display":"` + tc.macVersion + `"}}`
					if tc.secondVersion != "" {
						offer += `,{"assets":[{"flavor":"macSoftware"}],"version":{"display":"` + tc.secondVersion + `"}}`
					}
					body = `{"meta":{"storefront":{"cc":"` + country + `"}},"results":{"123":{"id":"` + resultID + `","bundleId":"` + bundle + `","kind":"iosSoftware","deviceFamilies":["mac","iphone"],"offers":[` + offer + `]}}}`
					if tc.failure {
						status = 503
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			i := item("application", "Example", "/fixture/Example.app", "app-store")
			i.Source = "app-store"
			i.Identity = "com.example.store"
			i.Version = "8.0"
			i.lookup = i.Identity
			s.checkItem(&i)
			if i.Latest != tc.wantVersion || i.Update.Status != tc.wantStatus || (i.AppStore != nil) != tc.wantLink {
				t.Fatalf("%+v", i)
			}
			if i.AppStore != nil && (i.AppStore.ID != 123 || i.AppStore.Country != "au" || i.AppStore.BundleID != i.Identity) {
				t.Fatal(i.AppStore)
			}
		})
	}
}
